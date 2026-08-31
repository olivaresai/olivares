// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package aigateway

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.ai-gateway"

// SignalAIGateway is the SignalSource for this connector. CostSample carries no
// Source field, so this const exists for doc/consistency with the other connectors
// (it identifies the collector in logs and in the package doc) and to declare the
// provenance of the cost telemetry this connector produces. The SDK seeds neither an
// ai-gateway source nor this gateway value; a connector introduces its own open
// strings without an SDK release (docs/contracts/S02 §6).
const SignalAIGateway model.SignalSource = "ai_gateway"

// GatewayEnvoyAI is the deployment-surface tag stamped on every CostSample: the call
// was served THROUGH the Envoy AI Gateway. model.Gateway is an open string (the SDK
// seeds direct/bedrock/vertex/foundry but not the proxy), so this connector declares
// its own surface — module XI then attributes gateway-side spend distinctly from a
// first-party-API or cloud-gateway call.
const GatewayEnvoyAI model.Gateway = "envoy-ai-gateway"

// Source is the AI Gateway cost connector. It reads the Envoy AI Gateway's usage
// export (JSON-lines access log / usage records the operator ships) and emits one
// model.CostSample per usage record for module XXI (FinOps), gateway-side and
// Anthropic-first. The zero value is not usable; call New.
type Source struct {
	path string
	now  func() time.Time // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies the SDK contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns an ai-gateway source.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Envoy AI Gateway (usage/cost)",
		Description: "Observes the Envoy AI Gateway usage export (JSON-lines access log) and emits gateway-side cost telemetry per model/provider (Anthropic-first), read-only. Carries only token counts, model, provider and cost — never prompts or completions.",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "AI Gateway usage log file, or a directory of *.json / *.jsonl / *.log (+ .gz) usage exports (JSON lines)."},
		},
	}
}

// Open reads and validates configuration.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("path"))
	if s.path == "" {
		return errors.New("ai-gateway: path is required")
	}
	return nil
}

// Close releases resources; this connector holds none between Gather runs.
func (s *Source) Close(context.Context) error { return nil }

// Gather reads the configured usage export and emits one CostSample per usage
// record. It is a batch source: it lists the files, parses each, emits, and returns
// nil at EOF (the engine re-runs it on the next poll). It checks ctx.Err in the loop
// so a canceled gather stops promptly.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	files, err := s.listFiles()
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		recs, err := readRecords(f)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			if err := ctx.Err(); err != nil {
				return err
			}
			sample, ok := s.buildSample(rec)
			if !ok {
				continue
			}
			if err := sink.Emit(ctx, sample); err != nil {
				return err
			}
		}
	}
	return nil
}

// buildSample maps one usage record to a CostSample. It returns ok=false for a
// record that names no model or carries no usable token count / cost (a non-LLM line
// that landed in the same log), so a zero sample is never emitted.
//
// Provenance: the gateway REPORTS token counts but not money by default, so the
// sample is ProvenanceEstimated (the engine derives cost from list pricing). When the
// operator configured a CEL cost field that the record carries, that authoritative
// figure is set on CostMicroUSD and the sample is ProvenanceBilled (ARCHITECTURE.md —
// billed vs estimated is never conflated). The cache split is mapped ONLY when the
// record carries it; 0 means not reported, never invented.
func (s *Source) buildSample(rec usageRecord) (model.CostSample, bool) {
	model_ := rec.modelRef()
	if model_ == "" || !rec.hasUsage() {
		return model.CostSample{}, false
	}
	occurred, ok := rec.occurredAt()
	if !ok {
		occurred = s.clock().UTC()
	}
	sample := model.CostSample{
		ProviderRef:           rec.providerRef(),
		ModelRef:              model_,
		InputTokens:           rec.inputTokens(),
		OutputTokens:          rec.outputTokens(),
		OccurredAt:            occurred,
		CacheReadTokens:       rec.cacheReadTokens(),
		CacheCreation5mTokens: rec.cacheCreationTokens(),
		Gateway:               GatewayEnvoyAI,
		Provenance:            model.ProvenanceEstimated,
	}
	if micro, ok := rec.cost(); ok {
		sample.CostMicroUSD = micro
		sample.Provenance = model.ProvenanceBilled
	}
	return sample, true
}

// listFiles resolves the configured path to a sorted list of files. A directory
// contributes its *.json / *.jsonl / *.log entries (and their .gz variants); a file
// contributes itself.
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
		if isUsageFile(e.Name()) {
			files = append(files, filepath.Join(s.path, e.Name()))
		}
	}
	sort.Strings(files)
	return files, nil
}

// isUsageFile reports whether a directory entry name looks like a usage export this
// connector parses (JSON lines), accepting a .gz variant of each suffix.
func isUsageFile(name string) bool {
	n := strings.TrimSuffix(name, ".gz")
	return strings.HasSuffix(n, ".json") ||
		strings.HasSuffix(n, ".jsonl") ||
		strings.HasSuffix(n, ".ndjson") ||
		strings.HasSuffix(n, ".log")
}

// readRecords reads one usage file (gunzipping a .gz) into its records.
func readRecords(path string) ([]usageRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
		defer func() { _ = gz.Close() }()
		r = gz
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return recordsFromBytes(data), nil
}

func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}
