package cluster

import (
	"encoding/binary"
	"fmt"
	"math"
)

// The pass needs to persist an embedding as bytes, because that is what
// `person_cluster_members.embedding` is, and the arithmetic needs it back as
// `[]float32` on the next run.
//
// # Why little-endian float32 and not JSON
//
// The column is a BLOB holding a 512-wide embedding. Every candidate is 2048
// bytes. A JSON array is 3-6x larger, is not fixed-width, and -- the part that
// actually matters -- round-trips through `float64` text, so a stored value can
// come back as a slightly different float32 than the one that was written. That
// would make a re-clustering pass see a distance that is not the distance it
// wrote, and the difference would be invisible. Little-endian float32 is
// byte-exact: read it back and you have the same bits you stored.
//
// The endianness is fixed and documented rather than negotiated, because the
// blobs are written by one process and read by the same process on the same
// machine. If StashForge ever migrates a library across architectures, the
// migration must convert -- and that is a migration, not a fallback in the
// read path that would silently mis-read old blobs on big-endian hardware.
//
// # The width is checked on the way in and out
//
// `MarshalEmbedding` refuses a vector that is not `EmbeddingDim`, and
// `UnmarshalEmbedding` refuses a blob whose length is not exactly
// `4 * EmbeddingDim`. A short read is the shape a truncated or corrupted blob
// takes, and truncating a float32 vector to a shorter width still yields a
// "valid-looking" slice that compares as garbage rather than erroring.

// embeddingBytes is the size of one marshalled embedding.
const embeddingBytes = 4 * EmbeddingDim

// MarshalEmbedding encodes an embedding for storage.
//
// Refuses a wrong-width or unusable vector, because a member whose embedding
// cannot be compared is a member the next pass cannot reason about -- the store
// enforces that a member has an embedding, and it should be enforcing that it
// is a USABLE one.
func MarshalEmbedding(v []float32) ([]byte, error) {
	if err := ValidateEmbedding(v); err != nil {
		return nil, fmt.Errorf("refusing to store an unusable embedding: %w", err)
	}
	if len(v) != EmbeddingDim {
		return nil, fmt.Errorf("%w: width %d, want %d", ErrWrongWidth, len(v), EmbeddingDim)
	}
	buf := make([]byte, embeddingBytes)
	for i, x := range v {
		binary.LittleEndian.PutUint32(buf[4*i:], math.Float32bits(x))
	}
	return buf, nil
}

// UnmarshalEmbedding decodes a stored embedding.
//
// A blob of the wrong length is an error, not a best-effort read. This is the
// counterpart to the width check in CosineGeometry: a blob that decodes to the
// wrong width is a corrupt row, and a corrupt row that decodes to *some* width
// is exactly the failure this milestone exists to prevent -- a face that looks
// present but measures as nonsense.
func UnmarshalEmbedding(b []byte) ([]float32, error) {
	if len(b) != embeddingBytes {
		return nil, fmt.Errorf("%w: stored embedding is %d bytes, want %d",
			ErrWrongWidth, len(b), embeddingBytes)
	}
	v := make([]float32, EmbeddingDim)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v, nil
}
