//go:build ed2klive

// The live source test: does a real peer accept our handshake and answer a
// part request? This is the transfer plan's step 5, and the only step that
// can genuinely fail.
//
// # IT REPORTS BOTH NUMBERS, AND THE DISTINCTION IS THE WHOLE POINT
//
// Measured 2026-09-28 against 85.17.116.222:6082, three separate facts that
// look alike from the outside and have nothing to do with each other:
//
//	299   server-supplied source handles in a live search
//	  3   of the first 25 accepted a TCP connection
//	  0   of those 3 completed our ed2k DialSource handshake
//
// An earlier probe of mine printed "LIVE" for the middle number. It was
// wrong: a TCP connect proves a socket opened and nothing about whether the
// peer speaks ed2k, so the label asserted the conclusion the measurement
// could not support. So the two are counted separately and reported
// separately, and NEITHER is reported as the other.
//
// # WHY IT FAILS AND DOES NOT SKIP
//
// The same rule as every other file behind this tag: a test that skips prints
// `ok` having proved nothing. Without ED2K_LIVE_SERVERS this FAILS with the
// instruction to set it.
//
// A FAILURE here is also not automatically a defect. On 2026-09-28 this test
// failed with ErrRefused on every reachable handle, which is a finding about
// the network rather than about the code — the peers exist and do not talk
// to us, and Kad source lookup is the protocol that would find peers that
// do. So the failure message says which of the two it is, and a caller can
// tell "our code is wrong" from "no peer answered" without reading the diff.
//
// How to run:
//
//	ED2K_LIVE_SERVERS=85.17.116.222:6082 \
//	  go test -tags ed2klive ./internal/ed2kwire/ -run TestLiveOnePart -v
package ed2kwire

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/stashapp/stash-plugin-p2pdownloader/internal/ed2k"
)

// liveSourceAttempts is how many source handles to try.
//
// Not all of them: a search returns hundreds and a TCP dial to a stranger's
// dead port costs the full dial timeout. 25 is a budget, and the assertion is
// about the ratio, so raising it makes the test slower and not more
// informative.
const liveSourceAttempts = 25

// liveSearchKeyword is what this test searches for.
//
// "ubuntu" because the capture used it and a result for it is reproducible
// against the same server. A small-file search would be better for a part
// request (only a one-part file can be verified in one window) and is what
// TestLiveASmallFileComesBackWhenItsSourceAnswers covers; this one settles
// the handshake question, which does not depend on the file's size.
const liveSearchKeyword = "ubuntu"

func TestLiveOnePartComesBackFromARealSource(t *testing.T) {
	servers := liveServers(t)
	addr := servers[0]

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	srv, err := Dial(ctx, addr)
	if err != nil {
		t.Fatalf("Dial(%s): %v", addr, err)
	}
	defer srv.Close()
	t.Logf("connected to %s: %d users / %d files", addr, srv.Users(), srv.Files())

	results := liveSearch(t, srv)
	t.Logf("%d results for %q", len(results), liveSearchKeyword)

	// Phase 1: TCP reachability. Counted and reported, and NOT called live.
	connectable := make([]SearchResult, 0, liveSourceAttempts)
	for i, r := range results {
		if i >= liveSourceAttempts {
			break
		}
		target := sourceAddr(r)
		c, err := net.DialTimeout("tcp", target, 3*time.Second)
		if err != nil {
			continue
		}
		_ = c.Close()
		connectable = append(connectable, r)
	}
	t.Logf("PHASE 1 (TCP connect only, proves a socket opened): %d of %d "+
		"handles accepted a connection", len(connectable), liveSourceAttempts)

	if len(connectable) == 0 {
		t.Fatalf("no source handle in the first %d accepted a TCP "+
			"connection, so no ed2k handshake was attempted.\n\n"+
			"This is a network fact, not a finding about this code: the "+
			"server offered %d handles and none of them is reachable "+
			"today. Re-run later, or against a different server via "+
			"ED2K_LIVE_SERVERS.", liveSourceAttempts, len(results))
	}

	// Phase 2: the ed2k handshake, which is the actual question.
	var refused, spoke, answered int
	var lastErr error
	for _, r := range connectable {
		target := sourceAddr(r)
		sctx, scancel := context.WithTimeout(ctx, 20*time.Second)
		src, err := DialSource(sctx, target)
		if err != nil {
			refused++
			lastErr = err
			scancel()
			continue
		}
		spoke++

		end := int64(PartSize)
		if r.Size < end {
			end = r.Size
		}
		ans, err := src.RequestPart(sctx, PartRequest{
			FileHash: r.Hash,
			Start:    0,
			End:      uint32(end),
		})
		if err == nil {
			answered++
			t.Logf("  %s ANSWERED: %d bytes for a %d-byte file (%q)",
				target, len(ans.Data), r.Size, r.Name)
		} else {
			lastErr = err
			t.Logf("  %s handshake OK, part request: %v", target, err)
		}
		_ = src.Close()
		scancel()
	}

	t.Logf("PHASE 2 (the real question): %d of %d TCP-reachable handles "+
		"completed our ed2k handshake; %d then answered a part request",
		spoke, len(connectable), answered)

	if answered > 0 {
		// A real part from a real peer. The transfer path is proven.
		return
	}

	// Nothing answered. Report WHICH failure it is, so the reader does not
	// have to infer it from a message.
	//
	// A handshake refusal means the peer did not speak ed2k to us. A part
	// request that failed after a good handshake means it did speak and
	// then declined. Those point at different things, and conflating them
	// sends triage to the wrong place.
	switch {
	case spoke == 0 && refused == len(connectable):
		t.Fatalf("none of the %d TCP-reachable source handles "+
			"completed our ed2k handshake.\n\n"+
			"Last error: %v\n\n"+
			"THE PEERS EXIST AND DO NOT TALK TO US. This is not "+
			"evidence that the source-side opcodes are wrong -- it "+
			"is evidence that no peer answered at all, and the "+
			"opcodes remain correct-as-cited and unconfirmed-in-use. "+
			"A server's UserID/Port is a HINT that a source is "+
			"there; it is right about TCP reachability and "+
			"unreliable about ed2k. Kad source lookup "+
			"(OP_KAD2_SEARCH_SOURCE_REQ) is the protocol that "+
			"resolves a hash to peers directly, and on this "+
			"evidence it is the blocking item.",
			len(connectable), lastErr)
	case spoke > 0:
		t.Fatalf("%d of %d handles completed the handshake but none "+
			"answered a part request.\n\nLast error: %v\n\n"+
			"THIS IS A DIFFERENT FINDING from a handshake refusal: "+
			"these peers spoke ed2k and then declined the file, "+
			"which is ErrNoFile or a short answer rather than a "+
			"wrong opcode. The transfer reached a real source and "+
			"the source did not have what we asked for.",
			spoke, len(connectable), lastErr)
	default:
		t.Fatalf("only %d of %d handles completed the handshake, and "+
			"none answered a part request. Last error: %v",
			spoke, len(connectable), lastErr)
	}
}

// TestLiveASmallFileComesBackWhenItsSourceAnswers is the part-fetch half, and
// it is separate because a SMALL file is the only one this client can verify
// in a single window.
//
// ed2ktransfer.FetchOnePart proves bytes against the FILE hash. A file of
// several parts can only be hashed once every part is in hand, so one window
// can only succeed for a file that fits in one part (9,728,000 bytes). A big
// file's part proves the wire and nothing about the hash, and conflating the
// two would let a wire proof be reported as a transfer proof.
//
// So this searches for small files and asks for a whole one.
func TestLiveASmallFileComesBackWhenItsSourceAnswers(t *testing.T) {
	servers := liveServers(t)
	addr := servers[0]

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	srv, err := Dial(ctx, addr)
	if err != nil {
		t.Fatalf("Dial(%s): %v", addr, err)
	}
	defer srv.Close()

	small := liveSmallFiles(t, srv)
	t.Logf("%d distinct files are at most one part (%d bytes)", len(small),
		PartSize)
	if len(small) == 0 {
		t.Fatalf("no file smaller than one part came back from %d "+
			"small-file searches. The server answered, so this is "+
			"its index rather than this client", len(liveSmallKeywords))
	}

	var lastErr error
	for i, r := range small {
		if i >= liveSourceAttempts {
			break
		}
		sctx, scancel := context.WithTimeout(ctx, 20*time.Second)
		src, err := DialSource(sctx, sourceAddr(r))
		if err != nil {
			lastErr = err
			scancel()
			continue
		}
		ans, err := src.RequestPart(sctx, PartRequest{
			FileHash: r.Hash,
			Start:    0,
			End:      uint32(r.Size),
		})
		if err != nil {
			lastErr = err
			_ = src.Close()
			scancel()
			continue
		}

		// The window came back. Now the claim that matters: are these
		// bytes the file the hash names?
		got, err := verifyOnePartAgainst(r, ans.Data)
		_ = src.Close()
		scancel()
		if err != nil {
			t.Fatalf("a source answered a part request for a %d-byte "+
				"file and the bytes did not hash to the file's own "+
				"hash: %v", r.Size, err)
		}
		t.Logf("VERIFIED: %q, %d bytes, hash %x", r.Name, got.bytes, got.hash)
		return
	}

	t.Fatalf("no small file's source answered. %d candidates tried, last "+
		"error: %v\n\nThis is the same network finding as the handshake "+
		"test, on a set of files chosen to be verifiable in one window. "+
		"It is reported separately because a part answer for a small "+
		"file would ALSO let the hash be checked, and that is the "+
		"one thing a big file's part cannot do.", len(small), lastErr)
}

// verified is what a proven part looked like.
type verified struct {
	bytes int
	hash  [16]byte
}

// verifyOnePartAgainst hashes a window and compares it to the file's hash.
//
// For a file of at most one part, the file hash IS the part hash: ehash.go's
// multi-part branch hashes the part hashes' first eight bytes, so the two
// coincide only in the one-part case. This is the claim ed2ktransfer makes,
// checked here against bytes that came from a stranger.
//
// # IT CALLS ed2k.HashBytes, AND NOT A LOCAL COPY OF THE HASH
//
// The dependency ed2kwire -> ed2k is the wrong direction for a non-test file
// and the right one for a test: internal/ed2k does not import ed2kwire, so
// this compiles, and ed2k.HashBytes is the authority for what an ed2k hash
// is. A local md4 in this file would be a second implementation of the hash
// whose only value is that it can disagree with the real one, and a
// disagreement here would look exactly like a lying peer.
func verifyOnePartAgainst(r SearchResult, data []byte) (verified, error) {
	if len(data) == 0 {
		return verified{}, errors.New("the source sent an empty window")
	}
	sum := ed2k.HashBytes(data)
	if [16]byte(sum) != r.Hash {
		return verified{}, fmt.Errorf("the %d bytes hash to %x and the "+
			"server's result says %x, so the peer is not holding the "+
			"file it advertised", len(data), sum, r.Hash)
	}
	return verified{bytes: len(data), hash: r.Hash}, nil
}

// liveSearchKeywords are searched for a one-part file.
//
// A small file is what makes a one-part transfer verifiable at all, so the
// keyword is the lever for file size. Extensions a real server holds both
// small and large copies of, and each is capped so a busy server cannot turn
// the test into a long read.
var liveSmallKeywords = []string{
	"nfo", "txt", "png", "jpg", "gif", "sfv", "cue",
}

// liveSearch runs one keyword search and returns what came back.
func liveSearch(t *testing.T, srv *Server) []SearchResult {
	t.Helper()
	results := liveSearchKeyword_(t, srv, liveSearchKeyword)
	if len(results) == 0 {
		t.Fatalf("a live search for %q returned nothing from %s. The "+
			"login succeeded, so an empty answer is the server's "+
			"index and not a failure of this client",
			liveSearchKeyword, srv.Addr())
	}
	return results
}

// liveSearchKeyword_ sends one search and collects its results.
func liveSearchKeyword_(t *testing.T, srv *Server, keyword string) []SearchResult {
	t.Helper()
	if err := srv.Search(SearchRequest{Keyword: keyword, MaxResults: 100}); err != nil {
		t.Fatalf("Search(%q): %v", keyword, err)
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if err := srv.conn.SetReadDeadline(
			time.Now().Add(5 * time.Second)); err != nil {
			t.Fatalf("bounding the result read: %v", err)
		}
		hdr, payload, err := readFrame(srv.conn)
		if err != nil {
			if isTimeout(err) {
				return nil // the server went quiet: no results
			}
			t.Fatalf("reading the search result from %s: %v",
				srv.Addr(), err)
		}
		if hdr.Packet != opSearchResultLive {
			continue
		}
		plain, err := inflateExtHello(payload)
		if err != nil {
			t.Fatalf("inflating the result for %q: %v", keyword, err)
		}
		results, err := DecodeSearchResult(plain)
		if err != nil {
			t.Fatalf("decoding the LIVE result for %q: %v", keyword, err)
		}
		return results
	}
	return nil
}

// liveSmallFiles searches for files that fit in one part, deduplicated.
func liveSmallFiles(t *testing.T, srv *Server) []SearchResult {
	t.Helper()
	var small []SearchResult
	seen := map[[16]byte]bool{}
	for _, kw := range liveSmallKeywords {
		if len(small) >= 40 {
			break
		}
		for _, r := range liveSearchKeyword_(t, srv, kw) {
			if seen[r.Hash] || r.Size <= 0 || r.Size > PartSize {
				continue
			}
			seen[r.Hash] = true
			small = append(small, r)
		}
	}
	return small
}

// sourceAddr is a result's source handle as an address.
//
// A server reports where it believes a source can be had, so this is a CLAIM
// and the whole live test is a measurement of how often the claim is usable.
func sourceAddr(r SearchResult) string {
	ip := net.IPv4(byte(r.UserID), byte(r.UserID>>8),
		byte(r.UserID>>16), byte(r.UserID>>24))
	return net.JoinHostPort(ip.String(), fmt.Sprint(r.Port))
}

// opSearchResultLive is OP_SEARCHRESULT, 0x33.
//
// Restated here rather than imported so this file names the packet it is
// waiting for; the server file is the authority and search.go already pins
// the pairing of 0x16 (ask) and 0x33 (answer).
const opSearchResultLive byte = 0x33
