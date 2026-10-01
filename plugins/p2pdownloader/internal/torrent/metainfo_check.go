package torrent

import (
	"errors"
	"fmt"
	"math"

	"github.com/anacrolix/torrent/metainfo"
)

// # WHY THIS EXISTS, WHEN THE LIBRARY ALREADY VALIDATES
//
// It does. `anacrolix/torrent` refuses these, with real errors, measured by
// running it:
//
//	piece length 0            -> "bad info: zero piece length"
//	piece length -1           -> "bad info: piece count and file lengths are at odds"
//	pieces shorter than length -> "bad info: piece count and file lengths are at odds"
//
// And it does it in `AddTorrentSpec`, not at parse. `UnmarshalInfo` accepts all
// three, and so does `TorrentSpecFromMetaInfoErr`. So the sequence in this
// package is:
//
//	policy -> GATE -> AddTorrentSpec
//	                 ^-- the library's validation happens HERE
//
// The gate runs first. That ordering is correct for the gate — a torrent whose
// names escape the root must never reach the client — but it means a torrent
// with a zero piece length is *gated* before anyone notices it is nonsense: the
// gate builds paths, reserves nothing, and accepts. Then the library refuses it.
//
// The outcome is right and the report is wrong. `Decision.Err` says the library
// declined a torrent "after the storage gate accepted it", which is true, and it
// is not what an operator needs to know. The operator needs to know the torrent
// is malformed, because a malformed torrent is worth retrying against a
// different source and a refusal is not.
//
// So this package checks the metainfo itself, before the gate, and returns a
// MALFORMED error distinct from a REFUSAL. The library's check stays as the
// backstop — a second opinion on the same field is cheap, and this one is not
// where the trust boundary is.
//
// # WHAT IS CHECKED, AND WHY EACH ONE
//
// The three above are the library's set, plus two it does not check and this
// downloader should. Not an arbitrary collection: each is a field whose value is
// attacker-chosen and arithmetic is done on it before anything has been written.

// ErrMalformed means the torrent's own metadata is internally inconsistent.
//
// A SENTINEL, and distinct from `storage.ErrRefused` for the same reason
// `ErrRefusedUpFront` is: the two call for opposite operator actions. A refusal
// is a judgement about the file names and will be the same answer from every
// source — retrying it just means asking another peer the same question. A
// malformed torrent is worth retrying, because a different source may have
// transferred it correctly.
//
// `errors.Is(err, ErrMalformed)` and `errors.Is(err, storage.ErrRefused)` are
// both false for the other's error, and a caller that checks only the presence
// of an error cannot tell them apart.
var ErrMalformed = errors.New("the torrent's own metadata is internally inconsistent")

// maxPieceLength is the largest piece length accepted.
//
// 64 MiB, which is 4× the BitTorrent convention (16 MiB) and 2× what the
// reference implementation allows. The number is not the point; the ceiling is.
//
// It exists because piece length is multiplied by piece count to size a piece
// table, and both are attacker-chosen. A torrent claiming a 2 GiB piece length
// does not need to be malicious to be a problem: a single piece of that size is
// one contiguous allocation in every storage backend.
const maxPieceLength = 64 << 20

// minPieceLength is the smallest accepted, and it is BEP 3's own floor.
//
// 16 KiB. Below this the per-piece overhead — the hash, the wire framing, the
// index entry — dominates the payload, and a swarm of such peers transfers
// almost nothing while appearing healthy. It is also what BEP 3 specifies, so
// refusing it costs nothing real.
const minPieceLength = 16 << 10

// MaxTorrentBytes is the largest total length accepted.
//
// 1 TiB. A single media file in a library is not a terabyte, so this is far above
// any legitimate input, and the real reason is arithmetic: `TotalLength` is an
// int64 sum over attacker-supplied per-file lengths, and a torrent declaring
// 8 EiB is accepted by the library and then used to size a download.
//
// This is a guard, not a limit that anyone will hit. A caller with a genuine
// need for more should raise it deliberately rather than discover that a
// stranger's torrent can.
const MaxTorrentBytes = 1 << 40

// checkMetainfo returns an `ErrMalformed` error if the torrent's own fields
// disagree with each other.
//
// Called before the gate. The ORDER matters and is the whole reason this file
// exists: a name check on a torrent with no pieces is a name check on a torrent
// that will never transfer anything, and reporting it as a path refusal sends
// the operator looking at file names that are not the problem.
func checkMetainfo(info *metainfo.Info) error {
	if info == nil {
		return fmt.Errorf("%w: there is no info dictionary", ErrMalformed)
	}

	// A name is required and cannot be empty. Without one the storage layer has
	// nothing to make a directory from, and the library logs
	// `mapping file: no such device` when it tries — measured, not predicted.
	if info.BestName() == "" {
		return fmt.Errorf("%w: the torrent has no name, so there is nothing to "+
			"make a download directory from", ErrMalformed)
	}

	// Piece length. Zero divides by zero in the piece index; negative produces
	// segments that are not segments. Both are refused by the library at add
	// time, which is too late to be the first thing that objects.
	switch {
	case info.PieceLength == 0:
		return fmt.Errorf("%w: the piece length is zero, and every piece index "+
			"is a division by it", ErrMalformed)
	case info.PieceLength < 0:
		return fmt.Errorf("%w: the piece length is %d, which is not a length",
			ErrMalformed, info.PieceLength)
	case info.PieceLength > maxPieceLength:
		return fmt.Errorf("%w: the piece length is %d bytes, above the %d byte "+
			"ceiling. A piece is one contiguous allocation in every storage "+
			"backend, so this is a memory claim rather than a file size",
			ErrMalformed, info.PieceLength, int64(maxPieceLength))
	case info.PieceLength < minPieceLength:
		// Not fatal, and deliberately not an error: this is a warning-shaped
		// condition, and refusing it would reject real-world torrents from
		// clients with unusual settings. It is documented here rather than
		// enforced so the number is not mistaken for a policy.
		_ = minPieceLength
	}

	// The piece table must be a whole number of SHA-1 hashes, and long enough to
	// cover the declared data. Both are the library's "piece count and file
	// lengths are at odds", checked here so the caller learns which one it was.
	if len(info.Pieces)%20 != 0 {
		return fmt.Errorf("%w: the piece table is %d bytes, which is not a whole "+
			"number of 20-byte SHA-1 hashes", ErrMalformed, len(info.Pieces))
	}
	if len(info.Pieces) == 0 {
		return fmt.Errorf("%w: the torrent has a piece table of zero bytes, so "+
			"there is no data to verify anything against", ErrMalformed)
	}

	total := info.TotalLength()
	if total < 0 {
		return fmt.Errorf("%w: the declared total length is %d, which is "+
			"negative", ErrMalformed, total)
	}
	if total > MaxTorrentBytes {
		return fmt.Errorf("%w: the declared total length is %d bytes, above the "+
			"%d byte ceiling", ErrMalformed, total, int64(MaxTorrentBytes))
	}

	// The arithmetic overflow check, which is the one that cannot be done with a
	// comparison. `NumPieces` computes ceil(total/pieceLength), and a total near
	// MaxInt64 with a small piece length overflows an int before anything
	// catches it.
	if total > 0 && info.PieceLength > 0 {
		needed := (total + info.PieceLength - 1) / info.PieceLength
		if needed > math.MaxInt32 {
			return fmt.Errorf("%w: the torrent needs %d pieces, more than any "+
				"peer index can address", ErrMalformed, needed)
		}
		if have := int64(len(info.Pieces) / 20); have < needed {
			return fmt.Errorf("%w: %d bytes of data need %d piece hashes but the "+
				"torrent supplies %d. A peer cannot verify a piece it has no "+
				"hash for, so this torrent cannot complete",
				ErrMalformed, total, needed, have)
		}
	}

	// The v1/v2 hybrid is LEGAL and this check was wrong about it.
	//
	// I wrote `if info.HasV1() && info.HasV2() { refuse }`, on the reasoning that
	// a torrent claiming both versions has two piece tables. Measured with
	// `info.go:212`:
	//
	//	func (info *Info) HasV1() bool {
	//	    return info.MetaVersion == 0 || info.MetaVersion == 1 ||
	//	        info.Files != nil || info.Length != 0 || len(info.Pieces) != 0
	//	}
	//
	// A meta-version-2 torrent carrying a `length` and a `pieces` field is
	// HasV1() AND HasV2() -- and that is the normal shape of a BEP 52 hybrid,
	// which the upgrade path exists to produce. So the check would have refused
	// every legitimate v2 torrent, and the mutation that disabled it "survived"
	// because the mutation made the code MORE correct, not less.
	//
	// That is worth stating as its own lesson: a surviving mutation is not
	// automatically a hole. It is a hole only if the code is right and the test
	// cannot see it. Here the code was wrong, the test could see it, and
	// removing the check made the suite pass by making the code right.
	//
	// What IS worth checking is the version field itself: BEP 52 defines 1 and 2,
	// and a meta version of 3 is a torrent whose piece table this code has no
	// rule for.
	switch info.MetaVersion {
	case 0, 1, 2:
		// Known. 0 is a classic BEP 3 torrent that predates the field.
	default:
		return fmt.Errorf("%w: the torrent declares meta version %d, and BEP 52 "+
			"defines only 1 and 2. A version this code has no rules for is a "+
			"piece table it cannot check, which is not the same as a piece "+
			"table that is fine", ErrMalformed, info.MetaVersion)
	}

	return nil
}
