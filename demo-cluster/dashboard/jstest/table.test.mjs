import assert from 'node:assert/strict';
import { test } from 'node:test';

import { tableRows } from '../web/js/panels/table.mjs';

const plain = (text) => ({ text, tone: null });

test('tableRows shows one label per column', () => {
  const columns = [{ label: 'pod', header: 'Pod' }, { label: 'reason', header: 'Reason' }];
  const samples = [{ labels: { pod: 'payments-api-1', reason: 'Completed', extra: 'x' }, value: 1 }];
  assert.deepEqual(tableRows(columns, samples), [[plain('payments-api-1'), plain('Completed')]]);
});

test('tableRows leaves the cell of a missing label empty', () => {
  const columns = [{ label: 'pod', header: 'Pod' }, { label: 'container', header: 'Container' }];
  const samples = [{ labels: { pod: 'all-running' }, value: 0 }];
  assert.deepEqual(tableRows(columns, samples), [[plain('all-running'), plain('')]]);
});

test('tableRows shows the value column as a plain number', () => {
  const columns = [{ label: 'kind', header: 'Kind' }, { label: 'value', header: 'Count' }];
  const samples = [
    { labels: { kind: 'Pod' }, value: 3 },
    { labels: { kind: 'Service' }, value: 1.256 },
    { labels: { kind: 'Unknown' }, value: null },
  ];
  assert.deepEqual(tableRows(columns, samples), [
    [plain('Pod'), plain('3')],
    [plain('Service'), plain('1.26')],
    [plain('Unknown'), plain('–')],
  ]);
});

test('tableRows replaces a mapped cell and gives it the tone', () => {
  const columns = [
    { label: 'pod', header: 'Pod', map: [{ match: 'all-running', text: 'All containers running', tone: 'good' }] },
    { label: 'value', header: 'Status', map: [{ match: '0', text: 'unreachable', tone: 'critical' }] },
  ];
  const samples = [
    { labels: { pod: 'all-running' }, value: 0 },
    { labels: { pod: 'all-running-2' }, value: 10 },
  ];
  assert.deepEqual(tableRows(columns, samples), [
    [{ text: 'All containers running', tone: 'good' }, { text: 'unreachable', tone: 'critical' }],
    // Only an exact match is mapped.
    [plain('all-running-2'), plain('10')],
  ]);
});

test('tableRows sorts by the text of the first column', () => {
  const columns = [{ label: 'pod', header: 'Pod' }, { label: 'reason', header: 'Reason' }];
  const samples = [
    { labels: { pod: 'recommend-svc-1', reason: 'b' }, value: 1 },
    { labels: { pod: 'payments-api-1', reason: 'c' }, value: 1 },
    { labels: { pod: 'payments-api-1', reason: 'a' }, value: 1 },
  ];
  assert.deepEqual(
    tableRows(columns, samples).map((row) => row.map((cell) => cell.text)),
    // Rows with the same first cell keep the order they came in.
    [['payments-api-1', 'c'], ['payments-api-1', 'a'], ['recommend-svc-1', 'b']],
  );
});

test('tableRows has no rows without samples', () => {
  assert.deepEqual(tableRows([{ label: 'pod', header: 'Pod' }], []), []);
});
