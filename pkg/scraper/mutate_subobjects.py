#!/usr/bin/env python3
"""Mutation harness for the stash#7263 sub-object alignment fix.

A green test proves nothing until you watch it go red. Each mutation below
breaks the fix in a way a careless future edit might plausibly break it, and
the harness asserts the tests CATCH it. A surviving mutation means the tests
are blind to that class of defect.

The alignment property is the whole bug, so most of these mutations attack it
directly: put the per-attribute cleaning back, drop one of the two routes, or
reintroduce the one-element append that turns a sparse index into a panic.

Run:  python3 pkg/scraper/mutate_subobjects.py
"""

import subprocess
import sys
from pathlib import Path

PKG = Path(__file__).resolve().parent
ROOT = PKG.parent.parent
CONFIG = PKG / "mapped_config.go"
RESULT = PKG / "mapped_result.go"
MAPPED = PKG / "mapped.go"

ORIG = {p: p.read_text() for p in (CONFIG, RESULT, MAPPED)}

RUN = [
    "TestOnlyThePerformersWithAGenderGetOne",
    "TestAGenderInTheMiddleDoesNotShift",
    "TestAPerformerWithNoGenderNodeHasNoGenderAtAll",
    "TestARepeatedValueDoesNotShiftLaterAttributes",
    "TestDuplicateNamedSubObjectsAreCollapsedAfterAssembly",
    "TestANamelessSubObjectIsDropped",
    "TestTwoNamelessSubObjectsAreBothDropped",
    "TestTagsAreDeduplicatedByName",
    "TestTheSingleObjectRouteStillCleansPerAttribute",
    "TestTheConcatSplitBranchIsAlsoAligned",
    "TestASceneScrapeKeepsItsPerformersAligned",
    "TestASceneScrapeKeepsItsTagsAligned",
    "TestAnImageScrapeKeepsItsPerformersAligned",
    "TestAGalleryScrapeKeepsItsPerformersAligned",
    "TestMappedResultsSetSingleValue",
    "TestMappedResultsSetMultiValue",
]


def run_tests() -> tuple[int, str]:
    proc = subprocess.run(
        ["go", "test", "./", "-run", "|".join(RUN), "-count=1"],
        cwd=PKG,
        capture_output=True,
        text=True,
        timeout=300,
    )
    return proc.returncode, proc.stdout + proc.stderr


# Each mutation: (name, path, old, new, must_kill)
MUTATIONS = [
    (
        "the feature removed: sub-objects clean per attribute again",
        CONFIG,
        "\treturn s.processInternal(ctx, q, common, isMulti, true)\n}\n\n// processInternal",
        "\treturn s.processInternal(ctx, q, common, isMulti, false)\n}\n\n// processInternal",
        "the middle-gender, repeated-value and nameless tests",
    ),
    (
        "empty values removed again, which is one of the two shift triggers",
        CONFIG,
        "\t\tif q.getType() == SearchQuery || keepPositions {\n\t\t\treturn ret\n\t\t}\n\t\tret = attrConfig.cleanResults(ret)",
        "\t\tif q.getType() == SearchQuery {\n\t\t\treturn ret\n\t\t}\n\t\tret = attrConfig.cleanResults(ret)",
        "the middle-gender test, where the page has empty gender nodes",
    ),
    (
        "duplicates removed again -- the other shift trigger (plain branch)",
        CONFIG,
        "\t\tret = attrConfig.cleanResults(ret)\n\n\t}",
        "\t\t_ = ret\n\n\t}",
        "the repeated-value test -- cleanResults removed entirely",
    ),
    (
        "duplicates removed again -- the same trigger on the concat+split branch",
        CONFIG,
        "\t\t\tif q.getType() == SearchQuery || keepPositions {\n\t\t\t\treturn results\n\t\t\t}",
        "\t\t\tif q.getType() == SearchQuery {\n\t\t\t\treturn results\n\t\t\t}",
        "the concat+split test",
    ),
    (
        "splitStringPreservingEmpty drops empty segments again",
        CONFIG,
        "\treturn strings.Split(value, separator)\n}",
        "\tvar res []string\n\tfor _, str := range strings.Split(value, separator) {\n\t\tif str != \"\" {\n\t\t\tres = append(res, str)\n\t\t}\n\t}\n\treturn res\n}",
        "the concat+split test, which is the only one with an empty segment",
    ),
    (
        "postProcess stops using splitStringPreservingEmpty for sub-objects",
        CONFIG,
        "\t\t\tif keepPositions {\n\t\t\t\tresults = attrConfig.splitStringPreservingEmpty(result)\n\t\t\t} else {\n\t\t\t\tresults = attrConfig.splitString(result)\n\t\t\t}",
        "\t\t\tresults := attrConfig.splitString(result)",
        "the concat+split test",
    ),
    (
        "the one-element append is back, so a sparse index panics",
        RESULT,
        "\tfor len(r) <= index {\n\t\tr = append(r, make(mappedResult))\n\t}",
        "\tif index >= len(r) {\n\t\tr = append(r, make(mappedResult))\n\t}",
        "the sparse-index case, and the middle-gender test",
    ),
    (
        "growTo grows by one instead of to the index",
        RESULT,
        "\tfor len(r) <= index {\n\t\tr = append(r, make(mappedResult))\n\t}",
        "\tif len(r) <= index {\n\t\tr = append(r, make(mappedResult))\n\t}",
        "the sparse-index case",
    ),
    (
        "dedupeByName does not deduplicate",
        RESULT,
        "\t\tif seen[name] {\n\t\t\tlogger.Debugf(\"Dropping duplicate sub-object %q\", name)\n\t\t\tcontinue\n\t\t}",
        "",
        "the duplicate-collapse and tag tests",
    ),
    (
        "dedupeByName keeps the LAST occurrence instead of the first",
        RESULT,
        "\t\tif seen[name] {\n\t\t\tlogger.Debugf(\"Dropping duplicate sub-object %q\", name)\n\t\t\tcontinue\n\t\t}\n\t\tseen[name] = true",
        "\t\tif seen[name] {\n\t\t\tret = ret[:len(ret)-1]\n\t\t}\n\t\tseen[name] = true",
        "the duplicate-collapse test, which asserts first-wins",
    ),
    (
        "dedupeByName keeps nameless objects again",
        RESULT,
        "\t\tname, ok := result[\"Name\"].(string)\n\t\tif !ok || name == \"\" {\n\t\t\tlogger.Debug(\"Dropping sub-object with no name\")\n\t\t\tcontinue\n\t\t}",
        "\t\tname, ok := result[\"Name\"].(string)\n\t\tif false && (!ok || name == \"\") {\n\t\t\tlogger.Debug(\"Dropping sub-object with no name\")\n\t\t\tcontinue\n\t\t}",
        "the nameless tests and the tag-route test",
    ),
    (
        "the scene performers route no longer skips cleaning",
        MAPPED,
        "performerResults := performersMap.processSubObjects(ctx, q, s.Common, nil).dedupeByName()",
        "performerResults := performersMap.process(ctx, q, s.Common, nil)",
        "the scene-scrape test, which goes through the real mapped.go route",
    ),
	# The tag route is deliberately NOT mutation-tested for this property, and
	# the reason is worth stating: tags carry ONE attribute, and for a single
	# attribute "unique + delete empty" and "keep positions + dedupe by name"
	# produce the same list. Reverting the tag route to process is therefore an
	# output-equivalent refactor, not a bug, and no fixture can tell them apart.
	# The image-performers route below is multi-attribute and IS testable.
    (
        "the image performers route no longer skips cleaning",
        MAPPED,
        "ret.Performers = imagePerformersMap.processSubObjects(ctx, q, s.Common, nil).dedupeByName().scrapedPerformers()",
        "ret.Performers = imagePerformersMap.process(ctx, q, s.Common, nil).scrapedPerformers()",
        "the image-scrape test, which goes through the real mapped.go route",
    ),
    (
        "the gallery performers route no longer skips cleaning",
        MAPPED,
        "performerResults := galleryPerformersMap.processSubObjects(ctx, q, s.Common, urlsIsMulti).dedupeByName()",
        "performerResults := galleryPerformersMap.process(ctx, q, s.Common, urlsIsMulti)",
        "the gallery-scrape test",
    ),
]


def main() -> int:
    print("stash#7263 -- mutation harness\n")

    code, out = run_tests()
    if code != 0:
        print("FATAL: the tests fail on unmutated code.")
        print(out[-4000:])
        return 1
    print("  baseline: all tests pass\n")

    killed = 0
    survivors = []

    for name, path, old, new, why in MUTATIONS:
        if old not in ORIG[path]:
            print(f"  ERROR  {name}: anchor not found -- update the harness")
            survivors.append(name)
            continue

        path.write_text(ORIG[path].replace(old, new, 1))
        try:
            code, out = run_tests()
        finally:
            path.write_text(ORIG[path])

        if code != 0:
            killed += 1
            failed = [
                ln.strip() for ln in out.splitlines()
                if ln.strip().startswith("--- FAIL") or "panic:" in ln
            ]
            detail = failed[0].strip()[:82] if failed else "(non-zero exit)"
            print(f"  killed  {name}")
            print(f"          by: {detail}")
            print(f"          covers: {why}")
        else:
            survivors.append(name)
            print(f"  SURVIVED  {name}  <-- the tests are blind to this")
            print(f"          expected it to be caught by: {why}")

    print(f"\n  {killed} killed, {len(survivors)} survived")
    if survivors:
        print("\n  survivors (the tests cannot detect these):")
        for s in survivors:
            print(f"    - {s}")
        return 1

    for p, original in ORIG.items():
        if p.read_text() != original:
            print(f"\n  FATAL: {p.name} was not restored")
            return 1
    print("  sources restored, harness clean")
    return 0


if __name__ == "__main__":
    sys.exit(main())