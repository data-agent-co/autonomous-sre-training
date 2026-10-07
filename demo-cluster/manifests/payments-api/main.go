package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	mu        sync.Mutex
	allocated [][]byte

	httpRequests = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "payments_api_http_requests_total",
			Help: "Total HTTP requests, labeled by method, path, status.",
		},
		[]string{"method", "path", "status"},
	)
	httpDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "payments_api_http_request_duration_seconds",
			Help:    "HTTP request duration in seconds.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		},
		[]string{"method", "path"},
	)
)

func allocateMB(n int) {
	chunk := make([]byte, n*1024*1024)
	for i := 0; i < len(chunk); i += 4096 {
		chunk[i] = byte(i)
	}
	mu.Lock()
	allocated = append(allocated, chunk)
	mu.Unlock()
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(status int) {
	sr.status = status
	sr.ResponseWriter.WriteHeader(status)
}

// instrument wraps a handler to record Prometheus metrics for each request.
func instrument(path string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sr := &statusRecorder{ResponseWriter: w, status: 200}
		h(sr, r)
		duration := time.Since(start).Seconds()
		httpRequests.WithLabelValues(r.Method, path, strconv.Itoa(sr.status)).Inc()
		httpDuration.WithLabelValues(r.Method, path).Observe(duration)
	}
}

func main() {
	if v := os.Getenv("STARTUP_ALLOC_MB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			log.Printf("STARTUP_ALLOC_MB=%d — allocating before serving", n)
			allocateMB(n)
		}
	}

	http.HandleFunc("/healthz", instrument("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	http.HandleFunc("/allocate", instrument("/allocate", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("mb"))
		if n <= 0 {
			n = 32
		}
		allocateMB(n)
		mu.Lock()
		total := len(allocated)
		mu.Unlock()
		fmt.Fprintf(w, "allocated %dMB; total chunks: %d\n", n, total)
	}))
	http.HandleFunc("/reset", instrument("/reset", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		allocated = nil
		mu.Unlock()
		_, _ = w.Write([]byte("reset"))
	}))
	// Prometheus metrics on the same :8080 port.
	http.Handle("/metrics", promhttp.Handler())

	log.Println("payments-api listening on :8080  (/metrics on same port)")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
