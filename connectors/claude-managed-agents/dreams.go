// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudemanagedagents

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/httpx"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// dreams.go observes the Dreams research preview (platform.claude.com/docs/en/
// managed-agents/dreams, verified 2026-06-10): an ASYNC MEMORY-MUTATION job that reads
// one memory store + 1–100 past sessions and produces a NEW output memory store (the
// inputs are never modified; on failure/cancel the output persists with partial
// contents). The control plane treats a dream as a HIGH-RISK memory mutation, never as
// just another job (CUR-3):
//
//   - INVENTORY: every dream is a governed object (who executed it — the underlying
//     pipeline session —, which store and sessions it read, which store it produced).
//   - ADMISSION (deny-closed): a dream's OUTPUT store is UNTRUSTED until a human admits
//     it (HITL). The connector emits a pending-admission finding per output store
//     and treats only the operator-recorded admitted_dream_outputs config list as
//     admitted; everything else is unadmitted by construction. The cryptographic
//     integrity verification of the store's CONTENT is contract — this gate
//     stands alone as untrusted-until-admitted and leaves that seam open.
//   - DRIFT: an unadmitted output store OBSERVED attached to a session (the verified
//     memory_store_id list filter) is the admission control failing — a HIGH finding.
//   - HONEST DEGRADE: the preview is GATED. An org without access gets ONE posture
//     finding declaring the surface uncovered (never fabricated data), and the poller
//     stops asking until restart.
//
// Progress is poll-only — the live webhook catalog has NO dream.* family (verified
// 2026-06-10), so the dreams surface rides the GET poller exclusively.

// dreamsBetaSuffix is the additional beta gate Dreams requires on top of the Managed
// Agents beta; the two travel comma-separated in one anthropic-beta header (verbatim
// from the Dreams doc curl).
const dreamsBetaSuffix = "dreaming-2026-04-21"

// dreamCostType labels a dream's token usage so FinOps attributes memory-curation
// spend distinctly (dreams bill at standard API token rates for the selected model).
const dreamCostType = "dream"

// Dream is the dream resource (drm_..., verbatim live shape). instructions is
// operator-authored content the connector never reads (the field is not declared —
// minimal data, docs/SECURITY-HARDENING.md).
type Dream struct {
	ID      string        `json:"id"`
	Status  string        `json:"status"` // pending|running|completed|failed|canceled
	Inputs  []DreamInput  `json:"inputs"`
	Outputs []DreamOutput `json:"outputs"`
	Model   struct {
		ID string `json:"id"`
	} `json:"model"`
	SessionID  string `json:"session_id"` // the underlying pipeline session once running
	CreatedAt  string `json:"created_at"`
	EndedAt    string `json:"ended_at"`
	ArchivedAt string `json:"archived_at"`
	Usage      struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	} `json:"usage"`
	Error struct {
		Type string `json:"type"`
	} `json:"error"`
}

// DreamInput is one inputs[] entry — the discriminated union of the memory store to
// curate and the past sessions ("transcripts") to read: {type:"memory_store",
// memory_store_id} | {type:"sessions", session_ids[1..100]}.
type DreamInput struct {
	Type          string   `json:"type"`
	MemoryStoreID string   `json:"memory_store_id"`
	SessionIDs    []string `json:"session_ids"`
}

// DreamOutput is one outputs[] entry: the NEW memory store the dream produced. It
// appears once the dream starts running and persists (with partial contents) on
// failure/cancel.
type DreamOutput struct {
	Type          string `json:"type"`
	MemoryStoreID string `json:"memory_store_id"`
}

// terminal reports whether the dream reached a terminal status (usage final).
func (d Dream) terminal() bool {
	switch d.Status {
	case "completed", "failed", "canceled":
		return true
	default:
		return false
	}
}

// inputStoreIDs / inputSessionIDs flatten the inputs union.
func (d Dream) inputStoreIDs() []string {
	var out []string
	for _, in := range d.Inputs {
		if in.Type == "memory_store" && in.MemoryStoreID != "" {
			out = append(out, in.MemoryStoreID)
		}
	}
	return out
}

func (d Dream) inputSessionIDs() []string {
	var out []string
	for _, in := range d.Inputs {
		if in.Type == "sessions" {
			out = append(out, in.SessionIDs...)
		}
	}
	return out
}

// outputStoreIDs flattens the outputs (empty until the dream runs).
func (d Dream) outputStoreIDs() []string {
	var out []string
	for _, o := range d.Outputs {
		if o.Type == "memory_store" && o.MemoryStoreID != "" {
			out = append(out, o.MemoryStoreID)
		}
	}
	return out
}

// dreamPage is the dreams LIST envelope ({data, next_page} family, `page` cursor,
// limit max 100, newest first, non-archived by default).
type dreamPage struct {
	Data     []Dream `json:"data"`
	NextPage string  `json:"next_page"`
}

// fetchDreams lists the workspace's (non-archived) dreams. It uses the dreams client
// (the anthropic-beta header additionally carries dreaming-2026-04-21), bounded by
// maxPages.
func (c *client) fetchDreams(ctx context.Context) ([]Dream, error) {
	var out []Dream
	page := ""
	for i := 0; i < c.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		q := url.Values{"limit": {"100"}}
		if page != "" {
			q.Set("page", page)
		}
		var p dreamPage
		if err := c.dreams.GetJSON(ctx, "/v1/dreams", q, &p); err != nil {
			return out, err
		}
		out = append(out, p.Data...)
		if p.NextPage == "" {
			break
		}
		page = p.NextPage
	}
	return out, nil
}

// dreamsGated classifies a dreams fetch error as the GATED-preview case: a 403 (the
// generic beta-without-access mapping) or 404 (the endpoints are unpublished for orgs
// outside the preview; the exact no-access shape is undocumented — both are treated as
// "no access", stated as such). A 401 is a credential fault, not gating.
func dreamsGated(err error) bool {
	var se *httpx.StatusError
	if !errors.As(err, &se) {
		return false
	}
	return se.Status == 403 || se.Status == 404
}

// dreamsGatedFinding is the one-shot honest-degrade posture record: the org has no
// access to the GATED research preview, so the dreams surface is DECLARED uncovered
// rather than silently absent or fabricated.
func dreamsGatedFinding(status int, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingPosture,
		Severity:    model.SeverityInfo,
		SubjectKind: connectorSubject,
		SubjectRef:  "dreams",
		Title:       "CMA Dreams research preview not accessible (GATED) — surface declared, not covered",
		DetailHash:  redact.Hash(fmt.Sprintf("dreams gated status=%d; the dreaming-2026-04-21 preview is access-gated (request via claude.com/form/claude-managed-agents); the connector covers the surface honestly as ABSENT and stops polling it until restart", status)),
		OccurredAt:  at,
	}
}

// dreamObservations maps one dream to its governed-object observations. Stable
// timestamps (the dream's own created_at / ended_at) de-dup re-emission across polls.
//
//   - workspace→dream inventory edge (the job as a first-class governed object) and a
//     workspace→memory_store edge per OUTPUT store (the new store is inventoried the
//     moment it exists, before anything attaches it).
//   - session-origin graph edges once the pipeline session exists: session→input store
//     (read), session→output store (write), session→input session (read) — the memory
//     mutation's full provenance in the R/RW graph, with the dream id as ToolRef.
//   - the pending-admission finding per output store (unless operator-admitted), the
//     admission acknowledgment for admitted stores (ledger evidence the HITL decision
//     was recorded), and a failure finding for a failed dream.
//   - a CostSample once terminal (unpriced tokens, CostType "dream", estimated — module
//     XI applies list pricing; segmented by CostType so billed workspace reconciliation
//     can exclude it).
func dreamObservations(d Dream, workspaceRef string, admitted map[string]bool, fallbackAt time.Time) []model.Observation {
	created := parseTime(d.CreatedAt)
	if created.IsZero() {
		created = fallbackAt
	}
	ended := parseTime(d.EndedAt)
	if ended.IsZero() {
		ended = created
	}
	dreamRef := redact.Clean(d.ID)
	sessionRef := redact.Clean(d.SessionID)

	var out []model.Observation
	out = append(out, model.EdgeObservation{
		OriginKind:   originWorkspace,
		OriginRef:    labelRef(workspaceRef, "workspace"),
		ResourceKind: kindDream,
		ResourceRef:  dreamRef,
		Mode:         model.ModeRead,
		Source:       model.SignalCMA,
		Confidence:   model.ConfidenceAttributed,
		ObservedAt:   created,
	})

	sessionEdge := func(kind, ref string, mode model.AccessMode) model.EdgeObservation {
		return model.EdgeObservation{
			OriginKind:   originSession,
			OriginRef:    sessionRef,
			ResourceKind: kind,
			ResourceRef:  redact.Clean(ref),
			Mode:         mode,
			Source:       model.SignalCMA,
			Confidence:   model.ConfidenceAttributed,
			ToolRef:      dreamRef,
			ObservedAt:   created,
		}
	}
	if sessionRef != "" {
		for _, id := range d.inputStoreIDs() {
			out = append(out, sessionEdge(kindMemoryStore, id, model.ModeRead))
		}
		for _, id := range d.inputSessionIDs() {
			out = append(out, sessionEdge(kindManagedAgent, id, model.ModeRead))
		}
	}
	for _, id := range d.outputStoreIDs() {
		out = append(out, model.EdgeObservation{
			OriginKind:   originWorkspace,
			OriginRef:    labelRef(workspaceRef, "workspace"),
			ResourceKind: kindMemoryStore,
			ResourceRef:  redact.Clean(id),
			Mode:         model.ModeRead,
			Source:       model.SignalCMA,
			Confidence:   model.ConfidenceAttributed,
			ObservedAt:   created,
		})
		if sessionRef != "" {
			out = append(out, sessionEdge(kindMemoryStore, id, model.ModeWrite))
		}
		if f, ok := dreamAdmissionFinding(d, id, admitted[id], ended); ok {
			out = append(out, f)
		}
	}
	if d.Status == "failed" {
		out = append(out, model.FindingReport{
			Kind:        findingGovernance,
			Severity:    model.SeverityLow,
			SubjectKind: kindDream,
			SubjectRef:  dreamRef,
			Title:       "CMA dream failed (" + labelRef(d.Error.Type, "unknown error") + ")",
			DetailHash:  redact.Hash("dream failed id=" + d.ID + " error=" + d.Error.Type + " model=" + d.Model.ID + "; on failure the output store persists with partial contents — review or discard it (Dreams)"),
			OWASPASI:    []string{asiMemoryPoison},
			OccurredAt:  ended,
		})
	}
	if cs, ok := dreamCostSample(d, ended); ok {
		out = append(out, cs)
	}
	return out
}

// dreamAdmissionFinding is the per-output-store admission record — the heart of the
// deny-closed gate. An UNADMITTED store gets the pending-admission finding (Medium,
// ASI06: an unreviewed machine-curated memory store is a poisoning vector for every
// session that mounts it); the AGPL plane routes it to the HITL queue and a human
// decides via the governed approval. An ADMITTED store (the operator transcribed
// the approval into admitted_dream_outputs) gets an Info acknowledgment so the ledger
// carries the recorded decision. ok is false for an empty store ref.
func dreamAdmissionFinding(d Dream, storeID string, isAdmitted bool, at time.Time) (model.FindingReport, bool) {
	if storeID == "" {
		return model.FindingReport{}, false
	}
	detail := fmt.Sprintf("dream=%s model=%s status=%s session=%s input_stores=%v transcripts=%d output_store=%s",
		d.ID, d.Model.ID, d.Status, d.SessionID, d.inputStoreIDs(), len(d.inputSessionIDs()), storeID)
	if isAdmitted {
		return model.FindingReport{
			Kind:        findingGovernance,
			Severity:    model.SeverityInfo,
			SubjectKind: kindMemoryStore,
			SubjectRef:  redact.Clean(storeID),
			Title:       "CMA dream output memory store ADMITTED for productive attach (operator-recorded HITL decision)",
			DetailHash:  redact.Hash(detail + " admission=recorded"),
			OccurredAt:  at,
		}, true
	}
	return model.FindingReport{
		Kind:        findingGovernance,
		Severity:    model.SeverityMedium,
		SubjectKind: kindMemoryStore,
		SubjectRef:  redact.Clean(storeID),
		Title:       "CMA dream produced an output memory store awaiting HITL admission — do not attach to productive sessions",
		DetailHash:  redact.Hash(detail + " admission=pending; a dream's output store is an UNTRUSTED machine-curated memory mutation until a human reviews and admits it (HITL; content-integrity verification is the contract)"),
		OWASPASI:    []string{asiMemoryPoison},
		OccurredAt:  at,
	}, true
}

// unadmittedAttachFinding is the PERMITTED-vs-OBSERVED drift for the admission gate: a
// session was OBSERVED with an unadmitted dream-output store among its resources — the
// memory mutation reached a productive session without the human gate. HIGH: this is
// the exact failure mode the admission control exists to stop (and ≥High persists into
// the security view).
func unadmittedAttachFinding(dreamID, storeID string, s Session, fallbackAt time.Time) model.FindingReport {
	at := parseTime(s.CreatedAt)
	if at.IsZero() {
		at = fallbackAt
	}
	return model.FindingReport{
		Kind:        findingGovernance,
		Severity:    model.SeverityHigh,
		SubjectKind: kindMemoryStore,
		SubjectRef:  redact.Clean(storeID),
		Title:       "CMA dream output store attached to a session WITHOUT HITL admission",
		DetailHash:  redact.Hash(fmt.Sprintf("unadmitted attach store=%s dream=%s session=%s session_status=%s; the dream's machine-curated output store reached a session before a human admitted it — revoke the attach or admit the store via the governed approval", storeID, dreamID, s.ID, s.Status)),
		OWASPASI:    []string{asiMemoryPoison},
		OccurredAt:  at,
	}
}

// dreamCostSample maps a TERMINAL dream's token usage to a CostSample so FinOps
// attributes memory-curation spend (dreams bill at standard API token rates). Unpriced
// here (token counts only): provenance estimated, CostMicroUSD 0 — module XI applies
// list pricing; CostType "dream" segments it from ordinary inference so billed
// workspace reconciliation can exclude it. ok is false before terminal status (usage
// still moving) or when there is no usage to report.
func dreamCostSample(d Dream, at time.Time) (model.CostSample, bool) {
	u := d.Usage
	if !d.terminal() || (u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadInputTokens == 0 && u.CacheCreationInputTokens == 0) {
		return model.CostSample{}, false
	}
	return model.CostSample{
		ProviderRef:           "anthropic",
		ModelRef:              redact.Clean(d.Model.ID),
		SessionRef:            redact.Clean(d.SessionID),
		InputTokens:           u.InputTokens,
		OutputTokens:          u.OutputTokens,
		CacheReadTokens:       u.CacheReadInputTokens,
		CacheCreation5mTokens: u.CacheCreationInputTokens,
		CostType:              dreamCostType,
		Provenance:            model.ProvenanceEstimated,
		Gateway:               model.GatewayDirect,
		OccurredAt:            at,
	}, true
}
