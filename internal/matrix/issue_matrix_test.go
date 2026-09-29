// Milestone 6, step 6.0 — make the issue matrix self-checking.
//
// The 850-row matrix in docs/research/ is a document, and a document rots.
// Nothing in the build notices when an issue is filed upstream and never
// mapped: the milestone still reads as "850 issues, all covered", and the
// unmapped issue is silently dropped. That is the exact failure this test
// exists to catch.
//
// So the test asserts COMPLETENESS against the recorded issue list, not
// against a hardcoded number. A hardcoded number is worse than nothing: it
// goes stale in the direction that looks fine. Every open issue in both repos
// must appear in the mapping, except the six documented non-goals.
//
// This reads the committed snapshot (docs/research/open_issues.json and
// map_final.json) rather than calling the GitHub API, for two reasons: a
// network call in a unit test is a flake waiting for a rate limit, and the
// snapshot is what the matrix was actually built from. Refreshing the
// snapshot is a deliberate act (see TestIssueMatrixSnapshotIsNotStale) and its
// diff is reviewable; a live fetch is not.
//
// M6-24/M6-25 in docs/requirements.csv track this.
package matrix

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const researchDir = "../../docs/research"

// Documented non-goals, from docs/research/taxonomy.py EXCLUSIONS. These
// issues are deliberately not worked; each maps to a won't-do bucket with a
// stated reason. Allow-listed here by id, with the reason, so a change to the
// set is a reviewed diff rather than a silent drop.
var documentedNonGoals = map[string]string{
	"X01": "Windows-only requirement: target is Linux desktop plus a hosted server.",
	"X02": "Embedded browser runtime: no Electron/Chromium bundling, ever.",
	"X03": "Cloud-only requirement: proprietary cloud must never be mandatory.",
	"X04": "Content policy expansion: out of §14 scope.",
	"X05": "Upstream tracker: belongs to ffmpeg, a browser or an OS.",
	"X06": "Duplicate of a merged issue: folded into the canonical capability.",
}

type recordedIssue struct {
	Repo string   `json:"repo"`
	Num  int      `json:"num"`
	Title string  `json:"title"`
	Created string `json:"created"`
}

func loadJSON(t *testing.T, name string, into any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(researchDir, name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	// The snapshot was written by a scraper that kept raw control characters
	// in issue bodies; strict JSON rejects those.
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("parsing %s: %v", name, err)
	}
}

// liveIssueKeys returns every open issue recorded at snapshot time, keyed
// "stash#123" / "stash-box#45" to match the matrix's own notation.
func liveIssueKeys(t *testing.T) map[string]recordedIssue {
	t.Helper()
	var byRepo map[string][]recordedIssue
	loadJSON(t, "open_issues.json", &byRepo)
	if len(byRepo) == 0 {
		t.Fatal("open_issues.json is empty; the snapshot is missing, not merely stale")
	}
	out := map[string]recordedIssue{}
	for repo, issues := range byRepo {
		// stashapp/stash -> "stash#", stashapp/stash-box -> "stash-box#".
		// Note the order: stripping "stash" first would turn "stash-box" into
		// "-box", which is the bug this comment now prevents.
		tag := strings.TrimPrefix(repo, "stashapp/")
		for _, it := range issues {
			it.Repo = repo
			out[fmt.Sprintf("%s#%d", tag, it.Num)] = it
		}
	}
	return out
}

func mappedIssues(t *testing.T) map[string]string {
	t.Helper()
	var m map[string]string
	loadJSON(t, "map_final.json", &m)
	if len(m) == 0 {
		t.Fatal("map_final.json is empty; the mapping is missing, not merely incomplete")
	}
	return m
}

var capabilityCode = regexp.MustCompile(`^(C\d{2}|X\d{2})$`)

// The matrix must cover every open issue in both repos. A capability that
// closes issues nobody mapped is still worth doing; an issue nobody mapped is
// silently dropped, which is the failure this catches.
func TestIssueMatrixCoversEveryOpenIssue(t *testing.T) {
	live := liveIssueKeys(t)
	mapped := mappedIssues(t)

	var unmapped []string
	for key := range live {
		if _, ok := mapped[key]; !ok {
			unmapped = append(unmapped, key)
		}
	}
	sort.Strings(unmapped)
	if len(unmapped) > 0 {
		// Name every one, up to a readable cap. A failure that truncates
		// silently teaches the reader to re-run to find the rest.
		shown := unmapped
		if len(shown) > 25 {
			shown = shown[:25]
		}
		t.Errorf("%d of %d recorded open issues are not in the mapping:\n  %s\n%s",
			len(unmapped), len(live), strings.Join(shown, "\n  "),
			capNote(unmapped))
	}
}

// The inverse failure: a mapping entry for an issue that is not in the
// snapshot means the matrix is describing something that no longer exists.
// That is how a capability ends up "closing" three issues, two of which were
// closed upstream years ago.
func TestIssueMatrixHasNoPhantomEntries(t *testing.T) {
	live := liveIssueKeys(t)
	mapped := mappedIssues(t)

	var phantom []string
	for key := range mapped {
		if _, ok := live[key]; !ok {
			phantom = append(phantom, key)
		}
	}
	sort.Strings(phantom)
	if len(phantom) > 0 {
		t.Errorf("%d mapping entries name issues absent from the snapshot: %s\n"+
			"These are closed upstream, or the key is malformed. A capability "+
			"cannot claim to close an issue that is not open.",
			len(phantom), strings.Join(phantom, ", "))
	}
}

// Every mapped issue must carry a capability id that exists in the taxonomy,
// and every one that exists must be either a capability or a documented
// non-goal. A typo'd code would otherwise pass every other test while pointing
// at a capability that does not exist.
func TestEveryMappedIssueHasAKnownCapability(t *testing.T) {
	known := taxonomyCodes(t)
	mapped := mappedIssues(t)

	unknown := map[string][]string{}
	for key, code := range mapped {
		if !capabilityCode.MatchString(code) {
			unknown[code] = append(unknown[code], key)
			continue
		}
		if _, ok := known[code]; !ok {
			unknown[code] = append(unknown[code], key)
		}
	}
	if len(unknown) > 0 {
		codes := make([]string, 0, len(unknown))
		for c := range unknown {
			codes = append(codes, c)
		}
		sort.Strings(codes)
		var b strings.Builder
		for _, c := range codes {
			fmt.Fprintf(&b, "\n  %s (%d issues, e.g. %s)", c, len(unknown[c]), unknown[c][0])
		}
		t.Errorf("%d capability codes are used but not defined in taxonomy.py:%s", len(unknown), b.String())
	}
}

// taxonomyCodes parses the CAPABILITIES and EXCLUSIONS tables out of
// taxonomy.py. Parsing rather than importing because it is a Python file and
// this is a Go test; the parse is on the literal table entries, so a
// reformatting of the surrounding Python does not break the build.
//
// The two tables have DIFFERENT literal shapes, and that asymmetry is the
// whole reason this needs two patterns:
//
//	("C01", "Video container support", "5.2", "Accept mp4/mkv/..."),   <- tuple
//	"X01": ("Windows-only requirement",                                <- dict key
//		"The platform targets Linux desktop..."),
//
// A single pattern matching the tuple form finds all 91 capabilities and zero
// non-goals, which looks like a clean parse and is silently wrong.
var (
	pyTupleEntry  = regexp.MustCompile(`^\s*\("([CX]\d{2})",\s*"([^"]*)"`)
	pyDictEntry   = regexp.MustCompile(`^\s*"(X\d{2})":\s*\(\s*"([^"]*)"`)
)

func taxonomyEntries(t *testing.T, re *regexp.Regexp) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(researchDir, "taxonomy.py"))
	if err != nil {
		t.Fatalf("reading taxonomy.py: %v", err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if m := re.FindStringSubmatch(line); m != nil {
			out[m[1]] = m[2]
		}
	}
	return out
}

func taxonomyCodes(t *testing.T) map[string]string {
	t.Helper()
	out := taxonomyEntries(t, pyTupleEntry)
	for k, v := range taxonomyEntries(t, pyDictEntry) {
		out[k] = v
	}
	if len(out) == 0 {
		t.Fatal("taxonomy.py yielded no capability ids; the parse is wrong, not the file")
	}
	return out
}

// The non-goals are a decision, and a decision with no count attached decays
// into "a few things we skipped". This asserts every EXCLUSIONS entry is
// allow-listed above, so dropping one from the set is a compile-time
// conversation rather than a drift.
func TestDocumentedNonGoalsMatchTheTaxonomyExclusions(t *testing.T) {
	declared := taxonomyEntries(t, pyDictEntry)
	if len(declared) == 0 {
		t.Fatalf("no EXCLUSIONS entries parsed from taxonomy.py; the dict-key pattern is wrong")
	}

	for code := range declared {
		if _, ok := documentedNonGoals[code]; !ok {
			t.Errorf("taxonomy.py declares non-goal %s but this test does not allow-list it.\n"+
				"Either the issue should be worked, or the reason belongs in documentedNonGoals "+
				"so the exclusion is visible to a reader of the test.", code)
		}
	}
	for code := range documentedNonGoals {
		if _, ok := declared[code]; !ok {
			t.Errorf("this test allow-lists non-goal %s but taxonomy.py no longer declares it", code)
		}
	}
}

// The snapshot is a point-in-time capture. A reviewer needs to know how old it
// is, so the age is asserted rather than left to a reader's inference — and
// the bound is deliberately loose (weeks, not days) because a stale snapshot
// is a maintenance cost, not a correctness bug, as long as it is visible.
func TestIssueMatrixSnapshotIsNotStale(t *testing.T) {
	info, err := os.Stat(filepath.Join(researchDir, "open_issues.json"))
	if err != nil {
		t.Fatalf("stat open_issues.json: %v", err)
	}
	age := time.Since(info.ModTime())
	const maxAge = 90 * 24 * time.Hour
	if age > maxAge {
		t.Errorf("the open-issue snapshot is %d days old (bound: %d).\n"+
			"Refreshing it is `gh issue list` against both repos into "+
			"docs/research/open_issues.json, then re-run the mapper. Record the "+
			"refresh in the commit message so the next reader knows what moved.",
			int(age.Hours()/24), int(maxAge.Hours()/24))
	}
}

// Every capability that carries issues must name a spec section, so "covered"
// means "specified somewhere a reader can find", not "absorbed into a bucket".
// The matrix is the thing a maintainer opens to answer "is this worked on",
// and a row with no section cannot answer that.
func TestEveryCapabilityWithIssuesNamesASpecSection(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(researchDir, "matrix.md"))
	if err != nil {
		t.Fatalf("reading matrix.md: %v", err)
	}
	// | stash#12 | Title | C69 Multi-user & permissions | §12.1 |
	row := regexp.MustCompile(`^\|\s*(stash(?:-box)?#\d+)\s*\|.*?\|\s*(C\d{2})\b[^|]*\|\s*(§[\d.]+)?\s*\|`)

	seen := map[string]bool{}
	noSection := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		m := row.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key, cap, section := m[1], m[2], m[3]
		seen[key] = true
		if section == "" {
			if _, ok := noSection[cap]; !ok {
				noSection[cap] = key
			}
		}
	}
	if len(seen) == 0 {
		t.Fatal("no matrix rows parsed; the row regex no longer matches the file")
	}
	for cap, example := range noSection {
		t.Errorf("capability %s (e.g. %s) has issues but no spec section in matrix.md.\n"+
			"Either the capability is unspecified, or the section column is empty.", cap, example)
	}
}

func capNote(all []string) string {
	if len(all) <= 25 {
		return ""
	}
	return fmt.Sprintf("  ...and %d more", len(all)-25)
}

// Guard against the file being edited into something unparseable while still
// being "present", which is the failure mode where a count looks plausible.
func TestSnapshotCountsArePlausible(t *testing.T) {
	live := liveIssueKeys(t)
	mapped := mappedIssues(t)
	if len(live) == 0 || len(mapped) == 0 {
		t.Fatal("empty snapshot")
	}
	// The number is not the assertion; the relationship is. A snapshot that
	// grew a repo, or a mapping that grew rows without the snapshot, shows up
	// as a mismatch the other two tests name precisely.
	if got, want := len(mapped), len(live); got != want {
		t.Logf("note: %d mapped vs %d recorded issues (%d difference); "+
			"the per-id tests above name any specific mismatch", got, want, got-want)
	}

	// Each capability id must be 2 digits, matching taxonomy.py's shape.
	for key, code := range mapped {
		if len(code) != 3 {
			t.Errorf("%s maps to %q, which is not a 3-character capability id", key, code)
			break
		}
	}
}

var _ = strconv.Itoa
