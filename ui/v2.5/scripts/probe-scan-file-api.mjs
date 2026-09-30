// Establish what stash#6457 ("Update API to scan in file(s), add metadata on
// scan") is actually asking for, by MEASURING the current behaviour rather than
// reading the title and implementing it.
//
// The issue has two clauses and they are not equally open:
//
//   (a) "scan in file(s)" -- does the API already accept FILE paths?
//   (b) "add metadata on scan" -- can a caller ATTACH metadata to a scanned file?
//
// I was about to implement (a) and it looks like it already works: `walkDir`
// returns early when the root is not a directory, so SymWalk over a file path
// visits that file. That is a reading, not a fact. This measures it.
//
// And the load-bearing question for (b) is what the scan pipeline currently
// does with a file, and where metadata could be attached. That is read from the
// source rather than guessed.

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

// scripts/ -> v2.5/ -> ui/ -> stash/  THREE levels, not two. The first version
// resolved one level short, every read returned "", and all 8 checks failed
// VACUOUSLY -- a guard whose subject it never read. `src()` returning "" for a
// file that exists is exactly the "matches zero things" trap, so assert the
// reads are non-empty instead of trusting the checks below.
const HERE = dirname(fileURLToPath(import.meta.url));
const ROOT = join(HERE, "..", "..", "..");

const EMPTY_READS = [];
let failures = 0;
const check = (name, cond, detail = "") => {
  if (cond) console.log(`  ok   ${name}`);
  else {
    console.log(`  FAIL ${name}${detail ? ` -- ${detail}` : ""}`);
    failures++;
  }
};
const src = (p) => {
  try {
    const t = readFileSync(join(ROOT, p), "utf8");
    if (!t) EMPTY_READS.push(p);
    return t;
  } catch {
    EMPTY_READS.push(p + " (unreadable)");
    return "";
  }
};

console.log("(a) DOES THE SCAN API ALREADY ACCEPT FILE PATHS?");

// walkDir is the recursion; the question is whether a non-directory root is
// visited once and the walk then stops.
{
  const walk = src("pkg/file/walk.go");
  const i = walk.indexOf("func walkDir(");
  const body = walk.slice(i, walk.indexOf("\nfunc ", i + 1));
  console.log("  walkDir:");
  for (const l of body.split("\n").slice(0, 8)) console.log("    " + l.trim());

  check(
    "walkDir returns early when the root is NOT a directory",
    /if err := walkDirFn\(path, d, nil\); err != nil \|\| !d\.IsDir\(\)/.test(body),
    "a file root would otherwise recurse into readDir"
  );
  check(
    "and it is reached at all, i.e. the walk starts at the root",
    /walkDir\(f, root, &statDirEntry\{info\}, fn\)/.test(walk)
  );
  check("SymWalk delegates to walkSym", /func SymWalk[\s\S]*?return walkSym\(fs, path, path, walkFn\)/.test(walk));
}

// queueFiles is the caller: it iterates the requested paths and SymWalks each.
{
  const task = src("internal/manager/task_scan.go");
  const i = task.indexOf("func (j *ScanJob) queueFiles(");
  const body = task.slice(i, task.indexOf("\nfunc ", i + 1));
  console.log("\n  queueFiles:");
  for (const l of body.split("\n").filter((l) => l.trim()).slice(0, 12)) console.log("    " + l.trim());

  check("queueFiles SymWalks each requested path, whatever it is", /for _, p := range paths \{[\s\S]{0,80}SymWalk\(fs, p,/.test(body));
  check(
    "it does NOT require the path to be a directory first",
    !/IsDir\(\)|Stat\(p\)|isDir/.test(body),
    "a pre-check for IsDir would be the thing rejecting files"
  );
}

// getScanPaths is the other half: does it drop a file path?
{
  // getScanPaths and ScanMetadataInput live in manager_tasks.go, not task_scan.go.
  // The first version looked in the wrong file and reported "NOT FOUND", which
  // read as a missing capability rather than a wrong path.
  const m = src("internal/manager/manager_tasks.go");
  const i = m.indexOf("func getScanPaths(");
  check("getScanPaths exists, in manager_tasks.go", i > 0, "not found in manager_tasks.go");
  check("and is reached from Execute", /getScanPaths\(input\.Paths\)/.test(src("internal/manager/task_scan.go")));
  if (i > 0) {
    const body = m.slice(i, m.indexOf("\nfunc ", i + 1));
    console.log("\n  getScanPaths body:");
    for (const l of body.split("\n").filter((l) => l.trim()).slice(0, 14)) console.log("    " + l.trim());
    // The load-bearing question for clause (a): does it map an arbitrary path to
    // a library, or does it only accept paths that already belong to one?
    check(
      "it maps a requested path to a CONFIGURED stash library, so a file outside any library cannot be scanned this way",
      /GetStashFromPath|GetStashFromDirPath/.test(body),
      body.slice(0, 120).replace(/\n/g, " ")
    );
  }
}

console.log("\n(b) CAN A CALLER ATTACH METADATA TO A SCANNED FILE?");

// The scan mutation's input is the API surface. If it has no metadata field,
// (b) is genuinely open.
{
  const gql = src("graphql/schema/types/metadata.graphql");
  const i = gql.indexOf("input ScanMetadataInput {");
  const body = gql.slice(i, gql.indexOf("\n}", i));
  console.log("\n  ScanMetadataInput fields:");
  const fields = [...body.matchAll(/^\s{2}(\w+):/gm)].map((m) => m[1]);
  for (const f of fields) console.log("    " + f);

  check("it accepts paths, so clause (a) has a surface", fields.includes("paths"));
  check(
    "it accepts NO ids and NO metadata -- so (b) is genuinely open",
    !fields.some((f) => /id|performer|studio|tag|detail|title|meta/i.test(f)),
    `fields: ${fields.join(",")}`
  );
}

// The resolver is where an input would have to be threaded through.
{
  const r = src("internal/api/resolver_mutation_metadata.go");
  const i = r.indexOf("func (r *mutationResolver) MetadataScan(");
  const body = r.slice(i, r.indexOf("\nfunc ", i + 1));
  console.log("\n  MetadataScan resolver:");
  for (const l of body.split("\n").filter((l) => l.trim()).slice(0, 10)) console.log("    " + l.trim());
  check("the resolver takes only ScanMetadataInput and returns a job id", /MetadataScan\(ctx context\.Context, input manager\.ScanMetadataInput\)/.test(body));
  // "no metadata parameter" is a claim about the SIGNATURE, not the body -- the
  // body is the function called MetadataScan, so grepping it for /Metadata/
  // matches its own name. The first version did exactly that and failed wrongly.
  const sig = body.slice(0, body.indexOf(")"));
  check("so the whole chain has no metadata parameter to thread", !/Metadata2|Detail|Performer|Studio|Tag/.test(sig), sig.trim());
}

// The ScanMetadataInput Go type: the place a metadata field would have to land.
{
  const m = src("internal/manager/manager_tasks.go");
  const i = m.indexOf("type ScanMetadataInput struct");
  check("ScanMetadataInput is the Go-side input a new field must join", i > 0);
  if (i > 0) {
    const body = m.slice(i, m.indexOf("\n}", i));
    console.log("\n  manager.ScanMetadataInput:");
    for (const l of body.split("\n").filter((l) => l.trim())) console.log("    " + l.trim());
  }
}

console.log("\nTHE SCOPE THIS ESTABLISHES");
console.log("  (a) scan in file(s): the walk already visits a file root, and the");
console.log("      caller passes paths through unfiltered. A file path is not");
console.log("      rejected by the walk.");
console.log("  (b) add metadata on scan: genuinely absent from the schema, the");
console.log("      resolver and the Go input. That is the whole of the gap.");
console.log("");
console.log("  NOTE, and it is the reason this probe exists: whether a file path");
console.log("  is USEFUL is a different question from whether the walk VISITS it.");
console.log("  A single file inside a stash library can be walked, but if it does");
console.log("  not already belong to a known library its stash-ignore and");
console.log("  extension filters decide what happens next -- and that is where a");
console.log("  'scan this one file' API would earn its keep, by carrying metadata");
console.log("  for a file the library has not accepted yet.");
console.log("  Nothing above measures that, so nothing above claims it.");

console.log("\nNON-VACUITY: every source read must have returned content");
console.log("  reads: " + (EMPTY_READS.length ? "EMPTY -> " + EMPTY_READS.join(", ") : "all non-empty"));
check("no source read came back empty (a silent read makes every check below vacuous)", EMPTY_READS.length === 0, EMPTY_READS.join(", "));

console.log(
  failures === 0
    ? "\nOK: (a) is already satisfied by the walk; (b) is the real and open gap"
    : `\nFAIL: ${failures} check(s)`
);
process.exit(failures === 0 ? 0 : 1);
