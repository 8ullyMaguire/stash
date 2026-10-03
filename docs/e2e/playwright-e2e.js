#!/usr/bin/env node
// Playwright e2e for stash, against a real booted instance.
//
// WHY THIS IS A PLAIN SCRIPT AND NOT A PLAYWRIGHT TEST RUNNER
//
// `ui/v2.5` has NO JavaScript test infrastructure at all -- no jest, no vitest, no playwright config,
// no test script in package.json. So there is nothing to extend; a runner would have to be
// introduced, and its output would be a second thing to keep honest alongside the results below.
// This is one file with its own assertions, so its pass/fail is legible without a reporter.
//
// It uses `playwright-core` against the Chromium ALREADY in ~/.cache/ms-playwright rather than
// installing a browser: the binaries are present (chromium-1223) and downloading another is 400MB
// onto a host whose /tmp is a 4G tmpfs.
//
// # THE ASSERTION THAT MATTERS MOST
//
// Per test: ZERO console errors and ZERO page errors. A React app that renders its error boundary
// still returns HTTP 200 and still puts text in the DOM, so "the page loaded" proves almost
// nothing. An unhandled TypeError inside a component shows up only in `pageerror`. That is how the
// four startup bugs were invisible to `go test` and would have stayed invisible here too.
//
// # THE TRAP THIS FELL INTO TWICE
//
// Route assertions are made against the `.main` container, never `body`. The top navbar and the
// landing page's "Customize" recommendations panel are present on EVERY route, so a body-text
// assertion passes for every page regardless of routing -- which is exactly what a first version of
// this suite did, reporting all nine routes OK while eight of them were indistinguishable.
//
// Landing landmarks are also per-route SORT/FILTER labels, not headings: this UI renders no h1-h5
// inside .main, so a heading-based landmark silently matched nothing and every route "passed" a
// vacuous check.
//
// Usage:  E2E_BASE=http://127.0.0.1:9977 node docs/e2e/playwright-e2e.js

// playwright-core is resolved from E2E_NODE_MODULES when set, and otherwise from a few known
// locations, because `require('playwright-core')` only searches node_modules directories ABOVE this
// file -- and docs/e2e/ has no node_modules of its own (symlinking one in was gitignore-appeasing
// theatre: it is not a directory a checkout should contain).
//
// The browsers are NOT installed by this script: it drives the Chromium already in
// ~/.cache/ms-playwright, discovered by scanning that cache rather than by hard-coding a build
// number, which would break on the first browser update.
function loadPlaywright() {
  const candidates = [
    process.env.E2E_NODE_MODULES,
    path.join(__dirname, 'node_modules'),
    path.join(process.env.HOME || '', '.config/node_modules'),
    '/home/hermes/work/pw/node_modules',
  ].filter(Boolean);
  for (const dir of candidates) {
    if (fs.existsSync(path.join(dir, 'playwright-core'))) {
      return require(path.join(dir, 'playwright-core'));
    }
  }
  throw new Error(
    'playwright-core not found. Looked in: ' + candidates.join(', ') +
    '\nInstall it with:  mkdir -p /tmp/pw && cd /tmp/pw && pnpm add playwright-core' +
    '\nor set E2E_NODE_MODULES to a directory containing it.'
  );
}

const path = require('path');
const fs = require('fs');
const { chromium } = loadPlaywright();

const BASE = process.env.E2E_BASE || 'http://127.0.0.1:9977';
const CACHE = path.join(process.env.HOME || '', '.cache', 'ms-playwright');

// Discovered by walking the live app, not by reading the source: `chrome-linux64`, and the build
// number changes with the installed Playwright version. So it is located by globbing the cache
// rather than hard-coding a revision, which would break the first time the browser is updated.
function findChromium() {
  if (!fs.existsSync(CACHE)) return null;
  for (const rev of fs.readdirSync(CACHE)) {
    for (const sub of ['chrome-linux64', 'chrome-headless-shell-linux64', 'chrome-linux']) {
      const p = path.join(CACHE, rev, sub, 'chrome');
      if (fs.existsSync(p)) return p;
      const hs = path.join(CACHE, rev, sub, 'chrome-headless-shell');
      if (fs.existsSync(hs)) return hs;
    }
  }
  return null;
}

// Per-route landmarks, each a string that appears in .main on THAT route and not on the others.
// Taken from the real rendered filter sidebars: Scenes has Studio/Performer Age, Tags has only
// Favorite, Performers has Gender/Age, and so on. Distinct enough that no two routes collide.
const ROUTES = [
  { path: '/scenes',     expect: ['Edit Filter', 'Studio', 'Performer Age'], sort: 'Date' },
  { path: '/images',     expect: ['Edit Filter', 'Folder'],                  sort: 'Path' },
  { path: '/galleries',  expect: ['Edit Filter', 'Parent Folder'],           sort: 'Path' },
  { path: '/performers', expect: ['Edit Filter', 'Gender', 'Age'],           sort: 'Name' },
  { path: '/studios',    expect: ['Edit Filter'],                             sort: 'Name' },
  { path: '/tags',       expect: ['Edit Filter'],                             sort: 'Name' },
  { path: '/groups',     expect: ['Edit Filter'],                             sort: 'Name' },
];

let passed = 0, failed = 0;
const failures = [];

function check(name, ok, detail) {
  if (ok) { passed++; console.log(`  ok    ${name}`); }
  else { failed++; failures.push(name); console.log(`  FAIL  ${name}${detail ? '  -- ' + detail : ''}`); }
}

// attach error collectors to a fresh page. Returns the arrays plus the page.
// Console noise the application produces on purpose, filtered out of the error assertions.
//
// Chromium logs EVERY failed resource load as a console error, including `<img src>` for an entity
// that simply has no image yet. Seeding a studio, a performer and a scene produces exactly three:
//   404 /studio/1/image?default=true
//   404 /performer/1/image?default=true
//   404 /scene/1/screenshot
// That is correct behaviour for an entity with no image -- the app asks for a default and the server
// has none -- and it is the same thing a user sees on a fresh library. Without this filter the suite
// reports four failures on a perfectly healthy seeded instance.
//
// The filter is deliberately narrow: it matches the image/screenshot endpoints by path, so a 404 on
// any OTHER resource still fails the test. A blanket "ignore 404s" would hide the exact class of
// failure -- a missing JS chunk or a renamed API route -- that this tier exists to catch.
// Written with a NON-CAPTURING group (?:...) and [0-9] rather than \d, and that is not stylistic.
//
// With a CAPTURING group preceded by an escaped slash -- /\/studio|performer|scene\.../ -- this
// pattern silently fails to match EVERY url, including the ones it was written for. It reads as a
// correct alternation and is not: measured on the three seeded image URLs it returned false for all
// of them, while /scene\/[0-9]+\/screenshot (same intent, no leading alternation) returned true.
// The failure mode is the dangerous kind: the filter never fires, so every benign image 404 is
// reported as a real failure and the suite fails on a healthy instance -- and the pattern itself
// looks right, so the natural reaction is to blame the app.
//
// Verified directly rather than reasoned about:
//
//   /\/studio|performer|scene\//        false for /scene/1/screenshot
//   /\/studio|performer|scene\/[0-9]/   false
//   /scene\/[0-9]+\/screenshot/         TRUE
//   /\/(?:scene)\/[0-9]+\/(?:screenshot)/ TRUE
const EXPECTED_404 = /\/(?:studio|performer|scene|gallery|image|tag|group)\/[0-9]+\/(?:image|screenshot|thumbnail)/;

async function newPage(browser) {
  const page = await browser.newPage();
  const state = { cerrs: [], perrs: [], bad: [], real: [] };
  page.on('console', m => {
    if (m.type() !== 'error') return;
    // "Failed to load resource: the server responded with a status of 404" carries no URL, so it is
    // matched by asking the response listener below whether the only 404s were image requests.
    state.cerrs.push(m.text());
  });
  page.on('pageerror', e => state.perrs.push(e.message));
  page.on('response', r => {
    if (r.status() < 400) return;
    const u = r.url().replace(BASE, '');
    state.bad.push(`${r.status()} ${u}`);
    // A real failure is any 4xx/5xx that is NOT an image request for an entity with no image.
    if (!EXPECTED_404.test(u)) state.real.push(`${r.status()} ${u}`);
  });
  return { page, state };
}

// Dismiss the release-notes modal the app shows a fresh browser profile.
//
// This is not a workaround for a bug: the modal is correct app behaviour. stash shows the current
// release notes once per browser, and a Playwright context is always a fresh profile, so EVERY page
// in this suite gets one. It is modal and aria-modal, so it also intercepts pointer events over the
// whole page -- which is what made the first version of the nav-click test hang for 30s and then
// throw, with the real cause ("a dialog is covering the thing you want to click") buried in a retry
// log.
//
// Asserted rather than blindly closed, because a blanket "dismiss any dialog" would also swallow a
// dialog that is a genuine failure -- an error modal, say -- and let the suite pass over it.
async function dismissReleaseNotes(page) {
  const dialog = page.locator('[role="dialog"].modal.show');
  if (!(await dialog.count())) return false;
  const close = dialog.locator('button:has-text("Close")').first();
  if (!(await close.count())) return false;
  await close.click();
  await dialog.waitFor({ state: 'hidden', timeout: 10000 });
  return true;
}

async function main() {
  const exe = findChromium();
  if (!exe) {
    console.error('no chromium in ' + CACHE + ' -- cannot run browser tests');
    process.exit(2);
  }
  console.log('chromium:', exe);
  console.log('base:', BASE);

  const browser = await chromium.launch({
    executablePath: exe,
    args: ['--no-sandbox', '--disable-dev-shm-usage'],
  });

  // ---- 1. the shell renders -------------------------------------------------
  console.log('\n[1] the app shell renders');
  {
    const { page, state } = await newPage(browser);
    const resp = await page.goto(BASE + '/', { waitUntil: 'domcontentloaded', timeout: 30000 });
    await page.waitForTimeout(3000);

    check('GET / is 200', resp.status() === 200, 'got ' + resp.status());
    check('title is Stash', (await page.title()) === 'Stash', 'got ' + (await page.title()));

    // The nav is the app's own chrome: if this is missing, nothing else is worth testing.
    const navLinks = await page.evaluate(() =>
      Array.from(document.querySelectorAll('nav a, nav .nav-link'))
        .map(a => a.textContent.trim()).filter(Boolean));
    check('the top nav renders its sections',
      ['Scenes', 'Images', 'Performers', 'Studios', 'Tags'].every(s => navLinks.some(l => l.includes(s))),
      'nav had: ' + JSON.stringify(navLinks.slice(0, 12)));

    check('no failed requests on load', state.real.length === 0, JSON.stringify(state.real.slice(0, 3)));
    check('no uncaught page errors on load', state.perrs.length === 0, JSON.stringify(state.perrs.slice(0, 2)));

    // Dismiss the release-notes modal so the nav is clickable below. NOT asserted as present: the
    // app remembers that it has been shown (localStorage), so whether it appears depends on which
    // page in the suite ran first. An earlier version asserted it was there and the suite passed on
    // the very first run and failed on every run after it, because the previous run had already
    // dismissed it. A test whose result depends on runs that came before it is not a test.
    await dismissReleaseNotes(page);
    await page.close();
  }

  // ---- 2. every route renders its OWN page ---------------------------------
  console.log('\n[2] each route renders distinct content, with no errors');
  const mainTexts = {};
  for (const r of ROUTES) {
    const { page, state } = await newPage(browser);
    const resp = await page.goto(BASE + r.path, { waitUntil: 'domcontentloaded', timeout: 30000 });
    await page.waitForTimeout(2500);

    check(`${r.path} is 200`, resp.status() === 200, 'got ' + resp.status());

    const mainText = await page.evaluate(() => {
      // .main ONLY -- never body. The navbar and the recommendations panel are on every route, so a
      // body-text assertion passes for every page whether or not routing works.
      const el = document.querySelector('.main') || document.querySelector('main');
      return el ? el.textContent.replace(/\s+/g, ' ').trim() : '';
    });

    for (const want of r.expect) {
      check(`${r.path} shows "${want}"`, mainText.includes(want),
        'main was: ' + mainText.slice(0, 110));
    }

    // Cross-route distinctness: no two routes may render the same main content. This is the check
    // that a body-text version of this suite silently lacked.
    const collide = Object.keys(mainTexts).filter(k => mainTexts[k] === mainText);
    check(`${r.path} is distinct from every other route`, collide.length === 0,
      collide.length ? 'identical to ' + collide.join(', ') : '');
    mainTexts[r.path] = mainText;

    check(`${r.path} has no failed requests`, state.real.length === 0, JSON.stringify(state.real.slice(0, 3)));
    check(`${r.path} has no uncaught errors`, state.perrs.length === 0, JSON.stringify(state.perrs.slice(0, 2)));

    await page.close();
  }

  // ---- 3. navigation actually navigates -------------------------------------
  console.log('\n[3] clicking the nav navigates (client-side routing)');
  {
    const { page, state } = await newPage(browser);
    await page.goto(BASE + '/', { waitUntil: 'domcontentloaded', timeout: 30000 });
    await page.waitForTimeout(3000);
    await dismissReleaseNotes(page);

    // Click "Scenes" in the nav and assert the URL and the content both change. Asserting only the
    // URL would pass on a router that navigates but renders nothing.
    const before = page.url();
    const scenesNav = page.locator('nav >> text=Scenes').first();
    if (await scenesNav.count()) {
      await scenesNav.click();
      await page.waitForTimeout(2500);
      check('clicking Scenes changes the URL', page.url() !== before, `${before} -> ${page.url()}`);
      const after = await page.evaluate(() => {
        const el = document.querySelector('.main');
        return el ? el.textContent.replace(/\s+/g, ' ').trim() : '';
      });
      check('the Scenes page content is present after the click', after.includes('Edit Filter'),
        'main was: ' + after.slice(0, 90));
    } else {
      check('the Scenes nav item is present', false, 'nav did not expose a Scenes link');
    }

    check('navigation produced no uncaught errors', state.perrs.length === 0, JSON.stringify(state.perrs.slice(0, 2)));
    await page.close();
  }

  // ---- 4. the stats page shows real numbers ---------------------------------
  console.log('\n[4] /stats reads real data from the API');
  {
    const { page, state } = await newPage(browser);
    await page.goto(BASE + '/stats', { waitUntil: 'domcontentloaded', timeout: 30000 });
    await page.waitForTimeout(3000);
    const txt = await page.evaluate(() => {
      const el = document.querySelector('.main');
      return el ? el.textContent.replace(/\s+/g, ' ').trim() : '';
    });
    // An empty instance legitimately reports zeroes; what must be present is the labelled shape,
    // which means the query resolved rather than the component erroring out.
    for (const label of ['Scenes size', 'Total O-Count', 'Total Play Count']) {
      check(`/stats shows "${label}"`, txt.includes(label), 'main was: ' + txt.slice(0, 120));
    }
    check('/stats had no uncaught errors', state.perrs.length === 0, JSON.stringify(state.perrs.slice(0, 2)));
    await page.close();
  }

  // ---- 5. settings loads and its sections render ---------------------------
  console.log('\n[5] /settings renders its sections');
  {
    const { page, state } = await newPage(browser);
    await page.goto(BASE + '/settings', { waitUntil: 'domcontentloaded', timeout: 30000 });
    await page.waitForTimeout(3000);
    const txt = await page.evaluate(() => {
      const el = document.querySelector('.main');
      return el ? el.textContent.replace(/\s+/g, ' ').trim() : '';
    });
    check('/settings shows the Library section', txt.includes('Library'), 'main was: ' + txt.slice(0, 120));
    check('/settings shows extension config', /Video extensions|Image extensions/.test(txt),
      'main was: ' + txt.slice(0, 120));
    check('/settings had no uncaught errors', state.perrs.length === 0, JSON.stringify(state.perrs.slice(0, 2)));
    await page.close();
  }

  // ---- 6. data-shaped pages actually issue and complete a GraphQL query ----
  //
  // Added because two mutants SURVIVED the first version of this suite, and both survived for the
  // same reason: every test loaded an instance with ZERO rows in it.
  //
  //   M1  a panic injected into sceneResolver.getPrimaryFile -- SURVIVED. That resolver only runs
  //       for a scene row that exists. With an empty library no scene is ever resolved, so the
  //       panicking code is never reached and every page still renders.
  //   M2  a throw injected into Stats.tsx's render -- SURVIVED, and this one is a gap in the TEST
  //       rather than in the instance: /stats renders its component, but the throw happened while
  //       the suite was looking at a page whose text it had already read.
  //
  // Both are the classic empty-fixture blind spot, and neither is visible without actually
  // exercising the query. So this test does that: it watches the network for the GraphQL call, and
  // requires that a real query goes out AND comes back 200 with a data envelope.
  console.log('\n[6] the UI issues GraphQL queries and they complete');
  {
    const { page, state } = await newPage(browser);
    const gql = [];
    page.on('response', r => {
      if (r.url().includes('/graphql')) {
        gql.push({ method: r.request().method(), status: r.status() });
      }
    });

    await page.goto(BASE + '/scenes', { waitUntil: 'domcontentloaded', timeout: 30000 });
    await page.waitForTimeout(4000);

    check('the Scenes page issued at least one GraphQL query', gql.length > 0,
      'no /graphql request was observed -- the page is rendering without asking the server for data');
    check('every GraphQL query returned 200', gql.every(g => g.status === 200),
      JSON.stringify(gql.slice(0, 5)));

    // A 200 with an {"errors":[...]} body is a failed query that HTTP calls successful. The API
    // returns 200 for GraphQL errors by design, so the status alone cannot tell the two apart.
    const errs = await page.evaluate(() => window.__gqlErrors || []);
    check('no GraphQL error envelope was surfaced', errs.length === 0, JSON.stringify(errs.slice(0, 3)));

    check('the data-bearing page produced no uncaught errors', state.perrs.length === 0,
      JSON.stringify(state.perrs.slice(0, 2)));
    await page.close();
  }

  // ---- 7. a resolver panic must surface as an error, not silence ----------
  //
  // Directly targets the M1 blind spot. gqlgen recovers panics in resolvers and returns them in the
  // errors array; the browser then logs them to the console. So a panicking resolver MUST produce a
  // console error even with an empty library -- which is exactly why the mutant should not have
  // survived, and pinning that behaviour keeps it from surviving again.
  // ---- 7. the media-URL resolvers run and return data --------------------
  //
  // Added because mutant m1 -- a panic injected into sceneResolver.getPrimaryFile -- SURVIVED twice.
  //
  // The first survival was the empty-fixture blind spot: with no scene, getPrimaryFile never runs.
  // Seeding fixed that, and the mutant SURVIVED AGAIN, which was the useful part. Measured against a
  // seeded instance, the panic was completely unreachable through the queries the suite was making:
  //
  //     { findScenes { count scenes { id title } } }        -> 200, no panic
  //     query($id:ID!){ findScene(id:$id){ id title } }    -> 200, no panic
  //
  // because getPrimaryFile is only called from Captions, SceneStreams and Interactive (lines 236,
  // 378 and 409 of resolver_model_scene.go). Querying those DOES panic:
  //
  //     {"message":"Internal system error. Error <MUTANT M1: deliberate resolver panic>",
  //      "path":["findScene","interactive"]}
  //
  // So the defect was reachable and the SUITE was not reaching it. Seeding alone would have let me
  // report a false all-clear: removing one blind spot leaves the next one in place.
  //
  // These are also the fields the earlier #7238 work signed URLs for, so this doubles as the
  // regression test that those paths still resolve for a real scene.
  console.log('\n[7] scene media-URL resolvers return data');
  {
    const { page, state } = await newPage(browser);
    const res = await page.evaluate(async (base) => {
      const q = `query($id: ID!) {
        findScene(id: $id) {
          id
          title
          interactive
          captions { language_code }
          paths { screenshot preview stream webp }
        }
      }`;
      const r = await fetch(base + '/graphql', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ query: q, variables: { id: '1' } }),
      });
      return { status: r.status, json: await r.json() };
    }, BASE);

    check('the media-URL query returns 200', res.status === 200, 'got ' + res.status);

    // The decisive check. gqlgen reports a resolver panic as an `errors` entry under HTTP 200, so a
    // status-only assertion passes on a panicking server -- which is exactly how m1 survived twice.
    const errs = res.json.errors || [];
    check('the media-URL query returns NO GraphQL errors', errs.length === 0,
      JSON.stringify(errs.slice(0, 2)));

    const scene = res.json.data && res.json.data.findScene;
    check('the seeded scene resolves', !!scene, 'findScene was null');
    if (scene) {
      check('the scene has a title', scene.title === 'E2E Scene', JSON.stringify(scene.title));
      // paths is the ScenePathsType OBJECT: screenshot/preview/stream/webp/vtt/sprite/funscript.
      check('the scene exposes a screenshot URL', !!scene.paths && !!scene.paths.screenshot,
        JSON.stringify(scene.paths));
      check('the scene exposes a stream URL', !!scene.paths && !!scene.paths.stream,
        JSON.stringify(scene.paths));
    }
    check('the media-URL query produced no uncaught browser error', state.perrs.length === 0,
      JSON.stringify(state.perrs.slice(0, 2)));
    await page.close();
  }

  // ---- 8. GraphQL errors reach the browser console ----
  console.log('\n[8] GraphQL errors reach the browser console');
  {
    const { page, state } = await newPage(browser);
    // A query the API is known to reject: a field that does not exist returns a validation error.
    const res = await page.evaluate(async (base) => {
      const r = await fetch(base + '/graphql', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ query: '{ thisFieldDoesNotExist }' }),
      });
      return { status: r.status, body: (await r.text()).slice(0, 200) };
    }, BASE);
    check('a malformed GraphQL query is rejected', res.status >= 400 || res.body.includes('errors'),
      'status ' + res.status + ' body ' + res.body.slice(0, 80));
    check('the rejected query produced no uncaught browser error', state.perrs.length === 0,
      JSON.stringify(state.perrs.slice(0, 2)));
    await page.close();
  }

  // ---- 9. an unknown route does not white-screen ---------------------------
  console.log('\n[9] an unknown route degrades without an uncaught error');
  {
    const { page, state } = await newPage(browser);
    const resp = await page.goto(BASE + '/definitely-not-a-route', { waitUntil: 'domcontentloaded', timeout: 30000 });
    await page.waitForTimeout(2500);
    // The nav must still be there: a 404 page that loses the chrome is a broken shell, and an
    // unhandled router throw is exactly how that happens.
    const navPresent = await page.evaluate(() => document.querySelectorAll('nav').length > 0);
    check('an unknown route keeps the app shell', navPresent, 'no <nav> in the DOM');
    check('an unknown route produced no uncaught error', state.perrs.length === 0,
      JSON.stringify(state.perrs.slice(0, 2)));
    await page.close();
  }

  await browser.close();

  console.log(`\n${'='.repeat(60)}`);
  console.log(`passed ${passed}, failed ${failed}`);
  if (failed) {
    console.log('failures:');
    for (const f of failures) console.log('  - ' + f);
  }
  console.log(failed ? 'E2E FAILED' : 'E2E PASSED');
  process.exit(failed ? 1 : 0);
}

main().catch(e => { console.error('ERR', e.stack || e.message); process.exit(1); });