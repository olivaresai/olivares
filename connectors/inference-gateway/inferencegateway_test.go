// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package inferencegateway_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	inferencegateway "github.com/olivaresai/olivares/connectors/inference-gateway"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// capturingSink records the EdgeObservations a Gather emits (the awskms pattern).
type capturingSink struct{ edges []model.EdgeObservation }

func (c *capturingSink) Emit(_ context.Context, obs model.Observation) error {
	if e, ok := obs.(model.EdgeObservation); ok {
		c.edges = append(c.edges, e)
	}
	return nil
}

func gather(t *testing.T, path string) []model.EdgeObservation {
	t.Helper()
	s := inferencegateway.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"path": path}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return sink.edges
}

// byResource indexes the emitted edges by (kind, ref) for assertion.
func byResource(edges []model.EdgeObservation) map[string]model.EdgeObservation {
	m := make(map[string]model.EdgeObservation, len(edges))
	for _, e := range edges {
		m[e.ResourceKind+"|"+e.ResourceRef] = e
	}
	return m
}

// TestPolicyEdges checks the v1alpha2 fixture: an InferencePool + an InferenceModel
// yield exactly two PERMITTED edges with the right refs/mode/source/origin, and the
// unrelated Deployment + the malformed InferenceModel are skipped.
func TestPolicyEdges(t *testing.T) {
	edges := gather(t, "testdata/manifests.yaml")
	if len(edges) != 2 {
		t.Fatalf("got %d edges, want 2 (1 pool + 1 model): %+v", len(edges), edges)
	}

	idx := byResource(edges)

	// InferencePool edge: origin is the EPP extensionRef name (the routing actor),
	// resource is "<ns>/<pool>", RW, declared (SignalPolicy), attributed.
	pool, ok := idx["inference.pool|llm/vllm-llama3-pool"]
	if !ok {
		t.Fatalf("missing pool edge; have %v", keys(idx))
	}
	if pool.OriginKind != "identity" || pool.OriginRef != "vllm-llama3-epp" {
		t.Errorf("pool origin = %q/%q, want identity/vllm-llama3-epp", pool.OriginKind, pool.OriginRef)
	}
	if pool.Mode != model.ModeReadWrite {
		t.Errorf("pool mode = %q, want readwrite", pool.Mode)
	}
	if pool.Source != model.SignalPolicy {
		t.Errorf("pool source = %q, want policy (declared/permitted side)", pool.Source)
	}
	if pool.Confidence != model.ConfidenceAttributed {
		t.Errorf("pool confidence = %q, want attributed", pool.Confidence)
	}
	if pool.ToolRef != "k8s.inference_gateway" {
		t.Errorf("pool toolRef = %q, want k8s.inference_gateway", pool.ToolRef)
	}

	// InferenceModel edge: origin is the modelName, resource is "<ns>/<model> -> <ns>/<pool>".
	mdl, ok := idx["inference.model|llm/meta-llama/Llama-3-8b -> llm/vllm-llama3-pool"]
	if !ok {
		t.Fatalf("missing model edge; have %v", keys(idx))
	}
	if mdl.OriginRef != "meta-llama/Llama-3-8b" {
		t.Errorf("model origin = %q, want meta-llama/Llama-3-8b", mdl.OriginRef)
	}
	if mdl.Mode != model.ModeReadWrite || mdl.Source != model.SignalPolicy {
		t.Errorf("model mode/source = %q/%q, want readwrite/policy", mdl.Mode, mdl.Source)
	}
}

// TestV1GAShape checks the v1 GA fixture parses through the GA field names
// (targetPorts + endpointPickerRef on the pool; InferenceObjective with priority +
// poolRef and no modelName, identified by metadata.name).
func TestV1GAShape(t *testing.T) {
	edges := gather(t, "testdata/v1-ga.yaml")
	if len(edges) != 2 {
		t.Fatalf("got %d edges, want 2 (1 pool + 1 objective): %+v", len(edges), edges)
	}
	idx := byResource(edges)

	pool, ok := idx["inference.pool|serving/qwen3-pool"]
	if !ok {
		t.Fatalf("missing GA pool edge; have %v", keys(idx))
	}
	if pool.OriginRef != "qwen3-epp" { // endpointPickerRef.name
		t.Errorf("GA pool origin = %q, want qwen3-epp (endpointPickerRef)", pool.OriginRef)
	}

	// InferenceObjective has no modelName -> identified by metadata.name.
	obj, ok := idx["inference.model|serving/qwen3-objective -> serving/qwen3-pool"]
	if !ok {
		t.Fatalf("missing GA objective edge; have %v", keys(idx))
	}
	if obj.OriginRef != "qwen3-objective" {
		t.Errorf("GA objective origin = %q, want qwen3-objective (metadata.name)", obj.OriginRef)
	}
	if obj.Mode != model.ModeReadWrite || obj.Source != model.SignalPolicy {
		t.Errorf("GA objective mode/source = %q/%q, want readwrite/policy", obj.Mode, obj.Source)
	}
}

// TestPoolWithoutEPPFallsBackToPoolName verifies that an InferencePool with no EPP
// reference uses its own name as the origin (never an empty origin).
func TestPoolWithoutEPPFallsBackToPoolName(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pool.yaml", `
apiVersion: inference.networking.k8s.io/v1
kind: InferencePool
metadata:
  name: bare-pool
  namespace: ns1
spec:
  targetPorts:
    - number: 8000
  selector:
    matchLabels:
      app: bare
`)
	edges := gather(t, dir)
	if len(edges) != 1 {
		t.Fatalf("got %d edges, want 1: %+v", len(edges), edges)
	}
	if edges[0].OriginRef != "bare-pool" {
		t.Errorf("origin = %q, want bare-pool (fallback to pool name when no EPP)", edges[0].OriginRef)
	}
}

// TestNoLeak is the minimal-data negative test (docs/SECURITY-HARDENING.md): the fixture plants a
// recognizable AWS access key as a pod-selector label value; it must NEVER appear in
// any emitted edge field (the connector emits identities/refs, never selector values
// or any payload).
func TestNoLeak(t *testing.T) {
	edges := gather(t, "testdata/manifests.yaml")
	if len(edges) == 0 {
		t.Fatal("expected edges")
	}
	blob, err := json.Marshal(edges)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, planted := range []string{"AKIAIOSFODNN7EXAMPLE", "planted-credential-do-not-leak"} {
		if strings.Contains(string(blob), planted) {
			t.Fatalf("planted value %q leaked into emitted edges: %s", planted, blob)
		}
	}
}

// TestMissingPathIsConfigError verifies Open rejects a missing required setting.
func TestMissingPathIsConfigError(t *testing.T) {
	s := inferencegateway.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err == nil {
		t.Fatal("Open with no path should error")
	}
}

// TestDescriptor sanity-checks the self-description and name.
func TestDescriptor(t *testing.T) {
	d := inferencegateway.New().Descriptor()
	if d.Name != "olivares.inference-gateway" {
		t.Errorf("name = %q", d.Name)
	}
	if d.Type != sdk.TypeSource {
		t.Errorf("type = %q, want source", d.Type)
	}
	var hasPath bool
	for _, f := range d.ConfigFields {
		if f.Key == "path" && f.Required {
			hasPath = true
		}
	}
	if !hasPath {
		t.Error("descriptor missing required path config field")
	}
}

func keys(m map[string]model.EdgeObservation) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
