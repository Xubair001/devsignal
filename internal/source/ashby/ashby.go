// Package ashby adapts the Ashby public job-board API.
//
// Tier A: https://api.ashbyhq.com/posting-api/job-board/{name} is documented,
// unauthenticated and intended for third-party consumption. No account is
// created, nothing is authenticated and no terms are accepted (hard rule 5).
//
// Ashby returns the largest bodies of any board family measured here — the
// openai board is 12.4 MB against Greenhouse's 143 KB for gitlab — because full
// descriptions are inline for every posting. That is why source.MaxBodyBytes is
// 32 MiB: an 8 MiB cap silently rejected the biggest employers, and a source
// that excludes them looks like a small market rather than a misconfiguration.
package ashby

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Xubair001/devsignal/internal/source"
)

const (
	atsType = "ashby"
	// includeCompensation asks for pay data where the employer published it.
	// Compensation is parsed later (money is int64 minor units, hard rule 1);
	// requesting it here means the stored RawDocument already contains it and a
	// re-parse does not need a re-fetch.
	endpoint = "https://api.ashbyhq.com/posting-api/job-board/%s?includeCompensation=true"
)

func init() {
	source.Register(atsType, func(o source.Options) (source.Adapter, error) {
		board := strings.TrimSpace(o.Config["board_token"])
		if board == "" {
			return nil, fmt.Errorf("ashby: board_token (the Ashby job-board name) is required")
		}
		if o.Client == nil {
			return nil, fmt.Errorf("ashby: client is required")
		}
		return &Adapter{board: board, client: o.Client}, nil
	})
}

type Adapter struct {
	board  string
	client *source.Client
}

func New(board string, c *source.Client) *Adapter { return &Adapter{board: board, client: c} }

func (a *Adapter) ID() string        { return atsType + ":" + a.board }
func (a *Adapter) Tier() source.Tier { return source.TierA }

// Fetch returns the whole board as one document.
func (a *Adapter) Fetch(ctx context.Context, cur source.Cursor) ([]source.RawDocument, source.Cursor, error) {
	url := fmt.Sprintf(endpoint, a.board)

	resp, err := a.client.GetConditional(ctx, url, cur)
	if err != nil {
		// Not-modified is a successful poll and counts for liveness.
		return nil, cur, err
	}

	next := source.Cursor{ETag: resp.ETag, LastModified: resp.LastModified}
	doc := source.RawDocument{
		SourceJobID: "board:" + a.board,
		Body:        resp.Body,
		ContentType: resp.ContentType,
		FetchedAt:   time.Now().UTC(),
		URL:         url,
	}
	return []source.RawDocument{doc}, next, nil
}

// wire mirrors only the fields used. Unknown keys are ignored so a source adding
// one cannot break ingestion.
type wire struct {
	Jobs []struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Location string `json:"location"`
		// SecondaryLocations carries the other places a posting is open. Ashby is
		// the only family here that separates them from the primary location.
		SecondaryLocations []struct {
			Location string `json:"location"`
		} `json:"secondaryLocations"`
		EmploymentType string `json:"employmentType"`
		// IsRemote and WorkplaceType overlap. WorkplaceType is preferred when
		// present because it distinguishes hybrid, which the boolean cannot.
		IsRemote        bool   `json:"isRemote"`
		WorkplaceType   string `json:"workplaceType"`
		PublishedAt     string `json:"publishedAt"`
		JobURL          string `json:"jobUrl"`
		ApplyURL        string `json:"applyUrl"`
		DescriptionHTML string `json:"descriptionHtml"`
		// IsListed false means the employer unlisted it from their public board.
		// Respecting that is not optional: it is their statement about their own
		// posting, and ingesting it anyway would publish something they withdrew.
		IsListed bool `json:"isListed"`
	} `json:"jobs"`
}

// Parse is pure: no network, no database, no clock.
func (a *Adapter) Parse(doc source.RawDocument) ([]source.ParsedPosting, error) {
	var w wire
	if err := json.Unmarshal(doc.Body, &w); err != nil {
		return nil, fmt.Errorf("ashby: decode: %w", err)
	}

	out := make([]source.ParsedPosting, 0, len(w.Jobs))
	for _, j := range w.Jobs {
		if strings.TrimSpace(j.ID) == "" || strings.TrimSpace(j.Title) == "" {
			continue
		}
		if !j.IsListed {
			// Unlisted by the employer. Skipped rather than stored closed, because
			// we never saw it open on this board.
			continue
		}

		loc := strings.TrimSpace(j.Location)
		if len(j.SecondaryLocations) > 0 {
			all := []string{loc}
			for _, s := range j.SecondaryLocations {
				if v := strings.TrimSpace(s.Location); v != "" {
					all = append(all, v)
				}
			}
			loc = strings.Join(all, "; ")
		}

		apply := strings.TrimSpace(j.ApplyURL)
		if apply == "" {
			apply = strings.TrimSpace(j.JobURL)
		}

		p := source.ParsedPosting{
			SourceJobID: j.ID,
			ATSType:     atsType,
			ATSJobID:    j.ID,
			Title:       strings.TrimSpace(j.Title),
			// Ashby returns no company name. Left empty deliberately: resolution
			// falls back to the board token, and deriving a display name from a
			// slug would be a guess rendered as fact (hard rule 3).
			CompanyName:            "",
			DescriptionHTML:        j.DescriptionHTML,
			ApplyURL:               apply,
			LocationRaw:            loc,
			WorkMode:               workMode(j.WorkplaceType, j.IsRemote),
			SourceReportedPostedAt: parseTime(j.PublishedAt),
		}
		p.ContentHash = contentHash(p.Title, p.DescriptionHTML, loc, apply)
		out = append(out, p)
	}
	return out, nil
}

// workMode prefers the stated workplace type, which can express hybrid, and
// falls back to the boolean, which cannot. Neither stated leaves it empty.
func workMode(workplaceType string, isRemote bool) string {
	switch strings.ToLower(strings.TrimSpace(workplaceType)) {
	case source.WorkRemote:
		return source.WorkRemote
	case source.WorkHybrid:
		return source.WorkHybrid
	case source.WorkOnsite, "on-site", "inoffice", "in-office":
		return source.WorkOnsite
	}
	if isRemote {
		return source.WorkRemote
	}
	return ""
}

// parseTime returns nil rather than a zero time when absent, so "unknown" stays
// distinguishable from "epoch".
func parseTime(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}

func contentHash(parts ...string) []byte {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return h.Sum(nil)
}
