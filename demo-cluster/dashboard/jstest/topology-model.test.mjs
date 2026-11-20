import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';

import {
  deriveTopology,
  describeFailures,
  edgeWords,
  routeWires,
  topologyQueries,
} from '../web/js/topology-model.mjs';

// A topology like the one in dashboard.json, with short expressions: two
// apps that get traffic (one of them with a sidecar) and one that does not.

const app = (id, more) => ({
  id,
  label: id,
  icon: 'catalog',
  ready: `ready ${id}`,
  waiting: `waiting ${id}`,
  oomKilledNow: `oomKilledNow ${id}`,
  oomKilled: `oomKilled ${id}`,
  memory: `memory ${id}`,
  restarts: `restarts ${id}`,
  ...more,
});

const spec = {
  title: 'Demo apps',
  namespace: 'demo-apps',
  source: { id: 'loadgen', label: 'Traffic', sub: 'loadgen', icon: 'traffic', ready: 'ready loadgen' },
  nodes: [
    app('payments-api', {
      sub: 'Go · 128Mi',
      traffic: { rate: 'rate payments-api', errors: 'errors payments-api' },
      ports: ['metrics', 'scan'],
      metricsUp: 'up payments-api',
      fault: { badge: 'F1', name: 'OOMKill', active: 'fault payments-api', trigger: './scripts/trigger-oomkill.sh' },
    }),
    app('recommend-svc', {
      sub: 'Python · 128Mi + sidecar',
      traffic: { rate: 'rate recommend-svc', errors: 'errors recommend-svc' },
      ports: ['metrics', 'scan'],
      metricsUp: 'up recommend-svc',
      fault: {
        badge: 'F2',
        name: 'memory leak',
        active: 'fault recommend-svc',
        trigger: './scripts/trigger-slowleak.sh',
      },
    }),
    app('catalog-svc', { sub: 'nginx · 64Mi', ports: ['scan'] }),
  ],
  ports: {
    metrics: { legend: 'Prometheus scrapes /metrics every 5s' },
    scan: { legend: 'K8sGPT scans the pods about every 30s', up: 'up k8sgpt' },
  },
};

// The three answers a query can give.
const value = (number) => ({ samples: [{ labels: {}, value: number }] });
const EMPTY = { samples: [] };
const FAILED = { error: 'the dashboard server is unreachable' };

// One sample per container, as the waiting and oomKilled queries return them.
const containers = (...samples) => ({
  samples: samples.map(([container, reason]) => ({ labels: { container, reason }, value: 1 })),
});

// The answers of a healthy cluster: every pod ready, loadgen sending 2.07
// requests per second to each of the two apps, nothing wrong.
function healthy() {
  const answers = new Map([
    ['ready loadgen', value(1)],
    ['up k8sgpt', value(1)],
  ]);
  for (const node of spec.nodes) {
    answers.set(node.ready, value(1));
    answers.set(node.waiting, EMPTY);
    answers.set(node.oomKilledNow, EMPTY);
    answers.set(node.oomKilled, EMPTY);
    answers.set(node.memory, value(16.4));
    answers.set(node.restarts, value(0));
    if (node.traffic) {
      answers.set(node.traffic.rate, value(2.07));
      answers.set(node.traffic.errors, value(0));
      answers.set(node.metricsUp, value(1));
      answers.set(node.fault.active, value(0));
    }
  }
  return answers;
}

// derive() is the healthy cluster with some answers replaced. The model lists
// the nodes in the order of the spec.
const derive = (changes = {}) => deriveTopology(spec, new Map([...healthy(), ...Object.entries(changes)]));
const payments = (changes) => derive(changes).nodes[0];
const recommend = (changes) => derive(changes).nodes[1];
const catalog = (changes) => derive(changes).nodes[2];
const statusOf = ({ status, sub }) => ({ status, sub });

// Queries

test('topologyQueries lists every expression once', () => {
  const exprs = topologyQueries(spec);
  assert.deepEqual([...exprs].sort(), [...healthy().keys()].sort());
  // The scan port's expression is shared by the three nodes.
  assert.equal(exprs.filter((expr) => expr === 'up k8sgpt').length, 1);
});

test('describeFailures counts the failed queries and gives the first reason', () => {
  assert.equal(describeFailures(healthy()), null);
  assert.equal(describeFailures(new Map()), null);
  const results = new Map([
    ['a', value(1)],
    ['b', { error: 'query timed out after 10s' }],
    ['c', EMPTY],
    ['d', FAILED],
  ]);
  assert.equal(describeFailures(results), '2 of 4 topology queries failed: query timed out after 10s');
});

// Node status

test('a healthy node shows its own sub-line', () => {
  assert.deepEqual(statusOf(payments()), { status: 'healthy', sub: 'Go · 128Mi' });
  assert.deepEqual(statusOf(catalog()), { status: 'healthy', sub: 'nginx · 64Mi' });
});

test('a node is unknown when its readiness query failed, returned nothing or was not asked', () => {
  const unknown = { status: 'unknown', sub: 'no data' };
  assert.deepEqual(statusOf(payments({ 'ready payments-api': FAILED })), unknown);
  assert.deepEqual(statusOf(payments({ 'ready payments-api': EMPTY })), unknown);
  // A value Prometheus could not give as a number (NaN) is no answer either.
  assert.deepEqual(statusOf(payments({ 'ready payments-api': value(null) })), unknown);

  const notAsked = healthy();
  notAsked.delete('ready payments-api');
  assert.deepEqual(statusOf(deriveTopology(spec, notAsked).nodes[0]), unknown);
});

test('unknown wins over every other signal', () => {
  const node = payments({
    'ready payments-api': FAILED,
    'waiting payments-api': containers(['payments-api', 'CrashLoopBackOff']),
    'memory payments-api': value(90),
  });
  assert.deepEqual(statusOf(node), { status: 'unknown', sub: 'no data' });
});

test('a waiting reason other than starting is an error, even while the pod counts as ready', () => {
  for (const reason of ['CrashLoopBackOff', 'ImagePullBackOff', 'CreateContainerConfigError']) {
    const node = payments({ 'waiting payments-api': containers(['payments-api', reason]) });
    assert.deepEqual(statusOf(node), { status: 'error', sub: reason });
  }
});

test('a node that is not ready is an error', () => {
  const notReady = { 'ready payments-api': value(0) };
  assert.deepEqual(statusOf(payments(notReady)), { status: 'error', sub: 'not ready' });
});

test('the sub-line of an error is the waiting reason, else OOMKilled, else "not ready"', () => {
  const notReady = { 'ready payments-api': value(0) };
  const killed = { 'oomKilled payments-api': containers(['payments-api', 'OOMKilled']) };
  const waiting = { 'waiting payments-api': containers(['payments-api', 'CrashLoopBackOff']) };

  assert.equal(payments({ ...notReady, ...killed, ...waiting }).sub, 'CrashLoopBackOff');
  assert.equal(payments({ ...notReady, ...killed }).sub, 'OOMKilled');
  assert.equal(payments(notReady).sub, 'not ready');
  // A pod that is not ready because it is still starting says so.
  const creating = { 'waiting payments-api': containers(['payments-api', 'ContainerCreating']) };
  assert.deepEqual(statusOf(payments({ ...notReady, ...creating })), { status: 'error', sub: 'ContainerCreating' });
});

test('a failing container is shown before one that is only starting', () => {
  const node = recommend({
    'waiting recommend-svc': containers(['recommend-svc', 'PodInitializing'], ['leak-fixture', 'CrashLoopBackOff']),
  });
  assert.deepEqual(statusOf(node), { status: 'error', sub: 'CrashLoopBackOff (leak-fixture)' });
});

test('a waiting sample without a reason still reads as waiting', () => {
  const node = payments({ 'waiting payments-api': { samples: [{ labels: { container: 'payments-api' }, value: 1 }] } });
  assert.deepEqual(statusOf(node), { status: 'error', sub: 'waiting' });
});

test('a ready node with a starting container is degraded: starting', () => {
  for (const reason of ['ContainerCreating', 'PodInitializing']) {
    const node = payments({ 'waiting payments-api': containers(['payments-api', reason]) });
    assert.deepEqual(statusOf(node), { status: 'degraded', sub: 'starting' });
  }
});

test('a ready node that was OOMKilled moments ago is degraded: OOMKilled, restarted', () => {
  const node = payments({ 'oomKilled payments-api': containers(['payments-api', 'OOMKilled']) });
  assert.deepEqual(statusOf(node), { status: 'degraded', sub: 'OOMKilled, restarted' });
});

test('a container that is down after an OOMKill is not said to have restarted', () => {
  const down = { 'oomKilledNow recommend-svc': containers(['leak-fixture', 'OOMKilled']) };
  const restarted = { 'oomKilled recommend-svc': containers(['leak-fixture', 'OOMKilled']) };

  // The pod cannot serve: an error that names the kill, not just "not ready".
  const notReady = { 'ready recommend-svc': value(0) };
  assert.deepEqual(statusOf(recommend({ ...notReady, ...down })), {
    status: 'error',
    sub: 'OOMKilled (leak-fixture)',
  });
  // Another pod of the app still serves, as during a rollout.
  assert.deepEqual(statusOf(recommend(down)), { status: 'degraded', sub: 'OOMKilled (leak-fixture)' });
  // Down now says more than restarted a moment ago.
  assert.deepEqual(statusOf(recommend({ ...down, ...restarted })), {
    status: 'degraded',
    sub: 'OOMKilled (leak-fixture)',
  });
  // A failed or empty answer makes no node look killed.
  assert.deepEqual(statusOf(recommend({ 'oomKilledNow recommend-svc': FAILED })), {
    status: 'healthy',
    sub: 'Python · 128Mi + sidecar',
  });
});

test('memory at half the limit or more is degraded', () => {
  const at = (percent) => statusOf(payments({ 'memory payments-api': value(percent) }));
  assert.deepEqual(at(49.9), { status: 'healthy', sub: 'Go · 128Mi' });
  assert.deepEqual(at(50), { status: 'degraded', sub: 'memory 50% of limit' });
  assert.deepEqual(at(60.7), { status: 'degraded', sub: 'memory 60% of limit' });
  assert.deepEqual(at(131), { status: 'degraded', sub: 'memory 131% of limit' });
});

test('a restart in the last five minutes is degraded', () => {
  const at = (restarts) => statusOf(payments({ 'restarts payments-api': value(restarts) }));
  assert.deepEqual(at(0.4), { status: 'healthy', sub: 'Go · 128Mi' });
  // increase() extrapolates: one restart comes back as a little over 1.
  assert.deepEqual(at(1.017), { status: 'degraded', sub: '1 restart in 5m' });
  assert.deepEqual(at(3.05), { status: 'degraded', sub: '3 restarts in 5m' });
});

test('an active fault with no other sign is degraded: the name of the fault', () => {
  assert.deepEqual(statusOf(recommend({ 'fault recommend-svc': value(1) })), {
    status: 'degraded',
    sub: 'memory leak',
  });
});

test('the first reason to be degraded that applies is the one shown', () => {
  const reasons = [
    [{ 'waiting recommend-svc': containers(['recommend-svc', 'ContainerCreating']) }, 'starting'],
    [{ 'oomKilled recommend-svc': containers(['recommend-svc', 'OOMKilled']) }, 'OOMKilled, restarted'],
    [{ 'memory recommend-svc': value(72) }, 'memory 72% of limit'],
    [{ 'restarts recommend-svc': value(2) }, '2 restarts in 5m'],
    [{ 'fault recommend-svc': value(1) }, 'memory leak'],
  ];
  reasons.forEach(([, expected], first) => {
    const changes = Object.assign({}, ...reasons.slice(first).map(([change]) => change));
    assert.deepEqual(statusOf(recommend(changes)), { status: 'degraded', sub: expected });
  });
});

test('a container that is not the app itself is named', () => {
  const sidecarKilled = { 'oomKilled recommend-svc': containers(['leak-fixture', 'OOMKilled']) };
  assert.equal(recommend(sidecarKilled).sub, 'OOMKilled, restarted (leak-fixture)');
  assert.equal(recommend({ ...sidecarKilled, 'ready recommend-svc': value(0) }).sub, 'OOMKilled (leak-fixture)');
  assert.equal(
    recommend({ 'waiting recommend-svc': containers(['leak-fixture', 'ContainerCreating']) }).sub,
    'starting (leak-fixture)',
  );
  // catalog-svc runs a container called nginx.
  assert.equal(catalog({ 'waiting catalog-svc': containers(['nginx', 'ImagePullBackOff']) }).sub, 'ImagePullBackOff (nginx)');
});

test('when several containers are affected the app itself is shown, else the first by name', () => {
  const both = containers(['leak-fixture', 'CrashLoopBackOff'], ['recommend-svc', 'ImagePullBackOff']);
  assert.equal(recommend({ 'waiting recommend-svc': both }).sub, 'ImagePullBackOff');
  const sidecars = containers(['zeta', 'OOMKilled'], ['alpha', 'OOMKilled']);
  assert.equal(recommend({ 'oomKilled recommend-svc': sidecars }).sub, 'OOMKilled, restarted (alpha)');
});

test('a missing optional signal is not evidence: the node stays healthy', () => {
  for (const answer of [FAILED, EMPTY]) {
    const node = payments({
      'waiting payments-api': answer,
      'oomKilled payments-api': answer,
      'memory payments-api': answer,
      'restarts payments-api': answer,
      'fault payments-api': answer,
      'errors payments-api': answer,
    });
    assert.deepEqual(statusOf(node), { status: 'healthy', sub: 'Go · 128Mi' });
    assert.equal(node.title, 'payments-api: healthy');
    assert.equal(node.fault.state, 'idle');
    assert.deepEqual(node.edge, { state: 'flow', errors: 'none', label: '2.07 req/s' });
  }
});

// Node titles

test('a node title gives the status, then memory and restarts', () => {
  assert.equal(payments().title, 'payments-api: healthy · memory 16% of limit · 0 restarts in 5m');
});

test('a node title gives the reason of every status but healthy, once', () => {
  assert.equal(
    payments({ 'waiting payments-api': containers(['payments-api', 'CrashLoopBackOff']), 'restarts payments-api': value(4.07) }).title,
    'payments-api: error · CrashLoopBackOff · memory 16% of limit · 4 restarts in 5m',
  );
  assert.equal(
    recommend({ 'oomKilled recommend-svc': containers(['leak-fixture', 'OOMKilled']), 'restarts recommend-svc': value(1) }).title,
    'recommend-svc: degraded · OOMKilled, restarted (leak-fixture) · memory 16% of limit · 1 restart in 5m',
  );
  // The reason is one of the facts here, and is not said twice.
  assert.equal(
    recommend({ 'memory recommend-svc': value(60) }).title,
    'recommend-svc: degraded · memory 60% of limit · 0 restarts in 5m',
  );
  assert.equal(
    recommend({ 'restarts recommend-svc': value(1) }).title,
    'recommend-svc: degraded · memory 16% of limit · 1 restart in 5m',
  );
  assert.equal(
    payments({ 'ready payments-api': FAILED }).title,
    'payments-api: unknown · no data · memory 16% of limit · 0 restarts in 5m',
  );
});

// Fault badges

test('a fault badge is active while its query returns more than 0', () => {
  assert.deepEqual(payments({ 'fault payments-api': value(2) }).fault, {
    state: 'active',
    title: 'F1 OOMKill: active',
  });
  const idle = { state: 'idle', title: 'F1 OOMKill: idle. Trigger it with ./scripts/trigger-oomkill.sh' };
  assert.deepEqual(payments().fault, idle);
  assert.deepEqual(payments({ 'fault payments-api': EMPTY }).fault, idle);
  assert.deepEqual(payments({ 'fault payments-api': FAILED }).fault, idle);
  assert.equal(recommend({ 'fault recommend-svc': value(1) }).fault.title, 'F2 memory leak: active');
});

test('a node without a fault has no badge', () => {
  assert.equal(catalog().fault, null);
});

// Traffic edges

test('traffic flows while the source runs and the rate is above 0.05', () => {
  assert.deepEqual(payments().edge, { state: 'flow', errors: 'none', label: '2.07 req/s' });
  assert.deepEqual(payments({ 'rate payments-api': value(0.06) }).edge, {
    state: 'flow',
    errors: 'none',
    label: '0.06 req/s',
  });
});

test('the share of 5xx answers tints the flow from 1 percent and from 50 percent', () => {
  const at = (percent) => payments({ 'errors payments-api': value(percent) }).edge.errors;
  assert.equal(at(0.99), 'none');
  assert.equal(at(1), 'mid');
  assert.equal(at(3), 'mid');
  assert.equal(at(49.9), 'mid');
  assert.equal(at(50), 'high');
  assert.equal(at(60), 'high');
});

test('an edge is idle without traffic or without a running source', () => {
  const idle = { state: 'idle', errors: 'none', label: '' };
  assert.deepEqual(payments({ 'rate payments-api': value(0.05) }).edge, idle);
  assert.deepEqual(payments({ 'rate payments-api': value(0) }).edge, idle);
  assert.deepEqual(payments({ 'rate payments-api': FAILED }).edge, idle);
  assert.deepEqual(payments({ 'rate payments-api': EMPTY }).edge, idle);
  assert.deepEqual(payments({ 'ready loadgen': value(0) }).edge, idle);
  assert.deepEqual(payments({ 'ready loadgen': FAILED }).edge, idle);
  // Errors colour a flow. Without one they show nowhere.
  assert.deepEqual(payments({ 'ready loadgen': value(0), 'errors payments-api': value(80) }).edge, idle);
});

test('the edge into a node in error is blocked, whatever the traffic', () => {
  const blocked = { state: 'blocked', errors: 'none', label: '✗ down' };
  const crashing = { 'waiting payments-api': containers(['payments-api', 'CrashLoopBackOff']) };
  assert.deepEqual(payments(crashing).edge, blocked);
  assert.deepEqual(payments({ ...crashing, 'errors payments-api': value(100) }).edge, blocked);
  assert.deepEqual(payments({ ...crashing, 'ready loadgen': FAILED }).edge, blocked);
  assert.deepEqual(payments({ 'ready payments-api': value(0), 'rate payments-api': value(0) }).edge, blocked);
});

test('a degraded or unknown node keeps its traffic', () => {
  assert.equal(payments({ 'memory payments-api': value(80) }).edge.state, 'flow');
  assert.equal(payments({ 'ready payments-api': FAILED }).edge.state, 'flow');
});

test('a node that gets no traffic has no edge', () => {
  assert.equal(catalog().edge, null);
});

// Traffic source

test('a running source shows the sum of the rates, with one decimal', () => {
  assert.deepEqual(derive().source, {
    state: 'running',
    errors: 'none',
    sub: '4.1 req/s',
    title: 'Traffic (loadgen): running · 4.1 req/s · payments-api: 2.07 req/s, 0% 5xx · recommend-svc: 2.07 req/s, 0% 5xx',
  });
  assert.equal(derive({ 'rate payments-api': value(2), 'rate recommend-svc': value(2) }).source.sub, '4.0 req/s');
  // A rate that is not known is left out of the sum.
  const partly = derive({ 'rate recommend-svc': FAILED, 'errors payments-api': EMPTY }).source;
  assert.equal(partly.sub, '2.1 req/s');
  assert.equal(partly.title, 'Traffic (loadgen): running · 2.1 req/s · payments-api: 2.07 req/s · recommend-svc: no data');
});

test('a running source with no known rate says that it runs', () => {
  const source = derive({ 'rate payments-api': FAILED, 'rate recommend-svc': EMPTY }).source;
  assert.equal(source.state, 'running');
  assert.equal(source.sub, 'running');
  assert.equal(source.title, 'Traffic (loadgen): running · payments-api: no data · recommend-svc: no data');
});

test('a running source takes the worst error level of its edges', () => {
  assert.equal(derive({ 'errors payments-api': value(3) }).source.errors, 'mid');
  assert.equal(derive({ 'errors payments-api': value(3), 'errors recommend-svc': value(60) }).source.errors, 'high');
  // A blocked edge carries no flow, so its errors do not count.
  const crashing = { 'waiting payments-api': containers(['payments-api', 'CrashLoopBackOff']) };
  assert.equal(derive({ ...crashing, 'errors payments-api': value(100) }).source.errors, 'none');
});

test('a source that is not ready is idle', () => {
  assert.deepEqual(derive({ 'ready loadgen': value(0), 'errors payments-api': value(60) }).source, {
    state: 'idle',
    errors: 'none',
    sub: 'idle',
    title: 'Traffic (loadgen): idle · no pod is ready',
  });
});

test('a source whose readiness query failed or returned nothing is unknown', () => {
  const unknown = { state: 'unknown', errors: 'none', sub: 'no data', title: 'Traffic (loadgen): no data' };
  assert.deepEqual(derive({ 'ready loadgen': FAILED }).source, unknown);
  assert.deepEqual(derive({ 'ready loadgen': EMPTY }).source, unknown);
});

test('a source without a sub-line of its own is named by its label', () => {
  const plain = { ...spec, source: { ...spec.source, sub: undefined } };
  assert.equal(deriveTopology(plain, new Map()).source.title, 'Traffic: no data');
});

// Ports

test('the metrics port follows the scrape of its own node', () => {
  const port = (changes) => payments(changes).ports[0];
  assert.deepEqual(port(), { kind: 'metrics', state: 'up', title: 'Metrics: Prometheus scrapes /metrics every 5s · up' });
  assert.deepEqual(port({ 'up payments-api': value(0) }), {
    kind: 'metrics',
    state: 'down',
    title: 'Metrics: Prometheus scrapes /metrics every 5s · down',
  });
  for (const answer of [FAILED, EMPTY]) {
    assert.deepEqual(port({ 'up payments-api': answer }), {
      kind: 'metrics',
      state: 'unknown',
      title: 'Metrics: Prometheus scrapes /metrics every 5s · no data',
    });
  }
  // The other node's port does not move.
  assert.equal(recommend({ 'up payments-api': value(0) }).ports[0].state, 'up');
});

test('the scan port of every node follows one query', () => {
  const scanStates = (changes) => derive(changes).nodes.map((node) => node.ports.at(-1).state);
  assert.deepEqual(scanStates(), ['up', 'up', 'up']);
  assert.deepEqual(scanStates({ 'up k8sgpt': value(0) }), ['down', 'down', 'down']);
  assert.deepEqual(scanStates({ 'up k8sgpt': FAILED }), ['unknown', 'unknown', 'unknown']);
  assert.equal(catalog().ports[0].title, 'Scan: K8sGPT scans the pods about every 30s · up');
  assert.equal(catalog({ 'up k8sgpt': value(0) }).ports[0].title, 'Scan: K8sGPT scans the pods about every 30s · down');
});

test('the legend says for each kind of port whether any is up', () => {
  assert.deepEqual(derive().ports, [
    { kind: 'metrics', up: true },
    { kind: 'scan', up: true },
  ]);
  assert.deepEqual(derive({ 'up payments-api': value(0), 'up k8sgpt': value(0) }).ports, [
    { kind: 'metrics', up: true },
    { kind: 'scan', up: false },
  ]);
  assert.deepEqual(derive({ 'up payments-api': value(0), 'up recommend-svc': FAILED }).ports, [
    { kind: 'metrics', up: false },
    { kind: 'scan', up: true },
  ]);
});

// The whole model

test('with no answer at all everything is unknown, and nothing looks broken or healthy', () => {
  for (const results of [new Map(), new Map([...healthy().keys()].map((expr) => [expr, FAILED]))]) {
    const model = deriveTopology(spec, results);
    assert.deepEqual(model.source, {
      state: 'unknown',
      errors: 'none',
      sub: 'no data',
      title: 'Traffic (loadgen): no data',
    });
    assert.deepEqual(model.nodes.map(statusOf), Array(3).fill({ status: 'unknown', sub: 'no data' }));
    assert.deepEqual(
      model.nodes.map((node) => node.title),
      ['payments-api: unknown · no data', 'recommend-svc: unknown · no data', 'catalog-svc: unknown · no data'],
    );
    assert.deepEqual(model.nodes.map((node) => node.fault?.state ?? null), ['idle', 'idle', null]);
    assert.deepEqual(model.nodes.map((node) => node.edge?.state ?? null), ['idle', 'idle', null]);
    assert.ok(model.nodes.every((node) => node.ports.every((port) => port.state === 'unknown')));
    assert.deepEqual(model.ports, [
      { kind: 'metrics', up: false },
      { kind: 'scan', up: false },
    ]);
  }
});

test('only the value 1 says ready or up', () => {
  // The queries answer 1 or 0. Any other number is read as "not".
  assert.deepEqual(statusOf(payments({ 'ready payments-api': value(0.5) })), { status: 'error', sub: 'not ready' });
  assert.equal(derive({ 'ready loadgen': value(2) }).source.state, 'idle');
  assert.equal(payments({ 'up payments-api': value(0.5) }).ports[0].state, 'down');
  assert.equal(catalog({ 'up k8sgpt': value(2) }).ports[0].state, 'down');
});

test('the topology of dashboard.json has the shape these rules read', () => {
  const file = new URL('../web/dashboard.json', import.meta.url);
  const { topology } = JSON.parse(readFileSync(file, 'utf8'));
  const exprs = topologyQueries(topology);
  // 1 for the source, 6 per node, 4 more per node with traffic (rate, errors,
  // scrape and fault), 1 for the scan port.
  const fed = topology.nodes.filter((node) => node.traffic).length;
  assert.equal(exprs.length, 1 + 6 * topology.nodes.length + 4 * fed + 1);

  const model = deriveTopology(topology, new Map(exprs.map((expr) => [expr, value(1)])));
  assert.equal(model.source.state, 'running');
  assert.deepEqual(model.nodes.map((node) => node.ports.map((port) => port.state)), [
    ['up', 'up'],
    ['up', 'up'],
    ['up'],
  ]);
  assert.deepEqual(model.ports.map((port) => port.kind), Object.keys(topology.ports));
});

// Wires
//
// The layout of the card, in round numbers: the source on the left, two
// nodes stacked beside it, a third node alone on the right. Each node is 170
// by 105.

const SOURCE = { left: 100, right: 188, top: 86, bottom: 158 };
const UPPER = { left: 284, right: 454, top: 0, bottom: 105 };
const LOWER = { left: 284, right: 454, top: 139, bottom: 244 };
const ASIDE = { left: 550, right: 720, top: 70, bottom: 175 };

const layout = (more = {}) => ({
  source: SOURCE,
  nodes: [
    { box: UPPER, fed: true },
    { box: LOWER, fed: true },
    { box: ASIDE, fed: false },
  ],
  ...more,
});

test('the traffic leaves the source in one stem that stops a pixel short of the fork', () => {
  // The fork is a third of the way from the source to the nodes.
  assert.equal(routeWires(layout()).stem, 'M188,122 H219');
});

test('each edge runs from the fork into the middle of its node, where the arrowhead touches it', () => {
  const { edges } = routeWires(layout());
  assert.deepEqual(edges, [
    {
      line: 'M220,122 V53 H279',
      head: 'M284,53 l-5,-5 v10 z',
      label: { x: 252, y: 44 },
      motion: 'M188,122 H220 V53 H279',
    },
    {
      line: 'M220,122 V192 H279',
      head: 'M284,192 l-5,-5 v10 z',
      label: { x: 252, y: 183 },
      motion: 'M188,122 H220 V192 H279',
    },
  ]);
});

test('without a node that gets traffic there is no stem and no edge', () => {
  const nodes = layout().nodes.map((node) => ({ ...node, fed: false }));
  const { stem, edges } = routeWires(layout({ nodes }));
  assert.equal(stem, '');
  assert.deepEqual(edges, []);
});

test('the link runs out of the right side of the fed nodes into the left side of the other node', () => {
  // A run out of the middle of each fed node's right side, a spine halfway
  // across the gap, and a run into the middle of the other node's left side.
  assert.equal(routeWires(layout()).link, 'M454,53 H502 M454,192 H502 M502,53 V192 M502,123 H550');
});

test('the spine reaches a node beyond the fed nodes, above or below them', () => {
  const low = { ...ASIDE, top: 230, bottom: 335 };
  const nodes = [...layout().nodes.slice(0, 2), { box: low, fed: false }];
  assert.equal(routeWires(layout({ nodes })).link, 'M454,53 H502 M454,192 H502 M502,53 V283 M502,283 H550');
});

test('without a fed node or without another node there is no link', () => {
  const [upper, lower, aside] = layout().nodes;
  assert.equal(routeWires(layout({ nodes: [upper, lower] })).link, '');
  assert.equal(routeWires(layout({ nodes: [{ ...aside }] })).link, '');
});

test('edgeWords says what an edge shows', () => {
  assert.equal(edgeWords({ state: 'flow', errors: 'none', label: '2.07 req/s' }), 'traffic 2.07 req/s');
  assert.equal(
    edgeWords({ state: 'flow', errors: 'mid', label: '2.07 req/s' }),
    'traffic 2.07 req/s, some answers are 5xx',
  );
  assert.equal(
    edgeWords({ state: 'flow', errors: 'high', label: '1 req/s' }),
    'traffic 1 req/s, at least half of the answers are 5xx',
  );
  assert.equal(edgeWords({ state: 'blocked', errors: 'none', label: '✗ down' }), 'traffic blocked');
  assert.equal(edgeWords({ state: 'idle', errors: 'none', label: '' }), 'no traffic');
});

test('edgeWords has words for every edge the model derives', () => {
  const spec = JSON.parse(readFileSync(new URL('../web/dashboard.json', import.meta.url), 'utf8')).topology;
  for (const node of deriveTopology(spec, new Map()).nodes) {
    if (node.edge) assert.equal(edgeWords(node.edge), 'no traffic');
  }
});
