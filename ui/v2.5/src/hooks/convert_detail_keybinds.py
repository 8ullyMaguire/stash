#!/usr/bin/env python3
"""Convert the seven DETAIL components' Mousetrap.bind effects to useScopedKeybinds.

#2833's second half. The eight LIST components were converted by hand because they share
one identical two-key shape. These seven are the opposite: `Scene.tsx` binds fifteen keys
across two effects, and lists fifteen unbinds. Rewriting that by hand is a 60-line diff
per file that cannot be reviewed by eye, and a single missed unbind is the ORIGINAL bug --
`Mousetrap.unbind` installs a no-op over the previous owner rather than restoring it.

So the conversion is mechanical, and therefore auditable in a different way: this script
REFUSES to convert a file whose effect does not match the expected shape, and it prints
every conversion for review. A file it skips is a file whose effect has been reworked
since, which needs a human rather than a regex.

WHAT IS PRESERVED, and this is the whole risk:

  * The KEY SET, exactly. Same keys, same order.
  * The CALLBACK BODIES, verbatim, moved into the map literal.
  * The DEPENDENCY ARRAY where one exists.
  * EVERY effect in the file that binds keys, not just the first -- `Scene.tsx` also binds
    `.` in a second effect and `Performer.tsx` binds `c g m` in another, and converting
    only the first would leave `e` unscoped in `Performer.tsx`, the exact key #2833 is
    about, while reporting success.

WHAT CHANGES, and only this: `Mousetrap.bind(k, cb)` becomes a map entry, and the
cleanup's `Mousetrap.unbind(k)` list disappears because the hook returns one unbind for
the whole map.

Run from ui/v2.5:  python3 src/hooks/convert_detail_keybinds.py [--apply]
"""

import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1] / "components"

TARGETS = [
    "Tags/TagDetails/Tag.tsx",
    "Scenes/SceneDetails/Scene.tsx",
    "Performers/PerformerDetails/Performer.tsx",
    "Images/ImageDetails/Image.tsx",
    "Groups/GroupDetails/Group.tsx",
    "Galleries/GalleryDetails/Gallery.tsx",
    "Studios/StudioDetails/Studio.tsx",
]

# ## WHY THIS IS A BRACE MATCHER AND NOT A REGEX
#
# The first version was `Mousetrap\.bind\(\s*"key",\s*(?P<cb>.*?)\s*\);` with a LAZY
# `.*?`, and it silently truncated every multi-line callback at the first `);` inside it:
#
#     Mousetrap.bind("d d", () => {
#       setIsDeleteAlertOpen(true);
#     });
#
# matched as cb = `() => { setIsDeleteAlertOpen(true)` -- the closing brace of the arrow
# body and the closing paren of the call were both dropped. The emitted entry was then
# `"d d": (() => { setIsDeleteAlertOpen(true),` and the file did not parse: tsc went from
# 12 pre-existing errors to 68, and the failures pointed at the line AFTER the broken
# entry rather than at it.
#
# It was caught by tsc, not by the tests -- 15 tests were green over the broken output,
# because they assert on imports and bind/unbind pairs, not on whether the file parses.
# That is worth saying plainly: a test suite that reads as coverage and cannot see
# unparseable source is not coverage of the conversion.
def find_binds(body):
    r"""[(key, callback_text)] for each Mousetrap.bind call in `body`.

    Parens are counted FROM THE `bind(` OPENING PAREN, which is the only offset that
    works for every shape:

      Mousetrap.bind("a", () => f());              single-line
      Mousetrap.bind("d d", () => { g(); });       multi-line, with `);` inside it

    Two earlier versions got this wrong in ways worth recording, because both produced
    output that LOOKED converted:

      - a regex with a lazy `(?P<cb>.*?)` ending at `\);` truncated every multi-line
        callback at the first `);` inside it, dropping the arrow body's closing brace.
        tsc went from 12 pre-existing errors to 68, and pointed at the line AFTER the
        break rather than at it.
      - starting the paren counter at `m.end()` of a regex that had already consumed
        `bind(`'s paren returned the callback as the single character `(`, so every map
        entry came out `"a": ((),` -- again converted-shaped, again unparseable.

    Caught by tsc both times, and NOT by the 15 tests, which assert on imports and
    bind/unbind pairs rather than on whether the file parses. A suite that reads as
    coverage and cannot see unparseable source is not covering the conversion.
    """
    out = []
    for m in re.finditer(r"Mousetrap\.bind\s*\(", body):
        open_at = m.end() - 1  # index of the `bind(`'s own opening paren
        key_m = re.match(r'\s*"([^"]+)"\s*,', body[open_at + 1 :])
        if not key_m:
            raise ValueError("bind with a non-literal key at offset %d" % open_at)
        key = key_m.group(1)

        cb_start = open_at + 1 + key_m.end()
        depth = 1  # the `bind(` paren is open
        i = cb_start
        while i < len(body):
            c = body[i]
            if c == "(":
                depth += 1
            elif c == ")":
                depth -= 1
                if depth == 0:
                    break
            i += 1
        else:
            raise ValueError("unbalanced bind for %r" % key)

        cb = body[cb_start:i].strip()
        if cb.endswith(";"):
            cb = cb[:-1].rstrip()
        out.append((key, cb))
    return out


UNBIND = re.compile(r'\s*Mousetrap\.unbind\("(?P<key>[^"]+)"\);\n')


def find_effects(src):
    """Every Mousetrap.bind useEffect in `src`.

    Returns a list of (start, end, body, deps, had_deps_array).

    WHY BRACES AND NOT A REGEX: the first version matched the effect body with a regex
    ending at `});`, and every one of these effects contains a `});` INSIDE a callback --
    `Mousetrap.bind("d d", () => { setIsDeleteAlertOpen(true); });` -- so the body was
    truncated after two keys, the cleanup parsed as empty, and the script reported
    "leaking: ['d d', 'e']" about three files whose cleanups plainly unbind both. A false
    leak report is worse than none: it sends someone to fix a bug that does not exist.
    """
    found = []
    for m in re.finditer(r"[ ]*useEffect\(\(\) => \{", src):
        open_brace = m.end() - 1
        depth = 0
        i = open_brace
        end_brace = None
        while i < len(src):
            if src[i] == "{":
                depth += 1
            elif src[i] == "}":
                depth -= 1
                if depth == 0:
                    end_brace = i
                    break
            i += 1
        if end_brace is None:
            continue

        body = src[open_brace + 1 : end_brace]
        if "Mousetrap.bind" not in body:
            continue

        tail = src[end_brace + 1 : end_brace + 40]
        dm = re.match(r",\s*(?P<deps>\[[^\]]*\])\s*\);", tail)
        if dm:
            found.append((m.start(), end_brace + 1 + dm.end(), body, dm.group("deps"), True))
            continue
        m2 = re.match(r"\s*\);", tail)
        if m2:
            found.append((m.start(), end_brace + 1 + m2.end(), body, "[]", False))

    return found


def build_effect(out, start, body, deps, had_deps):
    """Validate one effect. Returns (replacement, report) or raises ValueError."""
    binds = find_binds(body)
    if not binds:
        raise ValueError("no parseable binds")

    keys = [k for k, _ in binds]
    if len(set(keys)) != len(keys):
        raise ValueError("duplicate keys in one effect: %s" % keys)

    # The cleanup must unbind every key that was bound. A key bound and never unbound is
    # a handler that outlives its component -- and converting it would hide that behind
    # the hook's single unbind.
    cleanup = body[body.rfind("return () => {") :] if "return () => {" in body else ""
    unbound = UNBIND.findall(cleanup)

    missing = sorted(set(keys) - set(unbound))
    if missing:
        raise ValueError(
            "cleanup does not unbind every bound key.\n"
            "      bound:   %s\n"
            "      unbound: %s\n"
            "      leaking: %s" % (sorted(keys), sorted(unbound), missing)
        )

    # An EXTRA unbind is allowed only when conditional. `Tag.tsx` unbinds "s s" under
    # `if (isEditing)`, and "s s" belongs to the rating keybinds. An extra UNCONDITIONAL
    # unbind for a key this effect never bound is a key being taken from whoever owns it,
    # which is the very defect being fixed, so it is refused rather than preserved.
    extra = sorted(set(unbound) - set(keys))
    if extra and not re.search(r"if\s*\([^)]*\)\s*\{[^}]*unbind", cleanup, re.S):
        raise ValueError(
            "cleanup UNCONDITIONALLY unbinds key(s) this effect never bound: %s. "
            "That takes a key from whoever owns it -- the defect this fixes." % extra
        )

    indent = re.match(r"[ ]*", out[start:]).group(0)
    inner = indent + "  "

    entries = []
    for key, cb in binds:
        cb_text = ("\n" + inner + "  ").join(cb.strip().split("\n"))
        entries.append('%s// %s\n%s"%s": (%s),' % (inner, key, inner, key, cb_text))

    deps_arg = "" if deps == "[]" else ", " + deps
    dep_note = (
        "deps %s" % deps if had_deps else "NO dep array -> now keyed on the key set"
    )

    head = (
        "%(i)s// #2833: scoped, so this page takes a contended key from the list behind it\n"
        "%(i)s// while mounted, and the list gets it back on the way out. Unscoped,\n"
        "%(i)s// `Mousetrap.unbind` installs a no-op over the previous owner rather than\n"
        "%(i)s// restoring it, so the list silently loses the key -- the reported\n"
        "%(i)s// \"works every other time\".\n"
        "%(i)suseScopedKeybinds(\n"
        "%(i)s  {\n"
    ) % {"i": indent}

    tail_text = (
        "\n%(i)s  }%(deps)s\n%(i)s);" % {"i": indent, "deps": deps_arg}
    )

    replacement = head + "\n".join(entries) + tail_text
    return replacement, "%d keys %s (%s)" % (len(binds), keys, dep_note)


def convert(src):
    """Returns (new_src, report) or raises ValueError with the reason."""
    effects = find_effects(src)
    if not effects:
        raise ValueError("no Mousetrap.bind useEffect matched the expected shape")

    plans = [e for e in effects if find_binds(e[2])]
    if not plans:
        raise ValueError("effects matched but none contained parseable binds")

    # Validate ALL before writing ANY: a file is converted whole or not at all, because a
    # half-converted detail page leaves `e` unscoped -- the key #2833 is about -- while the
    # report reads as a success.
    built = []
    for start, end, body, deps, had_deps in plans:
        replacement, report = build_effect(src, start, body, deps, had_deps)
        built.append((start, end, replacement, report))

    out = src
    # Right to left, so each splice leaves the earlier offsets valid.
    for start, end, replacement, _ in sorted(built, key=lambda t: t[0], reverse=True):
        out = out[:start] + replacement + out[end:]

    if "hooks/mousetrapScope" not in out:
        # TWO import forms are in the tree: six files use
        # `import Mousetrap from "mousetrap"` and `Image.tsx` uses
        # `import * as Mousetrap from "mousetrap"`. Anchoring on the first form only left
        # `Image.tsx` calling a hook it had not imported -- and tsc caught it as
        # TS2304 rather than anything noticing at build or run time.
        out, n = re.subn(
            r'(import (?:\* as )?Mousetrap from "mousetrap";\n)',
            r'\1import { useScopedKeybinds } from "src/hooks/mousetrapScope";\n',
            out,
            count=1,
        )
        if n != 1:
            raise ValueError(
                "no `import Mousetrap from \"mousetrap\"` line found, so the hook "
                "import could not be added -- refusing to write a file that references "
                "an unimported symbol"
            )

    leftover = len(re.findall(r'Mousetrap\.bind\("', out))
    return out, "%s | effects: %d | leftover binds in file: %d" % (
        "; ".join(r for _, _, _, r in built),
        len(built),
        leftover,
    )


def main():
    apply = "--apply" in sys.argv
    skipped = []

    for rel in TARGETS:
        path = ROOT / rel
        src = path.read_text()
        try:
            out, report = convert(src)
        except ValueError as why:
            skipped.append((rel, str(why)))
            print("  SKIP  %s: %s" % (rel, why))
            continue

        print("  %s  %s: %s" % ("converted" if apply else "would convert", rel, report))
        if apply:
            path.write_text(out)

    print()
    if skipped:
        print("%d file(s) NOT converted -- each needs a human:" % len(skipped))
        for rel, why in skipped:
            print("  %s\n    %s" % (rel, why))
        return 1
    print("all detail components converted" if apply else "dry run; pass --apply to write")
    return 0


if __name__ == "__main__":
    sys.exit(main())