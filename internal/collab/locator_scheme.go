package collab

import (
	"fmt"
	"net/url"
	"strings"
)

// LocatorScheme is a locator's protocol, as a closed set.
//
// # WHY A CLOSED SET, AND NOT A STRING
//
// The downloader plugin needs to know which protocols it will ever be asked to
// fetch, for two reasons that pull in opposite directions. It must not accept
// a protocol it cannot handle and then fail confusingly at transfer time —
// "file://", say, which would turn a downloader into a local file reader. And
// it must not accept a protocol merely because the string looks like one,
// because the value arrives from a plugin over RPC, which means it arrives from
// outside the trust boundary.
//
// A closed enum makes both errors impossible rather than merely unlikely: a
// `file://` locator cannot be represented, and ParseLocatorScheme refuses a
// string outside the set rather than passing it through for someone downstream
// to interpret.
//
// The seam matters more here than it looks. If the scheme were a bare string,
// "no scheme I recognise" and "a scheme I do recognise but spelled differently"
// would both be handled by the same fallthrough, and a downloader that falls
// through is the shape step 5.2's path sanitisation has to defend against.
type LocatorScheme string

const (
	// SchemeMagnet is a BitTorrent magnet link. Not a URL in the http sense:
	// it carries no host and no path, and its trackers are inside the value.
	SchemeMagnet LocatorScheme = "magnet"

	// SchemeTorrent is a URL to a .torrent METAINFO file. The file is a few
	// kilobytes of tracker and infohash information; the content is
	// elsewhere, and fetching this URL is not fetching the media.
	SchemeTorrent LocatorScheme = "torrent"

	// SchemeHTTP is a direct URL to the file itself.
	SchemeHTTP LocatorScheme = "http"

	// SchemeHTTPS is SchemeHTTP, over TLS. A separate constant rather than a
	// flag on SchemeHTTP, so that "the transport is encrypted" is a value a
	// caller can switch on and a UI can filter by, instead of a boolean that
	// can be set on a scheme that has none.
	SchemeHTTPS LocatorScheme = "https"

	// SchemeED2K is an ed2k link: `ed2k://|file|<name>|<size>|<hash>|/`. The
	// hash is MD4 of the file, which is a protocol property and not a
	// security property — worth knowing, because it means an ed2k locator
	// identifies content far more weakly than a magnet does.
	SchemeED2K LocatorScheme = "ed2k"
)

// locatorSchemes is the set, in one place, so the three things that need to
// agree cannot drift: the parser, the validator, and the UI's dropdown.
var locatorSchemes = []LocatorScheme{
	SchemeMagnet, SchemeTorrent, SchemeHTTP, SchemeHTTPS, SchemeED2K,
}

// LocatorSchemeNames lists the schemes as strings, for error messages and for
// the UI. Derived from the one table above rather than written out again.
func LocatorSchemeNames() []string {
	names := make([]string, 0, len(locatorSchemes))
	for _, s := range locatorSchemes {
		names = append(names, string(s))
	}
	return names
}

// IsKnown reports whether the scheme is one this build handles.
func (s LocatorScheme) IsKnown() bool {
	for _, known := range locatorSchemes {
		if s == known {
			return true
		}
	}
	return false
}

// ParseLocatorScheme classifies a locator string into a scheme.
//
// The ORDER of the checks is the security-relevant part, and it is deliberate:
//
//   - `file`, `ftp`, `gopher` and friends are refused, not merely unrecognised.
//     An unrecognised scheme is rejected anyway — but refusing the dangerous
//     ones by NAME means the error can say "that scheme would read a local
//     file", which is an answer an operator can act on, rather than "unknown
//     scheme", which is not.
//   - `magnet:` and `ed2k://` are checked before URL parsing, because neither is
//     a URL. `url.Parse` accepts `magnet:?xt=...` and hands back a
//     url.URL whose Path is the whole string, and a downloader that then
//     trusted that Path would be reconstructing a locator from a field that was
//     never one.
//   - Only then does it fall through to URL parsing, and the http/https check
//     is on the SCHEME the parser reports, not on a prefix, so
//     `httpx://example.com` is not mistaken for https.
func ParseLocatorScheme(raw string) (LocatorScheme, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("the locator is empty, so it has no scheme")
	}

	lower := strings.ToLower(value)

	// The schemes that are dangerous rather than merely unknown, named
	// explicitly so the refusal can be specific.
	for _, dangerous := range []struct{ prefix, effect string }{
		{"file:", "a locator with this scheme makes a downloader read a local file, " +
			"which turns fetching material into reading anything the user can read"},
		{"ftp:", "this build speaks only BitTorrent and HTTP"},
		{"gopher:", "gopher has been dead since 2009, and nothing in this project speaks it"},
	} {
		if strings.HasPrefix(lower, dangerous.prefix) {
			return "", fmt.Errorf("the locator's scheme is %q, which is refused: %s",
				dangerous.prefix[:len(dangerous.prefix)-1], dangerous.effect)
		}
	}

	// Non-URL schemes, checked first and by prefix, because url.Parse misreads
	// both of these.
	if strings.HasPrefix(lower, "magnet:") {
		return SchemeMagnet, nil
	}
	if strings.HasPrefix(lower, "ed2k://") {
		return SchemeED2K, nil
	}

	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("the locator does not parse as a URL: %w", err)
	}

	switch strings.ToLower(parsed.Scheme) {
	case "http":
		return SchemeHTTP, nil
	case "https":
		return SchemeHTTPS, nil
	}

	// An empty scheme is the case worth naming: a bare `example.com/file.torrent`
	// is what people paste, and silently treating it as https would be a
	// transport decision made by a guess.
	if parsed.Scheme == "" {
		return "", fmt.Errorf("the locator %q has no scheme. A bare host or path "+
			"is refused rather than assumed to be https, because guessing the "+
			"transport is how content gets fetched over the wrong one. Write the "+
			"scheme explicitly: https://, magnet:, or ed2k://", value)
	}

	return "", fmt.Errorf("the locator's scheme %q is not one this build handles. "+
		"It handles %s", parsed.Scheme, strings.Join(LocatorSchemeNames(), ", "))
}

// ValidateLocator checks a locator string and returns its scheme.
//
// The combined form, and it is the function a plugin should call rather than
// ParseLocatorScheme: validating a locator and classifying it are the same
// operation, and a caller that does the first with one function and infers the
// second from the string has two places to get it wrong instead of one.
//
// It returns the scheme rather than only an error, because the caller needs it
// to store the locator denormalised — and reading it back out of the string a
// second time is how the stored scheme and the parsed one come to disagree.
func ValidateLocator(raw string) (LocatorScheme, error) {
	scheme, err := ParseLocatorScheme(raw)
	if err != nil {
		return "", err
	}
	return scheme, nil
}
