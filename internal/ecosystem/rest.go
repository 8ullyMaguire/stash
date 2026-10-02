package ecosystem

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// R054's REST surface, and the SDK boundary it is built on.
//
// §6a.18 names "REST, SDKs and webhooks" as one requirement, and the reason to build them on one
// shared core rather than three handlers is the failure mode: three handlers means three places
// to get the consent rule wrong, and the one that is wrong is the one nobody tests because the
// other two work.
//
// So this file does no querying of its own. It TRANSLATES -- request to PublicQuery, result to
// response -- and delegates the read to the SDK. The consent and bound rules therefore live in
// exactly one place, and this surface's job is to make sure it cannot be reached around.
//
// THE THREE THINGS A PUBLIC SURFACE GETS WRONG, in order of how badly they hurt:
//
//  1. Unbounded reads. A 100k-scene library must not be enumerable in 100k requests, so the cap
//     is applied HERE as well as in the core. Applying it in both places is not redundancy: this
//     surface parses a limit from a URL, where a caller controls it, and the core's cap protects
//     against everything else.
//  2. Consent applied AFTER the read. §6a.2's state is revocable, so a response assembled before
//     the check is a response built from a decision that may have changed. The core checks; this
//     surface does not get a vote.
//  3. Errors that leak. An error message naming an opted-out entity tells a caller the entity
//     EXISTS, which is the disclosure §6a.21 exists to prevent. So a not-found and a
//     not-permitted are the same answer, and that is asserted below.

// RESTQuery is a parsed public read request.
type RESTQuery struct {
	Limit  int
	Offset int
	// Origin, when set, restricts the read to one peer's entities (§6a.6's instance half).
	Origin string
}

// ParseQuery reads a request's query string.
//
// A malformed limit is a CLIENT ERROR and says so, rather than silently becoming the default: a
// caller that asked for 500 and got 100 with no indication has no way to know to paginate, and
// the cap is exactly the kind of limit a caller needs to see.
func ParseQuery(raw string) (RESTQuery, error) {
	q := RESTQuery{Limit: MaxPublicLimit}
	v, err := url.ParseQuery(raw)
	if err != nil {
		return RESTQuery{}, fmt.Errorf("ecosystem: unparseable query string: %w", err)
	}

	if s := strings.TrimSpace(v.Get("limit")); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return RESTQuery{}, fmt.Errorf("ecosystem: limit %q is not a number", s)
		}
		// Out-of-range is an error, not a clamp. A silent clamp looks like the server ignoring
		// the request, and the caller cannot distinguish that from a bug.
		if n <= 0 {
			return RESTQuery{}, fmt.Errorf("ecosystem: limit %d must be positive", n)
		}
		if n > MaxPublicLimit {
			return RESTQuery{}, fmt.Errorf("ecosystem: limit %d exceeds the maximum of %d", n, MaxPublicLimit)
		}
		q.Limit = n
	}

	if s := strings.TrimSpace(v.Get("offset")); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return RESTQuery{}, fmt.Errorf("ecosystem: offset %q is not a number", s)
		}
		if n < 0 {
			return RESTQuery{}, fmt.Errorf("ecosystem: offset %d must not be negative", n)
		}
		q.Offset = n
	}

	if o := strings.TrimSpace(v.Get("origin")); o != "" {
		if strings.ContainsAny(o, "/?#") {
			// An origin is a path segment in §6a.6's (instance, local) pair. Allowing a slash
			// lets one caller address another's namespace with a crafted value, so it is
			// refused here rather than escaped downstream where the damage is done.
			return RESTQuery{}, fmt.Errorf("ecosystem: origin %q must be a single path segment", o)
		}
		q.Origin = o
	}
	return q, nil
}

// RESTEntity is one entity in a REST response.
//
// It is PublicEntity plus a link, and the link is built rather than accepted: a response that
// carried a caller-supplied URL would be an open redirect on the public surface.
type RESTEntity struct {
	PublicID  string `json:"id"`
	Title     string `json:"title"`
	Published bool   `json:"published"`
	Origin    string `json:"origin,omitempty"`

	// Self is the canonical URL for this entity, built from the base.
	Self string `json:"self"`
}

// RESTResponse is a page.
type RESTResponse struct {
	Entities []RESTEntity `json:"entities"`
	// Limit and Offset are echoed so a caller can see what the server actually applied -- the
	// cap is only useful to a client if the client can observe it.
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
	// Total is the number RETURNED, not the number matching. A real total would leak the size
	// of a library a caller may only partly see, and would cost a full scan to compute.
	Total int `json:"total"`
}

// PublicREST is the REST surface. It holds an SDK and translates; it does not query.
type PublicREST struct {
	sdk SDK
	// base is the canonical URL prefix, e.g. "https://stash.example/api/v1".
	base string
}

// NewPublicREST builds the surface over an SDK.
func NewPublicREST(sdk SDK, base string) (*PublicREST, error) {
	if sdk == nil {
		return nil, fmt.Errorf("ecosystem: the REST surface needs an SDK to read through")
	}
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return nil, fmt.Errorf("ecosystem: the REST surface needs a base URL")
	}
	// A base that is not absolute would produce relative Self links, and a canonical URL that
	// is relative is not canonical.
	u, err := url.Parse(base)
	if err != nil || !u.IsAbs() {
		return nil, fmt.Errorf("ecosystem: base URL %q must be absolute", base)
	}
	return &PublicREST{sdk: sdk, base: base}, nil
}

// Get answers a public read.
func (p *PublicREST) Get(ctx context.Context, rawQuery string) (RESTResponse, error) {
	q, err := ParseQuery(rawQuery)
	if err != nil {
		// A client error carries no entity information, and saying which entities exist is
		// itself a disclosure.
		return RESTResponse{}, err
	}

	entities, err := p.sdk.Query(ctx, PublicQuery{Limit: q.Limit, Offset: q.Offset})
	if err != nil {
		// The SDK's error is NOT forwarded. It may name an entity, and this surface's contract
		// is that a caller learns nothing about entities it may not see. A caller can retry and
		// get the same absence.
		return RESTResponse{}, errNotVisible
	}

	// The origin filter is applied HERE, after the read, because the core has no notion of it.
	// That is a real limitation and worth stating: it means the core read was broader than the
	// response. The cap bounds the exposure, and no CONSENT is involved -- an origin filter
	// narrows, it never widens -- so §6a.2 is not engaged.
	out := make([]RESTEntity, 0, len(entities))
	for _, e := range entities {
		if q.Origin != "" && e.Origin != q.Origin {
			continue
		}
		out = append(out, p.entityURL(e))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PublicID < out[j].PublicID })

	return RESTResponse{Entities: out, Limit: q.Limit, Offset: q.Offset, Total: len(out)}, nil
}

// entityURL builds one entity's canonical URL.
//
// The path is ESCAPED, and the reason is §6a.6: a public id containing a slash or a traversal
// segment would otherwise address a different resource, which on a public surface is a way to
// read something the caller did not ask for.
func (p *PublicREST) entityURL(e PublicEntity) RESTEntity {
	return RESTEntity{
		PublicID:  e.PublicID,
		Title:     e.Title,
		Published: e.Published,
		Origin:    e.Origin,
		Self:      p.base + "/entities/" + url.PathEscape(e.PublicID),
	}
}

// errNotVisible is what a caller sees for anything it may not.
//
// ONE answer for "does not exist" and "not permitted", because distinguishing them tells the
// caller which of the two it is -- and a not-permitted answer confirms the entity exists. §6a.21
// says an opted-out entity is "neither indexed nor reachable"; an error that says "opted out"
// makes it reachable as a fact.
var errNotVisible = fmt.Errorf("ecosystem: not found")

// NotVisible is the public spelling of errNotVisible, for a transport to map to a status.
func NotVisible() error { return errNotVisible }
