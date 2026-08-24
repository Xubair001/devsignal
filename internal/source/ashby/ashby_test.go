package ashby

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xubair001/devsignal/internal/source"
)

// -update rewrites the golden file. Refreshing a fixture must be a deliberate
// act: a golden diff is a review item, never something to auto-rebaseline.
var update = flag.Bool("update", false, "rewrite the golden file")

const fixture = "linear-board.json"

// golden is the full normalized output, field by field. Source payloads drift
// constantly and the failure is almost never a clean error — it is a parser that
// still returns a row with an empty field.
type golden struct {
	SourceJobID            string `json:"source_job_id"`
	ATSType                string `json:"ats_type"`
	ATSJobID               string `json:"ats_job_id"`
	Title                  string `json:"title"`
	CompanyName            string `json:"company_name"`
	ApplyURL               string `json:"apply_url"`
	LocationRaw            string `json:"location_raw"`
	WorkMode               string `json:"work_mode"`
	SourceReportedPostedAt string `json:"source_reported_posted_at"`
	DescriptionLen         int    `json:"description_len"`
	DescriptionHead        string `json:"description_head"`
	ContentHash            string `json:"content_hash"`
}

func toGolden(p source.ParsedPosting) golden {
	g := golden{
		SourceJobID: p.SourceJobID, ATSType: p.ATSType, ATSJobID: p.ATSJobID,
		Title: p.Title, CompanyName: p.CompanyName, ApplyURL: p.ApplyURL,
		LocationRaw: p.LocationRaw, WorkMode: p.WorkMode,
		DescriptionLen:  len(p.DescriptionHTML),
		DescriptionHead: firstN(p.DescriptionHTML, 120),
		ContentHash:     hex.EncodeToString(p.ContentHash),
	}
	if p.SourceReportedPostedAt != nil {
		g.SourceReportedPostedAt = p.SourceReportedPostedAt.Format("2006-01-02T15:04:05Z")
	}
	return g
}

func firstN(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func loadFixture(t *testing.T) source.RawDocument {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return source.RawDocument{
		SourceJobID: "board:linear", Body: body, ContentType: "application/json",
	}
}

func TestParseGoldenFile(t *testing.T) {
	a := New("linear", nil) // Parse is pure: no client needed
	got, err := a.Parse(loadFixture(t))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no postings parsed from a real board payload")
	}

	gs := make([]golden, 0, len(got))
	for _, p := range got {
		gs = append(gs, toGolden(p))
	}
	actual, err := json.MarshalIndent(gs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join("testdata", "linear-board.golden.json")
	if *update {
		if err := os.WriteFile(path, append(actual, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("golden file rewritten")
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden file missing (run with -update): %v", err)
	}
	if strings.TrimSpace(string(want)) != strings.TrimSpace(string(actual)) {
		t.Errorf("parsed output changed.\n--- want ---\n%s\n--- got ---\n%s", want, actual)
	}
}

// Purity is what makes re-parsing history safe.
func TestParseIsDeterministic(t *testing.T) {
	a := New("linear", nil)
	doc := loadFixture(t)
	first, err := a.Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		again, err := a.Parse(doc)
		if err != nil {
			t.Fatal(err)
		}
		if len(again) != len(first) {
			t.Fatalf("run %d: %d postings, want %d", i, len(again), len(first))
		}
		for j := range first {
			if string(again[j].ContentHash) != string(first[j].ContentHash) {
				t.Fatalf("run %d posting %d: content hash is not stable", i, j)
			}
		}
	}
}

// A board refreshing createdAt must not invalidate the extraction cache: that
// would make us pay the model again for identical text and make fit scores flap.
func TestContentHashIgnoresTimestamps(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	for _, j := range raw["jobs"].([]any) {
		j.(map[string]any)["publishedAt"] = "2030-01-01T00:00:00.000+00:00"
	}
	bumped, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}

	a := New("linear", nil)
	before, err := a.Parse(source.RawDocument{Body: body})
	if err != nil {
		t.Fatal(err)
	}
	after, err := a.Parse(source.RawDocument{Body: bumped})
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) || len(before) == 0 {
		t.Fatalf("posting count changed: %d -> %d", len(before), len(after))
	}
	for i := range before {
		if string(before[i].ContentHash) != string(after[i].ContentHash) {
			t.Errorf("posting %d: hash changed when only the timestamp moved", i)
		}
		if after[i].SourceReportedPostedAt == nil ||
			after[i].SourceReportedPostedAt.Year() != 2030 {
			t.Errorf("posting %d: source_reported_posted_at not propagated", i)
		}
	}
}

// Ashby gives both a workplace type and a boolean. The type wins because it can
// express hybrid, which the boolean cannot.
func TestWorkModePrefersWorkplaceTypeOverTheBoolean(t *testing.T) {
	a := New("x", nil)
	body := []byte(`{"jobs":[
	  {"id":"a","title":"A","isListed":true,"workplaceType":"Hybrid","isRemote":true},
	  {"id":"b","title":"B","isListed":true,"isRemote":true},
	  {"id":"c","title":"C","isListed":true,"workplaceType":"Onsite","isRemote":false},
	  {"id":"d","title":"D","isListed":true}
	]}`)
	got, err := a.Parse(source.RawDocument{Body: body})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{source.WorkHybrid, source.WorkRemote, source.WorkOnsite, ""}
	if len(got) != len(want) {
		t.Fatalf("got %d postings, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].WorkMode != w {
			t.Errorf("posting %d work mode = %q, want %q", i, got[i].WorkMode, w)
		}
	}
}

// isListed false is the employer saying they withdrew the posting from their own
// public board. Ingesting it anyway would republish something they took down.
func TestUnlistedPostingsAreSkipped(t *testing.T) {
	a := New("x", nil)
	body := []byte(`{"jobs":[
	  {"id":"a","title":"Listed","isListed":true},
	  {"id":"b","title":"Withdrawn","isListed":false}
	]}`)
	got, err := a.Parse(source.RawDocument{Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ATSJobID != "a" {
		t.Fatalf("unlisted posting was ingested: %d postings", len(got))
	}
}

// A posting open in several places must keep all of them: normalization decides
// what to do with them, and dropping all but one loses real information.
func TestSecondaryLocationsArePreserved(t *testing.T) {
	a := New("x", nil)
	body := []byte(`{"jobs":[{"id":"a","title":"Job","isListed":true,
	  "location":"Berlin","secondaryLocations":[
	    {"location":"Munich"},{"location":"Vienna"}]}]}`)
	got, err := a.Parse(source.RawDocument{Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d postings", len(got))
	}
	for _, city := range []string{"Berlin", "Munich", "Vienna"} {
		if !strings.Contains(got[0].LocationRaw, city) {
			t.Errorf("location %q lost %s", got[0].LocationRaw, city)
		}
	}
}

// Ashby returns no company name. Inventing one from the slug would be a guess
// rendered as fact, so the field must stay empty for resolution to fall back.
func TestCompanyNameIsLeftEmptyRatherThanGuessed(t *testing.T) {
	a := New("linear", nil)
	got, err := a.Parse(loadFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range got {
		if p.CompanyName != "" {
			t.Fatalf("posting %d invented a company name %q", i, p.CompanyName)
		}
	}
}

func TestParseSkipsUnusableRows(t *testing.T) {
	a := New("x", nil)
	body := []byte(`{"jobs":[
	  {"id":"","title":"Has no id","isListed":true},
	  {"id":"b","title":"   ","isListed":true},
	  {"id":"c","title":"Real Job","isListed":true,"applyUrl":"https://x/c"}
	]}`)
	got, err := a.Parse(source.RawDocument{Body: body})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d postings, want 1 (unusable rows must be dropped)", len(got))
	}
	if got[0].ATSJobID != "c" {
		t.Errorf("ats_job_id = %q, want c", got[0].ATSJobID)
	}
}

// Falling back to hostedUrl matters: a posting with no apply link is one the user
// cannot act on, which is worse than not showing it.
func TestApplyURLFallsBackToJobURL(t *testing.T) {
	a := New("x", nil)
	body := []byte(`{"jobs":[{"id":"a","title":"Job","isListed":true,
	  "jobUrl":"https://jobs.ashbyhq.com/x/a"}]}`)
	got, err := a.Parse(source.RawDocument{Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].ApplyURL != "https://jobs.ashbyhq.com/x/a" {
		t.Errorf("apply url = %q, want the job url", got[0].ApplyURL)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	a := New("x", nil)
	if _, err := a.Parse(source.RawDocument{Body: []byte("<html>nope</html>")}); err == nil {
		t.Error("parsing HTML as JSON must fail loudly, not return zero postings")
	}
}

// The registry is how a source family is enabled, and a missing slug must fail
// at construction rather than produce requests to a malformed URL.
func TestRegistrationRequiresABoardToken(t *testing.T) {
	if _, err := source.Build("ashby", source.Options{
		Config: map[string]string{}, Client: source.NewClient(source.DefaultClientConfig()),
	}); err == nil {
		t.Error("building without a board token must fail")
	}
	if _, err := source.Build("ashby", source.Options{
		Config: map[string]string{"board_token": "linear"},
	}); err == nil {
		t.Error("building without a client must fail")
	}
	a, err := source.Build("ashby", source.Options{
		Config: map[string]string{"board_token": "linear"},
		Client: source.NewClient(source.DefaultClientConfig()),
	})
	if err != nil {
		t.Fatalf("valid build failed: %v", err)
	}
	if a.ID() != "ashby:linear" {
		t.Errorf("id = %q", a.ID())
	}
	if a.Tier() != source.TierA {
		t.Errorf("tier = %q, want a", a.Tier())
	}
}
