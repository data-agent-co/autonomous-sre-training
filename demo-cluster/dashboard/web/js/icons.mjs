// Inline SVG icons. The page loads no icon font: every icon is drawn here on
// a 24x24 grid with round stroke caps and takes its colour from the text
// around it (stroke="currentColor").

import { svgEl } from './dom.mjs';

// Each icon is a list of [tag, attributes] shapes. To add an icon, add an
// entry here and call icon('<name>').
const ICONS = {
  // A circled "i": opens a panel's description.
  info: [
    ['circle', { cx: 12, cy: 12, r: 9 }],
    ['path', { d: 'M12 11v6' }],
    ['path', { d: 'M12 7.5v.01' }],
  ],
  // A warning triangle: the page banner and a panel's error bar.
  alert: [
    ['path', { d: 'M12 4 21 19.5H3z' }],
    ['path', { d: 'M12 10v4.5' }],
    ['path', { d: 'M12 17v.01' }],
  ],

  // The topology. A node names its icon in dashboard.json; a port's icon has
  // the name of its kind.

  // Waves leaving a point: the traffic source.
  traffic: [
    ['circle', { cx: 6, cy: 12, r: 1.5 }],
    ['path', { d: 'M11 7.5a6.4 6.4 0 0 1 0 9' }],
    ['path', { d: 'M15.5 4a11.3 11.3 0 0 1 0 16' }],
  ],
  // A bank card: payments-api.
  payments: [
    ['rect', { x: 3, y: 6, width: 18, height: 12, rx: 2 }],
    ['path', { d: 'M3 10.5h18' }],
    ['path', { d: 'M7 14.5h3' }],
  ],
  // A star: recommend-svc.
  recommend: [['path', { d: 'M12 4l2.1 5.6 6 .3-4.7 3.7 1.6 5.8-5-3.3-5 3.3 1.6-5.8-4.7-3.7 6-.3z' }]],
  // A grid of tiles: catalog-svc.
  catalog: [
    ['rect', { x: 4, y: 4, width: 6.5, height: 6.5, rx: 1.5 }],
    ['rect', { x: 13.5, y: 4, width: 6.5, height: 6.5, rx: 1.5 }],
    ['rect', { x: 4, y: 13.5, width: 6.5, height: 6.5, rx: 1.5 }],
    ['rect', { x: 13.5, y: 13.5, width: 6.5, height: 6.5, rx: 1.5 }],
  ],
  // Bars of a chart: the port Prometheus scrapes.
  metrics: [['path', { d: 'M5 19v-6M12 19V5M19 19v-9' }]],
  // A magnifying glass: the port K8sGPT scans.
  scan: [
    ['circle', { cx: 10.5, cy: 10.5, r: 6 }],
    ['path', { d: 'M15 15l5 5' }],
  ],
};

// icon('info') returns a new <svg> element, 16px square unless size says
// otherwise. It is decorative (aria-hidden): the control around it carries
// the label. An unknown name is a programming error and throws.
export function icon(name, size = 16) {
  const shapes = ICONS[name];
  if (!shapes) throw new Error(`unknown icon "${name}"`);
  return svgEl(
    'svg',
    {
      class: 'icon',
      viewBox: '0 0 24 24',
      width: size,
      height: size,
      fill: 'none',
      stroke: 'currentColor',
      'stroke-width': 2,
      'stroke-linecap': 'round',
      'stroke-linejoin': 'round',
      'aria-hidden': 'true',
    },
    shapes.map(([tag, attrs]) => svgEl(tag, attrs)),
  );
}
