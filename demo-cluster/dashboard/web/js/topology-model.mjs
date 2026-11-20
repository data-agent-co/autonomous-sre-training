// The pure half of the topology card: which expressions it asks Prometheus,
// what the answers mean (deriveTopology) and where the wires run
// (routeWires). Nothing here touches the DOM, so node --test covers every
// rule. topology.mjs draws what these functions return.

import { formatValue } from './format.mjs';

// A container that waits for one of these reasons is starting, not failing.
const STARTING_REASONS = new Set(['ContainerCreating', 'PodInitializing']);

// Memory, in percent of the limit, from which a node counts as degraded.
const MEMORY_DEGRADED_PERCENT = 50;

// Requests per second above which an edge shows traffic.
const MIN_FLOW_RATE = 0.05;

// Share of 5xx answers, in percent, from which traffic turns amber and red.
const ERRORS_MID_PERCENT = 1;
const ERRORS_HIGH_PERCENT = 50;

const PORT_STATE_WORDS = { up: 'up', down: 'down', unknown: 'no data' };

// Queries

// portUpExpr gives the expression that says whether a node's port is up: the
// node's own (metricsUp for the metrics port), else the one every node
// shares (ports.scan.up).
function portUpExpr(spec, node, kind) {
  return node[`${kind}Up`] ?? spec.ports[kind].up;
}

// topologyQueries lists every expression the topology needs, each once.
export function topologyQueries(spec) {
  const exprs = [spec.source.ready];
  for (const node of spec.nodes) {
    exprs.push(node.ready, node.waiting, node.oomKilledNow, node.oomKilled, node.memory, node.restarts);
    exprs.push(node.traffic?.rate, node.traffic?.errors, node.fault?.active);
    for (const kind of node.ports ?? []) exprs.push(portUpExpr(spec, node, kind));
  }
  return [...new Set(exprs.filter((expr) => typeof expr === 'string' && expr !== ''))];
}

// Answers
//
// results is a Map from expression to { samples: [{ labels, value }] } or
// { error }. A query that failed, returned nothing or was never asked gives
// no evidence: by itself it makes nothing look broken and nothing healthy.

function samplesOf(results, expr) {
  return results.get(expr)?.samples ?? null;
}

// valueOf reads a query that answers with one number, or null.
function valueOf(results, expr) {
  return samplesOf(results, expr)?.[0]?.value ?? null;
}

// describeFailures says how many queries failed and why, or is null when
// none did. Queries tend to fail for one reason, so the first message
// stands for all.
export function describeFailures(results) {
  const errors = [...results.values()].flatMap((result) => ('error' in result ? [result.error] : []));
  if (errors.length === 0) return null;
  return `${errors.length} of ${results.size} topology queries failed: ${errors[0]}`;
}

// Nodes

// The texts count in whole numbers, rounded down, so that they agree with
// the thresholds: 49.6% is not yet "50% of limit".
const memoryFact = (percent) => `memory ${Math.floor(percent)}% of limit`;

function restartsFact(restarts) {
  const count = Math.floor(restarts);
  return `${count} ${count === 1 ? 'restart' : 'restarts'} in 5m`;
}

// affected picks the sample to show when several containers of a pod are in
// the same state: the app's own container first, then by name. It is null
// when there is none.
function affected(samples, node) {
  const name = (sample) => sample.labels.container ?? '';
  const byName = [...samples].sort((a, b) => name(a).localeCompare(name(b), 'en'));
  return byName.find((sample) => name(sample) === node.id) ?? byName[0] ?? null;
}

// inContainer names the container when it is not the app's own: a pod can
// carry a sidecar, and the reader should know which of the two it is.
function inContainer(text, sample, node) {
  const { container } = sample.labels;
  return container && container !== node.id ? `${text} (${container})` : text;
}

// nodeStatus gives a node's status and the sub-line that explains it. The
// first rule that applies wins. Only readiness and a container that cannot
// start make a node an error: the app cannot serve then. The other signals
// say that it serves, but something is wrong or was wrong moments ago.
function nodeStatus(node, results, { memory, restarts, faultActive }) {
  const ready = valueOf(results, node.ready);
  if (ready === null) return { status: 'unknown', sub: 'no data' };

  const waiting = samplesOf(results, node.waiting) ?? [];
  const isStarting = (sample) => STARTING_REASONS.has(sample.labels.reason);
  const failing = affected(waiting.filter((sample) => !isStarting(sample)), node);
  const starting = affected(waiting.filter(isStarting), node);
  // down is a container that was killed for its memory and has not started
  // again yet. killed is one that has, moments ago.
  const down = affected(samplesOf(results, node.oomKilledNow) ?? [], node);
  const killed = affected(samplesOf(results, node.oomKilled) ?? [], node);

  if (failing || ready !== 1) {
    const stuck = failing ?? starting;
    if (stuck) return { status: 'error', sub: inContainer(stuck.labels.reason ?? 'waiting', stuck, node) };
    const dead = down ?? killed;
    if (dead) return { status: 'error', sub: inContainer('OOMKilled', dead, node) };
    return { status: 'error', sub: 'not ready' };
  }

  const degraded = (sub) => ({ status: 'degraded', sub });
  if (starting) return degraded(inContainer('starting', starting, node));
  // Ready although a container is down: another pod of the app serves, as
  // it does for a moment during a rollout.
  if (down) return degraded(inContainer('OOMKilled', down, node));
  if (killed) return degraded(inContainer('OOMKilled, restarted', killed, node));
  if (memory !== null && memory >= MEMORY_DEGRADED_PERCENT) return degraded(memoryFact(memory));
  if (restarts !== null && restarts >= 1) return degraded(restartsFact(restarts));
  if (faultActive) return degraded(node.fault.name);
  return { status: 'healthy', sub: node.sub };
}

function deriveFault(fault, active) {
  const name = `${fault.badge} ${fault.name}`;
  return active
    ? { state: 'active', title: `${name}: active` }
    : { state: 'idle', title: `${name}: idle. Trigger it with ${fault.trigger}` };
}

function errorLevel(percent) {
  if (percent === null || percent < ERRORS_MID_PERCENT) return 'none';
  return percent >= ERRORS_HIGH_PERCENT ? 'high' : 'mid';
}

// deriveEdge gives the state of the traffic edge into a node. Traffic to an
// app that cannot serve is blocked, whatever the source does.
function deriveEdge(traffic, status, results, sourceRunning) {
  if (status === 'error') return { state: 'blocked', errors: 'none', label: '✗ down' };
  const rate = valueOf(results, traffic.rate);
  if (!sourceRunning || rate === null || rate <= MIN_FLOW_RATE) return { state: 'idle', errors: 'none', label: '' };
  return {
    state: 'flow',
    errors: errorLevel(valueOf(results, traffic.errors)),
    label: formatValue(rate, 'requests/sec'),
  };
}

// edgeWords says in words what the edge into a node shows. The wires are an
// image that is hidden from a screen reader, so the node's own name carries
// this.
export function edgeWords(edge) {
  if (edge.state === 'blocked') return 'traffic blocked';
  if (edge.state === 'idle') return 'no traffic';
  const errors = { none: '', mid: ', some answers are 5xx', high: ', at least half of the answers are 5xx' };
  return `traffic ${edge.label}${errors[edge.errors]}`;
}

function derivePort(spec, node, kind, results) {
  const up = valueOf(results, portUpExpr(spec, node, kind));
  const state = up === null ? 'unknown' : up === 1 ? 'up' : 'down';
  const name = kind[0].toUpperCase() + kind.slice(1);
  return { kind, state, title: `${name}: ${spec.ports[kind].legend} · ${PORT_STATE_WORDS[state]}` };
}

function deriveNode(spec, node, results, sourceRunning) {
  const memory = valueOf(results, node.memory);
  const restarts = valueOf(results, node.restarts);
  const faultActive = node.fault ? (valueOf(results, node.fault.active) ?? 0) > 0 : false;
  const { status, sub } = nodeStatus(node, results, { memory, restarts, faultActive });

  const facts = [];
  if (memory !== null) facts.push(memoryFact(memory));
  if (restarts !== null) facts.push(restartsFact(restarts));
  // The sub-line explains every status but healthy. It is left out where a
  // fact says the same.
  const reason = status === 'healthy' || facts.includes(sub) ? [] : [sub];

  return {
    status,
    sub,
    title: `${node.label}: ${[status, ...reason, ...facts].join(' · ')}`,
    fault: node.fault ? deriveFault(node.fault, faultActive) : null,
    edge: node.traffic ? deriveEdge(node.traffic, status, results, sourceRunning) : null,
    ports: (node.ports ?? []).map((kind) => derivePort(spec, node, kind, results)),
  };
}

// Traffic source

// trafficFact is one node's share of the traffic, for the source's title.
function trafficFact(node, results) {
  const rate = valueOf(results, node.traffic.rate);
  if (rate === null) return `${node.label}: no data`;
  const errors = valueOf(results, node.traffic.errors);
  const share = errors === null ? '' : `, ${formatValue(errors, 'percent')} 5xx`;
  return `${node.label}: ${formatValue(rate, 'requests/sec')}${share}`;
}

function deriveSource(spec, results, nodes) {
  const { source } = spec;
  const name = source.sub ? `${source.label} (${source.sub})` : source.label;
  const ready = valueOf(results, source.ready);
  if (ready === null) return { state: 'unknown', errors: 'none', sub: 'no data', title: `${name}: no data` };
  if (ready !== 1) {
    return { state: 'idle', errors: 'none', sub: 'idle', title: `${name}: idle · no pod is ready` };
  }

  const fed = spec.nodes.filter((node) => node.traffic);
  const rates = fed.map((node) => valueOf(results, node.traffic.rate)).filter((rate) => rate !== null);
  // One decimal, always: the sum of two rates that move would otherwise
  // change its width on every refresh.
  const total = rates.length > 0 ? formatValue(rates.reduce((sum, rate) => sum + rate, 0), 'requests/sec', 1) : null;
  const levels = nodes.map((node) => node.edge?.errors);
  return {
    state: 'running',
    errors: levels.includes('high') ? 'high' : levels.includes('mid') ? 'mid' : 'none',
    sub: total ?? 'running',
    title: [`${name}: running`, total, ...fed.map((node) => trafficFact(node, results))]
      .filter((part) => part !== null)
      .join(' · '),
  };
}

// deriveTopology turns the answers into what the card shows:
//
//   source  { state: 'running' | 'idle' | 'unknown', errors, sub, title }
//   nodes   [{ status: 'healthy' | 'degraded' | 'error' | 'unknown', sub, title,
//              fault: { state: 'active' | 'idle', title } | null,
//              edge: { state: 'blocked' | 'flow' | 'idle', errors, label } | null,
//              ports: [{ kind, state: 'up' | 'down' | 'unknown', title }] }]
//   ports   [{ kind, up }], one per port kind: whether any node's port of
//           that kind is up
//
// errors is 'none', 'mid' or 'high': the share of 5xx answers in the traffic.
export function deriveTopology(spec, results) {
  const sourceRunning = valueOf(results, spec.source.ready) === 1;
  const nodes = spec.nodes.map((node) => deriveNode(spec, node, results, sourceRunning));
  return {
    source: deriveSource(spec, results, nodes),
    nodes,
    ports: Object.keys(spec.ports).map((kind) => ({
      kind,
      up: nodes.some((node) => node.ports.some((port) => port.kind === kind && port.state === 'up')),
    })),
  };
}

// Wires
//
// routeWires works in whole px, in the coordinates of the element the wires
// are drawn over. A box is { left, right, top, bottom }.

// An arrowhead is this long and twice as tall.
const ARROW = 5;
// The baseline of an edge's label above its line.
const LABEL_RISE = 9;

const middle = (a, b) => Math.round((a + b) / 2);

// routeEdges draws one stem out of the source's right edge and a branch from
// its end into the left edge of every node that gets traffic.
function routeEdges(source, targets) {
  if (targets.length === 0) return { stem: '', edges: [] };
  const y = middle(source.top, source.bottom);
  const forkX = source.right + Math.round((Math.min(...targets.map((box) => box.left)) - source.right) / 3);
  const edges = targets.map((box) => {
    const cy = middle(box.top, box.bottom);
    // The line stops where the arrowhead starts, and the arrowhead's tip
    // touches the node.
    const run = `V${cy} H${box.left - ARROW}`;
    return {
      line: `M${forkX},${y} ${run}`,
      head: `M${box.left},${cy} l${-ARROW},${-ARROW} v${2 * ARROW} z`,
      label: { x: middle(forkX, box.left), y: cy - LABEL_RISE },
      // The path a dot travels from the source to the node.
      motion: `M${source.right},${y} H${forkX} ${run}`,
    };
  });
  // The branches are drawn over the stem's last pixel. Stopping one short
  // keeps a translucent line from showing a darker spot there.
  return { stem: `M${source.right},${y} H${forkX - 1}`, edges };
}

// routeLink joins the nodes that get traffic to the nodes that get none:
// a run out of the middle of the right side of each node that gets traffic,
// a spine halfway across the gap, and a run into the middle of the left
// side of each other node. It is one path, so where its runs meet the
// spine a translucent line shows no darker spot. It is empty while either
// side has no node.
function routeLink(fed, others) {
  if (fed.length === 0 || others.length === 0) return '';
  const spineX = middle(Math.max(...fed.map((box) => box.right)), Math.min(...others.map((box) => box.left)));
  const outs = fed.map((box) => ({ x: box.right, y: middle(box.top, box.bottom) }));
  const ins = others.map((box) => ({ x: box.left, y: middle(box.top, box.bottom) }));
  const ys = [...outs, ...ins].map((point) => point.y);
  return [
    ...outs.map(({ x, y }) => `M${x},${y} H${spineX}`),
    `M${spineX},${Math.min(...ys)} V${Math.max(...ys)}`,
    ...ins.map(({ x, y }) => `M${spineX},${y} H${x}`),
  ].join(' ');
}

// routeWires places every wire of the card:
//
//   source  the traffic source's box
//   nodes   [{ box, fed }]; fed says the node gets traffic
//
// It returns SVG path data: stem and edges[] ({ line, head, label, motion })
// for the traffic, and link, which joins the nodes that get traffic to the
// others.
export function routeWires({ source, nodes }) {
  const boxes = (fed) => nodes.filter((node) => node.fed === fed).map((node) => node.box);
  return { ...routeEdges(source, boxes(true)), link: routeLink(boxes(true), boxes(false)) };
}
