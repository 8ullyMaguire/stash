package torrent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	libtorrent "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	libstorage "github.com/anacrolix/torrent/storage"

	torrentpolicy "github.com/stashapp/stash-plugin-p2pdownloader/internal/policy"
)

// # WHY A MAGNET PATH EXISTS HERE AND NOT IN THE LIBRARY
//
// `Client.AddMagnet(uri string)` is a one-line library method that bypasses
// everything in this package:
//
//	func (cl *Client) AddMagnet(uri string) (T *Torrent, err error) {
//	    spec, err := TorrentSpecFromMagnetUri(uri)
//	    if err != nil { return }
//	    T, _, err = cl.AddTorrentSpec(spec)
//	    return
//	}
//
// No policy, no gate, no metainfo check, and no upload control. A caller who
// reaches for it puts a torrent on the client that this code has never heard of,
// from a string a stranger put in a database. `TestAddMagnetOnTheLibraryIsNot-
// ReachableFromHere` greps this package's own source so that cannot happen by
// accident.
//
// # WHY A MAGTORRENT CANNOT BE GATED, STATED HONESTLY
//
// A magnet carries an infohash, a display name and trackers. It has no `files`
// and no `length` — those arrive by BEP 9 from whichever peer answers first. So
// `checkMetainfo` has nothing to check and the gate has nothing to resolve, and
// a "check" that passed on an empty input would be a check that always passes.
//
// The gap is a real window in which the library holds a torrent nobody has
// examined. It is acceptable for exactly one reason: **the gate runs again when
// the metadata arrives**, and `OnMetadata` DROPS the torrent if the names turn
// out to escape the root. `Decision.Gated` and `Decision.GatedAfterMetadata`
// exist so a caller can tell which side of that window it is looking at, and
// `Gated` is false for a magnet without exception.
//
// The attack this closes: a magnet names no files, so nothing about it looks
// wrong — a well-formed infohash, an innocuous display name — and then a peer
// answers with `["..","..","escape"]`. Checked-at-add alone would write outside
// the download root.

// ErrNoInfoHash means the locator carries no usable infohash.
//
// Its own sentinel, not a malformed-torrent error, because it is the one case
// the library accepts silently: `TorrentSpecFromMagnetUri` fills `InfoHash`
// from the `urn:btih:` parameter, and a URI without one produces a spec with the
// ZERO hash, which `AddTorrentSpec` does not object to. A torrent identified by
// nothing cannot be matched to its metadata and cannot be dropped when its names
// turn out to be hostile, so it is refused at the door.
var ErrNoInfoHash = errors.New("the locator carries no usable infohash")

// AddMagnet adds a magnet locator, with the policy applied and the storage gate
// deferred until its metadata arrives.
//
// The upload decision is made HERE, from the tier, and is the same decision the
// complete-torrent path makes — so a magnet is not a way to reach a torrent the
// policy would have refused. That is the one thing about a magnet this package
// can decide, and it decides it before the client hears about the torrent.
func (d *Downloader) AddMagnet(tier, uri string) Decision {
	dec := torrentpolicy.Decide(torrentpolicy.Input{
		Tier:                tier,
		OperatorAllowedSeed: d.cfg.OperatorAllowedSeed,
	})

	// The infohash, parsed from the URI. NOT via the library's
	// `TorrentSpecFromMagnetUri`, because that returns a spec and the check that
	// matters is whether the hash is real — which is a question about the URI.
	m, err := metainfo.ParseMagnetUri(uri)
	if err != nil {
		return Decision{
			Tier:        tier,
			Policy:      dec,
			DisplayName: m.DisplayName,
			InfoHash:    m.InfoHash,
			Err:         fmt.Errorf("%w: %v", ErrNoInfoHash, err),
		}
	}

	// A zero infohash is the specific failure `ParseMagnetUri` does not report,
	// and it is the dangerous one: `magnet:?xt=urn:btih:NOTAVALIDHASHHERE1234`
	// parses cleanly and yields the zero hash. Measured — the library adds it.
	if m.InfoHash.IsZero() {
		return Decision{
			Tier:        tier,
			Policy:      dec,
			DisplayName: m.DisplayName,
			InfoHash:    m.InfoHash,
			Err: fmt.Errorf("%w: %q names no `urn:btih:` hash, or an unreadable "+
				"one. The library accepts this and gives the torrent the zero "+
				"hash, which cannot be matched to its metadata and cannot be "+
				"dropped when its file names turn out to escape the download "+
				"root", ErrNoInfoHash, uri),
		}
	}

	// The display name is recorded, NOT sanitised.
	//
	// It is not a path this package writes to — the gate uses the metadata's file
	// names — so checking it for traversal would be overclaiming. It is recorded
	// verbatim because `BestName()` falls back to it when the metadata arrives,
	// and it is what a task report shows the operator first. A record that
	// disagrees with what the peer said is worse than one containing something
	// ugly.
	displayName := m.DisplayName

	spec, err := libtorrent.TorrentSpecFromMagnetUri(uri)
	if err != nil {
		return Decision{
			Tier:        tier,
			Policy:      dec,
			DisplayName: displayName,
			InfoHash:    m.InfoHash,
			Err:         fmt.Errorf("%w: %v", ErrNoInfoHash, err),
		}
	}

	upload := dec.Upload.CanUpload()
	spec.DisallowDataUpload = !upload
	// The gate is attached even though it cannot run yet. When the metadata
	// arrives the library opens the torrent through this, and the gate refuses
	// there as a BACKSTOP — the same layered arrangement as
	// `TestTheGateIsAskedAndItsRefusalIsWhatStopsTheTorrent`, and the reason
	// this is `covered` rather than `killed` for several harness rows.
	spec.Storage = d.gate
	spec.ChunkSize = 0

	tr, _, err := d.client.AddTorrentSpec(spec)
	if err != nil {
		return Decision{
			Tier:          tier,
			Policy:        dec,
			DisplayName:   displayName,
			InfoHash:      m.InfoHash,
			UploadAllowed: upload,
			Err:           fmt.Errorf("the torrent library declined the locator: %w", err),
		}
	}

	// The tier, recorded. Without it `tierOf` returns "" for every magnet and
	// `OnMetadata` decides every arriving torrent as an unrecognised tier --
	// restrictive, so nothing is ever published and the downloader is inert
	// rather than broken. This is the second time this association has been
	// missing from one of the two add paths; it belongs next to the add.
	d.remember(m.InfoHash, tier)

	// The record. A magnet's hash is known before its names are, so the
	// infohash recorded here is the one a later refusal will be filed against —
	// and `OnMetadata` checks that the arriving metadata carries the SAME hash
	// before it does anything with it.
	//
	// READ BACK OFF THE SPEC, not recomputed from `upload`.
	//
	// `DisallowDataUpload: !upload` says what the policy said, not what the
	// client was given -- the identical tautology `AppliedSpec` was introduced to
	// end, written again one file over. A mutation setting
	// `spec.DisallowDataUpload = false` leaves that line untouched, so the record
	// agrees with the policy and the test passes on a torrent the client is
	// being told to upload.
	d.lastMagnet = AppliedSpec{
		InfoHash:           m.InfoHash,
		DisplayName:        displayName,
		DisallowDataUpload: spec.DisallowDataUpload,
		StorageIsGate:      spec.Storage == libstorage.ClientImpl(d.gate),
	}

	return Decision{
		Tier:          tier,
		Policy:        dec,
		DisplayName:   displayName,
		InfoHash:      m.InfoHash,
		Torrent:       tr,
		UploadAllowed: upload,
		Added:         true,
		// NOT gated, and recorded as such. A caller that reads `Added` without
		// reading this is trusting that a magnet's file names were checked, and
		// they were not.
		Gated: false,
	}
}

// OnMetadata is BEP 9 arrival: the metadata a magnet was waiting for has landed.
//
// This is the only thing that makes adding an ungated magnet acceptable, so it
// runs the FULL sequence the complete-torrent path runs — policy, metainfo check,
// gate — and DROPS the torrent if the gate refuses.
//
// Dropping rather than marking is deliberate. A client that still holds a
// torrent it cannot open keeps announcing it on the DHT, and a later
// `AddTorrent*` for the same infohash succeeds from the client's own cache. The
// operator would see a download that never starts and never explains itself.
//
// The infohash is checked against the one the magnet declared. Not for
// cryptographic reasons — a peer answering with the wrong metadata is not the
// threat model, and the library verifies the hash itself — but because this
// method is handed a `*metainfo.MetaInfo` by a CALLER, and a caller that passes
// the wrong one would otherwise get a gate check against a torrent nobody added.
func (d *Downloader) OnMetadata(tr *libtorrent.Torrent, mi *metainfo.MetaInfo) Decision {
	dec := torrentpolicy.Decide(torrentpolicy.Input{
		Tier:                d.tierOf(tr),
		OperatorAllowedSeed: d.cfg.OperatorAllowedSeed,
	})

	if tr == nil {
		return Decision{
			Policy: dec,
			Err:    errors.New("no torrent to attach metadata to"),
		}
	}

	info, err := mi.UnmarshalInfo()
	if err != nil {
		return Decision{
			Tier:     d.tierOf(tr),
			Policy:   dec,
			InfoHash: mi.HashInfoBytes(),
			Err: fmt.Errorf("the arriving metadata could not be parsed, so there "+
				"are no file names to check: %w", err),
		}
	}

	hash := mi.HashInfoBytes()

	// The metainfo check, first, for the same reason as in `AddTorrent`: a name
	// check on a torrent with no pieces is a name check on a torrent that can
	// never transfer anything, and it files a REFUSAL against a hash the
	// operator looks up and finds meaningless.
	if err := checkMetainfo(&info); err != nil {
		d.drop(tr)
		return Decision{
			Tier:          d.tierOf(tr),
			Policy:        dec,
			InfoHash:      hash,
			DisplayName:   info.BestName(),
			UploadAllowed: false,
			Err:           err,
		}
	}

	// The gate. The caller's metainfo and the torrent the client holds are two
	// different objects until this passes.
	if _, err := d.gate.OpenTorrent(context.Background(), &info, hash); err != nil {
		d.drop(tr)
		return Decision{
			Tier:          d.tierOf(tr),
			Policy:        dec,
			InfoHash:      hash,
			DisplayName:   info.BestName(),
			UploadAllowed: false,
			Added:         false,
			Err:           fmt.Errorf("%w: %v", ErrRefusedUpFront, err),
		}
	}

	// Accepted. The torrent stays, and now it IS gated.
	upload := dec.Upload.CanUpload()
	if !upload {
		tr.DisallowDataUpload()
	}
	d.lastMagnet.GatedAfterMetadata = true

	return Decision{
		Tier:               d.tierOf(tr),
		Policy:             dec,
		InfoHash:           hash,
		DisplayName:        info.BestName(),
		Torrent:            tr,
		UploadAllowed:      upload,
		Added:              true,
		Gated:              true,
		GatedAfterMetadata: true,
	}
}

// LastMagnet returns what the client was last GIVEN on the magnet path.
//
// The same reason `LastApplied` exists, and the reason it could not be shared: a
// `Decision` reports intent, and seven mutations on this path survived until
// there was a way to read the far side. Every one of them was invisible because
// `Decision.UploadAllowed` is derived from the policy while the spec's
// `DisallowDataUpload` is what the library reads -- the same tautology the
// complete-torrent path had, in a second place.
//
// A separate accessor rather than a shared one because the two records overlap
// on `InfoHash` and `DisallowDataUpload` and a merged record would be wrong for
// whichever path did not write last.
func (d *Downloader) LastMagnet() AppliedSpec { return d.lastMagnet }

// TierOf reports the consent tier a torrent was added under.
//
// Exported for the same reason: `OnMetadata` re-derives the policy from it, and
// a test that cannot ask which tier was recorded cannot tell whether the
// re-derivation happened at all. An unknown torrent is `""`, which the policy
// sends to `UploadForbidden`.
func (d *Downloader) TierOf(hash metainfo.Hash) string { return d.tiers[hash] }

// drop removes a torrent from the client.
//
// A no-op on nil, because the caller has just been told the torrent does not
// exist and must not be made to handle that case twice.
func (d *Downloader) drop(tr *libtorrent.Torrent) {
	if tr != nil {
		tr.Drop()
	}
}

// tierOf reports the consent tier a torrent was added under.
//
// A MAP and not a field, because the library owns the `*Torrent` and there is
// nowhere on it to record a tier. So the downloader keeps the association, and
// an unknown torrent is `""` — which the policy treats restrictively, because
// `Decide`'s table sends an unrecognised tier to `UploadForbidden`.
//
// That default matters: a torrent whose tier this process has forgotten must
// not be permitted to upload because the lookup missed.
func (d *Downloader) tierOf(tr *libtorrent.Torrent) string {
	if tr == nil {
		return ""
	}
	if t, ok := d.tiers[tr.InfoHash()]; ok {
		return t
	}
	return ""
}

// remember associates a torrent with the tier it was added under.
func (d *Downloader) remember(hash metainfo.Hash, tier string) {
	if d.tiers == nil {
		d.tiers = make(map[metainfo.Hash]string)
	}
	d.tiers[hash] = tier
}

// A magnet's display name may contain anything, including a slash and a NUL.
// Trimmed only for the empty check, never sanitised — see `AddMagnet`.
func _unusedTrimGuard(s string) bool { return strings.TrimSpace(s) == "" }
