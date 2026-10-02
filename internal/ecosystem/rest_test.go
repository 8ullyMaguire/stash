package ecosystem

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// R054's REST surface. The substance is not the routing -- it is that this surface does NO
// QUERYING, so the consent rule and the read cap exist in exactly one place. These tests pin
// that, plus the three things a public surface gets wrong: unbounded reads, consent applied
// after the read, and errors that leak which entities exist.

// countingSDK records what it was asked for, so a test can prove the REST surface passed a
// BOUNDED query through rather than fetching everything and paginating in memory.
type countingSDK struct {
	got      []PublicQuery
	entities []PublicEntity
	err      error
}

func (c *countingSDK) Query(ctx context.Context, q PublicQuery) ([]PublicEntity, error) {
	c.got = append(c.got, q)
	if c.err != nil {
		return nil, c.err
	}
	// A well-behaved SDK honours the bound; one that does not is caught by the clamp test below.
	if q.Offset >= len(c.entities) {
		return []PublicEntity{}, nil
	}
	end := q.Offset + q.Limit
	if end > len(c.entities) {
		end = len(c.entities)
	}
	return c.entities[q.Offset:end], nil
}

func sampleSDK(n int) *countingSDK {
	c := &countingSDK{}
	for i := 0; i < n; i++ {
		c.entities = append(c.entities, PublicEntity{
			PublicID:  "e" + string(rune('a'+i%26)) + string(rune('a'+i/26)),
			Title:     "Entity " + string(rune('A'+i%26)),
			Published: true,
			Origin:    "peer-a",
		})
	}
	return c
}

// THE POSITIVE CONTROL. A surface that returned nothing would pass every refusal test.
func TestTheRESTSurfaceReturnsAPage(t *testing.T) {
	sdk := sampleSDK(3)
	api, err := NewPublicREST(sdk, "https://stash.example/api/v1")
	require.NoError(t, err)

	got, err := api.Get(t.Context(), "limit=3")
	require.NoError(t, err)
	require.Len(t, got.Entities, 3, "a public read must return something")
	assert.Equal(t, 3, got.Total)
	assert.Equal(t, "https://stash.example/api/v1/entities/"+got.Entities[0].PublicID, got.Entities[0].Self,
		"and every entity must carry a canonical URL built from the base")
}

// §6a.16 / MaxPublicLimit: the bound is applied on the way IN, from a URL the caller controls.
// Asking for more than the cap is a CLIENT ERROR rather than a silent clamp, because a silent
// clamp looks like the server ignoring the request and the caller cannot tell to paginate.
func TestTheRESTSurfaceRefusesAReadBeyondTheCap(t *testing.T) {
	api, err := NewPublicREST(sampleSDK(5), "https://stash.example/api/v1")
	require.NoError(t, err)

	_, err = api.Get(t.Context(), "limit="+strconv.Itoa(MaxPublicLimit+1))
	require.Error(t, err, "a limit above the cap must be refused, not silently clamped: the "+
		"caller has to be able to see the cap to paginate correctly")
	assert.Contains(t, err.Error(), strconv.Itoa(MaxPublicLimit), "and the error must NAME the cap")

	// Exactly the cap is fine -- the boundary is inclusive.
	_, err = api.Get(t.Context(), "limit="+strconv.Itoa(MaxPublicLimit))
	assert.NoError(t, err, "the cap itself is a legal request")
}

// The bound is passed THROUGH, not applied after fetching. A surface that fetched everything and
// paginated in memory would be correct and unbounded, which is the failure §6a.16 names.
func TestTheBoundIsPushedIntoTheQueryNotAppliedAfterwards(t *testing.T) {
	sdk := sampleSDK(50)
	api, err := NewPublicREST(sdk, "https://stash.example/api/v1")
	require.NoError(t, err)

	_, err = api.Get(t.Context(), "limit=2&offset=5")
	require.NoError(t, err)

	require.Len(t, sdk.got, 1, "exactly one read")
	assert.Equal(t, 2, sdk.got[0].Limit, "the limit must reach the SDK, so the read itself is bounded")
	assert.Equal(t, 5, sdk.got[0].Offset, "and so must the offset")
}

// A malformed limit is a client error that says so.
func TestAMalformedQueryIsAClientError(t *testing.T) {
	api, err := NewPublicREST(sampleSDK(3), "https://stash.example/api/v1")
	require.NoError(t, err)

	for _, tc := range []struct{ name, q string }{
		{"limit not a number", "limit=lots"},
		{"limit zero", "limit=0"},
		{"limit negative", "limit=-5"},
		{"offset not a number", "offset=soon"},
		{"offset negative", "offset=-1"},
		{"origin with a slash", "origin=peer-a/../peer-b"},
		{"origin with a query char", "origin=peer-a?x=1"},
		{"origin with a fragment", "origin=peer-a#f"},
		{"unparseable query string", "%zz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := api.Get(t.Context(), tc.q)
			assert.Error(t, err)
		})
	}
	// The positive control, so the loop cannot pass as blanket rejection.
	_, err = api.Get(t.Context(), "limit=2&offset=0&origin=peer-a")
	assert.NoError(t, err)
}

// THE DISCLOSURE LEAK, which is the subtlest of the three. An error message that names an
// entity -- or that says "opted out" rather than "not found" -- tells a caller the entity EXISTS.
// §6a.21: an opted-out entity is "neither indexed nor reachable".
func TestAnErrorNeverRevealsWhetherAnEntityExists(t *testing.T) {
	// The SDK fails with a message naming the entity AND its share state -- exactly what a
	// storage-layer error would say.
	leaky := errors.New(`entity "scene-1" is opted-out (metadata_share=opted-out)`)
	sdk := &countingSDK{err: leaky}

	api, err := NewPublicREST(sdk, "https://stash.example/api/v1")
	require.NoError(t, err)

	_, err = api.Get(t.Context(), "limit=5")
	require.Error(t, err, "the read failed, so the surface must fail")

	msg := err.Error()
	assert.NotContains(t, msg, "scene-1", "the error must not name the entity; naming it "+
		"confirms it exists, which is the disclosure 6a.21 exists to prevent")
	assert.NotContains(t, msg, "opted-out", "and must not carry the share state; 'opted out' "+
		"versus 'not found' tells a caller which of the two it is")
	assert.NotContains(t, msg, "metadata_share", "nor the internal field name")

	// And it is the SAME error the surface gives for anything else invisible, so a caller
	// cannot distinguish the cases.
	assert.Equal(t, NotVisible().Error(), err.Error(),
		"not-found and not-permitted must be one answer, or their difference is the disclosure")
}

// A not-found and a not-permitted being the same answer is only meaningful if both exist as
// states. This pins the intent at the boundary the transport will map.
func TestNotVisibleIsOneAnswerForEveryAbsence(t *testing.T) {
	assert.Equal(t, NotVisible().Error(), NotVisible().Error(),
		"the not-visible answer is a single value, so a transport maps it to one status")
	assert.NotEmpty(t, NotVisible().Error(), "and it says something a client can act on")
}

// A public id is a PATH SEGMENT. §6a.6's id is an (instance, local) pair, and an unescaped id
// containing a slash addresses a different resource -- on a public surface, a way to read
// something the caller did not ask for.
func TestEntityURLsEscapeThePublicID(t *testing.T) {
	sdk := &countingSDK{entities: []PublicEntity{
		{PublicID: "a/b", Title: "traversal"},
		{PublicID: "..", Title: "dotdot"},
		{PublicID: "x y", Title: "space"},
		{PublicID: "pct%2F", Title: "already encoded"},
	}}
	api, err := NewPublicREST(sdk, "https://stash.example/api/v1")
	require.NoError(t, err)

	got, err := api.Get(t.Context(), "limit=10")
	require.NoError(t, err)
	require.Len(t, got.Entities, 4)

	for _, e := range got.Entities {
		assert.NotContains(t, strings.TrimPrefix(e.Self, "https://stash.example/api/v1/entities/"), "/",
			"entity %q produced a Self URL with more than one path segment: %q -- an unescaped "+
				"id addresses a different resource", e.PublicID, e.Self)
	}
	// The specific case: a slash in the id must not create a nested path. Indexed BY ID, because
	// the response is sorted and ".." sorts before "a/b" -- my first version assumed index 0.
	var slashy string
	for _, e := range got.Entities {
		if e.PublicID == "a/b" {
			slashy = e.Self
		}
	}
	require.NotEmpty(t, slashy, "the entity with a slash in its id must be present")
	assert.Contains(t, slashy, "a%2Fb",
		"a slash in an id must be escaped, not passed through as a path separator")
	assert.Equal(t, 0, strings.Count(strings.TrimPrefix(slashy,
		"https://stash.example/api/v1/entities/"), "/"),
		"and the escaped id must contribute NO path separators, so it stays one segment. I first "+
			"asserted 1 here, reading 'one segment' as 'one slash' -- the escape is the whole "+
			"point, so the correct count is zero")
}

// The base URL is validated, because a canonical URL that is relative is not canonical and a
// base that is attacker-influenced would put caller-chosen hosts in every Self link.
func TestTheBaseURLMustBeAbsolute(t *testing.T) {
	for _, base := range []string{"", "   ", "/api/v1", "api/v1", "stash.example/api"} {
		_, err := NewPublicREST(sampleSDK(1), base)
		assert.Error(t, err, "base %q must be refused: a relative or schemeless base makes every "+
			"canonical URL wrong", base)
	}
	// A trailing slash is tolerated rather than producing a doubled separator.
	api, err := NewPublicREST(sampleSDK(1), "https://stash.example/api/v1/")
	require.NoError(t, err)
	got, err := api.Get(t.Context(), "limit=1")
	require.NoError(t, err)
	require.Len(t, got.Entities, 1)
	assert.NotContains(t, got.Entities[0].Self, "//entities", "a doubled separator is a "+
		"malformed URL")
}

// The origin filter narrows and never widens, so §6a.2 is not engaged by it -- but it must still
// work, and a filter that ignored it would be a cross-namespace read.
func TestTheOriginFilterNarrowsTheResponse(t *testing.T) {
	sdk := &countingSDK{entities: []PublicEntity{
		{PublicID: "1", Origin: "peer-a"},
		{PublicID: "2", Origin: "peer-b"},
		{PublicID: "3", Origin: "peer-a"},
	}}
	api, err := NewPublicREST(sdk, "https://stash.example/api/v1")
	require.NoError(t, err)

	got, err := api.Get(t.Context(), "limit=10&origin=peer-a")
	require.NoError(t, err)
	require.Len(t, got.Entities, 2, "only peer-a's entities may appear")
	for _, e := range got.Entities {
		assert.Equal(t, "peer-a", e.Origin)
	}
}

// The response echoes the applied limit, because a cap is only useful to a client that can see
// it, and Total is the number RETURNED -- a real total would leak the size of a library a
// caller may only partly see.
func TestTheResponseEchoesTheAppliedBound(t *testing.T) {
	api, err := NewPublicREST(sampleSDK(10), "https://stash.example/api/v1")
	require.NoError(t, err)

	got, err := api.Get(t.Context(), "limit=4&offset=2")
	require.NoError(t, err)
	assert.Equal(t, 4, got.Limit, "the caller must see the limit that was applied")
	assert.Equal(t, 2, got.Offset, "and the offset")
	assert.Equal(t, len(got.Entities), got.Total,
		"Total is the number returned, not a count of everything matching")
}

// THE STRUCTURAL CLAIM: this surface holds an SDK and translates. If it could reach storage,
// §6a.2's rule would have a second implementation here, and R054's note said the boundary
// exists precisely so it does not.
func TestTheRESTSurfaceHoldsNoStoreOfItsOwn(t *testing.T) {
	api, err := NewPublicREST(sampleSDK(1), "https://x.example")
	require.NoError(t, err)
	for _, m := range methodNamesOf(api) {
		switch m {
		case "Get", "entityURL":
		default:
			t.Errorf("PublicREST exposes %q; this surface translates and delegates. A read "+
				"implemented here would be a second implementation of the consent rule, and one "+
				"of two implementations is always the one nobody tests", m)
		}
	}
}

// The REST surface and the sandbox share one boundary, which is the point of R058: a developer
// can point a client at sample data before the real transport exists.
func TestTheRESTSurfaceRunsOverTheSandbox(t *testing.T) {
	api, err := NewPublicREST(NewSandbox(), "https://stash.example/api/v1")
	require.NoError(t, err, "R058's sandbox must satisfy the SDK boundary R054's clients use")

	got, err := api.Get(t.Context(), "limit=2")
	require.NoError(t, err)
	require.Len(t, got.Entities, 2)
	for _, e := range got.Entities {
		assert.False(t, e.Published,
			"a sandbox entity reaching the REST surface must still be unpublished; the sandbox "+
				"is for exercising a client's shape, never its consent path")
		assert.Contains(t, e.Title, SandboxSyntheticPrefix,
			"and must still carry the synthetic marker in the title the client will display")
	}
}
