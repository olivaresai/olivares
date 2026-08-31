// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package inferencegateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/olivaresai/olivares/connectors/internal/identity"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.inference-gateway"

// SignalInferenceGateway is this connector's OWN provenance value, an open-string
// model.SignalSource declared package-locally (the snowflake-audit precedent — the
// SDK seeds policy/cloudtrail/pg_audit but not this surface, and a connector
// introduces its own without an SDK release; docs/contracts/S02 §6). It is the
// connector's identity for display/diagnostics. NOTE: the EMITTED edge Source is
// model.SignalPolicy, not this value — these edges are DECLARED grants (the permitted
// side of the diff, like iceberg-catalog), not observed traffic. This const labels
// WHO declared the topology; SignalPolicy labels WHAT KIND of edge it is. See doc.go.
const SignalInferenceGateway model.SignalSource = "inference_gateway"

// Source is the inference-gateway source connector. It reads the operator-exported
// Gateway API Inference Extension CRD manifests and emits the DECLARED routing
// topology as PERMITTED policy edges (module III). It is a BATCH poller: Gather lists
// the files, parses the CRDs, emits, and returns nil at EOF (the engine re-runs it).
// The zero value is not usable; call New.
type Source struct {
	path string
	now  func() time.Time // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies the SDK contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns an inference-gateway source.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Kubernetes Gateway API Inference Extension",
		Description: "Parses exported InferencePool/InferenceModel/InferenceObjective CRDs and emits the DECLARED inference-routing topology as PERMITTED policy edges (permitted side of the R/RW diff), read-only. Never reaches the cluster, the gateway or a model-serving pod.",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "Inference Extension manifest file, or a directory of *.yaml / *.yml / *.json Kubernetes manifests (multi-document YAML supported)."},
		},
	}
}

// Open reads and validates configuration. A missing path is a configuration error
// reported here, not deferred to Gather (mirrors awskms/external-secrets).
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("path"))
	if s.path == "" {
		return errors.New("inference-gateway: path is required")
	}
	return nil
}

// Close releases resources; this connector holds none between Gather runs.
func (s *Source) Close(context.Context) error { return nil }

// Gather reads the configured manifests and emits one PERMITTED policy edge per
// InferencePool and per InferenceModel/InferenceObjective. It is a batch source: it
// returns nil when the manifests are exhausted. It never emits a payload (a CRD has
// none) and never contacts the cluster.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	docs, err := s.readDocs()
	if err != nil {
		return err
	}
	now := s.clock().UTC()
	for _, d := range docs {
		if err := ctx.Err(); err != nil {
			return err
		}
		edge, ok := buildEdge(d, now)
		if !ok {
			continue
		}
		if err := sink.Emit(ctx, edge); err != nil {
			return err
		}
	}
	return nil
}

// buildEdge maps one Inference Extension CRD to its PERMITTED policy edge. It returns
// ok=false for a kind it does not map or an object whose required identity does not
// resolve (skipped, never guessed — ARCHITECTURE.md).
//
//   - InferencePool -> origin is the EPP/endpoint-picker (else the pool name), the
//     resource is the pool itself ("<ns>/<pool>"). The pool fronts model-serving pods
//     that both consume the request and return the completion, so Mode is ReadWrite.
//   - InferenceModel/InferenceObjective -> origin is the served-model name (else
//     metadata.name for an Objective), the resource is the model bound to its pool
//     ("<ns>/<model> -> <poolRef>"). Mode is ReadWrite for the same reason.
//
// Source is always model.SignalPolicy (declared/permitted side) and Confidence is
// always attributed (the topology is exact declared config, not an inference).
func buildEdge(d document, now time.Time) (model.EdgeObservation, bool) {
	ns := namespaceOf(d)
	switch d.Kind {
	case kindInferencePool:
		origin, ok := poolOrigin(d)
		if !ok {
			return model.EdgeObservation{}, false
		}
		poolName := strings.TrimSpace(d.Metadata.Name)
		if poolName == "" {
			return model.EdgeObservation{}, false
		}
		return policyEdge(safeRef(origin), resourcePool, ns+"/"+poolName, now), true

	case kindInferenceModel, kindInferenceObjective:
		modelName, ok := modelIdentity(d)
		if !ok {
			return model.EdgeObservation{}, false
		}
		pool := poolRefName(d)
		if pool == "" {
			return model.EdgeObservation{}, false
		}
		// Reference the pool with the SAME namespace-qualified key the pool's own edge
		// uses (a PoolObjectReference is namespace-local), so the model→pool edge JOINS
		// cleanly onto the pool edge in the access map.
		resourceRef := ns + "/" + safeRef(modelName) + " -> " + ns + "/" + safeRef(pool)
		return policyEdge(safeRef(modelName), resourceModel, resourceRef, now), true

	default:
		return model.EdgeObservation{}, false
	}
}

// policyEdge builds the common PERMITTED edge for this connector. The edge Source is
// model.SignalPolicy (declared grant), the Confidence is attributed (exact config),
// the ToolRef is the constant inference-gateway surface label, and ObservedAt is the
// connector clock (the natural-key timestamp consumers de-dupe re-emitted edges on).
func policyEdge(origin, resourceKind, resourceRef string, now time.Time) model.EdgeObservation {
	return model.EdgeObservation{
		OriginKind:   identity.OriginKind,
		OriginRef:    origin,
		ResourceKind: resourceKind,
		ResourceRef:  resourceRef,
		Mode:         model.ModeReadWrite,
		Source:       model.SignalPolicy,
		Confidence:   model.ConfidenceAttributed,
		ToolRef:      toolRef,
		ObservedAt:   now,
	}
}

// poolRefName resolves the InferencePool name a model/objective binds to (spec.poolRef
// .name), or "" when absent.
func poolRefName(d document) string {
	if d.Spec.PoolRef == nil {
		return ""
	}
	return strings.TrimSpace(d.Spec.PoolRef.Name)
}

// safeRef defends the minimal-data posture: a CRD name/label is structural metadata,
// but a hostile or accidentally-pasted manifest could embed a secret in a name, so
// every identity/reference is scrubbed for known secret shapes before it travels into
// an edge (connectors/internal/redact). A clean name is returned unchanged.
func safeRef(s string) string { return redact.Clean(s) }

// readDocs resolves the configured path to its files and decodes every Inference
// Extension CRD document from them (multi-document YAML and JSON both decode through
// yaml.v3 — the canonical external-secrets path). A non-matching document is skipped.
// Files are read in sorted order for deterministic output.
func (s *Source) readDocs() ([]document, error) {
	files, err := s.listFiles()
	if err != nil {
		return nil, err
	}
	var docs []document
	for _, f := range files {
		fileDocs, err := decodeFile(f)
		if err != nil {
			return nil, err
		}
		docs = append(docs, fileDocs...)
	}
	return docs, nil
}

// listFiles resolves the configured path to a sorted list of files (a directory
// contributes its *.yaml / *.yml / *.json entries; a file contributes itself).
func (s *Source) listFiles() ([]string, error) {
	fi, err := os.Stat(s.path)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return []string{s.path}, nil
	}
	entries, err := os.ReadDir(s.path)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".yml") || strings.HasSuffix(n, ".json") {
			files = append(files, filepath.Join(s.path, n))
		}
	}
	sort.Strings(files)
	return files, nil
}

// decodeFile reads one manifest file and decodes each YAML document in it (a file may
// hold several objects separated by `---`). JSON is a subset of YAML, so a
// single-object .json file decodes through the same path. A document that is not an
// Inference Extension CRD this connector handles is skipped silently; an empty
// document (a stray `---` or comment-only block) decodes to the zero value and is
// skipped.
func decodeFile(path string) ([]document, error) {
	f, err := os.Open(path) //nolint:gosec // operator-supplied manifest path, read-only
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var out []document
	for {
		var d document
		if err := dec.Decode(&d); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if isInferenceDoc(d) {
			out = append(out, d)
		}
	}
	return out, nil
}

func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}
