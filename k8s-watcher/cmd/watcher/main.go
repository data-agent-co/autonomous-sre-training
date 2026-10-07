// watcher runs the Detection Pack's events-watcher (EV) and
// configmap-watcher (CM) pipelines in one process, fed by this module's
// informers (internal/informer) instead of the Pack's own.
//
// Each lane has its own HTTP server (/healthz, /readyz, /metrics) and
// metrics registry, on its own port, so metric names and the `component`
// label are the ones the Pack's components use. /readyz answers 503 until
// the lane's informers have synced. One lane's fatal error stops the
// process; an informer that cannot complete its initial list is one.
//
// Configuration is by environment variable:
//
//	EV_ENABLED, CM_ENABLED        lane toggles (default true; at least one on)
//	RESULT_NAMESPACE              where Results are written (default k8sgpt-system)
//	EV_HEALTH_ADDR, CM_HEALTH_ADDR  lane HTTP servers (default :8080, :8081)
//	DRY_RUN                       log Results instead of writing them
//	EV_DEDUP_WINDOW, EV_SWEEP_INTERVAL  EV dedup tuning (default 5m, 1m)
//	PROBE_ENRICHMENT_ENABLED      EV probe_failure enrichment (default true)
//	PROMETHEUS_URL                Prometheus for probe enrichment
//	LOG_LEVEL                     debug, info (default), warn, error
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/adapt"
	cmapp "github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/cm/app"
	evapp "github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/pack/ev/app"
)

var version = "dev"

type config struct {
	EVEnabled       bool
	CMEnabled       bool
	ResultNamespace string
	EVHealthAddr    string
	CMHealthAddr    string
	DryRun          bool
	DedupWindow     time.Duration
	SweepInterval   time.Duration
	ProbeEnrichment bool
	PrometheusURL   string
	LogLevel        slog.Level
}

func loadConfig(getenv func(string) string) (config, error) {
	c := config{
		EVEnabled:       true,
		CMEnabled:       true,
		ResultNamespace: "k8sgpt-system",
		EVHealthAddr:    ":8080",
		CMHealthAddr:    ":8081",
		DedupWindow:     5 * time.Minute,
		SweepInterval:   time.Minute,
		ProbeEnrichment: true,
		PrometheusURL:   getenv("PROMETHEUS_URL"),
	}
	var errs []error
	boolVar := func(name string, dst *bool) {
		if v := getenv(name); v != "" {
			b, ok := parseBool(v)
			if !ok {
				errs = append(errs, fmt.Errorf("%s=%q is not a boolean", name, v))
				return
			}
			*dst = b
		}
	}
	strVar := func(name string, dst *string) {
		if v := getenv(name); v != "" {
			*dst = v
		}
	}
	durVar := func(name string, dst *time.Duration) {
		if v := getenv(name); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				return
			}
			*dst = d
		}
	}
	boolVar("EV_ENABLED", &c.EVEnabled)
	boolVar("CM_ENABLED", &c.CMEnabled)
	strVar("RESULT_NAMESPACE", &c.ResultNamespace)
	strVar("EV_HEALTH_ADDR", &c.EVHealthAddr)
	strVar("CM_HEALTH_ADDR", &c.CMHealthAddr)
	boolVar("DRY_RUN", &c.DryRun)
	durVar("EV_DEDUP_WINDOW", &c.DedupWindow)
	durVar("EV_SWEEP_INTERVAL", &c.SweepInterval)
	boolVar("PROBE_ENRICHMENT_ENABLED", &c.ProbeEnrichment)
	if v := getenv("LOG_LEVEL"); v != "" {
		if err := c.LogLevel.UnmarshalText([]byte(v)); err != nil {
			errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
		}
	}
	if !c.EVEnabled && !c.CMEnabled {
		errs = append(errs, errors.New("EV_ENABLED and CM_ENABLED are both false; nothing to run"))
	}
	return c, errors.Join(errs...)
}

func parseBool(v string) (value, ok bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	}
	return false, false
}

// lane is one pipeline and the source feeding it.
type lane struct {
	name string
	run  func(context.Context) error
}

// lanes returns the enabled pipelines, each wired to its informer-backed
// source (see the adapt package).
func lanes(c config, client kubernetes.Interface, results dynamic.Interface, log *slog.Logger) []lane {
	var out []lane
	if c.EVEnabled {
		evLog := log.With("component", "events-watcher")
		out = append(out, lane{name: "events-watcher", run: func(ctx context.Context) error {
			return evapp.Run(ctx, evapp.Options{
				ResultNamespace:        c.ResultNamespace,
				HealthAddr:             c.EVHealthAddr,
				DedupWindow:            c.DedupWindow,
				SweepInterval:          c.SweepInterval,
				DryRun:                 c.DryRun,
				ProbeEnrichmentEnabled: c.ProbeEnrichment,
				PrometheusURL:          c.PrometheusURL,
				Source:                 &adapt.EventSource{Client: client, Log: evLog, WatchPods: c.ProbeEnrichment},
				ResultClient:           results,
				Version:                version,
				Log:                    evLog,
			})
		}})
	}
	if c.CMEnabled {
		cmLog := log.With("component", "configmap-watcher")
		out = append(out, lane{name: "configmap-watcher", run: func(ctx context.Context) error {
			return cmapp.Run(ctx, cmapp.Options{
				ResultNamespace: c.ResultNamespace,
				HealthAddr:      c.CMHealthAddr,
				DryRun:          c.DryRun,
				Source:          &adapt.ConfigMapSource{Client: client, Log: cmLog},
				ResultClient:    results,
				Version:         version,
				Log:             cmLog,
			})
		}})
	}
	return out
}

func main() {
	c, err := loadConfig(os.Getenv)
	if err != nil {
		slog.Error("invalid configuration", "err", err)
		os.Exit(2)
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: c.LogLevel}))

	restConfig, err := loadKubeConfig()
	if err != nil {
		log.Error("load kubeconfig", "err", err)
		os.Exit(1)
	}
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		log.Error("build kube client", "err", err)
		os.Exit(1)
	}
	results, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		log.Error("build result client", "err", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	enabled := lanes(c, client, results, log)
	names := make([]string, 0, len(enabled))
	for _, l := range enabled {
		names = append(names, l.name)
	}
	log.Info("watcher starting", "version", version, "lanes", names, "resultNamespace", c.ResultNamespace, "dryRun", c.DryRun)

	if err := runLanes(ctx, cancel, enabled, log); err != nil {
		os.Exit(1)
	}
	log.Info("watcher stopped")
}

// loadKubeConfig finds the API server the way controller-runtime's
// config.GetConfig does (without its --kubeconfig flag, which the watcher
// never parsed): KUBECONFIG if set, else the in-cluster config, else
// $HOME/.kube/config. Client-side rate limiting is off unless the config
// sets a QPS, leaving flow control to the API server's priority and
// fairness.
func loadKubeConfig() (*rest.Config, error) {
	cfg, err := kubeConfig()
	if err != nil {
		return nil, err
	}
	if cfg.QPS == 0 {
		cfg.QPS = -1
	}
	return cfg, nil
}

func kubeConfig() (*rest.Config, error) {
	if os.Getenv(clientcmd.RecommendedConfigPathEnvVar) == "" {
		if cfg, err := rest.InClusterConfig(); err == nil {
			return cfg, nil
		}
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if _, ok := os.LookupEnv("HOME"); !ok {
		u, err := user.Current()
		if err != nil {
			return nil, fmt.Errorf("could not get current user: %w", err)
		}
		rules.Precedence = append(rules.Precedence, filepath.Join(u.HomeDir, clientcmd.RecommendedHomeDir, clientcmd.RecommendedFileName))
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
}

// runLanes runs every lane until ctx is done. The first lane to fail cancels
// the rest, and its error is returned once all have stopped.
func runLanes(ctx context.Context, cancel context.CancelFunc, enabled []lane, log *slog.Logger) error {
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	for _, l := range enabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.run(ctx); err != nil {
				log.Error("lane exited with error", "lane", l.name, "err", err)
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: %w", l.name, err)
				}
				mu.Unlock()
				cancel()
			}
		}()
	}
	wg.Wait()
	return firstErr
}
