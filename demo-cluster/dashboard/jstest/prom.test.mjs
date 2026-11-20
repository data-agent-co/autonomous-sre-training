import assert from 'node:assert/strict';
import { test } from 'node:test';

import { PromError, alignWindow, createPromClient, gridTimes, parseValue, toGrid } from '../web/js/prom.mjs';

// A fetch stand-in that records each call and answers with the given reply.
function fakeFetch(reply) {
  const calls = [];
  const fetchImpl = async (url, init) => {
    calls.push({ url, init });
    if (reply instanceof Error) throw reply;
    return {
      ok: reply.status >= 200 && reply.status < 300,
      status: reply.status,
      json: async () => {
        if (reply.body === undefined) throw new SyntaxError('not JSON');
        return reply.body;
      },
    };
  };
  return { calls, fetchImpl };
}

const success = (data) => ({ status: 200, body: { status: 'success', data } });

test('parseValue turns NaN and the infinities into null', () => {
  assert.equal(parseValue('1.5'), 1.5);
  assert.equal(parseValue('0'), 0);
  assert.equal(parseValue('NaN'), null);
  assert.equal(parseValue('+Inf'), null);
  assert.equal(parseValue('-Inf'), null);
});

test('alignWindow never goes below the scrape interval', () => {
  assert.deepEqual(alignWindow(1003, 120), { start: 880, end: 1000, step: 5 });
});

test('alignWindow widens the step to stay under maxPoints', () => {
  // 6h over 720 points is 30s, a multiple of the 5s minimum. 1h still fits at 5s.
  assert.equal(alignWindow(7200, 3600).step, 5);
  assert.equal(alignWindow(43200, 21600).step, 30);
  assert.equal(alignWindow(43200, 21600, { maxPoints: 240 }).step, 90);
});

test('alignWindow snaps end to a step boundary and keeps start on the grid', () => {
  const w = alignWindow(100047, 3600);
  assert.equal(w.end % w.step, 0);
  assert.equal((w.end - w.start) % w.step, 0);
  assert.ok(w.end <= 100047 && 100047 - w.end < w.step);
});

test('alignWindow gives the same window for every now inside one step', () => {
  assert.deepEqual(alignWindow(1000, 120), alignWindow(1004.9, 120));
  assert.notDeepEqual(alignWindow(1000, 120), alignWindow(1005, 120));
});

test('gridTimes includes both ends', () => {
  assert.deepEqual(gridTimes({ start: 10, end: 25, step: 5 }), [10, 15, 20, 25]);
});

test('toGrid leaves null where a sample is missing and drops samples off the grid', () => {
  const series = { times: [5, 10, 20, 30], values: [9, 1, 3, 9] };
  assert.deepEqual(toGrid(series, { start: 10, end: 25, step: 5 }), [1, null, 3, null]);
});

test('instant posts the query as a form and maps a vector', async () => {
  const { calls, fetchImpl } = fakeFetch(success({
    resultType: 'vector',
    result: [
      { metric: { pod: 'a' }, value: [100, '2'] },
      { metric: { pod: 'b' }, value: [100, 'NaN'] },
    ],
  }));
  const prom = createPromClient({ fetchImpl });

  const samples = await prom.instant('up{job="x"}', { time: 100 });

  assert.deepEqual(samples, [
    { labels: { pod: 'a' }, time: 100, value: 2 },
    { labels: { pod: 'b' }, time: 100, value: null },
  ]);
  assert.equal(calls[0].url, '/api/v1/query');
  assert.equal(calls[0].init.method, 'POST');
  assert.equal(calls[0].init.headers['Content-Type'], 'application/x-www-form-urlencoded');
  assert.deepEqual(Object.fromEntries(new URLSearchParams(calls[0].init.body)), { query: 'up{job="x"}', time: '100' });
});

test('instant without a time sends the query alone', async () => {
  // Prometheus evaluates at its own now. A time of "undefined" would be a 400.
  const { calls, fetchImpl } = fakeFetch(success({ resultType: 'vector', result: [] }));
  const prom = createPromClient({ fetchImpl });

  await prom.instant('up');

  assert.deepEqual(Object.fromEntries(new URLSearchParams(calls[0].init.body)), { query: 'up' });
});

test('instant maps a scalar to one sample without labels', async () => {
  const { fetchImpl } = fakeFetch(success({ resultType: 'scalar', result: [100, '7'] }));
  const prom = createPromClient({ fetchImpl });

  assert.deepEqual(await prom.instant('scalar(x)'), [{ labels: {}, time: 100, value: 7 }]);
});

test('range sends start, end and step and maps a matrix', async () => {
  const { calls, fetchImpl } = fakeFetch(success({
    resultType: 'matrix',
    result: [{ metric: { job: 'x' }, values: [[10, '1'], [15, '+Inf'], [20, '3']] }],
  }));
  const prom = createPromClient({ baseUrl: '/prom', fetchImpl });

  const series = await prom.range('rate(x[1m])', { start: 10, end: 20, step: 5 });

  assert.deepEqual(series, [{ labels: { job: 'x' }, times: [10, 15, 20], values: [1, null, 3] }]);
  assert.equal(calls[0].url, '/prom/api/v1/query_range');
  assert.deepEqual(Object.fromEntries(new URLSearchParams(calls[0].init.body)),
    { query: 'rate(x[1m])', start: '10', end: '20', step: '5' });
});

test('range replaces $__interval with the step', async () => {
  const { calls, fetchImpl } = fakeFetch(success({ resultType: 'matrix', result: [] }));
  const prom = createPromClient({ fetchImpl });

  await prom.range('x - x offset $__interval > 0 or y[$__interval]', { start: 0, end: 60, step: 15 });

  assert.equal(new URLSearchParams(calls[0].init.body).get('query'), 'x - x offset 15s > 0 or y[15s]');
});

test('a Prometheus error keeps its message, status and type', async () => {
  const { fetchImpl } = fakeFetch({
    status: 400,
    body: { status: 'error', errorType: 'bad_data', error: 'parse error: unexpected end of input' },
  });
  const prom = createPromClient({ fetchImpl });

  await assert.rejects(prom.instant('up{'), (err) => {
    assert.ok(err instanceof PromError);
    assert.equal(err.message, 'parse error: unexpected end of input');
    assert.equal(err.status, 400);
    assert.equal(err.type, 'bad_data');
    return true;
  });
});

test('a non-JSON error response reports the HTTP status', async () => {
  const { fetchImpl } = fakeFetch({ status: 502 });
  const prom = createPromClient({ fetchImpl });

  await assert.rejects(prom.instant('up'), (err) => {
    assert.ok(err instanceof PromError);
    assert.equal(err.message, 'HTTP 502 from /api/v1/query');
    assert.equal(err.status, 502);
    assert.equal(err.type, 'response');
    return true;
  });
});

test('a 200 whose body is not a success is still an error', async () => {
  const { fetchImpl } = fakeFetch({ status: 200, body: { status: 'error', errorType: 'execution', error: 'boom' } });
  const prom = createPromClient({ fetchImpl });

  await assert.rejects(prom.instant('up'), { name: 'PromError', message: 'boom', type: 'execution' });
});

test('a failed fetch is a network error', async () => {
  const { fetchImpl } = fakeFetch(new TypeError('fetch failed'));
  const prom = createPromClient({ fetchImpl });

  await assert.rejects(prom.instant('up'), { name: 'PromError', type: 'network', status: 0 });
});

test('a query that outlives the timeout is a timeout error', async () => {
  const fetchImpl = (url, init) => new Promise((resolve, reject) => {
    init.signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')));
  });
  const prom = createPromClient({ fetchImpl, timeoutMs: 10 });

  await assert.rejects(prom.instant('up'), { name: 'PromError', type: 'timeout' });
});

// A fetch stand-in whose answer has headers but a body that never arrives:
// reading it ends only when the request is aborted.
function stalledBody() {
  return async (url, init) => ({
    ok: true,
    status: 200,
    json: () => new Promise((resolve, reject) => {
      init.signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')));
    }),
  });
}

test('a body that outlives the timeout is a timeout error', async () => {
  const prom = createPromClient({ fetchImpl: stalledBody(), timeoutMs: 10 });

  await assert.rejects(prom.range('up', { start: 0, end: 60, step: 5 }), { name: 'PromError', type: 'timeout' });
});

test('a connection that breaks in the middle of the body is a network error', async () => {
  const fetchImpl = async () => ({
    ok: true,
    status: 200,
    json: async () => {
      throw new TypeError('terminated');
    },
  });
  const prom = createPromClient({ fetchImpl });

  await assert.rejects(prom.instant('up'), { name: 'PromError', type: 'network', status: 0 });
});

test('a caller abort while the body arrives rejects with the abort', async () => {
  const prom = createPromClient({ fetchImpl: stalledBody() });
  const caller = new AbortController();

  const pending = prom.instant('up', { signal: caller.signal });
  // Let the headers arrive, so the abort lands on the body.
  await new Promise((resolve) => setImmediate(resolve));
  caller.abort();

  await assert.rejects(pending, (err) => !(err instanceof PromError) && err.name === 'AbortError');
});

test('a caller abort rejects with the abort, not a PromError', async () => {
  const fetchImpl = (url, init) => new Promise((resolve, reject) => {
    init.signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')));
  });
  const prom = createPromClient({ fetchImpl });
  const caller = new AbortController();

  const pending = prom.instant('up', { signal: caller.signal });
  caller.abort();

  await assert.rejects(pending, (err) => !(err instanceof PromError) && err.name === 'AbortError');
});

test('an instant query that returns a matrix is rejected', async () => {
  const { fetchImpl } = fakeFetch(success({ resultType: 'matrix', result: [] }));
  const prom = createPromClient({ fetchImpl });

  await assert.rejects(prom.instant('up[1m]'), { name: 'PromError', type: 'response' });
});
