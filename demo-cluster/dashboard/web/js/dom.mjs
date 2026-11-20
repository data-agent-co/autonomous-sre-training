// Small DOM builders shared by the panels and the topology.
//
// Text always goes in through textContent or a text node, never innerHTML:
// series names and label values come from Prometheus and are not trusted.
// Styles go through the CSSOM (style.setProperty), which the page's
// Content-Security-Policy allows without 'unsafe-inline'.

const SVG_NS = 'http://www.w3.org/2000/svg';

function applyProps(node, props) {
  for (const [key, value] of Object.entries(props)) {
    if (value === null || value === undefined || value === false) continue;
    switch (key) {
      case 'class':
        node.setAttribute('class', value);
        break;
      case 'text':
        node.textContent = String(value);
        break;
      case 'dataset':
        Object.assign(node.dataset, value);
        break;
      case 'style':
        for (const [prop, v] of Object.entries(value)) {
          if (v !== null && v !== undefined) node.style.setProperty(prop, String(v));
        }
        break;
      case 'on':
        for (const [event, handler] of Object.entries(value)) node.addEventListener(event, handler);
        break;
      default:
        node.setAttribute(key, value === true ? '' : String(value));
    }
  }
}

function appendChildren(node, children) {
  for (const child of children.flat(Infinity)) {
    if (child === null || child === undefined || child === false) continue;
    node.append(child instanceof Node ? child : document.createTextNode(String(child)));
  }
}

// el('div', { class: 'card', title: 'Help', dataset: { id: 'a' },
//             style: { '--tone': 'var(--red)' }, on: { click: fn } }, child, 'text')
//
// class, text, dataset, style and on are handled as shown; every other key is
// set as an attribute (true gives an empty attribute; null, undefined and
// false are skipped). Children may be nodes, strings, numbers or nested
// arrays; null, undefined and false are skipped.
export function el(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  applyProps(node, props);
  appendChildren(node, children);
  return node;
}

// Same as el(), for SVG elements: svgEl('path', { class: 'edge', d: 'M0,0 H10' }).
export function svgEl(tag, props = {}, ...children) {
  const node = document.createElementNS(SVG_NS, tag);
  applyProps(node, props);
  appendChildren(node, children);
  return node;
}

// Remove every child of node.
export function clear(node) {
  node.replaceChildren();
}
