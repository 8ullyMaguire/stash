package ed2kwire

import (
	"fmt"

	"github.com/monkeyWie/goed2k/protocol"
)

// Searching: asking a server which of its files match a name, and which sources
// have one.
//
// # WHY SEARCH IS OURS AND NOT THE LIBRARY'S
//
// goed2k has no search: its protocol package is opcodes, tags, framing and
// nodes.dat, and the absence is not an oversight I can lean on -- there is
// nothing to call. The search packet is built here.
//
// # THE TWO REQUESTS, AND WHY THEY ARE DIFFERENT SHAPES
//
// eDonkey2000 has two ways to ask, and picking the wrong one is a search that
// returns nothing forever:
//
//   - OP_SEARCHREQUEST (0x16) with a keyword tag. The server matches on
//     FILENAMES it holds. The response is OP_SEARCHRESULT (0x33) carrying
//     every matching file with its size, type and available-source count.
//   - The same opcode with a file-hash tag. The server looks for that exact
//     file across sources. The response is a DIFFERENT packet, and the one
//     that carries a source list per file.
//
// This file implements the first, because it is the one that can be exercised
// end to end without knowing a file that exists on the network. A search by
// hash is the next step, and it is NOT a variation of this one: the response
// shape differs, so "same request, different tag" would be wrong.
//
// # THE FRAMING IS THE SAME OFF-BY-ONE AS EVERY OTHER PACKET HERE
//
// The size field COUNTS the opcode byte, so frameBytes adds one. That has
// been this package's bug twice -- in the login and in the extended hello --
// so SearchRequest is written against frameBytes rather than assembling a
// header by hand, and TestTheSearchRequestFramesLikeEveryOtherPacket says so.

// opSearchRequest is OP_SEARCHREQUEST, 0x16.
//
// # 0x16 AND NOT 0x33
//
// 0x33 is OP_SEARCHRESULT -- what the server sends BACK. Sending it would be
// a client volunteering results, and the server would have no way to interpret
// a result set it never asked for.
const opSearchRequest byte = 0x16

// SearchRequest asks a server for files whose name matches a keyword.
//
// # WHAT THE SERVER MATCHES ON
//
// On the filename and the extension, both held in the string. A server does
// not do a content search, and a caller wanting one has the wrong packet.
type SearchRequest struct {
	// Keyword is the text to match against filenames. An empty keyword is
	// refused rather than sent: a server treats an empty string as a match
	// for everything, which returns the server's entire index and is a way to
	// be disconnected for asking a question nobody asked.
	Keyword string

	// MaxResults caps what this client will accept from one request. The
	// server chooses how much to send and the protocol has no field to ask
	// for less, so this bounds the RECEIVE side and not the send side.
	//
	// It exists because the response is unbounded on the wire: a keyword
	// like "." matches a great many filenames on a large server, and each
	// arrives as its own packet. Without a cap a single search can be made to
	// allocate without limit by a stranger who does not even have to mean it.
	MaxResults int
}

// DefaultMaxSearchResults is the cap used when a caller does not set one.
//
// Chosen to be generous enough that a real search for a real filename is not
// truncated, and small enough that a pathological keyword is bounded: 1000
// results at the size of one result packet is tens of megabytes at worst.
const DefaultMaxSearchResults = 1000

// Build renders the search request's payload: the keyword as a string tag.
//
// # WHY THE COUNT IS WRITTEN BY parseTagList's OWN SHAPE
//
// The tag list is a uint32 count followed by that many tags, which is what
// parseTagList reads. WriteTag and this count are the pair that
// TestARoundTripThroughOurOwnCodec exists for -- the extended hello hit the
// one-byte-count bug, and a count written at a different width than the reader
// expects is the same bug wearing a different tag.
func (r SearchRequest) Build() ([]byte, error) {
	if r.Keyword == "" {
		return nil, fmt.Errorf("%w: a search keyword is empty, and a server "+
			"reads an empty keyword as a request for its whole index",
			ErrRefused)
	}
	return encodeTagList(TagList{{
		ID:    tagIDSearchKeyword,
		Type:  tagTypeString,
		Value: append([]byte(r.Keyword), 0),
	}})
}

// Search asks a connected server for files matching a keyword.
//
// # IT IS SENT AFTER THE LOGIN AND THE HANDSHAKE
//
// A server allocates the session at login and applies the client's declared
// capabilities at the extended hello. A search sent before either is a search
// sent into a session that does not exist yet, and the failure is a server
// that ignores it.
//
// The handshake is not enforced here, because this package cannot observe
// whether SendExtHello was called -- a Server that recorded it would be a
// Server whose field is set by one call site and read by another, which is
// the same coupling the sentFirst flag exists to avoid. So the ordering is the
// caller's to get right, and it is stated here rather than checked badly.
func (s *Server) Search(req SearchRequest) error {
	payload, err := req.Build()
	if err != nil {
		return err
	}
	if err := s.sendFrame(protocol.EdonkeyHeader, opSearchRequest, payload); err != nil {
		return fmt.Errorf("sending the search request: %w", err)
	}
	return nil
}
