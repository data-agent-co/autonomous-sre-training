import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  BYTE_STEPS,
  TIME_STEPS,
  barLayout,
  buildPlots,
  legendColumns,
  lonePoints,
  seriesColors,
  thresholdKey,
  tickSpace,
  ticksWithin,
  tipPosition,
  yRange,
} from '../web/js/panels/timeseries.mjs';

const palette = { 'payments-api': 1, 'recommend-svc': 2, 'catalog-svc': 3, nginx: 3, loadgen: 4, 'leak-fixture': 5 };

const named = (...names) => names.map((name) => ({ name }));

test('seriesColors gives an entity the same slot in every panel', () => {
  assert.deepEqual(seriesColors(named('recommend-svc', 'payments-api'), palette), ['--series-2', '--series-1']);
  const containers = named('leak-fixture @ recommend-svc-5f54f476b9-njn62', 'nginx @ catalog-svc-69fdb5c5b9-mn8b7');
  assert.deepEqual(seriesColors(containers, palette), ['--series-5', '--series-3']);
});

test('seriesColors takes the longest palette key the name starts with', () => {
  const overlapping = { pay: 4, 'payments-api': 1 };
  assert.deepEqual(seriesColors(named('payments-api @ pod', 'payroll'), overlapping), ['--series-1', '--series-4']);
  // A key in the middle of a name does not count.
  assert.deepEqual(seriesColors(named('p95 payments-api'), palette), ['--series-7']);
});

test('seriesColors lets the query set a slot or a tone', () => {
  const series = [
    { name: 'payments-api', color: 6 },
    { name: 'limit', color: 'critical' },
  ];
  assert.deepEqual(seriesColors(series, palette), ['--series-6', '--tone-critical']);
});

test('seriesColors hands the free slots out in a fixed order', () => {
  assert.deepEqual(seriesColors(named('failed / sec ()', 'reconcile errors / sec', 'avg latency'), palette), [
    '--series-7',
    '--series-6',
    '--series-8',
  ]);
});

test('seriesColors skips the slots the panel already uses', () => {
  const series = [{ name: 'a' }, { name: 'pinned', color: 7 }, { name: 'payments-api' }, ...named('b', 'c', 'd')];
  // 7 and 1 are taken, so the free order is 6, 8, 2, 3.
  assert.deepEqual(seriesColors(series, palette), [
    '--series-6',
    '--series-7',
    '--series-1',
    '--series-8',
    '--series-2',
    '--series-3',
  ]);
});

test('seriesColors starts over after the eighth colour and never invents one', () => {
  const colors = seriesColors(named('a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i', 'j'), {});
  assert.deepEqual(colors.slice(0, 8), [7, 6, 8, 1, 2, 3, 4, 5].map((slot) => `--series-${slot}`));
  assert.deepEqual(colors.slice(8), ['--series-7', '--series-6']);
});

const timeWindow = { start: 100, end: 120, step: 5 };

test('buildPlots names, colours and grids the series of every query', () => {
  const panel = {
    title: 'Saturation',
    unit: 'percent',
    queries: [
      { expr: 'a', legend: '{{container}} @ {{pod}}' },
      { expr: 'b', legend: 'limit', color: 'critical', dash: true, fill: 0 },
    ],
  };
  const results = [
    [
      { labels: { container: 'nginx', pod: 'catalog-svc-1' }, times: [100, 105, 120], values: [1, 2, 3] },
      { labels: { container: 'leak-fixture', pod: 'recommend-svc-1' }, times: [110], values: [9] },
    ],
    [{ labels: {}, times: [100, 105, 110, 115, 120], values: [100, 100, 100, 100, 100] }],
  ];

  assert.deepEqual(buildPlots(panel, results, timeWindow, palette), {
    times: [100, 105, 110, 115, 120],
    plots: [
      {
        unit: 'percent',
        series: [
          // Sorted by name within the query; the second query follows.
          {
            name: 'leak-fixture @ recommend-svc-1',
            unit: 'percent',
            color: '--series-5',
            dash: false,
            fill: 0.1,
            values: [null, null, 9, null, null],
          },
          {
            name: 'nginx @ catalog-svc-1',
            unit: 'percent',
            color: '--series-3',
            dash: false,
            fill: 0.1,
            values: [1, 2, null, null, 3],
          },
          {
            name: 'limit',
            unit: 'percent',
            color: '--tone-critical',
            dash: true,
            fill: 0,
            values: [100, 100, 100, 100, 100],
          },
        ],
      },
    ],
  });
});

test('buildPlots draws a query with its own unit in a second plot', () => {
  const panel = {
    title: 'LLM calls',
    unit: 'ops/sec',
    fill: 0.2,
    queries: [
      { expr: 'latency', legend: 'avg latency', unit: 'seconds' },
      { expr: 'errors', legend: 'failed / sec ({{backend}})' },
    ],
  };
  const flat = (labels) => ({ labels, times: [100], values: [0] });
  const { plots } = buildPlots(panel, [[flat({})], [flat({ backend: 'openai' })]], timeWindow, palette);

  // The panel's own unit comes first, whatever the order of the queries.
  assert.deepEqual(plots.map((plot) => plot.unit), ['ops/sec', 'seconds']);
  assert.deepEqual(plots.map((plot) => plot.series.map((series) => series.name)), [
    ['failed / sec (openai)'],
    ['avg latency'],
  ]);
  // Free slots are counted over the whole panel, in query order.
  assert.deepEqual(plots.map((plot) => plot.series[0].color), ['--series-6', '--series-7']);
  assert.equal(plots[0].series[0].fill, 0.2);
});

test('buildPlots falls back to the panel title for a series without a name', () => {
  const panel = { title: 'Detections', unit: 'decimal', queries: [{ expr: 'x', legend: '{{object_namespace}}' }] };
  const { plots } = buildPlots(panel, [[{ labels: {}, times: [100], values: [0] }]], timeWindow, palette);
  assert.equal(plots[0].series[0].name, 'Detections');
});

test('buildPlots has no plots when no query returned a series', () => {
  const panel = { title: 'Empty', unit: 'decimal', queries: [{ expr: 'x' }, { expr: 'y', unit: 'seconds' }] };
  assert.deepEqual(buildPlots(panel, [[], []], timeWindow, palette), { times: [100, 105, 110, 115, 120], plots: [] });
});

test('legendColumns shows the last value unless the panel lists others', () => {
  assert.deepEqual(legendColumns(undefined).map((column) => column.head), ['Last']);
  assert.deepEqual(legendColumns({ position: 'right' }).map((column) => column.head), ['Last']);
  assert.deepEqual(legendColumns({ position: 'right', values: [] }).map((column) => column.head), ['Last']);
  const all = legendColumns({ values: ['last', 'mean', 'max'] });
  assert.deepEqual(all.map((column) => column.head), ['Last', 'Mean', 'Max']);
});

test('legendColumns computes each column from the series, skipping gaps', () => {
  const values = [4, null, 1, 3, null];
  const all = legendColumns({ values: ['last', 'mean', 'max'] });
  assert.deepEqual(all.map((column) => column.of(values)), [3, 8 / 3, 4]);
});

test('legendColumns rejects a value it does not know', () => {
  assert.throws(() => legendColumns({ values: ['last', 'p99'] }), /unknown legend value "p99"/);
});

test('thresholdKey formats each step in the panel unit', () => {
  const thresholds = { base: 'good', steps: [{ from: 100, tone: 'warning' }, { from: 500, tone: 'critical' }] };
  assert.deepEqual(thresholdKey(thresholds, 'milliseconds'), [
    { tone: 'warning', text: '100 ms' },
    { tone: 'critical', text: '500 ms' },
  ]);
  assert.deepEqual(thresholdKey({ base: 'good', steps: [{ from: 100000, tone: 'warning' }] }, 'bytes/sec'), [
    { tone: 'warning', text: '97.66 KiB/s' },
  ]);
});

test('thresholdKey is empty without thresholds', () => {
  assert.deepEqual(thresholdKey(undefined, 'percent'), []);
  assert.deepEqual(thresholdKey({ base: 'good' }, 'percent'), []);
});

// Compare with a tolerance: the headroom is a product of floats.
function assertRange(actual, expected) {
  assert.equal(actual.length, 2);
  actual.forEach((bound, side) => {
    assert.ok(Math.abs(bound - expected[side]) < 1e-9, `got [${actual}], want [${expected}]`);
  });
}

test('yRange runs from zero to a little above the data', () => {
  assertRange(yRange(1.8, 2.5), [0, 2.75]);
  assertRange(yRange(1.8, 2.5, { min: 0 }), [0, 2.75]);
});

test('yRange keeps the panel bounds as they are', () => {
  assertRange(yRange(0, 3, { min: 0, max: 100 }), [0, 100]);
  assertRange(yRange(20, 250, { min: 0, max: 100 }), [0, 100]);
  assertRange(yRange(-5, 50, { min: 0 }), [0, 55]);
  assertRange(yRange(10, 50, { max: 80 }), [0, 80]);
});

test('yRange goes below zero for negative data, with the same headroom', () => {
  assertRange(yRange(-50, 100), [-65, 115]);
  // All negative: zero stays in view at the top.
  assertRange(yRange(-10, -2), [-11, 1]);
});

test('yRange is 0..1 without data and for nothing but zeros', () => {
  assertRange(yRange(null, null), [0, 1]);
  assertRange(yRange(0, 0), [0, 1]);
  assertRange(yRange(0, 0, { min: 0 }), [0, 1]);
  assertRange(yRange(null, null, { min: 5 }), [5, 6]);
});

test('yRange brings a threshold into view once the data is half way there', () => {
  const thresholds = { base: 'good', steps: [{ from: 100, tone: 'warning' }, { from: 500, tone: 'critical' }] };
  // Far below both: the data keeps the whole plot.
  assertRange(yRange(1, 3, { min: 0, thresholds }), [0, 3.3]);
  assertRange(yRange(1, 49.9, { min: 0, thresholds }), [0, 54.89]);
  // Half way to the first step: it is drawn, the second is not.
  assertRange(yRange(1, 50, { min: 0, thresholds }), [0, 110]);
  assertRange(yRange(1, 120, { min: 0, thresholds }), [0, 132]);
  assertRange(yRange(1, 250, { min: 0, thresholds }), [0, 550]);
  assertRange(yRange(1, 900, { min: 0, thresholds }), [0, 990]);
});

test('yRange never lets a threshold move a fixed bound', () => {
  const thresholds = { base: 'good', steps: [{ from: 1, tone: 'warning' }, { from: 5, tone: 'critical' }] };
  assertRange(yRange(0, 4, { min: 0, max: 100, thresholds }), [0, 100]);
  assertRange(yRange(0, 0, { min: 0, thresholds }), [0, 1]);
});

test('tickSpace lets four steps fit on an axis, and never five', () => {
  for (const length of [90, 260, 1000]) {
    assert.ok(4 * tickSpace(length) <= length, `four steps on ${length} px`);
    assert.ok(5 * tickSpace(length) > length, `five steps on ${length} px`);
  }
});

const ascends = (steps) => steps.every((step, index) => index === 0 || step > steps[index - 1]);

test('TIME_STEPS ascends and has a step for every range the page offers', () => {
  assert.ok(ascends(TIME_STEPS));
  // A step as long as the range always leaves one tick in view.
  for (const range of [2 * 60, 5 * 60, 15 * 60, 3600, 6 * 3600]) {
    assert.ok(TIME_STEPS.includes(range), `${range} s`);
  }
});

test('BYTE_STEPS ascends in round binary amounts', () => {
  assert.ok(ascends(BYTE_STEPS));
  assert.equal(BYTE_STEPS[0], 1);
  for (const round of [1024, 5 * 1024, 2.5 * 1024 ** 2, 100 * 1024 ** 3]) {
    assert.ok(BYTE_STEPS.includes(round), `${round} B/s`);
  }
  // 100000 B/s is round in base 10, which is 97.66 KiB/s on the axis.
  assert.ok(!BYTE_STEPS.includes(100000));
  // A quarter and a half of the next scale, not 250 and 500: past 1024 KiB/s
  // the axis then reads 0.5, 1 and 1.5 MiB/s.
  assert.ok(BYTE_STEPS.includes(256 * 1024) && BYTE_STEPS.includes(512 * 1024));
  assert.ok(!BYTE_STEPS.includes(250 * 1024) && !BYTE_STEPS.includes(500 * 1024));
});

test('ticksWithin blanks the ticks outside the scale', () => {
  assert.deepEqual(ticksWithin([90, 100, 150, 200, 210], 100, 200), [null, 100, 150, 200, null]);
  // The one tick of a step longer than the range, past its end.
  assert.deepEqual(ticksWithin([300], 100, 220), [null]);
  assert.deepEqual(ticksWithin([], 100, 200), []);
});

test('barLayout centres one bar on its time position', () => {
  const { thickness, starts } = barLayout(1, 20);
  assert.equal(thickness, 14);
  assert.deepEqual(starts, [-7]);
});

test('barLayout puts the bars of a step side by side, two pixels apart', () => {
  const { thickness, starts } = barLayout(2, 20);
  assert.equal(thickness, 6);
  assert.deepEqual(starts, [-7, 1]);
});

test('barLayout never makes a bar thicker than 24 pixels', () => {
  const { thickness, starts } = barLayout(3, 1000);
  assert.equal(thickness, 24);
  assert.deepEqual(starts, [-38, -12, 14]);
});

test('barLayout shrinks the gap where it would leave hairlines', () => {
  const { thickness, starts } = barLayout(4, 5);
  assert.equal(thickness, 0.75);
  assert.deepEqual(starts, [-1.875, -0.875, 0.125, 1.125]);
  // The bars of one step stay inside it.
  assert.ok(starts[0] >= -2.5 && starts[3] + thickness <= 2.5);
});

test('lonePoints finds the samples that have no neighbour', () => {
  assert.deepEqual(lonePoints([1, null, 2, 3, null, 4], false), [0, 5]);
  assert.deepEqual(lonePoints([null, 5, null], false), [1]);
  assert.deepEqual(lonePoints([1, 2, 3], false), []);
  assert.deepEqual(lonePoints([null, null], false), []);
  assert.deepEqual(lonePoints([7], false), [0]);
});

test('lonePoints with gaps connected only marks a single sample', () => {
  assert.deepEqual(lonePoints([1, null, 2, null, 3], true), []);
  assert.deepEqual(lonePoints([null, null, 2, null], true), [2]);
  assert.deepEqual(lonePoints([null, null], true), []);
});

const tip = { width: 100, height: 60 };
const box = { width: 400, height: 200 };

test('tipPosition puts the tooltip to the right of the pointer', () => {
  assert.deepEqual(tipPosition({ x: 50, y: 100 }, tip, box), { left: 62, top: 70 });
});

test('tipPosition moves to the left when the right side has no room', () => {
  assert.deepEqual(tipPosition({ x: 350, y: 100 }, tip, box), { left: 238, top: 70 });
  // Exactly fitting on the right still counts.
  assert.deepEqual(tipPosition({ x: 288, y: 100 }, tip, box), { left: 300, top: 70 });
});

test('tipPosition keeps the tooltip inside the box vertically', () => {
  assert.deepEqual(tipPosition({ x: 50, y: 5 }, tip, box), { left: 62, top: 0 });
  assert.deepEqual(tipPosition({ x: 50, y: 199 }, tip, box), { left: 62, top: 140 });
});

test('tipPosition leaves the pointer row when the box is too narrow for either side', () => {
  const narrow = { width: 180, height: 200 };
  // Pointer in the upper half: the tooltip goes to the bottom edge.
  assert.deepEqual(tipPosition({ x: 90, y: 40 }, tip, narrow), { left: 40, top: 140 });
  // Pointer in the lower half: to the top edge.
  assert.deepEqual(tipPosition({ x: 90, y: 150 }, tip, narrow), { left: 40, top: 0 });
  // Near a side it stays inside the box.
  assert.deepEqual(tipPosition({ x: 100, y: 40 }, { width: 170, height: 60 }, narrow), { left: 10, top: 140 });
});

test('tipPosition copes with a tooltip larger than its box', () => {
  assert.deepEqual(tipPosition({ x: 20, y: 20 }, { width: 500, height: 300 }, box), { left: 0, top: 0 });
});
