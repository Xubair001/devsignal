// Package greenhouse adapts the Greenhouse public job-board API.
//
// Tier A: the endpoint is documented, unauthenticated, and intended for
// third-party consumption, which is why this is the source family to build
// first. No account is created, nothing is authenticated, and no terms are
// accepted — that single rule is what separates our posture from hiQ's.
package greenhouse

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/Xubair001/devsignal/internal/source"
)

// Work modes we are willing to assert from a location string. Anything not
// unambiguously stated stays empty — normalization owns geography.
const (
	workRemote = "remote"
	workHybrid = "hybrid"
)

const (
	atsType  = "greenhouse"
	endpoint = "https://boards-api.greenhouse.io/v1/boards/%s/jobs?content=true"
)

func init() {
	source.Register(atsType, func(o source.Options) (source.Adapter, error) {
		token := strings.TrimSpace(o.Config["board_token"])
		if token == "" {
			return nil, fmt.Errorf("greenhouse: board_token is required")
		}
		if o.Client == nil {
			return nil, fmt.Errorf("greenhouse: client is required")
		}
		return &Adapter{token: token, client: o.Client}, nil
	})
}

type Adapter struct {
	token  string
	client *source.Client
}

func New(token string, c *source.Client) *Adapter { return &Adapter{token: token, client: c} }

func (a *Adapter) ID() string        { return atsType + ":" + a.token }
func (a *Adapter) Tier() source.Tier { return source.TierA }

// Fetch returns the whole board as a single document.
//
// A bulk JSON endpoint means fetch and parse happen together, so there is no
// separate per-job detail request — which is exactly why a 5-minute poll is
// affordable here and would not be for an HTML career page.
func (a *Adapter) Fetch(ctx context.Context, cur source.Cursor) ([]source.RawDocument, source.Cursor, error) {
	url := fmt.Sprintf(endpoint, a.token)

	resp, err := a.client.GetConditional(ctx, url, cur)
	if err != nil {
		// Not-modified is a successful poll: it proves the board is reachable and
		// unchanged, so it counts for liveness. Propagate it as-is.
		return nil, cur, err
	}

	next := source.Cursor{ETag: resp.ETag, LastModified: resp.LastModified}
	doc := source.RawDocument{
		SourceJobID: "board:" + a.token, // the document IS the board
		Body:        resp.Body,
		ContentType: resp.ContentType,
		FetchedAt:   time.Now().UTC(),
		URL:         url,
	}
	return []source.RawDocument{doc}, next, nil
}

// wire mirrors only the fields we actually use. Unknown fields are ignored on
// purpose: a source adding a key must not break ingestion.
type wire struct {
	Jobs []struct {
		ID          int64  `json:"id"`
		Title       string `json:"title"`
		CompanyName string `json:"company_name"`
		AbsoluteURL string `json:"absolute_url"`
		Content     string `json:"content"`
		Language    string `json:"language"`
		Location    *struct {
			Name string `json:"name"`
		} `json:"location"`
		FirstPublished string `json:"first_published"`
		UpdatedAt      string `json:"updated_at"`
		RequisitionID  string `json:"requisition_id"`
	} `json:"jobs"`
}

// Parse is pure: no network, no database, no clock. Re-parsing a stored
// RawDocument must be deterministic, which is what makes the golden fixture
// meaningful and lets a whole source be re-parsed after a bug fix.
func (a *Adapter) Parse(doc source.RawDocument) ([]source.ParsedPosting, error) {
	var w wire
	if err := json.Unmarshal(doc.Body, &w); err != nil {
		return nil, fmt.Errorf("greenhouse: decode: %w", err)
	}

	out := make([]source.ParsedPosting, 0, len(w.Jobs))
	for _, j := range w.Jobs {
		if j.ID == 0 || strings.TrimSpace(j.Title) == "" {
			// A row with no identity or no title is unusable. Skipping it is
			// visible as a parse-yield drop rather than a silent bad record.
			continue
		}
		atsID := strconv.FormatInt(j.ID, 10)

		// Greenhouse double-escapes the description. Unescape once here so the
		// stored HTML is real HTML; sanitising is the renderer's job.
		desc := html.UnescapeString(j.Content)

		loc := ""
		if j.Location != nil {
			loc = strings.TrimSpace(j.Location.Name)
		}

		p := source.ParsedPosting{
			SourceJobID:            atsID,
			ATSType:                atsType,
			ATSJobID:               atsID,
			Title:                  strings.TrimSpace(j.Title),
			CompanyName:            strings.TrimSpace(j.CompanyName),
			DescriptionHTML:        desc,
			ApplyURL:               strings.TrimSpace(j.AbsoluteURL),
			Language:               normLang(j.Language),
			LocationRaw:            loc,
			WorkMode:               workModeFrom(loc),
			SourceReportedPostedAt: parseTime(j.FirstPublished),
			SourceUpdatedAt:        parseTime(j.UpdatedAt),
		}
		// Hash the fields that change MEANING. Deliberately excludes timestamps:
		// a board that refreshes updated_at must not invalidate the extraction
		// cache and make us pay the model again for identical text.
		p.ContentHash = contentHash(p.Title, desc, loc, p.ApplyURL)
		out = append(out, p)
	}
	return out, nil
}

// workModeFrom reads only what the string unambiguously states. Anything else
// stays empty — normalization (step 8) owns structured geography, and a
// half-guessed location is worse than none.
func workModeFrom(loc string) string {
	l := strings.ToLower(loc)
	switch {
	case strings.Contains(l, workRemote):
		return workRemote
	case strings.Contains(l, workHybrid):
		return workHybrid
	default:
		return ""
	}
}

func normLang(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) >= 2 {
		return s[:2]
	}
	return ""
}

// parseTime accepts the formats Greenhouse actually emits and returns nil rather
// than a zero time when absent, so "unknown" stays distinguishable from "epoch".
func parseTime(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05-07:00", "2006-01-02"} {
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
		h.Write([]byte{0}) // separator so ("ab","c") != ("a","bc")
	}
	return h.Sum(nil)
}
