#!/usr/bin/env python3
"""
Locate the code each `planned` bug report is about, so the verdict rests on this tree rather than
on the issue title.

## WHY TITLE-MATCHING IS NOT ENOUGH

An issue title says what a reporter SAW. A verdict needs what THIS codebase DOES. The failure
mode is concrete and has already happened in this project: an issue titled "performers sub-page
includes objects from other studios" sounds like an absent filter, and measuring it showed the
filter exists and works -- the defect is three fields in a card. Reading the title would have
produced `not-planned`, i.e. "upstream is wrong", which is worse than not answering.

So for each row this extracts candidate files by keyword, then reports which ones exist and which
keywords had NO hit at all. A keyword with no hit is the interesting signal: the code may not be
here, which is a `deferred` reason (wrong surface), not a `not-planned` one.

Read-only. Prints a table; writes nothing, because a keyword hit is a CANDIDATE, not a verdict.
"""
from __future__ import annotations

import json
import pathlib
import re
import sys

REPO = pathlib.Path(__file__).resolve().parents[1]
LEDGER = REPO / "docs" / "UPSTREAM-ISSUES.md"

# Directories worth searching: the code, not the vendored/built trees.
SEARCH_ROOTS = ["internal", "pkg", "ui/v2.5/src", "graphql"]
SKIP = re.compile(r"generated-graphql|node_modules|\.min\.|_test\.|\.snap$")

# keyword -> glob fragments. Deliberately coarse: this is a candidate finder, and a miss is
# reported rather than hidden.
KEYWORDS: list[tuple[str, list[str]]] = [
    ("scraper", ["scraper"]),
    ("freeones", ["freeones"]),
    ("synology", ["synology", "nas"]),
    ("chromecast", ["chromecast"]),
    ("lightbox", ["lightbox", "Lightbox"]),
    ("tagger", ["Tagger"]),
    ("preview", ["preview"]),
    ("screenshot", ["screenshot"]),
    ("transcode", ["transcode", "hwaccel", "hwdecode"]),
    ("vr", ["vrmode", "vr"]),
    ("playlist", ["playlist", "m3u"]),
    ("keyboard", ["mousetrap", "keybind"]),
    ("mobile", ["mobile", "android", "ios"]),
    ("thumbnail", ["thumbnail"]),
    ("performer", ["performer"]),
    ("gallery", ["gallery"]),
    ("movie", ["movie"]),
    ("studio", ["studio"]),
    ("tag", ["Tag"]),
    ("marker", ["marker"]),
    ("group", ["group"]),
    ("upload", ["upload"]),
    ("path", ["filepath", "path"]),
    ("sort", ["sort"]),
    ("filter", ["filter"]),
    ("metadata", ["metadata", "Metadata"]),
    ("backup", ["backup"]),
    ("plugin", ["plugin"]),
    ("api", ["api"]),
    ("ffmpeg", ["ffmpeg", "FFMPEG"]),
    ("cover", ["cover"]),
    ("sparse", ["sparse"]),
    ("import", ["import"]),
    ("export", ["export"]),
]


def code_files() -> list[pathlib.Path]:
    out = []
    for root in SEARCH_ROOTS:
        base = REPO / root
        if not base.exists():
            continue
        for p in base.rglob("*"):
            if not p.is_file() or SKIP.search(p.name):
                continue
            if p.suffix in {".go", ".ts", ".tsx", ".js", ".graphql", ".scss"}:
                out.append(p)
    return out


def main() -> int:
    rows = []
    for line in LEDGER.read_text().splitlines():
        m = re.match(r"^\|\s*(\d+)\s*\|", line)
        if not m:
            continue
        c = [x.strip() for x in line.strip().strip("|").split("|")]
        if len(c) >= 4 and c[-1] == "planned" and "bug report" in c[-2]:
            rows.append((int(m.group(1)), c[1]))

    files = code_files()
    blob = {p: p.read_text(errors="ignore") for p in files}
    print(f"{len(files)} source files indexed; {len(rows)} planned bug reports\n")

    results = {}
    for num, title in rows:
        tl = title.lower()
        hits, misses = [], []
        for kw, frags in KEYWORDS:
            if kw not in tl:
                continue
            n = sum(1 for t in blob.values() if any(f.lower() in t.lower() for f in frags))
            (hits if n else misses).append(f"{kw}({n})")
        results[num] = {"title": title, "hits": hits, "misses": misses}
        flag = "  <-- keyword with NO code" if misses else ""
        print(f"#{num:<6} {title[:52]:<52} {','.join(hits) or '-'}{flag}")
        if misses:
            print(f"        missing: {','.join(misses)}")

    (REPO / "docs" / "planned-candidates.json").write_text(json.dumps(results, indent=2) + "\n")
    print(f"\nwrote docs/planned-candidates.json ({sum(1 for r in results.values() if r['misses'])} rows with an unmatched keyword)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
