// The topology card: the demo apps as boxes, the traffic that reaches them,
// the ports they are read through and their fault badges. The DOM is built
// once. Every refresh changes attributes and text in place, which keeps the
// animations running. topology-model.mjs holds the rules for what is shown;
// css/topology.css draws each state.

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

// An attribute or a text is written only when it changes, so a tooltip that
// is showing and a text the reader has selected survive a refresh.
function setAttr(node, name, value) {
  if (node.getAttribute(name) !== value) node.setAttribute(name, value);
}

function setText(node, text) {
  if (node.textContent !== text) node.textContent = text;
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

// The source and the nodes are groups that the keyboard can reach. render()
// names each with its whole state, because a hover text is out of reach of
// a keyboard, a touch screen and a screen reader.
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

  const keys = Object.entries(spec.ports).map(([kind, port]) => {
    const key = el('span', { class: 'topo-port', dataset: { kind } }, icon(kind, KEY_ICON_SIZE));
    return { key, chip: el('li', { class: 'topo-chip' }, key, port.legend) };
  });
  const legend = el('ul', { class: 'topo-legend' }, keys.map(({ chip }) => chip));

  const edges = fed.map((node) => node.edge);
  const stem = svgEl('path', { class: 'topo-stem' });
  const link = svgEl('path', { class: 'topo-link' });
  // A nested <svg> cuts what it holds at its own box. It is kept on the
  // panel's inner box, so a wire stops where the panel's content scrolls
  // out of view.
  const clip = svgEl('svg', {}, stem, edges.map((edge) => edge.root), link);

  const stack = el(
    'div',
    { class: 'topo-stack' },
    el('h3', { class: 'topo-layer-title' }, spec.title, ' ', el('span', { text: spec.namespace })),
    panel,
    legend,
    svgEl('svg', { class: 'topo-wires', 'aria-hidden': 'true' }, clip),
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
    keys: keys.map(({ key }) => key),
    stack,
    panel,
    wires: { clip, stem, edges, link },
    problem,
    problemText,
    notes,
  };
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
      setAttr(badge, 'title', node.fault.title);
      setAttr(badge, 'aria-label', node.fault.title);
    }
    node.ports.forEach((port, at) => {
      setAttr(ports[at], 'data-state', port.state);
      setAttr(ports[at], 'title', port.title);
      setAttr(ports[at], 'aria-label', port.title);
    });
    if (node.edge) {
      setAttr(edge.root, 'data-state', node.edge.state);
      setAttr(edge.root, 'data-errors', node.edge.errors);
      setText(edge.label, node.edge.label);
    }
  });

  model.ports.forEach((port, index) => setAttr(view.keys[index], 'data-state', port.up ? 'up' : 'down'));
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

  const { panel } = view;
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
    nodes: view.nodes.map((node) => ({ box: box(node.root), fed: node.edge !== null })),
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
