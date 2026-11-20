# Vendored files

Third-party files the dashboard page loads. They are copied here unchanged so
the image needs no CDN and no Node build step.

| File | From | SHA-256 |
|---|---|---|
| `uPlot.iife.min.js` | [uPlot](https://github.com/leeoniya/uPlot) 1.6.32, `dist/uPlot.iife.min.js` in the npm package `uplot@1.6.32` | `19c8d4c6ad88929a79f4ae49d6f7161566dfd0ba3d15cc495e974f787eb78f1f` |
| `uPlot.min.css` | same package, `dist/uPlot.min.css` | `df630c6a8d6f8eeaff264b50f73ce5b114f646ffd9a0bb74f049b0a00135fa04` |
| `uPlot.LICENSE` | same package, `LICENSE` (MIT) | `8f989229699b4fe2f1a0432d0e9edc338a8a911e250e2d1b01ecd770a5f5b1bd` |

To update uPlot, replace the three files from the new package and update the
version and checksums above, the `NOTICE` entry at the repository root and the
"Pinned versions" table in [`demo-cluster/README.md`](../../../README.md):

```bash
npm pack uplot@<version>
tar -xzf uplot-<version>.tgz
cp package/dist/uPlot.iife.min.js package/dist/uPlot.min.css .
cp package/LICENSE uPlot.LICENSE
shasum -a 256 uPlot.iife.min.js uPlot.min.css uPlot.LICENSE
```
