package greenhouse

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

// golden is the full normalized output, field by field. Source payloads drift
// constantly and the failure is almost never a clean error — it is a parser that
// still returns a row with an empty field. This is the test that catches it.
type golden struct {
	SourceJobID            string `json:"source_job_id"`
	ATSType                string `json:"ats_type"`
	ATSJobID               string `json:"ats_job_id"`
	Title                  string `json:"title"`
	CompanyName            string `json:"company_name"`
	ApplyURL               string `json:"apply_url"`
	Language               string `json:"language"`
	LocationRaw            string `json:"location_raw"`
	WorkMode               string `json:"work_mode"`
	SourceReportedPostedAt string `json:"source_reported_posted_at"`
	SourceUpdatedAt        string `json:"source_updated_at"`
	DescriptionLen         int    `json:"description_len"`
	DescriptionHead        string `json:"description_head"`
	ContentHash            string `json:"content_hash"`
}

func toGolden(p source.ParsedPosting) golden {
	g := golden{
		SourceJobID: p.SourceJobID, ATSType: p.ATSType, ATSJobID: p.ATSJobID,
		Title: p.Title, CompanyName: p.CompanyName, ApplyURL: p.ApplyURL,
		Language: p.Language, LocationRaw: p.LocationRaw, WorkMode: p.WorkMode,
		DescriptionLen:  len(p.DescriptionHTML),
		DescriptionHead: firstN(p.DescriptionHTML, 120),
		ContentHash:     hex.EncodeToString(p.ContentHash),
	}
	if p.SourceReportedPostedAt != nil {
		g.SourceReportedPostedAt = p.SourceReportedPostedAt.Format("2006-01-02T15:04:05Z")
	}
	if p.SourceUpdatedAt != nil {
		g.SourceUpdatedAt = p.SourceUpdatedAt.Format("2006-01-02T15:04:05Z")
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
	body, err := os.ReadFile(filepath.Join("testdata", "gitlab-board.json"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return source.RawDocument{
		SourceJobID: "board:gitlab", Body: body, ContentType: "application/json",
	}
}

func TestParseGoldenFile(t *testing.T) {
	a := New("gitlab", nil) // Parse is pure: no client needed
	got, err := a.Parse(loadFixture(t))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	gs := make([]golden, 0, len(got))
	for _, p := range got {
		gs = append(gs, toGolden(p))
	}
	actual, err := json.MarshalIndent(gs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join("testdata", "gitlab-board.golden.json")
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

// Purity is the property that makes re-parsing history safe.
func TestParseIsDeterministic(t *testing.T) {
	a := New("gitlab", nil)
	doc := loadFixture(t)
	first, err := a.Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
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

// The content hash must track meaning, not presentation. A board refreshing its
// timestamps must NOT invalidate the extraction cache — that would make us pay
// the model again for identical text and make fit scores flap.
func TestContentHashIgnoresTimestamps(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "gitlab-board.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	jobs := raw["jobs"].([]any)
	for _, j := range jobs {
		m := j.(map[string]any)
		m["updated_at"] = "2030-01-01T00:00:00-04:00"
		m["first_published"] = "2029-01-01T00:00:00-04:00"
	}
	bumped, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}

	a := New("gitlab", nil)
	before, err := a.Parse(source.RawDocument{Body: body})
	if err != nil {
		t.Fatal(err)
	}
	after, err := a.Parse(source.RawDocument{Body: bumped})
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("posting count changed: %d -> %d", len(before), len(after))
	}
	for i := range before {
		if string(before[i].ContentHash) != string(after[i].ContentHash) {
			t.Errorf("posting %d: hash changed when only timestamps moved", i)
		}
		// ...but the reported timestamp itself must still be carried through.
		if after[i].SourceUpdatedAt == nil || after[i].SourceUpdatedAt.Year() != 2030 {
			t.Errorf("posting %d: source_updated_at not propagated", i)
		}
	}
}

func TestParseSkipsUnusableRows(t *testing.T) {
	a := New("x", nil)
	// No id, and a blank title: neither is a usable posting.
	body := []byte(`{"jobs":[
	  {"id":0,"title":"Has no id"},
	  {"id":5,"title":"   "},
	  {"id":7,"title":"Real Job","company_name":"Co","absolute_url":"https://x/7"}
	]}`)
	got, err := a.Parse(source.RawDocument{Body: body})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d postings, want 1 (unusable rows must be dropped)", len(got))
	}
	if got[0].ATSJobID != "7" {
		t.Errorf("ats_job_id = %q, want 7", got[0].ATSJobID)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	a := New("x", nil)
	if _, err := a.Parse(source.RawDocument{Body: []byte("<html>not json</html>")}); err == nil {
		t.Fatal("garbage body was accepted")
	}
}

func TestWorkModeOnlyWhenStated(t *testing.T) {
	cases := map[string]string{
		"Remote, Italy":              source.WorkRemote,
		"Remote, Canada; Remote, US": source.WorkRemote,
		"San Francisco (Remote OK)":  source.WorkRemote,
		"Hybrid - Berlin":            source.WorkHybrid,
		"Bangalore, India":           "",
		"":                           "",
	}
	for in, want := range cases {
		if got := workModeFrom(in); got != want {
			t.Errorf("workModeFrom(%q) = %q, want %q", in, got, want)
		}
	}
}
