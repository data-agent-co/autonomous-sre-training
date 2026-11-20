// A table panel: one row for every series its instant queries return.

import { el } from '../dom.mjs';
import { formatValue } from '../format.mjs';

// tableRows turns samples ({ labels, value }) into rows of cells
// ({ text, tone }), sorted by the text of the first column. A column shows a
// label, or the sample's value as a plain number when its label is "value".
// A cell whose text matches one of the column's map entries shows that
// entry's text and carries its tone; every other cell has no tone.
export function tableRows(columns, samples) {
  const rows = samples.map((sample) =>
    columns.map((column) => {
      const text =
        column.label === 'value' ? formatValue(sample.value, 'decimal') : (sample.labels[column.label] ?? '');
      const mapped = column.map?.find((entry) => entry.match === text);
      return mapped ? { text: mapped.text, tone: mapped.tone } : { text, tone: null };
    }),
  );
  return rows.sort((a, b) => a[0].text.localeCompare(b[0].text, 'en'));
}

// mountTable fills a panel frame and returns { load(ctx), render(rows) }.
export function mountTable(frame, panel) {
  if (!Array.isArray(panel.columns) || panel.columns.length === 0) throw new Error('a table panel needs columns');

  const body = el('tbody');
  const table = el(
    'table',
    { class: 'data-table' },
    el(
      'thead',
      {},
      el(
        'tr',
        {},
        panel.columns.map((column) =>
          el('th', { scope: 'col', text: column.header, style: { width: column.width ? `${column.width}px` : null } }),
        ),
      ),
    ),
    body,
  );
  const empty = el('p', { class: 'panel-message', text: panel.empty ?? 'No data', hidden: true });

  frame.root.classList.add('table');
  frame.body.append(table, empty);

  // A mapped cell shows a dot in its tone before the text.
  const cellContent = ({ text, tone }) =>
    tone ? el('span', { class: 'toned', dataset: { tone } }, el('span', { class: 'dot' }), text) : text;

  let shown = '';
  return {
    async load(ctx) {
      const samples = await Promise.all(panel.queries.map((query) => ctx.instant(query.expr)));
      return tableRows(panel.columns, samples.flat());
    },
    render(rows) {
      // Unchanged rows are left alone, so a refresh does not drop a text
      // selection the reader is making.
      const key = JSON.stringify(rows);
      if (key === shown) return;
      shown = key;
      table.hidden = rows.length === 0;
      empty.hidden = rows.length > 0;
      body.replaceChildren(...rows.map((row) => el('tr', {}, row.map((cell) => el('td', {}, cellContent(cell))))));
    },
  };
}
