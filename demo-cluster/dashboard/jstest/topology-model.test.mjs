import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';

import { hasIcon } from '../web/js/icons.mjs';
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

const shipped = () => JSON.parse(readFileSync(new URL('../web/dashboard.json', import.meta.url), 'utf8')).topology;

test('the topology of dashboard.json has the shape these rules read', () => {
  const topology = shipped();
  const exprs = topologyQueries(topology);
  // 1 for the source, 6 per node, 4 more per node with traffic (rate, errors,
  // scrape and fault), 1 for the scan port. Then the control plane: 8 for
  // Prometheus, 10 for K8sGPT (its Scan stage asks what the scan port asks,
  // and its Results need what a row of Prometheus asks), 4 for Slack and 4
  // for the links, which share the relay's with Slack.
  const fed = topology.nodes.filter((node) => node.traffic).length;
  assert.equal(exprs.length, 1 + 6 * topology.nodes.length + 4 * fed + 1 + 8 + 10 + 4 + 4);

  const model = deriveTopology(topology, new Map(exprs.map((expr) => [expr, value(1)])));
  assert.equal(model.source.state, 'running');
  assert.deepEqual(model.nodes.map((node) => node.ports.map((port) => port.state)), [
    ['up', 'up'],
    ['up', 'up'],
    ['up'],
  ]);
  assert.deepEqual(model.ports.map((port) => port.kind), Object.keys(topology.ports));
});

// Control plane
//
// A control plane like the one in dashboard.json, with short expressions:
// Prometheus reads the metrics ports, K8sGPT the scan ports, and Slack gets
// what the two find.

const plane = {
  title: 'Control plane',
  note: 'Both reach Slack through slack-relay.',
  boxes: [
    {
      id: 'prometheus',
      label: 'Prometheus',
      sub: 'monitoring',
      icon: 'prometheus',
      reads: 'metrics',
      access: { label: 'read-only', title: 'Prometheus only reads.' },
      stages: [
        { id: 'scrape', label: 'Scrape', title: 'every 5s', up: 'up scrape' },
        { id: 'alert', label: 'Alert', activeText: 'firing', active: 'alert firing' },
      ],
      rows: [
        {
          id: 'targets',
          label: 'App targets',
          text: 'two apps',
          activeText: 'not all scraped',
          active: 'targets down',
          ok: 'targets up',
        },
        {
          id: 'rule',
          label: 'Alert rule',
          text: 'DemoContainerOOMKilled',
          failedText: 'not loaded',
          failed: 'rule missing',
          active: 'alert firing',
          ok: 'rule loaded',
          info: { expr: 'alert firing', text: '{value} firing' },
        },
      ],
    },
    {
      id: 'k8sgpt',
      label: 'K8sGPT',
      sub: 'k8sgpt-system',
      icon: 'scan',
      reads: 'scan',
      wide: true,
      access: { label: 'read-only', title: 'K8sGPT only reads demo-apps.' },
      stages: [
        // The question the scan port of every node asks.
        { id: 'scan', label: 'Scan', up: 'up k8sgpt' },
        { id: 'result', label: 'Result', title: 'one per problem', active: 'results count' },
      ],
      rows: [
        { id: 'operator', label: 'Operator', text: 'reconciles about every 30s', up: 'up operator' },
        {
          id: 'llm',
          label: 'LLM',
          text: 'explains each Result',
          idleText: 'off',
          failedText: 'calls failed',
          failed: 'llm failing',
          ok: 'llm on',
          set: 'llm set',
          needs: 'llm known',
          info: { expr: 'llm info', text: '{model} · {backend}' },
        },
      ],
      list: {
        title: 'Results',
        scope: 'k8sgpt-system',
        expr: 'results',
        needs: 'results known',
        limit: 2,
        empty: 'No Results.',
      },
    },
    {
      id: 'slack',
      label: 'Slack',
      sub: 'the one path out',
      icon: 'slack',
      access: { label: 'posts out', write: true, title: 'slack-relay posts to your webhook.' },
      stages: [{ id: 'relay', label: 'Relay', idleText: 'off', up: 'up relay', needs: 'relay known' }],
      rows: [
        {
          id: 'relay',
          label: 'slack-relay',
          text: 'posts findings',
          idleText: 'off (no webhook)',
          up: 'up relay',
          needs: 'relay known',
        },
        {
          id: 'webhook',
          label: 'Webhook',
          text: 'probed every 30s',
          idleText: 'off',
          failedText: 'unreachable',
          up: 'up webhook',
        },
      ],
    },
  ],
  links: [
    { id: 'results', from: 'k8sgpt', to: 'slack', label: 'Results', activeText: 'exist', active: 'results sent', ok: 'up relay' },
    {
      id: 'alerts',
      from: 'prometheus',
      to: 'slack',
      label: 'alerts',
      title: 'through Alertmanager',
      activeText: 'firing',
      active: 'alerts sent',
      ok: 'alerts path',
      needs: 'relay known',
    },
  ],
};

const planeSpec = { ...spec, controlPlane: plane };

const labelled = (...samples) => ({ samples: samples.map((labels) => ({ labels, value: 1 })) });
const LLM_INFO = labelled({ model: 'gpt-4o-mini', backend: 'openai' });

// The answers of a control plane where everything works, Slack and the LLM
// are on, and there is nothing to report.
function healthyPlane() {
  return new Map([
    ...healthy(),
    ['up scrape', value(1)],
    ['alert firing', EMPTY],
    ['targets down', EMPTY],
    ['targets up', value(1)],
    ['rule missing', EMPTY],
    ['rule loaded', value(1)],
    ['results count', EMPTY],
    ['up operator', value(1)],
    ['llm failing', EMPTY],
    ['llm on', value(1)],
    ['llm set', value(1)],
    ['llm known', value(1)],
    ['llm info', LLM_INFO],
    ['results', EMPTY],
    ['results known', value(1)],
    ['up relay', value(1)],
    ['relay known', value(1)],
    ['up webhook', value(1)],
    ['results sent', EMPTY],
    ['alerts sent', EMPTY],
    ['alerts path', value(1)],
  ]);
}

// What Prometheus answers with Slack off: the relay and the probe do not
// exist, so nothing that asks about them gets a sample. What would report
// the relay is still there ('relay known').
const SLACK_OFF = {
  'up relay': EMPTY,
  'up webhook': EMPTY,
  'results sent': EMPTY,
  'alerts sent': EMPTY,
  'alerts path': EMPTY,
};

const derivePlane = (changes = {}, from = planeSpec) =>
  deriveTopology(from, new Map([...healthyPlane(), ...Object.entries(changes)])).controlPlane;
const boxOf = (id, changes) => derivePlane(changes).boxes.find((box) => box.id === id);
const stageOf = (box, id, changes) => boxOf(box, changes).stages.find((stage) => stage.id === id);
const rowOf = (box, id, changes) => boxOf(box, changes).rows.find((row) => row.id === id);
const linkOf = (id, changes) => derivePlane(changes).links.find((link) => link.id === id);
const listOf = (changes) => boxOf('k8sgpt', changes).list;

test('a spec without a control plane has no such layer', () => {
  assert.equal(derive().controlPlane, null);
  // What the existing card asks does not change either.
  assert.deepEqual([...topologyQueries(spec)].sort(), [...healthy().keys()].sort());
});

test('topologyQueries lists the expressions of the control plane once each', () => {
  const exprs = topologyQueries(planeSpec);
  assert.deepEqual([...exprs].sort(), [...healthyPlane().keys()].sort());
  // Two stages and a link ask about the relay, a stage and a row about the
  // alert, and the Scan stage asks what the scan ports ask.
  for (const shared of ['up relay', 'alert firing', 'up k8sgpt']) {
    assert.equal(exprs.filter((expr) => expr === shared).length, 1);
  }
  // What a link needs is asked too, also when nothing else asks it. The
  // list asks 'results known' already.
  const links = plane.links.map((link) => ({ ...link, needs: `${link.id} known` }));
  const asked = topologyQueries({ ...planeSpec, controlPlane: { ...plane, links } });
  assert.deepEqual(asked.filter((expr) => !exprs.includes(expr)), ['alerts known']);
});

// The state of a stage, a row and a link

test('the first of failed, active and ok that is above 0 gives the state', () => {
  const state = (changes) => rowOf('prometheus', 'rule', changes).state;
  const [missing, firing, loaded] = ['rule missing', 'alert firing', 'rule loaded'];
  assert.equal(state({ [missing]: value(1), [firing]: value(1), [loaded]: value(1) }), 'failed');
  assert.equal(state({ [missing]: EMPTY, [firing]: value(1), [loaded]: value(1) }), 'active');
  assert.equal(state({ [missing]: EMPTY, [firing]: EMPTY, [loaded]: value(1) }), 'ok');
  assert.equal(state({ [missing]: EMPTY, [firing]: EMPTY, [loaded]: EMPTY }), 'idle');
  // A value of 0, and one Prometheus could not give as a number, is not above 0.
  assert.equal(state({ [missing]: value(0), [firing]: value(0), [loaded]: value(3) }), 'ok');
  assert.equal(state({ [missing]: value(null), [firing]: value(0), [loaded]: value(0) }), 'idle');
  // A query that failed makes nothing look broken and nothing healthy.
  assert.equal(state({ [missing]: FAILED, [firing]: FAILED, [loaded]: value(1) }), 'ok');
  assert.equal(state({ [missing]: FAILED, [firing]: FAILED, [loaded]: FAILED }), 'idle');
});

test('up is short for a health check: above 0 is ok, any other value is failed, no sample is idle', () => {
  const state = (answer) => rowOf('k8sgpt', 'operator', { 'up operator': answer }).state;
  assert.equal(state(value(1)), 'ok');
  assert.equal(state(value(0)), 'failed');
  assert.equal(state(EMPTY), 'idle');
  assert.equal(state(FAILED), 'idle');
  // A count of ready replicas says ok as well as a 1 does.
  assert.equal(state(value(2)), 'ok');
  assert.equal(state(value(-1)), 'failed');
  assert.equal(state(value(null)), 'idle');
});

test('an entry without an expression is idle', () => {
  const [prometheus, ...rest] = plane.boxes;
  const bare = {
    ...planeSpec,
    controlPlane: {
      ...plane,
      boxes: [
        {
          ...prometheus,
          stages: [...prometheus.stages, { id: 'bare', label: 'Bare' }],
          rows: [...prometheus.rows, { id: 'bare', label: 'Bare', text: 'asks nothing' }],
        },
        ...rest,
      ],
    },
  };
  const box = derivePlane({}, bare).boxes[0];
  assert.deepEqual(box.stages.at(-1), { id: 'bare', label: 'Bare', state: 'idle', title: 'Bare: no data' });
  // A row that knows nothing says so in place of what it does.
  assert.deepEqual(box.rows.at(-1), {
    id: 'bare',
    label: 'Bare',
    text: 'no data',
    state: 'idle',
    title: 'Bare: no data',
  });
});

// Titles and sub-lines

test('a title gives the label, the word for the state and the detail', () => {
  assert.deepEqual(stageOf('prometheus', 'scrape'), {
    id: 'scrape',
    label: 'Scrape',
    state: 'ok',
    title: 'Scrape: ok · every 5s',
  });
  assert.equal(stageOf('prometheus', 'scrape', { 'up scrape': value(0) }).title, 'Scrape: down · every 5s');
  assert.equal(stageOf('k8sgpt', 'result', { 'results count': value(2) }).title, 'Result: active · one per problem');
  assert.equal(stageOf('k8sgpt', 'result').title, 'Result: idle · one per problem');
  // A stage without a detail says the state and no more.
  assert.equal(stageOf('k8sgpt', 'scan').title, 'Scan: ok');
  assert.deepEqual(rowOf('k8sgpt', 'operator'), {
    id: 'operator',
    label: 'Operator',
    text: 'reconciles about every 30s',
    state: 'ok',
    title: 'Operator: ok · reconciles about every 30s',
  });
  // Without a text of its own for the state, a row keeps its sub-line.
  assert.deepEqual(rowOf('k8sgpt', 'operator', { 'up operator': value(0) }), {
    id: 'operator',
    label: 'Operator',
    text: 'reconciles about every 30s',
    state: 'failed',
    title: 'Operator: down · reconciles about every 30s',
  });
});

test('the text an entry has for a state is its word, and a row shows it as its sub-line', () => {
  assert.equal(stageOf('prometheus', 'alert', { 'alert firing': value(1) }).title, 'Alert: firing');
  assert.equal(stageOf('slack', 'relay', SLACK_OFF).title, 'Relay: off');

  const text = ({ text, title }) => ({ text, title });
  assert.deepEqual(text(rowOf('slack', 'relay', SLACK_OFF)), {
    text: 'off (no webhook)',
    title: 'slack-relay: off (no webhook)',
  });
  assert.deepEqual(text(rowOf('slack', 'webhook', { 'up webhook': value(0) })), {
    text: 'unreachable',
    title: 'Webhook: unreachable',
  });
  assert.deepEqual(text(rowOf('prometheus', 'targets', { 'targets down': value(1), 'targets up': value(0) })), {
    text: 'not all scraped',
    title: 'App targets: not all scraped',
  });
  // There is no text for ok: the row says what it does.
  assert.deepEqual(text(rowOf('slack', 'webhook')), {
    text: 'probed every 30s',
    title: 'Webhook: ok · probed every 30s',
  });
});

test('an idle text needs an answer behind it', () => {
  // The query failed: nobody knows that Slack is off.
  const row = rowOf('slack', 'relay', { 'up relay': FAILED });
  assert.deepEqual(row, {
    id: 'relay',
    label: 'slack-relay',
    text: 'no data',
    state: 'idle',
    title: 'slack-relay: no data',
  });
  assert.equal(stageOf('slack', 'relay', { 'up relay': FAILED }).title, 'Relay: no data');
  const unknown = { 'llm on': FAILED, 'llm set': FAILED, 'llm failing': FAILED, 'llm info': FAILED };
  assert.equal(rowOf('k8sgpt', 'llm', unknown).title, 'LLM: no data');
  // Every state query must have answered: the one that failed might have
  // said that the LLM is on, or that its calls fail.
  const llmOff = { 'llm on': EMPTY, 'llm set': EMPTY, 'llm failing': EMPTY, 'llm info': EMPTY };
  for (const missing of ['llm on', 'llm set', 'llm failing']) {
    const row = rowOf('k8sgpt', 'llm', { ...llmOff, [missing]: FAILED });
    assert.equal(row.title, 'LLM: no data', missing);
  }
  assert.equal(rowOf('k8sgpt', 'llm', llmOff).title, 'LLM: off');
  // A row that knows nothing does not show what its info query says either.
  assert.equal(rowOf('k8sgpt', 'llm', { 'llm on': FAILED, 'llm set': FAILED }).text, 'no data');
  // The same holds for the word "off" of a link.
  assert.equal(linkOf('alerts', { 'alerts sent': FAILED, 'alerts path': EMPTY }).title, 'alerts: no data · through Alertmanager');
  assert.equal(linkOf('alerts', { 'alerts sent': EMPTY, 'alerts path': FAILED }).title, 'alerts: no data · through Alertmanager');
});

test('an idle text also needs what the entry leans on', () => {
  // Nothing reports the relay: no sample for it does not say that Slack is off.
  for (const missing of [EMPTY, FAILED, value(0), value(null)]) {
    const blind = { ...SLACK_OFF, 'relay known': missing };
    assert.deepEqual(rowOf('slack', 'relay', blind), {
      id: 'relay',
      label: 'slack-relay',
      text: 'no data',
      state: 'idle',
      title: 'slack-relay: no data',
    });
    assert.equal(stageOf('slack', 'relay', blind).title, 'Relay: no data');
    assert.equal(linkOf('alerts', blind).title, 'alerts: no data · through Alertmanager');
    assert.equal(linkOf('alerts', blind).state, 'off');
    // The box is off, and its name does not say why either.
    assert.equal(boxOf('slack', blind).title, 'Slack: no data');
  }
  assert.equal(rowOf('k8sgpt', 'llm', { 'llm on': EMPTY, 'llm set': EMPTY, 'llm info': EMPTY, 'llm known': EMPTY }).title, 'LLM: no data');
  assert.equal(rowOf('k8sgpt', 'llm', { 'llm on': EMPTY, 'llm set': EMPTY, 'llm info': EMPTY, 'llm known': value(1) }).title, 'LLM: off');

  // An entry that needs nothing goes by its own answers: the fixture's Webhook
  // row and its Results link.
  const blind = { ...SLACK_OFF, 'relay known': EMPTY };
  assert.equal(rowOf('slack', 'webhook', blind).title, 'Webhook: off');
  assert.equal(linkOf('results', blind).title, 'Results: off');
});

test('what an entry needs changes no state that has evidence of its own', () => {
  // In the healthy plane every entry that needs something has an answer above 0.
  const blind = { 'relay known': EMPTY, 'llm known': EMPTY };
  assert.deepEqual(derivePlane(blind), derivePlane());
  assert.equal(rowOf('slack', 'relay', { ...blind, 'up relay': value(0) }).title, 'slack-relay: down · posts findings');
  assert.equal(rowOf('k8sgpt', 'llm', { ...blind, 'llm failing': value(1) }).title, 'LLM: calls failed');
  assert.equal(linkOf('alerts', { ...blind, 'alerts sent': value(1) }).title, 'alerts: firing · through Alertmanager');
});

test('a health check without a sample says "no data", any other idle entry "idle"', () => {
  assert.equal(rowOf('k8sgpt', 'operator', { 'up operator': EMPTY }).title, 'Operator: no data');
  assert.equal(rowOf('k8sgpt', 'operator', { 'up operator': EMPTY }).text, 'no data');
  assert.equal(rowOf('prometheus', 'targets', { 'targets up': EMPTY }).title, 'App targets: idle · two apps');
});

test('info fills the sub-line from the first sample of its query', () => {
  assert.deepEqual(rowOf('k8sgpt', 'llm'), {
    id: 'llm',
    label: 'LLM',
    text: 'gpt-4o-mini · openai',
    state: 'ok',
    title: 'LLM: ok · gpt-4o-mini · openai',
  });
  const two = labelled({ model: 'first', backend: 'a' }, { model: 'second', backend: 'b' });
  assert.equal(rowOf('k8sgpt', 'llm', { 'llm info': two }).text, 'first · a');
  // {value} is the sample's value.
  const firing = rowOf('prometheus', 'rule', { 'alert firing': value(2) });
  assert.deepEqual(firing, {
    id: 'rule',
    label: 'Alert rule',
    text: '2 firing',
    state: 'active',
    title: 'Alert rule: active · 2 firing',
  });
  // A label the sample does not have is left empty.
  assert.equal(rowOf('k8sgpt', 'llm', { 'llm info': labelled({ backend: 'openai' }) }).text, ' · openai');
});

test('set is the neutral state of a thing that is configured and not yet proven to work', () => {
  // The LLM is set, and nothing says yet that it answers.
  const set = rowOf('k8sgpt', 'llm', { 'llm on': EMPTY });
  assert.deepEqual(set, {
    id: 'llm',
    label: 'LLM',
    text: 'gpt-4o-mini · openai',
    state: 'set',
    title: 'LLM: set · gpt-4o-mini · openai',
  });
  // ok is evidence and wins over set, and a failure wins over both.
  assert.equal(rowOf('k8sgpt', 'llm').state, 'ok');
  assert.equal(rowOf('k8sgpt', 'llm', { 'llm on': EMPTY, 'llm failing': value(1) }).state, 'failed');
  // A query of ok that failed leaves what set says.
  assert.equal(rowOf('k8sgpt', 'llm', { 'llm on': FAILED }).state, 'set');
  // A set row is not ok, and it keeps its box on.
  const box = boxOf('k8sgpt', { 'llm on': EMPTY });
  assert.equal(box.title, 'K8sGPT: LLM set · Results: none');
  assert.equal(boxOf('k8sgpt', { 'up k8sgpt': EMPTY, 'up operator': EMPTY, 'llm on': EMPTY }).off, false);
});

test('without a sample for its info a row keeps its text', () => {
  for (const answer of [EMPTY, FAILED]) {
    const row = rowOf('k8sgpt', 'llm', { 'llm info': answer });
    assert.equal(row.text, 'explains each Result');
    assert.equal(row.title, 'LLM: ok · explains each Result');
  }
  assert.equal(rowOf('prometheus', 'rule').text, 'DemoContainerOOMKilled');
});

test("a row's own text for its state comes before its info", () => {
  const failing = rowOf('k8sgpt', 'llm', { 'llm failing': value(3) });
  assert.deepEqual(failing, {
    id: 'llm',
    label: 'LLM',
    text: 'calls failed',
    state: 'failed',
    title: 'LLM: calls failed',
  });
  const off = rowOf('k8sgpt', 'llm', { 'llm on': EMPTY, 'llm set': EMPTY });
  assert.equal(off.text, 'off');
});

// The list

const result = (kind, object, backend, name = `${kind}-${object}`.toLowerCase()) => ({
  kind,
  object,
  name,
  result_namespace: 'k8sgpt-system',
  ...(backend === undefined ? {} : { backend }),
});

test('a list with no sample says "none" and keeps its empty text', () => {
  assert.deepEqual(listOf(), { count: 0, summary: 'none', rows: [], more: 0, empty: 'No Results.' });
});

test('a list row gives the kind, the object and the backend of a sample', () => {
  const one = labelled(result('Pod', 'demo-apps/payments-api-848f', 'openai', 'demoappspaymentsapi848f'));
  assert.deepEqual(listOf({ results: one }), {
    count: 1,
    summary: '1 in all · Pod 1',
    rows: [
      {
        kind: 'Pod',
        object: 'demo-apps/payments-api-848f',
        backend: 'openai',
        title: 'Pod demo-apps/payments-api-848f · Result demoappspaymentsapi848f · backend openai',
      },
    ],
    more: 0,
    empty: 'No Results.',
  });
});

test('the list is sorted by kind and then by object, and counts each kind', () => {
  const samples = labelled(
    result('Pod', 'demo-apps/recommend-svc-1', 'openai'),
    result('ConfigMap', 'demo-apps/payments-config', 'k8sgpt-detection-pack:cm'),
    result('Pod', 'demo-apps/payments-api-1', 'openai'),
  );
  const list = listOf({ results: samples });
  assert.equal(list.count, 3);
  assert.equal(list.summary, '3 in all · ConfigMap 1, Pod 2');
  // The fixture shows two rows at most.
  assert.deepEqual(
    list.rows.map(({ kind, object }) => `${kind} ${object}`),
    ['ConfigMap demo-apps/payments-config', 'Pod demo-apps/payments-api-1'],
  );
  assert.equal(list.more, 1);
});

test('a list shows no more rows than its limit and says how many it leaves out', () => {
  const samples = (count) => labelled(...Array.from({ length: count }, (_, at) => result('Pod', `pod-${at}`, 'openai')));
  const shown = (count) => {
    const { rows, more } = listOf({ results: samples(count) });
    return [rows.length, more];
  };
  assert.deepEqual(shown(2), [2, 0]);
  assert.deepEqual(shown(3), [2, 1]);
  assert.deepEqual(shown(7), [2, 5]);
  assert.equal(listOf({ results: samples(7) }).summary, '7 in all · Pod 7');
});

test('a sample without a backend, or with an empty one, has none', () => {
  const samples = labelled(result('Pod', 'a'), result('Pod', 'b', ''));
  assert.deepEqual(listOf({ results: samples }).rows, [
    { kind: 'Pod', object: 'a', backend: '', title: 'Pod a · Result pod-a' },
    { kind: 'Pod', object: 'b', backend: '', title: 'Pod b · Result pod-b' },
  ]);
});

test('a list whose query failed has no count: no evidence is not "none"', () => {
  assert.deepEqual(listOf({ results: FAILED }), {
    count: null,
    summary: 'no data',
    rows: [],
    more: 0,
    empty: 'No Results.',
  });
});

test('an empty list says "none" only while what it needs is there', () => {
  // Nothing reports the Results: an empty answer does not say that there are none.
  for (const missing of [EMPTY, FAILED, value(0)]) {
    assert.deepEqual(listOf({ 'results known': missing }), {
      count: null,
      summary: 'no data',
      rows: [],
      more: 0,
      empty: 'No Results.',
    });
  }
  assert.equal(boxOf('k8sgpt', { 'results known': EMPTY }).title, 'K8sGPT: ok · Results: no data');
  // Results that are there are shown whatever else is missing.
  const one = labelled(result('Pod', 'a'));
  assert.equal(listOf({ results: one, 'results known': EMPTY }).count, 1);
  // A list that needs nothing goes by its own answer.
  const [prometheus, k8sgpt, slack] = plane.boxes;
  const { needs: _, ...list } = k8sgpt.list;
  const plain = { ...planeSpec, controlPlane: { ...plane, boxes: [prometheus, { ...k8sgpt, list }, slack] } };
  assert.equal(derivePlane({ 'results known': EMPTY }, plain).boxes[1].list.summary, 'none');
});

// Links

test('a link is live while its active expression is above 0, ready while its ok expression is, else off', () => {
  assert.deepEqual(linkOf('alerts'), {
    id: 'alerts',
    label: 'alerts',
    state: 'ready',
    title: 'alerts: ready · through Alertmanager',
  });
  assert.deepEqual(linkOf('alerts', { 'alerts sent': value(1) }), {
    id: 'alerts',
    label: 'alerts',
    state: 'live',
    title: 'alerts: firing · through Alertmanager',
  });
  assert.deepEqual(linkOf('alerts', { 'alerts path': value(0) }), {
    id: 'alerts',
    label: 'alerts',
    state: 'off',
    title: 'alerts: off · through Alertmanager',
  });
  for (const answer of [EMPTY, FAILED]) {
    assert.equal(linkOf('alerts', { 'alerts path': answer }).state, 'off');
  }
  assert.equal(linkOf('results').title, 'Results: ready');
});

test('a link whose active expression is above 0 is live whatever its ok expression says', () => {
  for (const answer of [value(0), EMPTY, FAILED]) {
    assert.equal(linkOf('alerts', { 'alerts sent': value(2), 'alerts path': answer }).state, 'live');
  }
  // A link reads active and ok and nothing else.
  assert.equal(linkOf('results', { 'results sent': value(0), 'up relay': value(1) }).state, 'ready');
});

// Boxes

test('a box is off while every one of its stages and rows is idle', () => {
  assert.equal(boxOf('slack').off, false);
  assert.equal(boxOf('slack', SLACK_OFF).off, true);
  // One row that knows something is enough.
  assert.equal(boxOf('slack', { ...SLACK_OFF, 'up webhook': value(0) }).off, false);
  assert.equal(boxOf('slack', { ...SLACK_OFF, 'up webhook': value(1) }).off, false);
  // So is one stage: both rows of Prometheus are idle, and it still scrapes.
  assert.equal(boxOf('prometheus', { 'targets up': EMPTY, 'rule loaded': EMPTY }).off, false);
  // The list is not asked: Results without anything running leave the box off.
  const dead = { 'up k8sgpt': EMPTY, 'results count': EMPTY, 'up operator': EMPTY, 'llm on': EMPTY, 'llm set': EMPTY, 'llm info': EMPTY };
  assert.equal(boxOf('k8sgpt', { ...dead, results: labelled(result('Pod', 'a')) }).off, true);
});

test('with Slack off its box and both links are off, and nothing else changes', () => {
  const on = derivePlane();
  const off = derivePlane(SLACK_OFF);
  assert.deepEqual(off.boxes[2], {
    id: 'slack',
    title: 'Slack: off (no webhook)',
    stages: [{ id: 'relay', label: 'Relay', state: 'idle', title: 'Relay: off' }],
    rows: [
      { id: 'relay', label: 'slack-relay', text: 'off (no webhook)', state: 'idle', title: 'slack-relay: off (no webhook)' },
      { id: 'webhook', label: 'Webhook', text: 'off', state: 'idle', title: 'Webhook: off' },
    ],
    list: null,
    off: true,
  });
  assert.deepEqual(off.links.map((link) => link.state), ['off', 'off']);
  assert.deepEqual(off.boxes.slice(0, 2), on.boxes.slice(0, 2));
  // The app layer does not look at Slack.
  const apps = ({ source, nodes, ports }) => ({ source, nodes, ports });
  assert.deepEqual(
    apps(deriveTopology(planeSpec, new Map([...healthyPlane(), ...Object.entries(SLACK_OFF)]))),
    apps(deriveTopology(planeSpec, healthyPlane())),
  );
});

test('a box sums itself up: the rows that are not ok, its list and what reaches it', () => {
  const title = (id, changes) => boxOf(id, changes).title;
  assert.equal(title('prometheus'), 'Prometheus: ok');
  assert.equal(title('k8sgpt'), 'K8sGPT: ok · Results: none');
  assert.equal(title('slack'), 'Slack: ok · ready for Results and alerts');

  const found = { results: labelled(result('Pod', 'a'), result('Pod', 'b')), 'results count': value(2) };
  assert.equal(title('k8sgpt', { ...found, 'llm on': EMPTY, 'llm set': EMPTY, 'llm info': EMPTY }), 'K8sGPT: LLM off · Results: 2');
  assert.equal(
    title('k8sgpt', { 'up operator': value(0), 'llm failing': value(1), results: FAILED }),
    'K8sGPT: Operator down · LLM calls failed · Results: no data',
  );
  assert.equal(
    title('prometheus', { 'alert firing': value(1), 'targets down': value(1) }),
    'Prometheus: App targets not all scraped · Alert rule active',
  );
});

test("the name of the box a link runs into says what the link carries", () => {
  const title = (changes) => boxOf('slack', changes).title;
  // A live link says what exists for it to carry, in its own word: not that
  // it is being sent now.
  assert.equal(title({ 'results sent': value(1), 'alerts sent': value(1) }), 'Slack: ok · Results exist · alerts firing');
  assert.equal(title({ 'alerts sent': value(1) }), 'Slack: ok · alerts firing · ready for Results');
  assert.equal(title({ 'alerts path': EMPTY }), 'Slack: ok · ready for Results · alerts off');
  assert.equal(
    title({ 'up webhook': value(0), 'results sent': value(1) }),
    'Slack: Webhook unreachable · Results exist · ready for alerts',
  );
  // A link that is off comes before one that knows nothing.
  assert.equal(
    title({ 'up relay': value(0), 'alerts path': FAILED }),
    'Slack: slack-relay down · Results off · no data for alerts',
  );
  assert.equal(
    title({ 'up relay': EMPTY, 'relay known': EMPTY, 'alerts path': EMPTY }),
    'Slack: slack-relay no data · Results off · no data for alerts',
  );
  // The boxes the links leave do not repeat it.
  assert.equal(boxOf('prometheus', { 'alerts sent': value(1) }).title, 'Prometheus: ok');
  // A link whose queries failed is not said to be off, and neither is one
  // without what it needs.
  assert.equal(
    title({ 'alerts sent': FAILED, 'alerts path': FAILED }),
    'Slack: ok · ready for Results · no data for alerts',
  );
  assert.equal(
    title({ 'alerts path': EMPTY, 'relay known': EMPTY }),
    'Slack: ok · ready for Results · no data for alerts',
  );
});

test('with no answer at all the control plane is idle, and nothing in it looks broken or working', () => {
  for (const results of [new Map(), new Map([...healthyPlane().keys()].map((expr) => [expr, FAILED]))]) {
    const { boxes, links } = deriveTopology(planeSpec, results).controlPlane;
    const entries = boxes.flatMap((box) => [...box.stages, ...box.rows]);
    assert.ok(entries.every((entry) => entry.state === 'idle'));
    assert.ok(boxes.every((box) => box.off));
    assert.deepEqual(links.map((link) => link.state), ['off', 'off']);
    assert.deepEqual(boxes[1].list, { count: null, summary: 'no data', rows: [], more: 0, empty: 'No Results.' });

    // Every title says "no data", and so none says down, off or ok.
    for (const { title } of [...boxes, ...entries, ...links]) assert.match(title, /^[^:·]+: no data( · |$)/);
    assert.deepEqual(boxes.map((box) => box.title), ['Prometheus: no data', 'K8sGPT: no data', 'Slack: no data']);
    assert.deepEqual(links.map((link) => link.title), ['Results: no data', 'alerts: no data · through Alertmanager']);
    // No row claims a state it has no answer for, or says what it does when
    // it works: each says that it knows nothing.
    for (const box of boxes) assert.deepEqual(box.rows.map((row) => row.text), box.rows.map(() => 'no data'));
    assert.deepEqual(boxes[1].rows.map((row) => row.title), ['Operator: no data', 'LLM: no data']);
  }
});

test('a link without a word of its own for live says "live"', () => {
  const links = plane.links.map(({ activeText: _, ...link }) => link);
  const plain = { ...planeSpec, controlPlane: { ...plane, links } };
  const live = derivePlane({ 'alerts sent': value(1) }, plain);
  assert.equal(live.links[1].title, 'alerts: live · through Alertmanager');
  assert.equal(live.boxes[2].title, 'Slack: ok · alerts live · ready for Results');
});

test("a list's tagText says what the tag of a row is", () => {
  const [prometheus, k8sgpt, slack] = plane.boxes;
  const list = { ...k8sgpt.list, tagText: 'as set' };
  const tagged = { ...planeSpec, controlPlane: { ...plane, boxes: [prometheus, { ...k8sgpt, list }, slack] } };
  const one = labelled(result('Pod', 'a', 'openai'), result('Pod', 'b'));
  const rows = derivePlane({ results: one }, tagged).boxes[1].list.rows;
  assert.deepEqual(
    rows.map((row) => row.title),
    ['Pod a · Result pod-a · backend openai: as set', 'Pod b · Result pod-b'],
  );
});

test('the control plane of dashboard.json has the shape these rules read', () => {
  const topology = shipped();
  const { boxes, links } = topology.controlPlane;
  assert.deepEqual(boxes.map((box) => box.id), ['prometheus', 'k8sgpt', 'slack']);
  for (const box of boxes) {
    if (box.reads !== undefined) assert.ok(box.reads in topology.ports, `${box.id} reads "${box.reads}"`);
  }
  for (const link of links) {
    for (const end of [link.from, link.to]) assert.ok(boxes.some((box) => box.id === end), `${link.id}: "${end}"`);
  }
  // The Scan stage and the scan marks on the nodes ask one question, so the
  // two cannot disagree.
  const scan = boxes.find((box) => box.reads === 'scan').stages.find((stage) => stage.id === 'scan');
  assert.equal(scan.up, topology.ports.scan.up);

  const model = deriveTopology(topology, new Map()).controlPlane;
  assert.deepEqual(model.boxes.map((box) => [box.id, box.off]), [
    ['prometheus', true],
    ['k8sgpt', true],
    ['slack', true],
  ]);
  assert.deepEqual(model.links.map((link) => [link.id, link.state]), [
    ['results', 'off'],
    ['alerts', 'off'],
  ]);

  // Every query answers, with no sample: Prometheus runs and sees none of
  // the control plane, as while kube-state-metrics is not scraped. Nothing
  // may then say off, none or idle, which would be claims with nothing
  // behind them, except the Alert stage: no alert fires, and Prometheus
  // says so itself.
  const blind = deriveTopology(topology, new Map(topologyQueries(topology).map((expr) => [expr, EMPTY]))).controlPlane;
  assert.deepEqual(
    blind.links.map((link) => link.title.split(' · ')[0]),
    ['Results: no data', 'alerts: no data'],
  );
  const list = blind.boxes.find((box) => box.list).list;
  assert.deepEqual([list.count, list.summary], [null, 'no data']);
  const said = blind.boxes
    .flatMap((box) => [...box.stages, ...box.rows])
    .map((entry) => entry.title.split(' · ')[0])
    .filter((word) => !word.endsWith(': no data'));
  assert.deepEqual(said, ['Alert: idle']);

  // Every icon the topology names is one the page can draw: an unknown name
  // would cost the page its topology card.
  const named = [topology.source, ...topology.nodes, ...boxes, ...boxes.flatMap((box) => box.rows)];
  const kinds = Object.keys(topology.ports);
  for (const name of [...named.map((item) => item.icon), ...kinds, 'check', 'cross', 'key']) {
    if (name !== undefined) assert.ok(hasIcon(name), `icon "${name}"`);
  }
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

// Wires of the control plane
//
// The same layout with its port marks, the panel around the nodes and the
// control plane under it. A mark is 20 by 20 and sits on the middle of its
// node's bottom edge: metrics left of scan where a node has both.

const mark = (node, dx = 0) => {
  const x = (node.left + node.right) / 2 + dx;
  return { left: x - 10, right: x + 10, top: node.bottom - 10, bottom: node.bottom + 10 };
};
const BOTH = (node) => ({ metrics: mark(node, -12), scan: mark(node, 12) });

// The panel leaves 24px under the lowest mark, and the layers are 30px apart.
const PANEL = {
  inner: { left: 1, right: 819, top: -15, bottom: 278 },
  outer: { left: 0, right: 820, top: -16, bottom: 279 },
};

const reader = (id, reads, box) => ({ id, reads, box });
const LINKS = [
  { id: 'results', from: 'k8sgpt', to: 'slack' },
  { id: 'alerts', from: 'prometheus', to: 'slack' },
];

// The three boxes in a row, with 28px under them for the link that runs
// around the middle one.
const ROW = {
  box: { left: 0, right: 820, top: 309, bottom: 620 },
  row: { left: 15, right: 805, top: 344, bottom: 580 },
  boxes: [
    reader('prometheus', 'metrics', { left: 15, right: 255, top: 344, bottom: 552 }),
    reader('k8sgpt', 'scan', { left: 271, right: 541, top: 344, bottom: 552 }),
    reader('slack', undefined, { left: 605, right: 805, top: 344, bottom: 552 }),
  ],
  links: LINKS,
};

// The same boxes one above the other, as on a narrow screen.
const STACK = {
  box: { left: 0, right: 820, top: 309, bottom: 960 },
  row: { left: 25, right: 795, top: 344, bottom: 900 },
  boxes: [
    reader('prometheus', 'metrics', { left: 25, right: 795, top: 344, bottom: 500 }),
    reader('k8sgpt', 'scan', { left: 25, right: 795, top: 530, bottom: 760 }),
    reader('slack', undefined, { left: 25, right: 795, top: 790, bottom: 900 }),
  ],
  links: LINKS,
};

const wired = (controlPlane = ROW, more = {}) =>
  routeWires({
    source: SOURCE,
    nodes: [
      { box: UPPER, fed: true, ports: BOTH(UPPER) },
      { box: LOWER, fed: true, ports: BOTH(LOWER) },
      { box: ASIDE, fed: false, ports: { scan: mark(ASIDE) } },
    ],
    panel: PANEL,
    controlPlane,
    ...more,
  });
const busOf = (kind, ...args) => wired(...args).buses.find((bus) => bus.kind === kind);
const linkTo = (id, ...args) => wired(...args).links.find((link) => link.id === id);

// segments reads path data made of M, H and V into its straight pieces.
function segments(path) {
  const pieces = [];
  let [x, y] = [0, 0];
  for (const [, command, numbers] of path.matchAll(/([MHV])([^MHV]*)/g)) {
    const [first, second] = numbers.split(/[ ,]+/).filter(Boolean).map(Number);
    // M moves to a point. H and V draw to a new x or a new y.
    const [toX, toY] = command === 'M' ? [first, second] : command === 'H' ? [first, y] : [x, first];
    if (command !== 'M') pieces.push({ x1: x, y1: y, x2: toX, y2: toY });
    [x, y] = [toX, toY];
  }
  return pieces;
}
const within = (value, a, b) => value >= Math.min(a, b) && value <= Math.max(a, b);
// Whether an upright piece and a level piece meet, at an end or between.
const meet = (upright, level) => within(upright.x1, level.x1, level.x2) && within(level.y1, upright.y1, upright.y2);
const uprights = (path) => segments(path).filter((piece) => piece.x1 === piece.x2);
const levels = (path) => segments(path).filter((piece) => piece.y1 === piece.y2);

test('without a control plane there is no bus and no link', () => {
  const { buses, links } = routeWires(layout());
  assert.deepEqual(buses, []);
  assert.deepEqual(links, []);
});

test('only a kind of port that a box reads gets a bus', () => {
  assert.deepEqual(wired().buses.map((bus) => bus.kind), ['metrics', 'scan']);
  const unread = { ...ROW, boxes: ROW.boxes.map((box) => (box.id === 'prometheus' ? { ...box, reads: undefined } : box)) };
  assert.deepEqual(wired(unread).buses.map((bus) => bus.kind), ['scan']);
  // A kind no node has a port of has nothing to join.
  const odd = { ...ROW, boxes: ROW.boxes.map((box) => (box.id === 'slack' ? { ...box, reads: 'logs' } : box)) };
  assert.deepEqual(wired(odd).buses.map((bus) => bus.kind), ['metrics', 'scan']);
});

test('a port with no node below it drops straight down to the collector of its kind', () => {
  // The marks of the lower node and of the node that stands alone.
  assert.deepEqual(busOf('metrics').drops.slice(1), ['M357,254 V262']);
  assert.deepEqual(busOf('scan').drops.slice(1), ['M381,254 V262', 'M635,185 V262']);
});

test('collectors that overlap side by side run at different heights above the bottom edge of the panel, the first box lowest', () => {
  // Stacked, both step out right and overlap: a third and two thirds of the
  // way from the edge (278) up to the lowest mark (254).
  assert.equal(busOf('metrics', STACK).collector, 'M357,270 H410 M466,270 H410');
  assert.equal(busOf('scan', STACK).collector, 'M381,262 H508 M635,262 H508');
});

test('collectors that do not overlap side by side run at the same height, the highest', () => {
  // In the row, the metrics collector (272 to 357) is left of the scan one (381 to 635).
  assert.equal(busOf('metrics').collector, 'M272,262 H315 M357,262 H315');
  assert.equal(busOf('scan').collector, 'M381,262 H508 M635,262 H508');
});

test('a collector runs from both ends to where its trunk leaves, the middle of the collector', () => {
  for (const { collector, trunk } of wired().buses) {
    const [start] = segments(trunk);
    const ends = levels(collector).map((piece) => [piece.x2, piece.y2]);
    assert.deepEqual(ends, [
      [start.x1, start.y1],
      [start.x1, start.y1],
    ]);
  }
});

test('a trunk runs in its lane between the layers into the middle of the top edge of its box', () => {
  // The trunks do not overlap side by side, so both lanes are half way from
  // the panel (279) to the control plane (309). The line stops where the
  // arrowhead starts.
  assert.deepEqual(busOf('metrics'), {
    kind: 'metrics',
    drops: ['M357,115 V133 H272 V262', 'M357,254 V262'],
    collector: 'M272,262 H315 M357,262 H315',
    phases: { drops: [-275, -50], collector: [-43, -42] },
    trunk: 'M315,262 V294 H135 V339',
    head: 'M135,344 l-5,-5 h10 z',
    dot: null,
  });
  assert.equal(busOf('scan').trunk, 'M508,262 V294 H406 V339');
  assert.equal(busOf('scan').head, 'M406,344 l-5,-5 h10 z');
});

test('a trunk that starts above the middle of its box runs straight down', () => {
  // One bus has the room to itself: its collector is half way up.
  const under = { ...ROW, boxes: [reader('prometheus', 'metrics', { left: 312, right: 512, top: 344, bottom: 552 })], links: [] };
  assert.equal(busOf('metrics', under).trunk, 'M412,266 V339');
});

// The dash phase at the end of a piece: its offset plus its length.
const endPhase = (path, offset) => offset + segments(path).reduce((sum, p) => sum + Math.abs(p.x2 - p.x1) + Math.abs(p.y2 - p.y1), 0);
// The dash phase at a point of the collector, from the run that holds it.
function collectorPhase(bus, x) {
  const runs = bus.collector.split(/ (?=M)/);
  const at = runs.findIndex((run) => levels(run).some((piece) => within(x, piece.x1, piece.x2)));
  const [run] = levels(runs[at]);
  return bus.phases.collector[at] + Math.abs(x - run.x1);
}

test('the dashes of a bus run on without a break from each port into its trunk', () => {
  for (const plane of [ROW, STACK]) {
    for (const bus of wired(plane).buses) {
      const [start] = segments(bus.trunk);
      // The trunk starts at phase 0; each run of the collector ends there.
      bus.collector.split(/ (?=M)/).forEach((run, at) => assert.equal(endPhase(run, bus.phases.collector[at]), 0));
      // Each drop ends where the collector has the same phase.
      bus.drops.forEach((drop, at) => {
        const [end] = segments(drop).slice(-1);
        assert.equal(end.y2, start.y1);
        assert.equal(endPhase(drop, bus.phases.drops[at]), collectorPhase(bus, end.x2), `${bus.kind} drop ${at}`);
      });
    }
  }
});

test('the pieces of a bus whose trunk leaves the panel edge keep their own dashes', () => {
  const beside = { inner: { ...PANEL.inner, left: 500, right: 600 }, outer: { ...PANEL.outer, left: 499, right: 601 } };
  assert.deepEqual(busOf('metrics', ROW, { panel: beside }).phases, { drops: [0, 0], collector: [0] });
});

test('in a row no trunk crosses the collector or the lane of the other bus', () => {
  const [metrics, scan] = wired().buses;
  for (const [mine, other] of [
    [metrics, scan],
    [scan, metrics],
  ]) {
    for (const upright of uprights(mine.trunk)) {
      for (const level of [...levels(other.collector), ...levels(other.trunk)]) {
        assert.equal(meet(upright, level), false, `${mine.kind} trunk meets the ${other.kind} bus`);
      }
    }
  }
  // The trunks do not overlap side by side, so their lanes share a height.
  const [, metricsLane] = segments(metrics.trunk);
  const [, scanLane] = segments(scan.trunk);
  assert.equal(metricsLane.y1, scanLane.y1);
});

test('a port with a node below it steps out beside that node, on the side of the box that reads it', () => {
  // The upper node's marks are above the lower node (284 to 454). In the row,
  // Prometheus is left of the marks and K8sGPT right of them.
  assert.equal(busOf('metrics').drops[0], 'M357,115 V133 H272 V262');
  assert.equal(busOf('scan').drops[0], 'M381,115 V133 H466 V262');
  // Stacked, both boxes are centred right of the marks.
  assert.equal(busOf('metrics', STACK).drops[0], 'M357,115 V133 H466 V270');
  assert.equal(busOf('scan', STACK).drops[0], 'M381,115 V127 H478 V262');
  // A lower node that is not under the mark is not in the way. The
  // collectors still run below it.
  const apart = { left: 600, right: 770, top: 139, bottom: 244 };
  const nodes = [
    { box: UPPER, fed: true, ports: BOTH(UPPER) },
    { box: apart, fed: true, ports: {} },
  ];
  assert.deepEqual(busOf('metrics', ROW, { nodes }).drops, ['M357,115 V255']);
});

test('a line steps out to the left only when the lines left of it do too', () => {
  // The box that reads the right mark is on the left, the other on the right:
  // stepping each its own way would cross the lines. Both step out right.
  const swapped = {
    ...ROW,
    boxes: [
      reader('prometheus', 'metrics', { left: 605, right: 805, top: 344, bottom: 552 }),
      reader('k8sgpt', 'scan', { left: 15, right: 255, top: 344, bottom: 552 }),
    ],
    links: [],
  };
  assert.equal(busOf('metrics', swapped).drops[0], 'M357,115 V133 H466 V270');
  assert.equal(busOf('scan', swapped).drops[0], 'M381,115 V127 H478 V262');
});

test('the lines of one node never cross, and on one side they nest: the mark nearest that side turns lowest and drops nearest', () => {
  for (const plane of [ROW, STACK]) {
    const [metrics, scan] = [busOf('metrics', plane).drops[0], busOf('scan', plane).drops[0]];
    for (const [a, b] of [
      [metrics, scan],
      [scan, metrics],
    ]) {
      for (const upright of uprights(a)) {
        for (const level of levels(b)) assert.equal(meet(upright, level), false);
      }
    }
    // Both runs stay between the mark (115) and the node below (139).
    for (const drop of [metrics, scan]) {
      const [, run] = segments(drop);
      assert.ok(run.y1 > 115 && run.y1 < 139);
    }
  }
  // Stacked, both step out right: the left mark turns lowest and drops nearest.
  const [, metricsRun] = segments(busOf('metrics', STACK).drops[0]);
  const [, scanRun] = segments(busOf('scan', STACK).drops[0]);
  assert.ok(metricsRun.y1 > scanRun.y1 && metricsRun.x2 < scanRun.x2);
});

test('where the panel cuts a collector, the trunk leaves from the middle of what is in view', () => {
  const narrow = { inner: { ...PANEL.inner, left: 300, right: 440 }, outer: { ...PANEL.outer, left: 299, right: 441 } };
  // In view of the metrics collector (272 to 357): 300 to 357.
  const metrics = busOf('metrics', ROW, { panel: narrow });
  assert.equal(metrics.trunk, 'M329,262 V294 H135 V339');
  assert.equal(metrics.collector, 'M272,262 H329 M357,262 H329');
  // The drops are not moved: the page cuts them at the panel.
  assert.deepEqual(metrics.drops, busOf('metrics').drops);
});

test('a trunk that would step aside by a few px leaves right above the middle of its box', () => {
  // One bus: its collector runs from 357 to 466 at 266, and its middle is 412.
  const under = (left) => ({ ...ROW, boxes: [reader('prometheus', 'metrics', { left, right: left + 200, top: 344, bottom: 552 })], links: [] });
  // The middle of the box is 8px to the right: no step.
  const near = busOf('metrics', under(320));
  assert.equal(near.trunk, 'M420,266 V339');
  assert.equal(near.collector, 'M357,266 H420 M466,266 H420');
  assert.equal(near.head, 'M420,344 l-5,-5 h10 z');
  // 9px is a step that reads as one.
  assert.equal(busOf('metrics', under(321)).trunk, 'M412,266 V294 H421 V339');
  // The trunk does not leave its collector for this: with 460 to 466 in
  // view, the middle of the box (468) is 5px away and off the collector.
  const sliver = { inner: { ...PANEL.inner, left: 460 }, outer: { ...PANEL.outer, left: 459 } };
  assert.equal(busOf('metrics', under(368), { panel: sliver }).trunk, 'M463,266 V294 H468 V339');
  // The same on the other side: the middle of the box (458) is left of it.
  assert.equal(busOf('metrics', under(358), { panel: sliver }).trunk, 'M463,266 V294 H458 V339');
  // A trunk that ends at a dot has no box to line up with: the scan
  // collector's middle (508) stays, 4px from the middle of the boxes.
  const stack = { ...STACK, boxes: STACK.boxes.map((box) => ({ ...box, box: { ...box.box, left: 112, right: 912 } })) };
  assert.equal(busOf('scan', stack).trunk, 'M508,262 V309');
  // In the row, with 381 to 440 of the scan collector in view, its middle
  // (411) is 5px from the middle of the K8sGPT box.
  const narrow = { inner: { ...PANEL.inner, left: 300, right: 440 }, outer: { ...PANEL.outer, left: 299, right: 441 } };
  assert.equal(busOf('scan', ROW, { panel: narrow }).trunk, 'M406,262 V339');
});

test('with no part of a collector in view, the trunk leaves the bottom edge of the panel', () => {
  const beside = { inner: { ...PANEL.inner, left: 500, right: 600 }, outer: { ...PANEL.outer, left: 499, right: 601 } };
  const metrics = busOf('metrics', ROW, { panel: beside });
  assert.equal(metrics.trunk, 'M550,278 V289 H135 V339');
  assert.equal(metrics.collector, 'M272,262 H357');
});

test('a bus with one port has no collector to draw', () => {
  const nodes = [{ box: LOWER, fed: true, ports: { metrics: mark(LOWER) } }];
  const [bus] = wired(ROW, { nodes }).buses;
  assert.deepEqual(bus.drops, ['M369,254 V266']);
  assert.equal(bus.collector, '');
  assert.equal(bus.trunk, 'M369,266 V294 H135 V339');
});

test('the trunk of a box under another one ends at a dot on the top edge of the control plane', () => {
  // Stacked, the first box is still reached. Its middle (410) is 2px from
  // the middle of the collector, so the trunk runs straight down.
  assert.equal(busOf('metrics', STACK).trunk, 'M410,270 V339');
  assert.equal(busOf('metrics', STACK).dot, null);
  const scan = busOf('scan', STACK);
  assert.equal(scan.trunk, 'M508,262 V309');
  assert.equal(scan.head, '');
  assert.deepEqual(scan.dot, { x: 508, y: 309 });
});

test('a link between boxes side by side is a straight run at mid-height, with its label over it', () => {
  assert.deepEqual(linkTo('results'), {
    id: 'results',
    line: 'M541,448 H600',
    head: 'M605,448 l-5,-5 v10 z',
    label: { x: 573, y: 439, align: 'middle' },
  });
});

test('a link past a box in a row runs under it, from bottom edge to bottom edge, with its label on the run', () => {
  // Half way down the room under the boxes (552 to 580).
  assert.deepEqual(linkTo('alerts'), {
    id: 'alerts',
    line: 'M135,552 V566 H705 V557',
    head: 'M705,552 l-5,5 h10 z',
    label: { x: 420, y: 569, align: 'middle' },
  });
});

test('a link between stacked boxes is a straight run from bottom edge to top edge, with its label beside it', () => {
  assert.deepEqual(linkTo('results', STACK), {
    id: 'results',
    line: 'M410,760 V785',
    head: 'M410,790 l-5,-5 h10 z',
    label: { x: 418, y: 778, align: 'start' },
  });
});

test('a link past a box in a stack runs down the gutter left of the boxes, with its label in the first gap', () => {
  // The gutter is between the left edge of the control plane (0) and the boxes (25).
  assert.deepEqual(linkTo('alerts', STACK), {
    id: 'alerts',
    line: 'M25,422 H13 V845 H20',
    head: 'M25,845 l-5,-5 v10 z',
    label: { x: 25, y: 518, align: 'start' },
  });
});

test('a link runs the way it points', () => {
  const back = [{ id: 'results', from: 'slack', to: 'k8sgpt' }];
  assert.deepEqual(linkTo('results', { ...ROW, links: back }), {
    id: 'results',
    line: 'M605,448 H546',
    head: 'M541,448 l5,-5 v10 z',
    label: { x: 573, y: 439, align: 'middle' },
  });
  assert.deepEqual(linkTo('results', { ...STACK, links: back }), {
    id: 'results',
    line: 'M410,790 V765',
    head: 'M410,760 l-5,5 h10 z',
    label: { x: 418, y: 778, align: 'start' },
  });
});

test('a link between boxes that are neither side by side nor stacked has no route', () => {
  const [prometheus, k8sgpt, slack] = ROW.boxes;
  const diagonal = { ...ROW, boxes: [prometheus, k8sgpt, { ...slack, box: { left: 605, right: 805, top: 600, bottom: 700 } }] };
  assert.deepEqual(linkTo('results', diagonal), { id: 'results', line: '', head: '', label: null });
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

test('trunks that overlap side by side between the layers keep their own lanes, the first box highest', () => {
  // With Prometheus moved right, the metrics lane (412 to 455) lies over
  // the scan lane (406 to 508).
  const crossed = {
    ...ROW,
    boxes: [
      reader('prometheus', 'metrics', { left: 355, right: 555, top: 344, bottom: 552 }),
      reader('k8sgpt', 'scan', { left: 271, right: 541, top: 344, bottom: 552 }),
    ],
    links: [],
  };
  const [metrics, scan] = wired(crossed).buses;
  const [, metricsLane] = segments(metrics.trunk);
  const [, scanLane] = segments(scan.trunk);
  assert.ok(metricsLane.y1 < scanLane.y1);
});
