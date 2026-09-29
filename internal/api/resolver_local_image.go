package api

// Resolution of image URLs that point back at this instance. #5538.
//
// The rule package provides is narrow -- recognise the four image routes
// Stash serves, and hand back their bytes without an HTTP request. Everything
// about knowing WHICH record a path refers to belongs here, where the
// repository is reachable, and nothing about it leaks into package utils.
//
// A transaction is opened per lookup rather than passed in, because the
// callers run outside any transaction: these resolvers are invoked from
// ProcessImageInput while building a mutation input, before the mutation's own
// withTxn begins. Opening a short read transaction per image is correct here
// and cheap -- these are point reads by primary key.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/stashapp/stash/pkg/utils"
)

// localImageResolverFor returns a resolver bound to this resolver's
// repository. The returned func is what package utils calls.
func (r *mutationResolver) localImageResolverFor(ctx context.Context) utils.LocalImageResolver {
	return func(path string) ([]byte, error) {
		return r.readLocalImage(ctx, path)
	}
}

// readLocalImage serves one recognised image path. The path has already been
// validated by the matcher in package utils -- it is guaranteed to be
// /{kind}/{id}/{image|screenshot} with {id} a bare positive integer -- so the
// parsing here cannot fail on anything the matcher would have rejected. The
// strconv error is still handled rather than ignored, because a panic here
// would take down a mutation and a wrong-kind path must not resolve to the
// wrong record.
func (r *mutationResolver) readLocalImage(ctx context.Context, path string) ([]byte, error) {
	kind, id, ok := splitLocalImagePath(path)
	if !ok {
		return nil, fmt.Errorf("not a stash image path: %q", path)
	}

	// The image is read in its own read transaction: the caller may be
	// holding none, and nesting a read inside a write transaction on the same
	// connection is how SQLite deadlocks.
	var data []byte
	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		var err error
		switch kind {
		case "performer":
			data, err = r.repository.Performer.GetImage(ctx, id)
		case "studio":
			data, err = r.repository.Studio.GetImage(ctx, id)
		case "tag":
			data, err = r.repository.Tag.GetImage(ctx, id)
		case "scene":
			// A scene's image endpoint is its screenshot, but the screenshot
			// route is filesystem-backed and falls back to a generated cover.
			// The cover in the database is the part that is a real record --
			// it is what the cover was explicitly set to -- and it is what
			// the route serves when the scene has one, so it is the faithful
			// in-process equivalent. Scenes with no explicit cover fall back
			// to the generated placeholder, which is what the route does too.
			data, err = r.repository.Scene.GetCover(ctx, id)
		default:
			err = fmt.Errorf("unknown image kind %q", kind)
		}
		return err
	}); err != nil {
		return nil, err
	}

	return data, nil
}

// splitLocalImagePath splits a validated image path into its kind and id.
func splitLocalImagePath(path string) (kind string, id int, ok bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 3 {
		return "", 0, false
	}

	// The id is a bare positive integer, guaranteed by the matcher. A parse
	// failure here would mean the matcher and this function disagree, which
	// is a bug rather than bad input, so it is reported as such.
	n, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", 0, false
	}
	if n <= 0 {
		return "", 0, false
	}

	return parts[0], n, true
}
