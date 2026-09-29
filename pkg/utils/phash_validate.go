package utils

// Validation of generated perceptual hashes. #2149.
//
// THE PROBLEM. A perceptual hash is computed from a sprite of 25 screenshots
// spread across the video. When ffmpeg cannot decode a file -- a broken
// container, a zero duration, a codec it does not support -- the screenshots
// do not fail loudly. They come back as SOMETHING: a black frame, a colour
// bar, the last decoded frame repeated 25 times. The hash of that is a real,
// well-formed 64-bit value, and it is the SAME for every file that fails the
// same way.
//
// Those hashes are then uploaded to StashDB and matched against. So a library
// of undecodable files produces one recurring hash, that hash matches every
// other broken file in the world, and identify confidently pairs unrelated
// scenes. The damage is not a missing fingerprint; it is a confidently wrong
// match, which is the one failure mode a user is least likely to notice.
//
// THE FIX. Refuse to store a hash that matches a known-bad one. Not by exact
// equality: the reporter notes these values "may vary in the wild by 1-3
// bits", because the exact frames ffmpeg emits for a colour bar depend on the
// build and the codec. So the check is a Hamming-distance match.
//
// WHERE THIS IS APPLIED, and why there and nowhere else. The check belongs at
// GENERATION time, in the task that computes and stores a phash, not in the
// matcher. A bad hash that is never stored cannot be uploaded, cannot be
// matched against, and cannot come back from a database written by an older
// version. Fixing it in the matcher instead would leave the bad value in the
// library and in StashDB, and would only stop this instance from being fooled
// by it.
//
// It is deliberately NOT a hard failure. A file whose phash looks like one of
// these is not necessarily corrupt -- the check is a heuristic over nine
// observed values, and refusing outright would silently drop legitimate
// fingerprints. The fingerprint is skipped and the reason logged, so the file
// is simply left without a phash, which is the honest state: no hash is
// better than a wrong one.

import (
	"fmt"
	"math/bits"
)

// knownBadPhashes are phash values observed from files ffmpeg could not
// decode, reported on #2149. They come from solid-colour frames, colour bars
// and zero-duration files.
//
// Stored as hex strings because that is how they appear in the issue, in
// StashDB dumps and in every log line that mentions a phash -- a decimal
// literal here would be unreadable and uncheckable against a bug report.
var knownBadPhashes = []string{
	"a000000000800080",
	"8080808080808080",
	"870707030787fefc",
	"82070707078ffff8",
	"8055557555575575",
	"805555555d755d55",
	"87070707037ef8fc",
	"8707070303fefcdc",
	"cdcdcdc9c1233332",
}

// badPhashTolerance is how many bits a hash may differ from a known-bad value
// and still be rejected.
//
// The issue states these vary "by 1-3 bits" in the wild. Three is therefore
// the tolerance that covers what was actually observed, and it is set there
// deliberately rather than higher: at 64 bits, a distance of 4 already has a
// 1-in-1.6-million chance of catching a genuine hash, and the cost of a false
// rejection is a file quietly left without a fingerprint. Widening this
// trades a real, visible loss for a merely less likely mistake.
const badPhashTolerance = 3

// badPhashPatterns holds the known-bad values, parsed once at init. A parse
// failure would mean this file and the issue disagree, which is a programming
// error rather than bad input, so it panics at startup: a silently skipped
// entry would quietly disable a safety check.
var badPhashPatterns = func() []uint64 {
	out := make([]uint64, 0, len(knownBadPhashes))
	for _, h := range knownBadPhashes {
		v, err := parseHex64(h)
		if err != nil {
			panic(fmt.Sprintf("utils: bad phash constant %q: %v", h, err))
		}
		out = append(out, v)
	}
	return out
}()

func parseHex64(s string) (uint64, error) {
	// An empty string must be an error, not 0. Returning 0 would make
	// PhashDistanceTo(0x1234, "") report the distance to a hash of zero --
	// a plausible-looking number derived from nothing. The same applies to any
	// string whose digits are invalid: fail loudly so a bad diagnostic
	// argument cannot be mistaken for a real distance.
	if s == "" {
		return 0, fmt.Errorf("empty hex value")
	}

	var v uint64
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			v = v<<4 | uint64(c-'0')
		case c >= 'a' && c <= 'f':
			v = v<<4 | uint64(c-'a'+10)
		case c >= 'A' && c <= 'F':
			v = v<<4 | uint64(c-'A'+10)
		default:
			return 0, fmt.Errorf("invalid hex digit %q", c)
		}
	}
	return v, nil
}

// HammingDistance64 returns the number of differing bits between two hashes.
//
// phash_distance in SQLite counts differing bits the same way, so a value
// accepted here is accepted by the duplicate-finder too. If that SQL function
// ever changes, this must change with it.
func HammingDistance64(a, b uint64) int {
	return bits.OnesCount64(a ^ b)
}

// IsBadPhash reports whether phash matches a known-bad value from #2149, and
// if so returns that value in hex, for logging.
func IsBadPhash(phash uint64) (bool, string) {
	for _, bad := range badPhashPatterns {
		if d := HammingDistance64(phash, bad); d <= badPhashTolerance {
			return true, PhashToString(int64(bad))
		}
	}
	return false, ""
}

// PhashDistanceTo returns the Hamming distance between a phash and a hex
// string. It exists for log lines that report how close a rejected value was
// to the known-bad one it matched. An unparseable string yields -1 rather than
// a panic, because this is a diagnostic path and must not take down a task.
func PhashDistanceTo(phash uint64, hex string) int {
	other, err := parseHex64(hex)
	if err != nil {
		return -1
	}
	return HammingDistance64(phash, other)
}

// KnownBadPhashes returns the known-bad values, for logging and for tests that
// need the list without duplicating it.
func KnownBadPhashes() []string {
	out := make([]string, len(knownBadPhashes))
	copy(out, knownBadPhashes)
	return out
}
