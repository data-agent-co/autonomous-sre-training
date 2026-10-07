package emitter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	corev1alpha1 "github.com/data-agent-co/autonomous-sre-training/k8s-watcher/internal/k8sgpt/v1alpha1"
)

// The golden files in testdata were recorded from the Client this package
// had before it moved to the dynamic client: controller-runtime's typed
// client with the k8sgpt-operator v0.2.27 Result type. Each one holds, per
// pipeline, a Result already in the cluster, a list of input Results as
// DryRun printed them, and for every input in turn the error Emit returned
// and the API requests it sent. TestClient_MatchesRecordedRequests replays
// the inputs against the same fake API server and expects the same
// requests, so the Result objects written are unchanged.

type goldenFile struct {
	Server  map[string]any `json:"server"`
	Results []struct {
		Name   string `json:"name"`
		DryRun string `json:"dryRun"`
	} `json:"results"`
	Steps []goldenStep `json:"steps"`
}

type goldenStep struct {
	Emit     string          `json:"emit"`
	Err      string          `json:"err"`
	Requests []goldenRequest `json:"requests"`
}

type goldenRequest struct {
	Method      string         `json:"method"`
	Path        string         `json:"path"`
	Query       string         `json:"query"`
	ContentType string         `json:"contentType"`
	Body        map[string]any `json:"body,omitempty"`
}

const resultsPath = "/apis/core.k8sgpt.ai/v1alpha1/namespaces/k8sgpt-system/results"

// fakeResultsAPI serves the Results of one namespace from memory, sets the
// fields an API server sets on create and update, and records each request.
// A Get of a name starting with "fail-get" fails with a server error.
type fakeResultsAPI struct {
	mu   sync.Mutex
	objs map[string]map[string]any
	rv   int
	reqs []goldenRequest
}

func (a *fakeResultsAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		panic(err)
	}
	var obj, recorded map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &obj); err != nil {
			panic(err)
		}
		_ = json.Unmarshal(body, &recorded)
	}
	a.reqs = append(a.reqs, goldenRequest{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
		ContentType: r.Header.Get("Content-Type"), Body: maskEmitTimes(recorded),
	})
	name := path.Base(r.URL.Path)
	switch r.Method {
	case http.MethodGet:
		if strings.HasPrefix(name, "fail-get") {
			writeStatus(w, http.StatusInternalServerError, "InternalError", "boom")
			return
		}
		o, ok := a.objs[r.URL.Path]
		if !ok {
			writeStatus(w, http.StatusNotFound, "NotFound", `results.core.k8sgpt.ai "`+name+`" not found`)
			return
		}
		writeObject(w, http.StatusOK, o)
	case http.MethodPost:
		md := obj["metadata"].(map[string]any)
		p := r.URL.Path + "/" + md["name"].(string)
		if _, ok := a.objs[p]; ok {
			writeStatus(w, http.StatusConflict, "AlreadyExists", "exists")
			return
		}
		a.rv++
		md["uid"] = "uid-" + md["name"].(string)
		md["resourceVersion"] = strconv.Itoa(a.rv)
		md["creationTimestamp"] = "2026-10-01T00:00:00Z"
		md["generation"] = 1
		a.objs[p] = obj
		writeObject(w, http.StatusCreated, obj)
	case http.MethodPut:
		if _, ok := a.objs[r.URL.Path]; !ok {
			writeStatus(w, http.StatusNotFound, "NotFound", "missing")
			return
		}
		a.rv++
		obj["metadata"].(map[string]any)["resourceVersion"] = strconv.Itoa(a.rv)
		a.objs[r.URL.Path] = obj
		writeObject(w, http.StatusOK, obj)
	default:
		writeStatus(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "no")
	}
}

func (a *fakeResultsAPI) take() []goldenRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.reqs
	a.reqs = nil
	return out
}

func writeObject(w http.ResponseWriter, code int, obj map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(obj)
}

func writeStatus(w http.ResponseWriter, code int, reason, msg string) {
	writeObject(w, code, map[string]any{
		"kind": "Status", "apiVersion": "v1", "metadata": map[string]any{},
		"status": "Failure", "message": msg, "reason": reason, "code": code,
	})
}

// maskEmitTimes replaces the lifecycle timestamps Emit sets from the clock
// with "<now>". The pre-existing Result's timestamps (September 2026) stay.
func maskEmitTimes(obj map[string]any) map[string]any {
	md, _ := obj["metadata"].(map[string]any)
	an, _ := md["annotations"].(map[string]any)
	for _, k := range []string{annoFirstObserved, annoLastObserved} {
		if v, ok := an[k].(string); ok && !strings.HasPrefix(v, "2026-09-0") {
			an[k] = "<now>"
		}
	}
	return obj
}

func TestClient_MatchesRecordedRequests(t *testing.T) {
	for lane, equal := range map[string]SpecEqual{"ev": SameEvent, "cm": SameChange} {
		t.Run(lane, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/golden_" + lane + ".json")
			require.NoError(t, err)
			var golden goldenFile
			require.NoError(t, json.Unmarshal(raw, &golden))

			// The local Result type prints the same DryRun JSON, byte for
			// byte, as the operator's type did.
			inputs := map[string]string{}
			for _, in := range golden.Results {
				var r corev1alpha1.Result
				require.NoError(t, json.Unmarshal([]byte(in.DryRun), &r), in.Name)
				out, err := json.MarshalIndent(&r, "", "  ")
				require.NoError(t, err)
				require.Equal(t, in.DryRun, string(out), in.Name)
				inputs[in.Name] = in.DryRun
			}

			api := &fakeResultsAPI{objs: map[string]map[string]any{
				resultsPath + "/preexisting": golden.Server,
			}}
			srv := httptest.NewServer(api)
			t.Cleanup(srv.Close)
			// QPS -1 turns client-side rate limiting off, as loadKubeConfig
			// in cmd/watcher does.
			dc, err := dynamic.NewForConfig(&rest.Config{Host: srv.URL, QPS: -1})
			require.NoError(t, err)
			e := New(dc, equal, quietLog())

			require.NotEmpty(t, golden.Steps)
			for _, step := range golden.Steps {
				// A fresh copy per step: Emit stamps annotations on it.
				var in corev1alpha1.Result
				require.NoError(t, json.Unmarshal([]byte(inputs[step.Emit]), &in))
				err := e.Emit(context.Background(), &in)
				got := goldenStep{Emit: step.Emit, Requests: api.take()}
				if err != nil {
					got.Err = err.Error()
				}
				require.Equal(t, step, got)
			}
		})
	}
}
