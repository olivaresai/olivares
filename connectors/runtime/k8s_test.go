// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// k8sRecorder records request methods+paths and the Authorization header so a
// test can assert read-only access and bearer auth.
type k8sRecorder struct {
	mu      sync.Mutex
	calls   []string
	authSet bool
}

// startK8sServer serves the standard list endpoints over TLS (httptest) with the
// given per-path body+status. It returns the server URL and the recorder.
func startK8sServer(t *testing.T, bodies map[string]string, status map[string]int) (string, *k8sRecorder) {
	t.Helper()
	rec := &k8sRecorder{}
	h := http.NewServeMux()
	for path, body := range bodies {
		path, body := path, body
		code := http.StatusOK
		if status != nil {
			if c, ok := status[path]; ok {
				code = c
			}
		}
		h.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			rec.mu.Lock()
			rec.calls = append(rec.calls, r.Method+" "+r.URL.Path)
			if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				rec.authSet = true
			}
			rec.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_, _ = w.Write([]byte(body))
		})
	}
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	return srv.URL, rec
}

func (r *k8sRecorder) methods() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	copy(out, r.calls)
	return out
}

func k8sCfg(apiServer, token string, namespaces string) config {
	settings := map[string]string{
		cfgEnableLinux:           "false",
		cfgEnableDocker:          "false",
		cfgEnableK8s:             "true",
		cfgK8sAPIServer:          apiServer,
		cfgK8sToken:              token,
		cfgK8sInsecureSkipVerify: "true",
	}
	if namespaces != "" {
		settings[cfgK8sNamespaces] = namespaces
	}
	return loadConfig(sinkConfig(settings))
}

func k8sFixtures() map[string]string {
	return map[string]string{
		"/api/v1/nodes": `{"items":[
			{"metadata":{"name":"node-b"}},
			{"metadata":{"name":"node-a"}}
		]}`,
		"/api/v1/namespaces": `{"items":[{"metadata":{"name":"default"}},{"metadata":{"name":"ai"}}]}`,
		"/api/v1/pods": `{"items":[
			{"metadata":{"name":"agent-1","namespace":"ai"},"spec":{"nodeName":"node-a","containers":[{"image":"ollama/ollama:latest"},{"image":"busybox:1.36"}]}},
			{"metadata":{"name":"web-1","namespace":"default"},"spec":{"nodeName":"node-b","containers":[{"image":"nginx:1.25"}]}},
			{"metadata":{"name":"pending-1","namespace":"ai"},"spec":{"containers":[{"image":"vllm/vllm:latest"}]}}
		]}`,
		"/apis/apps/v1/deployments": `{"items":[
			{"metadata":{"name":"agent","namespace":"ai"}},
			{"metadata":{"name":"web","namespace":"default"}}
		]}`,
	}
}

func TestGatherK8s_GoldenEdges(t *testing.T) {
	url, rec := startK8sServer(t, k8sFixtures(), nil)
	cfg := k8sCfg(url, "dummy-token", "")
	sink := &fakeSink{}
	if err := gatherK8s(context.Background(), cfg, sink, time.Now().UTC()); err != nil {
		t.Fatalf("gatherK8s: %v", err)
	}

	// cluster ref is the api server host (httptest URL host:port).
	host := strings.TrimPrefix(url, "https://")
	got := sink.sortedEdgeKeys()
	want := []string{
		// nodes (sorted: node-a, node-b)
		"k8s.cluster|" + host + " -> k8s.node|node-a tool= mode=unknown src=runtime conf=attributed",
		"k8s.cluster|" + host + " -> k8s.node|node-b tool= mode=unknown src=runtime conf=attributed",
		// pods (sorted by ns/name: ai/agent-1, ai/pending-1, default/web-1)
		"k8s.node|node-a -> k8s.pod|ai/agent-1 tool=ollama/ollama:latest mode=unknown src=runtime conf=attributed",
		"k8s.pod|ai/agent-1 -> container.image|ollama/ollama:latest tool= mode=unknown src=runtime conf=attributed",
		"k8s.pod|ai/agent-1 -> container.image|busybox:1.36 tool= mode=unknown src=runtime conf=attributed",
		"k8s.node|unscheduled -> k8s.pod|ai/pending-1 tool=vllm/vllm:latest mode=unknown src=runtime conf=attributed",
		"k8s.pod|ai/pending-1 -> container.image|vllm/vllm:latest tool= mode=unknown src=runtime conf=attributed",
		"k8s.node|node-b -> k8s.pod|default/web-1 tool=nginx:1.25 mode=unknown src=runtime conf=attributed",
		"k8s.pod|default/web-1 -> container.image|nginx:1.25 tool= mode=unknown src=runtime conf=attributed",
		// deployments (sorted: ai/agent, default/web)
		"k8s.namespace|ai -> k8s.deployment|ai/agent tool= mode=unknown src=runtime conf=attributed",
		"k8s.namespace|default -> k8s.deployment|default/web tool= mode=unknown src=runtime conf=attributed",
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("edge set mismatch:\n got=%v\nwant=%v", got, want)
	}
	if fs := sink.findings(); len(fs) != 0 {
		t.Fatalf("expected 0 findings, got %d: %+v", len(fs), fs)
	}

	// Read-only + bearer auth assertions.
	if !rec.authSet {
		t.Fatalf("expected a Bearer Authorization header on k8s requests")
	}
	for _, call := range rec.methods() {
		if !strings.HasPrefix(call, "GET ") {
			t.Fatalf("non-GET request issued to k8s: %q", call)
		}
	}
}

func TestGatherK8s_NamespaceScoped(t *testing.T) {
	fx := k8sFixtures()
	// add a per-namespace pods endpoint so scoping resolves.
	fx["/api/v1/namespaces/ai/pods"] = `{"items":[
		{"metadata":{"name":"agent-1","namespace":"ai"},"spec":{"nodeName":"node-a","containers":[{"image":"ollama/ollama:latest"}]}}
	]}`
	url, rec := startK8sServer(t, fx, nil)
	cfg := k8sCfg(url, "dummy-token", "ai")
	sink := &fakeSink{}
	if err := gatherK8s(context.Background(), cfg, sink, time.Now().UTC()); err != nil {
		t.Fatalf("gatherK8s: %v", err)
	}
	// Only the ai/agent-1 pod should appear (scoped); web-1 / pending-1 excluded.
	for _, e := range sink.edges() {
		if e.ResourceKind == resK8sPod && e.ResourceRef != "ai/agent-1" {
			t.Fatalf("namespace-scoped run leaked pod %q", e.ResourceRef)
		}
	}
	// The cluster-wide /api/v1/pods endpoint must NOT have been called.
	for _, call := range rec.methods() {
		if call == "GET /api/v1/pods" {
			t.Fatalf("namespace-scoped run called cluster-wide /api/v1/pods")
		}
	}
}

func TestGatherK8s_Unauthorized_HealthFinding(t *testing.T) {
	url, _ := startK8sServer(t, k8sFixtures(), map[string]int{"/api/v1/nodes": http.StatusUnauthorized})
	cfg := k8sCfg(url, "dummy-token", "")
	sink := &fakeSink{}
	if err := gatherK8s(context.Background(), cfg, sink, time.Now().UTC()); err != nil {
		t.Fatalf("gatherK8s should turn 401 into a finding, got err %v", err)
	}
	fs := sink.findings()
	if len(fs) != 1 {
		t.Fatalf("expected exactly 1 health finding, got %d: %+v", len(fs), fs)
	}
	if fs[0].Kind != "health" || fs[0].SubjectKind != subjectK8sCluster {
		t.Fatalf("unexpected finding: %+v", fs[0])
	}
	if fs[0].DetailHash == "" {
		t.Fatalf("finding must carry a hashed detail")
	}
}

func TestGatherK8s_NoCluster_SilentSkip(t *testing.T) {
	// No api server and not in-cluster ⇒ silent skip. Force the in-cluster env
	// vars unset so the test is deterministic even when run inside a k8s pod.
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	cfg := loadConfig(sinkConfig(map[string]string{
		cfgEnableLinux:  "false",
		cfgEnableDocker: "false",
		cfgEnableK8s:    "true",
	}))
	sink := &fakeSink{}
	if err := gatherK8s(context.Background(), cfg, sink, time.Now().UTC()); err != nil {
		t.Fatalf("no cluster should be a silent skip, got %v", err)
	}
	if len(sink.edges()) != 0 || len(sink.findings()) != 0 {
		t.Fatalf("no cluster must emit nothing: edges=%d findings=%d", len(sink.edges()), len(sink.findings()))
	}
}
