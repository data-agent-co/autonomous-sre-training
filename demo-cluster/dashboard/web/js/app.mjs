// The page. It reads dashboard.json, builds the header controls, the
// topology card and one frame per panel, and runs the refresh loop that
// feeds them from Prometheus.

import { el } from './dom.mjs';
import { formatClock, parseDuration } from './format.mjs';
import { icon } from './icons.mjs';
import { mountStat } from './panels/stat.mjs';
import { mountTable } from './panels/table.mjs';
import { mountTimeseries } from './panels/timeseries.mjs';
import { PromError, alignWindow, createPromClient } from './prom.mjs';
import { checkDashboard, durations, limiter, serverClock, tickClient } from './refresh.mjs';

const RANGES = ['2m', '5m', '15m', '1h', '6h'];
const REFRESHES = ['5s', '10s', '30s', '1m'];
const REFRESH_OFF = 'off';
const MAX_IN_FLIGHT = 6;

// The same key as in theme.js, which applies the stored theme before the
// first paint.
const THEME_KEY = 'demo-dashboard-theme';

// A panel kind is a function mount(frame, panel, dashboard) that fills the
// frame and returns { load(ctx), render(data) }: load asks Prometheus and
// shapes the answer, render draws it. A kind that draws on a canvas also
// returns restyle(), called when the theme changes.
const KINDS = { stat: mountStat, timeseries: mountTimeseries, table: mountTable };

const prom = createPromClient();

// What the page is showing. main() fills these in once dashboard.json is
// read.
let dashboard = null;
let views = [];
// Panels that could not be built. They count as failed on every tick.
let broken = 0;
let topology = null;
let rangeSec = 0;
let refreshSec = 0;
let minStepSec = 0;

// Banner and status

const banner = document.getElementById('banner');
const notices = new Map();

// The banner lists the page-level problems, one line each, and hides when
// there is none. Each problem has a key; a message of null takes it back.
function setNotice(key, message) {
  // The banner is an alert: writing the same text again would have a screen
  // reader read it again on every refresh.
  if ((notices.get(key) ?? null) === message) return;
  if (message === null) notices.delete(key);
  else notices.set(key, message);
  banner.replaceChildren(...[...notices.values()].map((text) => el('p', {}, icon('alert'), el('span', { text }))));
  banner.hidden = notices.size === 0;
}

function setStatus(state, text) {
  document.getElementById('status').dataset.state = state;
  document.getElementById('status-text').textContent = text;
}

// Queries

const limit = limiter(MAX_IN_FLIGHT);
const clock = serverClock();

// Panels

// One description is open at a time. It closes on Escape and when the
// pointer or the keyboard focus goes anywhere else.
let openInfo = null;

function closeInfo() {
  if (!openInfo) return;
  openInfo.button.setAttribute('aria-expanded', 'false');
  openInfo.popover.hidden = true;
  openInfo = null;
}

function closeInfoFromOutside(event) {
  if (openInfo && !openInfo.button.contains(event.target) && !openInfo.popover.contains(event.target)) closeInfo();
}

document.addEventListener('pointerdown', closeInfoFromOutside);
document.addEventListener('focusin', closeInfoFromOutside);
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape') closeInfo();
});

// infoControl builds a panel's info button and the description it opens.
function infoControl(panel) {
  const popover = el('div', { class: 'popover', id: `about-${panel.id}`, hidden: true, text: panel.description });
  const button = el(
    'button',
    {
      type: 'button',
      class: 'icon-button',
      'aria-label': `About: ${panel.title}`,
      'aria-expanded': 'false',
      'aria-controls': popover.id,
    },
    icon('info'),
  );
  const info = { button, popover };
  const open = () => {
    closeInfo();
    button.setAttribute('aria-expanded', 'true');
    popover.hidden = false;
    // The description opens under the button. Where that runs past the
    // bottom of the window and there is room above, it opens upwards.
    popover.classList.remove('above');
    const ceiling = Math.max(document.querySelector('.page-head').getBoundingClientRect().bottom, 0);
    const fitsBelow = popover.getBoundingClientRect().bottom <= window.innerHeight;
    const fitsAbove = button.getBoundingClientRect().top - popover.offsetHeight >= ceiling;
    popover.classList.toggle('above', !fitsBelow && fitsAbove);
    openInfo = info;
  };
  button.addEventListener('click', () => (openInfo === info ? closeInfo() : open()));
  // Reaching the button with the keyboard opens the description as well.
  button.addEventListener('focus', () => {
    if (button.matches(':focus-visible')) open();
  });
  return info;
}

// panelFrame builds the box every panel lives in: a header with the title,
// room for the panel's own extras and the info button, then the body.
function panelFrame(panel) {
  const extras = el('div', { class: 'panel-extras' });
  const body = el('div', { class: 'panel-body' });
  const errorText = el('span');
  const error = el('p', { class: 'panel-error', hidden: true }, icon('alert', 14), errorText);
  const info = panel.description ? infoControl(panel) : null;
  const { x, y, w, h } = panel.grid;
  const root = el(
    'article',
    { class: 'card panel', 'aria-labelledby': `title-${panel.id}`, style: { '--x': x, '--y': y, '--w': w, '--h': h } },
    el(
      'header',
      { class: 'panel-head' },
      el('h2', { class: 'panel-title', id: `title-${panel.id}`, title: panel.title, text: panel.title }),
      extras,
      info?.button,
    ),
    info?.popover,
    body,
    error,
  );
  return {
    root,
    extras,
    body,
    // The panel drew fresh data.
    settle() {
      root.classList.add('has-data');
      root.classList.remove('has-error');
      error.hidden = true;
    },
    // The panel could not be drawn. What it showed last stays, dimmed.
    fail(message) {
      root.classList.add('has-error');
      errorText.textContent = message;
      error.hidden = false;
    },
  };
}

const traced = new Set();

function describe(err) {
  if (err instanceof PromError) return `Query failed: ${err.message}`;
  // Anything else is a mistake in the panel file or in the page. Keep the
  // trace where whoever debugs it will look, once: the same mistake comes
  // back on every refresh.
  if (!traced.has(err.message)) {
    traced.add(err.message);
    console.error(err);
  }
  return `Panel error: ${err.message}`;
}

// mountPanel adds a panel to the grid. It returns null for a panel that
// cannot be built; the frame then says why.
function mountPanel(panel) {
  const frame = panelFrame(panel);
  document.getElementById('grid').append(frame.root);
  try {
    const mount = KINDS[panel.kind];
    if (!mount) throw new Error(`unknown panel kind "${panel.kind}"`);
    return { frame, ...mount(frame, panel, dashboard) };
  } catch (err) {
    frame.fail(describe(err));
    return null;
  }
}

// refreshPanel loads and draws one panel as soon as its own queries settle.
// It never throws: a failure is shown in the panel and returned. A tick that
// was dropped meanwhile changes nothing.
async function refreshPanel(view, ctx) {
  try {
    const data = await view.load(ctx);
    if (ctx.signal.aborted) return null;
    view.render(data);
    view.frame.settle();
    return null;
  } catch (err) {
    if (ctx.signal.aborted) return null;
    view.frame.fail(describe(err));
    return err;
  }
}

// The refresh loop

let timer = null;
let client = null;
// The tick in progress, as the controller that can drop it.
let current = null;
// A tick came due while the tab was hidden.
let missed = false;
let topologyUpdate = null;

// A tick's window ends at the cluster's now (see serverClock).
const newClient = (signal) =>
  tickClient(prom, limit, alignWindow(clock.now(), rangeSec, { minStepSec }), { signal, minStepSec, clock });

// The topology does not depend on the time range, so an update that is
// still running when the next tick starts is simply awaited again. Its
// update() promises never to reject; should it break that promise, the
// banner says so and the loop goes on.
function refreshTopology() {
  if (!topology) return null;
  topologyUpdate ??= Promise.resolve()
    .then(() => topology.update())
    .then(
      () => setNotice('topology', null),
      (err) => setNotice('topology', `The topology could not be updated: ${err.message}`),
    )
    .finally(() => {
      topologyUpdate = null;
    });
  return topologyUpdate;
}

function report(errors) {
  const failed = broken + errors.length;
  const total = broken + views.length;
  if (failed === 0) setStatus('ok', `Updated ${formatClock(Date.now() / 1000, true)}`);
  else setStatus(failed === total ? 'failed' : 'partial', `${failed} ${failed === 1 ? 'panel' : 'panels'} failed`);
  setNotice('queries', errors.length > 0 && failed === total ? `Every query failed: ${errors[0].message}` : null);
  setNotice('tick', null);
}

// tick runs one refresh. Nothing awaits it (the timer, restart() and the tab
// coming back into view start it), so it reports its own failure.
//
// A tick learns the cluster's time from its own answers. When they put it
// somewhere else than the tick assumed, its window ended at the wrong moment:
// that is so for the first tick, and for the first one after the clocks
// drifted apart, as after sleep. Such a tick runs once more, at once (again).
async function tick(again = false) {
  if (document.hidden) {
    missed = true;
    return;
  }
  if (current) return;
  missed = false;
  const abort = new AbortController();
  current = abort;
  let stale = false;
  try {
    client = newClient(abort.signal);
    const [, ...outcomes] = await Promise.all([refreshTopology(), ...views.map((view) => refreshPanel(view, client))]);
    if (!abort.signal.aborted) {
      report(outcomes.filter((err) => err !== null));
      stale = !again && clock.moved(minStepSec);
    }
  } catch (err) {
    // The panels and the topology answer for their own failures, so this is
    // a fault in the page itself.
    console.error(err);
    setNotice('tick', `The page could not refresh: ${err.message}`);
    setStatus('failed', 'Not updated');
  } finally {
    if (current === abort) current = null;
  }
  if (stale) tick(true);
}

// restart drops the running tick, whose window or pace is out of date, and
// starts over at once.
function restart() {
  current?.abort();
  current = null;
  clearInterval(timer);
  timer = refreshSec > 0 ? setInterval(tick, refreshSec * 1000) : null;
  tick();
}

document.addEventListener('visibilitychange', () => {
  if (!document.hidden && missed) tick();
});

// Header controls

function fillSelect(select, options, selected) {
  select.replaceChildren(
    ...options.map(({ value, text }) => el('option', { value, text, selected: value === selected })),
  );
}

function startControls() {
  const range = document.getElementById('range');
  fillSelect(range, durations(RANGES, dashboard.range), dashboard.range);
  range.addEventListener('change', () => {
    rangeSec = parseDuration(range.value);
    restart();
  });

  const refresh = document.getElementById('refresh');
  const paces = [{ value: REFRESH_OFF, text: 'Off' }, ...durations(REFRESHES, dashboard.refresh)];
  fillSelect(refresh, paces, dashboard.refresh);
  refresh.addEventListener('change', () => {
    refreshSec = refresh.value === REFRESH_OFF ? 0 : parseDuration(refresh.value);
    restart();
  });

  // index.html keeps both hidden until they have something to offer.
  for (const select of [range, refresh]) select.closest('.field').hidden = false;
}

function startThemeSwitch() {
  const buttons = [...document.querySelectorAll('[data-theme-choice]')];
  const mark = () => {
    const { theme } = document.documentElement.dataset;
    for (const button of buttons) button.setAttribute('aria-pressed', String(button.dataset.themeChoice === theme));
  };
  for (const button of buttons) {
    button.addEventListener('click', () => {
      document.documentElement.dataset.theme = button.dataset.themeChoice;
      mark();
      // Everything follows the CSS variables, except what is drawn on a canvas.
      for (const view of views) view.restyle?.();
      try {
        localStorage.setItem(THEME_KEY, button.dataset.themeChoice);
        setNotice('theme', null);
      } catch (err) {
        setNotice('theme', `The theme lasts for this visit only. It could not be stored: ${err.message}`);
      }
    });
  }
  mark();
}

// Start

async function loadDashboard() {
  const response = await fetch('dashboard.json', { cache: 'no-store' });
  if (!response.ok) throw new Error(`the server answered HTTP ${response.status}`);
  return checkDashboard(await response.json());
}

// The topology is a module of its own. It is loaded here and not imported at
// the top, so that a fault in it costs the page its topology card and
// nothing else: a failed import at the top would stop every module.
async function startTopology() {
  const root = document.getElementById('topology');
  // index.html keeps the card hidden until there is a topology to put in it.
  root.hidden = false;
  try {
    const { mountTopology } = await import('./topology.mjs');
    return mountTopology(root, dashboard.topology, { instant: (expr) => client.instant(expr) });
  } catch (err) {
    console.error(err);
    root.replaceChildren(el('p', { class: 'panel-message', text: `The topology could not be shown: ${err.message}` }));
    return null;
  }
}

async function main() {
  startThemeSwitch();
  try {
    dashboard = await loadDashboard();
  } catch (err) {
    setNotice('dashboard', `dashboard.json could not be used: ${err.message}`);
    setStatus('failed', 'Not loaded');
    return;
  }

  document.title = dashboard.title;
  document.getElementById('title').textContent = dashboard.title;
  document.getElementById('description').textContent = dashboard.description ?? '';
  rangeSec = parseDuration(dashboard.range);
  refreshSec = parseDuration(dashboard.refresh);
  minStepSec = parseDuration(dashboard.minStep);
  // Whatever asks before the first tick gets a client of its own.
  client = newClient(new AbortController().signal);

  startControls();
  const mounted = dashboard.panels.map(mountPanel);
  views = mounted.filter((view) => view !== null);
  broken = mounted.length - views.length;
  topology = await startTopology();
  restart();
}

main().catch((err) => {
  console.error(err);
  setNotice('page', `The page could not start: ${err.message}`);
  setStatus('failed', 'Not started');
});
