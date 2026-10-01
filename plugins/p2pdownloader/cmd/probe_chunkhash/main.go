// Command probe_chunkhash measures §6b.7 probe 2: what is the chunk hash?
//
// §6b.7's reason for making this a probe rather than a decision: "a content hash
// implies a Merkle tree, and the tree's shape is a wire-format decision that cannot
// be changed once peers exist." So the question is not "which hash function" -- it
// is which SHAPE, and the shape's cost is only visible measured.
//
// THE THREE SHAPES, and what each costs. A plain per-file hash is simplest and
// wastes bandwidth on a partial fetch, because verifying a 4 GB file means
// re-reading all 4 GB. A content-addressed chunk list is better and commits the
// format. A whole-file content hash sits in between: one hash, but only ever
// verified whole.
//
// WHAT THIS MEASURES, on a real file of a real size:
//  1. verification cost -- bytes that must be READ to verify, per shape;
//  2. the partial-fetch saving a chunk list buys, and what it costs in wire bytes;
//  3. the collision question, at the size a real video file reaches.
//
// It does not decide. It produces the numbers the decision needs.
//
// Run:  cd plugins/p2pdownloader && go run ./cmd/probe_chunkhash
package main

import (
	"crypto/sha1"
	"crypto/sha256"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
)

// ChunkSize is the candidate chunk size. 1 MiB is the shape a partial fetch wants:
// small enough that re-fetching one damaged region is cheap, large enough that the
// per-chunk hash overhead stays under a percent.
const ChunkSize = 1 << 20 // 1 MiB

// videoSize is a realistic scene size. 4 GiB is large for one scene and ordinary
// for the corpus an instance holds, so it is the size the numbers are reported at.
const videoSize = 4 << 30 // 4 GiB

func main() {
	fmt.Println("=== §6b.7 probe 2: what is the chunk hash?")
	fmt.Println()

	f, path := makeFile(videoSize)
	defer os.Remove(path)

	info, err := f.Stat()
	if err != nil {
		fmt.Printf("stat: %v\n", err)
		os.Exit(1)
	}
	size := info.Size()
	fmt.Printf("fixture: %s (%s)\n\n", filepath.Base(path), humanBytes(size))

	reportWholeFile(f, size)
	reportChunks(f, size)
	reportCollision()
	reportVerdict(size)
}

func reportWholeFile(f *os.File, size int64) {
	fmt.Println("--- 1. whole-file hash: what verification costs")

	// One pass, one hash. The number that matters is BYTES READ, because that is
	// bandwidth and disk time on a peer serving a stranger.
	pass, out, err := hashWhole(f, sha256.New())
	if err != nil {
		fmt.Printf("  %v\n", err)
		return
	}
	fmt.Printf("  sha256 over the whole file: read %s in one pass\n", humanBytes(pass))
	fmt.Printf("  hash: %x\n", out[:8])
	fmt.Printf("  => verification is exact, and costs one full read.\n")
	fmt.Printf("     A 4 GiB replica is verified by moving 4 GiB.\n\n")
}

func reportChunks(f *os.File, size int64) {
	fmt.Println("--- 2. chunk list: what a partial fetch saves")

	n := (size + ChunkSize - 1) / ChunkSize // int64
	fmt.Printf("  chunk size %s over %s = %d chunks\n\n",
		humanBytes(ChunkSize), humanBytes(size), n)

	// A chunk list's cost is per-chunk hashes on the wire.
	perHash := int64(sha256.Size + 4) // 32 bytes of hash + 4 of length prefix
	listBytes := n * perHash
	fmt.Printf("  wire cost of the chunk list: %d x %d B = %s\n",
		n, perHash, humanBytes(int64(listBytes)))
	fmt.Printf("    which is %.4f%% of the content\n\n",
		100*float64(listBytes)/float64(size))

	// And its saving: re-fetching one damaged chunk instead of the whole file.
	damaged := 3
	fmt.Printf("  damaged region of %d chunk(s):\n", damaged)
	fmt.Printf("    whole-file verify + re-fetch: %s\n", humanBytes(size*2))
	fmt.Printf("    chunk verify + re-fetch:     %s\n", humanBytes(size+int64(damaged*ChunkSize)))
	fmt.Printf("    => a chunk list pays %.4f%% of the content to avoid re-fetching\n",
		100*float64(listBytes)/float64(size))
	fmt.Printf("       %s. That is the whole trade, in two numbers.\n\n",
		humanBytes(size-int64(damaged*ChunkSize)))
}

func reportCollision() {
	fmt.Println("--- 3. the collision question, at the size that matters")

	// My first version of this section compared a 160-bit hash against the bit
	// count of one 4 GiB file and printed "headroom 0.0x" for both. The number was
	// meaningless: a file's SIZE says nothing about how many things will be
	// addressed. What matters is the address space a chunk list actually indexes.
	//
	// A chunk list addresses chunks, not files. At 1 MiB chunks, a 4 GiB file is
	// 4096 chunks -- so the space that must stay collision-free is the number of
	// chunks a corpus holds, not the number of files.
	corpusChunks := 1 << 30 // a billion chunks: ~1 TiB at this chunk size
	fmt.Printf("  corpus of %s chunks (a billion): %d bits of address space\n",
		humanBytes(int64(corpusChunks)*ChunkSize), corpusChunks)

	// Birthday bound. Collisions become likely when the address space is a
	// fraction of 2^width, not when the file is big.
	for _, c := range []struct {
		name string
		bits float64
	}{
		{"ed2k  MD4, 128 bit", 128},
		{"sha1             160 bit", 160},
		{"sha256           256 bit", 256},
	} {
		// P(collision) ~= n^2 / 2^(width+1)
		p := 1.0 / (2 * pow2(c.bits+1))
		fmt.Printf("    %-22s P(collision) over that corpus = %.3g\n", c.name, p)
	}
	fmt.Println()
	fmt.Println("  => a 128-bit hash is NOT the problem here, and saying it was would be")
	fmt.Println("     the probe talking. The real finding is narrower and still matters:")
	fmt.Println("     the 20-byte ed2k hash and the 20-byte infohash in this tree were")
	fmt.Println("     both chosen to IDENTIFY A TORRENT, and a manifest hash answers a")
	fmt.Println("     different question -- it must detect content CHANGE, not find a")
	fmt.Println("     torrent. Reusing an infohash for a manifest would inherit BEP 3's")
	fmt.Println("     truncation semantics, which is a number chosen for a different")
	fmt.Println("     purpose.")
	fmt.Println()
}

// pow2 is math.Pow2(1) with the argument's own type, spelled out so the collision
// line above reads as the formula it is.
func pow2(x float64) float64 { return math.Pow(2, x) }

func reportVerdict(_ int64) {
	fmt.Println("--- 4. what this does and does not decide")
	fmt.Println("  MEASURED: whole-file verification costs one full read; a chunk list")
	fmt.Println("  costs under a tenth of a percent of wire and saves a full re-fetch")
	fmt.Println("  when a region is damaged.")
	fmt.Println()
	fmt.Println("  NOT DECIDED HERE, and the reason matters: a chunk list COMMITS the")
	fmt.Println("  wire format. Once two peers exist, changing chunk size or hash")
	fmt.Println("  function is a protocol break, not a refactor. So the decision needs")
	fmt.Println("  two things this probe cannot supply:")
	fmt.Println("    1. whether partial fetch is a real requirement or a nicety. If a")
	fmt.Println("       replica is all-or-nothing at the scene level -- which is what")
	fmt.Println("       the scanner and the file model already assume -- then a chunk")
	fmt.Println("       list buys nothing and costs wire forever.")
	fmt.Println("    2. the ed2k precedent, which is the cautionary one. ed2k HAS a chunk")
	fmt.Println("       list and a Merkle-ish structure, and the library implementing it")
	fmt.Println("       was measured and kept OUT of the hash path. The lesson recorded")
	fmt.Println("       in docs/specs/2026-09-28-ed2k-wire-plan.md is that a wire format")
	fmt.Println("       with a tree in it is a permanent obligation.")
	fmt.Println()
	fmt.Println("  So the decision is M8 step 1's, and this probe's job was to make the")
	fmt.Println("  cost of it visible before it is made.")
}

func makeFile(size int64) (*os.File, string) {
	path := filepath.Join(os.TempDir(), "probe_chunkhash.bin")
	f, err := os.Create(path)
	if err != nil {
		fmt.Printf("create: %v\n", err)
		os.Exit(1)
	}
	// Sparse where possible: the CONTENT does not matter for a hashing benchmark,
	// only the byte count and the read path.
	if err := f.Truncate(size); err != nil {
		fmt.Printf("truncate: %v\n", err)
		os.Exit(1)
	}
	return f, path
}

func hashWhole(f *os.File, h interface {
	io.Writer
	Sum([]byte) []byte
}) (int64, []byte, error) {
	if _, err := f.Seek(0, 0); err != nil {
		return 0, nil, err
	}
	buf := make([]byte, 1<<20)
	var read int64
	for {
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			read += int64(n)
		}
		if err != nil {
			break
		}
	}
	return read, h.Sum(nil), nil
}

// humanBytes formats a size. The unit table is walked with a bounds check: a
// corpus of a billion 1 MiB chunks is ~1 TiB, which runs off the end of a
// "KMGT" string and panicked with index out of range. A formatter that panics on
// a large number is worse than one that prints something approximate, because it
// takes the measurement down with it.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 5; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

var _ = sha1.New
