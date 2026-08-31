// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// assistants.go implements the Assistants API governance surfaces: inventory of
// assistants, files, and vector stores, plus operator-declared policy enforcement
// (allowed models/tools per assistant, blocked file purposes).
//
// READ-ONLY AND MINIMAL-DATA (docs/SECURITY-HARDENING.md-3): every call is a GET via the
// shared GET-only modelprovider client. The connector never reads assistant
// instructions, thread messages, file content, or secrets — only inventory
// metadata (ids, model, tool types, file purpose/size/status, vector-store
// counts). It never creates, modifies, or deletes assistants/files/stores.
//
// The Assistants API (v2) requires the header OpenAI-Beta: assistants=v2; the
// connector uses a second client (assistantsClient) with that header so the org
// API client is unaffected.
package openai

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Finding subjects for the assistants governance surfaces.
const (
	subjectAssistant   = "openai.assistant"
	subjectFile        = "openai.file"
	subjectVectorStore = "openai.vector_store"
)

// ---------- Assistants inventory (GET /v1/assistants) ----------

// gatherAssistants paginates the Assistants API and emits one inventory finding
// per assistant (model, tool types, metadata), then runs the operator-declared
// policy check against each. On unavailable statuses it degrades honestly.
func (s *Source) gatherAssistants(ctx context.Context, sink sdk.Sink) error {
	var assistants []assistantEntry
	after := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var resp assistantsResponse
		q := url.Values{"limit": {"100"}, "order": {"desc"}}
		if after != "" {
			q.Set("after", after)
		}
		if err := s.assistantsClient.GetJSON(ctx, "/v1/assistants", q, &resp); err != nil {
			if isUnavailable(err) {
				if !s.clock().UTC().Before(assistantsSunset) {
					return sink.Emit(ctx, s.assistantsRemovedFinding())
				}
				return sink.Emit(ctx, s.unavailableFinding("assistants", "/v1/assistants"))
			}
			return err
		}
		for _, a := range resp.Data {
			if a.ID == "" {
				continue
			}
			assistants = append(assistants, a)
			if err := sink.Emit(ctx, s.assistantFinding(a)); err != nil {
				return err
			}
		}
		if !resp.HasMore || resp.LastID == "" {
			break
		}
		after = resp.LastID
	}
	if err := s.enforceAssistantsPolicy(ctx, sink, assistants); err != nil {
		return err
	}
	return s.emitAssistantsDeprecation(ctx, sink, assistants)
}

// assistantFinding builds an inventory finding for one assistant. The model and
// tool types are governance-relevant metadata; instructions are never read.
func (s *Source) assistantFinding(a assistantEntry) model.FindingReport {
	tools := assistantToolTypes(a.Tools)
	title := fmt.Sprintf("OpenAI assistant %q: model=%s, tools=[%s]",
		truncateName(a.Name, 40), a.Model, strings.Join(tools, ","))

	detail := fmt.Sprintf("openai assistant id=%s name=%s model=%s tools=[%s]",
		a.ID, a.Name, a.Model, strings.Join(tools, ","))

	return model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectAssistant,
		SubjectRef:  a.ID,
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}
}

// ---------- Files inventory (GET /v1/files) ----------

// gatherFiles lists all uploaded files and emits one inventory finding per file
// (purpose, size, status). The file content is never read. On a 403/404 it
// degrades honestly.
func (s *Source) gatherFiles(ctx context.Context, sink sdk.Sink) error {
	var resp filesResponse
	q := url.Values{}
	if err := s.client.GetJSON(ctx, "/v1/files", q, &resp); err != nil {
		if isUnavailable(err) {
			return sink.Emit(ctx, s.unavailableFinding("files", "/v1/files"))
		}
		return err
	}
	for _, f := range resp.Data {
		if f.ID == "" {
			continue
		}
		if err := sink.Emit(ctx, s.fileFinding(f)); err != nil {
			return err
		}
	}
	return nil
}

// fileFinding builds an inventory finding for one uploaded file. Filename is
// included (it's operator-chosen metadata, not content); file bytes are never
// read.
func (s *Source) fileFinding(f fileEntry) model.FindingReport {
	title := fmt.Sprintf("OpenAI file %q: purpose=%s, size=%d bytes, status=%s",
		truncateName(f.Filename, 40), f.Purpose, f.Bytes, f.Status)

	detail := fmt.Sprintf("openai file id=%s filename=%s purpose=%s bytes=%d status=%s",
		f.ID, f.Filename, f.Purpose, f.Bytes, f.Status)

	return model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectFile,
		SubjectRef:  f.ID,
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}
}

// ---------- Vector Stores inventory (GET /v1/vector_stores) ----------

// gatherVectorStores paginates the vector stores list and emits one inventory
// finding per store (file counts, usage, status). On a 403/404 it degrades.
func (s *Source) gatherVectorStores(ctx context.Context, sink sdk.Sink) error {
	after := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var resp vectorStoresResponse
		q := url.Values{"limit": {"100"}, "order": {"desc"}}
		if after != "" {
			q.Set("after", after)
		}
		if err := s.assistantsClient.GetJSON(ctx, "/v1/vector_stores", q, &resp); err != nil {
			if isUnavailable(err) {
				return sink.Emit(ctx, s.unavailableFinding("vector_stores", "/v1/vector_stores"))
			}
			return err
		}
		for _, vs := range resp.Data {
			if vs.ID == "" {
				continue
			}
			if err := sink.Emit(ctx, s.vectorStoreFinding(vs)); err != nil {
				return err
			}
		}
		if !resp.HasMore || resp.LastID == "" {
			return nil
		}
		after = resp.LastID
	}
	return nil
}

// vectorStoreFinding builds an inventory finding for one vector store.
func (s *Source) vectorStoreFinding(vs vectorStoreEntry) model.FindingReport {
	title := fmt.Sprintf("OpenAI vector store %q: files=%d (completed=%d, failed=%d), usage=%d bytes, status=%s",
		truncateName(vs.Name, 40), vs.FileCounts.Total, vs.FileCounts.Completed,
		vs.FileCounts.Failed, vs.UsageBytes, vs.Status)

	detail := fmt.Sprintf("openai vector_store id=%s name=%s total_files=%d completed=%d failed=%d usage_bytes=%d status=%s",
		vs.ID, vs.Name, vs.FileCounts.Total, vs.FileCounts.Completed,
		vs.FileCounts.Failed, vs.UsageBytes, vs.Status)

	sev := model.SeverityInfo
	if vs.FileCounts.Failed > 0 {
		sev = model.SeverityLow
	}

	return model.FindingReport{
		Kind:        "inventory",
		Severity:    sev,
		SubjectKind: subjectVectorStore,
		SubjectRef:  vs.ID,
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}
}

// ---------- Policy enforcement ----------

// assistantsPolicy holds the operator-declared policy for assistants governance.
// It is configured via the connector's config fields and defaults to nil (no
// policy = no enforcement, inventory-only mode).
type assistantsPolicy struct {
	AllowedModels       []string // model prefixes allowed for assistants (empty = all)
	AllowedTools        []string // tool types allowed (empty = all)
	BlockedFilePurposes []string // file purposes that are disallowed (empty = none blocked)
}

// enforceAssistantsPolicy checks each collected assistant against the operator
// policy and emits a policy_violation finding per violation.
func (s *Source) enforceAssistantsPolicy(ctx context.Context, sink sdk.Sink, assistants []assistantEntry) error {
	if s.asstPolicy == nil {
		return nil
	}
	for _, a := range assistants {
		if err := s.checkAssistantModel(ctx, sink, a); err != nil {
			return err
		}
		if err := s.checkAssistantTools(ctx, sink, a); err != nil {
			return err
		}
	}
	return nil
}

// checkAssistantModel emits a policy_violation if the assistant uses a model not
// in the allowed list.
func (s *Source) checkAssistantModel(ctx context.Context, sink sdk.Sink, a assistantEntry) error {
	if len(s.asstPolicy.AllowedModels) == 0 || a.Model == "" {
		return nil
	}
	for _, prefix := range s.asstPolicy.AllowedModels {
		if hasPrefix(a.Model, prefix) {
			return nil
		}
	}
	title := fmt.Sprintf("Assistant %q uses disallowed model %q", truncateName(a.Name, 30), a.Model)
	return sink.Emit(ctx, model.FindingReport{
		Kind:        "policy_violation",
		Severity:    model.SeverityHigh,
		SubjectKind: subjectAssistant,
		SubjectRef:  a.ID,
		Title:       title,
		DetailHash:  redact.Hash(fmt.Sprintf("openai assistant_model_violation id=%s model=%s", a.ID, a.Model)),
		OccurredAt:  s.clock().UTC(),
	})
}

// checkAssistantTools emits a policy_violation for each tool type on the
// assistant that is not in the allowed list.
func (s *Source) checkAssistantTools(ctx context.Context, sink sdk.Sink, a assistantEntry) error {
	if len(s.asstPolicy.AllowedTools) == 0 {
		return nil
	}
	allowed := make(map[string]bool, len(s.asstPolicy.AllowedTools))
	for _, t := range s.asstPolicy.AllowedTools {
		allowed[t] = true
	}
	for _, t := range a.Tools {
		if t.Type == "" || allowed[t.Type] {
			continue
		}
		title := fmt.Sprintf("Assistant %q uses disallowed tool %q", truncateName(a.Name, 30), t.Type)
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        "policy_violation",
			Severity:    model.SeverityHigh,
			SubjectKind: subjectAssistant,
			SubjectRef:  a.ID,
			Title:       title,
			DetailHash:  redact.Hash(fmt.Sprintf("openai assistant_tool_violation id=%s tool=%s", a.ID, t.Type)),
			OccurredAt:  s.clock().UTC(),
		}); err != nil {
			return err
		}
	}
	return nil
}

// ---------- Helpers ----------

// assistantToolTypes extracts the tool type strings from an assistant's tool list.
func assistantToolTypes(tools []assistantTool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		if t.Type != "" {
			out = append(out, t.Type)
		}
	}
	return out
}

// truncateName caps a display name at n runes, appending "…" if truncated.
func truncateName(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
