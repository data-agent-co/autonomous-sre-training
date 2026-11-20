// dashboard serves the demo dashboard: one page with a live topology of the
// demo apps and the panels defined in web/dashboard.json.
//
// The page and its assets are embedded in the binary. The page reads its data
// through this server, which forwards two Prometheus endpoints and nothing
// else: /api/v1/query and /api/v1/query_range. Both only read, so the page
// cannot change or delete anything in Prometheus.
//
// There is no login: the demo cluster reaches the server through a local
// port-forward only. Do not expose it on a network you do not trust. The
// server does keep other web sites that are open in the same browser from
// reading Prometheus through it (see refusal), which is also why it answers
// queries only when it is addressed as localhost or by IP address.
//
// Configuration is by environment variable:
//
//	LISTEN_ADDR     address to serve on (default :8080)
//	PROMETHEUS_URL  Prometheus base URL (default: the kube-prometheus-stack
//	                service in the monitoring namespace)
//	WEB_DIR         serve the page from this directory instead of the embedded
//	                copy, so edits show on reload (local development)
//	LOG_LEVEL       debug, info (default), warn, error
package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"
)

//go:embed web
var embedded embed.FS

const (
	defaultListenAddr    = ":8080"
	defaultPrometheusURL = "http://prometheus-stack-kube-prom-prometheus.monitoring.svc:9090"

	// The longest expression in web/dashboard.json is under 2 KiB.
	maxQueryBodyBytes = 64 << 10

	// The page gives up on a query after 10 seconds (web/js/prom.mjs).
	prometheusTimeout = 30 * time.Second
	shutdownTimeout   = 10 * time.Second
)

// proxiedPaths are the only Prometheus endpoints the server forwards.
var proxiedPaths = []string{"/api/v1/query", "/api/v1/query_range"}

// contentSecurityPolicy lets the page load scripts, styles and data from this
// server only. The page has no inline script or style.
const contentSecurityPolicy = "default-src 'self'; img-src 'self' data:; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// contentTypes maps the extensions of the files in web/ to their media type.
// The image has no /etc/mime.types, so the server does not rely on one.
var contentTypes = map[string]string{
	".html":    "text/html; charset=utf-8",
	".css":     "text/css; charset=utf-8",
	".mjs":     "text/javascript; charset=utf-8",
	".js":      "text/javascript; charset=utf-8",
	".json":    "application/json",
	".svg":     "image/svg+xml",
	".md":      "text/markdown; charset=utf-8",
	".LICENSE": "text/plain; charset=utf-8",
}

type config struct {
	ListenAddr    string
	PrometheusURL *url.URL
	WebDir        string
	LogLevel      slog.Level
}

func loadConfig(getenv func(string) string) (config, error) {
	c := config{ListenAddr: defaultListenAddr, WebDir: getenv("WEB_DIR")}
	if v := getenv("LISTEN_ADDR"); v != "" {
		c.ListenAddr = v
	}

	var errs []error
	rawURL := defaultPrometheusURL
	if v := getenv("PROMETHEUS_URL"); v != "" {
		rawURL = v
	}
	u, err := url.Parse(rawURL)
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("PROMETHEUS_URL: %w", err))
	case (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
		errs = append(errs, fmt.Errorf("PROMETHEUS_URL=%q is not an http or https URL", rawURL))
	default:
		c.PrometheusURL = u
	}
	if v := getenv("LOG_LEVEL"); v != "" {
		if err := c.LogLevel.UnmarshalText([]byte(v)); err != nil {
			errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
		}
	}
	return c, errors.Join(errs...)
}

// staticFiles returns the page's files: the embedded web/ directory, or
// webDir when it is set.
func staticFiles(webDir string) (fs.FS, error) {
	if webDir == "" {
		return fs.Sub(embedded, "web")
	}
	info, err := os.Stat(webDir)
	if err != nil {
		return nil, fmt.Errorf("WEB_DIR: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("WEB_DIR=%q is not a directory", webDir)
	}
	return os.DirFS(webDir), nil
}

// newHandler routes /healthz, the two proxied Prometheus endpoints and the
// static page. Every other path under /api/ answers 404.
func newHandler(prometheus *url.URL, static fs.FS, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	proxy := newPrometheusProxy(prometheus, log)
	for _, p := range proxiedPaths {
		mux.Handle(p, proxy)
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeAPIError(w, http.StatusNotFound, "not_found", "the dashboard forwards only "+strings.Join(proxiedPaths, " and "))
	})
	mux.Handle("/", staticHandler(static))
	return mux
}

// refusal says why a request for Prometheus data is turned away. It is empty
// when the request may pass.
//
// The server has no login, so the browser is all that stands between a web
// page the user has open and the cluster's metrics. Two kinds of page must
// not get through:
//
//   - A page of another site that calls this server. The browser marks such
//     a request with Sec-Fetch-Site or, in an older browser, with an Origin
//     that is not this server.
//   - A page of another site whose host name was pointed at this machine
//     after the page loaded (DNS rebinding). The browser takes its requests
//     for same-origin ones, but they carry that site's name in Host. So only
//     localhost and IP addresses are accepted there: neither can be rebound.
//
// A client that is not a browser sends neither Sec-Fetch-Site nor Origin. It
// passes when it addresses the server as localhost or by IP address; inside
// the cluster that is the pod or Service IP, not the Service name.
func refusal(r *http.Request) string {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return "requests from another site are not allowed"
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		if u, err := url.Parse(origin); err != nil || !strings.EqualFold(u.Host, r.Host) {
			return "requests from another origin are not allowed"
		}
	}
	if !isLocalHost(r.Host) {
		return fmt.Sprintf("open the dashboard at localhost or at an IP address, not at %q", r.Host)
	}
	return ""
}

// isLocalHost reports whether hostport, the Host of a request, names
// localhost, a name under .localhost, or an IP address.
func isLocalHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport // no port
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "localhost" || strings.HasSuffix(host, ".localhost") || net.ParseIP(host) != nil
}

// newPrometheusProxy forwards GET and POST requests to Prometheus under the
// same path, unless refusal turns them away. The request's Cookie,
// Authorization and Origin headers stay behind, and so do the CORS headers
// of the answer.
func newPrometheusProxy(target *url.URL, log *slog.Logger) http.Handler {
	transport := &http.Transport{
		// In-cluster traffic: never through a proxy from the environment.
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		ResponseHeaderTimeout: prometheusTimeout,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Header.Del("Cookie")
			r.Out.Header.Del("Authorization")
			// Prometheus answers an Origin with CORS headers that let any
			// site read the reply.
			r.Out.Header.Del("Origin")
		},
		// In case Prometheus sends CORS headers without being asked.
		ModifyResponse: func(resp *http.Response) error {
			for name := range resp.Header {
				if strings.HasPrefix(name, "Access-Control-") {
					resp.Header.Del(name)
				}
			}
			return nil
		},
		Transport: transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			var tooLarge *http.MaxBytesError
			switch {
			case errors.As(err, &tooLarge):
				writeAPIError(w, http.StatusRequestEntityTooLarge, "bad_data",
					fmt.Sprintf("the query is larger than %d bytes", maxQueryBodyBytes))
			case errors.Is(err, context.Canceled):
				// The browser dropped the request; there is nobody to answer.
				log.Debug("query cancelled by the client", "path", r.URL.Path)
				w.WriteHeader(http.StatusBadGateway)
			default:
				log.Warn("Prometheus request failed", "path", r.URL.Path, "err", err)
				writeAPIError(w, http.StatusBadGateway, "unavailable", "Prometheus is unreachable from the dashboard server")
			}
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, POST")
			writeAPIError(w, http.StatusMethodNotAllowed, "bad_data", "use GET or POST")
			return
		}
		if why := refusal(r); why != "" {
			log.Warn("query refused", "reason", why, "host", r.Host, "origin", r.Header.Get("Origin"))
			writeAPIError(w, http.StatusForbidden, "forbidden", why)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxQueryBodyBytes)
		proxy.ServeHTTP(w, r)
	})
}

// writeAPIError answers in the shape of a Prometheus API error, which is what
// the page's client expects from these paths.
func writeAPIError(w http.ResponseWriter, status int, errorType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"status":"error","errorType":%q,"error":%q}`+"\n", errorType, message)
}

// staticHandler serves the files of root. "/" is index.html; a directory or a
// missing file is a 404, so nothing is listed.
func staticHandler(root fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		info, err := fs.Stat(root, name)
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		if ct, ok := contentTypes[path.Ext(name)]; ok {
			h.Set("Content-Type", ct)
		}
		// The files carry no modification time (they are embedded), so the
		// browser cannot revalidate them; always fetch, they are small.
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		http.ServeFileFS(w, r, root, name)
	})
}

// run serves until ctx is done, then shuts the server down, giving requests
// in flight shutdownTimeout to finish.
func run(ctx context.Context, getenv func(string) string) error {
	c, err := loadConfig(getenv)
	if err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: c.LogLevel}))
	static, err := staticFiles(c.WebDir)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              c.ListenAddr,
		Handler:           newHandler(c.PrometheusURL, static, log),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      prometheusTimeout + 15*time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	failed := make(chan error, 1)
	go func() {
		log.Info("dashboard listening", "addr", c.ListenAddr, "prometheus", c.PrometheusURL.Redacted(), "webDir", c.WebDir)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			failed <- err
		}
	}()

	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelShutdown()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	log.Info("dashboard stopped")
	return nil
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err := run(ctx, os.Getenv)
	cancel()
	if err != nil {
		slog.Error("dashboard failed", "err", err)
		os.Exit(1)
	}
}
