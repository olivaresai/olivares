// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package crossplane

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
const Name = "olivares.crossplane"

// SignalCrossplane is the package-local SignalSource for this connector
//. FindingReport carries no Source field, so this provenance value is
// woven into the finding Kind/Title and kept here for documentation and consistency
// with the other IaC posture connectors (each declares its own open-string source).
// It is NOT added to sdk/model/enums.go (the SDK enum stays untouched; an
// open-string source needs no SDK release — S02 §6).
const SignalCrossplane model.SignalSource = "crossplane"

// findingKind is the FindingReport.Kind for every inventory finding: a Crossplane
// composite resource definition recorded into the estate inventory.
const findingKind = "crossplane_xrd"

// subjectKind is the FindingReport.SubjectKind: a Crossplane XRD.
const subjectKind = "crossplane.xrd"

// Source is the Crossplane XRD interop connector. It satisfies
// sdk.SourceConnector. It is INTROSPECTION ONLY — it takes the composite API surface
// of exported XRD manifests into the estate inventory and is NOT a Crossplane
// Operator (the boundary): it runs no controller, reads no Composite Resources
// or Claims, and mutates nothing. The zero value is not usable; call New.
type Source struct {
	path string
	now  func() time.Time // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies the SourceConnector contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns a crossplane source.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Crossplane XRD inventory",
		Description: "Parses exported Crossplane CompositeResourceDefinition (apiextensions.crossplane.io/v1) manifests and records each composite API surface — group, composite kind, and declared versions (served/referenceable) — into the estate inventory. Introspection only; not an Operator: reads no composite resources or claims and mutates nothing.",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "Crossplane XRD manifest file, or a directory of *.yaml / *.yml / *.json manifests (export with `kubectl get xrd -o yaml`). Multi-document YAML supported."},
		},
	}
}

// Open reads and validates configuration.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("path"))
	if s.path == "" {
		return errors.New("crossplane: path is required")
	}
	return nil
}

// Close releases resources; this connector holds none between Gather runs.
func (s *Source) Close(context.Context) error { return nil }

// Gather reads the configured manifests and emits one inventory FindingReport per
// XRD: the composite API surface the platform team introduced. It is a batch source:
// it returns nil when the manifests are exhausted, and the engine re-runs it on its
// re-poll schedule. It emits NO edges (an XRD declares a TYPE in the inventory, not
// an access flow — an edge would fabricate one; matches the istio-telemetry / argocd
// posture connectors).
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
		f, ok := buildFinding(d, now)
		if !ok {
			continue
		}
		if err := sink.Emit(ctx, f); err != nil {
			return err
		}
	}
	return nil
}

// buildFinding maps one XRD document to its inventory finding. An XRD with no
// metadata.name yields nothing (a stable subject ref is required); otherwise it
// yields exactly one Info finding that records the composite API surface (group /
// kind / plural and the declared versions). Severity is always Info — an XRD is an
// inventory fact (a type the estate exposes), not a risk classification.
func buildFinding(d document, now time.Time) (model.FindingReport, bool) {
	name := subjectRef(d)
	if name == "" {
		return model.FindingReport{}, false
	}
	api := compositeAPI(d)
	kind := strings.TrimSpace(d.Spec.Names.Kind)
	versions := versionLabels(d)

	// Title: a short, non-sensitive sentence describing the composite API surface.
	// e.g. "crossplane: composite resource definition xdatabases.custom-api.example.org
	// (kind XDatabase, versions: v1alpha1[served], v1beta1[not served])".
	var b strings.Builder
	b.WriteString("crossplane: composite resource definition ")
	b.WriteString(api)
	if kind != "" {
		b.WriteString(" (kind ")
		b.WriteString(kind)
	} else {
		b.WriteString(" (")
	}
	if len(versions) > 0 {
		b.WriteString(", versions: ")
		b.WriteString(strings.Join(versions, ", "))
	} else {
		b.WriteString(", no versions declared")
	}
	b.WriteString(")")
	title := b.String()

	// detail key is the stable, non-sensitive identity of this XRD:
	// "crossplane:<name>:<group>/<kind>:<sorted versions>". It is scrubbed defensively
	// (it is composed only of structural API names and fixed tokens, so it holds no
	// secret, but the no-leak guarantee is enforced, not assumed).
	detail := redact.Clean(
		string(SignalCrossplane) + ":" + name + ":" +
			strings.TrimSpace(d.Spec.Group) + "/" + kind + ":" +
			strings.Join(versionNames(d), ","))

	return model.FindingReport{
		Kind:        findingKind,
		Severity:    model.SeverityInfo,
		SubjectKind: subjectKind,
		SubjectRef:  name,
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  now,
	}, true
}

// readDocs resolves the configured path to its files and decodes every XRD document
// from them. A non-XRD document is skipped. Files are read in sorted order for
// deterministic output.
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
// XRD is skipped silently. An empty document (a stray `---` or a comment-only block)
// decodes to the zero value and is skipped.
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
		if isXRDDoc(d) {
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
