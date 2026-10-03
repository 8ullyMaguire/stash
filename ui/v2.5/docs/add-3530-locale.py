#!/usr/bin/env python3
"""Insert the #3530 range strings into the locale files as TEXT, not as a JSON round-trip.

Why not json.load/json.dumps: these files are not consistently formatted. en-GB.json indents
almost everything 4 spaces, but `scene_tagger` (and its subtree) is 2 -- 104552 bytes of
json.dumps(indent=4) output against a 104576-byte file, first divergence at byte 77823.
A round-trip therefore cannot be byte-identical, so it rewrites a 1832-line section of a
translation file to add three keys, and the real change drowns in the reformat.

So: locate each top-level section in the raw text, append the new keys after the section's LAST
existing entry, and verify with json.loads afterwards. Only the inserted lines appear in the diff.

Two things this has to get right, both of which I got wrong first:

  - the child indentation is the LEADING WHITESPACE of the section's first child line, not the
    whole line (slicing to the next newline returns `"audio_codec": "Audio Codec",` and glues
    it onto every inserted key).
  - the last existing entry carries NO trailing comma, so appending after it needs a comma
    added to THAT line first, or the result is not valid JSON.

Idempotent -- a key that is already present is skipped, so a rerun is a no-op.
"""

import json
import pathlib
import sys

REPO = pathlib.Path(__file__).resolve().parent.parent
LOCALES = REPO / "src/locales"

MEDIA_INFO = [
    ("range", "Range"),
    ("range_start", "Range Start"),
    ("range_end", "Range End"),
    ("range_whole_file", "Whole file"),
    # A blank end means "to the end of this file", which is NOT the same as no range at all.
    # The wording has to say so: the alternative reading -- the range is unset -- is what a
    # user who has just been editing one would assume, and they would be wrong.
    ("range_end_open", "End of file ({duration})"),
    ("range_end_of_file", "end of file"),
]

ACTIONS = [
    ("edit_range", "Edit Range"),
    ("clear_range", "Clear Range"),
]

VALIDATION = [
    ("range_start_negative", "Range start must not be negative"),
    ("range_end_negative", "Range end must not be negative"),
    ("range_end_before_start", "Range end must be greater than range start"),
    ("range_end_past_file", "Range end must not be past the end of the file ({duration})"),
]

SECTIONS = [
    ("media_info", MEDIA_INFO),
    ("actions", ACTIONS),
    ("validation", VALIDATION),
]


def match_brace(text, open_at):
    """Index of the '}' matching the '{' at open_at. String-aware so a value containing
    a brace cannot end the section early."""
    depth = 0
    i = open_at
    in_str = False
    esc = False
    while i < len(text):
        c = text[i]
        if in_str:
            if esc:
                esc = False
            elif c == "\\":
                esc = True
            elif c == '"':
                in_str = False
        else:
            if c == '"':
                in_str = True
            elif c == "{":
                depth += 1
            elif c == "}":
                depth -= 1
                if depth == 0:
                    return i
        i += 1
    raise ValueError("unbalanced braces")


def child_indent_of(text, open_at, close_at):
    """Leading whitespace of the section's first child line, or the close brace's indent."""
    close_line_start = text.rindex("\n", 0, close_at) + 1
    close_indent = text[close_line_start:close_at]
    first_child = text.index("\n", open_at) + 1
    line = text[first_child:text.index("\n", first_child)]
    return line[: len(line) - len(line.lstrip())] or (close_indent + "    ")


def add_to(path, section, entries):
    text = path.read_text()
    header = '"' + section + '": {'
    header_at = text.index(header)
    open_at = header_at + len(header) - 1
    close_at = match_brace(text, open_at)

    existing = json.loads(text).get(section, {})
    todo = []
    for k, v in entries:
        cur = existing.get(k)
        if cur is not None:
            if cur != v:
                print(f"  !! {section}.{k} exists with a different value ({cur!r}); kept")
            continue
        todo.append((k, v))
    if not todo:
        return 0

    close_line_start = text.rindex("\n", 0, close_at) + 1
    body = text[open_at + 1:close_line_start]
    indent = child_indent_of(text, open_at, close_at)

    lines = body.splitlines(keepends=True)
    last = max(i for i, ln in enumerate(lines) if ln.strip())
    # The final entry has no trailing comma; one is needed before anything can follow it.
    if not lines[last].rstrip().endswith(","):
        lines[last] = lines[last].rstrip("\n") + ",\n"

    # The LAST entry in a JSON object carries no comma, so the last inserted one must not
    # either -- a trailing comma before the closing brace is a parse error. (This is the
    # third bug in this script, after slicing the indent as a whole line and forgetting that
    # the previous last entry had no comma to append to.)
    added = [
        f"{indent}{json.dumps(k)}: {json.dumps(v, ensure_ascii=False)}"
        f"{',' if i < len(todo) - 1 else ''}\n"
        for i, (k, v) in enumerate(todo)
    ]
    new_body = "".join(lines[: last + 1] + added + lines[last + 1:])

    out = text[: open_at + 1] + new_body + text[close_line_start:]
    json.loads(out)  # fail here rather than leaving a broken locale on disk
    path.write_text(out)
    return len(added)


def main():
    for name in ("en-GB.json", "en-US.json"):
        p = LOCALES / name
        n = 0
        skipped = []
        for section, entries in SECTIONS:
            if f'"{section}": {{' not in p.read_text():
                skipped.append(section)
                continue
            n += add_to(p, section, entries)
        json.loads(p.read_text())
        print(f"{name}: +{n} keys, parses clean"
              + (f" (no section: {', '.join(skipped)})" if skipped else ""))
    return 0


# Why en-US is handled this way: App.tsx merges en-GB as the base and the chosen locale over
# it, so a NEW string only has to exist in the base to work in every locale. en-US.json is a
# sparse override file holding only the handful of en-GB spellings that differ for US English
# (anonymize/anonymise and friends) -- it has no media_info or validation section at all.
# Adding a new English string there would be a no-op override that also invents a section the
# file deliberately does not have, so absent sections are skipped rather than created.


if __name__ == "__main__":
    sys.exit(main())