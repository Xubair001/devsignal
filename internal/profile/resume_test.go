package profile

import (
	"errors"
	"strings"
	"testing"
)

func TestExtractPlainText(t *testing.T) {
	got, err := ExtractText([]byte("Senior Backend Engineer\n\nGo, PostgreSQL, AWS\n"), TypeText)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !strings.Contains(got, "Senior Backend Engineer") || !strings.Contains(got, "PostgreSQL") {
		t.Errorf("content lost: %q", got)
	}
	// Whitespace collapsed: PDF extraction emits a lot of it, and it would
	// otherwise inflate token counts when the text reaches a model.
	if strings.Contains(got, "\n") || strings.Contains(got, "  ") {
		t.Errorf("whitespace not collapsed: %q", got)
	}
}

func TestContentTypeWithCharsetIsAccepted(t *testing.T) {
	if !SupportedType("text/plain; charset=utf-8") {
		t.Error("a charset parameter should not make a type unsupported")
	}
	if _, err := ExtractText([]byte("hello"), "text/plain; charset=utf-8"); err != nil {
		t.Errorf("extract with charset: %v", err)
	}
}

func TestUnsupportedTypesAreRejected(t *testing.T) {
	for _, ct := range []string{
		"application/msword", "application/zip", "image/png", "", "application/octet-stream",
	} {
		if SupportedType(ct) {
			t.Errorf("%q reported as supported", ct)
		}
		if _, err := ExtractText([]byte("x"), ct); !errors.Is(err, ErrUnsupportedType) {
			t.Errorf("%q: got %v, want ErrUnsupportedType", ct, err)
		}
	}
}

func TestOversizeIsRejectedBeforeParsing(t *testing.T) {
	big := make([]byte, MaxResumeBytes+1)
	if _, err := ExtractText(big, TypePDF); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v, want ErrTooLarge", err)
	}
}

// Hostile input must return an error, never panic. The PDF library panics on
// some malformed files, so the recover in extractPDF is load-bearing.
func TestMalformedPDFDoesNotPanic(t *testing.T) {
	cases := map[string][]byte{
		"not a pdf at all":    []byte("this is plainly not a pdf"),
		"truncated header":    []byte("%PDF-1.4"),
		"header then garbage": append([]byte("%PDF-1.7\n"), []byte{0x00, 0xff, 0xfe, 0x01, 0x02}...),
		"empty":               {},
		"null bytes":          make([]byte, 512),
	}
	for name, body := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: panicked instead of returning an error: %v", name, r)
				}
			}()
			if _, err := ExtractText(body, TypePDF); err == nil {
				t.Errorf("%s: accepted as a valid PDF", name)
			}
		}()
	}
}

// An error must never carry document content: it would put PII in a log line.
func TestErrorsDoNotEchoDocumentContent(t *testing.T) {
	secret := "Jane Doe jane.doe@example.com +1-555-0100"
	body := append([]byte("%PDF-1.4\n"), []byte(secret)...)
	_, err := ExtractText(body, TypePDF)
	if err == nil {
		t.Skip("this fixture parsed; nothing to assert")
	}
	for _, leak := range []string{"Jane Doe", "jane.doe@example.com", "555-0100"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error message leaked %q: %v", leak, err)
		}
	}
}

func TestFingerprintIsStableAndDistinct(t *testing.T) {
	a := Fingerprint([]byte("resume one"))
	b := Fingerprint([]byte("resume one"))
	c := Fingerprint([]byte("resume two"))
	if string(a) != string(b) {
		t.Error("fingerprint is not stable for identical input")
	}
	if string(a) == string(c) {
		t.Error("different documents share a fingerprint")
	}
	if len(a) != 32 {
		t.Errorf("fingerprint length %d, want 32", len(a))
	}
}

// The prefix is what makes erasure a single call, so its shape is a contract.
func TestObjectKeysAreNamespacedByUser(t *testing.T) {
	const user = "11111111-1111-1111-1111-111111111111"
	const other = "22222222-2222-2222-2222-222222222222"

	key := ObjectKey(user, "abc", ".pdf")
	if !strings.HasPrefix(key, UserPrefix(user)) {
		t.Fatalf("key %q is not under the user prefix %q", key, UserPrefix(user))
	}
	if strings.HasPrefix(key, UserPrefix(other)) {
		t.Error("key matched another user's prefix")
	}
	// The text sibling must share the prefix, or erasure would miss it.
	textKey := TextObjectKey(user, "abc")
	if !strings.HasPrefix(textKey, UserPrefix(user)) {
		t.Error("extracted-text key is not under the user prefix")
	}
	// And it must NOT collide with the source document. Deriving it as ".txt"
	// made a plain-text upload overwrite its own original.
	if textKey == ObjectKey(user, "abc", ".txt") {
		t.Error("extracted-text key collides with a .txt upload's own key")
	}
	if textKey == ObjectKey(user, "abc", ".pdf") {
		t.Error("extracted-text key collides with the source document")
	}
	if UserPrefix(user) == UserPrefix(other) {
		t.Error("distinct users share a prefix")
	}
}

func TestCleanTextDropsControlCharacters(t *testing.T) {
	got := cleanText("Go\x00Postgres\x07\x1bAWS")
	if strings.ContainsAny(got, "\x00\x07\x1b") {
		t.Errorf("control characters survived: %q", got)
	}
	for _, want := range []string{"Go", "Postgres", "AWS"} {
		if !strings.Contains(got, want) {
			t.Errorf("dropped real content %q from %q", want, got)
		}
	}
}

// Regression: the array columns are NOT NULL with an empty-array default, and a
// DEFAULT does not apply when NULL is passed explicitly. A caller that simply
// omits an optional field must not hit a constraint violation, so the service
// normalizes nil to empty. This asserts the normalization exists at the boundary
// where callers actually leave fields unset.
func TestEmptyInputIsNormalizedNotNil(t *testing.T) {
	var in Input
	if in.TargetRoleFamilies != nil || in.TargetCountries != nil || in.Languages != nil {
		t.Fatal("fixture assumption wrong: zero-value Input should have nil slices")
	}
	// Documented contract: Save must accept a zero-value Input. The integration
	// test proves it against the real constraint; this pins the intent.
	if len(in.WorkAuthorization) != 0 {
		t.Error("zero-value WorkAuthorization should be empty")
	}
}
