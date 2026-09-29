package scraper

import (
	"context"
	"fmt"
	"net/http"

	"github.com/stashapp/stash/pkg/utils"
)

// getImage fetches an image, retrying with different Referer strategies when
// the server refuses. The ladder itself lives in pkg/utils, shared with
// ReadImageFromURL, so both outbound image paths behave identically. See
// utils.DoWithRefererLadder for why it is ordered the way it is.
func (i *imageGetter) getImage(ctx context.Context, imageURL string) (*string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, err
	}

	userAgent := i.globalConfig.GetScraperUserAgent()
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}

	// Applied once, before the ladder. The ladder reuses this request and only
	// re-sets the Referer, so a scraper's headers are identical on every
	// attempt -- and stash's own scraper authenticates, which would fail on
	// attempts two and three if the modifier ran per attempt and the header
	// were rebuilt from scratch.
	if i.requestModifier != nil {
		i.requestModifier(req)
	}

	resp, body, err := utils.DoWithRefererLadder(ctx, i.client, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = http.DetectContentType(body)
	}

	// A 200 that is not an image is a real failure and the caller should hear
	// about it. A 200 with an HTML body is what a server sends when it is
	// quietly refusing to serve the file, and storing that as the performer
	// picture reproduces the stash#5538 failure mode: a save that succeeds with
	// a broken image and no error anywhere.
	if !utils.IsImageContentType(contentType) {
		return nil, fmt.Errorf("expected an image from %s, got content-type %q", imageURL, contentType)
	}

	encoded := "data:" + contentType + ";base64," + utils.GetBase64StringFromData(body)
	return &encoded, nil
}
