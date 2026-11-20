import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';

import { parseMarkdown } from '../web/js/markdown.mjs';

const text = (value) => ({ type: 'text', text: value });
const code = (value) => ({ type: 'code', text: value });
const bold = (...inlines) => ({ type: 'bold', inlines });
const italic = (...inlines) => ({ type: 'italic', inlines });

// The inlines of a one-paragraph text.
function inline(source) {
  const blocks = parseMarkdown(source);
  assert.equal(blocks.length, 1);
  assert.equal(blocks[0].type, 'paragraph');
  return blocks[0].inlines;
}

test('a ### line is a heading', () => {
  assert.deepEqual(parseMarkdown('### Detection layers'), [
    { type: 'heading', inlines: [text('Detection layers')] },
  ]);
});

test('other heading levels stay plain text', () => {
  assert.deepEqual(parseMarkdown('# Title'), [{ type: 'paragraph', inlines: [text('# Title')] }]);
  assert.deepEqual(parseMarkdown('#### Deep'), [{ type: 'paragraph', inlines: [text('#### Deep')] }]);
  assert.deepEqual(parseMarkdown('###nospace'), [{ type: 'paragraph', inlines: [text('###nospace')] }]);
});

test('consecutive lines form one paragraph and a blank line ends it', () => {
  assert.deepEqual(parseMarkdown('one\ntwo\r\n\nthree'), [
    { type: 'paragraph', inlines: [text('one two')] },
    { type: 'paragraph', inlines: [text('three')] },
  ]);
});

test('lines starting with "- " form a list', () => {
  assert.deepEqual(parseMarkdown('- first\n- second `x`\n'), [
    { type: 'list', items: [[text('first')], [text('second '), code('x')]] },
  ]);
});

test('a heading or a list ends the paragraph above it', () => {
  const blocks = parseMarkdown('intro\n- item\n### Next\ntail');
  assert.deepEqual(blocks.map((block) => block.type), ['paragraph', 'list', 'heading', 'paragraph']);
});

test('a pipe table has a header and rows', () => {
  const blocks = parseMarkdown('| App | Limit |\n|---|:---:|\n| `payments-api` | 128Mi |\n| catalog-svc | 64Mi |');
  assert.deepEqual(blocks, [
    {
      type: 'table',
      head: [[text('App')], [text('Limit')]],
      rows: [
        [[code('payments-api')], [text('128Mi')]],
        [[text('catalog-svc')], [text('64Mi')]],
      ],
    },
  ]);
});

test('table rows are fitted to the header', () => {
  const [table] = parseMarkdown('| a | b | c |\n|---|---|---|\n| 1 |\n| 1 | 2 | 3 | 4 |\n| 1 |  | 3');
  assert.deepEqual(table.rows, [
    [[text('1')], [], []],
    [[text('1')], [text('2')], [text('3')]],
    [[text('1')], [], [text('3')]],
  ]);
});

test('an escaped pipe stays inside its cell', () => {
  const [table] = parseMarkdown('| expr |\n|---|\n| a \\| b |');
  assert.deepEqual(table.rows, [[[text('a | b')]]]);
});

test('pipe lines without a rule under them are a paragraph', () => {
  assert.deepEqual(parseMarkdown('| not | a table |'), [
    { type: 'paragraph', inlines: [text('| not | a table |')] },
  ]);
});

test('a table ends at the first line that is not a row', () => {
  const blocks = parseMarkdown('| a |\n|---|\n| 1 |\nafter');
  assert.deepEqual(blocks.map((block) => block.type), ['table', 'paragraph']);
});

test('inline code, bold and italic', () => {
  assert.deepEqual(inline('run `make up` **now**, *please*'), [
    text('run '),
    code('make up'),
    text(' '),
    bold(text('now')),
    text(', '),
    italic(text('please')),
  ]);
});

test('marks nest inside bold and italic, but not inside code', () => {
  assert.deepEqual(inline('**a *b* `c`**'), [bold(text('a '), italic(text('b')), text(' '), code('c'))]);
  assert.deepEqual(inline('*a **b** c*'), [italic(text('a '), bold(text('b')), text(' c'))]);
  assert.deepEqual(inline('`**not bold**`'), [code('**not bold**')]);
});

test('marks that do not close stay plain text', () => {
  assert.deepEqual(inline('a `b'), [text('a `b')]);
  assert.deepEqual(inline('**open'), [text('**open')]);
  assert.deepEqual(inline('5 * 3 * 2'), [text('5 * 3 * 2')]);
  assert.deepEqual(inline('pod=~"x-.*" and *'), [text('pod=~"x-.*" and *')]);
  assert.deepEqual(inline('``'), [text('``')]);
});

test('HTML in the text is text', () => {
  assert.deepEqual(inline('<img src=x onerror=alert(1)> **b**'), [
    text('<img src=x onerror=alert(1)> '),
    bold(text('b')),
  ]);
});

test('the notes in dashboard.json parse into the blocks they were written as', () => {
  const dashboard = JSON.parse(readFileSync(new URL('../web/dashboard.json', import.meta.url), 'utf8'));
  const blocks = parseMarkdown(dashboard.topology.notes);

  assert.deepEqual(blocks.map((block) => block.type), ['heading', 'table', 'heading', 'list', 'heading', 'list']);
  const [, apps, , layers] = blocks;
  assert.equal(apps.head.length, 6);
  assert.equal(apps.rows.length, 4);
  for (const row of apps.rows) assert.equal(row.length, 6);
  assert.deepEqual(apps.rows[0][0], [code('payments-api')]);
  assert.equal(layers.items.length, 3);
  assert.deepEqual(layers.items[0][0], bold(text('Golden signals (L, T, E, S)')));
});
