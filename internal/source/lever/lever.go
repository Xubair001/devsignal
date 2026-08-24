// Package lever adapts the Lever public postings API.
//
// Tier A: https://api.lever.co/v0/postings/{site} is documented, unauthenticated
// and intended for third-party consumption. No account is created, nothing is
// authenticated and no terms are accepted (hard rule 5).
//
// Same fetch shape as Greenhouse — one request returns the whole board with
// descriptions inline — which is what makes a frequent poll affordable.
package lever

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
	atsType = "lever"
	// mode=json matters: without it the endpoint serves HTML.
	endpoint = "https://api.lever.co/v0/postings/%s?mode=json"
)

func init() {
	source.Register(atsType, func(o source.Options) (source.Adapter, error) {
		site := strings.TrimSpace(o.Config["board_token"])
		if site == "" {
			return nil, fmt.Errorf("lever: board_token (the Lever site slug) is required")
		}
		if o.Client == nil {
			return nil, fmt.Errorf("lever: client is required")
		}
		return &Adapter{site: site, client: o.Client}, nil
	})
}

type Adapter struct {
	site   string
	client *source.Client
}

func New(site string, c *source.Client) *Adapter { return &Adapter{site: site, client: c} }

func (a *Adapter) ID() string        { return atsType + ":" + a.site }
func (a *Adapter) Tier() source.Tier { return source.TierA }

// Fetch returns the whole board as one document.
func (a *Adapter) Fetch(ctx context.Context, cur source.Cursor) ([]source.RawDocument, source.Cursor, error) {
	url := fmt.Sprintf(endpoint, a.site)

	resp, err := a.client.GetConditional(ctx, url, cur)
	if err != nil {
		// Not-modified is a successful poll and counts for liveness. Pass it on
		// unchanged rather than turning it into a failure.
		return nil, cur, err
	}

	next := source.Cursor{ETag: resp.ETag, LastModified: resp.LastModified}
	doc := source.RawDocument{
		SourceJobID: "board:" + a.site,
		Body:        resp.Body,
		ContentType: resp.ContentType,
		FetchedAt:   time.Now().UTC(),
		URL:         url,
	}
	return []source.RawDocument{doc}, next, nil
}

// wire mirrors only the fields used. Lever returns a top-level ARRAY, not an
// object with a jobs key — unlike every other board API here.
type wire []struct {
	ID   string `json:"id"`
	Text string `json:"text"` // the title
	// Lever states the work mode outright instead of leaving it to be read out
	// of a location string, which is the one place it is better than Greenhouse.
	WorkplaceType string `json:"workplaceType"`
	Country       string `json:"country"`
	Categories    *struct {
		Location     string   `json:"location"`
		AllLocations []string `json:"allLocations"`
		Commitment   string   `json:"commitment"`
		Department   string   `json:"department"`
		Team         string   `json:"team"`
	} `json:"categories"`
	Description string `json:"description"` // HTML
	// CreatedAt is epoch MILLISECONDS, not seconds and not a string.
	CreatedAt int64  `json:"createdAt"`
	HostedURL string `json:"hostedUrl"`
	ApplyURL  string `json:"applyUrl"`
}

// Parse is pure: no network, no database, no clock.
func (a *Adapter) Parse(doc source.RawDocument) ([]source.ParsedPosting, error) {
	var w wire
	if err := json.Unmarshal(doc.Body, &w); err != nil {
		return nil, fmt.Errorf("lever: decode: %w", err)
	}

	out := make([]source.ParsedPosting, 0, len(w))
	for _, j := range w {
		if strings.TrimSpace(j.ID) == "" || strings.TrimSpace(j.Text) == "" {
			// No identity or no title is unusable. Skipping shows up as a
			// parse-yield drop rather than a silent bad record.
			continue
		}

		loc := ""
		if j.Categories != nil {
			loc = strings.TrimSpace(j.Categories.Location)
			// A posting open in several places says so. Joining them keeps the
			// information for normalization instead of discarding all but one.
			if len(j.Categories.AllLocations) > 1 {
				loc = strings.Join(j.Categories.AllLocations, "; ")
			}
		}

		apply := strings.TrimSpace(j.ApplyURL)
		if apply == "" {
			apply = strings.TrimSpace(j.HostedURL)
		}

		p := source.ParsedPosting{
			SourceJobID: j.ID,
			ATSType:     atsType,
			ATSJobID:    j.ID,
			Title:       strings.TrimSpace(j.Text),
			// Lever does not return a company name anywhere in the payload. Left
			// empty on purpose: company resolution falls back to the board token,
			// and inventing a name from the slug would be a guess rendered as
			// fact (hard rule 3).
			CompanyName:            "",
			DescriptionHTML:        j.Description,
			ApplyURL:               apply,
			LocationRaw:            loc,
			WorkMode:               workMode(j.WorkplaceType),
			SourceReportedPostedAt: epochMillis(j.CreatedAt),
		}
		// Hash what changes MEANING. Timestamps are excluded so a board that
		// refreshes them does not invalidate the extraction cache and make us pay
		// the model again for identical text.
		p.ContentHash = contentHash(p.Title, p.DescriptionHTML, loc, apply)
		out = append(out, p)
	}
	return out, nil
}

// workMode maps Lever's stated value onto ours. Anything unrecognised stays
// empty rather than being guessed at.
func workMode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case source.WorkRemote:
		return source.WorkRemote
	case source.WorkHybrid:
		return source.WorkHybrid
	case source.WorkOnsite, "on-site":
		return source.WorkOnsite
	default:
		return ""
	}
}

// epochMillis converts Lever's millisecond timestamp. Zero means "not stated",
// which must stay distinguishable from the epoch.
func epochMillis(ms int64) *time.Time {
	if ms <= 0 {
		return nil
	}
	t := time.UnixMilli(ms).UTC()
	return &t
}

func contentHash(parts ...string) []byte {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0}) // separator so ("ab","c") != ("a","bc")
	}
	return h.Sum(nil)
}
