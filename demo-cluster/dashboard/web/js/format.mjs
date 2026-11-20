// Pure helpers shared by the panels and the topology: durations, values with
// their unit, series names, threshold tones and the legend calculations.
// Nothing here touches the DOM, so node --test covers all of it.

const DURATION_SECONDS = { s: 1, m: 60, h: 3600 };

// parseDuration('2m') is 120: digits followed by s, m or h, in seconds.
export function parseDuration(text) {
  const match = /^([1-9][0-9]*)([smh])$/.exec(text);
  if (!match) throw new Error(`bad duration "${text}": want digits followed by s, m or h`);
  return Number(match[1]) * DURATION_SECONDS[match[2]];
}

// A unit is a ladder of scales, smallest first; `per` is how many of a scale
// make one of the next. `base` is the scale the query's numbers are in. A
// value is shown on the largest scale it fills.
const TIME_SCALES = [
  { suffix: ' µs', per: 1000 },
  { suffix: ' ms', per: 1000 },
  { suffix: ' s', per: 60 },
  { suffix: ' min' },
];

const BYTE_RATE_SCALES = [
  { suffix: ' B/s', per: 1024 },
  { suffix: ' KiB/s', per: 1024 },
  { suffix: ' MiB/s', per: 1024 },
  { suffix: ' GiB/s' },
];

const UNITS = {
  decimal: { scales: [{ suffix: '' }], base: 0 },
  percent: { scales: [{ suffix: '%' }], base: 0 },
  'requests/sec': { scales: [{ suffix: ' req/s' }], base: 0 },
  'ops/sec': { scales: [{ suffix: ' ops/s' }], base: 0 },
  milliseconds: { scales: TIME_SCALES, base: 1 },
  seconds: { scales: TIME_SCALES, base: 2 },
  'bytes/sec': { scales: BYTE_RATE_SCALES, base: 0 },
};

const NO_VALUE = '–';
const DEFAULT_DECIMALS = 2;
// Below 1, a value keeps this many significant digits, down to MAX_DECIMALS.
const SMALL_DIGITS = 3;
const MAX_DECIMALS = 6;
const MAX_TICK_DECIMALS = 12;
// A tick on a unit that rescales has at most this many decimals: 0.125 MiB/s.
const SCALED_TICK_DECIMALS = 3;

function unitOf(unit) {
  const def = UNITS[unit];
  if (!def) throw new Error(`unknown unit "${unit}"`);
  return def;
}

// The number as text: exactly `decimals` decimals when given. Otherwise at
// most two, with trailing zeros dropped. Below 1, two decimals would turn a
// small rate into 0, so three significant digits are kept there (0.00339), as
// Perses does, down to a millionth.
function digits(number, decimals) {
  if (decimals !== undefined) return number.toFixed(decimals);
  const size = Math.abs(number);
  const small = size > 0 && size < 1;
  const places = small ? Math.min(MAX_DECIMALS, SMALL_DIGITS - 1 - Math.floor(Math.log10(size))) : DEFAULT_DECIMALS;
  return String(Number(number.toFixed(places)));
}

// onScale puts a number on the largest scale of its unit that it fills:
// { size, suffix }. The scale is picked by the absolute value; zero stays on
// the unit's own scale.
function onScale(value, unit, decimals) {
  const { scales, base } = unitOf(unit);
  let size = Math.abs(value);
  let index = base;
  if (size !== 0) {
    while (index > 0 && size < 1) {
      index -= 1;
      size *= scales[index].per;
    }
    // Compare the rounded number: 999.999 ms rounds to 1000 ms, which is 1 s.
    while (index < scales.length - 1 && Number(digits(size, decimals)) >= scales[index].per) {
      size /= scales[index].per;
      index += 1;
    }
  }
  return { size, suffix: scales[index].suffix };
}

// formatValue(2.0193, 'requests/sec') is '2.02 req/s'. A value that is null
// (or not a finite number) is an en dash. The sign is kept, and zero is on
// the unit's own scale ('0 ms').
export function formatValue(value, unit, decimals) {
  unitOf(unit);
  if (!Number.isFinite(value)) return NO_VALUE;
  const { size, suffix } = onScale(value, unit, decimals);
  const text = digits(size, decimals);
  const sign = value < 0 && Number(text) !== 0 ? '-' : '';
  return `${sign}${text}${suffix}`;
}

// factors lists, for each scale of a unit, what a number in the unit's own
// scale is multiplied by to be on that scale.
function factors({ scales, base }) {
  const factor = new Array(scales.length);
  factor[base] = 1;
  for (let index = base + 1; index < scales.length; index++) factor[index] = factor[index - 1] / scales[index - 1].per;
  for (let index = base - 1; index >= 0; index--) factor[index] = factor[index + 1] * scales[index].per;
  return factor;
}

// decimalsOf counts the decimals a step needs to be written out in full, and
// stops counting at max.
function decimalsOf(step, max) {
  let decimals = 0;
  while (decimals < max && Math.abs(Number(step.toFixed(decimals)) - step) > Math.abs(step) * 1e-9) decimals += 1;
  return decimals;
}

// formatTicks labels the ticks of one axis, which are `step` apart.
//
// On a unit that rescales, every tick is on the same scale: '0 ms', '0.5 ms',
// '1 ms', where formatValue would put '500 µs' between '0 ms' and '1 ms'.
// The scale is the largest one the highest tick fills on which the step is
// still a short number: 0.5 min, but 20 s and not 0.33 min.
export function formatTicks(ticks, unit, step) {
  const def = unitOf(unit);
  if (def.scales.length === 1) {
    const decimals = tickDecimals(unit, step);
    return ticks.map((tick) => formatValue(tick, unit, decimals));
  }
  const factor = factors(def);
  const top = ticks.reduce((highest, tick) => Math.max(highest, Math.abs(tick)), 0);
  let index = top === 0 ? def.base : def.scales.findLastIndex((scale, at) => at === 0 || top * factor[at] >= 1);
  while (index > 0 && decimalsOf(step * factor[index], SCALED_TICK_DECIMALS + 1) > SCALED_TICK_DECIMALS) index -= 1;
  const decimals = decimalsOf(step * factor[index], SCALED_TICK_DECIMALS);
  // Through Number: no trailing zeros, and no sign on a zero.
  return ticks.map((tick) => `${Number((tick * factor[index]).toFixed(decimals))}${def.scales[index].suffix}`);
}

// tickDecimals says how many decimals an axis needs so that ticks `step`
// apart read differently: 0.005, 0.010 and 0.015 ops/s must not differ in
// length. It is undefined when the default formatting is enough, and for
// every unit that rescales: formatTicks puts those on one scale itself.
export function tickDecimals(unit, step) {
  if (unitOf(unit).scales.length > 1) return undefined;
  let decimals = 0;
  while (decimals < MAX_TICK_DECIMALS && Number(step.toFixed(decimals)) !== step) decimals += 1;
  return decimals > DEFAULT_DECIMALS ? decimals : undefined;
}

// formatLegend names a series. With a template, every {{label}} becomes that
// label's value (nothing when the series has no such label). Without one the
// name is the label set, written {a="b", c="d"}. A name that would be blank
// is the fallback instead.
export function formatLegend(template, labels, fallback) {
  if (template) {
    const name = template.replace(/\{\{\s*([^{}\s]+)\s*\}\}/g, (_, label) => labels[label] ?? '');
    return name.trim() === '' ? fallback : name;
  }
  const pairs = Object.keys(labels).sort().map((label) => `${label}=${JSON.stringify(labels[label])}`);
  return pairs.length === 0 ? fallback : `{${pairs.join(', ')}}`;
}

// toneFor gives the tone of a value: the tone of the last step whose `from`
// is not above it, or the base tone below every step. No value has the tone
// "none".
export function toneFor(value, thresholds) {
  if (value === null) return 'none';
  let tone = thresholds.base;
  for (const step of thresholds.steps ?? []) {
    if (step.from <= value) tone = step.tone;
  }
  return tone;
}

// The legend calculations. Each skips nulls and is null when nothing is left.

export function lastValue(values) {
  return values.findLast((value) => value !== null) ?? null;
}

export function meanValue(values) {
  const present = values.filter((value) => value !== null);
  return present.length === 0 ? null : present.reduce((sum, value) => sum + value, 0) / present.length;
}

export function maxValue(values) {
  return values.reduce((max, value) => (value !== null && (max === null || value > max) ? value : max), null);
}

const pad2 = (number) => String(number).padStart(2, '0');

// formatClock gives the local time of a Unix timestamp as HH:MM:SS, or
// HH:MM without seconds.
export function formatClock(unixSeconds, withSeconds) {
  const date = new Date(unixSeconds * 1000);
  const clock = `${pad2(date.getHours())}:${pad2(date.getMinutes())}`;
  return withSeconds ? `${clock}:${pad2(date.getSeconds())}` : clock;
}
