// Package profile owns the developer's side of every match, including resume
// ingestion.
//
// This is the first place real PII enters the system, so two rules apply
// throughout: nothing identifying is ever logged, and every derived artifact is
// enumerable for erasure (see the privacy-surface skill).
package profile

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/ledongthuc/pdf"
)

// MaxResumeBytes caps an upload. Generous for a real CV, small enough that a
// malicious or broken upload cannot exhaust memory.
const MaxResumeBytes = 10 << 20 // 10 MiB

var (
	ErrTooLarge        = errors.New("resume exceeds the size limit")
	ErrUnsupportedType = errors.New("unsupported resume format")
	ErrNoTextFound     = errors.New("no readable text found in the document")
)

// Supported content types. Deliberately narrow: every additional parser is
// another way for hostile input to reach a library.
const (
	TypePDF  = "application/pdf"
	TypeText = "text/plain"
	TypeMD   = "text/markdown"
)

func SupportedType(ct string) bool {
	switch normalizeContentType(ct) {
	case TypePDF, TypeText, TypeMD:
		return true
	}
	return false
}

func normalizeContentType(ct string) string {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i]) // drop "; charset=..."
	}
	return ct
}

// ExtractText pulls readable text out of an uploaded document.
//
// Pure: no network, no database, no clock. That keeps it fast to test against
// hostile input, which is the input that matters here.
func ExtractText(body []byte, contentType string) (string, error) {
	if len(body) > MaxResumeBytes {
		return "", ErrTooLarge
	}
	switch normalizeContentType(contentType) {
	case TypePDF:
		return extractPDF(body)
	case TypeText, TypeMD:
		return cleanText(string(body)), nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupportedType, contentType)
	}
}

// extractPDF reads text from a PDF. The library panics on some malformed files,
// so the recover is load-bearing rather than defensive habit: a bad upload must
// return an error, not take down the process.
func extractPDF(body []byte) (out string, err error) {
	defer func() {
		if r := recover(); r != nil {
			// The panic value can echo document bytes, so it is deliberately not
			// included in the error — that would put PII in a log line.
			err = fmt.Errorf("%w: document could not be parsed", ErrUnsupportedType)
		}
	}()

	// The parser's own error text is deliberately NOT wrapped in. It can quote
	// document bytes, and an error string ends up in logs and in the persisted
	// parse_error column — which is exactly where resume content must not go.
	// The failure mode is fixed vocabulary; see classifyExtractError.
	r, err := pdf.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return "", fmt.Errorf("%w: could not read the document structure", ErrUnsupportedType)
	}
	buf, err := r.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("%w: could not extract a text layer", ErrUnsupportedType)
	}
	raw, err := io.ReadAll(io.LimitReader(buf, MaxResumeBytes))
	if err != nil {
		return "", fmt.Errorf("reading extracted text: %w", err)
	}

	text := cleanText(string(raw))
	if text == "" {
		// A scanned CV is images with no text layer. Saying so is more useful
		// than pretending the parse succeeded and storing an empty document.
		return "", ErrNoTextFound
	}
	return text, nil
}

// cleanText collapses whitespace and drops control characters. PDF extraction
// emits a lot of both, and they would otherwise inflate token counts for no
// benefit when the text reaches a model.
func cleanText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	lastSpace := true
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t' || r == ' ':
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		case unicode.IsControl(r):
			// dropped
		default:
			b.WriteRune(r)
			lastSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}

// Fingerprint identifies a document without storing it. Used to skip
// re-extraction of an identical re-upload, the same way opportunity extraction
// is cached on content hash.
func Fingerprint(body []byte) []byte {
	sum := sha256.Sum256(body)
	return sum[:]
}

// ObjectKey namespaces every object under the owning user.
//
// The prefix is what makes erasure a single call: everything belonging to a user
// can be removed even if the database rows are already gone.
func ObjectKey(userID, resumeID, ext string) string {
	return fmt.Sprintf("users/%s/resumes/%s%s", userID, resumeID, ext)
}

// TextObjectKey is the extracted-text sibling of a stored document.
//
// The suffix is deliberately distinct from any source extension. Deriving it as
// ".txt" collided with a plain-text upload's own key and silently overwrote the
// original document — which defeats keeping the source so a parser fix can be
// re-run without asking the user to upload again.
func TextObjectKey(userID, resumeID string) string {
	return fmt.Sprintf("users/%s/resumes/%s.extracted.txt", userID, resumeID)
}

// UserPrefix is the erasure unit.
func UserPrefix(userID string) string {
	return fmt.Sprintf("users/%s/", userID)
}
