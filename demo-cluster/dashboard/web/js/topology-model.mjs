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

// The states a stage or a row of the control plane is asked for, in the
// order they are tried. A link is asked for active and ok only.
const ASKED_STATES = ['failed', 'active', 'ok', 'set'];
const LINK_STATES = ['active', 'ok'];

// stateExprs lists the expressions that give a stage or a row its state.
function stateExprs(entry) {
  return [entry.up, ...ASKED_STATES.map((state) => entry[state])];
}

function linkExprs(link) {
  return LINK_STATES.map((state) => link[state]);
}

function controlPlaneExprs(plane) {
  const entries = plane.boxes.flatMap((box) => [...box.stages, ...box.rows]);
  const lists = plane.boxes.map((box) => box.list);
  return [
    ...entries.flatMap(stateExprs),
    ...plane.links.flatMap(linkExprs),
    ...[...entries, ...plane.links, ...lists].map((asked) => asked?.needs),
    ...entries.map((entry) => entry.info?.expr),
    ...lists.map((list) => list?.expr),
  ];
}

// topologyQueries lists every expression the topology needs, each once.
export function topologyQueries(spec) {
  const exprs = [spec.source.ready];
  for (const node of spec.nodes) {
    exprs.push(node.ready, node.waiting, node.oomKilledNow, node.oomKilled, node.memory, node.restarts);
    exprs.push(node.traffic?.rate, node.traffic?.errors, node.fault?.active);
    for (const kind of node.ports ?? []) exprs.push(portUpExpr(spec, node, kind));
  }
  if (spec.controlPlane) exprs.push(...controlPlaneExprs(spec.controlPlane));
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

// Control plane
//
// A stage and a row each have up to four expressions, and the first that
// answers a value above 0 gives the state: failed, then active, then ok,
// then set. With none above 0 the entry is idle. Ok is green and needs
// evidence that the thing works; set is neutral: the thing is configured,
// and nothing says yet whether it works. `up` is short for the health of one
// thing: a value above 0 is ok, any other value is failed, no sample is
// idle. A link reads active (live) and ok (ready) only, else it is off.
//
// Idle is what is left when nothing answers above 0, so by itself it proves
// little: Prometheus has no sample for a relay that does not exist, and none
// for one it cannot see. `needs` names what an idle reading leans on, as an
// expression that is above 0 while the source that would report the thing is
// there. Without it the entry says "no data", not "off".

// The word a title has for each state, unless the entry has a text of its
// own for that state (failedText, activeText, idleText).
const STATE_WORDS = { failed: 'down', active: 'active', ok: 'ok', set: 'set', idle: 'idle' };
// The word of an idle entry that has nothing to go by.
const NO_DATA = PORT_STATE_WORDS.unknown;
const LINK_WORDS = { live: 'live', ready: 'ready', off: 'off' };

const isAbove = (results, expr) => (valueOf(results, expr) ?? 0) > 0;

function entryState(entry, results) {
  if (entry.up !== undefined) {
    const up = valueOf(results, entry.up);
    if (up === null) return 'idle';
    return up > 0 ? 'ok' : 'failed';
  }
  return ASKED_STATES.find((state) => isAbove(results, entry[state])) ?? 'idle';
}

// knowsIdle says whether an idle reading of an entry means something. Idle
// is what is left when no state query answers above 0, so every one of them
// must have answered: one that failed might have said otherwise. And what
// the reading leans on (needs) must be there.
function knowsIdle(entry, results, exprs = stateExprs(entry)) {
  const asked = exprs.filter((expr) => expr !== undefined);
  if (asked.length === 0 || asked.some((expr) => samplesOf(results, expr) === null)) return false;
  return entry.needs === undefined || isAbove(results, entry.needs);
}

// ownText is the text an entry has for the state it is in, or null. An idle
// text such as "off" says something about the cluster, so it needs evidence
// behind it: with every query failed, nobody knows that Slack is off.
function ownText(entry, state, results) {
  if (state === 'idle' && !knowsIdle(entry, results)) return null;
  return entry[`${state}Text`] ?? null;
}

function stateWord(entry, state, results) {
  const own = ownText(entry, state, results);
  if (own !== null) return own;
  // A health check without a sample knows nothing, and so does any entry
  // whose idle reading has no evidence behind it.
  if (state === 'idle' && (entry.up !== undefined || !knowsIdle(entry, results))) return NO_DATA;
  return STATE_WORDS[state];
}

// entryTitle joins a label, the word for its state and a detail, like the
// node titles. A detail that only repeats the word is left out.
function entryTitle(label, word, detail) {
  return `${label}: ${detail && detail !== word ? `${word} · ${detail}` : word}`;
}

function deriveStage(stage, results) {
  const state = entryState(stage, results);
  const word = stateWord(stage, state, results);
  return { id: stage.id, label: stage.label, state, title: entryTitle(stage.label, word, stage.title) };
}

// infoText fills a row's info text from the first sample of its query:
// {label} is that label's value, {value} the sample's value. It is null
// when the query gave no sample.
function infoText(info, results) {
  const sample = info ? samplesOf(results, info.expr)?.[0] : undefined;
  if (!sample) return null;
  return info.text.replace(/\{([^{}]+)\}/g, (_, name) =>
    name === 'value' ? formatValue(sample.value, 'decimal') : (sample.labels[name] ?? ''),
  );
}

// deriveRow gives a row and the word for its state. The sub-line is the
// row's own text for the state, else what its info query says, else its
// plain text. A row that knows nothing says so there: its plain text is
// what it does when it works, which nobody can tell then.
function deriveRow(row, results) {
  const state = entryState(row, results);
  const word = stateWord(row, state, results);
  const blind = word === NO_DATA ? NO_DATA : null;
  const text = ownText(row, state, results) ?? blind ?? infoText(row.info, results) ?? row.text ?? '';
  return { row: { id: row.id, label: row.label, text, state, title: entryTitle(row.label, word, text) }, word };
}

const byText = (a, b) => a.localeCompare(b, 'en');

// deriveList turns the samples of a list query into rows, sorted by kind and
// then by object. A query that failed gives a count of null: no evidence is
// not "none". Neither is an empty answer while what the list needs is not
// there.
function deriveList(list, results) {
  const samples = samplesOf(results, list.expr);
  const blind = samples?.length === 0 && list.needs !== undefined && !isAbove(results, list.needs);
  if (samples === null || blind) return { count: null, summary: NO_DATA, rows: [], more: 0, empty: list.empty };

  const found = samples
    .map(({ labels }) => ({
      kind: labels.kind ?? '',
      object: labels.object ?? '',
      backend: labels.backend ?? '',
      name: labels.name ?? '',
    }))
    .sort((a, b) => byText(a.kind, b.kind) || byText(a.object, b.object));
  const perKind = new Map();
  for (const { kind } of found) perKind.set(kind, (perKind.get(kind) ?? 0) + 1);
  const kinds = [...perKind].map(([kind, count]) => `${kind} ${count}`).join(', ');

  // The tag is the backend the Result names. tagText says what that is.
  const tag = (backend) => (list.tagText ? `backend ${backend}: ${list.tagText}` : `backend ${backend}`);
  const rows = found.slice(0, list.limit).map(({ kind, object, backend, name }) => {
    const facts = [`${kind} ${object}`, name && `Result ${name}`, backend && tag(backend)];
    return { kind, object, backend, title: facts.filter(Boolean).join(' · ') };
  });
  return {
    count: found.length,
    summary: found.length === 0 ? 'none' : `${found.length} in all · ${kinds}`,
    rows,
    more: found.length - rows.length,
    empty: list.empty,
  };
}

// A link is live while its active expression is above 0 (a Result exists,
// an alert fires), ready while the path exists and off otherwise. Live says
// that there is something the path carries, not that it is being sent now:
// activeText names it. Like an idle text, the word "off" needs evidence
// behind it.
function knowsOff(link, results) {
  return knowsIdle(link, results, linkExprs(link));
}

function liveWord(link) {
  return link.activeText ?? LINK_WORDS.live;
}

function deriveLink(link, results) {
  const state = isAbove(results, link.active) ? 'live' : isAbove(results, link.ok) ? 'ready' : 'off';
  const word = state === 'live' ? liveWord(link) : state === 'ready' || knowsOff(link, results) ? LINK_WORDS[state] : NO_DATA;
  return { id: link.id, label: link.label, state, title: entryTitle(link.label, word, link.title) };
}

// linkWords says what the links into a box carry. The wires are an image
// that is hidden from a screen reader, so the box's own name carries this.
function linkWords(links, results) {
  const labels = (pick) => links.filter(pick).map(({ link }) => link.label).join(' and ');
  const live = links.filter(({ link }) => link.state === 'live').map(({ link, spec }) => `${link.label} ${liveWord(spec)}`);
  const ready = labels(({ link }) => link.state === 'ready');
  const off = labels(({ link, spec }) => link.state === 'off' && knowsOff(spec, results));
  const unknown = labels(({ link, spec }) => link.state === 'off' && !knowsOff(spec, results));
  return [
    ...live,
    ready && `ready for ${ready}`,
    off && `${off} off`,
    unknown && `${NO_DATA} for ${unknown}`,
  ].filter(Boolean);
}

// boxTitle sums a box up: the rows that are not ok, the count of its list
// and what reaches it. A box that is off says only why, in the words of its
// first row.
function boxTitle(box, { rows, list, off }, reached) {
  if (off) return `${box.label}: ${rows[0]?.word ?? STATE_WORDS.idle}`;
  const notOk = rows.filter(({ row }) => row.state !== 'ok').map(({ row, word }) => `${row.label} ${word}`);
  const count = list && `${box.list.title}: ${list.count === null ? NO_DATA : list.count || 'none'}`;
  return `${box.label}: ${[...(notOk.length > 0 ? notOk : ['ok']), count, ...reached].filter(Boolean).join(' · ')}`;
}

function deriveBox(box, results, reached) {
  const stages = box.stages.map((stage) => deriveStage(stage, results));
  const rows = box.rows.map((row) => deriveRow(row, results));
  const list = box.list ? deriveList(box.list, results) : null;
  // With nothing in it that works or fails, the box is drawn greyed.
  const off = [...stages, ...rows.map(({ row }) => row)].every(({ state }) => state === 'idle');
  return {
    id: box.id,
    title: boxTitle(box, { rows, list, off }, reached),
    stages,
    rows: rows.map(({ row }) => row),
    list,
    off,
  };
}

function deriveControlPlane(plane, results) {
  const links = plane.links.map((spec) => ({ spec, link: deriveLink(spec, results) }));
  const reached = (box) => linkWords(links.filter(({ spec }) => spec.to === box.id), results);
  return {
    boxes: plane.boxes.map((box) => deriveBox(box, results, reached(box))),
    links: links.map(({ link }) => link),
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
//   controlPlane  null without one in the spec, else
//     boxes  [{ id, title, off,
//               stages: [{ id, label, state, title }],
//               rows:   [{ id, label, text, state, title }],
//               list:   { count, summary, rows: [{ kind, object, backend, title }], more, empty } | null }]
//     links  [{ id, label, state: 'live' | 'ready' | 'off', title }]
//
// errors is 'none', 'mid' or 'high': the share of 5xx answers in the traffic.
// The state of a stage and of a row is 'failed', 'active', 'ok', 'set' or 'idle'.
// A box is off while every one of its stages and rows is idle. A list has a
// count of null while nothing says how many there are: its query failed, or
// it answered nothing and what the list needs is not there.
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
    controlPlane: spec.controlPlane ? deriveControlPlane(spec.controlPlane, results) : null,
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

// arrowhead gives the head whose tip is at (x, y), pointing the given way.
// Its line stops ARROW before the tip.
function arrowhead(x, y, toward) {
  if (toward === 'right') return `M${x},${y} l${-ARROW},${-ARROW} v${2 * ARROW} z`;
  if (toward === 'left') return `M${x},${y} l${ARROW},${-ARROW} v${2 * ARROW} z`;
  if (toward === 'down') return `M${x},${y} l${-ARROW},${-ARROW} h${2 * ARROW} z`;
  return `M${x},${y} l${-ARROW},${ARROW} h${2 * ARROW} z`;
}

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
      head: arrowhead(box.left, cy, 'right'),
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

// The wires of the control plane
//
// Nothing here knows a size from the style sheet. The lines share out the
// room they find between the boxes: css/topology.css says how much that is.

// A line that steps out beside a node keeps this far from the node, and the
// next line of the same node this far from the first.
const STEP_ASIDE = 12;
// The run of such a line keeps this far above the node it steps around, and
// the next line's run this far above the first.
const STEP_ABOVE = 6;
// A port is above a node that reaches within this distance of its middle.
const ABOVE_REACH = 4;
// A trunk that starts within this distance of the middle of its box starts
// right above that middle, and so runs straight down.
const TRUNK_SNAP = 8;
// A label next to an upright line starts this far from it.
const LABEL_ASIDE = 8;
// The baseline of a label below the height it is centred on.
const LABEL_DROP = 3;

// share gives the point that is `part` of `parts` of the way from a to b.
const share = (a, b, part, parts) => a + Math.round(((b - a) * part) / parts);

const overlapX = (a, b) => a.left < b.right && b.left < a.right;
const overlapY = (a, b) => a.top < b.bottom && b.top < a.bottom;

// portLines gives, for the ports of one node, where each line starts and
// where it drops: { kind, x, start }. A line drops straight from its port. A
// port with another node below it would run through that node, so its line
// steps out beside the node first, on the side of the box that reads it
// (toward[kind] is the middle of that box). A line steps out to the left
// only when the lines of the ports left of it do too, so that none crosses
// another. The lines on one side nest: the port nearest that side's edge
// turns lowest and drops nearest.
function portLines(node, others, kinds, toward) {
  const ports = kinds
    .filter((kind) => node.ports?.[kind])
    .map((kind) => ({ kind, box: node.ports[kind], x: middle(node.ports[kind].left, node.ports[kind].right) }))
    .sort((a, b) => a.x - b.x);
  const below = ({ box, x }) =>
    others.filter(
      (other) => other.bottom > box.bottom && x >= other.left - ABOVE_REACH && x <= other.right + ABOVE_REACH,
    );
  const stepped = ports.filter((port) => below(port).length > 0);
  const lefts = stepped.findIndex((port) => !(toward[port.kind] < port.x));
  const leftward = stepped.slice(0, lefts === -1 ? stepped.length : lefts).reverse();
  const rightward = stepped.filter((port) => !leftward.includes(port));

  const sides = [
    { lines: leftward, sign: -1, edge: (boxes) => Math.min(...boxes.map((other) => other.left)) },
    { lines: rightward, sign: 1, edge: (boxes) => Math.max(...boxes.map((other) => other.right)) },
  ];
  const turns = new Map();
  for (const { lines, sign, edge } of sides) {
    if (lines.length === 0) continue;
    const inTheWay = lines.flatMap(below);
    const beside = edge(inTheWay);
    const ceiling = Math.min(...inTheWay.map((other) => other.top));
    lines.forEach((port, index) => {
      const turn = index + 1;
      turns.set(port, { x: beside + sign * STEP_ASIDE * turn, runY: Math.max(port.box.bottom, ceiling - STEP_ABOVE * turn) });
    });
  }

  return ports.map((port) => {
    const start = `M${port.x},${port.box.bottom}`;
    const turn = turns.get(port);
    if (!turn) return { kind: port.kind, x: port.x, start };
    return { kind: port.kind, x: turn.x, start: `${start} V${turn.runY} H${turn.x}` };
  });
}

// pathLength measures path data made of M, H and V, as routeBuses writes it.
function pathLength(path) {
  let length = 0;
  let [x, y] = [0, 0];
  for (const [, command, numbers] of path.matchAll(/([MHV])([^MHV]*)/g)) {
    const [first, second] = numbers.split(/[ ,]+/).filter(Boolean).map(Number);
    if (command === 'M') [x, y] = [first, second];
    else if (command === 'H') [length, x] = [length + Math.abs(first - x), first];
    else [length, y] = [length + Math.abs(first - y), first];
  }
  return length;
}

// busPhases gives the dash offset of every drop and of each run of the
// collector, so that the dashes of a bus run on without a break from its
// ports into its trunk, which starts at offset 0 where it leaves the
// collector at x. Each piece is offset by minus what is left of the way to
// that point: drops that share a stretch of the collector then dash it in
// the same places. With no trunk on the collector (x is null) the pieces do
// not meet, and each keeps its own dashes.
function busPhases(drops, collector, x) {
  const runs = collector === '' ? [] : collector.split(/ (?=M)/);
  if (x === null) return { drops: drops.map(() => 0), collector: runs.map(() => 0) };
  return {
    drops: drops.map((drop) => -(pathLength(drop) + Math.abs(x - lastX(drop)))),
    collector: runs.map((run) => -pathLength(run)),
  };
}

// lastX is the x where path data made of M, H and V ends.
function lastX(path) {
  let x = 0;
  for (const [, command, numbers] of path.matchAll(/([MHV])([^MHV]*)/g)) {
    const [first] = numbers.split(/[ ,]+/).filter(Boolean).map(Number);
    if (command === 'M' || command === 'H') x = first;
  }
  return x;
}

// routeBuses draws one bus per kind of port that a box of the control plane
// reads: a drop from every port of the kind to a collector line near the app
// panel's bottom edge, and a trunk from the collector into the top of the
// box.
//
// The buses are laid out in the order of their boxes. Where collectors or
// trunks overlap side by side, the bus of the first box has the lowest
// collector and the highest lane between the layers: its trunk then starts below the other collector and has turned towards its box
// above the other lane. In a row the first box is the leftmost, and it reads
// the left mark of each node, so the second trunk comes down to the right of
// all of that: neither trunk crosses the other bus's collector or lane. With
// the heights the other way round, the first trunk would come down through
// the second collector.
function routeBuses(nodes, panel, plane) {
  const readers = plane.boxes.filter(
    (reader, index, all) =>
      reader.reads &&
      all.findIndex((other) => other.reads === reader.reads) === index &&
      nodes.some((node) => node.ports?.[reader.reads]),
  );
  if (readers.length === 0) return [];
  const kinds = readers.map((reader) => reader.reads);

  const toward = Object.fromEntries(readers.map((reader) => [reader.reads, middle(reader.box.left, reader.box.right)]));
  const lines = nodes.flatMap((node) => {
    const others = nodes.filter((other) => other !== node).map((other) => other.box);
    return portLines(node, others, kinds, toward);
  });
  // The collectors share the room under everything the panel shows.
  const lowest = Math.max(
    ...nodes.flatMap((node) => [node.box.bottom, ...kinds.map((kind) => node.ports?.[kind]?.bottom ?? -Infinity)]),
  );
  const { inner, outer } = panel;
  const spans = readers.map((reader) => {
    const xs = lines.filter((line) => line.kind === reader.reads).map((line) => line.x);
    return { left: Math.min(...xs), right: Math.max(...xs) };
  });
  // Collectors that do not overlap side by side cannot cross each other's
  // drops or trunks, so they all run at the highest height.
  const apart = spans.every((a, i) => spans.every((b, j) => i === j || a.right < b.left || b.right < a.left));

  const routes = readers.map((reader, rank) => {
    const mine = lines.filter((line) => line.kind === reader.reads);
    const y = share(inner.bottom, lowest, apart ? readers.length : rank + 1, readers.length + 1);
    const { left, right } = spans[rank];

    // A box under another one, as when the boxes are stacked, cannot be
    // reached without crossing the box above it.
    const target = reader.box;
    const covered = plane.boxes.some(
      (other) => other !== reader && other.box.bottom <= target.top && overlapX(other.box, target),
    );
    const into = middle(target.left, target.right);

    // The panel scrolls sideways on a narrow screen. The trunk leaves from
    // the middle of the part of the collector that is in view. With none in
    // view it leaves the panel's bottom edge, so the bus still shows.
    const from = Math.max(left, inner.left);
    const to = Math.min(right, inner.right);
    const inView = from <= to;
    const centre = inView ? middle(from, to) : middle(inner.left, inner.right);
    // A trunk that would step sideways by a few px on its way down looks
    // like a mistake. It leaves the collector right above its box instead,
    // where the collector in view reaches that far.
    const straight = !covered && Math.abs(centre - into) <= TRUNK_SNAP && into >= from && into <= to;
    const x = straight ? into : centre;
    const start = `M${x},${inView ? y : inner.bottom}`;
    // Two runs towards the trunk, so that the dashes of a bus that flows
    // move the way its data does.
    const collector = inView
      ? [left < x && `M${left},${y} H${x}`, right > x && `M${right},${y} H${x}`].filter(Boolean).join(' ')
      : left < right
        ? `M${left},${y} H${right}`
        : '';
    const drops = mine.map((line) => `${line.start} V${y}`);
    const bus = { kind: reader.reads, drops, collector, phases: busPhases(drops, collector, inView ? x : null) };
    // Where the trunk runs between the layers, side to side.
    const reach = covered ? { left: x, right: x } : { left: Math.min(x, into), right: Math.max(x, into) };
    return { bus, rank, target, covered, into, x, start, reach };
  });
  // Trunks that do not overlap side by side between the layers cannot cross
  // each other, so their lanes all run half way between the layers.
  const lanesApart = routes.every(
    (a, i) => routes.every((b, j) => i === j || a.reach.right < b.reach.left || b.reach.right < a.reach.left),
  );

  return routes.map(({ bus, rank, target, covered, into, x, start }) => {
    // The trunk of a box that cannot be reached ends at a dot on the control
    // plane's top edge.
    if (covered) return { ...bus, trunk: `${start} V${plane.box.top}`, head: '', dot: { x, y: plane.box.top } };

    const lane = lanesApart
      ? share(outer.bottom, plane.box.top, 1, 2)
      : share(outer.bottom, plane.box.top, rank + 1, readers.length + 1);
    const end = target.top - ARROW;
    return {
      ...bus,
      trunk: x === into ? `${start} V${end}` : `${start} V${lane} H${into} V${end}`,
      head: arrowhead(into, target.top, 'down'),
      dot: null,
    };
  });
}

// linkAcross joins two boxes that stand side by side. With nothing between
// them the link is a straight run at mid-height, with its label over it.
// With a box between them it leaves the bottom edge, runs under the boxes
// and comes up into the other bottom edge, and its label sits on that run.
function linkAcross(from, to, others, plane) {
  const forward = from.right <= to.left;
  const [near, far] = forward ? [from.right, to.left] : [from.left, to.right];
  const y = middle(Math.max(from.top, to.top), Math.min(from.bottom, to.bottom));
  const [gapLeft, gapRight] = [Math.min(near, far), Math.max(near, far)];
  const between = others.filter(
    (other) => other.left < gapRight && other.right > gapLeft && other.top < y && other.bottom > y,
  );
  if (between.length === 0) {
    return {
      line: `M${near},${y} H${forward ? far - ARROW : far + ARROW}`,
      head: arrowhead(far, y, forward ? 'right' : 'left'),
      label: { x: middle(near, far), y: y - LABEL_RISE, align: 'middle' },
    };
  }
  const out = middle(from.left, from.right);
  const into = middle(to.left, to.right);
  const floor = Math.max(from.bottom, to.bottom, ...between.map((other) => other.bottom));
  const lane = middle(floor, plane.row.bottom);
  return {
    line: `M${out},${from.bottom} V${lane} H${into} V${to.bottom + ARROW}`,
    head: arrowhead(into, to.bottom, 'up'),
    label: { x: middle(out, into), y: lane + LABEL_DROP, align: 'middle' },
  };
}

// linkDown joins two boxes of which one stands above the other. With nothing
// between them the link is a straight run from edge to edge, with its label
// beside it. With a box between them it runs down the gutter left of the
// boxes, and its label stands in the gap after the box it leaves.
function linkDown(from, to, others, plane) {
  const down = from.bottom <= to.top;
  const [upper, lower] = down ? [from, to] : [to, from];
  const x = middle(Math.max(from.left, to.left), Math.min(from.right, to.right));
  const between = others.filter(
    (other) => other.top < lower.top && other.bottom > upper.bottom && other.left < x && other.right > x,
  );
  if (between.length === 0) {
    const [near, far] = down ? [from.bottom, to.top] : [from.top, to.bottom];
    return {
      line: `M${x},${near} V${down ? far - ARROW : far + ARROW}`,
      head: arrowhead(x, far, down ? 'down' : 'up'),
      label: { x: x + LABEL_ASIDE, y: middle(near, far) + LABEL_DROP, align: 'start' },
    };
  }
  const gutter = middle(plane.box.left, Math.min(from.left, to.left, ...between.map((other) => other.left)));
  const outY = middle(from.top, from.bottom);
  const intoY = middle(to.top, to.bottom);
  const gap = down
    ? middle(from.bottom, Math.min(...between.map((other) => other.top)))
    : middle(from.top, Math.max(...between.map((other) => other.bottom)));
  return {
    line: `M${from.left},${outY} H${gutter} V${intoY} H${to.left - ARROW}`,
    head: arrowhead(to.left, intoY, 'right'),
    label: { x: from.left, y: gap + LABEL_DROP, align: 'start' },
  };
}

// routeLinks draws the links between the boxes of the control plane. A link
// between boxes that stand neither side by side nor one above the other has
// no route, and no label.
function routeLinks(plane) {
  const boxOf = (id) => plane.boxes.find((box) => box.id === id).box;
  return plane.links.map(({ id, from, to }) => {
    const [a, b] = [boxOf(from), boxOf(to)];
    const others = plane.boxes.filter((box) => box.id !== from && box.id !== to).map((box) => box.box);
    if (overlapY(a, b) && !overlapX(a, b)) return { id, ...linkAcross(a, b, others, plane) };
    if (overlapX(a, b) && !overlapY(a, b)) return { id, ...linkDown(a, b, others, plane) };
    return { id, line: '', head: '', label: null };
  });
}

// routeWires places every wire of the card:
//
//   source        the traffic source's box
//   nodes         [{ box, fed, ports }]; fed says the node gets traffic,
//                 ports holds the box of each of its port marks by kind
//   panel         { inner, outer }: the app panel's box without and with its
//                 border and scrollbar. inner is what is in view
//   controlPlane  null or absent without one, else { box, row, boxes, links }:
//                 the layer's own box, the box of its row of boxes with the
//                 room under them, boxes [{ id, box, reads }] in the order
//                 of the spec, and links [{ id, from, to }]
//
// It returns SVG path data: stem and edges[] ({ line, head, label, motion })
// for the traffic, and link, which joins the nodes that get traffic to the
// others. For the control plane it adds
//
//   buses  [{ kind, drops: [path], collector, trunk, head, dot }], one per
//          kind of port that a box reads. head is the arrowhead into that
//          box. A trunk that cannot reach its box has no head and ends at
//          dot { x, y }, which is null otherwise.
//   links  [{ id, line, head, label }]; label is { x, y, align }, with align
//          'middle' or 'start', or null for a link without a route
export function routeWires({ source, nodes, panel, controlPlane = null }) {
  const boxes = (fed) => nodes.filter((node) => node.fed === fed).map((node) => node.box);
  return {
    ...routeEdges(source, boxes(true)),
    link: routeLink(boxes(true), boxes(false)),
    buses: controlPlane ? routeBuses(nodes, panel, controlPlane) : [],
    links: controlPlane ? routeLinks(controlPlane) : [],
  };
}
