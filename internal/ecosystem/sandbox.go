package ecosystem

import (
	"context"
	"fmt"
)

// R058 — "Developer sandbox with sample data" — and §6a.18, "Ecosystem".
//
// WHY THIS IS SAMPLE DATA AND NOT A RUNNING INSTANCE
//
// R058 depends_on R054 (public API, SDKs, webhooks), which is not built. A "sandbox" in the
// sense of a live instance with a REST endpoint cannot exist yet because there is no REST
// surface to point a client at. So what is built here is the half that does not depend on the
// transport: a fixed, synthetic corpus that satisfies the SDK boundary R054's clients will be
// written against, plus the guarantees that make the data safe to hand to a third party.
//
// The guarantees are the substance. A list of fake scenes is a fixture; what a developer needs
// is to be able to TRUST the thing, and every guarantee below exists because its absence is a
// concrete way a developer's own library gets corrupted:
//
//   - a sample indistinguishable from real data ships into production
//   - a sample carrying consent state is mistaken for a real consent decision (§6a.2)
//   - an unbounded or unstable sample set makes every position-dependent test intermittent
//   - an un-namespaced sample id collides with a real entity on import

const (
	// SandboxName identifies the corpus wherever it is shown.
	SandboxName = "stashforge-sample-data"

	// SandboxSyntheticPrefix marks synthetic content in a human-visible field. It is a PREFIX
	// on the title rather than a flag alone because the title is what a developer sees in their
	// own UI after importing, and that is the last place the warning can usefully live.
	SandboxSyntheticPrefix = "[SAMPLE] "

	// SandboxIDSamples namespaces sample ids away from real ones.
	//
	// THE COLLISION THIS PREVENTS: a developer imports sample data into a library that already
	// has a performer with id 42, then imports real data containing a different performer with
	// id 42. Without the prefix the two are the same row as far as any id-based client is
	// concerned. R060's canonical URLs and any REST resource path would collide identically,
	// which is why the prefix also has to be a single path segment.
	SandboxIDSamples = "sample-"
)

// SyntheticEntity is one sample entity.
//
// It is deliberately NOT PublicEntity: a sample is not a public entity, and making it one would
// mean either carrying fields a sample has no business having (a consent state) or leaving a
// hole where a caller expects them.
type SyntheticEntity struct {
	// PublicID is namespaced with SandboxIDSamples.
	PublicID string
	Title    string

	// Synthetic is true on every entity the sandbox produces. It exists as a FIELD rather than
	// only as the title prefix because a client that drops titles would otherwise lose the
	// marker entirely.
	Synthetic bool

	// Share is deliberately EMPTY and is documented as such. It is present only so a caller
	// reading a sample can see that consent is absent rather than missing by oversight; nothing
	// sets it.
	Share string

	// Published is always false. §6a.21's gate must not be satisfiable by sample data.
	Published bool
}

// SampleSet is the corpus.
//
// Count exists alongside Entities rather than being len(Entities) because the clamp in Query has
// to be testable at a size larger than MaxPublicLimit, and the real corpus is deliberately small
// enough to fit in one page. Without Count the only way to observe the clamp is a fixture that
// reimplements Query -- which would test the fixture instead of the code.
type SampleSet struct {
	Name      string
	Entities  []SyntheticEntity
	Synthetic bool

	// Count is how many entities the corpus conceptually holds. Zero means "however many are in
	// Entities". A corpus larger than MaxPublicLimit is synthesised on demand.
	Count int
}

// Sandbox is the sample-data source, and satisfies SDK so a client written against R054's
// boundary can be pointed at it before R054's transport exists.
//
// THE WRITE PATH IS ABSENT BY CONSTRUCTION. There is no method that mutates anything and no
// field that could be reached to do so: a sample-data source that can write is a second way to
// create an entity that never passed through governance (§6a.19), and the guarantee is worth a
// test that fails when a method is added.
type Sandbox struct {
	// count is the corpus size, 0 meaning the default three. It is unexported and set only by
	// the tests, because a corpus that varies with caller configuration is not a corpus -- see
	// the stability test. It exists so the clamp in Query is exercisable at a size that exceeds
	// MaxPublicLimit without a second implementation of Query standing in for it.
	count int
}

// NewSandbox returns the sandbox with the default corpus.
func NewSandbox() *Sandbox { return &Sandbox{} }

// withCount returns a sandbox whose corpus holds n entities, for testing the bound.
func (s *Sandbox) withCount(n int) *Sandbox { return &Sandbox{count: n} }

// Sample returns the corpus.
//
// A FIXED list, built fresh each call rather than cached as a package variable, so a caller
// cannot mutate the shared slice and affect every other caller. The CONTENT is identical every
// time; only the slice is fresh.
func (s *Sandbox) Sample() SampleSet {
	titles := []string{
		"Example Scene One",
		"Example Scene Two",
		"Example Scene Three",
	}
	n := s.count
	if n == 0 {
		n = len(titles)
	}
	out := SampleSet{
		Name:      SandboxName,
		Synthetic: true,
		Count:     n,
		Entities:  make([]SyntheticEntity, 0, n),
	}
	for i := 0; i < n; i++ {
		// Beyond the named three, the index is appended, so a corpus of any size still has
		// DISTINCT titles -- a corpus of identical rows would let a count-only test pass.
		title := "Example Scene"
		if i < len(titles) {
			title = titles[i]
		} else {
			title = fmt.Sprintf("%s %d", title, i)
		}
		out.Entities = append(out.Entities, SyntheticEntity{
			// The id is derived from the position, which keeps it stable across calls AND
			// obviously not a real database id -- no real performer gets "sample-0".
			PublicID:  fmt.Sprintf("%s%d", SandboxIDSamples, i),
			Title:     SandboxSyntheticPrefix + title,
			Synthetic: true,
			// Share stays empty and Published stays false. See the type comments.
		})
	}
	return out
}

// Query satisfies SDK.
//
// It applies the SAME bound as any public read, rather than returning the whole corpus: a
// sample source that ignores MaxPublicLimit teaches a developer that the cap is not real, and
// the cap is one of the few things an SDK client most needs to get right on its first try.
func (s *Sandbox) Query(ctx context.Context, q PublicQuery) ([]PublicEntity, error) {
	bounded := q.bounded()

	all := s.Sample()
	if bounded.Offset >= len(all.Entities) {
		return []PublicEntity{}, nil
	}
	end := bounded.Offset + bounded.Limit
	if end > len(all.Entities) {
		end = len(all.Entities)
	}

	out := make([]PublicEntity, 0, end-bounded.Offset)
	for _, e := range all.Entities[bounded.Offset:end] {
		// Published is false on every one, and Origin names the corpus rather than an instance:
		// sample data came from nowhere, and attributing it to an instance would make §6a.6's
		// (instance, local) pair point at something that does not exist.
		out = append(out, PublicEntity{
			PublicID:  e.PublicID,
			Title:     e.Title,
			Published: false,
			Origin:    SandboxName,
		})
	}
	return out, nil
}
