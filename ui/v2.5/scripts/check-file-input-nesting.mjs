#!/usr/bin/env node
/**
 * Check for #7256: a file input nested inside a button.
 *
 * An <input type="file"> inside a <button> is invalid HTML -- a button's
 * permitted content is phrasing content, and an input is interactive. Firefox
 * refuses to activate the nested input, so "From file" silently does nothing
 * while "From URL" (a plain button with an onClick) works fine. That contrast
 * is the whole of the bug report.
 *
 * This is a source check rather than a rendering test because the frontend has
 * no test runner, and because the property is structural: it is decidable from
 * the JSX text without a DOM. What it cannot prove is that clicking the control
 * opens a picker -- that needs a browser. The check, the CSS and the id
 * pairing are all it can verify, and the CSS/id parts are verified by the
 * assertions at the bottom of this file.
 *
 * The check script has its own tests: node scripts/test-file-input-nesting.mjs.
 * Run both before trusting a change here.
 *
 * Run:  node scripts/check-file-input-nesting.mjs
 */

import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, extname } from "node:path";
import { findNestedFileInputs, stripComments } from "./check-file-input-nesting-lib.mjs";

const SRC = new URL("../src/", import.meta.url).pathname;

/** Every .tsx under src, so a new file with the pattern cannot hide. */
function tsxFiles(dir) {
  const out = [];
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) {
      out.push(...tsxFiles(full));
    } else if ([".tsx", ".jsx"].includes(extname(full))) {
      out.push(full);
    }
  }
  return out;
}

const failures = [];
let checked = 0;
let inputsSeen = 0;

for (const file of tsxFiles(SRC)) {
  checked += 1;
  const src = readFileSync(file, "utf8");
  inputsSeen += (stripComments(src).match(/type=["']file["']/g) ?? []).length;

  for (const hit of findNestedFileInputs(src)) {
    failures.push({
      file: file.replace(SRC, "src/"),
      line: hit.line,
      text: `<${hit.name} type="file"> is nested inside a <Button>`,
      inside: `line ${hit.buttonLine}`,
    });
  }
}

// The complementary check: a file input inside a <Form.Label>/<label> must be
// a SIBLING of that label and carry an id, and some label must reference it.
// Without the pairing, the visible control does nothing -- the same user-visible
// symptom as #7256, arrived at by a different route.
const IMAGE_INPUT = join(SRC, "components/Shared/ImageInput.tsx");
if (statSync(IMAGE_INPUT).isFile()) {
  const src = readFileSync(IMAGE_INPUT, "utf8");
  const ids = [...src.matchAll(/id=\{fileInputId\}/g)].length;
  const fors = [...src.matchAll(/htmlFor=\{fileInputId\}/g)].length;
  const declared = /const \[fileInputId\] = useState\(nextFileInputId\)/.test(
    src
  );

  if (!declared) {
    failures.push({
      file: "src/components/Shared/ImageInput.tsx",
      line: 0,
      text: "no fileInputId state -- the input cannot be targeted by a label",
      inside: "",
    });
  }
  if (ids === 0) {
    failures.push({
      file: "src/components/Shared/ImageInput.tsx",
      line: 0,
      text: "the file input has no id, so no label can activate it",
      inside: "",
    });
  }
  if (fors === 0) {
    failures.push({
      file: "src/components/Shared/ImageInput.tsx",
      line: 0,
      text: "no label references the input via htmlFor",
      inside: "",
    });
  }
  // One id per input, one htmlFor per id. Two inputs sharing an id means the
  // second label opens the first picker.
  if (ids !== fors) {
    failures.push({
      file: "src/components/Shared/ImageInput.tsx",
      line: 0,
      text: `${ids} inputs carry the id but ${fors} labels reference it; ` +
        "they must pair up one-to-one or a label activates the wrong input",
      inside: "",
    });
  }
}

// The CSS half of the fix. The JSX being valid is only half of #7256: the
// input must also still be ACTIVATABLE, which means it must be hidden rather
// than stacked over the label. The old rules (opacity: 0, position: absolute,
// stretched to 100%) existed only because the input was nested in a <button>
// that could not activate it, and they are what put a transparent element on
// top of the visible control. Their return would be a regression with no JSX
// change at all, so the check reads the stylesheet too.
const SCSS = join(SRC, "index.scss");
if (statSync(SCSS).isFile()) {
  const scss = readFileSync(SCSS, "utf8");
  const block = /\.image-input\b[^{]*\{([\s\S]*?)\n\}/.exec(scss);
  const rules = block ? block[1] : "";

  if (!/\[type=["']file["']\][^{]*\{[^}]*display:\s*none/.test(rules)) {
    failures.push({
      file: "src/index.scss",
      line: 0,
      text:
        "the file input is not hidden with `display: none` inside " +
        ".image-input; a stacked, transparent input swallows the click on " +
        "the label (#7256)",
      inside: "",
    });
  }
  if (/\[type=["']file["']\][^{]*\{[^}]*(position:\s*absolute|opacity:\s*0)/.test(rules)) {
    failures.push({
      file: "src/index.scss",
      line: 0,
      text:
        ".image-input still positions or fades the file input over the " +
        "label; that was the pre-#7256 workaround for an input nested in a " +
        "<button> and it intercepts the click",
      inside: "",
    });
  }
}

console.log(`#7256 file-input nesting check\n`);
console.log(`  ${checked} tsx files scanned, ${inputsSeen} file inputs found`);

if (failures.length > 0) {
  console.log(`\n  FAIL: ${failures.length} problem(s)\n`);
  for (const f of failures) {
    const where = f.line ? `${f.file}:${f.line}` : f.file;
    console.log(`  ${where}`);
    console.log(`    ${f.text}`);
    if (f.inside) {
      console.log(`    nested inside <Button> opened at ${f.inside}`);
    }
  }
  console.log(
    "\n  An <input> inside a <button> is invalid HTML. Firefox will not\n" +
      "  activate it, so the control silently does nothing. Use a <label>\n" +
      "  with htmlFor pointing at a sibling input instead. (#7256)"
  );
  process.exit(1);
}

console.log("\n  OK: no file input is nested in a button, the");
console.log("      label/input pairing is balanced, and the");
console.log("      input is hidden rather than overlaid");
process.exit(0);