// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package flux

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
const Name = "olivares.flux"

// SignalFlux is the package-local SignalSource for this connector. FindingReport
// carries no Source field, so this provenance value is woven into the finding
// Kind/Title and kept here for documentation and consistency with the argocd posture
// connector. It is NOT added to sdk/model/enums.go (the SDK enum stays untouched; an
// open-string source needs no SDK release — S02 §6).
const SignalFlux model.SignalSource = "flux"

// FindingReport kinds for the two Flux posture facets: the Ready reconciliation state
// and (only when a reconciled object is drifted) the drift state.
const (
	kindReady = "gitops_flux_ready"
	kindDrift = "gitops_flux_drift"
)

// Source is the Flux GitOps posture connector. It satisfies
// sdk.SourceConnector. It OBSERVES the GitOps estate by parsing exported Flux CRD
// manifests (GitRepository, Kustomization, HelmRelease) — it never calls the Flux
// controllers or the Kubernetes API, never triggers a reconcile, and reads no
// payloads. The zero value is not usable; call New.
type Source struct {
	path string
	now  func() time.Time // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies the SourceConnector contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns a flux source.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Flux GitOps posture",
		Description: "Parses exported Flux CRD manifests (GitRepository, Kustomization, HelmRelease across the three toolkit.fluxcd.io groups) and reports the GitOps reconciliation posture: Ready (reconciled / failing / unknown) and drift (applied vs attempted revision, observed vs spec generation). Read-only; never triggers a reconcile and reads no payloads.",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "Flux manifest file, or a directory of *.yaml / *.yml / *.json manifests (export with `kubectl get gitrepositories,kustomizations,helmreleases -A -o yaml`). Multi-document YAML supported."},
		},
	}
}

// Open reads and validates configuration.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("path"))
	if s.path == "" {
		return errors.New("flux: path is required")
	}
	return nil
}

// Close releases resources; this connector holds none between Gather runs.
func (s *Source) Close(context.Context) error { return nil }

// Gather reads the configured manifests and emits the GitOps posture findings for
// each Flux object. It is a batch source: it returns nil when the manifests are
// exhausted, and the engine re-runs it on its re-poll schedule. It emits NO edges
// (reconciliation status is observed posture, not an access flow — an edge would
// fabricate one; matches the argocd / istio-telemetry posture connectors).
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

// buildFindings maps one Flux document to its posture findings: always one Ready
// finding, plus (only when a reconciled object is drifted) one drift finding. An
// object with no name yields nothing (a stable subject ref is required); an object
// with no Ready condition is classified honestly as Unknown (Medium), never guessed
// reconciled.
func buildFindings(d document, now time.Time) []model.FindingReport {
	name := strings.TrimSpace(d.Metadata.Name)
	if name == "" {
		return nil
	}
	subjectKind := subjectKindOf(d)
	subject := subjectRef(d)
	out := make([]model.FindingReport, 0, 2)
	out = append(out, readyFinding(subjectKind, subject, d, now))
	// Drift is only a meaningful, additional signal for a reconciled (Ready==True)
	// object: a failing object's drift is subsumed by its High Ready finding, and an
	// Unknown object has no trustworthy applied revision to compare.
	if state, _ := readyOf(d); state == readyTrue && isDrifted(d) {
		out = append(out, driftFinding(subjectKind, subject, now))
	}
	return out
}

// readyFinding classifies the reconciliation posture from the Ready condition,
// VERBATIM: Ready==True => Info (reconciled), Ready==False => High (failing; the
// condition REASON token goes in the Title, never the message body), Ready
// absent/empty => Unknown (Medium), never silently Healthy.
func readyFinding(subjectKind, subject string, d document, now time.Time) model.FindingReport {
	state, reason := readyOf(d)
	var sev model.Severity
	var title, facet string
	switch state {
	case readyTrue:
		sev = model.SeverityInfo
		title = "flux: " + subjectKind + " " + subject + " Ready=True (reconciled)"
		facet = "ready:true"
	case readyFalse:
		sev = model.SeverityHigh
		// The reason is a short machine token (e.g. ArtifactFailed). It is the ONLY
		// detail from the condition that enters a field; the condition message (which
		// can carry a URL / chart path / YAML fragment) is never read. Scrub it
		// defensively even though a reason token is non-sensitive.
		r := redact.Clean(reason)
		if r == "" {
			r = "unknown reason"
		}
		title = "flux: " + subjectKind + " " + subject + " Ready=False (reconciliation failing: " + r + ")"
		facet = "ready:false:" + r
	default: // readyUnknown
		sev = model.SeverityMedium
		title = "flux: " + subjectKind + " " + subject + " Ready=Unknown (no reconciliation status reported)"
		facet = "ready:unknown"
	}
	return finding(kindReady, sev, subjectKind, subject, title, facet, now)
}

// driftFinding emits a Medium drift finding for a reconciled object whose applied
// revision differs from its attempted revision, or whose observedGeneration lags the
// spec generation. The revisions themselves are NEVER placed in the finding (they are
// compared in-memory in isDrifted); the Title states only that drift exists.
func driftFinding(subjectKind, subject string, now time.Time) model.FindingReport {
	title := "flux: " + subjectKind + " " + subject + " is drifted (applied revision != attempted, or observed generation lags spec)"
	return finding(kindDrift, model.SeverityMedium, subjectKind, subject, title, "drift", now)
}

// finding builds one FindingReport. The Title is a short, non-sensitive sentence; the
// DetailHash is a SHA-256 of a STABLE, non-sensitive key
// ("flux:<subject>:<facet>:<value>" form, with subject the ns/name) so the same
// posture re-hashes identically across runs without ever carrying a payload. No raw
// manifest content (a revision SHA, a chart version, an artifact URL) enters any
// field — only the structural posture.
func finding(kind string, sev model.Severity, subjectKind, subject, title, facet string, now time.Time) model.FindingReport {
	detail := redact.Clean(string(SignalFlux) + ":" + subject + ":" + facet)
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

// readDocs resolves the configured path to its files and decodes every Flux CRD
// document from them. A non-Flux document is skipped. Files are read in sorted order
// for deterministic output.
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
// Flux CRD is skipped silently. An empty document decodes to the zero value and is
// skipped.
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
		if isFluxDoc(d) {
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
