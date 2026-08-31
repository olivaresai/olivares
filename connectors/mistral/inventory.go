// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// inventory.go implements the HONEST coverage caveat and the OPT-IN, UNVERIFIED-OFFLINE
// org/workspace + API-key inventory seam.
//
// Mistral exposes NO public REST shape for its Admin/Management API (org/workspace/key/
// member management is documented only narratively) and NO public usage/billing/spending-
// cap API (dashboard-only). Rather than fabricate either, the connector:
//
//   - ALWAYS (when credentialed) emits an honest coverage caveat: cost/usage/spending-cap
//     are dashboard-only with no public API, so cost is metered around the inference path
//     (Meter) and the catalog is the live, real surface.
//   - Only when the operator opts in (manage_inventory) attempts the workspace/key
//     inventory at operator-overridable paths, modeling Mistral's own {object:"list",
//     data:[…]} list convention. A 403/404 (not entitled / path differs on this tenant /
//     surface not public) degrades to an honest "ingest unavailable" posture finding and
//     returns nil — never a hard failure, never a fabricated empty inventory. An over-age
//     inventoried key yields a rotation-posture finding (the key-lifecycle control point,
//     as in connectors/fal). Issuing/rotating a key is a MUTATION and is out of scope for
//     this read-first source (HITL-gated).
package mistral

import (
	"context"
	"fmt"
	"net/url"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Finding subjects for the Mistral governance posture findings.
const (
	subjectCoverage  = "mistral.coverage"
	subjectInventory = "mistral.inventory"
	subjectAPIKey    = "mistral.api_key"
	subjectWorkspace = "mistral.workspace"
)

// coverageCaveat is the honest coverage caveat (ARCHITECTURE.md, the directory's honesty bar):
// Mistral exposes no public usage/billing/spending-cap API, so the control plane meters
// cost around the inference path (Meter) and reads the live catalog — but does NOT claim
// usage/cost/cap observability it cannot perform. It mirrors connectors/fal's sales-gated
// caveat: a single Info posture finding, emitted every credentialed gather.
func (s *Source) coverageCaveat() model.FindingReport {
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectCoverage,
		SubjectRef:  "mistral",
		Title:       "Mistral usage/billing/spending-cap is dashboard-only; no public API (catalog is live, cost is metered around inference)",
		DetailHash:  redact.Hash("mistral: GET /v1/models is the only verified programmatic surface; per-workspace usage, billed cost and the monthly spending cap are Admin-Panel only with no published REST endpoint (confirmed vs docs/API-ref/OpenAPI/SDK); cost is metered via Meter (estimated from list pricing), the monthly spending cap cannot be read programmatically"),
		OccurredAt:  s.clock().UTC(),
	}
}

// gatherInventoryPosture lists the workspaces + API keys (opt-in, UNVERIFIED-OFFLINE) and
// emits an inventory finding per object plus a rotation-posture finding for any key older
// than key_max_age. On an unavailable surface (403/404) it degrades to a single honest
// posture finding and returns nil.
func (s *Source) gatherInventoryPosture(ctx context.Context, sink sdk.Sink) error {
	ws, err := s.listWorkspaces(ctx)
	if err != nil {
		if isUnavailable(err) {
			return sink.Emit(ctx, s.inventoryUnavailableFinding("workspace inventory", s.workspacesPath))
		}
		return err
	}
	for _, w := range ws {
		if err := sink.Emit(ctx, s.workspaceFinding(w)); err != nil {
			return err
		}
	}

	keys, err := s.listKeys(ctx)
	if err != nil {
		if isUnavailable(err) {
			return sink.Emit(ctx, s.inventoryUnavailableFinding("API-key inventory", s.keysPath))
		}
		return err
	}
	now := s.clock().UTC()
	for _, k := range keys {
		created := parseTime(k.CreatedAt)
		if created.IsZero() || created.After(now) {
			continue // cannot assess age without a sane creation time; do not guess
		}
		age := now.Sub(created)
		if age <= s.keyMaxAge {
			continue
		}
		days := int(age.Hours() / 24)
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        "governance",
			Severity:    model.SeverityMedium,
			SubjectKind: subjectAPIKey,
			SubjectRef:  firstNonEmpty(k.ID, k.Name),
			Title:       fmt.Sprintf("Mistral API key past rotation age (%d days old)", days),
			DetailHash:  redact.Hash(fmt.Sprintf("mistral key id=%s name=%q workspace=%q age_days=%d threshold_days=%d; rotate (key-lifecycle control point)", k.ID, k.Name, k.WorkspaceID, days, int(s.keyMaxAge.Hours()/24))),
			OccurredAt:  now,
		}); err != nil {
			return err
		}
	}
	return nil
}

// workspaceFinding records one inventoried workspace as an inventory finding (metadata
// only; non-sensitive). The name is folded into the hash, the id is the subject ref.
func (s *Source) workspaceFinding(w workspaceEntry) model.FindingReport {
	occurred := parseTime(w.CreatedAt)
	if occurred.IsZero() {
		occurred = s.clock().UTC()
	}
	return model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectWorkspace,
		SubjectRef:  firstNonEmpty(w.ID, w.Name),
		Title:       "Mistral workspace inventoried",
		DetailHash:  redact.Hash(fmt.Sprintf("mistral workspace id=%s name=%q", w.ID, w.Name)),
		OccurredAt:  occurred,
	}
}

// inventoryUnavailableFinding is the honest degrade when the UNVERIFIED-OFFLINE inventory
// surface returns 403/404 (not entitled / path differs on this tenant / Admin API not
// public). It records WHICH surface and the path tried, so an operator can correct the
// path or obtain entitlement — never a fabricated empty inventory.
func (s *Source) inventoryUnavailableFinding(surface, path string) model.FindingReport {
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityMedium,
		SubjectKind: subjectInventory,
		SubjectRef:  surface,
		Title:       "Mistral " + surface + " unavailable (Admin API not published / not entitled / path differs — UNVERIFIED-OFFLINE)",
		DetailHash:  redact.Hash("mistral inventory surface=" + surface + " path=" + path + " base=" + s.baseURL + " returned 403/404; Mistral publishes no concrete REST shape for org/workspace/key inventory — set the correct path for your tenant or manage via the Admin Panel"),
		OccurredAt:  s.clock().UTC(),
	}
}

// listWorkspaces paginates the UNVERIFIED-OFFLINE workspace-list endpoint. Cursor
// pagination via after_id + has_more (Mistral's {object:"list",data:[…]} convention),
// bounded by max_pages.
func (s *Source) listWorkspaces(ctx context.Context) ([]workspaceEntry, error) {
	var out []workspaceEntry
	after := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var resp workspacesResponse
		q := url.Values{}
		if after != "" {
			q.Set("after_id", after)
		}
		if err := s.client.GetJSON(ctx, s.workspacesPath, q, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Data...)
		if !resp.HasMore || resp.LastID == "" {
			break
		}
		after = resp.LastID
	}
	return out, nil
}

// listKeys paginates the UNVERIFIED-OFFLINE API-key-list endpoint. The API must return
// only masked key metadata, never a secret (this connector has no field that could hold a
// usable credential — apiKeyEntry mirrors that). Cursor pagination via after_id + has_more.
func (s *Source) listKeys(ctx context.Context) ([]apiKeyEntry, error) {
	var out []apiKeyEntry
	after := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var resp apiKeysResponse
		q := url.Values{}
		if after != "" {
			q.Set("after_id", after)
		}
		if err := s.client.GetJSON(ctx, s.keysPath, q, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Data...)
		if !resp.HasMore || resp.LastID == "" {
			break
		}
		after = resp.LastID
	}
	return out, nil
}
