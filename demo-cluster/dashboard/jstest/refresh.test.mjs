import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';

import { checkDashboard, durations, limiter, serverClock, tickClient } from '../web/js/refresh.mjs';

const pause = () => new Promise((resolve) => setImmediate(resolve));

// gate() is a task that runs until open(value) is called.
function gate() {
  let open;
  const done = new Promise((resolve) => {
    open = resolve;
  });
  return { open, task: () => done };
}

// startGated hands n gated tasks to limit and records which have started.
function startGated(limit, n) {
  const started = [];
  const gates = Array.from({ length: n }, gate);
  const runs = gates.map((g, index) =>
    limit(() => {
      started.push(index);
      return g.task();
    }),
  );
  return { started, gates, runs };
}

test('limiter starts a task only while fewer than max are running', async () => {
  const { started, gates, runs } = startGated(limiter(2), 4);
  await pause();
  assert.deepEqual(started, [0, 1]);

  gates[0].open('first');
  assert.equal(await runs[0], 'first');
  await pause();
  // One place came free, and it went to the task that waited longest.
  assert.deepEqual(started, [0, 1, 2]);

  gates.forEach((g) => g.open());
  await Promise.all(runs);
  assert.deepEqual(started, [0, 1, 2, 3]);
});

test('limiter passes a failure on and gives its place to the next task', async () => {
  const limit = limiter(1);
  const failed = limit(async () => {
    throw new Error('rejected');
  });
  const threw = limit(() => {
    throw new Error('thrown');
  });
  const fine = limit(async () => 'ok');

  await assert.rejects(failed, /rejected/);
  await assert.rejects(threw, /thrown/);
  assert.equal(await fine, 'ok');
});

test('limiter has every place free again when its tasks are done', async () => {
  const limit = limiter(2);
  await Promise.allSettled([1, 2, 3, 4, 5].map((n) => limit(async () => (n % 2 ? n : Promise.reject(new Error('odd one out'))))));

  const { started, gates, runs } = startGated(limit, 3);
  await pause();
  // Not fewer, which would mean a place was lost, and not more.
  assert.deepEqual(started, [0, 1]);
  gates.forEach((g) => g.open());
  await Promise.all(runs);
});

test('serverClock is the local clock until an answer says otherwise', () => {
  const clock = serverClock(() => 1000);
  assert.equal(clock.now(), 1000);

  // An empty vector carries no time.
  clock.note([]);
  clock.note([{ labels: {}, time: undefined, value: 1 }]);
  assert.equal(clock.now(), 1000);
});

test('serverClock follows the evaluation time of the answers', () => {
  let local = 1000;
  const clock = serverClock(() => local);

  // The cluster is 73 seconds behind.
  clock.note([{ labels: {}, time: 927, value: 1 }]);
  assert.equal(clock.now(), 927);

  // The difference holds until a new answer arrives.
  local = 1005;
  assert.equal(clock.now(), 932);
});

test('serverClock goes by the answer that was on its way for the shortest time', () => {
  let local = 1000;
  const clock = serverClock(() => local);

  // Arrived at once: the cluster is 10 seconds behind.
  clock.note([{ labels: {}, time: 990, value: 1 }]);
  // Evaluated half a second later, but 3 seconds on its way: it looks like 12.5.
  local = 1003;
  clock.note([{ labels: {}, time: 990.5, value: 1 }]);

  assert.equal(clock.now(), 1003 - 10);
});

test('serverClock measures again after every now()', () => {
  let local = 1000;
  const clock = serverClock(() => local);
  clock.note([{ labels: {}, time: 1000, value: 1 }]);
  assert.equal(clock.now(), 1000);

  // The cluster's clock fell a minute behind in the meantime. An earlier,
  // smaller difference must not outlive the tick it was measured in.
  local = 1100;
  clock.note([{ labels: {}, time: 1040, value: 1 }]);
  assert.equal(clock.now(), 1040);
});

test('serverClock says when the answers moved the clock under a window', () => {
  let local = 1000;
  const clock = serverClock(() => local);
  assert.equal(clock.moved(5), false);

  // A window is built on the browser's clock, then the answers say that the
  // cluster is 2 minutes behind: that window ended at the wrong moment.
  clock.now();
  clock.note([{ labels: {}, time: 880, value: 1 }]);
  assert.equal(clock.moved(5), true);

  // The next window uses the answer, and answers that agree with it within
  // a second do not count.
  clock.now();
  assert.equal(clock.moved(5), false);
  clock.note([{ labels: {}, time: 881, value: 1 }]);
  assert.equal(clock.moved(5), false);

  // A jump the other way counts as much: the cluster is now 89 s behind.
  local = 1010;
  clock.now();
  clock.note([{ labels: {}, time: 921, value: 1 }]);
  assert.equal(clock.moved(5), true);
  assert.equal(clock.moved(31), false);
});

// A Prometheus client stand-in that records what it was asked.
function fakeProm() {
  const calls = [];
  return {
    calls,
    range: async (expr, options) => {
      calls.push({ kind: 'range', expr, options });
      return [{ labels: {}, times: [], values: [] }];
    },
    instant: async (expr, options) => {
      calls.push({ kind: 'instant', expr, options });
      return [{ labels: {}, time: 500, value: 1 }];
    },
  };
}

// A limit that never makes a task wait.
const direct = (task) => task();
const minute = { start: 0, end: 60, step: 5 };

test('tickClient sends a query once, however many panels ask for it', async () => {
  const prom = fakeProm();
  const { signal } = new AbortController();
  const client = tickClient(prom, direct, minute, { signal, minStepSec: 5, clock: serverClock() });

  const [first, second] = await Promise.all([client.range('up'), client.range('up')]);
  await client.range('down');
  await client.instant('up');
  await client.instant('up');

  assert.equal(first, second);
  assert.deepEqual(prom.calls.map(({ kind, expr }) => `${kind} ${expr}`), ['range up', 'range down', 'instant up']);
});

test('tickClient asks range queries for its window and instant queries for now', async () => {
  const prom = fakeProm();
  const { signal } = new AbortController();
  const client = tickClient(prom, direct, minute, { signal, minStepSec: 5, clock: serverClock() });

  await client.range('up');
  await client.instant('up');

  assert.deepEqual(prom.calls[0].options, { start: 0, end: 60, step: 5, signal });
  // No time and no signal: Prometheus evaluates at its own now, and the
  // answer is still good when the tick is dropped.
  assert.equal(prom.calls[1].options, undefined);
});

test('tickClient is coarse when the step is longer than the scrape interval', () => {
  const withStep = (step) =>
    tickClient(fakeProm(), direct, { start: 0, end: 900, step }, { signal: null, minStepSec: 5, clock: serverClock() });

  assert.equal(withStep(5).coarse, false);
  assert.equal(withStep(15).coarse, true);
});

test('tickClient tells the clock when an instant query was evaluated', async () => {
  const clock = serverClock(() => 1000);
  const client = tickClient(fakeProm(), direct, minute, { signal: null, minStepSec: 5, clock });

  // The stand-in answers with time 500.
  await client.instant('up');

  assert.equal(clock.now(), 500);
});

test('tickClient runs its queries through the limit', async () => {
  const held = [];
  const limit = (task) => new Promise((resolve) => held.push(() => resolve(task())));
  const prom = fakeProm();
  const client = tickClient(prom, limit, minute, { signal: null, minStepSec: 5, clock: serverClock() });

  const pending = [client.range('up'), client.instant('up')];
  assert.equal(prom.calls.length, 0);

  held.forEach((release) => release());
  await Promise.all(pending);
  assert.equal(prom.calls.length, 2);
});

test('durations adds the default of the file, shortest first', () => {
  const values = (offered, fromFile) => durations(offered, fromFile).map(({ value }) => value);

  assert.deepEqual(values(['2m', '5m', '1h'], '3m'), ['2m', '3m', '5m', '1h']);
  assert.deepEqual(values(['5s', '1m'], '5s'), ['5s', '1m']);
  assert.deepEqual(values(['5s', '1m'], '90s'), ['5s', '1m', '90s']);
  assert.deepEqual(durations(['5s'], '5s'), [{ value: '5s', text: '5s' }]);
});

const shipped = JSON.parse(readFileSync(new URL('../web/dashboard.json', import.meta.url), 'utf8'));

test('checkDashboard accepts the panel file the page ships with', () => {
  assert.equal(checkDashboard(shipped), shipped);
});

test('checkDashboard names the first thing the page cannot work without', () => {
  // change edits a copy of the shipped file, or returns what to check instead.
  const refuses = (change, pattern) => {
    const copy = structuredClone(shipped);
    const replaced = change(copy);
    assert.throws(() => checkDashboard(replaced === undefined ? copy : replaced), pattern);
  };

  refuses(() => null, /not an object/);
  refuses(() => 'text', /not an object/);
  // An array is an object to typeof; it is still turned away.
  refuses(() => [], /version undefined/);
  refuses((file) => { file.version = 2; }, /version 2, and this page reads version 1/);
  refuses((file) => { delete file.title; }, /no title/);
  refuses((file) => { file.range = '2 minutes'; }, /bad duration "2 minutes"/);
  refuses((file) => { delete file.minStep; }, /no minStep/);
  refuses((file) => { file.palette = null; }, /no palette/);
  refuses((file) => { delete file.topology; }, /no topology/);
  refuses((file) => { file.panels = {}; }, /no panels/);
  refuses((file) => { file.panels[2] = null; }, /panel 3 needs an id and a title/);
  refuses((file) => { delete file.panels[2].id; }, /panel 3 needs an id and a title/);
  refuses((file) => { file.panels[3].grid = { x: 0, y: 0, w: '6', h: 4 }; }, /panel "restarts" needs a grid/);
  refuses((file) => { delete file.panels[3].grid; }, /panel "restarts" needs a grid/);
  refuses((file) => { file.panels[4].queries = []; }, /panel "health_score" needs queries/);
  refuses((file) => { file.panels[5].queries[1] = { legend: 'x' }; }, /panel "throughput" needs queries/);
  refuses((file) => { file.panels[5].queries = 'x'; }, /panel "throughput" needs queries/);
});
