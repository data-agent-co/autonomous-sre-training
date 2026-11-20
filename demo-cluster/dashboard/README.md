# demo-dashboard

The demo dashboard: one page with a live topology of the demo apps and the
same panels as the [Perses dashboard](../perses), served by a small Go server.
[`../scripts/up.sh`](../scripts/up.sh) builds the image `demo-dashboard:demo`
from this directory and runs it in the `monitoring` namespace.
[`../scripts/dashboard.sh`](../scripts/dashboard.sh) port-forwards it to
<http://localhost:8080>. What the page shows is described in
[../README.md](../README.md#demo-dashboard).

The server ([`main.go`](main.go)) serves the page, which is embedded in the
binary, answers `/healthz`, and forwards `/api/v1/query` and
`/api/v1/query_range` to Prometheus. It forwards nothing else, so the page can
read from Prometheus but not change it. There is no login: the server is meant
to be reached through a local port-forward only. It does keep other web sites
that are open in the same browser from reading Prometheus through it: it
refuses a query that comes from another site, and it answers queries only when
it is addressed as `localhost` or by IP address.

The page takes "now" from Prometheus, not from the browser: every answer
carries the time it was evaluated at. So the charts stay right when the
laptop's clock and the cluster's clock drift apart, which happens after sleep.

The page has no build step and loads nothing from the internet. Its charts
are drawn with [uPlot](https://github.com/leeoniya/uPlot), which is vendored
in [`web/vendor/`](web/vendor); the README there names the version.

## Layout

```
dashboard/
├── main.go               the server
├── main_test.go          tests of the server
├── dashboard_test.go     checks web/dashboard.json and compares it with the Perses dashboard
├── go.mod                the Go module (standard library only)
├── Dockerfile            builds the image demo-dashboard:demo
├── .dockerignore         keeps the tests and the manifest out of the image build
├── deployment.yaml       Deployment and Service demo-dashboard in monitoring
├── web/                  the page; all of it is embedded in the binary
│   ├── index.html        the page's frame: header, topology card, panel grid
│   ├── dashboard.json    the panel file: the topology and the panels
│   ├── css/              the styles
│   ├── js/               the page's code; modules are .mjs, so the tests can import them
│   └── vendor/           uPlot and its license (see the README there to update it)
└── jstest/               unit tests for the modules in web/js (node --test)
```

## Run it on your machine

The server needs a Prometheus to query. With the demo cluster up, port-forward
its Prometheus and start the server from this directory:

```bash
kubectl --context kind-demo-cluster -n monitoring \
  port-forward svc/prometheus-stack-kube-prom-prometheus 9090 &

LISTEN_ADDR=127.0.0.1:8090 WEB_DIR=web PROMETHEUS_URL=http://127.0.0.1:9090 go run .

kill %1    # after Ctrl-C: stop the port-forward
```

Open <http://127.0.0.1:8090>. With `WEB_DIR` set, the server reads the page
from that directory on every request, not from the embedded copy, so an edit
shows on reload. `LISTEN_ADDR` is set here because the default port, 8080, is
the one the cluster's own dashboard is forwarded to.

| Variable | Default | Meaning |
|---|---|---|
| `LISTEN_ADDR` | `:8080` | Address to serve on. |
| `PROMETHEUS_URL` | `http://prometheus-stack-kube-prom-prometheus.monitoring.svc:9090` | Prometheus base URL. The default is the Service inside the cluster. |
| `WEB_DIR` | empty | Serve the page from this directory instead of the embedded copy. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |

## Tests

```bash
go test ./...                    # the server, the panel file, parity with Perses
node --test jstest/*.test.mjs    # the page's logic that needs no browser
```

Neither needs a cluster. The module needs Go 1.27.1 or newer (see `go.mod`);
the JavaScript tests need Node.js (tested with v22). From the repository root,
`make test` and `make test-js` run the same tests, and `make ci` runs them
with the other checks (gofmt, go vet, golangci-lint, govulncheck).

## Change or add a panel

The panels are the `panels` list in [`web/dashboard.json`](web/dashboard.json).
Each has an `id`, a `title`, a `kind` (`stat`, `timeseries` or `table`), a
`grid` position on 24 columns and one or more `queries`. Each kind is drawn by
its module in [`web/js/panels/`](web/js/panels). The types at the top of
[`dashboard_test.go`](dashboard_test.go) list every field the page knows. The
tests reject any other field, so a misspelt name fails there and not as an
empty panel in the browser.

The Perses dashboard,
[`../perses/demo-cluster-overview.json`](../perses/demo-cluster-overview.json),
shows the same panels from its own file. Neither file is generated from the
other. `TestPanelsMatchPerses` fails when they differ, for example in a panel
that only one file has, or in a title, kind, query, unit, threshold, legend or
grid position. So:

1. Make the change in both files. A panel's `id` here is its key under
   `spec.panels` in the Perses file. `grid` takes the numbers of the Perses
   layout, with `y` counted from the first row of panels: Perses has its
   Markdown panel above that row.
2. Run `go test ./...`. A failure names the panel and the field that differs.
3. Re-run `../scripts/up.sh` to see the change in the cluster. It rebuilds
   the image, replaces the pod and loads the Perses file in the same run.

Descriptions and colours may differ between the two files. The `topology`
object has no counterpart in Perses, which shows a Markdown panel in its
place; `TestTopologyIsValid` checks its shape.
