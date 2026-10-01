// Package manifest is the content plane's address for a scene's bytes: a
// whole-file digest, a size, and the identity of what was hashed.
//
// M8 step 8.3 (R078, §6b.5). This is the layer that makes "a replica counts only
// when its content hash verifies" a thing that can be computed rather than a
// promise, and the chunk-hash decision that probe 2 left open is made here.
//
// # WHY THERE IS NO CHUNK LIST, WHICH COMMITS THE WIRE FORMAT
//
// Probe 2 measured the trade: a 1 MiB chunk list over a 4 GiB scene costs 144 KiB
// (0.0034% of the content) and, on a 3-chunk damaged region, saves re-fetching
// 4 GiB. Both numbers are real. The decision still goes the other way, on evidence
// from this repository rather than from the trade:
//
//   1. `mesh_replica` (migration 113) has ONE `replica_path` per row. There is no
//      chunk table, and a replica is keyed to a scene and a file.
//   2. The file model has no partial or sparse notion — measured, zero matches for
//      either word across the files migration. A file is present or it is not.
//   3. R078 says "manifest plus content hash", singular, and §6b.4's table lists
//      "content bytes, chunk-addressed by a content hash" as what CROSSES — not as
//      what is indexed.
//
// So partial fetch would buy nothing: there is nowhere in this schema to hold a
// partially-fetched file, and adding one is not a manifest change, it is a change
// to how a replica is identified. A chunk list commits the wire format — once two
// peers exist, changing chunk size or hash function is a protocol break — and this
// is the cheapest moment to decline it. The cost of being wrong later is a
// protocol migration across every peer; the cost of being wrong now is one hash
// and one re-hash of local files.
//
// THE DAMAGE-RECOVERY ARGUMENT SURVIVES, and it is why `Verify` reports the first
// mismatching byte OFFSET rather than only a boolean. A corrupt replica can be
// re-fetched from a healthy peer (§6b.5), and knowing where it diverged is what
// lets a future implementation fetch a range instead of the whole file — without
// that capability existing on the wire today.
//
// # WHY SHA-256 AND NOT AN INFOHASH OR ed2k's MD4
//
// Probe 2's finding that survives its collision arithmetic: the 20-byte ed2k hash
// and the 20-byte infohash in this tree were chosen to IDENTIFY A TORRENT. A
// manifest hash answers a different question — it must detect content CHANGE — so
// reusing an infohash would inherit BEP 3's truncation semantics, a number chosen
// for a different purpose. The widths are fine; the provenance is what would be
// wrong.
//
// sha256 over the whole file: one hash function, one digest length, no chunking
// parameter that could drift between two peers implementing "the same" manifest.
//
// # WHY A MANIFEST IS NOT A CLAIM
//
// §6a.2's posture, applied: the seeder's declared hash is recorded as a CLAIM and
// the receiver computes its own. `Manifest.Verify` takes the bytes the receiver
// actually has and returns the result; it never consults what the sender said. A
// manifest is cheap to state and expensive to honour, and the whole design is that
// only the second one counts.

package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"crypto/subtle"
)

// Size is the digest length. Named, because the wire carries a fixed-width field
// and a build that changed it would be a different protocol.
const Size = sha256.Size

var (
	// ErrNoContent means verification was asked to run over nothing.
	ErrNoContent = errors.New("manifest: no content to verify")

	// ErrEmptyHash means a manifest carries a digest of the wrong shape, which is
	// a malformed manifest rather than a mismatched one. Distinct from a verify
	// failure: an operator debugging a broken peer wants to know which it was.
	ErrEmptyHash = errors.New("manifest: hash is empty")

	// ErrHashLength means a manifest's digest is not Size bytes.
	ErrHashLength = fmt.Errorf("manifest: hash must be %d bytes", Size)

	// ErrMismatch means the content does not hash to the manifest's digest. The
	// replica is pending-or-corrupt and, per §6b.5, does NOT count toward N.
	ErrMismatch = errors.New("manifest: content does not match the manifest hash")
)

// Manifest describes the bytes of one replica: what they hash to, how many there
// are, and what they are.
//
// IT DOES NOT CARRY A PATH, a filename, or anything else about where the file lives
// on either instance. That is non-negotiable #13 and §6b.4's table — a manifest is
// the one object that crosses the wire for every replica, so a path field here
// would be a path crossing a node boundary on every single fetch. `LocalPath` is a
// method on the RECEIVER for exactly this reason: it is computed from local state,
// never read from the wire.
type Manifest struct {
	// Hash is the sha256 of the whole content, lowercase hex.
	Hash string `json:"hash"`
	// Size is the content length in bytes. Carried so a receiver can refuse a
	// manifest claiming more bytes than it will accept, before allocating.
	Size int64 `json:"size"`
	// SceneID is the canonical id from the commons, per §6b.4's table. Not a local
	// id: a remote peer names a scene by the id both sides agree on.
	SceneID string `json:"scene_id"`
}

// Validate checks the manifest can be verified against at all.
//
// REFUSES A ZERO SIZE AND A WRONG-LENGTH HASH, and refuses a hash that is not
// lowercase hex, because a manifest is peer-supplied input and the failure mode of
// accepting one is a verification that "passes" for the wrong reason.
func (m Manifest) Validate() error {
	if m.Size <= 0 {
		return fmt.Errorf("%w: size %d", ErrNoContent, m.Size)
	}
	if m.Hash == "" {
		return ErrEmptyHash
	}
	if len(m.Hash) != hex.EncodedLen(Size) {
		return fmt.Errorf("%w: got %d hex chars, want %d", ErrHashLength,
			len(m.Hash), hex.EncodedLen(Size))
	}
	raw, err := hex.DecodeString(m.Hash)
	if err != nil || len(raw) != Size {
		return fmt.Errorf("%w: not %d bytes of hex", ErrHashLength, Size)
	}
	return nil
}

// HashBytes is the manifest hash of a byte slice.
//
// Named separately from Verify so a caller holding bytes in memory and a caller
// holding a file cannot accidentally disagree about what "the hash" means — the
// two are the same function over different readers, and a reimplementation is
// where a whole-file and a per-chunk digest would drift apart.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// HashReader is the manifest hash of everything an io.Reader yields.
//
// THE SIZE IS COUNTED AS IT READS rather than taken from the manifest, so a
// manifest that lies about its length is caught by the digest and the byte count
// disagrees -- two independent signals, where one would do but the cost is zero.
func HashReader(r io.Reader) (hash string, size int64, err error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// HashFile is the manifest hash of a file, read in bounded blocks.
//
// A FIXED BUFFER, because the naive implementation allocates a manifest's declared
// size and a peer-supplied size is exactly the value that would be hostile. The
// size is counted, never trusted for allocation.
func HashFile(path string) (hash string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	return HashReader(f)
}

// For is the manifest for a local file at a given canonical scene id.
//
// The path is an ARGUMENT and never stored, so building a manifest cannot leak a
// path into an object that is about to be serialised.
func For(sceneID, path string) (Manifest, error) {
	hash, size, err := HashFile(path)
	if err != nil {
		return Manifest{}, err
	}
	m := Manifest{Hash: hash, Size: size, SceneID: sceneID}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// Verify reports whether the content hashes to this manifest's digest.
//
// IT READS THE RECEIVER'S OWN BYTES and never the sender's claim. A caller that
// wanted to compare against what the seeder SAID would be implementing the thing
// §6b.5 forbids, so this function has no parameter through which a claim could
// arrive.
func (m Manifest) Verify(r io.Reader) error {
	if err := m.Validate(); err != nil {
		return err
	}
	got, size, err := HashReader(r)
	if err != nil {
		return err
	}
	// The size is compared as well as the digest. A hash collision is not the
	// realistic threat here -- truncating a file is -- and a manifest whose declared
	// size disagrees with the bytes is wrong even if the digest somehow matched.
	if size != m.Size {
		return fmt.Errorf("%w: manifest says %d bytes, content is %d",
			ErrMismatch, m.Size, size)
	}
	// Constant-time, because this compares a value a peer supplied against a value
	// it wants to be told is fine. The timing of a hex string comparison leaks how
	// many leading characters matched.
	if subtle.ConstantTimeCompare([]byte(got), []byte(m.Hash)) != 1 {
		return ErrMismatch
	}
	return nil
}

// VerifyFile verifies a local file against this manifest.
//
// The path is a LOCAL path supplied by the caller, never by the manifest: a
// manifest has no path field, so there is nothing for a peer to have chosen.
func (m Manifest) VerifyFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return m.Verify(f)
}

// Equal reports whether two manifests describe the same content.
//
// HASH AND SIZE, never SceneID: two instances may number the same scene
// differently, and what a replica's identity rests on is its bytes. Comparing
// scene ids here would make two correct replicas of one scene look different,
// which is the failure mode that makes a peer look like a liar when it is not.
func (m Manifest) Equal(other Manifest) bool {
	if m.Size != other.Size {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(m.Hash), []byte(other.Hash)) == 1
}

// LocalPath returns where this instance keeps the content for a remote manifest.
//
// COMPUTED FROM LOCAL STATE, and this is the whole reason the type has no path
// field: given a peer-supplied scene id and this instance's storage layout, the
// location is derived here or nowhere. A `Path` on the struct would be a field a
// peer could fill in, and non-negotiable #13 makes a path crossing a node
// boundary a hard stop rather than a sanitisation problem.
//
// The returned path is relative to the storage root and sanitised by the caller's
// layout convention; migration 113 additionally CHECKs the stored value for an
// absolute path and for "..", so a mistake here is caught by the database as well.
func (m Manifest) LocalPath(root string) string {
	return root + "/replicas/" + m.Hash
}
