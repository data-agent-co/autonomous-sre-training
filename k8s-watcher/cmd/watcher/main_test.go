package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func laneNames(ls []lane) []string {
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		out = append(out, l.name)
	}
	return out
}

func TestLaneToggles(t *testing.T) {
	tests := []struct {
		name string
		vars map[string]string
		want []string
	}{
		{"defaults run both", nil, []string{"events-watcher", "configmap-watcher"}},
		{"EV off", map[string]string{"EV_ENABLED": "false"}, []string{"configmap-watcher"}},
		{"CM off", map[string]string{"CM_ENABLED": "false"}, []string{"events-watcher"}},
		{"demo values: CM only", map[string]string{"EV_ENABLED": "false", "CM_ENABLED": "true"}, []string{"configmap-watcher"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := loadConfig(env(tt.vars))
			require.NoError(t, err)
			assert.Equal(t, tt.want, laneNames(lanes(c, fake.NewClientset(), nil, slog.Default())))
		})
	}
}

func TestLoadConfigRejects(t *testing.T) {
	for name, vars := range map[string]map[string]string{
		"both lanes off": {"EV_ENABLED": "false", "CM_ENABLED": "0"},
		"bad boolean":    {"CM_ENABLED": "maybe"},
		"bad duration":   {"EV_DEDUP_WINDOW": "five"},
		"bad log level":  {"LOG_LEVEL": "loud"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadConfig(env(vars))
			assert.Error(t, err)
		})
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	c, err := loadConfig(env(map[string]string{
		"RESULT_NAMESPACE":         "results",
		"EV_HEALTH_ADDR":           ":9080",
		"CM_HEALTH_ADDR":           ":9081",
		"DRY_RUN":                  "true",
		"EV_DEDUP_WINDOW":          "2m",
		"PROBE_ENRICHMENT_ENABLED": "false",
		"LOG_LEVEL":                "debug",
	}))
	require.NoError(t, err)
	assert.Equal(t, "results", c.ResultNamespace)
	assert.Equal(t, ":9080", c.EVHealthAddr)
	assert.Equal(t, ":9081", c.CMHealthAddr)
	assert.True(t, c.DryRun)
	assert.Equal(t, 2*time.Minute, c.DedupWindow)
	assert.False(t, c.ProbeEnrichment)
	assert.Equal(t, slog.LevelDebug, c.LogLevel)
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestRunLanes_OnlyEnabledLaneStarts runs the real lanes in dry-run against
// a fake clientset with EV off, and checks from the logs that only the CM
// pipeline started.
func TestRunLanes_OnlyEnabledLaneStarts(t *testing.T) {
	c, err := loadConfig(env(map[string]string{
		"EV_ENABLED": "false", "DRY_RUN": "true",
		"CM_HEALTH_ADDR": "127.0.0.1:0",
	}))
	require.NoError(t, err)
	logs := &lockedBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runLanes(ctx, cancel, lanes(c, fake.NewClientset(), nil, log), log) }()

	require.Eventually(t, func() bool { return strings.Contains(logs.String(), "configmap informer synced") }, 5*time.Second, 20*time.Millisecond)
	cancel()
	require.NoError(t, <-done)
	assert.Contains(t, logs.String(), "configmap-watcher starting")
	assert.NotContains(t, logs.String(), "events-watcher starting")
}

func TestRunLanes_FailingLaneStopsTheOthers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan struct{})
	err := runLanes(ctx, cancel, []lane{
		{name: "healthy", run: func(ctx context.Context) error { <-ctx.Done(); close(stopped); return nil }},
		{name: "broken", run: func(context.Context) error { return assert.AnError }},
	}, slog.Default())
	require.ErrorIs(t, err, assert.AnError)
	assert.Contains(t, err.Error(), "broken")
	<-stopped
}

// TestLoadKubeConfig_FromKubeconfigEnv checks the KUBECONFIG path and that
// client-side rate limiting is off, as with controller-runtime's GetConfig.
func TestLoadKubeConfig_FromKubeconfigEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(path, []byte(`apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: https://127.0.0.1:6443
contexts:
- name: test
  context:
    cluster: test
    user: test
current-context: test
users:
- name: test
  user:
    token: test-token
`), 0o600))
	t.Setenv("KUBECONFIG", path)

	cfg, err := loadKubeConfig()
	require.NoError(t, err)
	assert.Equal(t, "https://127.0.0.1:6443", cfg.Host)
	assert.Equal(t, float32(-1), cfg.QPS)
}
