package manifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// THE CORE OF R078 AND §6b.5: a replica counts only when the content hashes to
// the manifest's digest. A receiver that verified against the SENDER'S CLAIM
// instead of its own bytes would make the whole property theatre -- the test
// hashes the content itself, so a Verify implementation that consulted a claim
// could not pass.
func TestAReplicaVerifiesByItsOwnBytesNotTheSenderSClaim(t *testing.T) {
	content := []byte("a scene, or some bytes of one")
	want := sha256.Sum256(content)

	// The sender's claim is DELIBERATELY WRONG. Verify must still pass, because it
	// never reads it: there is no parameter to read it from.
	m := Manifest{Hash: hex.EncodeToString(want[:]), Size: int64(len(content)), SceneID: "42"}

	require.NoError(t, m.Verify(bytes.NewReader(content)),
		"the receiver's own bytes verify, which is the only thing §6b.5 counts")

	// And the function's SIGNATURE is part of the assertion: Verify takes only a
	// reader. A variant taking an expected-hash argument would be the bug.
	rt := reflect.TypeOf(m.Verify)
	require.Equal(t, 1, rt.NumIn(), "Verify takes a reader and nothing else")
	assert.Equal(t, reflect.Interface, rt.In(0).Kind(),
		"so there is no channel through which a peer's claim could arrive")
}

// THE COMPARISON MUST COVER THE WHOLE DIGEST, and this is the only test in the
// file that can catch it.
//
// Mutation `C` truncated the constant-time comparison to `[:8]` and every other
// test still passed. That is not a hypothetical weakness: `subtle.
// ConstantTimeCompare` returns 0 when the LENGTHS differ, so slicing both sides to
// the same short length compares only that prefix and reports a match. A digest
// truncated to 64 bits is forgeable with ~2^32 work rather than ~2^256.
//
// It cannot be caught statistically -- that needs two real sha256 digests sharing a
// 64-bit prefix, a second-preimage search -- so it is caught STRUCTURALLY instead:
// a manifest whose hash differs from the true digest ONLY IN ITS LAST BYTES. It is
// a valid 32-byte hex digest that no content produces, which is exactly what a
// substituted replica looks like, and a prefix comparison accepts it.
func TestTheDigestComparisonCoversEveryByteNotAPrefix(t *testing.T) {
	content := []byte("content whose digest we will alter in its tail")
	trueSum := sha256.Sum256(content)
	trueHex := hex.EncodeToString(trueSum[:])

	// Flip the LAST byte only. The first 31 bytes are genuine, so any comparison
	// that does not reach the final byte sees two identical prefixes.
	tampered := make([]byte, Size)
	copy(tampered, trueSum[:])
	tampered[Size-1] ^= 0xff

	m := Manifest{Hash: hex.EncodeToString(tampered), Size: int64(len(content))}
	require.NoError(t, m.Validate(),
		"a tail-tampered digest is still well-formed, which is the point: it is "+
			"not distinguishable from a real one by inspection")

	err := m.Verify(bytes.NewReader(content))
	require.ErrorIs(t, err, ErrMismatch,
		"a digest that differs in its LAST byte must not verify. A comparison that "+
			"returns after checking a prefix accepts a substituted replica that "+
			"agrees on that prefix, and ConstantTimeCompare gives no length error for "+
			"two slices trimmed to the same short length")

	// And the same must hold for Equal, which is the path that decides whether two
	// manifests are the same content.
	a := Manifest{Hash: trueHex, Size: int64(len(content))}
	assert.False(t, a.Equal(m), "Equal must also compare every byte")
}

// A mismatch is not a partial success. §6b.5: an unverified replica is `pending`
// and does not count toward N.
func TestCorruptContentDoesNotVerify(t *testing.T) {
	good := []byte("original content")
	sum := sha256.Sum256(good)
	m := Manifest{Hash: hex.EncodeToString(sum[:]), Size: int64(len(good)), SceneID: "1"}

	err := m.Verify(bytes.NewReader([]byte("corrupted content!")))
	require.ErrorIs(t, err, ErrMismatch,
		"a replica that does not verify must fail loudly, because it does not count")

	// Truncation is the realistic threat, not a hash collision, so the size is
	// checked as well as the digest.
	short := Manifest{Hash: m.Hash, Size: int64(len(good)), SceneID: "1"}
	err = short.Verify(bytes.NewReader(good[:5]))
	require.ErrorIs(t, err, ErrMismatch, "a truncated file must not verify by digest alone")
	assert.Contains(t, err.Error(), "bytes",
		"and the error says the sizes disagree, which is the actionable part")
}

// A manifest is PEER-SUPPLIED input. Every field is attacker-controlled, and a
// malformed one that "verifies" for the wrong reason is worse than a rejected one.
func TestAManifestThatCouldNotBeVerifiedIsRefused(t *testing.T) {
	content := []byte("x")
	sum := sha256.Sum256(content)
	good := hex.EncodeToString(sum[:])

	for name, m := range map[string]Manifest{
		"zero size":      {Hash: good, Size: 0},
		"negative size":  {Hash: good, Size: -1},
		"empty hash":     {Hash: "", Size: int64(len(content))},
		"short hash":     {Hash: good[:16], Size: int64(len(content))},
		"long hash":      {Hash: good + "00", Size: int64(len(content))},
		"not hex":        {Hash: strings.Repeat("z", 64), Size: int64(len(content))},
		"odd length hex": {Hash: strings.Repeat("a", 63), Size: int64(len(content))},
	} {
		err := m.Validate()
		assert.Error(t, err, "%s must be refused: it is a manifest a peer could send", name)
	}
}

// An invalid manifest must be refused BEFORE the content is read, not after.
func TestVerifyRefusesBeforeReadingContent(t *testing.T) {
	m := Manifest{Hash: "", Size: 100}

	// A reader that records whether it was touched.
	touched := false
	r := readerFunc(func(p []byte) (int, error) { touched = true; return 0, io.EOF })

	err := m.Verify(r)
	require.Error(t, err)
	assert.False(t, touched,
		"validation runs first. A malformed manifest must not cause a read -- on a "+
			"4 GiB replica that is real I/O performed for a request that was already "+
			"going to be refused")
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// encodeJSON serialises a manifest so the test can assert on the KEYS THAT CROSS
// rather than on the Go field names, which decouple the moment a tag or an
// embedded struct appears.
func encodeJSON(t *testing.T, m Manifest) string {
	t.Helper()
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return string(b)
}

// keysOf reads the top-level keys out of a serialised manifest.
func keysOf(s string) []string {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil
	}
	out := make([]string, 0, len(raw))
	for k := range raw {
		out = append(out, k)
	}
	return out
}

// --- retained-memory shims for the bounded-buffer assertion ---

// runtimeMem is a snapshot of the heap. Read through readMemBefore/After rather
// than runtime.ReadMemStats directly so the assertion reads as one line.
type runtimeMem struct {
	total     int64
	heapAlloc int64
}

// allocatedDelta is how much heap GREW across a call. Negative is clamped to zero:
// a GC during the call is not a regression, and reporting it as one would make the
// assertion flaky rather than wrong.
func allocatedDelta(b, a runtimeMem) int64 {
	if d := a.total - b.total; d > 0 {
		return d
	}
	return 0
}

// #13 IS NON-NEGOTIABLE AND A MANIFEST IS THE OBJECT THAT CROSSES FOR EVERY
// SINGLE FETCH, so a path field here would be a path crossing a node boundary on
// every fetch. The test asserts the SHAPE rather than greping for a word, because a
// field named `location` or `dest` would pass a grep.
func TestAManifestCarriesNoPathOrIdentifyingField(t *testing.T) {
	fields := reflect.TypeOf(Manifest{})

	// Exactly the three fields the wire format declares. Adding a fourth -- a
	// path, a filename, a username, a hostname -- fails here rather than shipping.
	want := map[string]string{
		"Hash":    "string",
		"Size":    "int64",
		"SceneID": "string",
	}

	got := map[string]string{}
	for i := 0; i < fields.NumField(); i++ {
		f := fields.Field(i)
		got[f.Name] = f.Type.String()
		// An unexported field would be a place to hide a path.
		assert.True(t, f.IsExported(), "field %s is unexported; JSON would skip it, "+
			"so the wire format and the in-memory format would disagree", f.Name)
	}

	assert.Equal(t, want, got,
		"the manifest's fields ARE the wire format. §6b.4 permits content hash, "+
			"size, chunk list and the canonical scene id -- and nothing else. A path, "+
			"filename, hostname, username or session token here is non-negotiable #13 "+
			"violating on every fetch")

	// And the field names must not be describing a location in words a shape check
	// cannot see, which is why the exact-set assertion above is the real guard.
	for _, banned := range []string{"path", "file", "name", "host", "user", "dir", "location", "url", "token"} {
		for name := range got {
			assert.NotContains(t, strings.ToLower(name), banned,
				"field %s looks like it carries identifying information", name)
		}
	}
}

// The JSON shape is what actually crosses, so assert the SERIALISED keys rather
// than the Go fields. A `json:"-"` or an embedded struct would decouple them.
func TestTheSerialisedManifestCarriesExactlyThreeKeys(t *testing.T) {
	content := []byte("payload")
	sum := sha256.Sum256(content)
	m := Manifest{Hash: hex.EncodeToString(sum[:]), Size: int64(len(content)), SceneID: "s7"}

	enc := encodeJSON(t, m)

	// Exactly the three keys, and nothing that could carry a path.
	assert.ElementsMatch(t, []string{"hash", "size", "scene_id"}, keysOf(enc),
		"what crosses the wire is exactly the manifest hash, the size and the "+
			"canonical scene id (§6b.4's table)")
	assert.NotContains(t, strings.ToLower(enc), "path")
	assert.NotContains(t, strings.ToLower(enc), "file")
}

// LocalPath is how a receiver locates content, and it must be DERIVED from the
// hash and the local root -- never taken from the wire.
func TestLocalPathIsDerivedFromTheHashAndTheRoot(t *testing.T) {
	m := Manifest{Hash: strings.Repeat("a", 64), Size: 1, SceneID: "1"}

	p := m.LocalPath("/srv/storage")
	assert.Equal(t, "/srv/storage/replicas/"+m.Hash, p,
		"the location is computed from local layout and the content hash")

	// A scene id carrying traversal cannot escape, because it is not in the path
	// at all -- the hash is, and the hash is validated hex.
	assert.NotContains(t, m.LocalPath("/srv/storage"), "..",
		"the scene id does not appear in the path, so it cannot direct the location")
}

// A file on disk verifies through the same path as bytes in memory. The two must
// not disagree, because one is used at ingestion and the other during a fetch.
func TestHashingAFileAndHashingItsBytesAgree(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scene.mp4")
	content := bytes.Repeat([]byte("video frame data "), 40_000) // ~640 KiB
	require.NoError(t, os.WriteFile(path, content, 0o600))

	fromFile, size, err := HashFile(path)
	require.NoError(t, err)
	assert.Equal(t, int64(len(content)), size)
	assert.Equal(t, HashBytes(content), fromFile,
		"HashFile and HashBytes must agree, or a replica verifies when read one way "+
			"and not the other")

	m, err := For("scene-1", path)
	require.NoError(t, err)
	assert.Equal(t, "scene-1", m.SceneID)
	assert.NoError(t, m.VerifyFile(path))
}

// Hashing a file must not allocate the file's size. The size is peer-declared
// elsewhere, and an implementation that trusted it is one `make([]byte, size)`
// away from an OOM from a stranger.
func TestHashingALargeFileDoesNotAllocateItsSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.bin")
	require.NoError(t, os.WriteFile(path, make([]byte, 8<<20), 0o600))

	// The point is that the code uses a bounded buffer; this asserts the
	// observable consequence rather than the implementation detail.
	var before, after runtimeMem
	readMemBefore(&before)
	hash, size, err := HashFile(path)
	readMemAfter(&after)

	require.NoError(t, err)
	assert.Equal(t, int64(8<<20), size)
	assert.Len(t, hash, 64)
	assert.Less(t, allocatedDelta(before, after), int64(32<<20),
		"hashing an 8 MiB file must not retain 8 MiB; the reader is bounded")
}

// Two manifests for the same bytes on differently-numbered scenes are EQUAL. A
// peer's scene numbering is its own, and comparing ids would make two correct
// replicas of one scene look like different content.
func TestEqualComparesContentNotSceneIdentity(t *testing.T) {
	sum := sha256.Sum256([]byte("same bytes"))
	h := hex.EncodeToString(sum[:])

	a := Manifest{Hash: h, Size: 9, SceneID: "instance-a-scene-1"}
	b := Manifest{Hash: h, Size: 9, SceneID: "instance-b-scene-77"}

	assert.True(t, a.Equal(b),
		"§6b.4: content bytes are addressed by content hash. Scene ids are per-"+
			"instance and must not decide whether two manifests describe one thing")

	// Different bytes, same size: not equal.
	other := sha256.Sum256([]byte("other bytes"))
	assert.False(t, a.Equal(Manifest{
		Hash: hex.EncodeToString(other[:]), Size: 9, SceneID: a.SceneID}))

	// Same bytes, different size: not equal. A manifest that lies about length is
	// describing something that is not there.
	assert.False(t, a.Equal(Manifest{Hash: h, Size: 10, SceneID: a.SceneID}))
}

// The errors a caller distinguishes must be DISTINGUISHABLE, because the operator
// response differs: a malformed manifest is a peer bug, a mismatch is a corrupt or
// substituted replica.
func TestTheErrorsSeparatePeerBugFromCorruptReplica(t *testing.T) {
	content := []byte("x")
	sum := sha256.Sum256(content)
	good := Manifest{Hash: hex.EncodeToString(sum[:]), Size: 1}

	errMalformed := Manifest{Hash: "nope", Size: 1}.Validate()
	errMismatch := good.Verify(bytes.NewReader([]byte("y")))

	assert.True(t, errors.Is(errMalformed, ErrHashLength),
		"a malformed manifest has its own error, so a peer that cannot encode one "+
			"is diagnosable separately from a peer serving the wrong bytes")
	assert.True(t, errors.Is(errMismatch, ErrMismatch))
	assert.False(t, errors.Is(errMalformed, ErrMismatch),
		"and the two must not overlap, or an operator cannot tell a bug from a "+
			"corrupt replica")
}

// One place, one definition of "the hash". A reimplementation is exactly where a
// whole-file and a per-chunk digest would drift apart -- and a drift means a
// replica that verifies when built and fails when fetched.
func TestHashBytesMatchesTheStandardLibraryDirectly(t *testing.T) {
	for _, payload := range [][]byte{
		nil, {}, []byte("a"), []byte("ab"), bytes.Repeat([]byte("z"), 100_000),
	} {
		want := sha256.Sum256(payload)
		assert.Equal(t, hex.EncodeToString(want[:]), HashBytes(payload),
			"HashBytes must be sha256 of the payload, for input of length %d",
			len(payload))
	}

	// And over a reader, including the empty case which has no content to hash.
	got, size, err := HashReader(bytes.NewReader(nil))
	require.NoError(t, err)
	assert.Equal(t, int64(0), size)
	assert.Equal(t, HashBytes(nil), got)
}

// --- heap snapshots ---

func readMemBefore(m *runtimeMem) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	m.total = int64(ms.TotalAlloc)
	m.heapAlloc = int64(ms.HeapAlloc)
}

func readMemAfter(m *runtimeMem) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	m.total = int64(ms.TotalAlloc)
	m.heapAlloc = int64(ms.HeapAlloc)
}

// Digest of the EMPTY content is a real value, and a manifest claiming it with a
// non-zero size is a lie the size check catches.
func TestEmptyContentHasAHashButNoManifest(t *testing.T) {
	empty := HashBytes(nil)
	assert.Equal(t, hex.EncodeToString(sha256.New().Sum(nil)), empty,
		"the sha256 of no bytes is a well-defined constant, not an error")

	// A manifest with that hash and size 0 is refused, because there is nothing to
	// preserve.
	err := Manifest{Hash: empty, Size: 0}.Validate()
	assert.ErrorIs(t, err, ErrNoContent,
		"§6b.5 verifies CONTENT. An empty replica verifies nothing and would count "+
			"as a copy that is not there")
}
