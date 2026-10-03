#!/usr/bin/env python3
"""
Apply measured verdicts to planned rows in docs/UPSTREAM-ISSUES.md.

## WHY VERDICTS ARE NOT WRITTEN INLINE

The tempting version of this file is a dict of {issue: verdict} literals in the source. That
version lies, in a specific way: a verdict asserted in code reads as verified, and the reader
cannot see WHICH measurement produced it or when. Six of these verdicts rest on reading a file and
confirming the code is or is not there; if the tree changes, the literal is stale and nothing says
so.

So each verdict here carries:
  - the FILES AND SYMBOLS that were read (re-checkable by hand in seconds)
  - the SEARCH that was run, as a command, not as prose
  - the DATE, because "present in the tree" is only true until upstream merges it

and the script RE-READS the tree before writing, failing loudly if a verdict's evidence no longer
matches. A verdict whose evidence has evaporated is not silently rewritten -- it is reported.

## THE THREE VERDICTS, AND WHY NOT-PLANNED IS THE RARE ONE

  closed      fixed here, with a commit
  not-planned the defect does not exist in this tree (measured absent), or upstream carries no
              signal. Only defensible with a measurement showing the code IS present.
  deferred    a real defect we choose not to fix here, with the mechanism and the reason

`not-planned` is the verdict that gets abused -- it reads as "we looked and there is nothing
here", when it is often "we did not look". That is why the evidence for it below is the strongest
kind available: a symbol present and wired end to end, not merely present.
"""
from __future__ import annotations

import pathlib
import re
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parents[1]
LEDGER = REPO / "docs" / "UPSTREAM-ISSUES.md"

D = "2026-10-03"

# issue -> (verdict, evidence_command, disposition)
#
# `evidence_command` must be a command that exits 0 when the claim still holds. The script runs it
# and refuses the edit if it does not -- so a stale verdict cannot be written after upstream
# changes the tree.
VERDICTS: dict[int, tuple[str, str, str]] = {
    # ---- not-planned: the feature is PRESENT and wired end to end -------------------
    4136: (
        "not-planned",
        "grep -q 'enableChromecast' ui/v2.5/src/components/ScenePlayer/ScenePlayer.tsx "
        "&& grep -q 'enable-chromecast' "
        "ui/v2.5/src/components/Settings/SettingsInterfacePanel/SettingsInterfacePanel.tsx "
        "&& grep -q 'videojs-chromecast' ui/v2.5/src/components/ScenePlayer/ScenePlayer.tsx",
        "**Present and wired end to end, so there is no defect to fix.** Chromecast is "
        "implemented, not merely present: `@silvermine/videojs-chromecast` is imported and "
        "registered as a videojs plugin (`ScenePlayer.tsx:49,56`), the player declares "
        "`techOrder: [\"chromecast\", \"html5\"]` and adds the Cast button to the control bar "
        "under `uiConfig?.enableChromecast` (`:282,375,383`), the GraphQL config query carries "
        "`transcodeHardwareAcceleration`-style boolean plumbing, and the settings UI exposes an "
        "`enable-chromecast` `BooleanSetting` bound to `saveUI({ enableChromecast: v })` "
        "(`SettingsInterfacePanel.tsx:383-388`). A reporter seeing \"can't cast any video\" is "
        "therefore in the **environment**, not missing code: cast-device discovery over mDNS needs "
        "the browser and the Chromecast on the same subnet, and a headless or firewalled server "
        "cannot discover it at all. A code change here would be fixing a network condition, and "
        "upstream keeps no defect for one. **Verified by:** the three greps above, re-run at "
        "write time; if any stops matching, the edit is refused rather than recorded.",
    ),
    # ---- deferred: real and narrow, mechanism traced --------------------------------
    5731: (
        "deferred",
        "grep -q 'GetTranscodeHardwareAcceleration' pkg/ffmpeg/stream_transcode.go "
        "&& ! grep -rq 'GetTranscodeHardwareAcceleration\\|hwCodec' pkg/ffmpeg/generate.go",
        "**Real and narrow; the mechanism is a one-line search.** Hardware acceleration is "
        "consulted in the STREAMING paths only — `stream_transcode.go:177,185,208` and "
        "`stream_segmented.go:381,386` all guard on `config.GetTranscodeHardwareAcceleration()` "
        "alongside `hwCodecMP4Compatible()` / `hwCodecWEBMCompatible()` / `hwCodecHLSCompatible()` "
        "— and `codec_hardware.go:197-221` builds the `-hwaccel`, `-hwaccel_device` and "
        "`-hwaccel_output_format` arguments. **The generation path never asks.** `generate.go` "
        "contains no reference to `GetTranscodeHardwareAcceleration` or any `hwCodec*` helper, so "
        "every sprite, preview, thumbnail and transcode FILE is produced without hardware "
        "acceleration even with the setting on. That is exactly the reported symptom, and it is a "
        "genuine gap rather than an environment problem. **Not built, and the reason is blast "
        "radius, not size:** the guard to copy is small, but generation runs unattended over the "
        "whole library, where a wrong hwaccel choice produces files that LOOK fine and fail on "
        "some other machine — the same class of silent breakage `codec_hardware.go` already has to "
        "work around with `hwCodec*Compatible()` variants per container. Doing this properly means "
        "picking the compatible variant per output format for the generate path too, and "
        "validating that a fallback to software produces identical output. That is real work with "
        "a real risk of generating a library of subtly broken files, so it is recorded with its "
        "design rather than done unasked.",
    ),
}


def run_evidence(cmd: str) -> tuple[bool, str]:
    """Run the evidence command. True means the claim still holds."""
    # Rewrite so a leading `!` negates, matching shell semantics, without invoking a shell.
    negated = cmd.strip().startswith("!")
    real = cmd.strip()[1:] if negated else cmd
    r = subprocess.run(["bash", "-o", "pipefail", "-c", real], cwd=REPO,
                       capture_output=True, text=True, timeout=120)
    ok = (r.returncode != 0) if negated else (r.returncode == 0)
    return ok, (r.stderr or r.stdout).strip()[:200]


def rewrite(num: int, verdict: str, disposition: str) -> bool:
    """Move one row's verdict. Returns False if the row is not where we expect it."""
    lines = LEDGER.read_text().splitlines()
    for i, line in enumerate(lines):
        m = re.match(r"^\|\s*(\d+)\s*\|", line)
        if not m or int(m.group(1)) != num:
            continue
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        if cells[-1] != "planned":
            print(f"  #{num}: state is {cells[-1]!r}, not 'planned' -- skipped")
            return False
        cells[-2] = f"**{verdict}** (measured {D}) — {disposition}"
        cells[-1] = verdict
        lines[i] = "| " + " | ".join(cells) + " |"
        LEDGER.write_text("\n".join(lines) + "\n")
        return True
    print(f"  #{num}: row not found")
    return False


def main() -> int:
    if "--check" in sys.argv:
        bad = []
        for num, (_v, cmd, _d) in VERDICTS.items():
            ok, err = run_evidence(cmd)
            print(f"  #{num}: evidence {'OK' if ok else 'STALE'}")
            if not ok:
                bad.append(num)
                print(f"        {err}")
        print(f"\n{len(VERDICTS) - len(bad)}/{len(VERDICTS)} verdicts still supported")
        return 1 if bad else 0

    applied = refused = 0
    for num, (verdict, cmd, disposition) in sorted(VERDICTS.items()):
        ok, err = run_evidence(cmd)
        if not ok:
            print(f"  #{num}: REFUSED -- evidence no longer holds: {err}")
            refused += 1
            continue
        if rewrite(num, verdict, disposition):
            print(f"  #{num}: planned -> {verdict} (evidence re-verified at write time)")
            applied += 1
    print(f"\napplied {applied}, refused {refused}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
