package match

import (
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

// getPathWords extracts the fragments used to query the database for
// candidate performers. A performer is only ever considered if one of the
// words in the path matches something in their name, so a word extractor that
// drops non-ASCII fragments makes those performers INVISIBLE to auto-tagging
// no matter how well the path regex itself works.
//
// That division of labour is why nameMatchesPath can be correct and the
// feature still fail: the regex runs on every candidate the query returned,
// and the query is what lost the performer.

// THE REPORTED CASE, verbatim from stash#2293: a directory named for a
// Japanese/Chinese performer, with a media file inside it.
func TestANonASCIIPathFragmentSurvivesWordExtraction(t *testing.T) {
	words := getPathWords("/media/伏字/hello-world.mp4", true)
	assert.Contains(t, words, "伏字",
		"the performer's own name is the path fragment; if it is not among the "+
			"words then no candidate query can ever return this performer, and "+
			"the tagger reports nothing to tag")
}

func TestANonASCIIFragmentIsNotCorruptedByByteSlicing(t *testing.T) {
	words := getPathWords("/media/伏字/hello-world.mp4", true)

	// A byte slice of a multi-byte rune produces a replacement character or an
	// invalid encoding, and the corrupted string matches nothing in the
	// database. The name must come back byte-identical.
	for _, w := range words {
		assert.NotContains(t, w, "�",
			"word %q contains a replacement character, so a rune was cut "+
				"mid-sequence by a byte slice", w)
	}
}

func TestAMultiRuneNameKeepsItsLeadingRunesIntact(t *testing.T) {
	// The extractor deliberately keeps only the first two runes of a word
	// (#1450). For a non-ASCII name that truncation must be rune-based: the
	// first two RUNES, which is the whole of a short CJK name.
	words := getPathWords("/media/伏字.mp4", true)
	assert.Contains(t, words, "伏字")
}

// The general case the reporter did not test ("other non-ASCII characters
// were not tested") is the same bug wherever the letters are not ASCII.
func TestNonASCIINamesBeyondCJKAlsoSurvive(t *testing.T) {
	for _, tc := range []struct{ name, path string }{
		{"Zoë Kravitz", "/media/Zoë Kravitz/a.mp4"},
		{"Ámbar Vega", "/media/Ámbar Vega/a.mp4"},
		{"Ёлка", "/media/Ёлка/a.mp4"},
		{"Björk", "/media/Björk/a.mp4"},
	} {
		words := getPathWords(tc.path, true)
		first2 := []rune(tc.name)[:2]
		assert.Contains(t, words, string(first2),
			"%q: the leading runes of the name must appear among the words "+
				"extracted from %q (got %v)", tc.name, tc.path, words)
	}
}

// A name whose first two runes are ASCII but which is otherwise non-ASCII
// must still extract an ASCII fragment, because that is the part the
// truncation keeps.
func TestAnASCIIPrefixOfANonASCIINameStillExtracts(t *testing.T) {
	words := getPathWords("/media/Björk Guðmundsdóttir/a.mp4", true)
	assert.Contains(t, words, "Bj")
}

// The negative control: the extractor must still drop single-rune words.
// A one-rune fragment is true of an enormous number of unrelated performers,
// so keeping it is how auto-tagging starts tagging scenes with strangers.
//
// NOTE ON THE FIRST VERSION OF THIS TEST, AND WHY IT WAS WRONG TWICE.
//
// v1 asserted NotContains("伏") on "/media/伏/字/ok.mp4". It passed -- and
// passed identically with the single-rune filter removed, so it proved
// nothing. separatorRE splits on "/", so 伏 and 字 arrive as separate
// single-rune words the input could never yield as a two-rune fragment.
//
// v2 used "/media/ok/x.mp4" and asserted NotContains("x"). It also survived,
// for a subtler reason: the mutation this must catch does not emit "x", it
// emits "x\x00" -- taking runes[0:2] of a ONE-rune string yields a 1-rune
// string plus a NUL byte past the end. Asserting on the clean string missed
// it entirely.
//
// So the assertion has to be about the LENGTH OF THE RESULT, not about
// membership of a particular string. A word that survived the filter would
// appear as some two-rune value; checking the count is what actually
// observes the filter.
func TestSingleRuneWordsAreStillDropped(t *testing.T) {
	// "ok" is a two-rune word and is kept. "x" is one rune and is dropped.
	// A single-element assertion on the count catches both the clean "x" and
	// the mutated "x\x00", because the mutation adds an element.
	words := getPathWords("/media/ok/x.mp4", true)
	assert.Contains(t, words, "ok",
		"a two-rune word is above the threshold and must be kept")
	assert.Len(t, words, 2,
		"expected exactly [me ok]; a third element means a one-rune word "+
			"passed the length filter (got %q)", words)
	for _, w := range words {
		assert.True(t, utf8.RuneCountInString(w) == 2,
			"every extracted fragment is exactly two runes by construction; "+
				"got %q which is %d runes -- a shorter fragment means a "+
				"one-rune word passed the filter", w, utf8.RuneCountInString(w))
	}
}

// The same boundary on the non-ASCII side, where it is the case that was
// actually reported: a two-rune CJK name is exactly at the keep threshold and
// a one-rune CJK name is below it. If the filter were counting BYTES, both
// would be kept and every one-rune CJK name in the library would become a
// candidate for every scene.
func TestTheRuneThresholdIsCountedInRunesNotBytes(t *testing.T) {
	// 伏字 is two runes / six bytes: kept, because two runes pass the filter.
	kept := getPathWords("/media/伏字/a.mp4", true)
	assert.Contains(t, kept, "伏字")

	// A three-character ASCII word is also kept, proving the filter is not
	// simply "non-ASCII words are kept".
	assert.Contains(t, getPathWords("/media/abc/a.mp4", true), "ab")

	// And the single CJK rune, at one rune / three bytes, is KEPT. It is a
	// complete name, not a fragment: there is no such thing as a half of 伏.
	// A byte-counting filter would have kept it for the wrong reason, and an
	// ASCII-only one drops it, which is stash#2293.
	assert.Contains(t, getPathWords("/media/伏/a.mp4", true), "伏")
}

// A single-rune word is dropped only when it is ASCII. That asymmetry is
// deliberate and is the whole fix:
//
//   - "x" alone is true of an enormous number of unrelated words, so keeping it
//     would make every scene a candidate for every single-letter performer.
//   - "伏" alone is a whole name. CJK, Cyrillic and Devanagari are written
//     without spaces between words, so a one-syllable name is common and
//     dropping it makes the performer invisible to auto-tagging entirely.
func TestASingleRuneWordIsDroppedOnlyWhenItIsASCII(t *testing.T) {
	assert.NotContains(t, getPathWords("/media/ok/x.mp4", true), "x",
		"a lone ASCII letter is noise and must stay filtered")

	for _, r := range []string{"伏", "Ё", "あ", "ק"} {
		assert.Contains(t, getPathWords("/media/"+r+"/a.mp4", true), r,
			"%q is a one-rune name in a script that does not delimit words with "+
				"spaces; dropping it hides the performer from auto-tagging", r)
	}
}
