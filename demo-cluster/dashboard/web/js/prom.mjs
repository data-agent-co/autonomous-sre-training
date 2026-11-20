// Prometheus HTTP API client. The dashboard server proxies /api/v1/query and
// /api/v1/query_range to the in-cluster Prometheus, so the browser only ever
// talks to its own origin.

const QUERY_TIMEOUT_MS = 10000;

// A failed query. status is the HTTP status (0 when no response arrived) and
// type is Prometheus' errorType, or "network", "timeout" or "response".
export class PromError extends Error {
  constructor(message, { status = 0, type = '' } = {}) {
    super(message);
    this.name = 'PromError';
    this.status = status;
    this.type = type;
  }
}

// Prometheus sends sample values as strings. NaN and the infinities are not
// drawable and not summable, so they become null, the same as a missing
// sample.
export function parseValue(text) {
  const value = Number(text);
  return Number.isFinite(value) ? value : null;
}

// alignWindow picks the query window for a range of rangeSec seconds ending
// now. The step is a multiple of minStepSec (the scrape interval: a smaller
// step only repeats samples) that keeps the window under maxPoints points.
// 720 points keep the step at 30s on the longest range the page offers, 6h:
// the queries look back 1m, and a step longer than that would skip what
// happens in between, such as a container restart.
// end is snapped down to a step boundary, so successive refreshes ask for the
// same grid and a chart only gains points on its right edge.
export function alignWindow(nowSec, rangeSec, { minStepSec = 5, maxPoints = 720 } = {}) {
  const step = Math.max(minStepSec, Math.ceil(rangeSec / maxPoints / minStepSec) * minStepSec);
  const end = Math.floor(nowSec / step) * step;
  const start = end - Math.ceil(rangeSec / step) * step;
  return { start, end, step };
}

// gridTimes lists the timestamps start, start+step, ... end.
export function gridTimes({ start, end, step }) {
  const times = [];
  for (let t = start; t <= end; t += step) times.push(t);
  return times;
}

// toGrid places one range-query series on the window's grid. Slots with no
// sample are null, which charts draw as a gap.
export function toGrid(series, { start, end, step }) {
  const slots = new Array(Math.floor((end - start) / step) + 1).fill(null);
  for (let i = 0; i < series.times.length; i++) {
    const slot = Math.round((series.times[i] - start) / step);
    if (slot >= 0 && slot < slots.length) slots[slot] = series.values[i];
  }
  return slots;
}

function toSamples(data) {
  if (data.resultType === 'scalar') {
    return [{ labels: {}, time: data.result[0], value: parseValue(data.result[1]) }];
  }
  if (data.resultType !== 'vector') {
    throw new PromError(`instant query returned a ${data.resultType}, not a vector`, { type: 'response' });
  }
  return data.result.map((s) => ({ labels: s.metric, time: s.value[0], value: parseValue(s.value[1]) }));
}

function toSeries(data) {
  if (data.resultType !== 'matrix') {
    throw new PromError(`range query returned a ${data.resultType}, not a matrix`, { type: 'response' });
  }
  return data.result.map((s) => ({
    labels: s.metric,
    times: s.values.map((pair) => pair[0]),
    values: s.values.map((pair) => parseValue(pair[1])),
  }));
}

// withInterval replaces $__interval with the range step, as Perses does, so a
// bar can count what changed since the previous point instead of repeating a
// fixed window at every point.
export function withInterval(expr, stepSec) {
  return expr.replaceAll('$__interval', `${stepSec}s`);
}

// createPromClient returns { instant, range }.
//
//   instant(expr, { time, signal })             -> [{ labels, time, value }]
//   range(expr, { start, end, step, signal })   -> [{ labels, times, values }]
//
// Times are Unix seconds. value and values[] hold numbers or null (see
// parseValue). Both reject with a PromError.
export function createPromClient({
  baseUrl = '',
  fetchImpl = (...args) => globalThis.fetch(...args),
  timeoutMs = QUERY_TIMEOUT_MS,
} = {}) {
  async function call(path, params, signal) {
    const timeout = new AbortController();
    const timer = setTimeout(() => timeout.abort(), timeoutMs);
    const onAbort = () => timeout.abort();
    signal?.addEventListener('abort', onAbort, { once: true });
    if (signal?.aborted) timeout.abort();

    let response;
    let body;
    try {
      // POST, so a long expression is not limited by URL length.
      response = await fetchImpl(`${baseUrl}${path}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
        body: new URLSearchParams(params).toString(),
        signal: timeout.signal,
      });
      // A body that is not JSON (the error page of something in between)
      // still leaves a status to report. A body that never arrives whole is
      // a timeout or a broken connection: the catch below says which.
      body = await response.json().catch((err) => {
        if (err instanceof SyntaxError) return null;
        throw err;
      });
    } catch (err) {
      if (signal?.aborted) throw err;
      if (timeout.signal.aborted) {
        throw new PromError(`query timed out after ${timeoutMs / 1000}s`, { type: 'timeout' });
      }
      throw new PromError('the dashboard server is unreachable', { type: 'network' });
    } finally {
      clearTimeout(timer);
      signal?.removeEventListener('abort', onAbort);
    }

    if (!response.ok || body?.status !== 'success') {
      const message = body?.error || `HTTP ${response.status} from ${path}`;
      throw new PromError(message, { status: response.status, type: body?.errorType || 'response' });
    }
    return body.data;
  }

  return {
    async instant(expr, { time, signal } = {}) {
      const params = { query: expr };
      if (time !== undefined) params.time = String(time);
      return toSamples(await call('/api/v1/query', params, signal));
    },
    async range(expr, { start, end, step, signal }) {
      const params = { query: withInterval(expr, step), start: String(start), end: String(end), step: String(step) };
      return toSeries(await call('/api/v1/query_range', params, signal));
    },
  };
}
