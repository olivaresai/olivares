// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package istiotelemetry

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

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.istio-telemetry"

// SignalIstioTelemetry is the package-local SignalSource for this connector
//. FindingReport carries no Source field, so this provenance value is
// woven into the finding Kind/Title and kept here for documentation and consistency
// with the other network connectors (each declares its own open-string source). It
// is NOT added to sdk/model/enums.go (the SDK enum stays untouched).
const SignalIstioTelemetry model.SignalSource = "istio_telemetry"

// findingKind is the FindingReport.Kind for every posture finding: the mesh-wide
// observability posture a Telemetry resource declares.
const findingKind = "mesh_telemetry_posture"

// subjectKind is the FindingReport.SubjectKind: an Istio Telemetry resource.
const subjectKind = "istio.telemetry"

// Source is the Istio Telemetry posture connector. It satisfies
// sdk.SourceConnector. It OBSERVES the mesh-wide observability POSTURE by parsing
// exported Istio Telemetry CRD manifests — it never calls the Istio control plane,
// never opens a listener, and reads no traffic. The zero value is not usable; call
// New.
type Source struct {
	path string
	now  func() time.Time // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies the SourceConnector contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns an istio-telemetry source.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Istio Telemetry posture",
		Description: "Parses exported Istio Telemetry (telemetry.istio.io) CRD manifests and reports the mesh observability posture: where access logging / tracing is configured, and where a Telemetry resource DISABLES it (a deliberate observability blind spot). Read-only; reads no traffic and no payloads.",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "Istio Telemetry manifest file, or a directory of *.yaml / *.yml / *.json Kubernetes manifests (multi-document YAML supported)."},
		},
	}
}

// Open reads and validates configuration.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("path"))
	if s.path == "" {
		return errors.New("istio-telemetry: path is required")
	}
	return nil
}

// Close releases resources; this connector holds none between Gather runs.
func (s *Source) Close(context.Context) error { return nil }

// Gather reads the configured manifests and emits one FindingReport per declared
// observability posture of each Telemetry resource: Info for a signal that is
// configured & enabled (the coverage map), Medium for a signal a resource DISABLES
// (an observability blind spot — a scope deliberately made unobserved). It is a
// batch source: it returns nil when the manifests are exhausted, and the engine
// re-runs it. It emits NO edges (this is declared posture, not observed traffic — an
// edge would fabricate a flow that may never have happened).
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
		for _, f := range s.buildFindings(d, now) {
			if err := sink.Emit(ctx, f); err != nil {
				return err
			}
		}
	}
	return nil
}

// buildFindings maps one Telemetry document to its posture findings. A resource with
// a name (required for a stable subject ref) and at least one classified posture
// yields one finding per posture; a resource that declares no observability signal
// (or has no name) yields nothing — never an invented finding.
func (s *Source) buildFindings(d document, now time.Time) []model.FindingReport {
	name := strings.TrimSpace(d.Metadata.Name)
	if name == "" {
		return nil
	}
	subject := subjectRef(d)
	scope := scopeOf(d)
	postures := classifyDoc(d)
	out := make([]model.FindingReport, 0, len(postures))
	for _, p := range postures {
		out = append(out, finding(subject, scope, p, now))
	}
	return out
}

// finding builds one FindingReport from one classified posture. The Title is a
// short, non-sensitive sentence; the DetailHash is a SHA-256 of a STABLE, non-
// sensitive key ("istio_telemetry:<ns/name>:<signal>:<enabled|disabled>") so the same
// posture re-hashes identically across runs without ever carrying a payload. No raw
// manifest content (selector label values, provider names, filter expressions) enters
// any field — only the structural posture.
func finding(subject, scope string, p posture, now time.Time) model.FindingReport {
	what := signalLabel(p.signal)
	var sev model.Severity
	var title string
	state := "enabled"
	if p.disabled {
		state = "disabled"
		sev = model.SeverityMedium
		title = "istio telemetry: " + what + " DISABLED for " + scope + " (" + subject + ") — observability blind spot"
	} else {
		sev = model.SeverityInfo
		title = "istio telemetry: " + what + " enabled for " + scope + " (" + subject + ")"
	}
	// detail key is the stable, non-sensitive identity of this posture. It is scrubbed
	// defensively (it is composed only of the namespace/name and fixed tokens, so it
	// holds no secret, but the no-leak guarantee is enforced, not assumed).
	detail := redact.Clean(string(SignalIstioTelemetry) + ":" + subject + ":" + p.signal + ":" + state)
	return model.FindingReport{
		Kind:        findingKind,
		Severity:    sev,
		SubjectKind: subjectKind,
		SubjectRef:  subject,
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  now,
	}
}

// signalLabel renders a signal constant as a human phrase for the finding Title.
func signalLabel(signal string) string {
	switch signal {
	case signalAccessLogging:
		return "access logging"
	case signalTracing:
		return "tracing"
	case signalMetrics:
		return "metrics"
	default:
		return signal
	}
}

// scopeOf renders the workload scope a Telemetry resource applies to, for the Title.
// A selector with matchLabels narrows the policy to those workloads; an empty
// selector applies to the whole namespace (or mesh-wide when the resource lives in
// the Istio root namespace, which the operator knows from the namespace in the
// subject ref). The label VALUES are part of the operator's own manifest and are
// non-sensitive workload identifiers (app/version names), but they are scrubbed
// defensively before display so a mislabelled value can never carry a secret out.
func scopeOf(d document) string {
	labels := d.Spec.Selector.MatchLabels
	if len(labels) == 0 {
		return "namespace " + namespaceOf(d)
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, redact.Clean(k)+"="+redact.Clean(labels[k]))
	}
	return "workloads {" + strings.Join(parts, ",") + "}"
}

// readDocs resolves the configured path to its files and decodes every Telemetry CRD
// document from them (multi-document YAML and JSON both decode through yaml.v3). A
// non-Telemetry document is skipped. Files are read in sorted order for deterministic
// output.
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
// single-object .json file decodes through the same path. A document that is not a
// Telemetry CRD is skipped silently. An empty document (a stray `---` or a
// comment-only block) decodes to the zero value and is skipped.
func decodeFile(path string) ([]document, error) {
	f, err := os.Open(path)
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
		if isTelemetryDoc(d) {
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
