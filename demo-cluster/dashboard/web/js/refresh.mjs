// The parts of the refresh loop that work without a page, so that node --test
// covers them: the limit on queries in flight, the cluster's clock, one
// tick's view of Prometheus, and the checks on the panel file. app.mjs runs
// the loop itself.

import { parseDuration } from './format.mjs';

// limiter(max) returns run(task): it calls task() once fewer than max
// earlier tasks are still running, and settles as the task does.
export function limiter(max) {
  const waiting = [];
  let running = 0;
  return async (task) => {
    if (running < max) running += 1;
    else await new Promise((resolve) => waiting.push(resolve));
    try {
      return await task();
    } finally {
      // The task's place goes straight to the one that waited longest.
      const next = waiting.shift();
      if (next) next();
      else running -= 1;
    }
  };
}

// serverClock keeps the page on Prometheus' clock. A window ends "now", and
// that must be the moment Prometheus is at: a browser clock that runs ahead
// asks for points Prometheus does not have yet, and they come back as a drop
// to zero. A laptop's clock and the clock of the VM its cluster runs in do
// drift apart, most of all after sleep. Every instant query answers with the
// time it was evaluated at, so the difference is known after the first tick.
//
//   note(samples)  takes the evaluation time from an instant query's answer
//   now()          the cluster's time in Unix seconds, by the answers noted
//                  since now() was last called; the browser's time until
//                  there is one
//   moved(by)      whether the answers noted since now() put the cluster's
//                  clock `by` seconds or more from where now() had it. The
//                  window that now() gave then ends at the wrong moment:
//                  this is so on the first tick, and on the first one after
//                  the clocks drifted apart
export function serverClock(localNow = () => Date.now() / 1000) {
  let offset = 0;
  let measured = null;
  return {
    note(samples) {
      const time = samples[0]?.time;
      if (!Number.isFinite(time)) return;
      // An answer is on its way for a moment, which makes the cluster look
      // behind by that moment. The largest difference is the closest one.
      const seen = time - localNow();
      measured = measured === null ? seen : Math.max(measured, seen);
    },
    now() {
      if (measured !== null) offset = measured;
      measured = null;
      return localNow() + offset;
    },
    moved(by) {
      return measured !== null && Math.abs(measured - offset) >= by;
    },
  };
}

// tickClient is one tick's view of Prometheus. A query that several panels
// ask for is sent once and the answer shared. Range queries end when the
// tick is dropped (signal); instant queries do not depend on the window, so
// they are left to finish.
//
// coarse says that the points of the window are further apart than the
// scrape interval. The last point of a range answer can then be much older
// than the data, and whatever shows a current value asks for it on its own.
export function tickClient(prom, limit, timeWindow, { signal, minStepSec, clock }) {
  const asked = new Map();
  const once = (key, send) => {
    if (!asked.has(key)) asked.set(key, limit(send));
    return asked.get(key);
  };
  return {
    window: timeWindow,
    signal,
    coarse: timeWindow.step > minStepSec,
    range: (expr) => once(`range ${expr}`, () => prom.range(expr, { ...timeWindow, signal })),
    instant: (expr) =>
      once(`instant ${expr}`, async () => {
        const samples = await prom.instant(expr);
        clock.note(samples);
        return samples;
      }),
  };
}

// durations lists the choices on offer, plus the file's own default when it
// is not one of them, shortest first.
export function durations(offered, fromFile) {
  return [...new Set([...offered, fromFile])]
    .sort((a, b) => parseDuration(a) - parseDuration(b))
    .map((value) => ({ value, text: value }));
}

// checkDashboard stops at the first thing the page cannot work without. The
// Go tests check the file in full; this is for a file edited by hand and
// served with WEB_DIR.
export function checkDashboard(file) {
  const need = (ok, problem) => {
    if (!ok) throw new Error(problem);
  };
  need(file !== null && typeof file === 'object', 'it is not an object');
  need(file.version === 1, `it has version ${file.version}, and this page reads version 1`);
  need(typeof file.title === 'string', 'it has no title');
  for (const key of ['range', 'refresh', 'minStep']) {
    need(typeof file[key] === 'string', `it has no ${key}`);
    parseDuration(file[key]);
  }
  need(file.palette !== null && typeof file.palette === 'object', 'it has no palette');
  need(file.topology !== null && typeof file.topology === 'object', 'it has no topology');
  need(Array.isArray(file.panels), 'it has no panels');
  file.panels.forEach((panel, index) => {
    const named = typeof panel?.id === 'string' && typeof panel.title === 'string';
    need(named, `panel ${index + 1} needs an id and a title`);
    const placed = ['x', 'y', 'w', 'h'].every((side) => Number.isInteger(panel.grid?.[side]));
    need(placed, `panel "${panel.id}" needs a grid with x, y, w and h`);
    const queries = Array.isArray(panel.queries) ? panel.queries : [];
    const asks = queries.length > 0 && queries.every((query) => typeof query?.expr === 'string');
    need(asks, `panel "${panel.id}" needs queries, each with an expr`);
  });
  return file;
}
