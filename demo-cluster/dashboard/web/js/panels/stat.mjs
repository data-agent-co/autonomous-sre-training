// A stat panel: one number, the tone its thresholds give it and, when asked
// for, a sparkline of the same series.

import { el, svgEl } from '../dom.mjs';
import { formatLegend, formatValue, lastValue, toneFor } from '../format.mjs';
import { toGrid } from '../prom.mjs';

// The word on the status chip. The tile is tinted by its tone as well, but
// a status never rests on colour alone.
export const TONE_WORDS = { good: 'OK', warning: 'Warning', critical: 'Critical', info: 'Idle', none: 'No data' };

const round2 = (number) => Number(number.toFixed(2));

// sparkline lays the values out in a 100 by 100 box, the first on the left
// edge, the last on the right, the lowest at the bottom; a flat series runs
// through the middle. path is an SVG path with one stroke per unbroken run of
// samples, and end is where the last sample sits (null when there is none).
export function sparkline(values) {
  const present = values.filter((value) => value !== null);
  if (present.length === 0) return { path: '', end: null };
  const low = Math.min(...present);
  const span = Math.max(...present) - low;
  const x = (index) => round2(values.length > 1 ? (index / (values.length - 1)) * 100 : 100);
  const y = (value) => round2(span > 0 ? 100 - ((value - low) / span) * 100 : 50);

  let path = '';
  let end = null;
  values.forEach((value, index) => {
    if (value === null) return;
    const afterGap = index === 0 || values[index - 1] === null;
    const beforeGap = index === values.length - 1 || values[index + 1] === null;
    path += `${afterGap ? 'M' : 'L'}${x(index)} ${y(value)}`;
    // A sample between two gaps is a stroke of no length, which round line
    // caps draw as a dot.
    if (afterGap && beforeGap) path += 'h0';
    end = { x: x(index), y: y(value) };
  });
  return { path, end };
}

function sameLabels(a, b) {
  const names = Object.keys(a);
  return names.length === Object.keys(b).length && names.every((name) => a[name] === b[name]);
}

// statModel reads the panel's number from the series its range query
// returned: the last sample of the first series.
//
// On a coarse window (see tickClient) that sample can be much older than the
// data. The caller then passes current, the answer of an instant query for
// the same expression, and the number is that series' sample in it. The
// sparkline comes from the range answer either way.
export function statModel(panel, series, timeWindow, current = null) {
  const [first] = series;
  const values = first ? toGrid(first, timeWindow) : [];
  // A series that is younger than one step is in the instant answer only.
  const sample = current?.find((candidate) => !first || sameLabels(candidate.labels, first.labels));
  const value = sample?.value ?? lastValue(values);
  const labels = first?.labels ?? sample?.labels;
  return {
    text: formatValue(value, panel.unit, panel.decimals),
    name: labels ? formatLegend(panel.queries[0].legend, labels, panel.title) : '',
    tone: toneFor(value, panel.thresholds),
    spark: sparkline(values),
  };
}

// sparkView builds the sparkline of a tile: { root, draw(spark) }. The box
// stretches with the tile, so the line is drawn without regard to its aspect
// ratio; the dot on the last sample is an element of its own to stay round.
function sparkView() {
  const line = svgEl('path');
  const dot = el('span', { class: 'spark-dot', hidden: true });
  return {
    root: el(
      'div',
      { class: 'stat-spark' },
      svgEl('svg', { viewBox: '0 0 100 100', preserveAspectRatio: 'none', 'aria-hidden': 'true' }, line),
      dot,
    ),
    draw({ path, end }) {
      line.setAttribute('d', path);
      dot.hidden = end === null;
      if (end) {
        dot.style.setProperty('--x', `${end.x}%`);
        dot.style.setProperty('--y', `${end.y}%`);
      }
    },
  };
}

// mountStat fills a panel frame and returns { load(ctx), render(model) }.
export function mountStat(frame, panel) {
  if (!panel.thresholds) throw new Error('a stat panel needs thresholds: they give the tile its tone');

  const word = el('span');
  // The chip has nothing to say until the first value arrives.
  const chip = el('span', { class: 'chip', hidden: true }, el('span', { class: 'dot' }), word);
  const value = el('span', { class: 'stat-value' });
  const name = panel.label ? el('span', { class: 'stat-name' }) : null;
  const spark = panel.sparkline ? sparkView() : null;

  frame.root.classList.add('stat');
  if (panel.hero) frame.root.classList.add('hero');
  frame.extras.append(chip);
  frame.body.append(el('div', { class: 'stat-figure' }, value, name));
  if (spark) frame.body.append(spark.root);

  return {
    async load(ctx) {
      const [{ expr }] = panel.queries;
      const [series, current] = await Promise.all([ctx.range(expr), ctx.coarse ? ctx.instant(expr) : null]);
      return statModel(panel, series, ctx.window, current);
    },
    render(model) {
      frame.root.dataset.tone = model.tone;
      word.textContent = TONE_WORDS[model.tone];
      chip.hidden = false;
      value.textContent = model.text;
      if (name) name.textContent = model.name;
      spark?.draw(model.spark);
    },
  };
}
