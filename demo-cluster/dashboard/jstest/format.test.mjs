import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  formatClock,
  formatLegend,
  formatTicks,
  formatValue,
  lastValue,
  maxValue,
  meanValue,
  parseDuration,
  tickDecimals,
  toneFor,
} from '../web/js/format.mjs';

test('parseDuration reads seconds, minutes and hours', () => {
  assert.equal(parseDuration('5s'), 5);
  assert.equal(parseDuration('2m'), 120);
  assert.equal(parseDuration('6h'), 21600);
});

test('parseDuration rejects anything else', () => {
  for (const bad of ['', '0s', '5', 'm', '1.5m', '5 s', '2d', '-1m', '05s']) {
    assert.throws(() => parseDuration(bad), /bad duration/, `"${bad}"`);
  }
  assert.throws(() => parseDuration(undefined), /bad duration/);
});

test('formatValue keeps at most two decimals and drops trailing zeros', () => {
  assert.equal(formatValue(2.0193, 'decimal'), '2.02');
  assert.equal(formatValue(27.1, 'decimal'), '27.1');
  assert.equal(formatValue(15, 'decimal'), '15');
  assert.equal(formatValue(4.5, 'decimal'), '4.5');
  assert.equal(formatValue(100, 'decimal'), '100');
});

test('formatValue uses exactly the given decimals', () => {
  assert.equal(formatValue(100, 'decimal', 0), '100');
  assert.equal(formatValue(99.6, 'decimal', 0), '100');
  assert.equal(formatValue(0.02, 'ops/sec', 2), '0.02 ops/s');
  assert.equal(formatValue(0, 'ops/sec', 3), '0.000 ops/s');
  assert.equal(formatValue(0.02876, 'seconds', 2), '28.76 ms');
});

test('formatValue shows a missing value as an en dash', () => {
  assert.equal(formatValue(null, 'requests/sec'), '–');
  assert.equal(formatValue(undefined, 'decimal'), '–');
  assert.equal(formatValue(NaN, 'percent', 1), '–');
  assert.equal(formatValue(Infinity, 'decimal'), '–');
});

test('formatValue adds the unit suffix', () => {
  assert.equal(formatValue(22.7, 'percent'), '22.7%');
  assert.equal(formatValue(2.0193, 'requests/sec'), '2.02 req/s');
  assert.equal(formatValue(0.02, 'ops/sec'), '0.02 ops/s');
});

test('formatValue scales milliseconds from µs to minutes', () => {
  assert.equal(formatValue(0.95, 'milliseconds'), '950 µs');
  assert.equal(formatValue(1.03, 'milliseconds'), '1.03 ms');
  assert.equal(formatValue(100, 'milliseconds'), '100 ms');
  assert.equal(formatValue(2500, 'milliseconds'), '2.5 s');
  assert.equal(formatValue(90000, 'milliseconds'), '1.5 min');
  assert.equal(formatValue(7200000, 'milliseconds'), '120 min');
});

test('formatValue scales seconds the same way', () => {
  assert.equal(formatValue(0.0000025, 'seconds'), '2.5 µs');
  assert.equal(formatValue(0.005, 'seconds'), '5 ms');
  assert.equal(formatValue(1.2, 'seconds'), '1.2 s');
  assert.equal(formatValue(300, 'seconds'), '5 min');
});

test('formatValue scales byte rates by 1024', () => {
  assert.equal(formatValue(562, 'bytes/sec'), '562 B/s');
  assert.equal(formatValue(100000, 'bytes/sec'), '97.66 KiB/s');
  assert.equal(formatValue(500000, 'bytes/sec'), '488.28 KiB/s');
  assert.equal(formatValue(3 * 1024 ** 2, 'bytes/sec'), '3 MiB/s');
  assert.equal(formatValue(5 * 1024 ** 4, 'bytes/sec'), '5120 GiB/s');
});

test('formatValue moves up a scale when rounding reaches it', () => {
  assert.equal(formatValue(999.4, 'milliseconds'), '999.4 ms');
  assert.equal(formatValue(999.999, 'milliseconds'), '1 s');
  assert.equal(formatValue(0.999999, 'milliseconds'), '1 ms');
  assert.equal(formatValue(59.999, 'seconds'), '1 min');
  assert.equal(formatValue(1023.999, 'bytes/sec'), '1 KiB/s');
  assert.equal(formatValue(999.6, 'milliseconds', 0), '1 s');
});

test('formatValue shows zero on the unit\'s own scale', () => {
  assert.equal(formatValue(0, 'decimal'), '0');
  assert.equal(formatValue(0, 'percent'), '0%');
  assert.equal(formatValue(0, 'milliseconds'), '0 ms');
  assert.equal(formatValue(0, 'seconds'), '0 s');
  assert.equal(formatValue(0, 'bytes/sec'), '0 B/s');
  assert.equal(formatValue(-0, 'requests/sec'), '0 req/s');
});

test('formatValue scales by the absolute value and keeps the sign', () => {
  assert.equal(formatValue(-2500, 'milliseconds'), '-2.5 s');
  assert.equal(formatValue(-100000, 'bytes/sec'), '-97.66 KiB/s');
  assert.equal(formatValue(-4.5, 'percent'), '-4.5%');
  // A value that rounds to zero has no sign.
  assert.equal(formatValue(-0.0000001, 'decimal'), '0');
});

test('formatValue keeps three significant digits below 1', () => {
  // One reconcile error in 5 minutes is a rate of 0.0034: it must not read 0.
  assert.equal(formatValue(0.003389887964202783, 'ops/sec'), '0.00339 ops/s');
  assert.equal(formatValue(0.0049, 'requests/sec'), '0.0049 req/s');
  assert.equal(formatValue(0.123456, 'decimal'), '0.123');
  assert.equal(formatValue(-0.001, 'decimal'), '-0.001');
  assert.equal(formatValue(0.5, 'percent'), '0.5%');
  assert.equal(formatValue(0.02, 'ops/sec'), '0.02 ops/s');
  // Rounding can reach 1, where two decimals take over.
  assert.equal(formatValue(0.9996, 'decimal'), '1');
  assert.equal(formatValue(1.2345, 'decimal'), '1.23');
});

test('formatValue shows less than a millionth as 0, never in exponent form', () => {
  assert.equal(formatValue(0.000001, 'decimal'), '0.000001');
  assert.equal(formatValue(0.0000001, 'decimal'), '0');
  assert.equal(formatValue(1e-12, 'percent'), '0%');
});

test('formatValue with given decimals rounds as before', () => {
  assert.equal(formatValue(0.0034, 'ops/sec', 2), '0.00 ops/s');
  assert.equal(formatValue(0.0034, 'ops/sec', 3), '0.003 ops/s');
});

test('formatValue rejects an unknown unit, with or without a value', () => {
  assert.throws(() => formatValue(1, 'furlongs'), /unknown unit "furlongs"/);
  assert.throws(() => formatValue(null, 'furlongs'), /unknown unit "furlongs"/);
});

test('tickDecimals asks for more decimals only when two cannot tell ticks apart', () => {
  assert.equal(tickDecimals('ops/sec', 0.5), undefined);
  assert.equal(tickDecimals('ops/sec', 0.25), undefined);
  assert.equal(tickDecimals('decimal', 1), undefined);
  assert.equal(tickDecimals('ops/sec', 0.005), 3);
  assert.equal(tickDecimals('percent', 0.025), 3);
  assert.equal(tickDecimals('decimal', 0.0000005), 7);
});

test('tickDecimals leaves units that rescale alone', () => {
  assert.equal(tickDecimals('seconds', 0.005), undefined);
  assert.equal(tickDecimals('milliseconds', 0.0025), undefined);
  assert.equal(tickDecimals('bytes/sec', 0.125), undefined);
});

test('formatLegend fills label values into the template', () => {
  const labels = { container: 'leak-fixture', pod: 'recommend-svc-abc' };
  assert.equal(formatLegend('{{container}} @ {{pod}}', labels, 'Panel'), 'leak-fixture @ recommend-svc-abc');
  assert.equal(formatLegend('{{ container }}', labels, 'Panel'), 'leak-fixture');
  assert.equal(formatLegend('payments-api', labels, 'Panel'), 'payments-api');
});

test('formatLegend leaves a missing label empty', () => {
  assert.equal(formatLegend('failed / sec ({{backend}})', {}, 'Panel'), 'failed / sec ()');
  assert.equal(formatLegend('{{container}} @ {{pod}}', {}, 'Panel'), ' @ ');
});

test('formatLegend falls back when the name would be blank', () => {
  assert.equal(formatLegend('{{object_namespace}}', {}, 'Panel'), 'Panel');
  assert.equal(formatLegend(' {{a}} ', { a: '' }, 'Panel'), 'Panel');
});

test('formatLegend without a template writes the label set', () => {
  assert.equal(formatLegend(undefined, { pod: 'a', job: 'x' }, 'Panel'), '{job="x", pod="a"}');
  assert.equal(formatLegend('', { reason: 'say "hi"' }, 'Panel'), '{reason="say \\"hi\\""}');
  assert.equal(formatLegend(undefined, {}, 'Panel'), 'Panel');
});

const thresholds = { base: 'critical', steps: [{ from: 1, tone: 'warning' }, { from: 3, tone: 'good' }] };

test('toneFor picks the last step at or below the value', () => {
  assert.equal(toneFor(0, thresholds), 'critical');
  assert.equal(toneFor(0.99, thresholds), 'critical');
  assert.equal(toneFor(1, thresholds), 'warning');
  assert.equal(toneFor(2.9, thresholds), 'warning');
  assert.equal(toneFor(3, thresholds), 'good');
  assert.equal(toneFor(400, thresholds), 'good');
  assert.equal(toneFor(-5, thresholds), 'critical');
});

test('toneFor gives no value the tone none', () => {
  assert.equal(toneFor(null, thresholds), 'none');
});

test('toneFor uses the base tone when there are no steps', () => {
  assert.equal(toneFor(7, { base: 'info', steps: [] }), 'info');
  assert.equal(toneFor(7, { base: 'info' }), 'info');
});

test('lastValue, meanValue and maxValue skip nulls', () => {
  const values = [null, 4, 1, null, 3, null];
  assert.equal(lastValue(values), 3);
  assert.equal(meanValue(values), 8 / 3);
  assert.equal(maxValue(values), 4);
});

test('lastValue, meanValue and maxValue keep zero and negative values', () => {
  assert.equal(lastValue([5, 0, null]), 0);
  assert.equal(meanValue([-2, 0, 2]), 0);
  assert.equal(maxValue([-3, -1, null]), -1);
});

test('lastValue, meanValue and maxValue are null when nothing is left', () => {
  for (const values of [[], [null, null]]) {
    assert.equal(lastValue(values), null);
    assert.equal(meanValue(values), null);
    assert.equal(maxValue(values), null);
  }
});

test('formatClock gives the local time with or without seconds', () => {
  const morning = new Date(2026, 10, 19, 3, 4, 5).getTime() / 1000;
  assert.equal(formatClock(morning, true), '03:04:05');
  assert.equal(formatClock(morning, false), '03:04');

  const night = new Date(2026, 10, 19, 23, 59, 0).getTime() / 1000;
  assert.equal(formatClock(night, true), '23:59:00');
});

test('formatTicks puts every tick of an axis on one scale', () => {
  // formatValue alone would say '0 s', '500 µs' or '-1.46 MiB/s' here.
  assert.deepEqual(formatTicks([0, 0.05, 0.1], 'seconds', 0.05), ['0 ms', '50 ms', '100 ms']);
  assert.deepEqual(formatTicks([0, 0.5, 1, 1.5], 'milliseconds', 0.5), ['0 ms', '0.5 ms', '1 ms', '1.5 ms']);
  assert.deepEqual(formatTicks([0, 0.25, 0.5, 0.75, 1], 'milliseconds', 0.25), [
    '0 ms',
    '0.25 ms',
    '0.5 ms',
    '0.75 ms',
    '1 ms',
  ]);
  assert.deepEqual(formatTicks([0, 0.5, 1, 1.5], 'seconds', 0.5), ['0 s', '0.5 s', '1 s', '1.5 s']);
  assert.deepEqual(formatTicks([0, 1, 2], 'seconds', 1), ['0 s', '1 s', '2 s']);
});

test('formatTicks takes the largest scale on which the step is a short number', () => {
  // Half a minute is short; a third of a minute is not, so 20 s steps stay in seconds.
  assert.deepEqual(formatTicks([0, 30, 60, 90], 'seconds', 30), ['0 min', '0.5 min', '1 min', '1.5 min']);
  assert.deepEqual(formatTicks([0, 20, 40, 60, 80], 'seconds', 20), ['0 s', '20 s', '40 s', '60 s', '80 s']);

  const KiB = 1024;
  const MiB = 1024 * KiB;
  assert.deepEqual(formatTicks([0, 512 * KiB, MiB, 1.5 * MiB], 'bytes/sec', 512 * KiB), [
    '0 MiB/s',
    '0.5 MiB/s',
    '1 MiB/s',
    '1.5 MiB/s',
  ]);
  assert.deepEqual(formatTicks([-MiB, -768 * KiB, -512 * KiB, -256 * KiB, 0], 'bytes/sec', 256 * KiB), [
    '-1 MiB/s',
    '-0.75 MiB/s',
    '-0.5 MiB/s',
    '-0.25 MiB/s',
    '0 MiB/s',
  ]);
  // 200 KiB/s is 0.1953125 MiB/s: the axis stays in KiB/s past 1024.
  assert.deepEqual(formatTicks([0, 400 * KiB, 800 * KiB, 1200 * KiB], 'bytes/sec', 400 * KiB), [
    '0 KiB/s',
    '400 KiB/s',
    '800 KiB/s',
    '1200 KiB/s',
  ]);
  assert.deepEqual(formatTicks([0, 128, 256, 384], 'bytes/sec', 128), ['0 B/s', '128 B/s', '256 B/s', '384 B/s']);
});

test('formatTicks gives a unit that does not rescale the decimals its step needs', () => {
  assert.deepEqual(formatTicks([0, 25, 50], 'percent', 25), ['0%', '25%', '50%']);
  assert.deepEqual(formatTicks([0, 0.5, 1], 'requests/sec', 0.5), ['0 req/s', '0.5 req/s', '1 req/s']);
  assert.deepEqual(formatTicks([0, 0.005, 0.01], 'ops/sec', 0.005), ['0.000 ops/s', '0.005 ops/s', '0.010 ops/s']);
});

test('formatTicks copes with a lone zero, no tick and an unknown unit', () => {
  assert.deepEqual(formatTicks([0], 'seconds', 1), ['0 s']);
  assert.deepEqual(formatTicks([0], 'bytes/sec', 1024), ['0 B/s']);
  assert.deepEqual(formatTicks([], 'seconds', 1), []);
  assert.throws(() => formatTicks([0, 1], 'furlongs', 1), /unknown unit "furlongs"/);
});
