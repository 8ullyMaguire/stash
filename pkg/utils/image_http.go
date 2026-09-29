package utils

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Referer handling for outbound image requests. Solves stash#2540, which
// reported that the unconditional Referer Stash sends helps some hosts and
// breaks others, with no way to choose.
//
// WHY A LADDER RATHER THAN A SINGLE HEADER. Stash has always sent the image host
// as the Referer, which is what makes hotlink-protected hosts -- the common case
// for scraped images -- serve the file. Sending it unconditionally also breaks
// hosts that reject a cross-origin-looking referer, or that require a specific
// one. Neither behaviour is a bug in Stash; the hosts disagree with each other.
// So the first attempt stays exactly what it always was, and the alternatives
// are tried only when the server actively refuses.
//
// A per-scraper referer option was considered and rejected upstream: only a
// couple of scrapers need it, and the option would have to be threaded through
// every scraper definition, breaking all the ones that depend on today's
// behaviour.
//
// The order is part of the fix, not an implementation detail:
//
//	host   -- what Stash always sent, and what works most often
//	none   -- a server that 403s on a referer is usually checking for absence
//	domain -- the weakest signal, so the least likely to be what a server
//	          wanted, but some match on the registrable domain and will serve
//	          to a referer they consider same-site
//
// Only 403 triggers a retry. 401 is an authentication problem no referer change
// can fix, 404 is a missing file, and 5xx is the server's own fault; retrying
// those triples the load on a server that is already unhappy.
var RefererAttempts = []RefererStrategy{
	{Name: "host", Build: func(u *url.URL) string { return u.Scheme + "://" + u.Host + "/" }},
	{Name: "none", Build: nil},
	{Name: "domain", Build: func(u *url.URL) string {
		d := RegistrableDomain(u.Hostname())
		if d == "" {
			return ""
		}
		return u.Scheme + "://" + d + "/"
	}},
}

// RefererStrategy is one attempt: a name for the error message, and either a
// builder for the Referer value or nil to send no Referer at all.
type RefererStrategy struct {
	Name  string
	Build func(u *url.URL) string
}

// Apply sets the Referer for one attempt on req.
//
// The header is deleted first rather than overwritten, because the "none"
// strategy is defined by the ABSENCE of the header. A blanked header is still a
// header, so a retry that inherited the previous value would silently repeat the
// first attempt and the ladder would never work while appearing to.
func (s RefererStrategy) Apply(req *http.Request) {
	req.Header.Del("Referer")
	if s.Build == nil {
		return
	}
	if v := s.Build(req.URL); v != "" {
		req.Header.Set("Referer", v)
	}
}

// RegistrableDomain reduces a hostname to its registrable domain
// ("example.co.uk" from "images.example.co.uk").
//
// This is deliberately not a full public-suffix implementation. A public suffix
// list is a large maintained data set, and the only consumer here is a last-ditch
// Referer attempt on a host that has already returned 403 twice. Where the list
// and this differ -- an unknown multi-part suffix -- the result is a referer the
// server may or may not accept, which is the same situation as not having the
// list at all. The common cases are handled: a bare hostname is returned
// unchanged, and an IP address returns nothing at all.
//
// An IP returning nothing matters: taking the last two labels of "192.168.0.1"
// would produce "0.1", which as a referer is nonsense and, worse, would put part
// of a private address in a header sent to a third party.
func RegistrableDomain(host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return ""
	}

	// Brackets are stripped defensively. url.Hostname() already removes them,
	// but a bracketed literal arriving directly would otherwise be split on its
	// dots and produce garbage.
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if ip := net.ParseIP(host); ip != nil {
		return ""
	}

	labels := strings.Split(host, ".")
	if len(labels) <= 2 {
		return host
	}

	// Two-part public suffixes common enough to be worth handling. Anything
	// else falls through to the last two labels, which is wrong for e.g.
	// "example.com.au" if that ever stops being common.
	twoPartSuffixes := map[string]bool{
		"co.uk": true, "org.uk": true, "ac.uk": true, "gov.uk": true,
		"co.jp": true, "or.jp": true, "ne.jp": true,
		"com.au": true, "net.au": true, "org.au": true,
		"com.br": true, "com.mx": true, "co.nz": true, "co.za": true,
		"co.in": true, "com.cn": true, "com.tr": true,
	}
	if len(labels) >= 3 && twoPartSuffixes[labels[len(labels)-2]+"."+labels[len(labels)-1]] {
		return strings.Join(labels[len(labels)-3:], ".")
	}

	return strings.Join(labels[len(labels)-2:], ".")
}

// IsImageContentType reports whether a Content-Type names an image.
//
// Parameters are stripped first because servers append them ("; charset=binary",
// ";q=0.9") and a strict equality check would reject a perfectly good image. An
// empty type is treated as an image, leaving the decision to the caller's content
// sniffing; a non-empty type that is not an image is rejected.
func IsImageContentType(contentType string) bool {
	if contentType == "" {
		return true
	}
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = contentType[:i]
	}
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	return strings.HasPrefix(contentType, "image/")
}

// DoWithRefererLadder performs req with each Referer strategy in turn and
// returns the first response whose status is under 400, along with its body.
//
// The request is reused across attempts, so anything a caller has already put
// on it -- scraper auth headers, a User-Agent -- is identical every time and the
// attempts are comparable. Only the Referer changes.
//
// A transport error is returned immediately rather than retried: the request
// never reached a server that could object to the Referer, so the other
// strategies are not a second opinion on anything.
func DoWithRefererLadder(ctx context.Context, client *http.Client, req *http.Request) (*http.Response, []byte, error) {
	var lastErr error
	var attempted []string

	for _, strategy := range RefererAttempts {
		attempted = append(attempted, strategy.Name)

		// Do not retry a request whose context is already done, or the final
		// error would be a confusing "context canceled" instead of the 403 that
		// actually happened.
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return nil, nil, lastErr
			}
			return nil, nil, err
		}

		strategy.Apply(req)

		resp, err := client.Do(req)
		if err != nil {
			return nil, nil, err
		}

		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()

		if readErr != nil {
			return nil, nil, readErr
		}

		if resp.StatusCode < 400 {
			return resp, body, nil
		}

		lastErr = fmt.Errorf("http error %d", resp.StatusCode)

		// Only 403 is worth a second opinion. See RefererAttempts.
		if resp.StatusCode != http.StatusForbidden {
			return nil, nil, lastErr
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no attempt was made")
	}
	return nil, nil, fmt.Errorf("%s (tried referer: %s)", lastErr, strings.Join(attempted, ", "))
}
