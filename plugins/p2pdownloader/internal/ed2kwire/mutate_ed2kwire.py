"""Mutation harness for internal/ed2kwire.

Four verdicts, and the middle two are the point:

    KILLED     a test failed
    COVERED    the whole suite passed -- a LOWER LAYER already refuses this input
    SURVIVED   the suite passed AND the mutation is reachable, which is a hole
    SKIP       the probe did not compile, or its pattern is no longer in the file

Build errors are SKIP, never SURVIVED. A probe that does not compile is a defect
in the PROBE; scoring it as a hole sends the next reader into the tests looking
for a bug that is in this file. That inversion has bitten twice in this repo.

EVERY PROBE IS BOUNDED, EVERY PROBE RESTORES ITS FILE, AND NOTHING RUNS THE
SUITE UNBOUNDED. All three are here because of a real incident: a sweep of
internal/library was interrupted by a SIGTERM partway through and the probe in
flight left a mutation applied to integrate.go.

RUN IT WITH `python3 internal/ed2kwire/mutate_ed2kwire.py` AND READ THE EXIT
CODE.

    0  nothing survived and nothing was malformed
    1  a survivor -- go and look at a TEST
    2  a malformed probe -- go and look at THIS FILE

A single non-zero code sends the reader to the shorter list, so the two are kept
apart. `... | tail -40; echo $?` reports TAIL's status and has already produced
a clean-looking run of a sweep that had seven survivors.

READ THE EXIT CODE OF THE THING, NOT OF THE PIPE.

# EVERY PROBE HERE IS A BUG THIS PACKAGE ACTUALLY HAD

That is not a boast about coverage, it is the reason this file exists. All five
of the mutations below were live bugs in the handshake, found by the live tests
in live_test.go, and every one of them passed the entire hermetic suite first
because the fake servers had been written to agree with the code.

The two that are worth reading the labels for:

  - "the size no longer counts the opcode" and "obfuscate stops growing the
    size for the seed" are the same off-by-one, one layer apart. The header
    must describe what is on the wire; both mutations produce a packet that
    our own decoder reads back correctly and a real server desynchronises.

  - "mustRead discards the read error again" is the subtlest. It converts a
    read deadline into an apparent end-of-stream, so every quiet server
    reports EOF and every dial fails. It was a `_` on a returned error.
"""

import json
import os
import re
import subprocess
import sys

PKG = "internal/ed2kwire"
ROOT = os.path.dirname(os.path.abspath(__file__))
# TWO dirnames, like every other harness here: this file sits at
# <plugin>/internal/ed2kwire, so one climbs to <plugin>/internal and two
# reaches <plugin>, which is where go.mod lives.
REPO = os.path.dirname(os.path.dirname(ROOT))

SERVER = os.path.join("internal", "ed2kwire", "server.go")
OBFUSCATE = os.path.join("internal", "ed2kwire", "obfuscate.go")
TAG = os.path.join("internal", "ed2kwire", "tag.go")
EXTHELLO = os.path.join("internal", "ed2kwire", "exthello.go")
SEARCH = os.path.join("internal", "ed2kwire", "search.go")
SEARCHRESULT = os.path.join("internal", "ed2kwire", "searchresult.go")
SOURCE = os.path.join("internal", "ed2kwire", "source.go")

# The suite's own timeout. Without it, a mutation that makes a read block
# forever hangs the sweep rather than failing it, and a hang is not a kill.
# 200s is generous for a suite that takes about 10s.
SUITE_TIMEOUT = "200s"

# Per-probe bound, shorter than SUITE_TIMEOUT so a probe that hangs is reported
# as its own failure instead of taking the whole sweep with it.
PROBE_TIMEOUT = 150

# A pattern that is not in the file means the probe has gone stale, and a stale
# probe silently becomes a no-op that "passes". Every probe below is checked
# against the CURRENT source before it is applied, and a miss is a SKIP with
# the pattern printed -- never a silent success.
MUTATIONS = [
    # ---- the size arithmetic, which is the same bug twice ----
    # The header's Size field COUNTS the opcode byte, and `SizePacket()` —
    # which is what a reader subtracts — is `Size - 1`. Writing the body
    # length instead makes every packet one byte short, which our own decoder
    # cannot detect because it subtracts the same one.
    ("server: the size no longer counts the opcode byte",
     SERVER, "Size:     int32(len(body)) + 1, // +1 for the opcode byte",
     "Size:     int32(len(body)),", "."),

    # The other half of the same mistake: obfuscate adds four seed bytes to
    # the wire without adding four to the size, so a reader consumes 24 of the
    # 28 bytes sent and lands three bytes into the login body.
    ("obfuscate: the size no longer grows for the four seed bytes",
     OBFUSCATE, "size += obfuscationSeedSize", "size += 0",
     "LoginRequest"),

    # Only the first packet is obfuscated. Obfuscating a later one gets the
    # connection dropped mid-session after a login that otherwise worked.
    ("obfuscate: every packet is obfuscated, not just the first",
     SERVER, "\ts.sentFirst = true\n\tif _, err := s.conn.Write(obfuscated); err != nil {",
     "\tif _, err := s.conn.Write(obfuscated); err != nil {", "."),

    # ---- the handshake, which is where the real bugs lived ----
    # THE CLIENT SPEAKS FIRST. The ed2k protocol is client-first: the client
    # sends OP_LOGINREQUEST and only then does the server answer. The first
    # version of this read the server's hello first and answered it, which is
    # the KAD UDP shape, and which deadlocks against a real server: the server
    # waits for our login and we wait for its hello.
    ("server: the login request is no longer sent first",
     SERVER, "\tif err := srv.sendFirstPacket(); err != nil {",
     "\tif err := error(nil); err != nil {",
     "Login|ServerMessage|ServerGUID|SessionToken|Status"),

    # A login is confirmed by the ABSENCE of a refusal. The first version
    # waited for an OP_HELLO, which does not exist in the ed2k login
    # response, so every server timed out after having plainly answered.
    # Mutating heardAnything back to false is that same bug in miniature:
    # the server is talking and the client calls it silent.
        ("server: a server that spoke is still called silent",
     SERVER, "s.heardAnything = true\n\t\theard = true",
     "\t\t_ = heard", "SpeaksIsNeverCalledSilent|NeverSpeaksIsRefused|StaysSilent"),

    # ---- the counts, which were swapped ----
    # 0x40 is the server's OWN totals and 0x34 is a larger network count.
    # Reading 0x40 as an identifier produced a "GUID" that was the user
    # count followed by the server's address, changing on every connection.
    #
    # The 8 bytes are recycled into the count fields byte-swapped, so the
    # mutation COMPILES and produces plausible numbers rather than an error
    # -- a decode that looks right and is 8x wrong is the bug this guards.
    ("server: 0x40's counts are read big-endian",
     SERVER, "s.totalUsers = int32(binary.LittleEndian.Uint32(payload[0:4]))\n\t\ts.totalFiles = int32(binary.LittleEndian.Uint32(payload[4:8]))",
     "s.totalUsers = int32(binary.BigEndian.Uint32(payload[0:4]))\n\t\ts.totalFiles = int32(binary.BigEndian.Uint32(payload[4:8]))",
     "Counts"),

    # The two count packets are NOT interchangeable. A test asserting only
    # "the counts arrived" passes against a decoder that has them swapped,
    # because both numbers are plausible.
    ("server: both count packets are read the same way",
     SERVER, "s.users = int32(binary.LittleEndian.Uint32(payload[0:4]))\n\t\ts.files = int32(binary.LittleEndian.Uint32(payload[4:8]))",
     "s.users = s.totalUsers\n\t\ts.files = s.totalFiles",
     "Counts"),

    # ---- the errors, which is where the subtlest bug was ----
    # mustRead threw away the error that stopped the read, so a read that
    # ended because the DEADLINE EXPIRED was indistinguishable from a peer
    # that closed. Every quiet server reported EOF and every dial failed.
    ("server: mustRead discards the read error again",
     SERVER, "read, err := io.ReadFull(r, buf)\n\treturn buf[:read], err",
     "read, _ := io.ReadFull(r, buf)\n\treturn buf[:read], nil", "."),

    # "Quiet after speaking" is a finished conversation; "silent" is a server
    # that never accepted us. Both end in the same read error, so treating a
    # timeout as success hands back a Server whose first read will block.
    #
    # The LIVE guard on "nothing was heard", in readLoginConfirmation. This
    # replaced a probe on an identical-looking guard in drainFacts, which
    # survived every mutation because it was DEAD CODE: drainFacts is only
    # ever called on the path where a packet has already been heard, so its
    # copy of the check could never fire. Deleting a line nothing can reach
    # changes no test result, and that is the only reason the mutation
    # harness could see it.
    #
    # The replacement is the same check in the one function that can reach
    # it, so the guarantee is still guarded and now is actually testable.
    ("server: a server that never spoke is called silent",
     SERVER, "if !heard {\n\t\t\t\t\treturn fmt.Errorf(\"the server accepted the connection \"+\n\t\t\t\t\t\t\"and then said nothing at all: %w\", err)\n\t\t\t\t}",
     # BOTH variables have to be consumed. Removing the guard leaves `err`
     # unused in that branch, and removing its only reader leaves `heard`
     # declared and never read -- so the replacement consumes both. A probe
     # that does not compile is a defect in the PROBE, and the harness says
     # so rather than scoring it as a hole; this one was found that way.
     "\t\t\t\tif false { _ = err; _ = heard }",
     "NeverSpeaksIsRefused|NeverSpoke|SaysNothingAtAll"),

    # ---- the deadlines, which were guesses ----
    # 400ms passed every hermetic test because a local fake replies in
    # microseconds. A real server measured 3093ms to its first byte, so this
    # refused every connection on the network in 0.4s.
    ("server: the burst deadline is back to the value a loopback fake allows",
     SERVER, "var burstDrainTimeout = 8 * time.Second",
     "var burstDrainTimeout = 400 * time.Millisecond", "RealDeadlines"),

    ("server: the dial deadline no longer exceeds both drains",
     SERVER, "var dialTimeout = 40 * time.Second",
     "var dialTimeout = 1 * time.Millisecond", "RealDeadlines|Deathlines"),

    # ---- the tag decoder, which the library gets wrong ----
    # The wire is [type][id][value] with the type masked. The library's writer
    # uses [type|0x80][id][value], and a real server drops every tag encoded
    # that way -- silently, which looks exactly like a server that is down.
    ("tag: the type byte is no longer masked",
     TAG, "Type: payload[0] & 0x7F,", "Type: payload[0],", "."),

    # A Str-family tag's TYPE BYTE IS ITS LENGTH. The live server's own three
    # strings are 0x19/0x1B/0x14 — nine, eleven and four bytes — and that
    # agreement across three different lengths is what settled the encoding.
    ("tag: the Str-family length is no longer read from the type byte",
     TAG, "\t\tn := int(tag.Type - tagTypeStrBase)", "\t\tn := 1", "."),

    # This is the one that is SILENT. The value was computed correctly, used
    # for the consumed-byte count, and then not attached to the Tag — so every
    # parse succeeded and every accessor returned empty, with no error
    # anywhere. A test asserting only "ten tags parsed" passes.
    ("tag: the decoded value is no longer attached to the tag",
     TAG, "\ttag.Value = value\n\treturn tag, 2 + len(value), nil",
     "\treturn tag, 2 + len(value), nil", "."),

    # The count arrives from a stranger. Unbounded, a count of four billion in
    # a twenty-byte payload makes a four-billion-element slice before a single
    # byte is read — this probe dies with a fatal out-of-memory, which IS the
    # kill.
    ("tag: the tag count is no longer bounded before allocating",
     TAG, "\tif int(count) > len(rest)/2 {", "\tif false {", "TagCount"),

    # An unknown wire type has an UNKNOWN WIDTH, so every tag after it is
    # unreachable. Skipping it with a guessed width desynchronises the rest of
    # the list, which is worse than refusing.
    ("tag: an unknown wire type is skipped rather than refused",
     TAG, "\tdefault:\n\t\treturn Tag{}, 0, fmt.Errorf(\"tag 0x%02X has wire type",
     "\tdefault:\n\t\tif true {\n\t\t\treturn Tag{}, 2, nil\n\t\t}\n\t\treturn Tag{}, 0, fmt.Errorf(\"tag 0x%02X has wire type",
     "."),

    # The protocol byte is checked BEFORE the size, so a captive portal's HTTP
    # reply is reported as "not an ed2k server" rather than as an oversized
    # packet — the size check would otherwise answer the wrong question well.
    ("server: the size is checked before the protocol byte",
     SERVER, "\tif err := checkProtocolByte(header.Protocol); err != nil {\n\t\treturn header, nil, err\n\t}",
     "\tif false {\n\t}\n\tif size := header.SizePacket(); size < 0 || size > maxPacketSize {\n\t\treturn header, nil, err\n\t}",
     "NotAnED2K|NotAServer|Captive|Portal"),

    # A size that arrives from a stranger chooses our memory use without this.
    ("server: a packet size above the cap is no longer refused",
     SERVER, "if size := header.SizePacket(); size < 0 || size > maxPacketSize {",
     "if size := header.SizePacket(); size < 0 {", "."),

    # A seed of zero bytes is not a seed: a constant obfuscation is trivially
    # fingerprintable, which is what the mechanism exists to prevent, and a
    # fallback here produces a client every server drops with no error.
    # The source now reads through a parameter rather than calling rand.Read
    # directly, so that the failure path is reachable from a test at all. The
    # probe follows it: disabling the check on randRead is the same bug.
    ("obfuscate: the seed falls back to zeros when randomness fails",
     OBFUSCATE, "\tif _, err := randRead(seed[:]); err != nil {",
     "\tif _, err := randRead(seed[:]); false && err != nil {", "."),
    # ---- the extended hello, which is the step between login and search ----
    #
    # Every probe below is a bug this codec actually had on the day it was
    # written, and the count field is the one worth reading twice: the first
    # version wrote it as ONE byte, parseTagList reads FOUR, and every decode
    # then spanned the count byte and the first three bytes of the first tag.
    # A two-tag list came out claiming 100,762,114 tags -- and the error
    # named parseTagList, which was correct, and never the writer, which was
    # not. A loud failure in the wrong file is still the wrong file.
    # Compiles: a four-byte buffer with only the low byte set, rather than a
    # one-byte write. The first version of this probe replaced the whole
    # statement with plain.WriteByte, which left encoding/binary unused and
    # so failed to BUILD -- the harness correctly called that a malformed
    # probe rather than a survivor, and the fix is to keep binary in use.
    # # THE COUNT PROBE IS NOW A SIZE PROBE, AND THAT IS THE POINT
    #
    # The first version of this probe replaced PutUint32 with a single
    # plain.WriteByte -- which did not compile, because it left
    # encoding/binary unused. The second kept binary in use by writing
    # count[0] and leaving the other three bytes of the [4]byte at zero,
    # and it SURVIVED.
    #
    # It survived because it is not the bug. A one-byte count and a
    # four-byte count of the same value, written into a zero-initialised
    # array, produce IDENTICAL bytes. There is no way to express "one byte
    # instead of four" as a change to the wire, because on the wire the two
    # are the same thing.
    #
    # The real bug was writing a one-byte count and NOT padding it, and that
    # is a change to the LENGTH of what follows: the count byte was
    # immediately followed by the first tag. So the probe is on the byte the
    # reader lands on next.
    ("exthello: the count is followed by the first tag instead of 3 zero bytes",
     EXTHELLO, "var count [4]byte",
     "var count [1]byte; count[0] = 1; _ = binary.LittleEndian.Uint32; _ = count[0]",
     "TagCountIsWrittenAsFourBytes|ARoundTrip"),

    # The length prefix is the other half of the same class of bug. A string
    # tag's Value includes its own uint16 length, so writing the value bare
    # makes the reader take the first two bytes of the VALUE as a length --
    # and it reported "claims a 12406-byte string", which is 0x306E, the
    # first two bytes of "v0.60a". A confident, specific, wrong number.
    ("tag: a length-prefixed string is written without its length",
     TAG, "if wireType == tagTypeString {", "if false {",
     "ARoundTrip"),

    # Without the high bit the reader refuses its own output. The failure is
    # silent in a test that only counts tags, which is why the round trip
    # checks the type byte explicitly.
    ("tag: the type byte's high bit is not set on write",
     TAG, "w.WriteByte(wireType | 0x80)", "w.WriteByte(wireType)",
     "TheWrittenTagBytesCarryTheHighBit"),

    # # THE INFLATION BOUND
    #
    # Removing the limit lets a few hundred compressed bytes allocate
    # gigabytes. A limit applied AFTER inflating has already allocated the
    # bomb, so this probe also checks that LimitReader is what is removed --
    # changing the +1 would leave the read unbounded and the check one byte
    # too tight to matter.
    # The +1 is not decoration: the reader stops at the limit, so the read
    # returns limit+1 bytes and the check sees one byte too many. Drop it and
    # a payload of EXACTLY maxExtHelloInflated inflated bytes comes back
    # looking like it fit -- so the check has to be > and not >=, and neither
    # is right without the other.
    ("exthello: the reader stops at the limit with no room for the check",
     EXTHELLO, "io.LimitReader(zr, maxExtHelloInflated+1)",
     "io.LimitReader(zr, maxExtHelloInflated)",
     "ExactlyTheLimitIsAccepted"),

    ("exthello: the bomb is bounded but the post-read check is gone",
     EXTHELLO, "if len(plain) > maxExtHelloInflated {", "if false {",
     "CompressionBomb"),

    # # THE HEADER CHECKS ARE NOT PROBED, BECAUSE THEY ARE NOT THERE
    #
    # The first version of inflateExtHello validated the zlib header itself --
    # the compression method nibble, then the multiple-of-31 rule -- and both
    # were probed here. Both SURVIVED deletion, which is the interesting part:
    # zlib.NewReader validates the same header and this function already
    # wraps its error in ErrNotZlib, so the explicit checks were a second
    # opinion on an answered question. They have been removed, and these two
    # probes with them.
    #
    # A probe for deleted code is worse than no probe: it fails to apply, and
    # the harness reports that as a defect in itself rather than as a fact
    # about the code.

    # There is NO probe for the two-byte length guard that used to be here,
    # because the guard is gone.
    #
    # It was probed three times and survived every time: zlib.NewReader
    # refuses a one-byte payload with "unexpected EOF", and this function
    # wraps that in ErrNotZlib, so the behaviour the guard provided was
    # already the behaviour the reader provided. The panic that seemed to
    # justify the guard came from a probe calling DecodeExtHello directly --
    # which is the same entry point, and did not panic once the real
    # sequence was run rather than assumed.

    ("exthello: more tags than the limit are accepted",
     EXTHELLO, "if len(tags) > maxExtHelloTags {", "if false {",
     "MoreTagsThan"),
    # ---- the search request, which is a plain packet where the extended
    # ---- hello is a compressed one ----
    #
    # The compressor is the probe that matters. A search tag list is PLAIN,
    # and routing it through EncodeExtHello produces a packet a server
    # cannot read -- answered with silence, with nothing in this package
    # pointing at the cause. That is the same shape as the OP_HELLO mistake
    # this package made against every real server, so the probe that would
    # have caught it gets to exist.
    ("search: the request is compressed like an extended hello",
     SEARCH, "return encodeTagList(TagList{{",
     "return EncodeExtHello(TagList{{", "SearchRequestIsNotCompressed"),

    # The count is four bytes, as parseTagList reads. Third packet to carry
    # a tag list in this package, and the second to have gotten the width
    # wrong on a first attempt.
    ("search: the tag count is written as a byte again",
     TAG, "binary.LittleEndian.PutUint32(count[:], uint32(len(tags)))",
     "count[0] = byte(len(tags)); _ = binary.LittleEndian.Uint32",
     "TagCountOverTwoFiftyFiveUsesAllFourBytes"),

    # A string tag carries its own uint16 length. Writing the value bare
    # makes the reader take the first two bytes of the KEYWORD as a length,
    # and the resulting error names a string length nobody chose.
    ("search: a length-prefixed string is written without its length",
     TAG, "if wireType == tagTypeString {", "if false {",
     "KeywordSurvives|SearchRequestIsNotCompressed"),

    # The type byte's high bit marks a name-carrying tag. A round trip
    # cannot see this, because parseTag masks it off -- so the byte-level
    # assertion is what has to carry it.
    ("search: the type byte's high bit is not set on write",
     TAG, "w.WriteByte(wireType | 0x80)", "w.WriteByte(wireType)",
     "TheWrittenTagBytesCarryTheHighBit"),

    # An empty keyword is a request for the server's whole index. It is
    # refused, and a caller that does not get refused is disconnected for
    # asking a question nobody asked.
    ("search: an empty keyword is sent anyway",
     SEARCH, 'if r.Keyword == "" {', "if false {",
     "EmptyKeyword"),

    # The NUL terminator is part of the wire form. Without it the server
    # reads the keyword as running into whatever follows it, and the
    # length-prefix assertion above is what notices.
    ("search: the keyword is not NUL-terminated",
     SEARCH, "Value: append([]byte(r.Keyword), 0),", "Value: []byte(r.Keyword),",
     "TheKeywordIsNULTerminated"),

    # 0x33 is OP_SEARCHRESULT -- what the server sends BACK. Sending it
    # is a client volunteering results nobody asked for.
    ("search: the opcode is the RESULT opcode",
     SEARCH, "const opSearchRequest byte = 0x16", "const opSearchRequest byte = 0x33",
     "SearchOpcodeIsNotTheResultOpcode"),
    # ---- the search RESULT, decoded from a REAL capture ----
    #
    # These probes are different in kind from every other one here. The
    # request side round-trips through our own encoder; this side has no
    # encoder at all, because a stranger writes the bytes. So the tests
    # cannot be made to agree with a wrong decoder, and a survivor is
    # either a hole or a detail the capture happens not to exercise.
    #
    # The fixture is testdata/searchresult_live.bin: a real OP_SEARCHRESULT
    # from 85.17.116.222:6082, 27,950 compressed bytes inflating to 40,828
    # and holding 299 results.

    # THE 22 BYTES. A 16-byte read is the obvious boundary and is wrong:
    # it lands two bytes early and the next count comes out as 988,510,410.
    ("searchresult: a result's file ID is 16 bytes, not 22",
     SEARCHRESULT, "const fileIDLen = 16 + 4 + 2", "const fileIDLen = 16",
     "EachResultIsFollowedByTwentyTwoBytes|GoldenFirstResult"),

    # The header is 26 bytes, not 24, because the file ID it carries is 18
    # and not 16. Getting it wrong puts the first tag count at the wrong
    # offset and reports 332,452 tags.
    ("searchresult: the header is 24 bytes, not 26",
     SEARCHRESULT, "const searchResultHeaderLen = 4 + 4 + 18",
     "const searchResultHeaderLen = 4 + 4 + 16",
     "CapturedSearchResultDecodes|StrFamilyTypeByte"),    # A result with no room for its file ID ends the list. Making it an
    # error instead refuses 299 real results over one trailing byte, which
    # is the failure the first version had.
    ("searchresult: no room for a file ID is an error rather than the end",
     SEARCHRESULT, """		if off+fileIDLen > len(plain) {
			break
		}""", """		if off+fileIDLen > len(plain) {
			return nil, fmt.Errorf("search result %d has no room for "+
				"its file ID", len(results)+1)
		}""",
     "TruncatedResultIsRefused"),

    # The port is the confirmation that the 22-byte layout is right, and a
    # decoder that stops before the port cannot be shown to have read it.
    # NO PROBE FOR THE RESULT LOOP'S CONDITION, and its absence is the point.
    #
    # There was one, and it survived. Replacing `for off+4 <= len(plain)`
    # with `for off < len(plain)` leaves every test green: the capture
    # decodes to the same 299 results with the same last name and the same
    # last port, because the in-loop `break` -- not the condition -- is
    # what ends the list when a result has no room for its file ID.
    #
    # So the condition is redundant with the break, and a probe that cannot
    # be killed is not measuring anything. It is gone rather than pointed at
    # a weaker assertion. The same reasoning deleted the wider
    # `off+4+fileIDLen` version earlier, and both facts are recorded in the
    # comment above the loop in searchresult.go.
    #
    # What this does NOT say is that the condition is unnecessary to keep.
    # It says the CAPTURE cannot tell the two apart. A future change that
    # removed the in-loop break would have two silent failures stacked, and
    # the comment at the loop is the thing standing between that and a
    # subtly wrong decoder.

    ("searchresult: the port is not read from the file ID",
     SEARCHRESULT, "r.Port = binary.LittleEndian.Uint16(plain[off+20 : off+22])",
     "r.Port = 0",
     "EachResultIsFollowedByTwentyTwoBytes|GoldenFirstResult|"
     "CapturedSearchResultDecodes|LastResultIsComplete"),

    # The hash is the file's identity. A decoder that reads it from the
    # wrong offset produces 299 DIFFERENT wrong values, so a count-based
    # test sees nothing wrong -- which is why the golden test pins it as
    # hex.
    ("searchresult: the hash is not read from the file ID",
     SEARCHRESULT, "copy(r.Hash[:], plain[off:off+16])",
     "copy(r.Hash[:], plain[off:off+8])", "CapturedHashIsSixteenBytes|GoldenFirstResult"),
    # ---- the SOURCE handshake ----
    #
    # A source is a peer, reached the same way a server is. Nothing here
    # can prove the source PROTOCOL is right -- no live source has been
    # spoken to yet, and step 5 of the transfer plan is where that happens
    # for the first time. What these probes check is the shape of our own
    # first packet and the two things the handshake is allowed to refuse.

    # THE ONE THAT MATTERS MOST. The first version of this loop returned
    # success on ANY timeout, reasoning that "the peer spoke and then went
    # quiet" is the normal shape of a source handshake. True of a peer that
    # spoke -- and it made a peer that said NOTHING indistinguishable from
    # a completed handshake. The test found it as "a peer that accepted
    # the connection and then said nothing was reported as a successful
    # handshake", which is a hang with no error anywhere.
    ("source: quiet is accepted even if the peer never spoke",
     SOURCE, "if !heard {", "if false && !heard {",
     "ASourceThatNeverSpeaksIsRefused"),

    # THIS ONE SURVIVES, AND IT IS DOCUMENTED AS SURVIVING.
    #
    # Removing the connection deadline changes no observable behaviour in
    # this step, because readHandshakeAnswer sets its own read deadline on
    # every iteration -- the reads were already bounded. The line is kept
    # anyway, and the comment at it in source.go says why: it covers the
    # WRITE, which is the one operation nothing later bounds, and step 2's
    # part requests send up to 9500 bytes where a first packet sends 28.
    #
    # A probe that survives because it guards a real risk not yet
    # reachable is a different thing from one that survives because it is
    # redundant, and the difference is worth stating. Deleting the call
    # would turn this into the second kind.
    # ^ EXPECTED TO SURVIVE. See EXPECTED_SURVIVORS below and the comment
    #   above this probe.
    ("source: no deadline is set on the connection itself",
     SOURCE, "if err := conn.SetDeadline(time.Now().Add(dialTimeout)); err != nil {",
     "if err := error(nil); err != nil {",
     "ASourceThatNeverSpeaksIsRefused|FloodingPeerIsBounded"),

    # The marker opcode. A peer that does not see 0x01 does not know to
    # de-obfuscate, and drops the connection with nothing to report.
    ("source: the first packet is not marked as obfuscated",
     SOURCE, "obfuscated, err := obfuscate(frame, seed)",
     "obfuscated, err := obfuscate(frame, seed); obfuscated[5] = opLoginRequest",
     "TheSourceFirstPacketIsActuallyObfuscated|TheSourceFirstPacketIsTheSameObfuscated"),

    # The user hash is sixteen ZERO bytes and not a random one. A random
    # hash would make this client a different identity on every connection,
    # which reads as deliberate and is not.
    ("source: the handshake sends a random user hash",
     SOURCE, "Hash: [16]byte{},",
     "Hash: [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},",
     "TheSourceFirstPacketIsTheSameObfuscatedLogin"),

    # The body is built by loginRequest.body, and it is 24 bytes: a 16-byte
    # hash, a FOUR-byte port, and a 4-byte tag count. Dropping two bytes is
    # the exact failure of writing that port as a uint16, which is the bug
    # this package's own server login had -- a packet two bytes short,
    # which a peer reads as a tag count of whatever follows, with no error
    # at either end.
    #
    # The mutation appends or slices AFTER body() rather than replacing the
    # call, so `req` stays used. The two forms that rebuilt the body from
    # nothing left `req` declared and unused, and the harness correctly
    # reported them as SKIP -- a defect in the probe, not a hole.
    ("source: the handshake is not built through loginRequest.body",
     SOURCE, "body, err := req.body()",
     "body, err := req.body(); body = body[:len(body)-2]",
     "TheSourceFirstPacketIsTheSameObfuscatedLogin"),
]



# EXPECTED_SURVIVORS are probes that are KNOWN not to be killable, each with
# the reason, so the exit code can still mean "no unexplained hole".
#
# # WHY THIS IS A LIST AND NOT A TOLERATED COUNT
#
# A count is not reviewable. Three survivors and "you may tolerate two" does
# not say WHICH two, and the next run silently tolerates a different two. A
# named allowlist does, and adding an entry is a visible act with a reason
# attached to it.
#
# # AND A PROBE MAY ONLY BE HERE IF IT GUARDS A REAL RISK
#
# The distinction that matters is between a line that is REDUNDANT -- nothing
# depends on it, so deleting it changes nothing -- and a line that guards a
# risk that is NOT YET REACHABLE. The second belongs here. The first does not:
# it should be deleted, as two other guards in this package were.
EXPECTED_SURVIVORS = {
    "source: no deadline is set on the connection itself":
        "bounds the WRITE, which nothing later bounds; a 28-byte first "
        "packet never blocks (measured 17us) so no test can observe it, and "
        "step 2 sends up to 9500 bytes where this step sends 28",
}


def tests_matching(run_pat):
    """Return the names of tests the -run pattern actually selects.

    # WHY THIS EXISTS

    A probe whose -run pattern matches no test runs none of the suite, the
    run passes, and the probe is scored SURVIVED. That is the worst possible
    verdict for a probe that is not testing anything -- it puts a dead probe
    in the same list as a genuine hole, and the reader cannot tell them apart
    without re-deriving what -run does.

    It happened twice in this file. Both times the probe guarded a flag
    correctly and the pattern still named a test that had been renamed, so two
    probes reported SURVIVED for guards that were in fact covered by tests
    that did pass.

    A pattern is a claim about which tests guard a line. Checking it costs one
    subprocess; not checking it costs a reader an afternoon.
    """
    try:
        p = subprocess.run(
            ["go", "test", "./" + PKG + "/", "-list", ".", "-count=1"],
            cwd=REPO, capture_output=True, text=True,
            env=dict(os.environ, GOFLAGS="-mod=mod"), timeout=60)
    except subprocess.TimeoutExpired:
        return None
    names = [ln.strip() for ln in p.stdout.splitlines()
             if ln.strip().startswith("Test")]
    return [n for n in names
            if any(re.search(alt, n) for alt in run_pat.split("|"))]


def run_suite(run_pat):
    """Run the package's tests and return (output, timed_out, build_failed)."""
    env = dict(os.environ, GOFLAGS="-mod=mod")
    try:
        p = subprocess.run(
            ["go", "test", "./" + PKG + "/", "-run", run_pat,
             "-count=1", "-timeout", SUITE_TIMEOUT],
            cwd=REPO, capture_output=True, text=True, env=env,
            timeout=PROBE_TIMEOUT)
    except subprocess.TimeoutExpired:
        return "", True, False
    out = p.stdout + p.stderr
    build_failed = ("build failed" in out or "[build" in out
                    or "cannot use" in out or "undefined:" in out
                    or "syntax error" in out)
    return out, False, build_failed


def main():
    # A file's ORIGINAL content, kept in memory. A sweep interrupted at any
    # point still restores every file, because restoration does not depend on
    # the loop reaching its own epilogue.
    originals = {}
    for rel in (SERVER, OBFUSCATE, TAG, EXTHELLO, SEARCH, SEARCHRESULT,
                SOURCE):
        path = os.path.join(REPO, rel)
        if not os.path.exists(path):
            print("FATAL: %s does not exist under %s" % (rel, REPO))
            return 2
        with open(path) as fh:
            originals[rel] = fh.read()

    def restore_all():
        for rel, text in originals.items():
            with open(os.path.join(REPO, rel), "w") as fh:
                fh.write(text)

    print("### ed2kwire mutation harness")
    print("### %d probes, each bounded at %ds, each restored before the next\n"
          % (len(MUTATIONS), PROBE_TIMEOUT))

    killed = covered = survived = skipped = expected = 0
    survivors = []
    malformed = []
    try:
        for label, rel, old, new, run_pat in MUTATIONS:
            olds = old if isinstance(old, list) else [old]
            news = new if isinstance(new, list) else [new]
            if len(olds) != len(news):
                print("  SKIP      %s\n            %d edits to make and %d "
                      "replacements, so the probe cannot be applied as written"
                      % (label, len(olds), len(news)))
                skipped += 1
                malformed.append(label)
                continue

            path = os.path.join(REPO, rel)
            with open(path) as fh:
                text = fh.read()
            missing = [o for o in olds if o not in text]
            if missing:
                # Counted AND listed. A count with no list is a report nobody
                # can act on, and it is how a stale row survives a rename of
                # the thing it was probing.
                print("  SKIP      %s\n            %d of %d patterns are not "
                      "in %s, so the probe no longer matches the source and "
                      "would silently prove nothing"
                      % (label, len(missing), len(olds), rel))
                for m in missing:
                    print("            missing: %r" % (m[:90],))
                skipped += 1
                malformed.append(label)
                continue

            # A pattern that selects no test is a MALFORMED probe, not a
            # survivor. Checked before the mutation is applied so a broken
            # build cannot be confused with a dead pattern.
            selected = tests_matching(run_pat)
            if selected is None:
                print("  SKIP      %s\n            could not list the "
                      "package's tests, so the probe's -run pattern could "
                      "not be checked", label)
                skipped += 1
                malformed.append(label)
                continue
            if not selected:
                print("  SKIP      %s\n            the -run pattern %r "
                      "selects NO test, so the suite would pass without "
                      "running anything and this probe would report a "
                      "survivor that is not one", label, run_pat)
                skipped += 1
                malformed.append(label)
                continue

            mutated = text
            for o, n in zip(olds, news):
                mutated = mutated.replace(o, n, 1)
            with open(path, "w") as fh:
                fh.write(mutated)

            out, timed_out, build_failed = run_suite(run_pat)
            restore_all()  # restored before the next probe, not at the end

            if build_failed:
                print("  SKIP      %s\n            the mutation did not "
                      "compile, so it is a defect in THIS PROBE and not a "
                      "hole in the tests" % label)
                skipped += 1
                malformed.append(label)
                continue
            if timed_out:
                # A hang is NOT a kill. The suite neither passed nor failed;
                # calling it a kill would credit the tests with catching
                # something they never saw the end of.
                print("  SKIP      %s\n            the suite HUNG and was cut "
                      "off at %ds. A hang is not a kill" % (label, PROBE_TIMEOUT))
                skipped += 1
                malformed.append(label)
                continue

            failing = [ln.strip() for ln in out.splitlines()
                       if ln.strip().startswith("--- FAIL")]
            if failing:
                killed += 1
                print("  KILLED    %-58s %s" % (label, failing[0][:44]))
            elif "ok " in out and "FAIL" not in out:
                # The suite ran and passed. Whether that is COVERED or
                # SURVIVED depends on whether the mutation is reachable at
                # all, and this harness cannot tell — so it says SURVIVED and
                # sends the reader to the tests, which is the honest
                # direction: a reported hole costs one look, a hidden one
                # costs the next bug that depends on this line.
                if label in EXPECTED_SURVIVORS:
                    expected += 1
                    print("  EXPECTED  %s\n            known not to be "
                          "killable: %s" % (label,
                                            EXPECTED_SURVIVORS[label]))
                else:
                    survived += 1
                    survivors.append(label)
                    print("  SURVIVED  %s" % label)
            else:
                covered += 1
                print("  COVERED   %s" % label)
    finally:
        # Belt and braces. The loop already restores after every probe, so
        # this only matters if the loop raised.
        restore_all()

    print("\n### %d killed, %d covered, %d survived, %d skipped, "
          "%d expected"
          % (killed, covered, survived, skipped, expected))
    if survivors:
        print("\n### SURVIVORS -- go and look at a TEST")
        for s in survivors:
            print("    %s" % s)
    if malformed:
        print("\n### MALFORMED -- go and look at THIS FILE")
        for m in malformed:
            print("    %s" % m)

    # 0 clean, 1 a hole in the tests, 2 a defect in this harness. Kept apart
    # so one non-zero code sends the reader to the shorter list.
    stale_expected = sorted(set(EXPECTED_SURVIVORS) - {m[0] for m in MUTATIONS})
    if stale_expected:
        print("\n### STALE EXPECTATIONS -- go and look at THIS FILE")
        for lbl in stale_expected:
            print("    %s\n        no such probe exists any more, so this "
                  "allowlist entry is protecting nothing" % lbl)
        return 2
    if malformed:
        return 2
    if survivors:
        return 1
    return 0


if __name__ == "__main__":
    # Deliberately NOT a pipe: `| tail; echo $?` reports tail's status.
    sys.exit(main())
