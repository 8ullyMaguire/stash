// Package torrentpolicy decides what a downloader is ALLOWED to do with a
// torrent, once it is running.
//
// # WHY THIS EXISTS RATHER THAN A CONFIG FLAG
//
// The library's default is to upload opportunistically. `ClientConfig.Seed`'s
// own comment says it: *"Upload even after there's nothing in it for us. By
// default uploading is not altruistic, we'll only upload to encourage the peer
// to reciprocate."* That is a reasonable default for a client whose operator
// asked for a BitTorrent client.
//
// It is the wrong default here, for a reason that is not about politeness.
// spec §7.1 draws a line between STORING a locator and ACTING on it, and adds
// a third distinction this package exists to hold: a BitTorrent client that
// uploads is REDISTRIBUTING. Fetching is a read; uploading is a write to
// somebody else's library, and it happens to peers the operator never sees and
// cannot consent to.
//
// So the question "should this torrent seed?" is not a preference the operator
// picks once in a settings screen. It is answered per torrent, from the same
// consent tier that decided the transfer was allowed to start, and the answer
// has to be derived rather than defaulted — because the default is permissive
// and a permissive default here is a downloader that publishes a stranger's
// material without anyone deciding that it should.
//
// # THE DERIVATION
//
// From `collab.LocatorTier`, which §7.1 already defines:
//
//	TierUnverified          -> download only
//	TierSelfPublished       -> download and seed. The creator permits it.
//	TierPerformerClaimed    -> download and seed. A claim is an assertion by
//	                           someone with standing, and §7.1 treats it as
//	                           covering redistribution.
//	TierThirdPartyPermitted -> download and seed, and the locator was already
//	                           gated on this for the hand-off.
//	TierQuarantined         -> refused upstream; unreachable here.
//	TierDenied              -> refused upstream; unreachable here.
//
// `unknown` is the interesting one. It is a tier this build does not recognise —
// a value from a newer core, a hand-edited row, a truncated database. The
// policy is to DOWNLOAD ONLY, and the reason is worth stating: the tiers above
// are assertions by identified parties, and an assertion nobody can be
// identified for is not permission to publish. Failing toward seeding here
// would mean an unrecognised value silently acquires the most permissive
// behaviour in the table.
//
// # WHY THIS IS NOT PART OF THE CONSENT GATE
//
// Because it is a different question, asked at a different time, and a mistake
// in conflating them is invisible. The gate answers "may this locator be acted
// on at all" — once, at the moment the transfer starts. The seeding policy
// answers "may the bytes leave afterwards" — continuously, for as long as the
// client runs.
//
// Both are derived from the same tier, so they cannot disagree, and both live
// on the core side of the seam: the plugin has no tier of its own to read, and
// asking it to decide this would be asking the thing that wants to upload.

package torrentpolicy

// UploadPolicy is what a downloader may do with a torrent's data.
type UploadPolicy string

const (
	// UploadForbidden means: never send a chunk, to anyone, ever.
	//
	// Not a rate limit and not a "seed after N hours" — a hard no, because
	// every byte sent is a byte the operator did not agree to redistribute, and
	// a limit is a smaller promise than the one the tier did not make.
	UploadForbidden UploadPolicy = "forbidden"

	// UploadAllowed means: seed, at the client's configured rates.
	UploadAllowed UploadPolicy = "allowed"
)

// CanUpload reports whether the policy permits sending chunks.
//
// A method rather than a bare field so a caller cannot compare against a string
// literal and so a future policy (a rate cap, a peer-count limit) is a new
// constant rather than a change of meaning for an existing one. Changing what
// `UploadAllowed` means would silently re-permit uploads for every torrent
// decided under the old meaning, which is the same class of bug as changing
// what a consent tier means.
func (p UploadPolicy) CanUpload() bool {
	return p == UploadAllowed
}

// String is the value that goes into logs and task output.
func (p UploadPolicy) String() string { return string(p) }

// Policy is the decision for one torrent, with the reason attached.
//
// Same shape as the consent gate's decision, and for the same reason: a policy
// without a stated reason is a policy nobody can audit, and "why is this
// seeding" is a question an operator asks out loud the first time it happens.
type Policy struct {
	// Upload is what the downloader may do.
	Upload UploadPolicy

	// Tier is the tier the decision was derived from. Recorded so a later audit
	// names the state the decision was made at, rather than the state that
	// happens to be current.
	Tier string

	// Reason is populated for every decision, including the permissive one. A
	// permissive decision with no stated basis is the one that gets audited
	// and found wanting.
	Reason string
}

// Input is what the policy is derived from.
type Input struct {
	// Tier is the object's consent tier, as a string so this package does not
	// import the core's type.
	//
	// A STRING rather than the tier itself, and that is a real constraint worth
	// stating: the plugin is a separate module (docs/decisions/0001) and cannot
	// import `internal/collab`. So the tier arrives as whatever the host
	// returned, and an unrecognised value is a case this function must handle
	// rather than a case it can rule out at compile time. That is why `unknown`
	// is in the table and why it is the restrictive one.
	Tier string

	// OperatorAllowedSeed is the operator's own setting, if they set one.
	//
	// NEVER a way to OVERRIDE the tier downward-to-permissive. It can only
	// make a permissive decision more restrictive, and that is the whole
	// reason it is here: an operator who does not want their box seeding
	// anything should be able to say so without a code change, and an operator
	// who does want to seed something the tier does not permit should not be
	// able to say so at all.
	OperatorAllowedSeed bool
}

// tier values, as the core spells them.
//
// Duplicated from internal/collab/locator.go for the module-boundary reason
// above, and the duplication is checked: the plugin's
// `TestTheTierStringsMatchTheCore` reads core's source and fails if these drift.
// An unrecognised string is not a compile error here, so nothing else would
// notice.
const (
	tierUnverified          = "unverified"
	tierSelfPublished       = "self_published"
	tierPerformerClaimed    = "performer_claimed"
	tierThirdPartyPermitted = "third_party_permitted"
	tierQuarantined         = "quarantined"
	tierDenied              = "denied"
)

// Decide derives the upload policy from the object's tier.
//
// The derivation is a TABLE rather than a comparison chain, because the tiers
// are the core's vocabulary and this package does not import it: a chain would
// have to be edited in two places when a tier is added, and the second edit
// would be the one that decides a stranger's material may be published. A table
// puts every tier in one place, in this file, with a reason.
func Decide(in Input) Policy {
	// The operator's setting first, so it reads as the outer bound rather than
	// as a special case for one tier. "No seeding, ever" is a simpler thing to
	// implement than "no seeding for everything except these four", and the
	// simpler version is the one an operator can predict.
	if !in.OperatorAllowedSeed {
		return Policy{
			Upload: UploadForbidden,
			Tier:   in.Tier,
			Reason: "seeding is switched off for this downloader, so no chunk " +
				"is sent whatever the object's tier permits. The tier may permit " +
				"redistribution; the operator has declined to exercise it",
		}
	}

	switch in.Tier {
	case tierSelfPublished, tierPerformerClaimed, tierThirdPartyPermitted:
		return Policy{
			Upload: UploadAllowed,
			Tier:   in.Tier,
			Reason: "the object is " + in.Tier + ", which is an assertion by a " +
				"party with standing that redistribution is covered. Fetching is a " +
				"read; uploading is a write to a library the operator never sees, " +
				"so this needs the assertion rather than merely the absence of a " +
				"refusal",
		}

	case tierUnverified:
		// The interesting default, and the reason the table exists.
		//
		// `unverified` is the tier every object starts at, so it is the COMMON
		// case, and it means nobody has asserted anything. Download is fine —
		// the material is public and the operator asked for it. Uploading is
		// not, because "nobody has objected" is not "somebody permitted this".
		return Policy{
			Upload: UploadForbidden,
			Tier:   in.Tier,
			Reason: "the object is unverified, which means nobody has asserted " +
				"that redistribution is covered. Absence of a refusal is not " +
				"permission to publish, and this is the common case rather than " +
				"the edge one",
		}

	case tierQuarantined, tierDenied:
		// Unreachable through the consent gate, which refuses both before a
		// transfer starts. Reachable if something bypasses the gate, and the
		// answer here is the restrictive one so a bypass does not also become a
		// publish.
		return Policy{
			Upload: UploadForbidden,
			Tier:   in.Tier,
			Reason: "the object is " + in.Tier + ", which the consent gate " +
				"refuses. A download should not be running for it at all, and if " +
				"one is, refusing to upload is the smallest correct response",
		}

	default:
		// An unrecognised tier: a value from a newer core, a hand-edited row, a
		// truncated database. Download only.
		//
		// This is the restrictive direction on purpose. The tiers that permit
		// uploading are assertions by identified parties, and a value nobody
		// can be identified for is not one of them. Falling through to "unknown
		// means allowed" would make a schema change silently acquire the most
		// permissive behaviour in the table.
		return Policy{
			Upload: UploadForbidden,
			Tier:   in.Tier,
			Reason: "the object's tier " + tierOrUnknown(in.Tier) + " is not one " +
				"this build recognises, so there is no assertion behind it that " +
				"could cover redistribution. A torrent is downloaded but not " +
				"seeded; if the tier should permit seeding it has to be a tier " +
				"this build knows",
		}
	}
}

// tierOrUnknown renders an empty tier as something a log line can be read
// against, because an empty string in a task list is a blank cell.
func tierOrUnknown(t string) string {
	if t == "" {
		return "(empty)"
	}
	return "\"" + t + "\""
}

// UploadForbiddenFor is the fail-closed default for a client that has not been
// told otherwise.
//
// A package-level function rather than a zero value on Policy, because a zero
// `Policy` is an EMPTY struct: `Allowed` would read as false and `Upload` as the
// empty string, and an empty UploadPolicy is not `UploadForbidden` — so
// `CanUpload()` on it returns false for the RIGHT reason while the type system
// says nothing about it. Naming the safe default means a caller that has not
// decided has something to call.
func UploadForbiddenFor(tier string) Policy {
	return Policy{
		Upload: UploadForbidden,
		Tier:   tier,
		Reason: "no seeding decision was made for this torrent, so no chunk is " +
			"sent. A torrent that reaches the network without a decision is a " +
			"programming error, and this is the safe way for it to behave",
	}
}

// The tier names, exported.
//
// Deliberately through accessors rather than as exported constants, and the
// reason is the module boundary: these strings are DATA duplicated from core's
// `internal/collab`, not part of this package's vocabulary. Exporting them as
// constants would invite a caller to compare a tier against a constant instead
// of calling `Decide`, which is the one function that knows the full table --
// and a tier added to core would then be silently unhandled at the call site
// rather than falling through to the restrictive branch.
//
// The accessors are for the OTHER direction: a caller that HOLDS a tier string
// (from the host, over the plugin seam) and needs to ask what it is. That is a
// real question with a real answer, and `TestTheTierStringsMatchTheCore` is what
// keeps these in step with core's source.
var (
	// TierUnverified is where every object starts. Nobody has asserted anything
	// about it, so it is the common case and the one whose refusal matters most.
	TierUnverified = tierUnverified

	// TierSelfPublished is the creator permitting redistribution explicitly.
	TierSelfPublished = tierSelfPublished

	// TierPerformerClaimed is a claim by a party with standing.
	TierPerformerClaimed = tierPerformerClaimed

	// TierThirdPartyPermitted is third-party permission, already gated at
	// hand-off.
	TierThirdPartyPermitted = tierThirdPartyPermitted

	// TierQuarantined and TierDenied are refused by the consent gate before a
	// transfer starts. `Decide` handles them so a bypass of the gate does not
	// also become a publish.
	TierQuarantined = tierQuarantined
	TierDenied      = tierDenied
)
