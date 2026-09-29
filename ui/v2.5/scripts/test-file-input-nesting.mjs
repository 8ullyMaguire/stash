#!/usr/bin/env node
/**
 * Tests for check-file-input-nesting.mjs. #7256.
 *
 * The check script is the ONLY regression guard this fix has -- the frontend
 * has no test runner -- so the check itself needs testing. Without these, a
 * mutation that breaks the check (skip self-closing tags, ignore the depth)
 * passes silently, and the guard rots into something that reports clean on the
 * exact bug it was written for. That already happened once: the first version
 * skipped self-closing tags, and <Form.Control type="file" ... /> is
 * self-closing, so it reported OK on the original #7256 source.
 *
 * Each case below is a fixture plus the verdict it must get. They are
 * self-contained JSX strings, not files on disk, so the parser is exercised
 * directly and the tests cannot be broken by unrelated edits to src/.
 *
 * Run:  node scripts/test-file-input-nesting.mjs
 */

import {
  findNestedFileInputs,
  stripComments,
} from "./check-file-input-nesting-lib.mjs";

// Each case: [name, jsx, expectedNumberOfViolations]
const CASES = [
  [
    "the reported bug: a file input inside a Button",
    `<div>
  <span className="image-input">
    <Button className="minimal">
      <Icon icon={faFile} className="fa-fw" />
      <span>
        <FormattedMessage id="actions.from_file" />
      </span>
      <Form.Control
        type="file"
        onChange={onImageChange}
      />
    </Button>
  </span>
</div>`,
    1,
  ],
  [
    "a self-closing file input on ONE line inside a Button",
    `<Button className="minimal">
  <Form.Control type="file" onChange={h} />
</Button>`,
    1,
    // [inputLine, buttonLine] -- 1-based, as the check reports them.
    [2, 1],
  ],
  [
    "a native <input type=\"file\"> inside a Button",
    `<Button>
  <input type="file" onChange={h} />
</Button>`,
    1,
  ],
  [
    "a file input nested two levels deep inside a Button",
    `<Button>
  <div>
    <span>
      <Form.Control type="file" />
    </span>
  </div>
</Button>`,
    1,
  ],
  [
    "a file input as a SIBLING of a Button -- correct, not a violation",
    `<span className="image-input">
  <label className="btn minimal" htmlFor={fileInputId}>
    <Icon icon={faFile} />
  </label>
  <Form.Control id={fileInputId} type="file" onChange={h} />
</span>`,
    0,
  ],
  [
    "a file input inside a <label> -- valid HTML, not a violation",
    `<Form.Label className="image-input">
  <label className="btn" htmlFor={f}>Browse</label>
  <Form.Control id={f} type="file" />
</Form.Label>`,
    0,
  ],
  [
    "a file input after the Button has closed -- outside it",
    `<Button className="minimal">From file</Button>
<Form.Control type="file" onChange={h} />`,
    0,
  ],
  [
    "a non-file input inside a Button -- out of scope for this check",
    `<Button>
  <Form.Control type="text" onChange={h} />
</Button>`,
    0,
  ],
  [
    "two file inputs, both nested -- both reported",
    `<Button>
  <Form.Control type="file" />
  <Form.Control type="file" />
</Button>`,
    2,
  ],
  [
    "a file input inside a Button, after an unrelated nested pair",
    `<div>
  <Row>
    <Col>
      <Button className="minimal">
        <span><Icon icon={faFile} /></span>
        <Form.Control type="file" />
      </Button>
    </Col>
  </Row>
</div>`,
    1,
  ],
  [
    "a Button containing a self-closing tag does not swallow what follows",
    `<Button>
  <Icon icon={faFile} />
</Button>
<Form.Control type="file" />`,
    0,
  ],
  [
    "a file input mentioned only in a comment is not an element",
    `<Button className="minimal">
  {/* this used to nest <Form.Control type="file" /> in here */}
  <span>From file</span>
</Button>`,
    0,
  ],
  [
    "a block comment naming the type is not an element",
    `/* <Form.Control type="file" /> */
<Button>
  <span>x</span>
</Button>`,
    0,
  ],
  [
    "a // line comment naming the type is not an element",
    // Distinct from the block-comment case above: this needs the SECOND strip
    // rule. Both rules exist because the two comment forms leave different
    // traces -- a block comment is `/* ... */` anywhere, a line comment is
    // `// ...` to end of line -- and the fix's own source comment is a // one
    // describing exactly this bug. Dropping either rule silently starts
    // counting prose as markup.
    `<div>
  // <Form.Control type="file" /> used to live here
  <Button>
    <span>x</span>
  </Button>
</div>`,
    0,
  ],
  [
    "a // line comment inside a Button does not create a violation",
    `<Button>
  // <Form.Control type="file" />
  <span>x</span>
</Button>`,
    0,
  ],
  [
    "single quotes on the type attribute are recognised",
    `<Button>
  <Form.Control type='file' />
</Button>`,
    1,
  ],
  [
    "a self-closing tag inside a Button must not corrupt the depth",
    // The self-close guard has to be tested with a fixture where PUSHING the
    // tag would change the answer. Removing that guard does not affect any
    // other case here: a stray element on the stack is harmless until a
    // </...> pops it, and no other fixture has a closing tag whose nearest
    // match would be corrupted. This one does -- the </div> below would
    // otherwise be matched against the stray <Form.Control> and never reach
    // the <Button>, so the file input after it would look nested.
    `<div>
  <Button className="minimal">
    <Icon icon={faFile} />
  </Button>
  <Form.Control type="file" />
</div>`,
    0,
  ],
  [
    "a self-closing tag inside a Button, then a REAL violation still found",
    // Same shape, plus an input that genuinely is inside a later Button, so a
    // corrupted stack shows up as a MISSED violation rather than only a wrong
    // depth number.
    `<div>
  <Button>
    <Icon icon={faFile} />
  </Button>
  <Button>
    <Form.Control type="file" />
  </Button>
</div>`,
    1,
  ],
  [
    "a void <input> inside a Button must not corrupt the depth",
    // A native <input> is void. If it were pushed, every later </...> would be
    // matched against it instead of the element it really closes.
    `<div>
  <Button>
    <input type="file" />
  </Button>
  <Form.Control type="file" />
</div>`,
    1,
  ],
  [
    "a self-closing <input> outside a Button, then one inside",
    `<div>
  <input type="text" />
  <Button>
    <Form.Control type="file" />
  </Button>
</div>`,
    1,
  ],
  [
    "a lowercase <button> element is not the react-bootstrap Button",
    // Deliberately NOT a violation: this check is about the <Button> component,
    // and a raw <button> has the same problem. Documented in the script as a
    // known limit rather than silently half-covered.
    `<button>
  <Form.Control type="file" />
</button>`,
    0,
  ],
];

let failures = 0;

for (const [name, jsx, want, wantLines] of CASES) {
  const got = findNestedFileInputs(jsx);
  if (got.length !== want) {
    failures += 1;
    console.log(`  FAIL  ${name}`);
    console.log(`        expected ${want} violation(s), got ${got.length}`);
    for (const h of got) {
      console.log(`          line ${h.line} inside <Button> at line ${h.buttonLine}`);
    }
  } else if (wantLines) {
    // A violation reported at the wrong line is a violation a user cannot find.
    // The check prints these line numbers, so they are part of its output, not
    // an internal detail.
    const h = got[0];
    if (h.line !== wantLines[0] || h.buttonLine !== wantLines[1]) {
      failures += 1;
      console.log(`  FAIL  ${name}`);
      console.log(
        `        reported input line ${h.line} / button line ${h.buttonLine},` +
          ` want ${wantLines[0]} / ${wantLines[1]}`
      );
    } else {
      console.log(`  ok    ${name} (${got.length}, lines ${wantLines.join("/")})`);
    }
  } else {
    console.log(`  ok    ${name} (${got.length})`);
  }
}

// stripComments must preserve offsets AND line contents, or every line number
// the check prints is fiction. Length alone is not enough: a replacement that
// keeps the character count but shifts a line's content would pass it.
{
  const src = [
    "line one",
    "// a comment",
    "/* another",
    "   comment */",
    "line five",
  ].join("\n");
  const stripped = stripComments(src);
  const got = stripped.split("\n");

  const sameLength = stripped.length === src.length;
  const sameLines =
    got.length === 5 &&
    got[0] === "line one" &&
    got[1].trim() === "" &&
    got[2].trim() === "" &&
    got[3].trim() === "" &&
    got[4] === "line five";

  if (!sameLength) {
    failures += 1;
    console.log(
      `  FAIL  stripComments changed the length (${src.length} -> ${stripped.length})`
    );
  } else if (!sameLines) {
    failures += 1;
    console.log(
      "  FAIL  stripComments kept the length but moved content between lines: " +
        JSON.stringify(got)
    );
  } else {
    console.log("  ok    stripComments preserves offsets and line contents");
  }

  // And a violation found after comments must land on the right line.
  const jsx = [
    "<div>",
    "  // <Form.Control type=\"file\" /> in a comment",
    "  <Button>",
    "    <span>x</span>",
    "    <Form.Control type=\"file\" />",
    "  </Button>",
    "</div>",
  ].join("\n");
  const hits = findNestedFileInputs(jsx);
  if (hits.length !== 1 || hits[0].line !== 5 || hits[0].buttonLine !== 3) {
    failures += 1;
    console.log(
      `  FAIL  a violation after comments is reported at ` +
        `${hits[0]?.line}/${hits[0]?.buttonLine}, want 5/3`
    );
  } else {
    console.log("  ok    a violation after comments is reported on the right line");
  }
}

console.log("");
if (failures > 0) {
  console.log(`  ${failures} failing case(s)`);
  process.exit(1);
}
console.log(`  all ${CASES.length + 1} cases pass`);
process.exit(0);
