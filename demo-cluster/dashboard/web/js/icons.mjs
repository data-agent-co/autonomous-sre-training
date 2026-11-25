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

  // The control plane. A box and a row name their icon in dashboard.json.

  // A pulse line: Prometheus.
  prometheus: [['path', { d: 'M3 12.5h4l2.5-7 4.5 13 2.5-6h4.5' }]],
  // A hash, as before the name of a channel: Slack.
  slack: [['path', { d: 'M9.5 4 8 20M16 4l-1.5 16M4.5 9h15.5M4 15h15.5' }]],
  // A key: the box that posts to your Slack channel.
  key: [
    ['circle', { cx: 8, cy: 15.5, r: 4 }],
    ['path', { d: 'M11 12.5l9-9' }],
    ['path', { d: 'M16 7.5l3 3' }],
  ],
  // A check and a cross: a stage that works, and one that failed.
  check: [['path', { d: 'M5 12.5l4.5 4.5L19 7.5' }]],
  cross: [['path', { d: 'M6.5 6.5l11 11M17.5 6.5l-11 11' }]],
  // Layers: kube-state-metrics, the state of the cluster's objects.
  layers: [
    ['path', { d: 'M12 4l8 4-8 4-8-4z' }],
    ['path', { d: 'M4 12l8 4 8-4' }],
    ['path', { d: 'M4 16l8 4 8-4' }],
  ],
  // A bell: Alertmanager.
  bell: [
    ['path', { d: 'M6 16.5V11a6 6 0 0 1 12 0v5.5l1.5 2h-15z' }],
    ['path', { d: 'M10 21a2.2 2.2 0 0 0 4 0' }],
  ],
  // Two arrows that chase each other: the operator's reconcile loop.
  loop: [
    ['path', { d: 'M5 10.5a7.2 7.2 0 0 1 13-2.5' }],
    ['path', { d: 'M18.5 3.5V8H14' }],
    ['path', { d: 'M19 13.5a7.2 7.2 0 0 1-13 2.5' }],
    ['path', { d: 'M5.5 20.5V16H10' }],
  ],
  // A four-pointed spark: the LLM.
  spark: [['path', { d: 'M12 3.5l2 6.5 6.5 2-6.5 2-2 6.5-2-6.5-6.5-2 6.5-2z' }]],
  // An eye: the watcher.
  eye: [
    ['path', { d: 'M2.5 12s3.5-6.5 9.5-6.5 9.5 6.5 9.5 6.5-3.5 6.5-9.5 6.5S2.5 12 2.5 12z' }],
    ['circle', { cx: 12, cy: 12, r: 2.5 }],
  ],
  // A paper plane: slack-relay, which sends.
  send: [
    ['path', { d: 'M20.5 3.5l-17 7 6.5 3 3 6.5z' }],
    ['path', { d: 'M20.5 3.5L10 13.5' }],
  ],
  // Two links of a chain: the webhook.
  link: [
    ['path', { d: 'M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1' }],
    ['path', { d: 'M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1' }],
  ],
};

// hasIcon says whether icon(name) can draw the name. The tests use it to
// check the names in dashboard.json.
export function hasIcon(name) {
  return Object.hasOwn(ICONS, name);
}

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
