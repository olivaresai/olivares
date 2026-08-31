// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
)

// SourceRoster is the live connector/source reconfiguration surface the
// console/CLI drive. The composition root (cmd/olivares) implements it over the
// durable source roster (the sealed store) plus the running runtime: a write is
// PERSISTED first, then applied to the live engine (add / rotate / remove a single
// connector) — no process restart. The API package owns only the transport and
// the auth gate; all connector construction, secret-reference resolution and the
// add/remove/rotate mechanics live in the implementation, which holds the runtime
// (modules never can, so this is never a module route).
//
// Every method is deny-closed and honest: a source that cannot be built or
// validated is REJECTED with a reason (never silently dropped, never faked), and
// changes outside the source roster (listeners, TLS, the database DSN, the bus,
// identity/roster providers, knowledge documents, connector-trust) are reported
// as requiring a restart, never pretended to be applied live.
type SourceRoster interface {
	// ListSources returns the persisted roster, each annotated with its live
	// running status. Never returns a secret value (config carries references).
	ListSources(ctx context.Context) ([]SourceRosterEntry, error)
	// PutSource persists a source definition (create or update by name) and then
	// applies it to the running engine, returning the live outcome.
	PutSource(ctx context.Context, actor auth.Principal, in SourceRosterInput) (SourceApplyResult, error)
	// DeleteSource removes a source definition and stops it in the running engine.
	DeleteSource(ctx context.Context, actor auth.Principal, name string) (SourceApplyResult, error)
	// ReloadSources re-reads the whole roster and reconciles it against the running
	// engine (add/remove/rotate every drifted source), returning a per-source
	// report. This is the SIGHUP/endpoint full re-sync.
	ReloadSources(ctx context.Context, actor auth.Principal) (SourceReloadReport, error)
}

// SourceRosterInput is the editable shape of a source definition. Config carries
// connector settings and secret REFERENCES (e.g. "store:vault/token"), never a
// literal secret value.
type SourceRosterInput struct {
	Name        string             `json:"name"`
	Kind        string             `json:"kind,omitempty"`
	Tenant      string             `json:"tenant"`
	PollSeconds int                `json:"poll_seconds,omitempty"`
	Enabled     bool               `json:"enabled"`
	Config      map[string]string  `json:"config,omitempty"`
	Plugin      *SourcePluginInput `json:"plugin,omitempty"`
}

// SourcePluginInput provisions an external (third-party) connector-plugin source.
type SourcePluginInput struct {
	Path           string   `json:"path"`
	SHA256         string   `json:"sha256"`
	Bundle         string   `json:"bundle"`
	PredicateTypes []string `json:"predicate_types,omitempty"`
}

// SourceRosterEntry is the read shape: the persisted definition plus its live
// status ("running"/"failed"/"stopped"/"disabled"/"not_wired").
type SourceRosterEntry struct {
	Name        string             `json:"name"`
	Kind        string             `json:"kind,omitempty"`
	Tenant      string             `json:"tenant"`
	PollSeconds int                `json:"poll_seconds,omitempty"`
	Enabled     bool               `json:"enabled"`
	Config      map[string]string  `json:"config,omitempty"`
	Plugin      *SourcePluginInput `json:"plugin,omitempty"`
	Status      string             `json:"status"`
	SourceMode  string             `json:"source_mode"`
}

func normalizeRosterSourceMode(mode string) string {
	if strings.ToLower(strings.TrimSpace(mode)) == "live" {
		return "live"
	}
	return "export"
}

func rosterEntrySourceMode(src SourceRosterEntry) string {
	if strings.TrimSpace(src.SourceMode) != "" {
		return normalizeRosterSourceMode(src.SourceMode)
	}
	if src.Config != nil {
		return normalizeRosterSourceMode(src.Config["mode"])
	}
	return "export"
}

func normalizeRosterEntry(entry *SourceRosterEntry) {
	if entry == nil {
		return
	}
	entry.SourceMode = rosterEntrySourceMode(*entry)
}

// CapabilityPosture says WHY an optional capability is not delivering its full
// function, so a component that was never provisioned is not reported with the
// same word as one that is broken. An install with no embeddings provider is
// INCOMPLETE, not faulty: naming it is honest (the doctrine in
// docs-site start/honesty-and-limits.md — a capability without its provider is
// never presented as working), but calling it a fault is not.
//
// The zero value is deliberately NOT the benign classification: a status that
// does not state its posture is rendered as a fault (deny-closed). Forgetting to
// set this field can only ever cost a louder signal, never a quieter one.
type CapabilityPosture string

const (
	// PostureUnstated is the zero value: the source did not classify itself, so
	// consumers must treat the capability as impaired.
	PostureUnstated CapabilityPosture = ""
	// PostureReady — provisioned and delivering its full function.
	PostureReady CapabilityPosture = "ready"
	// PostureNotConfigured — no provider was supplied at all. This is the
	// product's deliberate DEFAULT posture for optional capabilities, reached by
	// installing correctly and configuring nothing; it is not a malfunction.
	PostureNotConfigured CapabilityPosture = "not_configured"
	// PostureImpaired — the capability is provisioned (or was asked for) but is
	// not delivering: a half-written provider block, a policy denial, an
	// unreadable gate, an unwired seam. This is a fault an operator must fix.
	PostureImpaired CapabilityPosture = "impaired"
)

// KnowledgeStatus is the non-secret runtime posture of the knowledge plane. The
// composition root owns the decision because it wires the embedder provider and the
// model-access gate; core/api only renders the already-redacted summary.
type KnowledgeStatus struct {
	EmbedderKind        string                    `json:"embedder_kind"`      // semantic | local-hash
	RetrievalSemantic   bool                      `json:"retrieval_semantic"` // false means lexical/degraded
	Reason              string                    `json:"reason,omitempty"`   // short, non-sensitive posture code
	Posture             CapabilityPosture         `json:"posture,omitempty"`  // classifies Reason: ready | not_configured | impaired
	GuardProfile        string                    `json:"guard_profile"`      // acl_aware unless downgraded KBs exist
	GuardWarning        string                    `json:"guard_warning,omitempty"`
	GuardDowngradeCount int                       `json:"guard_downgrade_count,omitempty"`
	GuardPublicOnlyKBs  []KnowledgeGuardDowngrade `json:"guard_public_only_kbs,omitempty"`
}

// KnowledgeGuardDowngrade names one active KB-level downgrade from ACL-aware
// retrieval to public-only. It is intended for authenticated operator views.
type KnowledgeGuardDowngrade struct {
	TenantID   string `json:"tenant_id,omitempty"`
	TenantSlug string `json:"tenant_slug,omitempty"`
	KBName     string `json:"kb_name"`
	Profile    string `json:"profile"`
	Reason     string `json:"reason,omitempty"`
	UpdatedBy  string `json:"updated_by,omitempty"`
}

// KnowledgeStatusProvider supplies the current process-level knowledge posture.
type KnowledgeStatusProvider interface {
	KnowledgeStatus(ctx context.Context) KnowledgeStatus
}

// SourceApplyResult is the outcome of applying ONE source change to the live
// engine after it was persisted. Persisted is always true on a successful write;
// Applied reports whether the LIVE swap also succeeded. When a write persists but
// the live apply is rejected, Note carries the honest reason — the durable row is
// correct and the change takes effect on the next reload/restart (the PDP
// publish-then-activate honesty).
type SourceApplyResult struct {
	Name      string `json:"name"`
	Action    string `json:"action"` // "added" | "rotated" | "removed" | "disabled" | "unchanged"
	Persisted bool   `json:"persisted"`
	Applied   bool   `json:"applied"`
	Note      string `json:"note,omitempty"`
}

// SourceReloadReport is the result of a full roster reconcile.
type SourceReloadReport struct {
	Added     []string          `json:"added,omitempty"`
	Removed   []string          `json:"removed,omitempty"`
	Rotated   []string          `json:"rotated,omitempty"`
	Unchanged int               `json:"unchanged"`
	Rejected  []SourceRejection `json:"rejected,omitempty"`
	// RequiresRestart names the configuration domains this live reload does NOT
	// cover — changes to them are read once at boot and need a restart. Stated
	// every time so the operator is never misled that a reload applied them.
	RequiresRestart []string `json:"requires_restart,omitempty"`
}

// SourceRejection is one source the reconciler could not apply, with the reason.
type SourceRejection struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}
