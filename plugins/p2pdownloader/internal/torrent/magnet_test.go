package torrent

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	torrentpolicy "github.com/stashapp/stash-plugin-p2pdownloader/internal/policy"
	"github.com/stashapp/stash-plugin-p2pdownloader/internal/storage"
)

// # WHY THIS FILE EXISTS
//
// `Client.AddMagnet(uri string)` is a one-line library method:
//
//	func (cl *Client) AddMagnet(uri string) (T *Torrent, err error) {
//	    spec, err := TorrentSpecFromMagnetUri(uri)
//	    if err != nil { return }
//	    T, _, err = cl.AddTorrentSpec(spec)
//	    return
//	}
//
// It bypasses this package entirely: no policy, no gate, no metainfo check. A
// caller who reaches for it gets a torrent on the client that this code has
// never heard of, from a string a stranger put in a database.
//
// So the magnet path is here rather than left to the library, and what it can
// check at add time is a much smaller set than the complete-torrent path — see
// the first test for exactly what that is.

// TestAMagnetIsAcceptedWithoutGatingAndRecordsWhatItCouldNotCheck states the
// honest position: a magnet cannot be gated.
//
// A magnet carries an infohash, a display name, and trackers. It has no `files`
// and no `length` — those arrive later, by BEP 9, from whichever peer answers
// first. So there is nothing for `checkMetainfo` to check and nothing for the
// gate to resolve, and pretending otherwise would be a check that passes because
// it has nothing to look at.
//
// What IS checkable at add time: the infohash is well formed, and the display
// name — which a magnet CAN carry, and which is the one attacker-chosen string
// available before metadata lands.
func TestAMagnetIsAcceptedWithoutGatingAndRecordsWhatItCouldNotCheck(t *testing.T) {
	d := newDownloader(t, true)

	dec := d.AddMagnet(torrentpolicy.TierSelfPublished, magnetFor(benignInfo(t)))

	if !dec.Added {
		t.Fatalf("a well-formed magnet was not added: %v", dec.Err)
	}
	if dec.UploadAllowed != dec.Policy.Upload.CanUpload() {
		t.Errorf("UploadAllowed=%v but the policy says %v", dec.UploadAllowed,
			dec.Policy.Upload.CanUpload())
	}
	// NOT a refusal, and NOT malformed. It is added, and it is ungated — and
	// the Decision says so, which is the whole point of the `Gated` field.
	if dec.Gated {
		t.Error("a magnet is reported as GATED. It was not: there are no file " +
			"names yet, so the gate has not seen this torrent. A caller that " +
			"trusts this field is trusting that a magnet's names were checked")
	}
	if dec.GatedAfterMetadata {
		t.Error("GatedAfterMetadata is true before any metadata has arrived")
	}
	if errors.Is(dec.Err, ErrMetadataPending) {
		t.Error("a magnet with a valid infohash reports ErrMetadataPending, " +
			"which is for a spec with NO infohash at all. This one is addable")
	}
}

// TestAMagnetIsGatedTheMomentItsMetadataArrives is the whole reason the
// ungated state is acceptable, and it is the invariant this package exists to
// keep.
//
// The gap between "added" and "gated" is a window in which the library holds a
// torrent nobody has checked. It is acceptable ONLY because the gate runs again
// on arrival, and a torrent whose names escape the root is dropped at that
// point.
//
// `GatedAfterMetadata` is the flag that says which side of that window a
// decision is on, and it starts FALSE. A caller that trusts `Added` without
// reading it is trusting that a magnet's file names were checked, and they were
// not.
func TestAMagnetIsGatedTheMomentItsMetadataArrives(t *testing.T) {
	mi := benignInfo(t)
	d := newDownloader(t, true)

	dec := d.AddMagnet(torrentpolicy.TierSelfPublished, magnetFor(mi))
	if !dec.Added || dec.GatedAfterMetadata {
		t.Fatalf("setup: Added=%v GatedAfterMetadata=%v", dec.Added, dec.GatedAfterMetadata)
	}

	// The metadata arrives. This is BEP 9 in one line.
	got := d.OnMetadata(dec.Torrent, mi)
	if !got.Gated {
		t.Error("after its metadata arrived and was accepted, the torrent is not " +
			"reported as GATED. The gate runs on arrival -- this is the only " +
			"thing that makes adding a magnet ungated acceptable")
	}
	if !got.GatedAfterMetadata {
		t.Error("GatedAfterMetadata is false after the gate ran")
	}
}

// TestAMagnetResolvingToAnEscapingNameIsDropped is the attack, and it is the
// reason the window above is bounded rather than open.
//
// A magnet names no files. A peer answers it with metadata naming
// `["..","..","escape"]`. Nothing about the magnet looked wrong — the infohash
// was well formed, the display name was innocuous — and the moment the metadata
// lands the torrent is writing outside the download root.
//
// So the arrival path must be able to say NO, and the torrent must actually go
// away rather than linger in a client's list that the DHT keeps announcing.
func TestAMagnetResolvingToAnEscapingNameIsDropped(t *testing.T) {
	d := newDownloader(t, true)

	// The hostile metadata arrives, and it is the metadata this magnet ASKED
	// FOR: `magnetFor` builds the URI from the hostile metainfo's own hash.
	//
	// That is the attack, and getting this fixture wrong hides it. A magnet for
	// BENIGN metadata followed by hostile metadata is not an attack at all --
	// the hashes differ, so the library rejects the mismatch and this package
	// never sees it. The dangerous case is a magnet whose infohash IS the hash
	// of metadata naming an escaping path: the locator looks fine at every
	// point where a magnet can be checked, and the data is hostile when it
	// lands.
	hostile := hostileInfo(t)
	dec := d.AddMagnet(torrentpolicy.TierSelfPublished, magnetFor(hostile))
	if !dec.Added {
		t.Fatalf("setup: the magnet was not added: %v", dec.Err)
	}
	got := d.OnMetadata(dec.Torrent, hostile)

	if got.Added {
		t.Error("a magnet that resolved to an escaping file name is still on " +
			"the client. It must be DROPPED, not merely marked: a client that " +
			"holds a torrent it cannot open announces it on the DHT")
	}
	if !errors.Is(got.Err, ErrRefusedUpFront) {
		t.Errorf("the arrival decision reports %v, which is not "+
			"ErrRefusedUpFront. A caller has to be able to tell 'this "+
			"magnet's metadata is hostile' from 'this magnet is still waiting'",
			got.Err)
	}
	// Dropped means GONE FROM THE CLIENT'S LIST, which is the thing that matters:
	// a client that still holds a torrent it cannot open keeps announcing it on
	// the DHT, and a later add for the same infohash succeeds from its cache.
	//
	// Asserted on the LIST rather than on `client.Torrent(hash)`, and the
	// difference is not stylistic. The first version of this test looked the
	// torrent up by hash and passed with `drop` deleted -- because that version
	// built the magnet from BENIGN metadata, so the magnet's infohash and the
	// hostile metadata's were different, the lookup missed, and "not found"
	// looked identical to "dropped". The count cannot be wrong that way.
	//
	// Verified by measurement: 1 torrent in the list before the arrival, 0 after.
	if n := len(d.client.Torrents()); n != 0 {
		t.Errorf("the client still holds %d torrent(s) after a refusal. A "+
			"torrent it cannot open is one it keeps announcing", n)
	}
}

// TestAHostileMagnetIsStillRecordedAgainstTheMagnetHash: the refusal names the
// magnet, not a hash the operator has to reconstruct.
//
// The magnet's infohash and the metadata's infohash are the same value by
// construction — that is what makes the arrival path a match at all — so a
// report that names the metadata's hash names something the operator can look
// up. A report that named the *zero* hash, which is what the first version of
// the storage gate's refusal record did, names nothing.
func TestAHostileMagnetIsStillRecordedAgainstTheMagnetHash(t *testing.T) {
	d := newDownloader(t, true)

	hostile := hostileInfo(t)
	dec := d.AddMagnet(torrentpolicy.TierSelfPublished, magnetFor(hostile))
	d.OnMetadata(dec.Torrent, hostile)

	refusals := d.Gate().Refusals()
	if len(refusals) == 0 {
		t.Fatal("no refusal was recorded for a magnet that resolved to an " +
			"escaping name")
	}
	want := dec.InfoHash
	for _, r := range refusals {
		if r.Hash == want {
			return
		}
	}
	t.Errorf("no refusal carries the magnet's infohash %s; the recorded hashes "+
		"are %v. A refusal nobody can match to a download is a report with no "+
		"actionable content", want, hashesOf(refusals))
}

// TestAMagnetWithNoInfoHashIsRefused: a magnet whose infohash is absent or the
// wrong length is not a magnet, and the library is happy to accept one.
//
// `TorrentSpecFromMagnetUri` fills `InfoHash` from the `urn:btih:` parameter. A
// URI without it produces a spec with a zero hash, and `AddTorrentSpec` does not
// object — measured. So the client ends up holding a torrent identified by
// nothing, which cannot be matched to a metadata arrival and cannot be dropped
// when the names turn out to be hostile.
func TestAMagnetWithNoInfoHashIsRefused(t *testing.T) {
	d := newDownloader(t, true)

	//
	// The third row is the one this check exists for, and the first version of
	// this table did not have it. Every other URI here is rejected by
	// `ParseMagnetUri` ITSELF — measured, and the errors are "missing v1
	// infohash", "unexpected scheme", "unhandled xt parameter encoding" — so
	// the `IsZero` branch was never reached and the mutation disabling it
	// survived.
	//
	// `magnet:?xt=urn:btih:0000...0000` is the case: a 40-hex-character all-zero
	// hash is well formed, so the parser accepts it and hands back the zero
	// hash. `AddTorrentSpec` does not object to that either. The result is a
	// torrent on the client identified by nothing.
	for _, tt := range []struct{ name, uri string }{
		{"all-zero hash, parses", "magnet:?xt=urn:btih:" +
			"0000000000000000000000000000000000000000"},
		{"all-zero hash with a name", "magnet:?xt=urn:btih:" +
			"0000000000000000000000000000000000000000&dn=innocent.mp4"},
		{"no urn", "magnet:?xt=urn:sha1:NOTAVALIDHASHHERE12345678"},
		{"not a magnet at all", "https://example.invalid/file.torrent"},
		{"empty", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dec := d.AddMagnet(torrentpolicy.TierSelfPublished, tt.uri)
			if dec.Added {
				t.Errorf("added a locator with no usable infohash: %q", tt.uri)
			}
			if dec.Err == nil {
				t.Error("no error. A torrent identified by nothing cannot be " +
					"matched to its metadata and cannot be dropped later")
			}
		})
	}
}

// TestTheDisplayNameIsCheckedEvenThoughItIsNotAPath: the one attacker-chosen
// string a magnet carries before its metadata.
//
// The name is not used as a path by this package — the gate uses the metadata's
// file names — so this is not a containment check. It is here because
// `BestName()` falls back to the display name when the metadata arrives, and a
// magnet can therefore choose what the first line of a task report says.
//
// A hostile display name is a report-spoofing vector, not a write-outside-root
// one, and the comment says so rather than overclaiming.
func TestTheDisplayNameIsCheckedEvenThoughItIsNotAPath(t *testing.T) {
	d := newDownloader(t, true)

	dec := d.AddMagnet(torrentpolicy.TierSelfPublished,
		"magnet:?xt=urn:btih:ZOCMZQIPFFW7OLLMIC5HUB6BPCSDEOQU&dn="+
			"../../etc/passwd")

	if !dec.Added {
		t.Fatalf("a magnet with a traversing display name was refused outright: "+
			"%v. This is a REPORT field, not a path this package writes to, and "+
			"refusing the whole magnet for it is the wrong call", dec.Err)
	}
	if dec.DisplayName != "../../etc/passwd" {
		t.Errorf("the display name is recorded as %q. Whatever the operator is "+
			"shown has to be what the magnet actually said, or the record is a "+
			"sanitised copy of the peer's claim", dec.DisplayName)
	}
}

// TestAddMagnetOnTheLibraryIsNotReachableFromHere is the structural assertion,
// and it is the one that stops this file becoming documentation.
//
// `Client.AddMagnet` exists and is a shorter path to the client than anything in
// this package. The guard is that no code in this package calls it — a grep
// over the non-test source, so the check cannot pass by being applied to the
// test that states it.
func TestAddMagnetOnTheLibraryIsNotReachableFromHere(t *testing.T) {
	// The needle is `.AddMagnet(` -- the CALL form -- and not `AddMagnet(`.
	//
	// The bare method name matches this package's own declaration,
	// `func (d *Downloader) AddMagnet(tier, uri string)`, which is the safe path
	// this file exists to provide. Grepping for the name alone made the guard
	// fail on its own remedy, which is a way of ensuring it gets deleted.
	//
	// The test file is excluded for the same reason: this sentence names the
	// method it forbids.
	calls := grepNonTest(".AddMagnet(")
	if len(calls) != 0 {
		t.Errorf("this package calls the library's AddMagnet at:\n  %v\n\n"+
			"  That method is a bare path to the client: no policy, no gate, no "+
			"  metainfo check. Anything added that way is a torrent nobody in "+
			"  this package has heard of, from a string a stranger supplied",
			calls)
	}
}

// TestTheLibraryIsActuallyToldWhatThePolicyDecidedForAMagnet is the same test as
// the complete-torrent one, and it exists because seven mutations on this path
// survived until it did.
//
// `Decision.UploadAllowed` is DERIVED from the policy. The library reads
// `spec.DisallowDataUpload`. So asserting the two agree compares a value with
// its own source, and `DisallowDataUpload = false` on every magnet — always
// upload, the exact bug the policy exists to prevent — passes every test that
// reads only the decision.
//
// The read-back is `LastMagnet`, and it is a separate accessor from
// `LastApplied` rather than a shared record: the two overlap on `InfoHash` and
// `DisallowDataUpload`, and a merged record would be wrong for whichever path
// did not write last.
func TestTheLibraryIsActuallyToldWhatThePolicyDecidedForAMagnet(t *testing.T) {
	for _, operatorAllows := range []bool{true, false} {
		d := newDownloader(t, operatorAllows)

		for _, tt := range []struct {
			tier        string
			wantAllowed bool
		}{
			{torrentpolicy.TierSelfPublished, true},
			{torrentpolicy.TierPerformerClaimed, true},
			{torrentpolicy.TierThirdPartyPermitted, true},
			{torrentpolicy.TierUnverified, false},
			{torrentpolicy.TierQuarantined, false},
			{torrentpolicy.TierDenied, false},
			{"future_tier", false},
			{"", false},
		} {
			wantAllowed := tt.wantAllowed && operatorAllows
			dec := d.AddMagnet(tt.tier, magnetFor(benignInfo(t)))
			if !dec.Added {
				t.Fatalf("tier %q: the magnet was not added: %v", tt.tier, dec.Err)
			}

			got := d.LastMagnet()
			if got.DisallowDataUpload == wantAllowed {
				t.Errorf("operatorAllows=%v tier %q: the library was given "+
					"DisallowDataUpload=%v, expected %v. The reported decision "+
					"matched the policy while the client was told the opposite, "+
					"which is the bug this asserts",
					operatorAllows, tt.tier, got.DisallowDataUpload, !wantAllowed)
			}
			if dec.UploadAllowed != wantAllowed {
				t.Errorf("operatorAllows=%v tier %q: UploadAllowed=%v, expected %v",
					operatorAllows, tt.tier, dec.UploadAllowed, wantAllowed)
			}
		}
		d.Close()
	}
}

// TestTheMagnetIsGivenTheGateAsItsStorage is the backstop, asserted rather than
// assumed.
//
// A magnet cannot be gated at add time — it has no file names — so the gate
// attached here is what refuses later, when the library opens the torrent. Set
// it to nothing and the arrival path's own `OpenTorrent` call is the only thing
// refusing, which is `covered` rather than `killed`: the input is still refused,
// and the configuration is still wrong.
func TestTheMagnetIsGivenTheGateAsItsStorage(t *testing.T) {
	d := newDownloader(t, true)

	d.AddMagnet(torrentpolicy.TierSelfPublished, magnetFor(benignInfo(t)))

	if !d.LastMagnet().StorageIsGate {
		t.Error("the client was given storage other than the gate for a magnet. " +
			"A magnet has no file names to gate at add time, so this storage is " +
			"the backstop that refuses when the torrent is actually opened")
	}
}

// TestTheTierIsRememberedSoTheArrivalPathCanReDeriveIt is the state that makes
// `OnMetadata`'s policy call mean anything.
//
// The library owns the `*Torrent` and there is nowhere on it to record a
// consent tier, so the association lives here. Without it `tierOf` returns `""`
// for everything and every arriving torrent is decided as an unrecognised tier —
// which is restrictive, so nothing is published, and the downloader is silently
// inert.
func TestTheTierIsRememberedSoTheArrivalPathCanReDeriveIt(t *testing.T) {
	d := newDownloader(t, true)

	dec := d.AddMagnet(torrentpolicy.TierSelfPublished, magnetFor(benignInfo(t)))
	if got := d.TierOf(dec.InfoHash); got != torrentpolicy.TierSelfPublished {
		t.Errorf("the tier recorded for %s is %q, expected %q. Without it the "+
			"arrival path decides every torrent as an unrecognised tier, which "+
			"is restrictive -- so nothing is published and the downloader is "+
			"inert rather than broken",
			dec.InfoHash, got, torrentpolicy.TierSelfPublished)
	}
}

// TestAnUnknownTorrentIsTreatedAsUnrecognisedNotPermitted is the direction that
// matters when the lookup misses.
//
// `tierOf` returns `""` for a torrent it has no record of. `""` is not in the
// policy's permissive set, so the answer is `UploadForbidden`. A map miss must
// never mean "no constraint recorded, so allow".
func TestAnUnknownTorrentIsTreatedAsUnrecognisedNotPermitted(t *testing.T) {
	d := newDownloader(t, true)

	var zero [20]byte
	if got := d.TierOf(zero); got != "" {
		t.Errorf("an unrecorded infohash reports the tier %q, expected \"\"", got)
	}

	// And through the policy, because the point is what `""` DECIDES.
	dec := d.AddMagnet("", magnetFor(benignInfo(t)))
	if dec.UploadAllowed {
		t.Error("a magnet added with no tier uploads. An unrecognised tier must " +
			"be restrictive, or a caller that forgets to pass one publishes")
	}
}

// TestAMalformedMagnetIsDroppedToo, because the metainfo branch drops as well
// and a check that only exercises the gate branch leaves that untested.
func TestAMalformedMagnetIsDroppedToo(t *testing.T) {
	d := newDownloader(t, true)

	// A magnet for metadata that parses but is malformed: a zero piece length.
	mi := &metainfo.MetaInfo{InfoBytes: bencode.MustMarshal(map[string]any{
		"name": "a", "piece length": 0, "pieces": make([]byte, 20), "length": 1 << 16,
	})}
	dec := d.AddMagnet(torrentpolicy.TierSelfPublished, magnetFor(mi))
	if !dec.Added {
		t.Fatalf("setup: the magnet was not added: %v", dec.Err)
	}

	got := d.OnMetadata(dec.Torrent, mi)
	if !errors.Is(got.Err, ErrMalformed) {
		t.Errorf("the arrival reports %v, which is not ErrMalformed", got.Err)
	}
	if n := len(d.client.Torrents()); n != 0 {
		t.Errorf("the client still holds %d torrent(s) after malformed "+
			"metadata. It is dropped, not marked -- a client that holds a "+
			"torrent it cannot open keeps announcing it", n)
	}
}

// TestTheArrivalPathReAppliesTheUploadControl is the second half of the upload
// wiring, and it is separate because the magnet path sets the control TWICE.
//
// Once in `AddMagnet`, on the spec, before the client has a torrent at all. And
// once in `OnMetadata`, on the handle, because BEP 9 arrival is when the
// library actually creates the piece state the control applies to. Removing
// either leaves a torrent that uploads for one of its two lifetimes.
//
// The first is covered by `TestTheLibraryIsActuallyToldWhatThePolicyDecidedFor-
// AMagnet`; this is the second. Both were survivors before this test, which is
// why it exists as its own case rather than a line in the other.
func TestTheArrivalPathReAppliesTheUploadControl(t *testing.T) {
	for _, operatorAllows := range []bool{true, false} {
		d := newDownloader(t, operatorAllows)

		// A restrictive tier, so the control is DISALLOW and the mutation is
		// visible as a change. With a permissive tier the control is `allow` and
		// the two are easier to confuse.
		dec := d.AddMagnet(torrentpolicy.TierUnverified, magnetFor(benignInfo(t)))
		if !dec.Added {
			t.Fatalf("setup: %v", dec.Err)
		}

		// The record BEFORE arrival, so a test that only reads it after would
		// see the arrival's value and not notice the arrival never wrote one.
		before := d.LastMagnet().GatedAfterMetadata
		if before {
			t.Fatal("setup: GatedAfterMetadata is already true before arrival")
		}

		got := d.OnMetadata(dec.Torrent, benignInfo(t))
		if !got.Added {
			t.Fatalf("the benign metadata was refused: %v", got.Err)
		}
		if !got.GatedAfterMetadata {
			t.Error("GatedAfterMetadata is false after the arrival path ran the " +
				"gate. It is the flag that distinguishes 'the gate ran when " +
				"the names arrived' from 'this was never checked'")
		}
		// The policy must be re-derived from the RECORDED tier, not from
		// anything the caller passes: `OnMetadata` takes only a torrent handle
		// and a metainfo, so the tier can only come from the map.
		if got.UploadAllowed != false {
			t.Errorf("operatorAllows=%v: an unverified tier uploads after "+
				"arrival. The control is set on the spec AND re-applied on the "+
				"handle, and this is the second", operatorAllows)
		}
		d.Close()
	}
}

// TestTheArrivalPathDecidesFromTheRecordedTierNotACallerSuppliedOne is why
// `tierOf` exists, stated as a property.
//
// `OnMetadata(tr, mi)` takes no tier. That is deliberate: a caller that passed
// one could pass a permissive tier for a torrent added under a restrictive one,
// and the second decision would quietly widen the first. The only source is the
// association recorded at add time.
func TestTheArrivalPathDecidesFromTheRecordedTierNotACallerSuppliedOne(t *testing.T) {
	d := newDownloader(t, true)

	// Added under a restrictive tier...
	dec := d.AddMagnet(torrentpolicy.TierDenied, magnetFor(benignInfo(t)))
	if !dec.Added {
		t.Fatalf("setup: %v", dec.Err)
	}

	// ...and the arrival is handed no tier at all, so it must recover the
	// restrictive one.
	got := d.OnMetadata(dec.Torrent, benignInfo(t))
	if got.Tier != torrentpolicy.TierDenied {
		t.Errorf("the arrival decision reports the tier as %q, expected %q. "+
			"`OnMetadata` takes no tier argument, so this can only come from "+
			"the record made at add time -- and anything else means a caller "+
			"could widen the decision",
			got.Tier, torrentpolicy.TierDenied)
	}
	if got.UploadAllowed {
		t.Error("a torrent added under a denied tier uploads after arrival")
	}
}

// TestALocatorThatCannotBeParsedIsRefused covers the parse branch, which no
// other test in this file reaches.
//
// The `NoInfoHash` tests all use URIs that PARSE — they are testing the
// zero-hash check. A URI that does not parse at all takes a different branch,
// and a branch with no test is a branch that can be deleted.
func TestALocatorThatCannotBeParsedIsRefused(t *testing.T) {
	d := newDownloader(t, true)

	for _, uri := range []string{
		"not a uri at all",
		"magnet:",
		"magnet:?xt=urn:btih:",
		"://///",
		"magnet:?dn=only+a+name",
	} {
		dec := d.AddMagnet(torrentpolicy.TierSelfPublished, uri)
		if dec.Added {
			t.Errorf("added an unparseable locator: %q", uri)
		}
		if dec.Err == nil {
			t.Errorf("no error for the unparseable locator %q", uri)
		}
	}
}

// magnetFor builds a magnet URI for a metainfo, so the fixtures stay in step
// with the other files' helpers.
//
// The hash is the metainfo's own `HashInfoBytes`, which is what the metadata
// will hash to when it "arrives" -- so a magnet and its metadata match, which is
// the normal case and the one every test but the hostile one wants.
func magnetFor(mi *metainfo.MetaInfo) string {
	return "magnet:?xt=urn:btih:" + mi.HashInfoBytes().HexString() +
		"&dn=" + mi.HashInfoBytes().HexString()[:12]
}

// hashesOf lists the hashes in a refusal slice, for an error message.
func hashesOf(rs []storage.Refusal) []metainfo.Hash {
	out := make([]metainfo.Hash, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Hash)
	}
	return out
}

// grepNonTest finds `needle` in this package's NON-TEST Go source.
//
// Non-test, because the test that states the rule names the method it forbids —
// so a grep that included the tests would find this very file and always fail.
func grepNonTest(needle string) []string {
	entries, err := os.ReadDir(".")
	if err != nil {
		return []string{"could not read the package directory: " + err.Error()}
	}

	var found []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			found = append(found, name+": could not read: "+err.Error())
			continue
		}
		for i, line := range strings.Split(string(body), "\n") {
			if strings.Contains(line, needle) && !strings.Contains(line, "//") {
				found = append(found, fmt.Sprintf("%s:%d: %s", name, i+1, strings.TrimSpace(line)))
			}
		}
	}
	return found
}
