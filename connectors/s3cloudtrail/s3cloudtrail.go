// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package s3cloudtrail

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/identity"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.s3-cloudtrail"

// Source is the s3-cloudtrail source connector. It reads CloudTrail log files and
// emits one edge per S3 event. The zero value is not usable; call New.
type Source struct {
	path   string
	shared identity.SharedSet
}

// Compile-time proof that Source satisfies the SDK contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns an s3-cloudtrail source.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "AWS CloudTrail (S3)",
		Description: "Captures R/RW access to S3 from AWS CloudTrail logs, read-only.",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "CloudTrail log file or a directory of *.json / *.json.gz files"},
			{Key: "shared_accounts", Type: sdk.FieldString, Description: "comma-separated IAM role ARNs that are shared (attribution marked approximate)"},
		},
	}
}

// Open reads and validates configuration.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("path"))
	if s.path == "" {
		return errors.New("s3-cloudtrail: path is required")
	}
	s.shared = identity.ParseSharedAccounts(cfg.Get("shared_accounts"))
	return nil
}

// Gather reads the configured file (or every *.json/*.json.gz file in the
// configured directory, in name order) and emits an edge per S3 event. It is a
// batch source: it returns nil when the files are exhausted.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	files, err := s.listFiles()
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherFile(ctx, f, sink); err != nil {
			return err
		}
	}
	return nil
}

// Close releases resources; this connector holds none between Gather runs.
func (s *Source) Close(context.Context) error { return nil }

// listFiles resolves the configured path to a sorted list of files. A directory
// contributes its *.json and *.json.gz entries; a file contributes itself.
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
		if n := e.Name(); strings.HasSuffix(n, ".json") || strings.HasSuffix(n, ".json.gz") {
			files = append(files, filepath.Join(s.path, n))
		}
	}
	sort.Strings(files)
	return files, nil
}

// gatherFile reads one CloudTrail file (gunzipping a .gz) and emits its S3 edges.
func (s *Source) gatherFile(ctx context.Context, path string, sink sdk.Sink) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer func() { _ = gz.Close() }()
		r = gz
	}

	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	for _, rec := range recordsFromBytes(data) {
		if err := ctx.Err(); err != nil {
			return err
		}
		edge, ok := s.buildEdge(rec)
		if !ok {
			continue
		}
		if err := sink.Emit(ctx, edge); err != nil {
			return err
		}
	}
	return nil
}

// buildEdge maps a CloudTrail S3 record to an edge, or ok=false if it is not an
// emittable S3 access (a non-S3 event, no resolvable resource or identity, or an
// unparseable timestamp).
func (s *Source) buildEdge(rec record) (model.EdgeObservation, bool) {
	// CLA-11: a Claude-on-Bedrock invocation in the trail becomes a model-access edge
	// (the Anthropic APIs do not see Bedrock; this CloudTrail path is the only one).
	if isBedrockModelInvocation(rec) {
		return s.buildBedrockEdge(rec)
	}
	if rec.EventSource != s3EventSource {
		return model.EdgeObservation{}, false
	}
	ts, ok := parseTime(rec.EventTime)
	if !ok {
		return model.EdgeObservation{}, false
	}
	kind, ref, ok := resolveResource(rec)
	if !ok {
		return model.EdgeObservation{}, false
	}
	origin, conf, ok := s.resolveIdentity(rec.UserIdentity)
	if !ok {
		return model.EdgeObservation{}, false
	}
	return model.EdgeObservation{
		OriginKind:   originKind,
		OriginRef:    origin,
		ResourceKind: kind,
		ResourceRef:  ref,
		Mode:         classifyMode(rec.ReadOnly),
		Source:       model.SignalCloudTrail,
		Confidence:   conf,
		ToolRef:      rec.EventName,
		ObservedAt:   ts,
	}, true
}
