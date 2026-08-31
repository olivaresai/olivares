// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file adds the read-only Admin-API GOVERNANCE surfaces (ANT2-04/05/06): the
// External Keys / CMEK inventory, the read-only Rate Limits inventory, and the
// governance fields of the Workspace object (data-residency, CMEK reference,
// compartment, tags, home geo). It is strictly read-only and minimal-data: it GETs
// inventory metadata and REFERENCES (ekey_ ids, never key material; tag labels, never
// secrets) and emits posture FindingReports (a workspace without a CMEK; the rate
// limits a gateway must mirror; an Admin-API surface that does not exist). Per
// §5 the surface honesty is honored: a workspace's created_by lineage is NOT asserted
// here (uncertain until verified against apikeys/list), and on a surface without the
// Admin API the connector degrades HONESTLY (a documented posture finding), never a
// fabricated empty inventory.
//
// Authority (verbatim, jun-2026): …/api/admin/external_keys (ANT2-04);
// …/manage-claude/rate-limits-api (ANT2-05); …/admin-api/workspaces/update-workspace
// (ANT2-06, the governance object read read-only here).
package claudeapi

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Admin-API governance endpoint paths (ANT2-04/05).
const (
	externalKeysPath = "/v1/organizations/external_keys"
	rateLimitsPath   = "/v1/organizations/rate_limits"
)

// FindingReport subjects for the governance posture findings.
const (
	subjectWorkspace  = "anthropic.workspace"
	subjectRateLimit  = "anthropic.rate_limit"
	subjectSurface    = "anthropic.surface"
	subjectAPIKey     = "anthropic.api_key"
	subjectSpendLimit = "anthropic.spend_limit"
)

// keyExpiryWarnWindow is how far ahead an active key's pending expiry is flagged so an
// operator rotates BEFORE the key lapses (key-lifecycle governance).
const keyExpiryWarnWindow = 14 * 24 * time.Hour

// knownBetaHeaders is the VERIFIED inventory of anthropic-beta HEADER VALUES the
// catalog recognizes (ANT2-16). Cites "~26" beta headers; rather than
// fabricate a list to hit a count, this models exactly the ones verified against
// primary docs / the codebase (jun-2026; +ce-user-management/mcp-tunnels VERIFIED
// 2026-07-20 against platform.claude.com). It is the feature-flag inventory the
// governance view shows; the authoritative live enum is the Models API / docs page —
// this is the verified subset, AsOf-stamped, not a fabricated full set.
//
// These are anthropic-beta HEADER strings ONLY. The 2026 reconciliation (D1-D8)
// removed entries that were dated tool-TYPE / context-edit identifiers, NOT headers —
// "advisor_20260301" (the advisor tool's "type"; its header is advisor-tool-2026-03-01,
// BetaAdvisorTool), "code_execution_20260120" (a tool "type"; current code execution is
// GA, no header), "agent_toolset_20260401" (Managed Agents tool "type"), and
// "clear_thinking_20251015" (a context-edit "type" under BetaContextManagement). The
// dated tool-TYPE identifiers live in their own catalog (servertools.go + the AGPL
// modules/models tool-type table), not in the header inventory. Sending one of them as
// an anthropic-beta header would be wrong — hence the correction.
var knownBetaHeaders = []string{
	filesBetaHeader,           // files-api-2025-04-14 (Files API)
	MCPBetaHeader,             // mcp-client-2025-11-20 (current Messages-API MCP connector)
	MCPBetaHeaderDeprecated,   // mcp-client-2025-04-04 (deprecated inline tool_configuration)
	fastModeBeta,              // fast-mode-2026-02-01 (usage-report speeds[] filter / fast mode, ANT2-02)
	BetaAdvisorTool,           // advisor-tool-2026-03-01 (advisor tool, D1)
	BetaTaskBudgets,           // task-budgets-2026-03-13 (Task Budgets, D4)
	BetaCompaction,            // compact-2026-01-12 (server-side compaction, D5)
	BetaMidConversationSystem, // mid-conversation-system-2026-04-07 (operator channel, D3)
	BetaContextManagement,     // context-management-2025-06-27 (context-editing strategies)
	BetaServerSideFallback,    // server-side-fallback-2026-06-01 (fallbacks chain)
	BetaFallbackCredit,        // fallback-credit-2026-06-01 (credit retry)
	betaCEUserManagement,      // ce-user-management-2026-07-13 (CE RBAC groups/custom-roles)
	betaMCPTunnels,            // mcp-tunnels-2026-06-22 (MCP Tunnels management API)
}

// betaCEUserManagement gates the Claude Enterprise user-management RBAC groups and
// custom-roles endpoints (VERIFIED 2026-07-20 against platform.claude.com; release
// note dated 2026-07-14). The member/invite endpoints take NO beta header — only the
// rbac_groups + rbac_roles requests require it (omitting it → 404). It is the header
// the claude-console connector sends on those reads and the group-membership actuator
// (adminactions.go) sends on its writes.
const betaCEUserManagement = "ce-user-management-2026-07-13"

// betaMCPTunnels gates the MCP Tunnels management API. VERIFIED 2026-07-20: on
// 2026-06-22 the management API MOVED from /v1/organizations/tunnels (Admin API) to
// /v1/tunnels (Claude API) under this header + the workspace:manage_tunnels WIF scope;
// the legacy Admin-API path remains during a migration window. We inventory the header
// (research-preview surface); we do not yet actuate tunnels.
const betaMCPTunnels = "mcp-tunnels-2026-06-22"

// KnownBetaHeaders returns the verified anthropic-beta header inventory (a copy, so a
// caller cannot mutate the package state). It is the feature-flag inventory
// render; it is the verified SUBSET of the documented ~26, never a fabricated full set.
func KnownBetaHeaders() []string {
	return append([]string(nil), knownBetaHeaders...)
}

// fetchExternalKeys lists the org's customer-managed encryption keys as inventory
// metadata (ANT2-04). It is read-only and carries NO key material: only the ekey_
// reference, the cloud-KMS provider TYPE, the validation state and timestamps.
func (s *Source) fetchExternalKeys(ctx context.Context) ([]modelprovider.ExternalKeyRef, error) {
	var out []modelprovider.ExternalKeyRef
	after := ""
	for i := 0; i < s.maxPages; i++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var resp externalKeysResponse
		q := url.Values{"limit": {"100"}}
		if after != "" {
			q.Set("after_id", after)
		}
		if err := s.client.GetJSON(ctx, externalKeysPath, q, &resp); err != nil {
			return nil, err
		}
		for _, k := range resp.Data {
			out = append(out, modelprovider.ExternalKeyRef{
				ID:              k.ID,
				Provider:        k.ProviderConfig.Type,
				Name:            k.Name,
				State:           k.Status,
				InUse:           k.InUse,
				LastValidatedAt: parseTime(k.LastValidatedAt),
				CreatedAt:       parseTime(k.CreatedAt),
			})
		}
		if !resp.HasMore || resp.LastID == "" {
			break
		}
		after = resp.LastID
	}
	return out, nil
}

// ExternalKeys returns the read-only External Keys / CMEK inventory (ANT2-04) so a
// module can surface it as CONSULTABLE posture — this is the exported accessor over
// fetchExternalKeys, added in because the data was already gathered into the
// catalog but nothing could serve it, so the identity console's typed client got a 404.
// It is read-only and minimal-data: an ekey_ REFERENCE, the cloud-KMS provider type and
// the validation state — never key material (the type has no field that could hold it).
// With no Admin credential it returns an empty inventory (the offline posture), never an
// error for "unconfigured" — so the caller distinguishes "not wired" from a transient
// fault. Same contract as RateLimits below.
func (s *Source) ExternalKeys(ctx context.Context) ([]modelprovider.ExternalKeyRef, error) {
	if s.adminKey == "" || s.client == nil {
		return nil, nil
	}
	return s.fetchExternalKeys(ctx)
}

// Workspaces returns the read-only workspace GOVERNANCE objects (ANT2-06): home geo,
// CMEK reference, compartment, tags and the data-residency policy. Exported accessor
// over fetchWorkspaces, same contract as ExternalKeys/RateLimits. ARCHIVED workspaces
// are carried through with their Archived flag set rather than dropped here — whether an
// archived workspace belongs in a given view is the CALLER's judgement, and a filter
// buried in the connector would silently change every consumer at once.
func (s *Source) Workspaces(ctx context.Context) ([]modelprovider.WorkspaceRef, error) {
	if s.adminKey == "" || s.client == nil {
		return nil, nil
	}
	return s.fetchWorkspaces(ctx)
}

// fetchRateLimits lists the read-only rate-limit inventory (ANT2-05). It reads the
// org-level groups plus per-workspace OVERRIDES for every non-archived workspace. The
// workspace list is already bounded by fetchWorkspaces' maxPages loop; one
// rate-limits call per active workspace is acceptable for the daily governance gather.
// Workspace endpoint absence means INHERIT the org value, not unlimited. The limits
// are inventory a gateway/proxy must keep in sync — never a control this connector
// mutates. Managed Agents are NOT covered by this API (caveat, not a zero).
func (s *Source) fetchRateLimits(ctx context.Context, workspaces []modelprovider.WorkspaceRef) ([]modelprovider.RateLimitRef, error) {
	out, err := s.fetchRateLimitsAt(ctx, rateLimitsPath, "")
	if err != nil {
		return nil, err
	}
	for _, w := range workspaces {
		if w.Archived {
			continue
		}
		wsLimits, err := s.fetchRateLimitsAt(ctx, "/v1/organizations/workspaces/"+w.ID+"/rate_limits", w.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, wsLimits...)
	}
	return out, nil
}

// RateLimits returns the read-only Rate Limits inventory (ANT2-05) so module X can
// surface it as a CONSULTABLE inventory a gateway/proxy must mirror (Managed Agents
// are NOT covered). It is the exported accessor over fetchRateLimits: read-only and
// minimal-data (refs/labels + numeric ceilings, never key material). With no Admin
// credential it returns an empty inventory (the offline posture), never an error for
// "unconfigured" — so the caller distinguishes "not wired" from a transient fault.
func (s *Source) RateLimits(ctx context.Context) ([]modelprovider.RateLimitRef, error) {
	if s.adminKey == "" || s.client == nil {
		return nil, nil
	}
	workspaces, err := s.fetchWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	return s.fetchRateLimits(ctx, workspaces)
}

// fetchRateLimitsAt reads one rate-limits endpoint (org or per-workspace), stamping
// the workspace ref on each returned limit.
func (s *Source) fetchRateLimitsAt(ctx context.Context, path, workspaceRef string) ([]modelprovider.RateLimitRef, error) {
	var out []modelprovider.RateLimitRef
	page := ""
	for i := 0; i < s.maxPages; i++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var resp rateLimitsResponse
		q := url.Values{}
		if page != "" {
			q.Set("page", page)
		}
		if err := s.client.GetJSON(ctx, path, q, &resp); err != nil {
			return nil, err
		}
		for _, r := range resp.Data {
			limits := make([]modelprovider.RateLimitValue, 0, len(r.Limits))
			for _, lim := range r.Limits {
				value := modelprovider.RateLimitValue{
					Type:  lim.Type,
					Value: lim.Value,
				}
				if lim.OrgLimit != nil {
					value.OrgLimit = *lim.OrgLimit
				}
				limits = append(limits, value)
			}
			out = append(out, modelprovider.RateLimitRef{
				WorkspaceRef: workspaceRef,
				GroupType:    r.GroupType,
				Models:       r.Models,
				Limits:       limits,
			})
		}
		if resp.NextPage == "" {
			break
		}
		page = resp.NextPage
	}
	return out, nil
}

// surface returns the connector's deployment surface record (defaulting to direct).
func (s *Source) surface() Surface {
	g := s.gateway
	if g == "" {
		g = model.GatewayDirect
	}
	if sf, ok := SurfaceFor(g); ok {
		return sf
	}
	// A third-party gateway with no modeled surface: assume nothing applies but
	// inference (fail-closed), so governance ingest degrades honestly rather than
	// 404-spamming an Admin API that may not exist.
	return Surface{Gateway: g, DisplayName: string(g), APIs: APISupport{Messages: true}}
}

// gatherGovernance emits the read-only governance posture findings (ANT2-04/05/06):
// a Medium finding per workspace that has no CMEK; an Info finding summarizing the
// rate limits a gateway/proxy must mirror (and the Managed-Agents caveat); and — when
// the connector is pointed at a surface WITHOUT the Admin API but an admin key is set
// — a Medium "ingest degraded" finding (honest degradation, never a fabricated empty
// inventory). It runs only when an Admin credential is configured.
func (s *Source) gatherGovernance(ctx context.Context, sink sdk.Sink) error {
	if s.adminKey == "" || s.client == nil {
		return nil
	}
	at := s.clock().UTC()
	sf := s.surface()

	// On a surface without the Admin API (Bedrock Mantle/legacy, Vertex, Foundry),
	// the Admin-API governance ingest is structurally impossible — say so once, do not
	// poll an endpoint that does not exist.
	if !sf.Supports("admin") {
		return sink.Emit(ctx, model.FindingReport{
			Kind:        "posture",
			Severity:    model.SeverityMedium,
			SubjectKind: subjectSurface,
			SubjectRef:  string(sf.Gateway),
			Title:       "Admin-API governance ingest unavailable on surface " + sf.DisplayName,
			DetailHash:  redact.Hash("surface " + string(sf.Gateway) + " exposes no Anthropic Admin API; External Keys/Rate Limits/Workspace governance cannot be read here — governed by " + sf.Operator),
			OccurredAt:  at,
		})
	}

	// Workspaces without a CMEK (ANT2-06 posture). Read the governance object and flag
	// any active workspace with no external_key_id, so a regulated estate sees the gap.
	workspaces, err := s.fetchWorkspaces(ctx)
	if err != nil {
		return err
	}
	for _, w := range workspaces {
		if w.Archived || w.ExternalKeyID != "" {
			continue
		}
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        "governance",
			Severity:    model.SeverityMedium,
			SubjectKind: subjectWorkspace,
			SubjectRef:  w.ID,
			Title:       "Workspace has no customer-managed encryption key (CMEK)",
			DetailHash:  redact.Hash("workspace " + w.ID + " has no external_key_id; encryption is provider-managed (ANT2-06)"),
			OccurredAt:  at,
		}); err != nil {
			return err
		}
	}

	// Workspace spend-limit actuation gap (verified jun-2026 against
	// platform.claude.com). The Admin API has NO endpoint to SET or CLEAR a workspace
	// spend limit: Update Workspace accepts only data_residency/external_key_id/name/tags,
	// and the Rate Limits API is read-only; spend limits are configured Console-only. So a
	// FinOps "budget exhausted" backstop CANNOT push a spend cap upstream by API — the
	// only API-actuable upstream hard caps are per-key deactivate/archive (recoverable)
	// and workspace archive (irreversible, revokes all keys in the workspace). This Info
	// finding records the gap honestly in the governance ledger so an operator does not
	// expect an API spend cap the provider does not expose (and the connector never POSTs
	// to a fabricated endpoint).
	if err := sink.Emit(ctx, model.FindingReport{
		Kind:        "governance",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectSpendLimit,
		SubjectRef:  "organization",
		Title:       "Workspace spend-limit cannot be set via the Admin API (Console-only) — backstop is key/workspace actuation",
		DetailHash:  redact.Hash("Anthropic Admin API exposes no set/clear workspace spend-limit endpoint (Update Workspace fields: data_residency/external_key_id/name/tags; Rate Limits API read-only; spend limits Console-only); API-actuable upstream caps = api_key deactivate/archive (recoverable) + workspace archive (irreversible, revokes all keys) — the FinOps defense-in-depth backstop"),
		OccurredAt:  at,
	}); err != nil {
		return err
	}

	// Rate limits a gateway/proxy must mirror (ANT2-05). One Info finding summarizing
	// the count, with the documented Managed-Agents caveat (NOT covered by this API).
	limits, err := s.fetchRateLimits(ctx, workspaces)
	if err != nil {
		return err
	}
	overridesByWorkspace := map[string]int{}
	for _, rl := range limits {
		if rl.WorkspaceRef != "" {
			overridesByWorkspace[rl.WorkspaceRef]++
		}
	}
	for _, w := range workspaces {
		if w.Archived || overridesByWorkspace[w.ID] > 0 {
			continue
		}
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        "governance",
			Severity:    model.SeverityInfo,
			SubjectKind: subjectWorkspace,
			SubjectRef:  w.ID,
			Title:       "Workspace has no rate-limit overrides — draws from the shared organization pool",
			DetailHash:  redact.Hash("workspace_rate_limit_overrides workspace_id=" + w.ID + " workspace_name=" + w.Name + "; no overrides reported; inherits org limits; NOT unlimited; overrides are Console-only"),
			OccurredAt:  at,
		}); err != nil {
			return err
		}
	}
	return sink.Emit(ctx, model.FindingReport{
		Kind:        "governance",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectRateLimit,
		SubjectRef:  "organization",
		Title:       fmt.Sprintf("%d rate-limit group(s) a gateway/proxy must keep in sync", len(limits)),
		DetailHash:  redact.Hash("rate_limit_groups count=" + strconv.Itoa(len(limits)) + "; gateways/proxies must mirror; workspace absence means inherit org limits, NOT unlimited; Managed Agents NOT covered by the Rate Limits API (ANT2-05)"),
		OccurredAt:  at,
	})
}

// gatherKeyLifecycle emits API-key hygiene posture findings (key-lifecycle/
// rotation governance). For each ACTIVE key it flags the two API-observable rotation
// gaps: a key with NO expiry (the long-lived-credential gap rotation governance
// targets), and a key whose expiry is imminent (rotate before it lapses). It is
// read-only and minimal-data — it reasons over the key REFERENCE and lifecycle
// timestamps the inventory already carries, never the secret (the API returns only a
// masked hint). Key CREATION and the secret are Console-only (the Admin API can only
// update an existing key's status), so the connector governs rotation by FLAGGING the
// hygiene gap here and ACTING on it through the governed deactivate seam (adminactions.go),
// never by minting a key. It runs only with an Admin credential on an Admin surface.
func (s *Source) gatherKeyLifecycle(ctx context.Context, sink sdk.Sink) error {
	if s.adminKey == "" || s.client == nil {
		return nil
	}
	keys, err := s.fetchKeys(ctx)
	if err != nil {
		return err
	}
	at := s.clock().UTC()
	for _, k := range keys {
		if k.Status != "active" {
			continue // inactive/archived keys are not a live rotation surface
		}
		var title, detail string
		principal := k.PrincipalType
		if principal == "" {
			principal = "unknown"
		}
		switch {
		case k.ExpiresAt.IsZero():
			title = "Active API key has no expiry — set an expiry and rotate (lifecycle gap)"
			detail = "api_key " + k.ID + " ws=" + k.WorkspaceRef + " status=active principal=" + principal + " expires_at=none; long-lived credential, no rotation deadline"
		case k.ExpiresAt.Sub(at) <= keyExpiryWarnWindow:
			title = "Active API key expires within " + keyExpiryWarnWindow.String() + " — rotate before it lapses"
			detail = "api_key " + k.ID + " ws=" + k.WorkspaceRef + " expires_at=" + k.ExpiresAt.Format(time.RFC3339) + ""
		default:
			continue // an active key with a comfortably-future expiry is healthy
		}
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        "governance",
			Severity:    model.SeverityMedium,
			SubjectKind: subjectAPIKey,
			SubjectRef:  k.ID,
			Title:       title,
			DetailHash:  redact.Hash(detail),
			OccurredAt:  at,
		}); err != nil {
			return err
		}
	}
	return nil
}

// betaHeaderInventoryFinding records the recognized anthropic-beta feature-flag
// inventory once per gather, so the governance ledger carries which experimental
// surfaces the catalog models (ANT2-16). Emitted only on a surface with the Models API
// (the source-of-truth surface); on a gateway it is a no-op (those surfaces have no
// Models API and the offline catalog is used).
func (s *Source) betaHeaderInventoryFinding(at time.Time) (model.FindingReport, bool) {
	if !s.surface().Supports("models") {
		return model.FindingReport{}, false
	}
	return model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectSurface,
		SubjectRef:  string(s.surface().Gateway),
		Title:       fmt.Sprintf("%d anthropic-beta feature flags recognized", len(knownBetaHeaders)),
		DetailHash:  redact.Hash("beta_headers=" + fmt.Sprint(knownBetaHeaders)),
		OccurredAt:  at,
	}, true
}
