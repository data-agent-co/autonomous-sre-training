// The topology card, in two layers. The demo apps: boxes, the traffic that
// reaches them, the ports they are read through and their fault badges. The
// control plane under them: what reads the apps and where its findings go.
// The DOM is built once. Every refresh changes attributes and text in
// place, which keeps the animations running. topology-model.mjs holds the
// rules for what is shown; css/topology.css draws each state.

import { clear, el, svgEl } from './dom.mjs';
import { icon } from './icons.mjs';
import { renderMarkdown } from './markdown.mjs';
import { deriveTopology, describeFailures, edgeWords, routeWires, topologyQueries } from './topology-model.mjs';

// Where the reader's choice to keep the notes open is stored.
const NOTES_KEY = 'demo-dashboard-notes';

const FIGURE_SIZE = 20;
const PORT_ICON_SIZE = 13;
const KEY_ICON_SIZE = 12;
const FLOW_DOT_RADIUS = 4;
const MARK_SIZE = 18;
const ROW_ICON_SIZE = 16;
const STEP_ICON_SIZE = 11;
const ACCESS_ICON_SIZE = 11;
const BUS_END_RADIUS = 3.5;

// An attribute or a text is written only when it changes, so a tooltip that
// is showing and a text the reader has selected survive a refresh.
function setAttr(node, name, value) {
  if (node.getAttribute(name) !== value) node.setAttribute(name, value);
}

function setText(node, text) {
  if (node.textContent !== text) node.textContent = text;
}

// What a state says in words goes to the hover text and to the name a
// screen reader reads.
function setTitle(node, title) {
  setAttr(node, 'title', title);
  setAttr(node, 'aria-label', title);
}

function readNotesOpen() {
  try {
    return localStorage.getItem(NOTES_KEY) === 'open';
  } catch {
    // The browser blocks storage for this page: the notes start closed.
    return false;
  }
}

function storeNotesOpen(open) {
  try {
    localStorage.setItem(NOTES_KEY, open ? 'open' : 'closed');
  } catch {
    // Without storage the choice lasts until the page is reloaded.
  }
}

// Building the card. Each builder returns the elements a refresh changes.

function figure(name) {
  const symbol = icon(name, FIGURE_SIZE);
  symbol.classList.add('topo-icon');
  return symbol;
}

// The source, the nodes and the boxes of the control plane are groups that
// the keyboard can reach. render() names each with its whole state, because
// a hover text is out of reach of a keyboard, a touch screen and a screen
// reader.
const BOX = { role: 'group', tabindex: 0 };

function buildSource(source) {
  const sub = el('div', { class: 'topo-sub' });
  const root = el(
    'div',
    { class: 'topo-src', ...BOX },
    figure(source.icon),
    el('div', { class: 'topo-label', text: source.label }),
    sub,
  );
  return { root, sub };
}

// The edge into a node that gets traffic: the line with its arrowhead, the
// label over it and the dot that travels along it.
function buildEdge() {
  const line = svgEl('path', { class: 'topo-edge-run' });
  const head = svgEl('path', { class: 'topo-edge-head' });
  const label = svgEl('text', { class: 'topo-edge-label' });
  const dot = svgEl('circle', { class: 'topo-edge-dot', r: FLOW_DOT_RADIUS });
  // The line and its head are one group, so they fade as one shape.
  const root = svgEl('g', { class: 'topo-edge' }, svgEl('g', { class: 'topo-edge-line' }, line, head), label, dot);
  return { root, line, head, label, dot };
}

// A node's box, and its edge when the node gets traffic. The edge is drawn
// with the other wires, not inside the box.
function buildNode(node) {
  const sub = el('div', { class: 'topo-sub' });
  // A badge says "F1" and shows its state as a colour, and a port shows an
  // icon only, so each is an image with a name of its own.
  const badge = node.fault ? el('div', { class: 'topo-badge', role: 'img', text: node.fault.badge }) : null;
  const ports = (node.ports ?? []).map((kind) =>
    el('span', { class: 'topo-port', role: 'img', dataset: { kind } }, icon(kind, PORT_ICON_SIZE)),
  );
  const root = el(
    'div',
    { class: 'topo-node', ...BOX },
    badge,
    el('span', { class: 'topo-dot' }),
    figure(node.icon),
    el('div', { class: 'topo-label', text: node.label }),
    sub,
    ports.length > 0 && el('div', { class: 'topo-ports' }, ports),
  );
  return { root, sub, badge, ports, edge: node.traffic ? buildEdge() : null };
}

// The control plane

function stepMark(name) {
  const symbol = icon(name, STEP_ICON_SIZE);
  symbol.classList.add(`topo-cp-step-${name}`);
  return symbol;
}

// A stage of a box's strip. Its mark is a dot, a check or a cross: the style
// sheet draws the dot and shows the one that goes with the state.
function buildStage(stage) {
  const mark = el('span', { class: 'topo-cp-step-mark' }, stepMark('check'), stepMark('cross'));
  return el('span', { class: 'topo-cp-step', role: 'img' }, mark, el('span', { text: stage.label }));
}

// A row is an image with one name, like a port: its dot, its label and its
// sub-line say one thing together.
function buildRow(row) {
  const text = el('span', { class: 'topo-cp-item-text' });
  const root = el(
    'div',
    { class: 'topo-cp-item', role: 'img' },
    el('span', { class: 'topo-cp-dot' }),
    row.icon && icon(row.icon, ROW_ICON_SIZE),
    el('div', {}, el('b', { text: row.label }), text),
  );
  return { root, text };
}

// The list of a box. id goes to its heading, so it must be unique in the
// page.
function buildList(list, id) {
  const name = [list.title, list.scope].filter(Boolean).join(', ');
  const heading = el('div', { class: 'topo-cp-list-title', id, text: name });
  const counts = el('p', { class: 'topo-cp-counts' });
  // A list without bullets is not a list to every screen reader, unless it
  // says so. The heading names it: a label of its own would say the same
  // words a second time.
  const rows = el('ul', { class: 'topo-cp-results', role: 'list', 'aria-labelledby': id });
  const more = el('p', { class: 'topo-cp-more', hidden: true });
  const none = el('p', { class: 'topo-cp-none', hidden: true }, icon('check', KEY_ICON_SIZE), list.empty);
  const root = el('div', { class: 'topo-cp-list' }, heading, counts, rows, more, none);
  return { root, counts, rows, more, none, shown: '' };
}

function buildResult({ kind, object, backend, title }) {
  return el(
    'li',
    { class: 'topo-cp-result', title },
    el('span', { class: 'topo-cp-dot' }),
    el('b', { text: kind }),
    el('span', { class: 'topo-cp-result-object', text: object }),
    backend && el('span', { class: 'topo-cp-tag', text: backend }),
  );
}

// The pill in a box's head says what the box may do in the cluster. Its
// name is its label alone. The sentence behind it is its hover text, which
// a screen reader reads as the description: in the name too, it would be
// read twice.
function buildAccess(access) {
  return el(
    'span',
    {
      class: 'topo-cp-access',
      role: 'note',
      title: access.title,
      'aria-label': access.label,
      dataset: access.write ? { write: 'true' } : {},
    },
    access.write && icon('key', ACCESS_ICON_SIZE),
    access.label,
  );
}

function buildBox(box) {
  const stages = box.stages.map(buildStage);
  const rows = box.rows.map(buildRow);
  const list = box.list ? buildList(box.list, `topo-cp-list-${box.id}`) : null;
  const mark = icon(box.icon, MARK_SIZE);
  mark.classList.add('topo-cp-mark');
  const strip = stages.flatMap((stage, index) =>
    index === 0 ? [stage] : [el('span', { class: 'topo-cp-step-sep', 'aria-hidden': 'true', text: '›' }), stage],
  );
  const root = el(
    'div',
    { class: 'topo-cp-box', ...BOX, dataset: { box: box.id } },
    el('div', { class: 'topo-cp-head' }, mark, el('b', { text: box.label }), box.access && buildAccess(box.access)),
    el('div', { class: 'topo-cp-sub', title: box.sub, text: box.sub }),
    el('div', { class: 'topo-cp-steps' }, strip),
    el('div', { class: 'topo-cp-rows' }, rows.map((row) => row.root)),
    list?.root,
  );
  if (box.wide) root.dataset.wide = 'true';
  return { id: box.id, reads: box.reads, root, stages, rows, list };
}

// A link between two boxes: its line, its arrowhead and its label. The label
// is the one part of the wires that catches the pointer, to show its title.
// The wires are hidden from a screen reader, so the same words are kept in
// text as well: a paragraph of the page, which buildControlPlane() hides
// from the eye instead.
function buildLink(link) {
  const line = svgEl('path', { class: 'topo-cp-link-line' });
  const head = svgEl('path', { class: 'topo-cp-link-head' });
  const title = svgEl('title');
  const label = svgEl('text', { class: 'topo-cp-link-label' }, title, link.label);
  const root = svgEl('g', { class: 'topo-cp-link' }, line, head, label);
  return { id: link.id, from: link.from, to: link.to, root, line, head, label, title, text: el('p') };
}

// The footnote says what the port marks stand for, in the words of the
// spec, and then what the spec adds.
function buildNote(spec) {
  const keys = Object.keys(spec.ports).map((kind) =>
    el('span', { class: 'topo-port', dataset: { kind } }, icon(kind, KEY_ICON_SIZE)),
  );
  const legends = Object.values(spec.ports).flatMap((port, index) => {
    // A mark and the first word of its legend stay on one line.
    const [first, ...rest] = port.legend.split(' ');
    return [index > 0 && ', ', el('span', { class: 'topo-cp-key' }, keys[index], ' ', first), ' ', rest.join(' ')];
  });
  const note = el(
    'p',
    { class: 'topo-cp-note' },
    "What reads the apps sits on each node's edge: ",
    legends,
    '. Each kind of mark joins one line into its reader. ',
    spec.controlPlane.note,
  );
  return { note, keys };
}

function buildControlPlane(spec) {
  const plane = spec.controlPlane;
  const boxes = plane.boxes.map(buildBox);
  // A box stands apart from the one before it when a link runs between the
  // two: the gap holds the link and its label.
  boxes.slice(1).forEach((box, index) => {
    const before = boxes[index].id;
    const joined = plane.links.some(
      (link) => (link.from === before && link.to === box.id) || (link.from === box.id && link.to === before),
    );
    if (joined) box.root.dataset.joined = 'true';
  });
  const row = el('div', { class: 'topo-cp-row' }, boxes.map((box) => box.root));
  const links = plane.links.map(buildLink);
  const { note, keys } = buildNote(spec);
  const root = el(
    'div',
    { class: 'topo-cp' },
    el('h3', { class: 'topo-layer-title', text: plane.title }),
    row,
    // What the links say, for a screen reader: after the boxes they join.
    el('div', { class: 'visually-hidden' }, links.map((link) => link.text)),
    note,
  );
  return { root, row, boxes, links, keys };
}

// A bus joins the ports of one kind to the box that reads the kind. Its
// drops and its collector are inside the app panel and are cut where the
// panel's content scrolls out of view; its trunk leaves the panel. The
// collector is two runs, one from each end to the trunk: each run is a path
// of its own, so that each can start its dashes where the drops before it
// leave off.
function buildBus(kind, nodes) {
  const drops = nodes.map(() => svgEl('path', { class: 'topo-bus-line' }));
  const collector = [0, 1].map(() => svgEl('path', { class: 'topo-bus-line' }));
  const trunk = svgEl('path', { class: 'topo-bus-line topo-bus-trunk' });
  const head = svgEl('path', { class: 'topo-bus-head' });
  const end = svgEl('circle', { class: 'topo-bus-end', r: BUS_END_RADIUS, hidden: true });
  return {
    kind,
    nodes,
    drops,
    collector,
    trunk,
    head,
    end,
    inside: svgEl('g', { class: 'topo-bus', 'data-kind': kind }, drops, collector),
    outside: svgEl('g', { class: 'topo-bus', 'data-kind': kind }, trunk, head, end),
  };
}

// One bus per kind of port that a box reads and a node has. nodes lists, by
// index, the nodes with a port of the kind: each has one drop.
function buildBuses(spec) {
  const kinds = [...new Set(spec.controlPlane.boxes.map((box) => box.reads).filter(Boolean))];
  return kinds.flatMap((kind) => {
    const nodes = spec.nodes.flatMap((node, index) => ((node.ports ?? []).includes(kind) ? [index] : []));
    return nodes.length > 0 ? [buildBus(kind, nodes)] : [];
  });
}

function buildView(spec) {
  const source = buildSource(spec.source);
  const nodes = spec.nodes.map(buildNode);

  // The nodes that get traffic are stacked in one column next to the
  // source. The others stand in a column of their own, with no edge.
  const fed = nodes.filter((node) => node.edge !== null);
  const columns = [fed, nodes.filter((node) => node.edge === null)]
    .filter((column) => column.length > 0)
    .map((column) => el('div', { class: 'topo-col' }, column.map((node) => node.root)));
  const row = el('div', { class: 'topo-row' }, source.root, columns);
  // A region that scrolls must be reachable with the keyboard, which needs
  // a role and a name.
  const panel = el('div', { class: 'topo-panel', role: 'group', 'aria-label': spec.title, tabindex: 0 }, row);

  const plane = spec.controlPlane ? buildControlPlane(spec) : null;
  const buses = plane ? buildBuses(spec) : [];

  const edges = fed.map((node) => node.edge);
  const stem = svgEl('path', { class: 'topo-stem' });
  const link = svgEl('path', { class: 'topo-link' });
  // A nested <svg> cuts what it holds at its own box. It is kept on the
  // panel's inner box, so a wire stops where the panel's content scrolls
  // out of view.
  const clip = svgEl('svg', {}, stem, edges.map((edge) => edge.root), link, buses.map((bus) => bus.inside));

  const stack = el(
    'div',
    { class: 'topo-stack' },
    el('h3', { class: 'topo-layer-title' }, spec.title, ' ', el('span', { text: spec.namespace })),
    panel,
    plane?.root,
    svgEl(
      'svg',
      { class: 'topo-wires', 'aria-hidden': 'true' },
      clip,
      buses.map((bus) => bus.outside),
      plane?.links.map((each) => each.root),
    ),
  );

  const problemText = el('span');
  const problem = el('p', { class: 'topo-problem', hidden: true }, icon('alert', 14), problemText);
  const notes = el(
    'details',
    { class: 'topo-notes', open: readNotesOpen() },
    el('summary', { text: 'Services, fault triggers and detection layers' }),
    el('div', { class: 'topo-notes-body' }, renderMarkdown(spec.notes)),
  );

  return {
    children: [el('h2', { class: 'card-title', text: 'Topology' }), stack, problem, notes],
    source,
    nodes,
    plane,
    buses,
    stack,
    panel,
    wires: { clip, stem, edges, link },
    problem,
    problemText,
    notes,
  };
}

// renderList shows a box's list. Its rows are the one part of the card that
// comes and goes, so they are built again, but only when they change.
function renderList(view, list) {
  setText(view.counts, list.summary);
  const shown = JSON.stringify(list.rows);
  if (shown !== view.shown) {
    view.shown = shown;
    view.rows.replaceChildren(...list.rows.map(buildResult));
  }
  view.rows.hidden = list.rows.length === 0;
  setText(view.more, list.more > 0 ? `+${list.more} more` : '');
  view.more.hidden = list.more === 0;
  // The empty text is for a list that is known to be empty, not for one
  // whose query failed.
  view.none.hidden = list.count !== 0;
}

function renderControlPlane(view, model) {
  model.boxes.forEach((box, index) => {
    const { root, stages, rows, list } = view.boxes[index];
    setAttr(root, 'data-off', String(box.off));
    setAttr(root, 'aria-label', box.title);
    box.stages.forEach((stage, at) => {
      setAttr(stages[at], 'data-state', stage.state);
      setTitle(stages[at], stage.title);
    });
    box.rows.forEach((row, at) => {
      setAttr(rows[at].root, 'data-state', row.state);
      setTitle(rows[at].root, row.title);
      setText(rows[at].text, row.text);
    });
    if (box.list) renderList(list, box.list);
  });
  model.links.forEach((link, index) => {
    const { root, title, text } = view.links[index];
    setAttr(root, 'data-state', link.state);
    // The same words for a pointer and for a screen reader.
    setText(title, link.title);
    setText(text, link.title);
  });
}

// render shows a model from deriveTopology(). The states are data
// attributes; css/topology.css gives each its look.
function render(view, model) {
  setAttr(view.source.root, 'data-state', model.source.state);
  setAttr(view.source.root, 'data-errors', model.source.errors);
  setAttr(view.source.root, 'title', model.source.title);
  setAttr(view.source.root, 'aria-label', model.source.title);
  setText(view.source.sub, model.source.sub);

  model.nodes.forEach((node, index) => {
    const { root, sub, badge, ports, edge } = view.nodes[index];
    setAttr(root, 'data-status', node.status);
    setAttr(root, 'data-fault', node.fault?.state ?? 'none');
    setAttr(root, 'title', node.title);
    setAttr(root, 'aria-label', node.edge ? `${node.title} · ${edgeWords(node.edge)}` : node.title);
    setText(sub, node.sub);
    if (node.fault) {
      setAttr(badge, 'data-state', node.fault.state);
      setTitle(badge, node.fault.title);
    }
    node.ports.forEach((port, at) => {
      setAttr(ports[at], 'data-state', port.state);
      setTitle(ports[at], port.title);
    });
    if (node.edge) {
      setAttr(edge.root, 'data-state', node.edge.state);
      setAttr(edge.root, 'data-errors', node.edge.errors);
      setText(edge.label, node.edge.label);
    }
  });

  if (!view.plane) return;
  renderControlPlane(view.plane, model.controlPlane);
  // A mark in the footnote and the lines of a bus are lit while any port of
  // their kind is up. A drop follows its own port: no line flows out of a
  // port that is not read.
  const lit = (kind) => (model.ports.find((port) => port.kind === kind)?.up ? 'up' : 'down');
  for (const key of view.plane.keys) setAttr(key, 'data-state', lit(key.dataset.kind));
  for (const bus of view.buses) {
    for (const part of [...bus.collector, bus.trunk, bus.head, bus.end]) setAttr(part, 'data-state', lit(bus.kind));
    bus.nodes.forEach((node, at) => {
      const port = model.nodes[node].ports.find(({ kind }) => kind === bus.kind);
      setAttr(bus.drops[at], 'data-state', port.state);
    });
  }
  keepInStep(view.buses);
}

// keepInStep starts the moving dashes of every bus line at the same moment.
// A line that lights up later would otherwise move half a dash behind the
// line it joins, and the dashes would no longer meet at the corner.
function keepInStep(buses) {
  for (const bus of buses) {
    for (const line of [...bus.drops, ...bus.collector, bus.trunk]) {
      for (const animation of line.getAnimations()) {
        if (animation.startTime !== 0) animation.startTime = 0;
      }
    }
  }
}

// measure reads where things are, in whole px from the stack's corner, and
// routes the wires between them. It is null while the card is not displayed:
// nothing has a place then.
function measure(view) {
  const frame = view.stack.getBoundingClientRect();
  if (frame.width === 0) return null;
  const box = (element) => {
    const rect = element.getBoundingClientRect();
    return {
      left: Math.round(rect.left - frame.left),
      right: Math.round(rect.right - frame.left),
      top: Math.round(rect.top - frame.top),
      bottom: Math.round(rect.bottom - frame.top),
    };
  };

  const { panel, plane } = view;
  const outer = box(panel);
  // The panel without its border and without a scrollbar.
  const inner = {
    left: outer.left + panel.clientLeft,
    top: outer.top + panel.clientTop,
    right: outer.left + panel.clientLeft + panel.clientWidth,
    bottom: outer.top + panel.clientTop + panel.clientHeight,
  };
  const wiring = routeWires({
    source: box(view.source.root),
    nodes: view.nodes.map((node) => ({
      box: box(node.root),
      fed: node.edge !== null,
      ports: Object.fromEntries(node.ports.map((port) => [port.dataset.kind, box(port)])),
    })),
    panel: { inner, outer },
    controlPlane: plane && {
      box: box(plane.root),
      row: box(plane.row),
      boxes: plane.boxes.map(({ id, reads, root }) => ({ id, reads, box: box(root) })),
      links: plane.links.map(({ id, from, to }) => ({ id, from, to })),
    },
  });
  return { inner, wiring };
}

// drawWires places the wires as measure() routed them.
function drawWires(view, { inner, wiring }) {
  const { clip, stem, edges, link } = view.wires;
  const width = inner.right - inner.left;
  const height = inner.bottom - inner.top;
  for (const [name, value] of Object.entries({ x: inner.left, y: inner.top, width, height })) {
    clip.setAttribute(name, value);
  }
  // The same numbers as its box: what the clip holds is placed in the
  // stack's coordinates.
  clip.setAttribute('viewBox', `${inner.left} ${inner.top} ${width} ${height}`);

  stem.setAttribute('d', wiring.stem);
  wiring.edges.forEach(({ line, head, label, motion }, index) => {
    const edge = edges[index];
    edge.line.setAttribute('d', line);
    edge.head.setAttribute('d', head);
    edge.label.setAttribute('x', label.x);
    edge.label.setAttribute('y', label.y);
    edge.dot.style.setProperty('offset-path', `path("${motion}")`);
  });
  link.setAttribute('d', wiring.link);

  for (const bus of view.buses) {
    // The page and the model count the buses by the same rule. Should they
    // ever differ, a bus without a route is not drawn.
    const route = wiring.buses.find(({ kind }) => kind === bus.kind);
    const runs = route?.collector ? route.collector.split(/ (?=M)/) : [];
    const pieces = [
      ...bus.drops.map((drop, index) => [drop, route?.drops[index], route?.phases.drops[index]]),
      ...bus.collector.map((run, index) => [run, runs[index], route?.phases.collector[index]]),
      [bus.trunk, route?.trunk, 0],
    ];
    for (const [path, d, phase] of pieces) {
      path.setAttribute('d', d ?? '');
      // The dashes of each piece start where the piece before it leaves
      // off: css/topology.css reads the offset from --topo-dash.
      path.style.setProperty('--topo-dash', String(phase ?? 0));
    }
    bus.head.setAttribute('d', route?.head ?? '');
    bus.end.toggleAttribute('hidden', !route?.dot);
    if (route?.dot) {
      bus.end.setAttribute('cx', route.dot.x);
      bus.end.setAttribute('cy', route.dot.y);
    }
  }
  (view.plane?.links ?? []).forEach((each, index) => {
    const { line, head, label } = wiring.links[index];
    each.line.setAttribute('d', line);
    each.head.setAttribute('d', head);
    each.label.toggleAttribute('hidden', label === null);
    if (label === null) return;
    each.label.setAttribute('x', label.x);
    each.label.setAttribute('y', label.y);
    each.label.dataset.align = label.align;
  });
}

// mountTopology(root, spec, { instant }) fills the card and returns
// { update(), destroy() }.
//
// spec is the topology object of dashboard.json. instant(expr) resolves to
// the samples of an instant query, [{ labels, value }], or rejects.
// update() asks every expression again and shows the answers; it returns a
// promise that never rejects. destroy() empties the card and stops watching.
export function mountTopology(root, spec, { instant }) {
  const view = buildView(spec);
  const exprs = topologyQueries(spec);
  root.classList.add('topology');
  root.append(...view.children);
  // Until the first answers arrive nothing is known, and the card says so.
  render(view, deriveTopology(spec, new Map()));

  // The wires follow the layout: they are placed again after every refresh,
  // when the card changes size and when the panel scrolls. Nothing is
  // written while they stay where they are.
  let drawn = '';
  function draw() {
    const measured = measure(view);
    const key = JSON.stringify(measured);
    if (measured === null || key === drawn) return;
    drawn = key;
    drawWires(view, measured);
  }

  const listeners = new AbortController();
  view.panel.addEventListener('scroll', draw, { passive: true, signal: listeners.signal });
  view.notes.addEventListener('toggle', () => storeNotesOpen(view.notes.open), { signal: listeners.signal });
  const observer = new ResizeObserver(draw);
  observer.observe(root);

  function showProblem(message) {
    setText(view.problemText, message ?? '');
    view.problem.hidden = message === null;
  }

  // ask never rejects: a failed query is an answer too.
  async function ask(expr) {
    try {
      return { samples: await instant(expr) };
    } catch (err) {
      return { error: err instanceof Error ? err.message : String(err) };
    }
  }

  const traced = new Set();
  let live = true;
  let turn = 0;

  async function update() {
    turn += 1;
    const mine = turn;
    try {
      const answers = await Promise.all(exprs.map(ask));
      // A newer update, or destroy(), has overtaken this one.
      if (!live || mine !== turn) return;
      const results = new Map(exprs.map((expr, index) => [expr, answers[index]]));
      render(view, deriveTopology(spec, results));
      root.classList.add('topo-loaded');
      showProblem(describeFailures(results));
      draw();
    } catch (err) {
      // The queries answer for their own failures, so this is a fault in the
      // card itself. The card says so, and the trace goes where whoever
      // debugs it will look, once: the same fault comes back on every refresh.
      showProblem(`The topology could not be updated: ${err.message}`);
      if (!traced.has(err.message)) {
        traced.add(err.message);
        console.error(err);
      }
    }
  }

  function destroy() {
    live = false;
    observer.disconnect();
    listeners.abort();
    root.classList.remove('topology', 'topo-loaded');
    clear(root);
  }

  return { update, destroy };
}
