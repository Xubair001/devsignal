//go:build integration

package profile

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestNoPIIInLogs runs the paths that HANDLE personal data and asserts none of
// it reaches the logs.
//
// Behavioural rather than a source scan. A grep for log calls mentioning an
// email would miss the two ways this actually happens: an error string that
// embeds the value it failed on, and a struct logged whole because it was
// convenient. Both only appear in the output.
//
// Resume ingest is the highest-risk path in the codebase — a resume is a name,
// an address, an employment history and sometimes a date of birth — so it is the
// one this exercises, including its failure branches, where a value is most
// likely to end up interpolated into a message.
//
// Hard rule 13 is absolute: not in errors, not in debug, not temporarily. The
// capture runs at Debug for that reason.
func TestNoPIIInLogs(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	svc := testServiceWithLogger(t, pool, log)
	userID, tenantID := newUser(t, pool)

	const (
		name  = "Renata Oyelaran"
		phone = "+44 20 7946 0958"
	)
	email := "renata.oyelaran-" + uuid.NewString()[:8] + "@example.test"
	resume := name + "\nLondon, United Kingdom\n" + email + "\n" + phone + "\n\n" +
		"SUMMARY\nStaff engineer, ten years on payment systems.\n\n" +
		"SKILLS\nGo, PostgreSQL, Kubernetes\n"

	headline := "Staff engineer — " + name
	if _, err := svc.Save(ctx, userID, tenantID, Input{Headline: &headline}); err != nil {
		t.Fatal(err)
	}

	// A filename carrying the candidate's name, which is how resumes are named.
	if _, err := svc.UploadResume(ctx, userID, Upload{
		Filename: "renata-oyelaran-cv.txt", ContentType: "text/plain",
		Body: []byte(resume),
	}); err != nil {
		t.Fatal(err)
	}

	// Failure branches: an unsupported type, and a document with no text layer.
	_, _ = svc.UploadResume(ctx, userID, Upload{
		Filename: "renata-oyelaran-photo.png", ContentType: "image/png",
		Body: []byte("\x89PNG\r\n\x1a\n" + name + " " + email),
	})
	_, _ = svc.UploadResume(ctx, userID, Upload{
		Filename: "renata-oyelaran-scan.pdf", ContentType: "application/pdf",
		Body: []byte("%PDF-1.4 " + name + " " + email),
	})

	out := buf.String()
	if strings.TrimSpace(out) == "" {
		t.Fatal("nothing was logged, so this test proves nothing — if the logging " +
			"moved, move this test with it rather than letting it pass empty")
	}

	local := email[:strings.Index(email, "@")]
	banned := map[string]string{
		email:             "the email address",
		local:             "the local part of the address",
		name:              "the candidate's name",
		"Renata":          "a name fragment",
		"Oyelaran":        "a surname fragment",
		phone:             "a phone number",
		"payment systems": "resume body text",
		"renata-oyelaran": "the filename, which carries the name",
	}
	for needle, what := range banned {
		if strings.Contains(out, needle) {
			t.Errorf("%s reached the logs.\n"+
				"Hard rule 13: log the user_id, log nothing about the person.\n"+
				"Offending line: %s", what, firstLineContaining(out, needle))
		}
	}

	// The allowed identifier IS present, so this cannot pass by logging nothing
	// useful.
	if !strings.Contains(out, userID.String()) {
		t.Error("no user_id in the logs either; the rule is to log the user_id, " +
			"so this is a gap rather than a pass")
	}
}

func firstLineContaining(haystack, needle string) string {
	for _, line := range strings.Split(haystack, "\n") {
		if strings.Contains(line, needle) {
			if len(line) > 280 {
				return line[:280] + "…"
			}
			return line
		}
	}
	return ""
}
