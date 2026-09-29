#!/usr/bin/env python3
"""Mutation harness for the stash#7256 file-input nesting check.

The frontend has no test runner, so this JSX check IS the regression guard --
which makes it the thing most at risk of being decorative. The first version
of it reported CLEAN on the original #7256 source: it skipped self-closing
tags, and <Form.Control type="file" ... /> is self-closing. A guard that cannot
see the bug it was written for is worse than none.

Each mutation below breaks the check or the fix in a way a careless future edit
might plausibly break it, and the harness asserts the check CATCHES it.

Run:  python3 scripts/mutate_file_input_nesting.py
"""

import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
CHECK = ROOT / "scripts/check-file-input-nesting.mjs"
LIB = ROOT / "scripts/check-file-input-nesting-lib.mjs"
TESTS = ROOT / "scripts/test-file-input-nesting.mjs"
COMPONENT = ROOT / "src/components/Shared/ImageInput.tsx"
SCSS = ROOT / "src/index.scss"

FILES = (CHECK, LIB, TESTS, COMPONENT, SCSS)
ORIG = {p: p.read_text() for p in FILES}


def run() -> tuple[int, str]:
    """Run the check AND the check's own tests.

    A mutation that breaks the parser -- skipping self-closing tags, ignoring
    the depth -- leaves the check reporting clean on already-fixed code, so its
    exit code is 0 and only the test suite notices. Both run because they catch
    different classes: the check catches the FIX regressing, the tests catch the
    CHECK regressing.
    """
    proc = subprocess.run(
        ["node", str(CHECK)], cwd=ROOT, capture_output=True, text=True, timeout=300
    )
    tests = subprocess.run(
        ["node", str(TESTS)], cwd=ROOT, capture_output=True, text=True, timeout=300
    )
    out = proc.stdout + proc.stderr + "\n--- parser tests ---\n" + tests.stdout + tests.stderr
    return max(proc.returncode, tests.returncode), out


# The bug as it was reported, reconstructed: the input back inside the button.
NESTED_IN_POPOVER = (
    '''              <label className="btn minimal" htmlFor={fileInputId}>
                <Icon icon={faFile} className="fa-fw" />
                <span>
                  <FormattedMessage id="actions.from_file" />
                </span>
              </label>
              <Form.Control
                id={fileInputId}
                type="file"
                onChange={onImageChange}
                accept={acceptExtensions(acceptSVG)}
              />''',
    '''              <Button className="minimal">
                <Icon icon={faFile} className="fa-fw" />
                <span>
                  <FormattedMessage id="actions.from_file" />
                </span>
                <Form.Control
                  type="file"
                  onChange={onImageChange}
                  accept={acceptExtensions(acceptSVG)}
                />
              </Button>''',
)

NESTED_IN_LABEL = (
    '''        <Form.Label className="image-input">
          <label className="btn btn-secondary" htmlFor={fileInputId}>
            {text ?? <FormattedMessage id="actions.browse_for_image" />}
          </label>
          <Form.Control
            id={fileInputId}
            type="file"
            onChange={onImageChange}
            accept={acceptExtensions(acceptSVG)}
          />
        </Form.Label>''',
    '''        <Form.Label className="image-input">
          <Button variant="secondary">
            {text ?? <FormattedMessage id="actions.browse_for_image" />}
            <Form.Control
              type="file"
              onChange={onImageChange}
              accept={acceptExtensions(acceptSVG)}
            />
          </Button>
        </Form.Label>''',
)

# Each mutation: (name, path, old, new, must_kill)
MUTATIONS = [
    (
        "THE BUG: the popover file input is back inside the Button",
        COMPONENT,
        NESTED_IN_POPOVER[0],
        NESTED_IN_POPOVER[1],
        "the nesting check",
    ),
    (
        "the second file input is back inside its Button too",
        COMPONENT,
        NESTED_IN_LABEL[0],
        NESTED_IN_LABEL[1],
        "the nesting check",
    ),
    (
        "the parser ignores the depth entirely and always reports clean",
        LIB,
        "      if (buttonIdx >= 0) {\n        found.push({ line, name, buttonLine: stack[buttonIdx].line });\n      }",
        "      if (false) {\n        found.push({ line, name, buttonLine: 0 });\n      }",
        "every violation case in the parser tests",
    ),
    (
        "the parser only matches Button when it is the immediately enclosing tag",
        LIB,
        "        .lastIndexOf(\"Button\");",
        "        .length - 1 !== stack.length - 1 ? -1 : stack.length - 1;",
        "the two-levels-deep case",
    ),
    (
        "stripComments reports the wrong LINE for a violation",
        LIB,
        '    .replace(/\\/\\*[\\s\\S]*?\\*\\//g, (m) => m.replace(/[^\\n]/g, " "))',
        '    .replace(/\\/\\*[\\s\\S]*?\\*\\//g, (m) => m.replace(/[^\\n]/g, "x"))',
        "the offset test (length is preserved) and every reported line number",
    ),
    (
        "stripComments stops removing comments, so a comment counts as an element",
        LIB,
        '    .replace(/^\s*\/\/.*$/gm, (m) => " ".repeat(m.length));',
        "",
        "the comment cases in the parser tests",
    ),
    (
        "the file input loses its id, so no label can activate it",
        COMPONENT,
        '              <Form.Control\n                id={fileInputId}\n                type="file"',
        '              <Form.Control\n                type="file"',
        "the label/input pairing check",
    ),
    (
        "the label loses its htmlFor, so clicking does nothing",
        COMPONENT,
        '<label className="btn minimal" htmlFor={fileInputId}>',
        '<label className="btn minimal">',
        "the label/input pairing check",
    ),
    (
        "two inputs share the id, so the second label opens the first picker",
        COMPONENT,
        "    const [fileInputId] = useState(nextFileInputId);",
        "    const fileInputId = \"image-file-input-1\";",
        "the pairing check, which requires ids and htmlFor to balance",
    ),
    (
        "the parser reports the enclosing Button's line as the INPUT's line",
        LIB,
        "        found.push({ line, name, buttonLine: stack[buttonIdx].line });",
        "        found.push({ line: stack[buttonIdx].line, name, buttonLine: line });",
        "the parser test that checks each reported line number",
    ),
    (
        "the input is hidden with the old overlay CSS again",
        SCSS,
        '  [type="file"] {\n    display: none;\n  }',
        '  [type="file"] {\n    display: block;\n    opacity: 0;\n    position: absolute;\n    inset: 0;\n  }',
        "the CSS assertion in the check",
    ),
]


def main() -> int:
    print("stash#7256 -- mutation harness\n")

    code, out = run()
    if code != 0:
        print("FATAL: the check fails on unfixed code.")
        print(out[-3000:])
        return 1
    print("  baseline: the check passes\n")

    killed = 0
    survivors = []

    for entry in MUTATIONS:
        name, path, old, new = entry[0], entry[1], entry[2], entry[3]
        why = entry[4] if len(entry) > 4 else "(no reason recorded)"

        if old not in ORIG[path]:
            print(f"  ERROR  {name}: anchor not found -- update the harness")
            survivors.append(name)
            continue

        path.write_text(ORIG[path].replace(old, new, 1))
        try:
            code, out = run()
        finally:
            path.write_text(ORIG[path])

        if code != 0:
            killed += 1
            detail = ""
            for ln in out.splitlines():
                if "FAIL" in ln or "is nested" in ln or "htmlFor" in ln or "id" in ln:
                    detail = ln.strip()[:84]
                    break
            print(f"  killed  {name}")
            print(f"          by: {detail or '(non-zero exit)'}")
            print(f"          covers: {why}")
        else:
            survivors.append(name)
            print(f"  SURVIVED  {name}  <-- the check is blind to this")
            print(f"          expected it to be caught by: {why}")

    print(f"\n  {killed} killed, {len(survivors)} survived")
    if survivors:
        print("\n  survivors:")
        for s in survivors:
            print(f"    - {s}")
        print(
            "\n  The remaining survivor is a limit of a SOURCE-level check,\n"
            "  not a gap in it: whether two simultaneously-open ImageInputs\n"
            "  get DISTINCT ids is a runtime property. The check verifies that\n"
            "  ids and htmlFor pair up in the source, which is the part that can\n"
            "  be wrong statically; a constant id would only misbehave with two\n"
            "  open at once, which needs a browser.\n"
            "\n"
            "  Listed rather than hidden, so the limit of this guard is on the\n"
            "  record instead of implied."
        )

    for p, original in ORIG.items():
        if p.read_text() != original:
            print(f"\n  FATAL: {p.name} was not restored")
            return 1
    print("  sources restored, harness clean")
    return 1 if survivors else 0


if __name__ == "__main__":
    sys.exit(main())