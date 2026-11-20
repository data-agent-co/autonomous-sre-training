package main

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// upstream is a stand-in Prometheus that records what the proxy sent it.
// Its answers carry header on top of the content type.
type upstream struct {
	*httptest.Server
	requests []*http.Request
	bodies   []string
	header   http.Header
}

func newUpstream(t *testing.T, status int, body string) *upstream {
	t.Helper()
	u := &upstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		u.requests = append(u.requests, r)
		u.bodies = append(u.bodies, string(b))
		for name, values := range u.header {
			w.Header()[name] = values
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(u.Close)
	return u
}

var testFiles = fstest.MapFS{
	"index.html":           {Data: []byte("<!doctype html><title>demo</title>")},
	"dashboard.json":       {Data: []byte(`{"panels":[]}`)},
	"css/base.css":         {Data: []byte("body{}")},
	"js/app.mjs":           {Data: []byte("export {};")},
	"vendor/uPlot.LICENSE": {Data: []byte("MIT")},
	"vendor/README.md":     {Data: []byte("# Vendored files")},
}

func newTestServer(t *testing.T, prometheusURL string, static fs.FS) *httptest.Server {
	t.Helper()
	target, err := url.Parse(prometheusURL)
	if err != nil {
		t.Fatalf("parse %q: %v", prometheusURL, err)
	}
	srv := httptest.NewServer(newHandler(target, static, slog.New(slog.DiscardHandler)))
	t.Cleanup(srv.Close)
	return srv
}

// reply is what a test needs of a response, read and closed.
type reply struct {
	status int
	header http.Header
	body   string
}

// do sends req without following redirects: each test is about what one
// request reaches.
func do(t *testing.T, req *http.Request) reply {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return reply{status: resp.StatusCode, header: resp.Header, body: string(body)}
}

func request(t *testing.T, method, target, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	return req
}

func TestHealthz(t *testing.T) {
	srv := newTestServer(t, "http://prometheus.invalid:9090", testFiles)

	got := do(t, request(t, http.MethodGet, srv.URL+"/healthz", ""))

	if got.status != http.StatusOK || got.body != "ok\n" {
		t.Fatalf("GET /healthz = %d %q, want 200 \"ok\\n\"", got.status, got.body)
	}
}

func TestProxyForwardsQueryEndpoints(t *testing.T) {
	const upstreamReply = `{"status":"success","data":{"resultType":"vector","result":[]}}`
	tests := []struct {
		name      string
		method    string
		path      string
		body      string
		wantPath  string
		wantQuery string
		wantBody  string
	}{
		{
			name: "GET query", method: http.MethodGet, path: "/api/v1/query?query=up&time=100",
			wantPath: "/api/v1/query", wantQuery: "query=up&time=100",
		},
		{
			name: "POST query", method: http.MethodPost, path: "/api/v1/query", body: "query=up",
			wantPath: "/api/v1/query", wantBody: "query=up",
		},
		{
			name: "POST query_range", method: http.MethodPost, path: "/api/v1/query_range",
			body:     "query=rate%28x%5B1m%5D%29&start=0&end=60&step=5",
			wantPath: "/api/v1/query_range", wantBody: "query=rate%28x%5B1m%5D%29&start=0&end=60&step=5",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prom := newUpstream(t, http.StatusOK, upstreamReply)
			srv := newTestServer(t, prom.URL, testFiles)

			req := request(t, tt.method, srv.URL+tt.path, tt.body)
			if tt.body != "" {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
			got := do(t, req)

			if got.status != http.StatusOK || got.body != upstreamReply {
				t.Fatalf("got %d %q, want 200 and the upstream reply", got.status, got.body)
			}
			if len(prom.requests) != 1 {
				t.Fatalf("upstream saw %d requests, want 1", len(prom.requests))
			}
			sent := prom.requests[0]
			if sent.Method != tt.method || sent.URL.Path != tt.wantPath || sent.URL.RawQuery != tt.wantQuery {
				t.Errorf("upstream got %s %s?%s, want %s %s?%s",
					sent.Method, sent.URL.Path, sent.URL.RawQuery, tt.method, tt.wantPath, tt.wantQuery)
			}
			if prom.bodies[0] != tt.wantBody {
				t.Errorf("upstream body = %q, want %q", prom.bodies[0], tt.wantBody)
			}
		})
	}
}

func TestProxyKeepsPrometheusBasePath(t *testing.T) {
	prom := newUpstream(t, http.StatusOK, `{"status":"success"}`)
	srv := newTestServer(t, prom.URL+"/prometheus", testFiles)

	do(t, request(t, http.MethodGet, srv.URL+"/api/v1/query?query=up", ""))

	if len(prom.requests) != 1 || prom.requests[0].URL.Path != "/prometheus/api/v1/query" {
		t.Fatalf("upstream requests = %v, want one for /prometheus/api/v1/query", paths(prom.requests))
	}
}

func TestProxyPassesPrometheusErrorsThrough(t *testing.T) {
	const upstreamReply = `{"status":"error","errorType":"bad_data","error":"parse error"}`
	prom := newUpstream(t, http.StatusBadRequest, upstreamReply)
	srv := newTestServer(t, prom.URL, testFiles)

	got := do(t, request(t, http.MethodGet, srv.URL+"/api/v1/query?query=up%7B", ""))

	if got.status != http.StatusBadRequest || got.body != upstreamReply {
		t.Fatalf("got %d %q, want the upstream 400 and its body", got.status, got.body)
	}
}

func TestProxyDropsCredentials(t *testing.T) {
	prom := newUpstream(t, http.StatusOK, `{"status":"success"}`)
	srv := newTestServer(t, prom.URL, testFiles)

	req := request(t, http.MethodGet, srv.URL+"/api/v1/query?query=up", "")
	req.Header.Set("Cookie", "session=secret")
	req.Header.Set("Authorization", "Bearer secret")
	do(t, req)

	if len(prom.requests) != 1 {
		t.Fatalf("upstream saw %d requests, want 1", len(prom.requests))
	}
	for _, name := range []string{"Cookie", "Authorization"} {
		if v := prom.requests[0].Header.Get(name); v != "" {
			t.Errorf("upstream got %s: %q, want it removed", name, v)
		}
	}
}

// Prometheus answers a request that carries an Origin with CORS headers that
// let any site read the reply. Neither the Origin nor such headers may pass.
func TestProxyStripsCORS(t *testing.T) {
	prom := newUpstream(t, http.StatusOK, `{"status":"success"}`)
	prom.header = http.Header{
		"Access-Control-Allow-Origin":   {"*"},
		"Access-Control-Allow-Methods":  {"GET, POST, OPTIONS"},
		"Access-Control-Expose-Headers": {"Date"},
	}
	srv := newTestServer(t, prom.URL, testFiles)

	req := request(t, http.MethodPost, srv.URL+"/api/v1/query", "query=up")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", srv.URL) // the page's own origin, as a browser sends it
	got := do(t, req)

	if got.status != http.StatusOK || len(prom.requests) != 1 {
		t.Fatalf("got %d, upstream saw %d requests; want 200 and 1", got.status, len(prom.requests))
	}
	if v := prom.requests[0].Header.Get("Origin"); v != "" {
		t.Errorf("upstream got Origin: %q, want it removed", v)
	}
	for name := range got.header {
		if strings.HasPrefix(name, "Access-Control-") {
			t.Errorf("answer carries %s: %q, want no CORS header", name, got.header.Get(name))
		}
	}
}

// The server has no login. A page of another site that is open in the same
// browser must still not read Prometheus through it: not by calling the
// server across origins, and not by pointing its own host name at this
// machine (DNS rebinding), which only the Host header gives away.
func TestProxyRefusesOtherSites(t *testing.T) {
	const self = "<self>" // stands for the test server's own origin
	tests := []struct {
		name    string
		header  map[string]string
		host    string // Host to send; empty for the test server's own
		refused bool
	}{
		{name: "not a browser"},
		{name: "the page itself", header: map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": self}},
		{name: "typed into the address bar", header: map[string]string{"Sec-Fetch-Site": "none"}},
		{name: "another site", header: map[string]string{"Sec-Fetch-Site": "cross-site"}, refused: true},
		{name: "another port of this machine", header: map[string]string{"Sec-Fetch-Site": "same-site"}, refused: true},
		{name: "another origin, older browser", header: map[string]string{"Origin": "https://evil.example"}, refused: true},
		{name: "sandboxed page", header: map[string]string{"Origin": "null"}, refused: true},
		{name: "localhost", host: "localhost:8080"},
		{name: "localhost in capitals with a dot", host: "LOCALHOST.:8080"},
		{name: "a name under localhost", host: "demo.localhost:8080"},
		{name: "IPv6 loopback", host: "[::1]:8080"},
		{name: "IPv4 address without a port", host: "127.0.0.1"},
		{name: "rebound host name", host: "rebind.attacker.example:8080", refused: true},
		{name: "host name that only starts like localhost", host: "localhost.attacker.example", refused: true},
		{name: "service name inside the cluster", host: "demo-dashboard.monitoring.svc:8080", refused: true},
		// To the browser a rebound page is the same origin as the server: its
		// requests say so, and only the Host gives them away.
		{
			name: "rebound host name, as a browser sends it", host: "rebind.attacker.example:8080", refused: true,
			header: map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://rebind.attacker.example:8080"},
		},
		// The port is part of an origin.
		{
			name: "another port of this machine, older browser", host: "localhost:8080", refused: true,
			header: map[string]string{"Origin": "http://localhost:3000"},
		},
	}
	for _, tt := range tests {
		// A page reads with GET as easily as with POST.
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			t.Run(tt.name+", "+method, func(t *testing.T) {
				prom := newUpstream(t, http.StatusOK, `{"status":"success"}`)
				srv := newTestServer(t, prom.URL, testFiles)

				target, body := srv.URL+"/api/v1/query?query=up", ""
				if method == http.MethodPost {
					target, body = srv.URL+"/api/v1/query", "query=up"
				}
				req := request(t, method, target, body)
				if body != "" {
					req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				}
				for name, value := range tt.header {
					if value == self {
						value = srv.URL
					}
					req.Header.Set(name, value)
				}
				if tt.host != "" {
					req.Host = tt.host
				}
				got := do(t, req)

				if !tt.refused {
					if got.status != http.StatusOK || len(prom.requests) != 1 {
						t.Fatalf("got %d %q, upstream saw %d requests; want 200 and 1", got.status, got.body, len(prom.requests))
					}
					return
				}
				if got.status != http.StatusForbidden || len(prom.requests) != 0 {
					t.Fatalf("got %d, upstream saw %d requests; want 403 and none", got.status, len(prom.requests))
				}
				if !strings.Contains(got.body, `"status":"error"`) || !strings.Contains(got.body, `"errorType":"forbidden"`) {
					t.Errorf("body = %q, want a Prometheus-style error of type forbidden", got.body)
				}
			})
		}
	}
}

// Only the two query endpoints reach Prometheus. Everything else under /api/
// must stop at the dashboard, most of all the admin API, which deletes data.
func TestOnlyQueryEndpointsAreForwarded(t *testing.T) {
	tests := []struct {
		method     string
		path       string
		wantStatus int
	}{
		{http.MethodPost, "/api/v1/admin/tsdb/delete_series?match[]=up", http.StatusNotFound},
		{http.MethodPost, "/api/v1/admin/tsdb/snapshot", http.StatusNotFound},
		{http.MethodGet, "/api/v1/label/__name__/values", http.StatusNotFound},
		{http.MethodGet, "/api/v1/series?match[]=up", http.StatusNotFound},
		{http.MethodGet, "/api/v1/targets", http.StatusNotFound},
		{http.MethodGet, "/api/v1/query/", http.StatusNotFound},
		{http.MethodGet, "/api/v1/query_range/../admin/tsdb/snapshot", http.StatusNotFound},
		{http.MethodGet, "/api/v1/status/config", http.StatusNotFound},
		{http.MethodPost, "/api/v1/write", http.StatusNotFound},
		{http.MethodPost, "/api/", http.StatusNotFound},
		{http.MethodDelete, "/api/v1/query", http.StatusMethodNotAllowed},
		{http.MethodPut, "/api/v1/query_range", http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			prom := newUpstream(t, http.StatusOK, `{"status":"success"}`)
			srv := newTestServer(t, prom.URL, testFiles)

			got := do(t, request(t, tt.method, srv.URL+tt.path, ""))

			if len(prom.requests) != 0 {
				t.Fatalf("upstream got %v, want nothing forwarded", paths(prom.requests))
			}
			// The mux answers an unclean path (/a/../b) with a redirect to the
			// clean one. That is fine as long as it does not point at a
			// forwarded path.
			redirected := got.status >= 300 && got.status < 400
			if got.status != tt.wantStatus && !redirected {
				t.Errorf("status = %d, want %d", got.status, tt.wantStatus)
			}
			if loc := got.header.Get("Location"); redirected && isProxied(loc) {
				t.Errorf("redirected to %q, a forwarded path", loc)
			}
		})
	}
}

func isProxied(location string) bool {
	u, err := url.Parse(location)
	if err != nil {
		return false
	}
	for _, p := range proxiedPaths {
		if u.Path == p {
			return true
		}
	}
	return false
}

func paths(requests []*http.Request) []string {
	out := make([]string, 0, len(requests))
	for _, r := range requests {
		out = append(out, r.Method+" "+r.URL.Path)
	}
	return out
}

func TestProxyRefusesOversizedBody(t *testing.T) {
	prom := newUpstream(t, http.StatusOK, `{"status":"success"}`)
	srv := newTestServer(t, prom.URL, testFiles)

	req := request(t, http.MethodPost, srv.URL+"/api/v1/query", "query="+strings.Repeat("a", maxQueryBodyBytes))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	got := do(t, req)

	if got.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d (%s), want 413", got.status, got.body)
	}
	if !strings.Contains(got.body, `"status":"error"`) {
		t.Errorf("body = %q, want a Prometheus-style error", got.body)
	}
}

func TestProxyReportsUnreachablePrometheus(t *testing.T) {
	prom := newUpstream(t, http.StatusOK, "")
	prom.Close() // nothing listens on its address any more
	srv := newTestServer(t, prom.URL, testFiles)

	got := do(t, request(t, http.MethodGet, srv.URL+"/api/v1/query?query=up", ""))

	if got.status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", got.status)
	}
	want := `{"status":"error","errorType":"unavailable","error":"Prometheus is unreachable from the dashboard server"}` + "\n"
	if got.body != want {
		t.Errorf("body = %q, want %q", got.body, want)
	}
}

func TestStaticFiles(t *testing.T) {
	tests := []struct {
		path            string
		wantStatus      int
		wantContentType string
		wantBody        string
	}{
		{"/", http.StatusOK, "text/html; charset=utf-8", "<!doctype html><title>demo</title>"},
		{"/dashboard.json", http.StatusOK, "application/json", `{"panels":[]}`},
		{"/css/base.css", http.StatusOK, "text/css; charset=utf-8", "body{}"},
		{"/js/app.mjs", http.StatusOK, "text/javascript; charset=utf-8", "export {};"},
		{"/vendor/uPlot.LICENSE", http.StatusOK, "text/plain; charset=utf-8", "MIT"},
		{"/vendor/README.md", http.StatusOK, "text/markdown; charset=utf-8", "# Vendored files"},
		{"/missing.html", http.StatusNotFound, "", ""},
		// Directories are not listed.
		{"/js/", http.StatusNotFound, "", ""},
		{"/js", http.StatusNotFound, "", ""},
		{"/vendor/", http.StatusNotFound, "", ""},
	}
	srv := newTestServer(t, "http://prometheus.invalid:9090", testFiles)
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := do(t, request(t, http.MethodGet, srv.URL+tt.path, ""))

			if got.status != tt.wantStatus {
				t.Fatalf("status = %d, want %d", got.status, tt.wantStatus)
			}
			if tt.wantStatus != http.StatusOK {
				return
			}
			if ct := got.header.Get("Content-Type"); ct != tt.wantContentType {
				t.Errorf("Content-Type = %q, want %q", ct, tt.wantContentType)
			}
			if got.body != tt.wantBody {
				t.Errorf("body = %q, want %q", got.body, tt.wantBody)
			}
		})
	}
}

func TestStaticFilesCarrySecurityHeaders(t *testing.T) {
	srv := newTestServer(t, "http://prometheus.invalid:9090", testFiles)

	got := do(t, request(t, http.MethodGet, srv.URL+"/", ""))

	want := map[string]string{
		"Content-Security-Policy": contentSecurityPolicy,
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"Cache-Control":           "no-store",
	}
	for name, value := range want {
		if v := got.header.Get(name); v != value {
			t.Errorf("%s = %q, want %q", name, v, value)
		}
	}
}

func TestStaticFilesRefuseWrites(t *testing.T) {
	srv := newTestServer(t, "http://prometheus.invalid:9090", testFiles)

	got := do(t, request(t, http.MethodPost, srv.URL+"/dashboard.json", "{}"))

	if got.status != http.StatusMethodNotAllowed {
		t.Fatalf("POST /dashboard.json = %d, want 405", got.status)
	}
}

// The page files the walk in TestEmbeddedPageIsComplete starts from:
// index.html, and two files no other file refers to by path. app.mjs fetches
// the panel file, and the license ships with the vendored library.
var pageRoots = []string{"index.html", "dashboard.json", "vendor/uPlot.LICENSE"}

// What a page file refers to: href and src in HTML; static, bare and dynamic
// imports of a relative path in a module, in any of the three quotes; url()
// and @import in a style sheet.
var (
	htmlRef   = regexp.MustCompile(`(?:href|src)\s*=\s*["']?([^"'\s>]+)`)
	moduleRef = regexp.MustCompile("(?:\\bfrom\\s*|\\bimport\\s*\\(?\\s*)['\"`](\\.{1,2}/[^'\"`\\s]+)['\"`]")
	cssRef    = regexp.MustCompile(`(?:url\(\s*|@import\s+)["']?([^"')\s;]+)`)
)

// The page must come whole out of the binary: every file index.html names,
// every module those import, the panel file and the license. Each also needs
// a content type, because a browser refuses a module or a style sheet that
// is served as something else.
func TestEmbeddedPageIsComplete(t *testing.T) {
	static, err := staticFiles("")
	if err != nil {
		t.Fatalf("staticFiles: %v", err)
	}
	seen := map[string]bool{}
	queue := append([]string(nil), pageRoots...)
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		seen[name] = true
		data, err := fs.ReadFile(static, name)
		if err != nil {
			t.Errorf("embedded %s: %v", name, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("embedded %s is empty", name)
		}
		if _, ok := contentTypes[path.Ext(name)]; !ok {
			t.Errorf("%s: contentTypes has no entry for %q", name, path.Ext(name))
		}
		var refs *regexp.Regexp
		switch path.Ext(name) {
		case ".html":
			refs = htmlRef
		case ".mjs":
			refs = moduleRef
		case ".css":
			refs = cssRef
		default:
			continue
		}
		for _, match := range refs.FindAllStringSubmatch(string(data), -1) {
			if ref := match[1]; !strings.HasPrefix(ref, "data:") {
				queue = append(queue, path.Join(path.Dir(name), ref))
			}
		}
	}
	// If the patterns above stopped matching, the walk would end early and
	// find nothing missing. These files are reached only through references:
	// a link, a script tag, a static import and a dynamic one.
	for _, name := range []string{"css/topology.css", "js/theme.js", "js/app.mjs", "js/panels/timeseries.mjs", "js/topology-model.mjs"} {
		if !seen[name] {
			t.Errorf("the walk from index.html did not reach %s", name)
		}
	}
}

func TestStaticFilesFromWebDir(t *testing.T) {
	dir := t.TempDir()
	static, err := staticFiles(dir)
	if err != nil {
		t.Fatalf("staticFiles(%q): %v", dir, err)
	}
	if _, err := fs.Stat(static, "."); err != nil {
		t.Errorf("stat root of WEB_DIR: %v", err)
	}

	if _, err := staticFiles(dir + "/missing"); err == nil {
		t.Error("staticFiles on a missing directory: got no error")
	}
}

func TestLoadConfig(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(name string) string { return vars[name] }
	}

	t.Run("defaults", func(t *testing.T) {
		c, err := loadConfig(env(nil))
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if c.ListenAddr != ":8080" || c.PrometheusURL.String() != defaultPrometheusURL || c.WebDir != "" || c.LogLevel != slog.LevelInfo {
			t.Errorf("defaults = %+v", c)
		}
	})

	t.Run("overrides", func(t *testing.T) {
		c, err := loadConfig(env(map[string]string{
			"LISTEN_ADDR":    "127.0.0.1:18080",
			"PROMETHEUS_URL": "http://127.0.0.1:9090",
			"WEB_DIR":        "web",
			"LOG_LEVEL":      "debug",
		}))
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if c.ListenAddr != "127.0.0.1:18080" || c.PrometheusURL.Host != "127.0.0.1:9090" || c.WebDir != "web" || c.LogLevel != slog.LevelDebug {
			t.Errorf("config = %+v", c)
		}
	})

	for _, bad := range []map[string]string{
		{"PROMETHEUS_URL": "prometheus:9090"},
		{"PROMETHEUS_URL": "ftp://prometheus"},
		{"PROMETHEUS_URL": "http://"},
		{"PROMETHEUS_URL": "http://bad host"},
		{"LOG_LEVEL": "loud"},
	} {
		t.Run("rejects", func(t *testing.T) {
			if _, err := loadConfig(env(bad)); err == nil {
				t.Errorf("loadConfig(%v): got no error", bad)
			}
		})
	}
}

// freeAddr returns a loopback address nothing listens on right now.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return addr
}

// healthz asks the server at addr once. The error is the transport's: nothing
// answered.
func healthz(ctx context.Context, addr string) (reply, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", http.NoBody)
	if err != nil {
		return reply{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return reply{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return reply{}, err
	}
	return reply{status: resp.StatusCode, header: resp.Header, body: string(body)}, nil
}

func TestRunServesUntilCancelled(t *testing.T) {
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	env := map[string]string{"LISTEN_ADDR": addr, "PROMETHEUS_URL": "http://prometheus.invalid:9090"}

	done := make(chan error, 1)
	go func() { done <- run(ctx, func(name string) string { return env[name] }) }()

	// The server needs a moment to bind; ask until it answers.
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := healthz(t.Context(), addr)
		if err == nil {
			if got.status != http.StatusOK || got.body != "ok\n" {
				t.Fatalf("GET /healthz = %d %q, want 200 \"ok\\n\"", got.status, got.body)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the server did not answer within 5s: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v after a clean shutdown, want nil", err)
		}
	case <-time.After(shutdownTimeout + 5*time.Second):
		t.Fatal("run did not return after its context was cancelled")
	}
	if _, err := healthz(t.Context(), addr); err == nil {
		t.Error("the server still answers after run returned")
	}
}

func TestRunFailsWhenTheAddressIsTaken(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = l.Close() }()
	env := map[string]string{"LISTEN_ADDR": l.Addr().String()}

	err = run(t.Context(), func(name string) string { return env[name] })

	if err == nil {
		t.Fatal("run on a taken address returned nil, want the listen error")
	}
}

func TestRunRejectsBadConfiguration(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"bad Prometheus URL": {"PROMETHEUS_URL": "prometheus:9090"},
		"missing WEB_DIR":    {"WEB_DIR": t.TempDir() + "/missing"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(t.Context(), func(key string) string { return env[key] }); err == nil {
				t.Error("run returned nil, want an error before it starts serving")
			}
		})
	}
}
