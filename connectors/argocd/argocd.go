// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package argocd

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
const Name = "olivares.argocd"

// SignalArgoCD is the package-local SignalSource for this connector. FindingReport
// carries no Source field, so this provenance value is woven into the finding
// Kind/Title and kept here for documentation and consistency with the other
// posture connectors. It is NOT added to sdk/model/enums.go (the SDK enum stays
// untouched; an open-string source needs no SDK release — S02 §6).
const SignalArgoCD model.SignalSource = "argocd"

// FindingReport kinds for the three Argo CD posture facets.
const (
	kindSync      = "gitops_argocd_sync"
	kindHealth    = "gitops_argocd_health"
	kindOperation = "gitops_argocd_operation"
)

// subjectKind is the FindingReport.SubjectKind: an Argo CD Application.
const subjectKind = "argocd.application"

// Source is the Argo CD GitOps posture connector. It satisfies
// sdk.SourceConnector. It OBSERVES the GitOps estate by parsing exported Argo CD
// Application CRD manifests — it never calls the Argo CD or Kubernetes API, never
// triggers a sync, and reads no payloads. The zero value is not usable; call New.
type Source struct {
	path string
	now  func() time.Time // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies the SourceConnector contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns an argocd source.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Argo CD GitOps posture",
		Description: "Parses exported Argo CD Application (argoproj.io/v1alpha1) manifests and reports the GitOps reconciliation posture: sync (Synced/OutOfSync drift), health (Healthy…Degraded) and the last sync operation outcome. Read-only; never triggers a sync and reads no payloads.",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "Argo CD Application manifest file, or a directory of *.yaml / *.yml / *.json manifests (export with `kubectl get applications -A -o yaml`). Multi-document YAML supported."},
		},
	}
}

// Open reads and validates configuration.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("path"))
	if s.path == "" {
		return errors.New("argocd: path is required")
	}
	return nil
}

// Close releases resources; this connector holds none between Gather runs.
func (s *Source) Close(context.Context) error { return nil }

// Gather reads the configured manifests and emits the GitOps posture findings for
// each Application. It is a batch source: it returns nil when the manifests are
// exhausted, and the engine re-runs it on its re-poll schedule. It emits NO edges
// (sync/health/drift is observed reconciliation posture, not an access flow — an
// edge would fabricate one; matches the istio-telemetry posture connector).
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
		for _, f := range buildFindings(d, now) {
			if err := sink.Emit(ctx, f); err != nil {
				return err
			}
		}
	}
	return nil
}

// buildFindings maps one Application document to its posture findings: one sync
// finding, one health finding, and (only when the last operation errored) one
// operation finding. An Application with no name yields nothing (a stable subject
// ref is required); an empty status field is classified honestly as "Unknown",
// never guessed healthy.
func buildFindings(d document, now time.Time) []model.FindingReport {
	name := strings.TrimSpace(d.Metadata.Name)
	if name == "" {
		return nil
	}
	subject := subjectRef(d)
	out := make([]model.FindingReport, 0, 3)
	out = append(out, syncFinding(subject, d.Status.Sync.Status, now))
	out = append(out, healthFinding(subject, d.Status.Health.Status, now))
	if op := opFinding(subject, d.Status.OperationState.Phase, now); op != nil {
		out = append(out, *op)
	}
	return out
}

// syncFinding classifies the sync posture. OutOfSync is drift (Medium); Synced is
// Info; anything else (including an absent status) is Unknown (Medium) — never
// silently treated as Synced.
func syncFinding(subject, status string, now time.Time) model.FindingReport {
	status = nonEmpty(strings.TrimSpace(status), "Unknown")
	sev := model.SeverityMedium
	title := "argocd: application " + subject + " sync=" + status
	switch status {
	case syncSynced:
		sev = model.SeverityInfo
	case syncOutOfSync:
		sev = model.SeverityMedium
		title = "argocd: application " + subject + " is OutOfSync (drift between desired Git state and live cluster)"
	}
	return finding(kindSync, sev, subject, title, "sync:"+status, now)
}

// healthFinding classifies the health posture using Argo CD's own status set.
func healthFinding(subject, status string, now time.Time) model.FindingReport {
	status = nonEmpty(strings.TrimSpace(status), "Unknown")
	var sev model.Severity
	switch status {
	case healthHealthy, healthProgressing, healthSuspended:
		sev = model.SeverityInfo
	case healthMissing:
		sev = model.SeverityMedium
	case healthDegraded:
		sev = model.SeverityHigh
	default: // Unknown or any unrecognized value
		sev = model.SeverityMedium
	}
	title := "argocd: application " + subject + " health=" + status
	return finding(kindHealth, sev, subject, title, "health:"+status, now)
}

// opFinding emits a High finding only when the last sync operation errored or
// failed. A successful/running/absent operation produces no finding (no noise).
func opFinding(subject, phase string, now time.Time) *model.FindingReport {
	phase = strings.TrimSpace(phase)
	if phase != phaseError && phase != phaseFailed {
		return nil
	}
	title := "argocd: application " + subject + " last sync operation " + phase
	f := finding(kindOperation, model.SeverityHigh, subject, title, "operation:"+phase, now)
	return &f
}

// finding builds one FindingReport. The Title is a short, non-sensitive sentence;
// the DetailHash is a SHA-256 of a STABLE, non-sensitive key
// ("argocd:<ns/name>:<facet>:<value>") so the same posture re-hashes identically
// across runs without ever carrying a payload. No raw manifest content enters any
// field — only the structural posture.
func finding(kind string, sev model.Severity, subject, title, facet string, now time.Time) model.FindingReport {
	detail := redact.Clean(string(SignalArgoCD) + ":" + subject + ":" + facet)
	return model.FindingReport{
		Kind:        kind,
		Severity:    sev,
		SubjectKind: subjectKind,
		SubjectRef:  subject,
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  now,
	}
}

// readDocs resolves the configured path to its files and decodes every Application
// CRD document from them. A non-Application document is skipped. Files are read in
// sorted order for deterministic output.
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

// decodeFile reads one manifest file and decodes each YAML document in it (a file
// may hold several objects separated by `---`). JSON is a subset of YAML, so a
// single-object .json file decodes through the same path. A document that is not an
// Application CRD is skipped silently. An empty document decodes to the zero value
// and is skipped.
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
		if isApplicationDoc(d) {
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

// nonEmpty returns a if non-empty, else b.
func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
