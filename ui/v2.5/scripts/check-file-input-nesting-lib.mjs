/**
 * The parser behind check-file-input-nesting.mjs. #7256.
 *
 * Extracted into its own module so the tests can exercise it directly on
 * fixture strings. That extraction is not tidiness: the check script is the only
 * regression guard this fix has, so a mutation that breaks the CHECK rather than
 * the fix would otherwise pass unnoticed. The first version of this check
 * skipped self-closing tags, and <Form.Control type="file" ... /> is
 * self-closing -- it reported OK on the original #7256 source, and nothing
 * noticed until the harness ran it against that source deliberately.
 *
 * KNOWN LIMIT: only the react-bootstrap <Button> component is recognised, not a
 * raw <button> element. A raw <button> with a nested file input has exactly the
 * same problem. It is not matched because <Button> is what this codebase uses
 * and a false positive on unrelated markup would make the check noise nobody
 * runs. test-file-input-nesting.mjs pins that limit as an explicit expected-0
 * case so it is a decision on the record rather than an oversight.
 */

/**
 * Void HTML elements: no closing tag, so they must never be pushed onto the
 * nesting stack.
 */
export const VOID_ELEMENTS = new Set([
  "input",
  "img",
  "br",
  "hr",
  "meta",
  "link",
  "area",
  "base",
  "col",
  "embed",
  "source",
  "track",
  "wbr",
]);

/**
 * Blank out comments while preserving every character position, so line
 * numbers and offsets stay valid.
 *
 * This matters because the fix's own comment describes the bug: the file
 * contains the literal text `<input type="file">` inside a JSX comment, and
 * without this the check would report a violation in the file that fixes it.
 */
export function stripComments(src) {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, " "))
    .replace(/^\s*\/\/.*$/gm, (m) => " ".repeat(m.length));
}

/**
 * Find every <input type="file"> that is nested inside a <Button>.
 *
 * The nesting is tracked by scanning for real JSX open and close tags rather
 * than counting angle brackets: a self-closing <Icon /> contributes an open and
 * a close on the same line, so naive counting nets to zero and never descends.
 * The reported bug also spans nine lines, so a per-line heuristic cannot see
 * it -- it needs real depth.
 *
 * A tag is "open" when it is not self-closing and not a close tag. Closing
 * </...> is matched against the innermost open tag, and a mismatch just means
 * the source is not the flat JSX this handles -- it must never throw, because a
 * check script that crashes on odd input is worse than one that under-reports.
 */
export function findNestedFileInputs(src) {
  const stripped = stripComments(src);
  const found = [];
  const stack = [];

  // <Tag ...>   <Tag ... />   </Tag>   <Tag ...>
  const tagRe = /<(\/?)([A-Za-z][A-Za-z0-9_.]*)((?:[^<>]|"[^"]*"|'[^']*')*?)(\/?)>/g;

  let match;
  while ((match = tagRe.exec(stripped)) !== null) {
    const [, slash, name, attrs, selfClose] = match;
    const line = stripped.slice(0, match.index).split("\n").length;

    if (slash === "/") {
      const idx = stack.map((t) => t.name).lastIndexOf(name);
      if (idx >= 0) {
        stack.length = idx;
      }
      continue;
    }

    // A file input is checked for nesting BEFORE both guards below, and the
    // order matters twice over.
    //
    // <Form.Control type="file" ... /> is SELF-CLOSING -- there is no separate
    // close tag -- so a `selfClose` guard placed first would skip it and the
    // check would never look at the one element it exists to find. That bug was
    // in the first version of this script: it reported clean on the original
    // #7256 source.
    //
    // <input> is also a VOID element, so it must not be pushed onto the stack
    // afterwards -- doing so would make the stack permanently deeper than the
    // source really is.
    if (/type=["']file["']/.test(attrs)) {
      const buttonIdx = stack
        .map((t) => t.name)
        .lastIndexOf("Button");
      if (buttonIdx >= 0) {
        found.push({ line, name, buttonLine: stack[buttonIdx].line });
      }
    }

    if (selfClose === "/") {
      continue;
    }

    if (VOID_ELEMENTS.has(name)) {
      continue;
    }

    // NOTE ON THE TWO GUARDS ABOVE. Both were mutation-tested and neither can
    // be killed: with either removed, the STACK contents differ but the
    // FINDINGS never do, because lastIndexOf searches the whole stack and a
    // stray entry above a <Button> only matters if a later close tag pops it
    // past the Button first. Traced exhaustively over five shapes -- stray
    // self-close inside a Button, stray outside one, void <input> inside a
    // Button, void <input> with no Button at all, and an unbalanced
    // self-closing tag before a mismatched close.
    //
    // They are kept because they are cheap and they keep the stack honest for
    // anyone who later changes lastIndexOf to an "immediately enclosing" check
    // -- which WOULD make them load-bearing. That change is not made here
    // precisely because it is not needed for correctness, only for a stricter
    // reading of "inside". See the two unkillable survivors in
    // mutate_file_input_nesting.py, which record the same limit from the
    // harness side.
    stack.push({ name, line, attrs });
  }

  return found;
}
