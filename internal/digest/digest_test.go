package digest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Xubair001/devsignal/internal/matching"
	"github.com/Xubair001/devsignal/internal/opportunity"
	"github.com/Xubair001/devsignal/internal/store"
)

func at(hour int) time.Time {
	return time.Date(2026, 8, 26, hour, 30, 0, 0, time.UTC)
}

// TestQuietHoursWrapAroundMidnight is the case that actually breaks.
//
// 21:00–08:00 is the normal configuration and it is NOT expressible as
// start <= h < end. Written as a BETWEEN it silently inverts, and the service
// then mails people at 3am and stays silent all afternoon — a bug that only
// shows up in production and only to users who were asleep.
func TestQuietHoursWrapAroundMidnight(t *testing.T) {
	const start, end = 21, 8
	quiet := []int{21, 22, 23, 0, 1, 4, 7}
	loud := []int{8, 9, 12, 17, 20}

	for _, h := range quiet {
		if !InQuietHours(at(h), start, end) {
			t.Errorf("%02d:30 should be inside a %02d-%02d window", h, start, end)
		}
	}
	for _, h := range loud {
		if InQuietHours(at(h), start, end) {
			t.Errorf("%02d:30 should be outside a %02d-%02d window", h, start, end)
		}
	}
}

func TestQuietHoursSameDayWindow(t *testing.T) {
	// A daytime window, which is the case a naive wrap-around fix breaks.
	const start, end = 9, 17
	if !InQuietHours(at(12), start, end) {
		t.Error("12:30 should be inside a 09-17 window")
	}
	for _, h := range []int{8, 17, 20, 3} {
		if InQuietHours(at(h), start, end) {
			t.Errorf("%02d:30 should be outside a 09-17 window", h)
		}
	}
}

// TestZeroWidthQuietWindowMeansNoQuietHours guards the boundary that could
// silence a user forever. start == end has two readings — no quiet hours, or a
// 24-hour one — and picking the wrong one means a user who set 0/0 never hears
// from us again and has no way to tell why.
func TestZeroWidthQuietWindowMeansNoQuietHours(t *testing.T) {
	for h := range 24 {
		if InQuietHours(at(h), 0, 0) {
			t.Fatalf("a zero-width window silenced %02d:30", h)
		}
	}
}

// TestInsufficientEvidenceNeverClearsAnyBar is the honesty rule as a test.
//
// "Not enough information" is not a low score — it says we could observe less
// than 60% of the model. Treating it as the bottom of a ladder would make the
// digest interrupt someone on the strength of data we admit we do not have, and
// it would do so most often precisely when extraction is broken.
func TestInsufficientEvidenceNeverClearsAnyBar(t *testing.T) {
	for _, bar := range []string{BarStrong, BarWorthALook} {
		if ClearsBar(matching.BandInsufficient, bar) {
			t.Errorf("%q band cleared the %q bar", matching.BandInsufficient, bar)
		}
	}
}

func TestClearsBar(t *testing.T) {
	cases := []struct {
		band matching.Band
		bar  string
		want bool
	}{
		{matching.BandStrong, BarStrong, true},
		{matching.BandWorth, BarStrong, false},
		{matching.BandStretch, BarStrong, false},
		{matching.BandStrong, BarWorthALook, true},
		{matching.BandWorth, BarWorthALook, true},
		{matching.BandStretch, BarWorthALook, false},
		// An unrecognized bar must fail closed. A typo in a settings value that
		// sent everything to everyone is not a recoverable mistake.
		{matching.BandStrong, "anything_goes", false},
	}
	for _, c := range cases {
		if got := ClearsBar(c.band, c.bar); got != c.want {
			t.Errorf("ClearsBar(%q, %q) = %v, want %v", c.band, c.bar, got, c.want)
		}
	}
}

// TestEmptyReasonDistinguishesQuietMarketFromHighBar checks the empty state
// carries the distinction a reader needs. "No matches today" cannot tell someone
// whether the market was quiet or their bar is set high, and those need opposite
// responses.
func TestEmptyReasonDistinguishesQuietMarketFromHighBar(t *testing.T) {
	nothingEligible := emptyReason(0, 0, 0, BarStrong)
	if !strings.Contains(nothingEligible, "nothing was eligible") {
		t.Errorf("zero considered should say nothing was eligible: %q", nothingEligible)
	}

	barTooHigh := emptyReason(40, 40, 0, BarStrong)
	if !strings.Contains(barTooHigh, "Strong fit") ||
		!strings.Contains(barTooHigh, "40") {
		t.Errorf("should name the count and the bar: %q", barTooHigh)
	}
	if barTooHigh == nothingEligible {
		t.Error("a quiet market and a high bar produced the same reason")
	}

	repeats := emptyReason(10, 0, 10, BarWorthALook)
	if !strings.Contains(repeats, "already") && !strings.Contains(repeats, "sent") {
		t.Errorf("repeat suppression should say so: %q", repeats)
	}
}

// TestEmptyDigestNeverPadsAndSaysWhy is blueprint §4.3's explicit empty state
// plus hard rule 3: never pad to a count.
func TestEmptyDigestNeverPadsAndSaysWhy(t *testing.T) {
	res := Result{
		UserID: "u", Outcome: OutcomeEmpty, Considered: 40, BelowBar: 40,
		Reason: emptyReason(40, 40, 0, BarStrong),
	}
	msg := Render(store.DigestCandidateUsersRow{MinBand: BarStrong}, res)

	if !strings.Contains(msg.Subject, EmptySubject) {
		t.Errorf("an empty digest must say so in the subject, got %q", msg.Subject)
	}
	if !strings.Contains(msg.Text, "40") {
		t.Error("the empty body should carry the honest count of what was considered")
	}
	if strings.Contains(msg.Text, "1.") {
		t.Error("the empty body appears to contain a numbered item")
	}
}

// TestRenderedDigestShowsNoPercentageAndNoInventedSalary is the display contract
// applied to email, which is the harder case: nobody can click through to a
// caveat, and a screenshot of a digest outlives any correction.
func TestRenderedDigestShowsNoPercentageAndNoInventedSalary(t *testing.T) {
	res := Result{
		UserID:  "u",
		Outcome: OutcomeSent,
		Items: []Item{{
			Match: matching.Match{Fit: matching.Fit{
				Score: 72, MaxPossible: 90, WeightsVersion: "w2",
				Factors: []matching.FactorScore{
					{
						Factor: matching.FactorDomain, Available: true, Value: 1,
						Contribution: 10, MaxContribution: 10,
					},
					{
						Factor: matching.FactorRequiredSkills, Available: false,
						Reason: "not extracted",
					},
				},
			}},
			Posting: opportunity.Summary{
				ID: "1", Title: "Senior Backend Engineer",
				Company: opportunity.Company{Name: "GitLab"},
				// Undisclosed. The renderer must say so, not invent a range.
				Salary:   nil,
				Liveness: opportunity.Liveness{VerifiedOpen: true},
			},
		}},
	}
	msg := Render(store.DigestCandidateUsersRow{MinBand: BarStrong}, res)

	for _, body := range []string{msg.Text, msg.HTML} {
		if strings.Contains(body, "%") {
			t.Errorf("a digest rendered a percentage: %q", body)
		}
		if !strings.Contains(body, "Salary not disclosed") {
			t.Error("an undisclosed salary must be stated, not omitted or invented")
		}
		if !strings.Contains(body, "Verified open") {
			t.Error("liveness is the product's central claim and must appear")
		}
		// The unscored factor has to be visible. A reader who cannot see that
		// part of the model was unavailable reads the band as more confident
		// than it is.
		if !strings.Contains(body, "required skills") {
			t.Error("an unscored factor was hidden rather than named")
		}
	}
}

// TestSubjectDoesNotOversellAnEmptyDigest guards a small dishonesty with an
// outsized cost: "Your daily digest" over an empty body is what teaches someone
// to stop opening the channel.
func TestSubjectDoesNotOversellAnEmptyDigest(t *testing.T) {
	empty := Render(store.DigestCandidateUsersRow{}, Result{Outcome: OutcomeEmpty})
	if strings.Contains(strings.ToLower(empty.Subject), "roles worth") {
		t.Errorf("empty digest subject oversells: %q", empty.Subject)
	}
}

// TestNullSenderFailsLoudly checks the default transport refuses rather than
// pretending. A digest run that reports success while delivering nothing is the
// exact failure hard rule 26 exists to prevent.
func TestNullSenderFailsLoudly(t *testing.T) {
	if err := (NullSender{}).Send(t.Context(), Message{}); err == nil {
		t.Fatal("the null sender silently accepted a message")
	}
	s, err := NewSender("", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != SenderNone {
		t.Errorf("default sender is %q, want %q", s.Name(), SenderNone)
	}
}

// TestUnknownSenderIsRejected: a typo in a deploy config must not silently
// disable the retention channel.
func TestUnknownSenderIsRejected(t *testing.T) {
	if _, err := NewSender("ses", "", nil); err == nil {
		t.Fatal("an unknown sender was accepted")
	}
}

func TestLogSenderWritesTheDigest(t *testing.T) {
	dir := t.TempDir()
	s := LogSender{Dir: dir}
	if err := s.Send(t.Context(), Message{
		To: "a@example.com", Subject: EmptySubject,
		Text: "body", UserID: "u1",
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("wrote %d files, want 1", len(entries))
	}
	b, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{EmptySubject, "body", "a@example.com"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("written digest missing %q", want)
		}
	}
}
