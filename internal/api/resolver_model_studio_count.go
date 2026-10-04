package api

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/txn"
)

// Per-request batching for the studio count resolvers.
//
// WHY NOT A DATALOADER
// ====================
// The obvious tool is a dataloader, and this tree has them (internal/api/loaders). It is the wrong
// one here, for a reason that only shows up once you read the client: gqlgen hands a field resolver
// one object at a time, so a dataloader batches by deferring the fetch until its tick (1ms in
// dataloaders.go:40). That works. The problem is the CACHE KEY.
//
// ui/v2.5/graphql/data/studio.graphql asks for every count twice:
//
//	scene_count
//	scene_count_all: scene_count(depth: -1)
//
// so within one request the same resolver is called at depth 0 and depth -1. A dataloader keyed on
// studio ID alone would answer the second call from the first call's cache and return the flat count
// for the recursive field -- fast, plausible, and wrong, on exactly the field the UI uses when
// "show child studio content" is on.
//
// Keying the loader on (studioID, depth) instead would fix that, but then the loader batches across
// studios only within one depth: the 12 depth=-1 walks still collapse to 1, which is the whole win.
// So the key is a struct here rather than an int, for the same reason.

// studioCountKey identifies one batched count for a page of studios at a specific depth.
//
// depth is part of the identity, not a parameter, because depth 0 and depth -1 are different
// answers to the same field. Negative is stored as itself rather than normalised to a sentinel,
// because -1 (unbounded) and -2 would mean the same thing but normalising invites the mistake of
// treating them as distinct when they are not.
type studioCountKey struct {
	depth int
}

// studioCountKind names which count is being fetched. A single int per kind would do, but the
// generated loaders in this tree name each one separately and matching that shape keeps the two
// mechanisms readable side by side.
type studioCountKind int

const (
	studioSceneCountKind studioCountKind = iota
	studioImageCountKind
	studioGalleryCountKind
	studioGroupCountKind
	studioPerformerCountKind
	studioSceneMarkerCountKind
)

// studioCountBatcher accumulates the studios a request asked about and answers them in one query per
// (kind, depth).
//
// A resolver call cannot know how many objects are coming -- gqlgen calls it once per studio, and the
// last call is indistinguishable from the others. So the batcher is filled as calls arrive and read
// on demand: each count resolver asks for its own value, and the batcher runs the query for the
// whole page the first time it is asked and serves every later call from the result.
//
// The trade is a first-call query that returns counts for studios the resolver has not reached yet.
// That is safe because the ids come from the page query itself, not from the resolver arguments.
//
// withDB is required rather than optional: pkg/sqlite/tx.go:50 dbWrapper resolves the transaction off
// the context and errors with "not in transaction" without one. The repository interface does not
// expose WithDB (only models.Repository does), so the caller supplies it -- the same way
// loaders.Middleware does at dataloaders.go:258.
type studioCountBatcher struct {
	mu sync.Mutex
	// cond wakes goroutines waiting on an in-flight fetch. gqlgen resolves list fields in parallel, so
	// the wait path is the common one, not an edge case.
	cond *sync.Cond
	// ids is the page's studio ids in page order, recorded on first use.
	ids []int
	// idIndex maps a studio id to its position in ids.
	idIndex map[int]int
	// results[key] is the completed answer for one (kind, depth).
	results map[studioCountKind]map[studioCountKey][]int
	// inFlight marks which (kind, depth) a goroutine is currently fetching.
	inFlight map[studioCountKind]map[studioCountKey]bool

	repo   models.StudioReaderWriter
	withDB func(ctx context.Context, fn txn.TxnFunc) error
}

func newStudioCountBatcher(repo models.StudioReaderWriter, withDB func(ctx context.Context, fn txn.TxnFunc) error) *studioCountBatcher {
	b := &studioCountBatcher{
		results:  make(map[studioCountKind]map[studioCountKey][]int),
		inFlight: make(map[studioCountKind]map[studioCountKey]bool),
		repo:     repo,
		withDB:   withDB,
	}
	b.cond = sync.NewCond(&b.mu)
	return b
}

// setPage records the ids for this request. Called once by the list resolver, which is the only place
// that knows the full page.
func (b *studioCountBatcher) setPage(ids []int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ids = ids
	b.idIndex = make(map[int]int, len(ids))
	for i, id := range ids {
		b.idIndex[id] = i
	}
}

// count returns the count for one studio at one depth, running the batched query if needed.
func (b *studioCountBatcher) count(ctx context.Context, kind studioCountKind, studioID int, depth *int) (int, error) {
	key := studioCountKey{depth: depthValue(depth)}

	// Resolve first, then decide whether to fetch. Holding the lock across a database call would
	// serialise the whole page; reading under the lock and releasing before the query lets the other
	// resolvers proceed to their own lookups.
	b.mu.Lock()
	if byKey, ok := b.results[kind]; ok {
		if counts, ok := byKey[key]; ok {
			if idx, ok := b.idIndex[studioID]; ok && idx < len(counts) {
				b.mu.Unlock()
				return counts[idx], nil
			}
		}
	}
	ids := b.ids
	b.mu.Unlock()

	if len(ids) == 0 {
		// No page recorded (a single-object query, or a resolver reached from somewhere other than
		// the studio list). Fall back to the per-studio path rather than guessing.
		return b.perStudio(ctx, kind, studioID, depth)
	}

	if err := b.fetch(ctx, kind, key, ids, depth); err != nil {
		return 0, err
	}

	b.mu.Lock()
	counts, haveResult := b.results[kind][key]
	idx, inPage := b.idIndex[studioID]
	b.mu.Unlock()

	if haveResult && inPage && idx < len(counts) {
		return counts[idx], nil
	}
	// The studio was not in the recorded page. Returning 0 would be a lie; the per-studio path is
	// correct if slower, and this only happens for a resolver reached with an unexpected id.
	//
	// The lock is released before this call on purpose: perStudio opens a transaction, and holding
	// b.mu across a database round trip would serialise every other resolver waiting on the same page.
	return b.perStudio(ctx, kind, studioID, depth)
}

// fetch runs the batched query for one (kind, depth), or waits for the goroutine already running it.
//
// gqlgen resolves the fields of a list in PARALLEL, so all N studios' SceneCount calls arrive at once.
// Returning an error on the second arrival (as this first did) fails the request outright; the correct
// behaviour is to wait for the in-flight fetch and then read the shared result. That is what the
// condition variable is for.
//
// Waiting is safe here precisely because the page is already known: the query covers every studio, so
// there is no need for the late arrivals to contribute ids.
func (b *studioCountBatcher) fetch(ctx context.Context, kind studioCountKind, key studioCountKey, ids []int, depth *int) error {
	b.mu.Lock()
	for {
		if _, done := b.results[kind][key]; done {
			b.mu.Unlock()
			return nil
		}
		if b.inFlight[kind] == nil {
			b.inFlight[kind] = make(map[studioCountKey]bool)
		}
		if !b.inFlight[kind][key] {
			b.inFlight[kind][key] = true
			break
		}
		// Someone else is running this exact query. Wait for them rather than issuing a duplicate.
		b.cond.Wait()
	}
	b.mu.Unlock()

	counts, err := b.runQuery(ctx, kind, ids, depth)

	b.mu.Lock()
	if err == nil {
		if b.results[kind] == nil {
			b.results[kind] = make(map[studioCountKey][]int)
		}
		b.results[kind][key] = counts
	}
	delete(b.inFlight[kind], key)
	// Wake every waiter: the success case so they can read the result, the error case so they do not
	// wait for a fetch that will never produce anything.
	b.cond.Broadcast()
	b.mu.Unlock()

	return err
}

// runQuery issues the batched query inside a transaction.
//
// The transaction is required, not decorative: pkg/sqlite/tx.go:50 resolves it off the context and
// fails with "not in transaction" without one. This is the same shape loaders.Middleware uses
// (dataloaders.go:258).
func (b *studioCountBatcher) runQuery(ctx context.Context, kind studioCountKind, ids []int, depth *int) ([]int, error) {
	var counts []int
	if err := b.withDB(ctx, func(ctx context.Context) error {
		var err error
		counts, err = b.runBatched(ctx, kind, ids, depth)
		return err
	}); err != nil {
		return nil, err
	}
	return counts, nil
}

// runBatched issues the one query that covers the whole page for a single count kind and depth.
func (b *studioCountBatcher) runBatched(ctx context.Context, kind studioCountKind, ids []int, depth *int) ([]int, error) {
	switch kind {
	case studioSceneCountKind:
		return b.repo.GetManySceneCount(ctx, ids, depth)
	case studioImageCountKind:
		return b.repo.GetManyImageCount(ctx, ids, depth)
	case studioGalleryCountKind:
		return b.repo.GetManyGalleryCount(ctx, ids, depth)
	case studioGroupCountKind:
		return b.repo.GetManyGroupCount(ctx, ids, depth)
	case studioPerformerCountKind:
		return b.repo.GetManyPerformerCount(ctx, ids, depth)
	case studioSceneMarkerCountKind:
		return b.repo.GetManySceneMarkerCount(ctx, ids, depth)
	default:
		return nil, fmt.Errorf("unknown studio count kind %d", kind)
	}
}

// perStudio is the unbatched fallback, used when no page has been recorded.
//
// It calls the repository's single-id path through the same batched interface by handing it a
// one-element slice, so there is exactly one SQL implementation of a count and therefore no way for
// the fast and slow paths to drift apart.
func (b *studioCountBatcher) perStudio(ctx context.Context, kind studioCountKind, studioID int, depth *int) (int, error) {
	counts, err := b.runBatched(ctx, kind, []int{studioID}, depth)
	if err != nil {
		return 0, err
	}
	if len(counts) == 0 {
		return 0, nil
	}
	return counts[0], nil
}

// studioCountMiddleware attaches a fresh per-request studio count batcher.
//
// It is deliberately a separate mechanism from the dataloaders in loaders/dataloaders.go, even though
// both are request-scoped batching. The dataloaders batch on a tick (wait = 1ms,
// dataloaders.go:40); this one does not, because a count resolver cannot wait to find out whether more
// studios are coming -- gqlgen gives it no signal that the page is finished. It reads the page that
// FindStudios already recorded, which is why this middleware only has to CREATE the batcher: the page
// itself is seeded by the list resolver.
//
// repo is models.Repository rather than the Studio sub-reader because WithDB lives on the parent; the
// same is true of the dataloaders, which take a Middleware carrying the whole repository.
func studioCountMiddleware(repo models.Repository) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := withStudioCountBatcher(r.Context(), repo)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// depthValue turns the GraphQL `depth: Int` argument into the plain int the repository wants.
// A missing argument is depth 0, which is the studio itself only.
func depthValue(depth *int) int {
	if depth == nil {
		return 0
	}
	return *depth
}

type studioCountContextKey struct{}

// withStudioCountBatcher attaches a batcher for the lifetime of one request.
func withStudioCountBatcher(ctx context.Context, repo models.Repository) context.Context {
	return context.WithValue(
		ctx,
		studioCountContextKey{},
		newStudioCountBatcher(repo.Studio, repo.WithDB),
	)
}

func studioCountBatcherFrom(ctx context.Context) (*studioCountBatcher, bool) {
	b, ok := ctx.Value(studioCountContextKey{}).(*studioCountBatcher)
	return b, ok
}
