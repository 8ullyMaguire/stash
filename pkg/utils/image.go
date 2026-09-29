package utils

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Timeout to get the image. Includes transfer time. May want to make this
// configurable at some point.
const imageGetTimeout = time.Second * 60

const base64RE = `^data:.+\/(.+);base64,(.*)$`

var base64Regex = regexp.MustCompile(base64RE)

// ProcessImageInput transforms an image string either from a base64 encoded
// string, or from a URL, and returns the image as a byte slice
//
// localResolver, when non-nil, serves URLs that point back at this instance
// without an HTTP round trip. See LocalImageResolver for why that is needed
// (#5538). Callers that have no repository access pass nil, and the behaviour
// is exactly as before.
func ProcessImageInput(ctx context.Context, imageInput string, localResolver LocalImageResolver) ([]byte, error) {
	if imageInput == "" {
		return []byte{}, nil
	}

	if base64Regex.MatchString(imageInput) {
		d, err := ProcessBase64Image(imageInput)
		return d, err
	}

	// A URL pointing back at this instance is served in-process. #5538.
	if localResolver != nil {
		if d, local, err := ReadLocalImage(imageInput, localResolver); local {
			if err != nil {
				return nil, err
			}
			if err := validateImageData(d); err != nil {
				return nil, err
			}
			return d, nil
		}
	}

	// assume input is a URL. Read it.
	d, err := ReadImageFromURL(ctx, imageInput)
	if err != nil {
		return nil, err
	}

	if err := validateImageData(d); err != nil {
		return nil, err
	}

	return d, nil
}

// validateImageData rejects HTML content, which is not a valid image and would
// execute as a document if served back to a browser. SVG (detected as XML or
// plain text) is still accepted and sandboxed on output by ServeImage.
func validateImageData(data []byte) error {
	if len(data) == 0 {
		return nil
	}

	contentType := http.DetectContentType(data)
	if strings.HasPrefix(contentType, "text/html") {
		return fmt.Errorf("unsupported image content type %q", contentType)
	}

	return nil
}

// localImageResolver serves image requests that point back at this instance,
// without going through the network. #5538.
//
// THE BUG IT FIXES. "Set image from URL" is commonly used to copy an image
// from one performer to another, and the URL to copy is the one Stash itself
// hands out: {baseURL}/performer/123/image. The backend then makes a plain
// HTTP GET for that URL -- and a plain GET carries no session cookie and no
// API key, so with authentication enabled it comes back 401.
//
// The 401 is the interesting part, because the failure is silent. The image
// routes fall back to a generated placeholder when the fetch fails or returns
// nothing, so the backend received a perfectly valid PNG of a blank performer
// and stored THAT as the new performer's image. With auth off the whole thing
// works, which is why the report reads as "it saves, then it malforms on
// reload": the save genuinely succeeded, with the wrong picture.
//
// WHY NOT JUST ATTACH AN API KEY TO THE OUTGOING REQUEST. Three reasons, in
// order of how hard they are to get wrong. (1) A key on an outbound request
// to a URL the user supplied is a credential leak the moment the URL is not
// actually Stash -- users paste URLs from anywhere. (2) Deciding whether a
// URL is "local" is the hard part: one instance is reachable as localhost, a
// LAN IP, a domain, a Tailscale address and through a reverse proxy, and a
// user may paste one today and another tomorrow. (3) An unconditional key
// would be the simplest version and is exactly what must not happen.
//
// WHAT THIS DOES INSTEAD. Recognise the image routes Stash serves itself --
// /performer/{id}/image, /studio/{id}/image, /tag/{id}/image,
// /scene/{id}/screenshot -- by PATH, and serve them in-process. No socket, no
// auth, no key, and the path is matched against a fixed set of routes so an
// arbitrary path is never treated as local.
//
// THE HOST IS DELIBERATELY IGNORED, and that is a trade-off worth stating
// rather than a detail.
//
// Ignoring it is what makes the feature work at all. One instance is reachable
// as localhost, a LAN IP, a domain, a Tailscale address and through a reverse
// proxy, and a user may paste one today and another tomorrow. Any host check
// either needs configuration (which base URL is "me"?) or is a guess, and a
// stale base URL silently reintroduces the 401, which is the bug being fixed.
//
// The cost is real, and it is a disclosure boundary rather than a
// code-execution one. A user who pastes
// https://anything.example/performer/123/image gets the LOCAL performer 123's
// image instead of a remote fetch. Nothing is returned that the user could not
// already read -- they are an authenticated operator of this instance, and
// performer images are served to them over HTTP anyway -- but it is not what
// the URL says, and an operator pasting an untrusted URL from a scraper or a
// browser extension should know that.
//
// The alternative, attaching a session cookie or API key to the outbound
// request, was rejected for the reason the maintainer gave on the issue: it
// sends a credential to a host chosen by whoever supplied the URL. That is a
// strictly worse failure than reading a local record, and it is exactly the
// failure a naive fix introduces.
//
// So: the path decides, the host does not, and the recognised set is four
// fixed routes with a bare integer id. Anything else still goes out over the
// network exactly as before.
type LocalImageResolver func(path string) ([]byte, error)

// localImagePaths are the Stash image routes, as a matcher over the URL path.
// Each entry is a literal prefix plus an {id} placeholder.
var localImagePaths = []string{
	"/performer/",
	"/studio/",
	"/tag/",
	"/scene/",
}

// localImageSuffixes are the image-bearing endpoints under those prefixes.
var localImageSuffixes = []string{
	"/image",
	"/screenshot",
}

// isLocalImagePath reports whether rawURL is one of Stash's own image
// endpoints, and if so returns the PATH with any base-URL prefix removed.
//
// IT TAKES A URL, NOT A PATH, and that distinction is the whole design. The
// caller passes whatever the user pasted, which is normally an absolute URL
// ({baseURL}/performer/123/image) and sometimes a bare path. Parsing the URL
// rather than matching raw text is what makes the host irrelevant: a user may
// reach this instance as localhost, a LAN IP, a domain, a Tailscale address or
// through a reverse proxy, and a text match on the whole URL would have to
// enumerate every one of them. url.Parse gets the path for free, and
// "is this us?" becomes a question about the PATH alone -- which is also
// strictly safer, because a hostile host cannot smuggle a path past it.
//
// A URL that does not parse is not a stash image. A URL with no host and no
// path (a bare "performer/123/image") is rejected too, since the leading slash
// is what distinguishes a path from a relative segment.
func isLocalImagePath(rawURL string) (string, bool) {
	if rawURL == "" {
		return "", false
	}

	// Parse first, so that "https://evil.example/performer/1/image" and
	// "/performer/1/image" reduce to the same path and the host is discarded.
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", false
	}

	// Only http and https are fetched over the network elsewhere, so only they
	// can be a stash image URL. This also rejects data: URIs, file: paths and
	// javascript: URLs before they reach the path logic.
	switch u.Scheme {
	case "", "http", "https":
	default:
		return "", false
	}

	path := u.Path
	if path == "" || !strings.HasPrefix(path, "/") {
		return "", false
	}

	// A query or fragment on a stash image URL is not something the image
	// routes accept or that a URL builder emits. The one query the routes do
	// use is ?default=true, and that returns a generated placeholder, which is
	// not what a user means by "copy this performer's image" -- so reject
	// rather than silently resolve to the placeholder.
	if u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}

	// Strip a leading subpath, if any, by locating the LAST position at which
	// a known route prefix begins. Searching rather than trimming a configured
	// base means it works with no configuration at all, which is the point:
	// there is nothing for the operator to get wrong, and no setting that can
	// be left stale. LastIndex rather than Index so that a path which merely
	// CONTAINS a route name later on -- /performer/1/image/performer/2/image
	// -- resolves on the final real route rather than an earlier one.
	matched, rest := "", ""
	for _, prefix := range localImagePaths {
		i := strings.LastIndex(path, prefix)
		if i < 0 {
			continue
		}
		candidate := path[i:]
		if len(candidate) > len(rest) {
			matched, rest = prefix, candidate
		}
	}
	if rest == "" {
		return "", false
	}

	for _, suffix := range localImageSuffixes {
		if !strings.HasSuffix(rest, suffix) {
			continue
		}

		// The id is what sits between the prefix and the suffix, and it must
		// be a bare positive integer: /performer/123/image. Anything else --
		// another slash, a dot, letters, a query or fragment -- is not a Stash
		// image route and must not be resolved internally. This is the check
		// that stops /performer/../../etc/passwd/image or /performer/1;drop/image
		// from being treated as local, and it is deliberately stricter than
		// the routes themselves would accept.
		//
		// The length test comes first, and it is not decoration. For
		// /performer/image no prefix occurs at all -- the string ends in
		// /image but has no /performer/ -- so slicing out the id would index
		// past the start and panic. Return false rather than continue: a path
		// that already ends in a known suffix but does not carry a
		// prefix+id+suffix shape is not a Stash image route, and trying the
		// remaining suffixes cannot make it one.
		if len(rest) <= len(matched)+len(suffix) {
			return "", false
		}

		id := rest[len(matched) : len(rest)-len(suffix)]
		if id == "" {
			return "", false
		}
		for _, c := range id {
			if c < '0' || c > '9' {
				return "", false
			}
		}

		// A leading zero is legal in a path but is not how a URL builder emits
		// an id, so reject rather than normalise: /performer/007/image must
		// not silently resolve to the same record as /performer/7/image.
		if len(id) > 1 && id[0] == '0' {
			return "", false
		}

		return rest, true
	}

	return "", false
}

// ReadLocalImage returns image data for a URL that points back at this
// instance, without an HTTP round trip. The second return value is false when
// the URL is not a Stash image route, in which case the caller must fall back
// to a real HTTP request.
func ReadLocalImage(rawURL string, resolve LocalImageResolver) ([]byte, bool, error) {
	path, ok := isLocalImagePath(rawURL)
	if !ok {
		return nil, false, nil
	}

	data, err := resolve(path)
	if err != nil {
		return nil, true, err
	}
	return data, true, nil
}

// ReadImageFromURL returns image data from a URL
func ReadImageFromURL(ctx context.Context, url string) ([]byte, error) {
	client := &http.Client{
		Transport: &http.Transport{ // ignore insecure certificates
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			Proxy:           http.ProxyFromEnvironment,
		},

		Timeout: imageGetTimeout,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	// assume is a URL for now

	// set the host of the URL as the referer
	if req.URL.Scheme != "" {
		req.Header.Set("Referer", req.URL.Scheme+"://"+req.Host+"/")
	}
	req.Header.Set("User-Agent", getUserAgent())

	resp, err := client.Do(req)

	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("http error %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return body, nil
}

// ProcessBase64Image transforms a base64 encoded string from a form post and
// returns the image itself as a byte slice.
func ProcessBase64Image(imageString string) ([]byte, error) {
	if imageString == "" {
		return nil, fmt.Errorf("empty image string")
	}

	matches := base64Regex.FindStringSubmatch(imageString)
	var encodedString string
	if len(matches) > 2 {
		encodedString = matches[2]
	} else {
		encodedString = imageString
	}
	imageData, err := GetDataFromBase64String(encodedString)
	if err != nil {
		return nil, err
	}

	if err := validateImageData(imageData); err != nil {
		return nil, err
	}

	return imageData, nil
}

// GetDataFromBase64String returns the given base64 encoded string as a byte slice
func GetDataFromBase64String(encodedString string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(encodedString)
}

// GetBase64StringFromData returns the given byte slice as a base64 encoded string
func GetBase64StringFromData(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

func ServeImage(w http.ResponseWriter, r *http.Request, image []byte) {
	contentType := http.DetectContentType(image)

	// SVG images are detected as XML or plain text; serve them as SVG so they
	// render. The sandboxing CSP below prevents any embedded script running.
	if contentType == "text/xml; charset=utf-8" || contentType == "text/plain; charset=utf-8" {
		contentType = "image/svg+xml"
	} else if strings.HasPrefix(contentType, "text/") {
		// any other text type (e.g. HTML) is not a valid image - never render it
		contentType = "application/octet-stream"
		w.Header().Set("Content-Disposition", "attachment")
	}

	// sandbox every image response so a stored SVG cannot execute script or
	// exfiltrate data; harmless for raster images.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src data:; style-src 'unsafe-inline'; sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	w.Header().Set("Content-Type", contentType)
	ServeStaticContent(w, r, image)
}
