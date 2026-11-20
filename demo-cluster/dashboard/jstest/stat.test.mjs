import assert from 'node:assert/strict';
import { test } from 'node:test';

import { toneFor } from '../web/js/format.mjs';
import { TONE_WORDS, sparkline, statModel } from '../web/js/panels/stat.mjs';

test('every tone a tile can take has its word, and a failing state is never called OK', () => {
  // The words the READMEs name.
  assert.deepEqual(TONE_WORDS, { good: 'OK', warning: 'Warning', critical: 'Critical', info: 'Idle', none: 'No data' });

  const thresholds = { base: 'good', steps: [{ from: 1, tone: 'warning' }, { from: 2, tone: 'critical' }] };
  assert.equal(TONE_WORDS[toneFor(0, thresholds)], 'OK');
  assert.equal(TONE_WORDS[toneFor(1, thresholds)], 'Warning');
  assert.equal(TONE_WORDS[toneFor(5, thresholds)], 'Critical');
  assert.equal(TONE_WORDS[toneFor(null, thresholds)], 'No data');
});

test('sparkline spreads the samples over the box, lowest at the bottom', () => {
  assert.deepEqual(sparkline([0, 5, 10]), { path: 'M0 100L50 50L100 0', end: { x: 100, y: 0 } });
  assert.deepEqual(sparkline([4, 2, 3, 2]), { path: 'M0 0L33.33 100L66.67 50L100 100', end: { x: 100, y: 100 } });
});

test('sparkline draws a flat series through the middle', () => {
  assert.deepEqual(sparkline([7, 7, 7]), { path: 'M0 50L50 50L100 50', end: { x: 100, y: 50 } });
  assert.deepEqual(sparkline([0, 0]), { path: 'M0 50L100 50', end: { x: 100, y: 50 } });
});

test('sparkline breaks the line at a gap', () => {
  assert.deepEqual(sparkline([0, 10, null, 10, 0]), {
    path: 'M0 100L25 0M75 0L100 100',
    end: { x: 100, y: 100 },
  });
});

test('sparkline draws a sample between two gaps as a dot', () => {
  assert.deepEqual(sparkline([null, 3, null, 1, 1]), {
    path: 'M25 0h0M75 100L100 100',
    end: { x: 100, y: 100 },
  });
});

test('sparkline ends at the last sample, not at the right edge', () => {
  assert.deepEqual(sparkline([2, 4, null, null, null]).end, { x: 25, y: 0 });
});

test('sparkline of one sample is a dot at the right edge', () => {
  assert.deepEqual(sparkline([5]), { path: 'M100 50h0', end: { x: 100, y: 50 } });
});

test('sparkline has nothing to draw without samples', () => {
  assert.deepEqual(sparkline([]), { path: '', end: null });
  assert.deepEqual(sparkline([null, null]), { path: '', end: null });
});

const timeWindow = { start: 100, end: 115, step: 5 };

const panel = {
  title: 'Demo apps healthy',
  unit: 'decimal',
  thresholds: { base: 'critical', steps: [{ from: 1, tone: 'warning' }, { from: 3, tone: 'good' }] },
  queries: [{ expr: 'x', legend: 'ready' }],
};

test('statModel shows the last sample of the first series', () => {
  const series = [
    { labels: {}, times: [100, 105, 110], values: [1, 2, 4] },
    { labels: {}, times: [100, 105, 110, 115], values: [9, 9, 9, 9] },
  ];
  const model = statModel(panel, series, timeWindow);

  assert.equal(model.text, '4');
  assert.equal(model.name, 'ready');
  assert.equal(model.tone, 'good');
  // The sparkline is the whole series on the window's grid; 115 has no sample.
  assert.deepEqual(model.spark, { path: 'M0 100L33.33 66.67L66.67 0', end: { x: 66.67, y: 0 } });
});

test('statModel takes its tone from the thresholds', () => {
  const at = (value) => statModel(panel, [{ labels: {}, times: [115], values: [value] }], timeWindow).tone;
  assert.equal(at(0), 'critical');
  assert.equal(at(2), 'warning');
  assert.equal(at(3), 'good');
});

test('statModel formats with the panel unit and decimals', () => {
  const latency = { ...panel, unit: 'seconds', decimals: 2 };
  assert.equal(statModel(latency, [{ labels: {}, times: [115], values: [0.02876] }], timeWindow).text, '28.76 ms');
});

test('statModel names the series from its labels', () => {
  const byPod = { ...panel, queries: [{ expr: 'x', legend: '{{pod}}' }] };
  assert.equal(statModel(byPod, [{ labels: { pod: 'a-1' }, times: [115], values: [1] }], timeWindow).name, 'a-1');
  assert.equal(statModel(byPod, [{ labels: {}, times: [115], values: [1] }], timeWindow).name, 'Demo apps healthy');
});

test('statModel has no value when the query returns nothing', () => {
  assert.deepEqual(statModel(panel, [], timeWindow), {
    text: '–',
    name: '',
    tone: 'none',
    spark: { path: '', end: null },
  });
});

test('statModel has no value when every sample is missing', () => {
  const model = statModel(panel, [{ labels: {}, times: [100, 105], values: [null, null] }], timeWindow);
  assert.equal(model.text, '–');
  assert.equal(model.tone, 'none');
  assert.equal(model.name, 'ready');
});

// On a coarse window the caller passes the answer of an instant query.

test('statModel shows the current sample when it is given one', () => {
  const series = [{ labels: {}, times: [100, 105, 110, 115], values: [0, 0, 0, 0] }];
  const model = statModel(panel, series, timeWindow, [{ labels: {}, time: 119, value: 3 }]);

  assert.equal(model.text, '3');
  assert.equal(model.tone, 'good');
  // The sparkline still shows the range answer.
  assert.deepEqual(model.spark, { path: 'M0 50L33.33 50L66.67 50L100 50', end: { x: 100, y: 50 } });
});

test('statModel takes the current sample of the same series', () => {
  const byPod = { ...panel, queries: [{ expr: 'x', legend: '{{pod}}' }] };
  const series = [{ labels: { pod: 'b', zone: 'x' }, times: [115], values: [1] }];
  const current = [
    { labels: { pod: 'a', zone: 'x' }, time: 119, value: 9 },
    // One label fewer and one label more are other series too.
    { labels: { pod: 'b' }, time: 119, value: 8 },
    { labels: { pod: 'b', zone: 'x', node: 'n' }, time: 119, value: 7 },
    { labels: { pod: 'b', zone: 'x' }, time: 119, value: 2 },
  ];
  const model = statModel(byPod, series, timeWindow, current);

  assert.equal(model.text, '2');
  assert.equal(model.name, 'b');
});

test('statModel keeps the last sample when the current answer lacks the series or its value', () => {
  const series = [{ labels: { pod: 'b' }, times: [110, 115], values: [1, 4] }];

  assert.equal(statModel(panel, series, timeWindow, []).text, '4');
  assert.equal(statModel(panel, series, timeWindow, [{ labels: { pod: 'a' }, time: 119, value: 9 }]).text, '4');
  assert.equal(statModel(panel, series, timeWindow, [{ labels: { pod: 'b' }, time: 119, value: null }]).text, '4');
});

test('statModel shows a series that is in the current answer only', () => {
  const byPod = { ...panel, queries: [{ expr: 'x', legend: '{{pod}}' }] };
  const model = statModel(byPod, [], timeWindow, [{ labels: { pod: 'new' }, time: 119, value: 1 }]);

  assert.equal(model.text, '1');
  assert.equal(model.name, 'new');
  assert.equal(model.tone, 'warning');
  assert.deepEqual(model.spark, { path: '', end: null });
});
