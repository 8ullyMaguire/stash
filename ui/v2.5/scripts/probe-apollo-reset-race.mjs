// Model of PR #7093's queueResetStore, against a real Apollo client.
//
// WHY A .mjs SCRIPT: the UI has no test runner (no `test` script, no
// jest/vitest), and `import` from the source is unavailable because node's ESM
// resolver rejects a module whose first import is a type-only export. This runs
// against a REAL ApolloClient -- a fake store would prove nothing about what
// resetStore does to in-flight queries, which is the entire premise of the PR.
//
// WHAT IS WORTH TESTING. #7093 claims resetStore() cancels in-flight queries
// and renders screens as "Error loading items", and fixes it by waiting for
// quiet, serializing, and coalescing:
//
//   if (resetQueued) return;                 <-- coalescing
//   resetQueued = false;                     <-- cleared BEFORE the reset
//   await client.resetStore();
//
// The comment asserts "events arriving before this point are covered by the
// reset below; later ones must queue their own". The question is whether that
// is true, and the answer decides whether the coalescing drops a scan-complete
// event -- which is a user seeing stale data until the next reload.

// Node's ESM resolver rejects a DIRECTORY import of "@apollo/client/core"
// (ERR_UNSUPPORTED_DIR_IMPORT) and suggests the concrete file itself. The
// package ships core.cjs (CommonJS) for exactly this case; a named import from
// a CJS module works fine under node's interop.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import apollo from "@apollo/client/core/core.cjs";

const { ApolloClient, InMemoryCache, gql, Observable } = apollo;

const HERE = dirname(fileURLToPath(import.meta.url));

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

let failures = 0;
const check = (name, cond, detail = "") => {
  if (cond) console.log(`  ok   ${name}`);
  else {
    console.log(`  FAIL ${name}${detail ? ` -- ${detail}` : ""}`);
    failures++;
  }
};

// A controllable in-flight query. We never let the link resolve until we say so,
// so the loading window is exact rather than timing-dependent.
function makeClient() {
  let resolvePending;
  const gate = new Promise((r) => {
    resolvePending = r;
  });
  let requests = 0;
  let lastResult = null;

  const client = new ApolloClient({
    cache: new InMemoryCache(),
    // Must be a real Observable, not a bare promise: Apollo's Concast
    // subscribes to it, and handing it a promise fails deep inside
    // utilities.cjs with "Cannot read properties of undefined (reading
    // 'subscribe')" -- a fixture bug that reads like an Apollo bug.
    link: {
      request: (op) => {
        requests++;
        lastResult = op.operationName;
        return new Observable((observer) => {
          gate.then((data) => {
            observer.next({ data });
            observer.complete();
          });
        });
      },
    },
  });

  return {
    client,
    requests: () => requests,
    lastResult: () => lastResult,
    release: () => resolvePending(),
    gate,
  };
}

// The exact shape of #7093's queueResetStore, extracted from the diff.
function makeQueue(client, { pollMs = 10, maxWaitMs = 200 } = {}) {
  const queriesInFlight = () => {
    for (const query of client.getObservableQueries().values()) {
      if (query.getCurrentResult().loading) return true;
    }
    return false;
  };

  let resetQueued = false;
  let resetChain = Promise.resolve();
  let resets = 0;
  let lastResetError = null;

  const queueResetStore = () => {
    if (resetQueued) return false; // coalesced
    resetQueued = true;
    resetChain = resetChain
      .then(async () => {
        const deadline = Date.now() + maxWaitMs;
        while (queriesInFlight() && Date.now() < deadline) {
          await sleep(pollMs);
        }
        resetQueued = false;
        resets++;
        await client.resetStore();
      })
      .catch((e) => {
        lastResetError = e;
      });
    return true;
  };

  return {
    queueResetStore,
    queriesInFlight,
    resets: () => resets,
    lastResetError: () => lastResetError,
    settled: () => resetChain,
  };
}

// ---------------------------------------------------------------------------
console.log("premise: getCurrentResult() is a pure read and does not fetch");
// ---------------------------------------------------------------------------
{
  const { client, requests } = makeClient();
  const obs = client.watchQuery({ query: gql`query Q { a }` });
  const before = requests();
  obs.getCurrentResult();
  obs.getCurrentResult();
  obs.getCurrentResult();
  check(
    "polling getCurrentResult issues no network request",
    requests() === before,
    `requests went ${before} -> ${requests()}`
  );
  await obs.result();
}

// ---------------------------------------------------------------------------
console.log("\nthe wait actually defers the reset while a query is loading");
// ---------------------------------------------------------------------------
{
  const { client, release, requests } = makeClient();
  const obs = client.watchQuery({ query: gql`query Q { a }` });
  const p = obs.result(); // now in flight, gated

  const q = makeQueue(client);
  check("a query in flight is reported as loading", q.queriesInFlight() === true);

  q.queueResetStore();
  await sleep(60);
  check(
    "the reset is still deferred after 6 polls",
    q.resets() === 0,
    `resets=${q.resets()}`
  );

  release();
  await p;
  await q.settled();
  check("the reset ran once the query finished", q.resets() === 1, `resets=${q.resets()}`);
  // resetStore() refetches every active query, so the count goes to 2. Asserting
  // 1 would be asserting that the reset DID NOT refetch -- the opposite of the
  // behaviour the PR depends on.
  check("the reset refetched the active query", requests() === 2, `requests=${requests()}`);
  void obs;
}

// ---------------------------------------------------------------------------
console.log("\ncoalescing: is a second event DROPPED, and is that sound?");
// ---------------------------------------------------------------------------
{
  const { client, release } = makeClient();
  const obs = client.watchQuery({ query: gql`query Q { a }` });
  const p = obs.result();

  const q = makeQueue(client, { pollMs: 20, maxWaitMs: 1000 });
  const first = q.queueResetStore(); // event A
  const second = q.queueResetStore(); // event B, while A is still waiting
  const third = q.queueResetStore(); // event C

  check("the first event is queued", first === true);
  check("the second event is COALESCED (dropped)", second === false);
  check("the third event is COALESCED (dropped)", third === false);

  release();
  await p;
  await q.settled();

  check("exactly one reset ran for three events", q.resets() === 1, `resets=${q.resets()}`);
  void obs;
}

// The claim in the source comment. "events arriving before this point are
// covered by the reset below" -- the reset's refetches happen AFTER the wait, so
// they DO see any data written before the reset ran. That part holds.
//
// What does NOT hold is the converse: a scan-complete event that arrives during
// the wait is dropped, and the reset that eventually runs began its refetch
// snapshot at an EARLIER time than the event. It survives only because the reset
// itself is deferred past the wait. So the outcome is right by construction, and
// the code is correct -- but only because of the ordering, which nothing pins.
// If someone later moves `resetQueued = false` after the reset (the obvious
// "cleanup" edit), the coalescing becomes a genuine dropped event.

// ---------------------------------------------------------------------------
console.log("\nthe deadline bounds the wait");
// ---------------------------------------------------------------------------
{
  const { client, release } = makeClient();
  const obs = client.watchQuery({ query: gql`query Q { a }` });
  const p = obs.result(); // gated

  // Attach the handler up front: with a query still in flight at the deadline,
  // BOTH obs.result() and resetStore() reject with the invariant that
  // cancelPendingFetches throws. That rejection IS the finding -- it is what the
  // user sees as "Error loading items" -- so it is captured and asserted on
  // rather than allowed to crash the run.
  let queryError = null;
  p.catch((e) => {
    queryError = e;
  });

  const q = makeQueue(client, { pollMs: 5, maxWaitMs: 60 });
  q.queueResetStore();
  // Do NOT await q.settled() here: resetStore() is itself blocked on the gated
  // query, so awaiting the chain hangs on the probe's own instrumentation. The
  // queue's deadline has already elapsed by the time we check, so race the
  // settle against a bounded wait.
  await Promise.race([q.settled(), sleep(300)]);
  await sleep(20);

  check(
    "the wait gives up at the deadline rather than hanging",
    q.resets() === 1,
    `resets=${q.resets()}`
  );
  check(
    "resetting with a query still in flight REJECTS the query (the reported bug)",
    queryError !== null,
    "the in-flight query resolved instead of being cancelled"
  );

  // Release the gate so the still-pending result settles and node does not report
  // an unsettled top-level await. By here every assertion has run.
  release();
  await sleep(20);
  void obs;
}

// ---------------------------------------------------------------------------
console.log("\nserialization: a reset queued DURING another reset runs after it");
// ---------------------------------------------------------------------------
// The first draft of this case asserted 2 resets from two back-to-back
// queueResetStore() calls and got 1 -- and the code was RIGHT and the assertion
// was wrong. The first callback is still inside its poll loop when the second
// event arrives, so `resetQueued` is still true and the second event is
// coalesced. Coalescing is the documented intent.
//
// To exercise the serialization path the second event must arrive in the window
// where the flag has been cleared but the first resetStore() is still running --
// which is the window the PR's `resetQueued = false` placement creates on
// purpose, so that a new event is not swallowed by an in-progress reset.
{
  const { client, release } = makeClient();
  const obs = client.watchQuery({ query: gql`query Q { a }` });
  const p = obs.result();

  const q = makeQueue(client, { pollMs: 5, maxWaitMs: 500 });
  q.queueResetStore();

  release();
  await p;
  // Let the first callback finish its poll loop, clear the flag, and enter
  // resetStore() -- which refetches through the SAME already-resolved gate, so
  // it completes promptly. Then queue the second event.
  await sleep(50);
  const midway = q.resets();
  const queued = q.queueResetStore();
  await Promise.race([q.settled(), sleep(500)]);

  check("the first reset ran before the second event was queued", midway === 1, `resets=${midway}`);
  check("the second event is accepted, not coalesced", queued === true);
  check(
    "it runs as a second, serialized reset",
    q.resets() === 2,
    `resets=${q.resets()}`
  );
  void obs;
}

// ---------------------------------------------------------------------------
console.log("\nTHE PREMISE, observed: resetStore() cancels in-flight queries");
// ---------------------------------------------------------------------------
// Apollo's own source, QueryManager.clearStore (core.cjs:1554):
//
//   QueryManager.prototype.clearStore = function (options) {
//     ...
//     this.cancelPendingFetches(globals.newInvariantError(42));
//
// so every in-flight query is REJECTED with an invariant. That invariant is
// precisely why screens briefly render a failed "Error loading items" state --
// and it is why a store reset has to wait for quiet. Read from the installed
// package rather than recalled from documentation.
{
  const corePath = join(
    HERE,
    "..",
    "node_modules",
    "@apollo",
    "client",
    "core",
    "core.cjs"
  );
  const core = readFileSync(corePath, "utf8");
  const clearStore = core.slice(core.indexOf("QueryManager.prototype.clearStore"));
  check(
    "clearStore calls cancelPendingFetches with an invariant error",
    /cancelPendingFetches\(globals\.newInvariantError\(/.test(clearStore.slice(0, 400))
  );
}

console.log(
  failures === 0
    ? "\nOK: the wait defers correctly, the deadline bounds it, and resets serialize"
    : `\nFAIL: ${failures} check(s)`
);
process.exit(failures === 0 ? 0 : 1);
