// Verify stash#571 ("Support for multiple performer images", bounty) before
// implementing anything for it.
//
// WHY THIS PROBE: the issue's premise is that the data model holds only ONE
// image per performer. Measured, that is not true, and the real defect is
// different from the one the issue describes. Writing a list field without
// checking would have built the wrong thing.
//
// THE TWO IMAGE SYSTEMS, which is the whole finding:
//
//   1. A legacy single-blob COLUMN on `performers`. The performer's image is
//      stored as a blob checksum in that column. THIS is what the API writes:
//      `blobJoinQueryBuilder.UpdateImage` issues
//          UPDATE performers SET <blobCol> = ? WHERE id = ?
//      and what `performerResolver.ImagePath` reads, via `HasImage`.
//
//   2. The `images` TABLE plus the `performers_images` join table (migration
//      13), which is many-to-many: performer_id, image_id, both indexed, both
//      ON DELETE CASCADE. THIS is what `performerResolver.ImageCount` reads,
//      via `image.CountByPerformerID`.
//
// So the mutation writes system 1 and the counter reads system 2. If a performer
// created with an image reports image_count 0, then the bounty issue is real,
// the gap is well defined, and `image_count` is simply lying to every client.

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..", "..", "..");

let failures = 0;
const EMPTY_READS = [];
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

console.log("SYSTEM 1: the single-blob column, which is what the API WRITES");
{
  const blob = src("pkg/sqlite/blob.go");
  const i = blob.indexOf("func (qb *blobJoinQueryBuilder) UpdateImage(");
  const body = blob.slice(i, blob.indexOf("\nfunc ", i + 1));
  console.log("  blobJoinQueryBuilder.UpdateImage:");
  for (const l of body.split("\n").filter((l) => l.trim()).slice(0, 12)) console.log("    " + l.trim());

  check(
    "it is a single-column UPDATE, so it can hold exactly one image",
    /UPDATE %s SET %s = \? WHERE id = \?/.test(body),
    "a many-image write would not be a single SET"
  );
  check("and it never touches the images or join tables", !/images|joinTable\) VALUES|INSERT INTO/.test(body.split("UPDATE")[0] + body.split("UPDATE")[1]));
}

console.log("\nSYSTEM 2: the images table + join table, which is what the counter READS");
{
  const mig = src("pkg/sqlite/migrations/13_images.up.sql");
  const i = mig.indexOf("CREATE TABLE `performers_images`");
  console.log("  migration 13:");
  for (const l of mig.slice(i, mig.indexOf(";", i) + 1).split("\n").filter((l) => l.trim())) console.log("    " + l.trim());

  check("the join table exists and is many-to-many", /performer_id/.test(mig.slice(i, i + 300)) && /image_id/.test(mig.slice(i, i + 300)));
  check("both sides are indexed", (mig.match(/index_performers_images_on_(performer|image)_id/g) || []).length === 2);
  check("with ON DELETE CASCADE on both sides", (mig.slice(i, i + 400).match(/on delete CASCADE/gi) || []).length === 2);

  // The schema allows many; the API does not. That is the gap, and it is in the
  // GRAPHQL surface, not the tables.
  const gql = src("graphql/schema/types/performer.graphql");
  const ci = gql.indexOf("input PerformerCreateInput");
  const create = gql.slice(ci, gql.indexOf("\n}", ci));
  const ui = gql.indexOf("input PerformerUpdateInput");
  const update = gql.slice(ui, gql.indexOf("\n}", ui));

  check("PerformerCreateInput takes a SINGLE image: String", /^\s*image: String$/m.test(create), create.match(/image:.*/)?.[0]);
  check("there is no images list on create", !/images:\s*\[/.test(create));
  check("nor on update", !/images:\s*\[/.test(update));

  // The read side already exposes the many-image shape, which is what makes the
  // divergence observable rather than merely theoretical.
  const pt = gql.slice(gql.indexOf("type Performer {"), gql.indexOf("\n}", gql.indexOf("type Performer {")));
  check("but Performer already exposes image_count: Int!", /image_count:\s*Int!/.test(pt));
  check("and image_path: String", /image_path:\s*String/.test(pt));
}

console.log("\nTHE DIVERGENCE: which store does each resolver read?");
{
  const r = src("internal/api/resolver_model_performer.go");

  const ip = r.slice(r.indexOf("func (r *performerResolver) ImagePath("), r.indexOf("\nfunc ", r.indexOf("func (r *performerResolver) ImagePath(") + 10));
  const ic = r.slice(r.indexOf("func (r *performerResolver) ImageCount("), r.indexOf("\nfunc ", r.indexOf("func (r *performerResolver) ImageCount(") + 10));

  console.log("  ImagePath:");
  for (const l of ip.split("\n").filter((l) => l.trim()).slice(0, 8)) console.log("    " + l.trim());
  console.log("  ImageCount:");
  for (const l of ic.split("\n").filter((l) => l.trim()).slice(0, 8)) console.log("    " + l.trim());

  check("ImagePath reads the BLOB (HasImage) -- system 1", /HasImage\(ctx, obj\.ID\)/.test(ip));
  check("ImageCount reads the images TABLE (image.CountByPerformerID) -- system 2", /image\.CountByPerformerID/.test(ic));
  check(
    "so a performer created WITH an image writes system 1 and is counted in system 2",
    /HasImage/.test(ip) && /CountByPerformerID/.test(ic)
  );
  check("and the write path never inserts a row the counter could see", !/CreateImage|AddImage|image_id/.test(src("internal/api/resolver_mutation_performer.go")));
}

console.log("\nNON-VACUITY");
console.log("  reads: " + (EMPTY_READS.length ? "EMPTY -> " + EMPTY_READS.join(", ") : "all non-empty"));
check("no source read came back empty", EMPTY_READS.length === 0, EMPTY_READS.join(", "));

console.log("\nWHAT THIS ESTABLISHES");
console.log("  The data model ALREADY supports many performer images");
console.log("  (performers_images is a many-to-many join table). The bounty issue's");
console.log("  premise -- that only one can be stored -- is not the blocker.");
console.log("");
console.log("  The actual gap is that the performer API never writes that table:");
console.log("  it writes a single-blob column, while image_count counts join rows");
console.log("  the mutation never creates. So image_count reports 0 for a performer");
console.log("  that demonstrably has an image, and a client cannot set a second one");
console.log("  through the API at all.");
console.log("");
console.log("  Consequence for the fix: adding an `images: [String!]` input is NOT");
console.log("  sufficient on its own -- it would have to write the join table, or");
console.log("  image_count would keep lying. That is a design choice, not a field.");
console.log("  NOT measured here: whether any code path DOES populate");
console.log("  performers_images (autotag's PerformerImages is the candidate). If");
console.log("  something already writes it, image_count is right for those rows and");
console.log("  the divergence is narrower than stated above. That is the next thing");
console.log("  to check, and it needs the store tests rather than a text probe.");

console.log(
  failures === 0
    ? "\nOK: many-to-many storage exists; the API writes a single blob and the counter reads the join table"
    : `\nFAIL: ${failures} check(s)`
);
process.exit(failures === 0 ? 0 : 1);
