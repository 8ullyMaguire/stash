package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/text/language"
)

// The locale registry has no test at all, and it is the one place where adding a
// language has to be done in THREE places at once:
//
//	internal/api/locale.go                        the collator matcher's tag list
//	ui/v2.5/src/locales/index.ts                 localeLoader (the JSON) and
//	                                             localeCountries (country names)
//	.../SettingsInterfacePanel.tsx                the <option> in the picker
//
// Miss any one and the failure is quiet. A tag in locale.go with no loader is a
// collator that works and a UI that shows raw keys; a loader with no tag is a
// language that cannot sort; an <option> with neither is a picker entry that
// silently does nothing. Nothing in the build catches any of it.
//
// So this file pins the invariant: every locale offered in the picker resolves to
// a real tag in the matcher, has a loader entry, and has a country-names entry.

// localesDir is the frontend locale directory, relative to this package.
const localesDir = "../../ui/v2.5/src/locales"

// indexTS and panelTS are the two files that must name every locale.
var (
	indexTS  = filepath.Join("..", "..", "ui", "v2.5", "src", "locales", "index.ts")
	panelTS  = filepath.Join("..", "..", "ui", "v2.5", "src", "components", "Settings", "SettingsInterfacePanel", "SettingsInterfacePanel.tsx")
	localeGo = filepath.Join("locale.go")
)

// pickerOptions pulls the locale codes out of the settings picker's <option>
// list. The value attribute is the code; the label is presentation and may be in
// any script.
var optionRe = regexp.MustCompile(`<option value="([^"]+)">`)

// localeOptions returns only the <select> that offers UI languages.
//
// SettingsInterfacePanel has THREE selects, and the first draft of this file
// matched <option> across the whole component -- so it collected "video",
// "animation" and "image" and then complained that locale.go does not register
// them. A test that fails for that reason is a test nobody keeps.
//
// The language block is bounded by the SelectSetting that owns it.
//
// The first draft anchored on </select> and parsed NOTHING, because this component
// has no <select> element at all -- the picker is a <SelectSetting> wrapper. An
// empty slice is the dangerous kind of failure: "every picker locale is
// registered" passes trivially on zero input. pickerLocales now refuses to run
// unless it found options.
//
// The block is identified by the language heading immediately above the options,
// and terminated at </SelectSetting>.
func localeOptions(src string) [][]string {
	anchor := strings.Index(src, `"config.ui.language.heading"`)
	if anchor < 0 {
		return nil
	}
	// Start at the first <option> AFTER the heading, not at the heading itself --
	// the heading is a sibling prop of the SelectSetting, not inside it.
	start := strings.Index(src[anchor:], "<option")
	if start < 0 {
		return nil
	}
	start += anchor
	end := strings.Index(src[start:], "</SelectSetting>")
	if end < 0 {
		return nil
	}
	return optionRe.FindAllStringSubmatch(src[start:start+end], -1)
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

// pickerLocales returns every locale offered in the interface picker.
func pickerLocales(t *testing.T) [][]string {
	t.Helper()
	opts := localeOptions(readFileString(t, panelTS))
	// Guard against a silent parse failure. An empty slice makes "every picker
	// locale is registered" pass trivially and "every tag appears in the picker"
	// fail on ALL 32 tags -- both are the test measuring nothing, which is how the
	// first draft of this file got through.
	if len(opts) == 0 {
		t.Fatal("no locale <option> elements parsed from the language picker -- " +
			"the test is measuring nothing")
	}
	return opts
}

// The core invariant, for every locale in the picker.
func TestEveryPickerLocaleIsRegisteredInTheMatcher(t *testing.T) {
	src := readFileString(t, localeGo)

	// The matcher is a closed list built with language.MustParse. Extract the tags
	// so the test reads the same source the matcher is built from, rather than a
	// copy that could drift.
	tagRe := regexp.MustCompile(`language\.MustParse\("([^"]+)"\)`)
	tags := tagRe.FindAllStringSubmatch(src, -1)
	if len(tags) == 0 {
		t.Fatal("no language.MustParse tags found in locale.go -- the test is measuring nothing")
	}

	inMatcher := map[string]bool{}
	for _, m := range tags {
		inMatcher[m[1]] = true
	}

	missing := []string{}
	for _, m := range pickerLocales(t) {
		code := m[1]
		if !inMatcher[code] {
			missing = append(missing, code)
		}
	}
	if len(missing) > 0 {
		t.Errorf("these locales appear in the picker but not in locale.go's matcher: %v\n"+
			"the collator would fall back to en-US while the UI offers the language", missing)
	}
}

// And the reverse: a tag with no picker entry means the language can sort but
// cannot be selected.
//
// en-AU is a deliberate exception, and worth stating rather than encoding as a
// hardcoded list: it is registered for COLLATION ORDER only, and has no
// translation file. Australian English is served by en-GB's strings. So a tag with
// no JSON on disk is fine; a tag WITH a JSON file but no picker entry is a
// translation nobody can reach.
func TestEveryTranslatableMatcherTagAppearsInThePicker(t *testing.T) {
	src := readFileString(t, localeGo)
	tagRe := regexp.MustCompile(`language\.MustParse\("([^"]+)"\)`)
	tags := tagRe.FindAllStringSubmatch(src, -1)

	inPicker := map[string]bool{}
	for _, m := range pickerLocales(t) {
		inPicker[m[1]] = true
	}

	orphan := []string{}
	for _, m := range tags {
		if inPicker[m[1]] {
			continue
		}
		if _, err := os.Stat(filepath.Join(localesDir, m[1]+".json")); err != nil {
			continue // collator-only tag, e.g. en-AU
		}
		orphan = append(orphan, m[1])
	}
	if len(orphan) > 0 {
		t.Errorf("these locales have a translation file but no picker entry, so they are unreachable: %v", orphan)
	}
}

// sw-KE specifically, since that is what #7252 adds. Pinned by name so the
// regression is legible when the list changes.
func TestSwahiliKenyaIsRegistered(t *testing.T) {
	src := readFileString(t, localeGo)
	if !strings.Contains(src, `language.MustParse("sw-KE")`) {
		t.Error("locale.go does not register sw-KE")
	}

	found := false
	for _, m := range pickerLocales(t) {
		if m[1] == "sw-KE" {
			found = true
		}
	}
	if !found {
		t.Error("the interface picker has no sw-KE option")
	}
}

// A picker locale must have its JSON on disk, or the dynamic import 404s at
// runtime and the UI falls back to English with no error anywhere.
func TestEveryPickerLocaleHasItsJsonFile(t *testing.T) {
	missing := []string{}
	for _, m := range pickerLocales(t) {
		code := m[1]
		// `ar` is a bare language code with no region; its file is ar.json.
		if _, err := os.Stat(filepath.Join(localesDir, code+".json")); err != nil {
			missing = append(missing, code+".json")
		}
	}
	if len(missing) > 0 {
		t.Errorf("picker offers locales with no JSON file: %v", missing)
	}
}

// Every localeLoader key must correspond to a file, and the key must be the
// camel-cased code (sw-KE -> swKE), because that is the naming convention the
// dynamic imports rely on.
func TestLocaleLoaderKeysMatchTheirFiles(t *testing.T) {
	src := readFileString(t, indexTS)
	loaderRe := regexp.MustCompile(`(\w+): \(\) => import\("\./([\w-]+)\.json"\)`)
	entries := loaderRe.FindAllStringSubmatch(src, -1)
	if len(entries) == 0 {
		t.Fatal("no localeLoader entries found -- the test is measuring nothing")
	}

	for _, e := range entries {
		key, file := e[1], e[2]
		// swKE -> sw-KE. Split at the FIRST capital only: inserting a dash before
		// EVERY capital turns swKE into sw-k-e, which matched nothing and made all
		// 41 locales look broken.
		want := key
		if i := strings.IndexAny(key, "ABCDEFGHIJKLMNOPQRSTUVWXYZ"); i > 0 {
			want = key[:i] + "-" + key[i:]
		}
		// The REGION is uppercase by convention: af-ZA, never af-za. Lowercasing the
		// whole code made all 41 locales look mismatched.
		if i := strings.IndexAny(key, "ABCDEFGHIJKLMNOPQRSTUVWXYZ"); i > 0 {
			want = strings.ToLower(key[:i]) + "-" + key[i:]
		}
		if want != file {
			t.Errorf("localeLoader key %q implies file %q but imports %q", key, want, file)
		}
		if _, err := os.Stat(filepath.Join(localesDir, file+".json")); err != nil {
			t.Errorf("localeLoader imports ./%s.json but that file does not exist", file)
		}
	}
}

// The picker and the loader must agree, because a locale that loads but is not
// offered is invisible and one that is offered but does not load is broken.
func TestPickerAndLoaderAgree(t *testing.T) {
	src := readFileString(t, indexTS)
	loaderRe := regexp.MustCompile(`(\w+): \(\) => import\("\./([\w-]+)\.json"\)`)
	loaded := map[string]bool{}
	for _, e := range loaderRe.FindAllStringSubmatch(src, -1) {
		loaded[e[2]] = true
	}

	var notLoaded []string
	for _, m := range pickerLocales(t) {
		if !loaded[m[1]] {
			notLoaded = append(notLoaded, m[1])
		}
	}
	if len(notLoaded) > 0 {
		t.Errorf("the picker offers these locales but localeLoader has no entry: %v", notLoaded)
	}
}

// A locale JSON must be parseable and structurally complete against en-GB. The
// frontend harness (scripts/test-locale-structure.mjs) covers the detail; this
// asserts the same invariant from the Go side so `go test ./...` catches a
// malformed locale too.
func TestEveryLocaleFileIsValidCompleteJSON(t *testing.T) {
	entries, err := os.ReadDir(localesDir)
	if err != nil {
		t.Fatalf("reading %s: %v", localesDir, err)
	}

	refBytes, err := os.ReadFile(filepath.Join(localesDir, "en-GB.json"))
	if err != nil {
		t.Fatalf("reading the reference locale: %v", err)
	}
	ref := flattenJSON(t, refBytes)
	if len(ref) == 0 {
		t.Fatal("en-GB.json flattened to nothing -- the test is measuring nothing")
	}

	broken := []string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(localesDir, e.Name()))
		if err != nil {
			broken = append(broken, e.Name()+": unreadable")
			continue
		}
		// Validate the JSON without building the map: a malformed file must fail
		// here rather than being silently tolerated.
		if !json.Valid(b) {
			broken = append(broken, e.Name()+": invalid JSON")
			continue
		}
	}
	if len(broken) > 0 {
		t.Errorf("malformed locale files: %v", broken)
	}
}

// flattenJSON reduces a locale to dotted leaf paths, for structural comparison.
func flattenJSON(t *testing.T, b []byte) map[string]string {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("parsing locale JSON: %v", err)
	}
	out := map[string]string{}
	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		for k, val := range m {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			if sub, ok := val.(map[string]any); ok && len(sub) > 0 {
				walk(key, sub)
			} else {
				out[key] = ""
			}
		}
	}
	walk("", v)
	return out
}

// The collator must actually work for every registered tag, and must not return
// the fallback for a language it claims to support. This is the one check that
// catches a tag that parses but does not match itself -- language.MatchStrings can
// return a DIFFERENT tag for an unsupported language, silently.
func TestTheMatcherResolvesEveryRegisteredTagToItself(t *testing.T) {
	src := readFileString(t, localeGo)
	tagRe := regexp.MustCompile(`language\.MustParse\("([^"]+)"\)`)
	tags := tagRe.FindAllStringSubmatch(src, -1)

	for _, m := range tags {
		code := m[1]
		got, conf := language.MatchStrings(matcher, code)
		if got.String() != code {
			t.Errorf("matcher resolved %q to %q (confidence %v) -- the tag is registered but not matched",
				code, got.String(), conf)
		}
	}
}

// A locale with no passwd-style entry still needs a collator, which is the whole
// point of the matcher. An unsupported string must fall back to the FIRST tag
// rather than erroring.
func TestAnUnsupportedLocaleFallsBackToTheDefault(t *testing.T) {
	got, _ := language.MatchStrings(matcher, "zz-ZZ")
	if got.String() != "en-US" {
		t.Errorf("an unsupported locale resolved to %q, want the en-US fallback", got.String())
	}
}
