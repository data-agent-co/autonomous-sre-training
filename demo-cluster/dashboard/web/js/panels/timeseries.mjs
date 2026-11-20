// Time series panels, drawn with uPlot (a global: the page loads
// vendor/uPlot.iife.min.js before its modules). The first half of this file
// is pure and covered by node --test. The second half is the DOM: the plots,
// the legend and the tooltip.

import { el } from '../dom.mjs';
import {
  formatClock,
  formatLegend,
  formatTicks,
  formatValue,
  lastValue,
  maxValue,
  meanValue,
} from '../format.mjs';
import { gridTimes, toGrid } from '../prom.mjs';

// The area under a line is a light wash of its colour unless the panel or
// the query says otherwise.
const DEFAULT_FILL = 0.1;

// Series that match no palette entry take the free colour slots in this
// order: first the hues no entity owns.
const FREE_SLOTS = [7, 6, 8, 1, 2, 3, 4, 5];

// seriesColors gives each series ({ name, color }) the CSS custom property
// that holds its colour. Colour follows the entity, not the rank: the
// query's own colour wins (a slot 1..8 or a tone), then the longest palette
// key the name starts with, then the next slot nobody in the panel uses.
// There are eight slots and no more, so a ninth free series starts over.
export function seriesColors(series, palette) {
  const prefixes = Object.keys(palette).sort((a, b) => b.length - a.length);
  const own = series.map(({ name, color }) => {
    if (color !== undefined) return color;
    const prefix = prefixes.find((key) => name.startsWith(key));
    return prefix === undefined ? null : palette[prefix];
  });
  const free = FREE_SLOTS.filter((slot) => !own.includes(slot));
  let taken = 0;
  return own.map((color) => {
    if (typeof color === 'string') return `--tone-${color}`;
    if (color !== null) return `--series-${color}`;
    const slot = taken < free.length ? free[taken] : FREE_SLOTS[(taken - free.length) % FREE_SLOTS.length];
    taken += 1;
    return `--series-${slot}`;
  });
}

// buildPlots turns what the panel's queries returned (results[i] belongs to
// panel.queries[i]) into what the panel draws:
//
//   { times, plots: [{ unit, series: [{ name, color, dash, fill, values }] }] }
//
// There is one plot per unit, the panel's own unit first, because a plot has
// one y axis. Every series is on the window's grid (times), with null where
// it has no sample. The series of a query are sorted by name, so their order
// and colours do not change from one refresh to the next.
export function buildPlots(panel, results, timeWindow, palette) {
  const series = panel.queries.flatMap((query, index) =>
    results[index]
      .map((found) => ({
        name: formatLegend(query.legend, found.labels, panel.title),
        unit: query.unit ?? panel.unit,
        color: query.color,
        dash: query.dash === true,
        fill: query.fill ?? panel.fill ?? DEFAULT_FILL,
        values: toGrid(found, timeWindow),
      }))
      .sort((a, b) => a.name.localeCompare(b.name, 'en')),
  );
  const colors = seriesColors(series, palette);
  const coloured = series.map((entry, index) => ({ ...entry, color: colors[index] }));
  const units = [...new Set([panel.unit, ...coloured.map((entry) => entry.unit)])];
  return {
    times: gridTimes(timeWindow),
    plots: units
      .map((unit) => ({ unit, series: coloured.filter((entry) => entry.unit === unit) }))
      .filter((plot) => plot.series.length > 0),
  };
}

const LEGEND_VALUES = {
  last: { head: 'Last', of: lastValue },
  mean: { head: 'Mean', of: meanValue },
  max: { head: 'Max', of: maxValue },
};

// legendColumns lists the value columns of a legend ({ head, of(values) }):
// the panel's legend.values, or the last value alone.
export function legendColumns(legend) {
  const names = legend?.values?.length ? legend.values : ['last'];
  return names.map((name) => {
    const column = LEGEND_VALUES[name];
    if (!column) throw new Error(`unknown legend value "${name}"`);
    return column;
  });
}

// thresholdKey lists a panel's thresholds for its header ({ tone, text }),
// so they are known even when they lie outside the plot.
export function thresholdKey(thresholds, unit) {
  return (thresholds?.steps ?? []).map((step) => ({ tone: step.tone, text: formatValue(step.from, unit) }));
}

const HEADROOM = 0.1;

// yRange picks the y axis for data that spans dataMin..dataMax (both null
// when there is none). The panel's min and max are kept as they are. A side
// without one runs from zero, or from below a negative minimum, to a little
// above the highest value. A threshold step widens the range only when the
// data has come at least half way to it: one far above would flatten the
// data. Without data, or with nothing but zeros, the axis is 0..1.
export function yRange(dataMin, dataMax, { min, max, thresholds } = {}) {
  const peak = Math.max(dataMax ?? 0, 0);
  let high = peak;
  for (const step of thresholds?.steps ?? []) {
    if (step.from <= 2 * peak) high = Math.max(high, step.from);
  }
  const floor = min ?? Math.min(dataMin ?? 0, 0);
  const pad = Math.max(high - floor, 0) * HEADROOM;
  const low = min === undefined && floor < 0 ? floor - pad : floor;
  const top = max ?? high + pad;
  return top > low ? [low, top] : [low, low + 1];
}

const MAX_TICKS = 5;

// tickSpace is the least room, in px, that one step between two ticks may
// take on an axis of the given length. It is a little more than a fifth, so
// that five steps never fit and the axis has at most MAX_TICKS ticks.
export function tickSpace(length) {
  return length / (MAX_TICKS - 0.5);
}

// The steps an axis may put between two ticks. uPlot looks a step up with a
// binary search, so each list ascends.

const MINUTE = 60;
const HOUR = 60 * MINUTE;

// The time axis, in seconds, up to a day. uPlot's own list jumps from one
// minute to five, and five minutes is longer than the default range: a plot
// too narrow for a tick every minute would have none.
export const TIME_STEPS = [
  ...[1, 5, 10, 15, 30],
  ...[1, 2, 5, 10, 15, 30].map((minutes) => minutes * MINUTE),
  ...[1, 2, 3, 6, 12, 24].map((hours) => hours * HOUR),
];

// Byte rates: round numbers of B/s, KiB/s, MiB/s and GiB/s. uPlot's own steps
// are round in base 10, which gives ticks like 97.66 KiB/s. The two longest
// steps of each scale are a quarter and a half of the next one, so an axis
// that runs past 1024 KiB/s reads 0.5, 1 and 1.5 MiB/s.
export const BYTE_STEPS = [1, 1024, 1024 ** 2, 1024 ** 3].flatMap((scale) =>
  [1, 2, 2.5, 5, 10, 20, 25, 50, 100, 200, 256, 512].map((round) => round * scale),
);

// ticksWithin blanks the ticks outside min..max, which is how a uPlot axis
// filter says "do not draw this one". uPlot's time axis always makes its
// first tick, even one past the end of the scale: that happens when the step
// is longer than what is left of the range.
export function ticksWithin(ticks, min, max) {
  return ticks.map((tick) => (tick >= min && tick <= max ? tick : null));
}

const BAR_MAX = 24;
const BAR_GAP = 2;
// The share of one time step that the bars of that step may fill together.
const BAR_GROUP = 0.8;

// barLayout places the bars of `count` series side by side around one time
// position; step is the distance to the next position. All numbers are px.
// It returns the thickness of a bar (never above BAR_MAX) and, per series,
// where its bar starts relative to the position. Neighbours are BAR_GAP
// apart; where that would leave hairlines the gap shrinks to a quarter of
// the room a bar has.
export function barLayout(count, step) {
  const slot = Math.min((step * BAR_GROUP) / count, BAR_MAX + BAR_GAP);
  const gap = Math.min(BAR_GAP, slot / 4);
  return {
    thickness: slot - gap,
    starts: Array.from({ length: count }, (_, index) => (index - count / 2) * slot + gap / 2),
  };
}

// lonePoints lists the indexes of the samples no line reaches: a line needs
// two samples next to each other. They are drawn as dots, or a series with a
// single sample would be invisible. With gaps connected only the one sample
// of a one-sample series is alone.
export function lonePoints(values, connectNulls) {
  const present = values.flatMap((value, index) => (value === null ? [] : [index]));
  if (connectNulls) return present.length === 1 ? present : [];
  return present.filter((index) => (values[index - 1] ?? null) === null && (values[index + 1] ?? null) === null);
}

const TIP_GAP = 12;

const clamp = (value, low, high) => Math.max(low, Math.min(value, high));

// tipPosition places the tooltip ({ width, height }) inside its box, clear of
// the pointer ({ x, y } in the box). It goes beside the pointer: to the right
// when it fits there, else to the left. In a box too narrow for either it
// goes to the bottom edge, or to the top edge when the pointer is in the
// lower half. It returns the tooltip's { left, top }.
export function tipPosition(pointer, tip, box) {
  const room = { x: Math.max(box.width - tip.width, 0), y: Math.max(box.height - tip.height, 0) };
  const beside = clamp(pointer.y - tip.height / 2, 0, room.y);
  const right = pointer.x + TIP_GAP;
  if (right <= room.x) return { left: right, top: beside };
  const left = pointer.x - TIP_GAP - tip.width;
  if (left >= 0) return { left, top: beside };
  return {
    left: clamp(pointer.x - tip.width / 2, 0, room.x),
    top: pointer.y < box.height / 2 ? room.y : 0,
  };
}

// The DOM half.

// All plots on the page share one cursor.
const SYNC_KEY = 'demo-dashboard';

const LINE_WIDTH = 2;
const LINE_DASH = [6, 4];
const BAR_RADIUS = 4;
const THRESHOLD_DASH = [4, 4];
const LONE_POINT = 6;
const HOVER_POINT = 12;
const HOVER_RING = 2;

const AXIS_FONT_PX = 11;
const Y_AXIS_GAP = 6;
// The time labels start low enough to clear the bottom label of the y axis,
// which hangs half a line under the plot: a tick on the plot's left edge puts
// its time label right under that one.
const X_AXIS_GAP = 9;
const X_AXIS_HEIGHT = 26;
const PLOT_PAD = 8;
const MIN_Y_TICK_SPACE = 24;
const X_TICK_GAP = 16;

// Time labels carry seconds for ranges up to this long.
const SECONDS_UP_TO = 15 * 60;

// The filter of the time axis and of its grid lines.
const onTimeScale = (chart, ticks) => ticksWithin(ticks, chart.scales.x.min, chart.scales.x.max);

// The canvas cannot resolve CSS variables, so the colours are read from the
// page; restyle() reads them again when the theme changes.
function readTheme() {
  const root = getComputedStyle(document.documentElement);
  const color = (property) => root.getPropertyValue(property).trim();
  return {
    color,
    grid: color('--grid'),
    axis: color('--axis'),
    surface: color('--surface2'),
    font: `${AXIS_FONT_PX}px ${getComputedStyle(document.body).fontFamily}`,
  };
}

let measuring = null;

function textWidth(text, font) {
  measuring ??= document.createElement('canvas').getContext('2d');
  measuring.font = font;
  return measuring.measureText(text).width;
}

const sizeOf = (rect) => ({ width: Math.floor(rect.width), height: Math.floor(rect.height) });

// A canvas that starts between two device pixels is drawn blurred, and the
// layout around a plot (text of any size, columns a 24th of the page wide)
// puts it there more often than not. onPixels moves a chart by the fraction
// of a pixel it is off.
function onPixels(box, chart) {
  const { left, top } = box.getBoundingClientRect();
  const off = (at) => Math.round(at * devicePixelRatio) / devicePixelRatio - at;
  chart.root.style.setProperty('left', `${off(left)}px`);
  chart.root.style.setProperty('top', `${off(top)}px`);
}

// The plots of one panel share the width of their y axis, so that stacked
// plots line up on the time axis. Each chart reports the width its own tick
// labels need and gets the widest back; a chart that was laid out with
// another width is told to lay itself out again.
function sharedAxisWidth() {
  const needs = new Map();
  const given = new Map();
  return {
    of(chart, need) {
      needs.set(chart, need);
      const width = Math.max(...needs.values());
      given.set(chart, width);
      for (const [other, was] of given) {
        if (was !== width) {
          given.set(other, width);
          other.redraw(false, true);
        }
      }
      return width;
    },
    drop(chart) {
      needs.delete(chart);
      given.delete(chart);
    },
  };
}

// uPlot's name for "a share of the plot's width" as the unit of a bar's place
// and size (BarsPathBuilderFacetUnit.PixelPercent in its type definitions).
const SHARE_OF_PLOT = 2;

// barPaths makes uPlot's bar drawer for a plot of `count` bar series. It
// tells uPlot where the bars of each series go (see barLayout) and rounds
// their tops; the foot of a bar stays square on the baseline.
function barPaths(uPlot, count) {
  const layout = (chart) => {
    const times = chart.data[0];
    const width = chart.valToPos(chart.scales.x.max, 'x');
    const step = times.length > 1 ? chart.valToPos(times[1], 'x') - chart.valToPos(times[0], 'x') : width;
    return { times, width, ...barLayout(count, step) };
  };
  return uPlot.paths.bars({
    // uPlot takes the radius as a share of the bar's thickness, half at most.
    radius: (chart) => [Math.min(BAR_RADIUS / layout(chart).thickness, 0.5), 0],
    disp: {
      x0: {
        unit: SHARE_OF_PLOT,
        values: (chart, seriesIndex) => {
          const { times, width, starts } = layout(chart);
          return times.map((time) => (chart.valToPos(time, 'x') + starts[seriesIndex - 1]) / width);
        },
      },
      size: {
        unit: SHARE_OF_PLOT,
        values: (chart) => {
          const { width, thickness } = layout(chart);
          return [thickness / width];
        },
      },
    },
  });
}

// mountTimeseries fills a panel frame and returns
// { load(ctx), render(model), restyle() }.
export function mountTimeseries(frame, panel, dashboard) {
  const { uPlot } = globalThis;
  if (!uPlot) throw new Error('the chart library, vendor/uPlot.iife.min.js, did not load');

  const columns = legendColumns(panel.legend);
  const thresholdSteps = panel.thresholds?.steps ?? [];
  const bars = panel.draw === 'bars';
  const hidden = new Set();
  const axisWidth = sharedAxisWidth();
  let theme = readTheme();
  let model = null;
  let signature = null;
  let seconds = true;
  let plots = [];
  let entries = [];
  let hovered = null;

  // A canvas has nothing to read out. To a screen reader the plots are one
  // image; the numbers are in the legend table, where the panel has one.
  const plotsBox = el('div', { class: 'ts-plots', role: 'img', 'aria-label': 'Chart' });
  const legendRows = el('tbody');
  const legend = el(
    'div',
    { class: 'ts-legend', hidden: true },
    el(
      'table',
      { 'aria-label': 'Legend' },
      el(
        'thead',
        {},
        el(
          'tr',
          {},
          el('th', { scope: 'col' }, el('span', { class: 'visually-hidden', text: 'Series' })),
          columns.map((column) => el('th', { scope: 'col', text: column.head })),
        ),
      ),
      legendRows,
    ),
  );
  const tipTime = el('div', { class: 'tip-time' });
  const tipRows = el('div', { class: 'tip-rows' });
  const tip = el('div', { class: 'ts-tip', hidden: true }, tipTime, tipRows);
  const drawing = el('div', { class: 'ts', dataset: { legend: panel.legend?.position ?? 'bottom' } }, plotsBox, legend);
  const empty = el('p', { class: 'panel-message', text: 'No data', hidden: true });

  frame.root.classList.add('timeseries');
  frame.body.append(drawing, tip, empty);

  const key = thresholdKey(panel.thresholds, panel.unit);
  if (key.length > 0) {
    frame.extras.append(
      el(
        'ul',
        { class: 'threshold-key', 'aria-label': 'Thresholds' },
        key.map(({ tone, text }) =>
          el(
            'li',
            { dataset: { tone }, title: `${tone} from ${text}` },
            el('span', { class: 'key key-dash' }),
            el('span', { class: 'visually-hidden', text: `${tone} from ` }),
            text,
          ),
        ),
      ),
    );
  }

  const seriesOf = (entry) => model.plots[entry.plotIndex].series[entry.seriesIndex];
  const clockWidth = () => textWidth(seconds ? '00:00:00' : '00:00', theme.font);

  // The mark next to a series' name: a line, a dashed line or a bar, like
  // the series itself. It carries the identity; the text stays in text colours.
  function seriesKey(series) {
    const shape = bars ? ' key-bar' : series.dash ? ' key-dash' : '';
    return el('span', { class: `key${shape}`, style: { '--key': `var(${series.color})` } });
  }

  function toggle(name) {
    if (!hidden.delete(name)) hidden.add(name);
    const show = !hidden.has(name);
    for (const entry of entries.filter((candidate) => candidate.name === name)) {
      entry.button.setAttribute('aria-pressed', String(show));
      entry.tipRow.hidden = !show;
      plots[entry.plotIndex].chart.setSeries(entry.seriesIndex + 1, { show });
    }
  }

  // One entry per series: its legend row and its tooltip row.
  function buildEntry(plotIndex, seriesIndex) {
    const series = model.plots[plotIndex].series[seriesIndex];
    const show = !hidden.has(series.name);
    const button = el(
      'button',
      { type: 'button', 'aria-pressed': String(show), title: series.name },
      seriesKey(series),
      el('span', { class: 'legend-name', text: series.name }),
    );
    const cells = columns.map(() => el('td'));
    const tipValue = el('strong');
    const tipName = el('span', { text: series.name });
    return {
      plotIndex,
      seriesIndex,
      name: series.name,
      button,
      cells,
      row: el('tr', { on: { click: () => toggle(series.name) } }, el('th', { scope: 'row' }, button), cells),
      tipValue,
      tipRow: el('div', { class: 'tip-row', hidden: !show }, seriesKey(series), tipValue, tipName),
    };
  }

  function showLegendValues() {
    for (const entry of entries) {
      const { values } = seriesOf(entry);
      const { unit } = model.plots[entry.plotIndex];
      entry.cells.forEach((cell, index) => {
        cell.textContent = formatValue(columns[index].of(values), unit);
      });
    }
  }

  // The tooltip follows the pointer in the plot it is over and lists every
  // series of the panel that has a sample at that time. A series without
  // one, such as a pod that is gone, is left out: after a few pod
  // replacements the list would otherwise outgrow the panel. The other plots
  // only move their cursor line.
  function showTip(chart) {
    if (hovered !== chart) return;
    const index = chart.cursor.idx;
    if (index === null) {
      tip.hidden = true;
      return;
    }
    tipTime.textContent = formatClock(model.times[index], true);
    for (const entry of entries) {
      const value = seriesOf(entry).values[index];
      entry.tipValue.textContent = formatValue(value, model.plots[entry.plotIndex].unit);
      entry.tipRow.hidden = value === null || hidden.has(entry.name);
    }
    tip.hidden = false;
    const box = frame.body.getBoundingClientRect();
    const pointer = {
      x: chart.rect.left - box.left + chart.cursor.left,
      y: chart.rect.top - box.top + chart.cursor.top,
    };
    const { left, top } = tipPosition(pointer, { width: tip.offsetWidth, height: tip.offsetHeight }, box);
    // Whole pixels keep the text sharp.
    tip.style.setProperty('transform', `translate(${Math.round(left)}px, ${Math.round(top)}px)`);
  }

  // Each threshold inside the y range is a dashed line in its tone's colour,
  // drawn over the grid and under the series.
  function drawThresholds(chart) {
    const { ctx, bbox, scales } = chart;
    ctx.save();
    ctx.lineWidth = devicePixelRatio;
    ctx.setLineDash(THRESHOLD_DASH.map((px) => px * devicePixelRatio));
    for (const step of thresholdSteps) {
      if (step.from < scales.y.min || step.from > scales.y.max) continue;
      // Half a pixel off the pixel grid keeps a line of odd width sharp.
      const y = Math.round(chart.valToPos(step.from, 'y', true)) + (ctx.lineWidth % 2) / 2;
      ctx.strokeStyle = theme.color(`--tone-${step.tone}`);
      ctx.beginPath();
      ctx.moveTo(bbox.left, y);
      ctx.lineTo(bbox.left + bbox.width, y);
      ctx.stroke();
    }
    ctx.restore();
  }

  function seriesOptions(series, color, drawBars) {
    const show = !hidden.has(series.name);
    if (drawBars) return { show, fill: color, width: 0, paths: drawBars, points: { show: false } };
    return {
      show,
      stroke: color,
      width: LINE_WIDTH,
      cap: series.dash ? 'butt' : 'round',
      dash: series.dash ? LINE_DASH.map((px) => px * devicePixelRatio) : undefined,
      fill: series.fill > 0 ? `color-mix(in srgb, ${color} ${series.fill * 100}%, transparent)` : undefined,
      spanGaps: panel.connectNulls === true,
      // No point markers, except on the samples no line reaches.
      points: {
        show: false,
        size: LONE_POINT,
        width: 0,
        fill: color,
        filter: (chart, seriesIndex) => {
          const lone = lonePoints(chart.data[seriesIndex], panel.connectNulls === true);
          return lone.length > 0 ? lone : null;
        },
      },
    };
  }

  // labelled is true for the plot that shows the time labels: the lowest one.
  function chartOptions(plot, labelled, size) {
    // min, max and thresholds are in the panel's unit; a plot in another
    // unit ranges on its own.
    const primary = plot.unit === panel.unit;
    const line = { stroke: theme.grid, width: 1 };
    const colors = plot.series.map((series) => theme.color(series.color));
    const drawBars = bars ? barPaths(uPlot, plot.series.length) : null;
    return {
      ...size,
      // Room for half a label at the top and right edges, and at the bottom
      // edge of a plot with no time axis under it.
      padding: [PLOT_PAD, () => Math.ceil(clockWidth() / 2), labelled ? 0 : PLOT_PAD, 0],
      legend: { show: false },
      select: { show: false },
      cursor: {
        y: false,
        drag: { x: false, y: false, setScale: false },
        points: bars
          ? { show: false }
          : {
              size: HOVER_POINT,
              width: HOVER_RING,
              fill: (chart, seriesIndex) => colors[seriesIndex - 1],
              stroke: theme.surface,
            },
        sync: { key: SYNC_KEY, scales: ['x', null] },
      },
      scales: {
        // Bars sit around their time position: half a step of room on each
        // side keeps the first and the last from being cut.
        x: bars
          ? {
              range: (chart, min, max) => {
                const times = chart.data[0];
                const half = times.length > 1 ? (times[1] - times[0]) / 2 : 0;
                return [min - half, max + half];
              },
            }
          : {},
        y: { range: (chart, min, max) => yRange(min, max, primary ? panel : {}) },
      },
      axes: [
        {
          stroke: theme.axis,
          font: theme.font,
          size: labelled ? X_AXIS_HEIGHT : 0,
          gap: X_AXIS_GAP,
          space: (chart, axisIndex, min, max, width) => Math.max(clockWidth() + X_TICK_GAP, tickSpace(width)),
          incrs: TIME_STEPS,
          filter: onTimeScale,
          values: (chart, ticks) =>
            ticks.map((time) => (labelled && time !== null ? formatClock(time, seconds) : null)),
          grid: { ...line, filter: onTimeScale },
          ticks: { show: false },
        },
        {
          stroke: theme.axis,
          font: theme.font,
          // Wide enough for its own labels, and for the left half of a time
          // label on a tick at the plot's left edge.
          size: (chart, labels) => {
            const widest = Math.max(0, ...(labels ?? []).map((label) => textWidth(label, theme.font)));
            return axisWidth.of(chart, Math.ceil(Math.max(widest + Y_AXIS_GAP + 2, clockWidth() / 2)));
          },
          gap: Y_AXIS_GAP,
          space: (chart, axisIndex, min, max, height) => Math.max(MIN_Y_TICK_SPACE, tickSpace(height)),
          ...(plot.unit === 'bytes/sec' ? { incrs: BYTE_STEPS } : {}),
          values: (chart, ticks, axisIndex, space, step) => formatTicks(ticks, plot.unit, step),
          grid: line,
          ticks: { show: false },
        },
      ],
      series: [{}, ...plot.series.map((series, index) => seriesOptions(series, colors[index], drawBars))],
      hooks: {
        drawAxes: primary && thresholdSteps.length > 0 ? [drawThresholds] : [],
        setCursor: [showTip],
      },
    };
  }

  const chartData = (plot) => [model.times, ...plot.series.map((series) => series.values)];

  const settle = () => {
    for (const { box, chart } of plots) onPixels(box, chart);
  };

  const resizes = new ResizeObserver((observed) => {
    for (const entry of observed) {
      const plot = plots.find((candidate) => candidate.box === entry.target);
      const size = sizeOf(entry.contentRect);
      if (plot && (size.width !== plot.chart.width || size.height !== plot.chart.height)) plot.chart.setSize(size);
    }
    settle();
  });

  // rebuild replaces the charts, the legend rows and the tooltip rows. It
  // runs when the set of series changes and when the theme does.
  function rebuild() {
    resizes.disconnect();
    for (const { chart } of plots) {
      axisWidth.drop(chart);
      chart.destroy();
    }
    hovered = null;
    tip.hidden = true;

    entries = model.plots.flatMap((plot, plotIndex) =>
      plot.series.map((_, seriesIndex) => buildEntry(plotIndex, seriesIndex)),
    );
    legendRows.replaceChildren(...entries.map((entry) => entry.row));
    tipRows.replaceChildren(...entries.map((entry) => entry.tipRow));
    showLegendValues();
    legend.hidden = !(panel.legend || model.plots.some((plot) => plot.series.length > 1));
    drawing.hidden = model.plots.length === 0;
    empty.hidden = !drawing.hidden;

    // All boxes go in before the first chart is made, so each chart starts
    // at its final size.
    const boxes = model.plots.map(() => el('div', { class: 'ts-plot' }));
    plotsBox.replaceChildren(...boxes);
    plots = model.plots.map((plot, index) => {
      const box = boxes[index];
      const labelled = index === model.plots.length - 1;
      const chart = new uPlot(chartOptions(plot, labelled, sizeOf(box.getBoundingClientRect())), chartData(plot), box);
      chart.over.addEventListener('mouseenter', () => {
        hovered = chart;
      });
      chart.over.addEventListener('mouseleave', () => {
        hovered = null;
        tip.hidden = true;
      });
      resizes.observe(box);
      return { box, chart };
    });
    settle();
  }

  return {
    async load(ctx) {
      const results = await Promise.all(panel.queries.map((query) => ctx.range(query.expr)));
      return buildPlots(panel, results, ctx.window, dashboard.palette);
    },
    render(next) {
      model = next;
      seconds = model.times.at(-1) - model.times[0] <= SECONDS_UP_TO;
      // A chart is kept for as long as it shows the same series.
      const names = JSON.stringify(model.plots.map((plot) => [plot.unit, plot.series.map((series) => series.name)]));
      if (names !== signature) {
        signature = names;
        rebuild();
        return;
      }
      plots.forEach(({ chart }, index) => chart.setData(chartData(model.plots[index])));
      showLegendValues();
      // The page around the plots may have moved since the last frame.
      settle();
    },
    restyle() {
      theme = readTheme();
      if (model) rebuild();
    },
  };
}
