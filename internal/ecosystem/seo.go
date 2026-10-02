package ecosystem

import (
	"fmt"
	"html"
	"strings"

	"github.com/stashapp/stash/internal/collab"
)

// R060 — "SEO public pages for every entity" — and §6a.21, "SEO surfaces, and the line they
// may not cross".
//
// DecideIndexing (above) answers whether an entity MAY have a public page. This is the half
// that was missing: rendering the page and describing it to a crawler.
//
// THE GATE CANNOT BE BYPASSED, BY CONSTRUCTION
//
// IndexEntity takes a `collab.ShareChoice`, not a boolean, and calls DecideIndexing itself.
// There is deliberately no "render this page" function that does not take consent state,
// because a render-then-check design produces two code paths that each look correct in review
// and only one of which asks. The consent decision is an INPUT here, so a caller cannot skip
// it by using a different entry point.
//
// WHY AN EMPTY PAGE IS STILL A PAGE
//
// A refused entity returns Page{}, not a page saying "this exists but is private". The two
// look identical to a crawler, which is the point: if a refusal rendered a distinguishable
// shell, the difference between "opted out" and "never existed" would be observable, and that
// difference is precisely the metadata consent is protecting. §6a.21's rule is that an
// opted-out entity is "neither indexed nor reachable through a public page" — reachable
// includes rendering something that answers the question.
//
// A caller that wants a response body for a refused entity uses NotFoundPage, which is
// deliberately the same bytes for every reason.

// Page is a rendered public page for one entity.
//
// Empty means "nothing may be served for this entity". The zero value is therefore the
// refusal, and that is what makes `if page.Empty()` a safe guard at every call site.
type Page struct {
	// Body is the HTML document. Empty when the entity is not indexable.
	Body string
	// Canonical is the absolute URL a crawler should index, and is also what deduplicates
	// the same entity served under more than one path.
	Canonical string
	// Robots is the robots directive for this page. It is a FIELD rather than something
	// implied by Body so a caller cannot emit a page while believing it is hidden: the two
	// decisions travel together or not at all.
	Robots string
	// Title is the visible title, kept alongside Body so a caller building a sitemap or an
	// OpenGraph card does not have to re-parse HTML to get it.
	Title string
}

// Empty reports whether the page may be served at all.
func (p Page) Empty() bool { return p.Body == "" }

// publicBaseURL is a placeholder base used by the tests. Production passes the instance's
// configured public URL; a Page whose canonical is relative would be resolved by whatever
// host the crawler guessed, which is how a staging hostname ends up in an index.
const publicBaseURL = "https://public.example"

// IndexEntity decides whether an entity may be publicly indexed and, if so, renders its page.
//
// This is the ONLY way to produce a public page. It asks DecideIndexing first and returns an
// empty Page on refusal, so consent is a precondition of rendering rather than a check that
// runs beside it.
func IndexEntity(e PublicEntity, share collab.ShareChoice, baseURL string) (Page, IndexDecision) {
	decision := DecideIndexing(share, e.Published)
	if !decision.Indexable {
		// Empty, not explanatory: see the type comment. The REASON is returned separately for
		// an operator reading logs, and must not reach the page.
		return Page{}, decision
	}

	// A base URL that does not end in a slash would produce "...exampleseene-42", so the
	// joining is done explicitly rather than with fmt's %s%v.
	base := strings.TrimRight(baseURL, "/")
	canonical := base + "/" + escapePathSegment(e.PublicID)

	title := html.EscapeString(e.Title)
	// The description reuses the title because PublicEntity carries no summary field. That is
	// a real limitation and worth stating rather than hiding: a description that repeats the
	// title is weak SEO, and adding a summary is the obvious next step. What matters for
	// correctness here is that the element EXISTS, so a later summary is a drop-in.
	description := title

	body := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>%s</title>
<meta name="description" content="%s">
<meta name="robots" content="%s">
<link rel="canonical" href="%s">
</head>
<body>
<h1>%s</h1>
</body>
</html>
`, title, description, "index, follow", html.EscapeString(canonical), title)

	return Page{
		Body:      body,
		Canonical: canonical,
		Robots:    "index, follow",
		Title:     e.Title,
	}, decision
}

// NotFoundPage is the body served for an entity that is not indexable AND for an id that does
// not exist.
//
// The single function is the point. Two callers rendering two different "not found" bodies is
// how the two cases become distinguishable, and a distinguishable refusal is a disclosure.
func NotFoundPage() string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Not found</title>
<meta name="robots" content="noindex, follow">
</head>
<body>
<h1>Not found</h1>
</body>
</html>
`
}

// escapePathSegment percent-encodes a value destined for a single URL path segment.
//
// I WROTE THIS WRONG FIRST, using html.EscapeString on the reasonable-sounding theory that it
// was "escaping for a URL". It is not -- it escapes for HTML, and it leaves "/" untouched. A
// PublicID of "a/../../b" therefore produced the canonical ".../a/../../b", which resolves
// outside its own entity. My own comment claimed the opposite of what the function did, which
// is worse than having no comment: it would have stopped the next reader checking.
//
// The encoding is done per-RUNE rather than with url.QueryEscape, because QueryEscape is
// form-encoding: it turns a space into "+", which in a path means a literal plus, and it
// escapes "/" to %2F only as a side effect of escaping every reserved character rather than as
// a deliberate statement that the separator is data here.
//
// Each BYTE of a multi-byte rune is escaped, so the result is valid percent-encoding rather
// than a string of escaped runes.
func escapePathSegment(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	// Three bytes minimum per input byte in the worst case, plus a separator.
	b.Grow(len(s) * 3)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isPathSegmentSafe(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0f])
	}
	return b.String()
}

// isPathSegmentSafe reports whether a byte may appear literally in a path segment.
//
// The unreserved set from RFC 3986 minus the sub-delims that are legal in a segment but
// confusing in a path we also emit into HTML: '&' and '=' and '+' are excluded because they
// are HTML-significant, and '%' because a literal percent would be ambiguous with our own
// escaping. Everything else is escaped, which is the safe default -- an over-escaped id still
// resolves, an under-escaped one can address a different entity.
func isPathSegmentSafe(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '-' || c == '_' || c == '.' || c == '~':
		return true
	}
	return false
}
