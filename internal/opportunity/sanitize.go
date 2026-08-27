package opportunity

import (
	"sync"

	"github.com/microcosm-cc/bluemonday"
)

// descriptionPolicy is the allow-list a job description is rendered through.
//
// This exists because `opportunity.description_text` holds the posting body
// EXACTLY as the board served it, tags and all — verified against the live
// corpus, where every row starts with a <div>. It is third-party content from a
// source anyone can post to, and it was being handed to clients as
// `description_html`. A client rendering that is a stored-XSS hole: a script in
// an employer's own posting would execute with an operator's session.
//
// An allow-list, not an escape or a blocklist. Escaping would show a reader a
// wall of &lt;p&gt; instead of a formatted posting, and a blocklist of dangerous
// tags is a bet that the list is complete — which it never is, because the
// attack surface includes attributes, protocols and CSS as well as tags.
//
// Built once: compiling a policy per request is measurable and pointless.
var descriptionPolicy = sync.OnceValue(func() *bluemonday.Policy {
	p := bluemonday.NewPolicy()

	// Structure a job description legitimately uses, and nothing else. No
	// <img>, <iframe>, <video>, <form>, <style> or <script>: none of them are
	// needed to read a posting, and each is a way to make a request the reader
	// did not ask for or to obscure what the page is doing.
	p.AllowElements(
		"p", "br", "div", "span",
		"h1", "h2", "h3", "h4", "h5", "h6",
		"strong", "b", "em", "i", "u", "s", "small", "sub", "sup",
		"ul", "ol", "li", "dl", "dt", "dd",
		"blockquote", "pre", "code",
		"table", "thead", "tbody", "tfoot", "tr", "th", "td",
		"hr",
	)

	// Links, restricted. Relative URLs are rejected because the posting is not
	// served from our origin, so a relative link resolves against the console and
	// points somewhere it was never meant to.
	p.AllowAttrs("href").OnElements("a")
	p.AllowURLSchemes("http", "https", "mailto")
	p.RequireNoFollowOnLinks(true)
	p.RequireNoReferrerOnLinks(true)
	// Opens off-site, and target=_blank without noopener hands the opener to the
	// destination page.
	p.AddTargetBlankToFullyQualifiedLinks(true)

	// Deliberately NOT allowed: class, id, style, and every data-* and on*
	// attribute. `style` alone carries CSS-based exfiltration and layout
	// hijacking; `class` and `id` let third-party markup collide with the
	// console's own styles and reshape the page around it.
	return p
})

// SanitizeDescription returns the posting body as HTML safe to render.
//
// Applied at the serve boundary rather than on ingest, deliberately. The stored
// value stays byte-identical to what the source sent, which is what provenance
// and the content hash depend on — sanitizing on the way in would change the
// hash, invalidate every cached extraction, and leave us unable to answer "what
// did the board actually publish". The trust boundary is the client, so that is
// where the filter belongs.
func SanitizeDescription(raw *string) *string {
	if raw == nil {
		return nil
	}
	clean := descriptionPolicy().Sanitize(*raw)
	return &clean
}
