// A small Markdown reader for the notes in dashboard.json. It knows what
// those notes use: "###" headings, pipe tables, "-" lists, paragraphs and the
// inline marks `code`, **bold** and *italic*. Everything else stays plain
// text. The text never reaches the page as HTML: parseMarkdown() turns it
// into a tree and renderMarkdown() builds elements and text nodes from that.

import { el } from './dom.mjs';

const HEADING = /^###\s+(.*)$/;
const LIST_ITEM = /^-\s+(.*)$/;
// The line under a table's header row: |---|:---:|---|
const TABLE_RULE = /^\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?$/;

// closeOf finds the mark that closes a span whose text starts at `from`, or
// -1. A span holds at least one character.
function closeOf(text, mark, from, emphasis) {
  for (let at = text.indexOf(mark, from + 1); at !== -1; at = text.indexOf(mark, at + 1)) {
    if (!emphasis) return at;
    const partOfBold = mark === '*' && (text[at + 1] === '*' || text[at - 1] === '*');
    if (!partOfBold && !/\s/.test(text[at - 1])) return at;
  }
  return -1;
}

function span(text, start, mark, type) {
  const from = start + mark.length;
  const emphasis = type !== 'code';
  // Emphasis hugs its text, so the stars in "2 * 3 * 4" stay stars.
  if (emphasis && (from >= text.length || /\s/.test(text[from]))) return null;
  const close = closeOf(text, mark, from, emphasis);
  if (close === -1) return null;
  const inner = text.slice(from, close);
  const node = emphasis ? { type, inlines: parseInline(inner) } : { type, text: inner };
  return { node, end: close + mark.length };
}

// spanAt returns the code, bold or italic span that opens at `start`, or
// null when the text there is plain.
function spanAt(text, start) {
  if (text[start] === '`') return span(text, start, '`', 'code');
  if (text.startsWith('**', start)) return span(text, start, '**', 'bold');
  if (text[start] === '*') return span(text, start, '*', 'italic');
  return null;
}

function parseInline(text) {
  const nodes = [];
  let plain = '';
  const flush = () => {
    if (plain !== '') nodes.push({ type: 'text', text: plain });
    plain = '';
  };
  let at = 0;
  while (at < text.length) {
    const found = spanAt(text, at);
    if (found) {
      flush();
      nodes.push(found.node);
      at = found.end;
    } else {
      plain += text[at];
      at += 1;
    }
  }
  flush();
  return nodes;
}

// tableCells splits a table row at its pipes; "\|" is a pipe inside a cell.
function tableCells(line) {
  const cells = [];
  let cell = '';
  // Start after the row's opening pipe.
  for (let at = 1; at < line.length; at++) {
    if (line[at] === '\\' && line[at + 1] === '|') {
      cell += '|';
      at += 1;
    } else if (line[at] === '|') {
      cells.push(cell);
      cell = '';
    } else {
      cell += line[at];
    }
  }
  // Text after the last pipe is a cell too: the closing pipe is optional.
  if (cell.trim() !== '') cells.push(cell);
  return cells.map((text) => parseInline(text.trim()));
}

function blockKind(lines, at) {
  const line = lines[at];
  if (HEADING.test(line)) return 'heading';
  if (LIST_ITEM.test(line)) return 'list';
  if (line.startsWith('|') && TABLE_RULE.test(lines[at + 1] ?? '')) return 'table';
  return 'paragraph';
}

// parseMarkdown turns the text into a list of blocks:
//
//   { type: 'heading', inlines }
//   { type: 'paragraph', inlines }
//   { type: 'list', items: [inlines, ...] }
//   { type: 'table', head: [inlines, ...], rows: [[inlines, ...], ...] }
//
// where inlines is a list of { type: 'text', text }, { type: 'code', text },
// { type: 'bold', inlines } and { type: 'italic', inlines }. Every table row
// has as many cells as the header.
export function parseMarkdown(text) {
  const lines = text.split(/\r?\n/).map((line) => line.trim());
  const blocks = [];
  let at = 0;
  while (at < lines.length) {
    if (lines[at] === '') {
      at += 1;
      continue;
    }
    const kind = blockKind(lines, at);
    if (kind === 'heading') {
      blocks.push({ type: 'heading', inlines: parseInline(HEADING.exec(lines[at])[1]) });
      at += 1;
    } else if (kind === 'list') {
      const items = [];
      for (; at < lines.length && LIST_ITEM.test(lines[at]); at++) {
        items.push(parseInline(LIST_ITEM.exec(lines[at])[1]));
      }
      blocks.push({ type: 'list', items });
    } else if (kind === 'table') {
      const head = tableCells(lines[at]);
      const rows = [];
      for (at += 2; at < lines.length && lines[at].startsWith('|'); at++) {
        const cells = tableCells(lines[at]);
        rows.push(head.map((_, column) => cells[column] ?? []));
      }
      blocks.push({ type: 'table', head, rows });
    } else {
      const start = at;
      do {
        at += 1;
      } while (at < lines.length && lines[at] !== '' && blockKind(lines, at) === 'paragraph');
      blocks.push({ type: 'paragraph', inlines: parseInline(lines.slice(start, at).join(' ')) });
    }
  }
  return blocks;
}

const INLINE_TAGS = { code: 'code', bold: 'strong', italic: 'em' };

function renderInlines(inlines) {
  return inlines.map((node) => {
    if (node.type === 'text') return node.text;
    if (node.type === 'code') return el('code', { class: 'md-code' }, node.text);
    return el(INLINE_TAGS[node.type], {}, renderInlines(node.inlines));
  });
}

function renderBlock(block) {
  switch (block.type) {
    case 'heading':
      return el('h3', { class: 'md-heading' }, renderInlines(block.inlines));
    case 'list':
      return el('ul', { class: 'md-list' }, block.items.map((item) => el('li', {}, renderInlines(item))));
    case 'table':
      // The wrapper scrolls sideways, so a wide table never widens the page.
      return el(
        'div',
        { class: 'md-table' },
        el(
          'table',
          {},
          el('thead', {}, el('tr', {}, block.head.map((cell) => el('th', { scope: 'col' }, renderInlines(cell))))),
          el('tbody', {}, block.rows.map((row) => el('tr', {}, row.map((cell) => el('td', {}, renderInlines(cell)))))),
        ),
      );
    default:
      return el('p', { class: 'md-paragraph' }, renderInlines(block.inlines));
  }
}

// renderMarkdown returns the text as a DocumentFragment. The elements carry
// md-* classes, which base.css styles.
export function renderMarkdown(text) {
  const fragment = document.createDocumentFragment();
  fragment.append(...parseMarkdown(text).map(renderBlock));
  return fragment;
}
