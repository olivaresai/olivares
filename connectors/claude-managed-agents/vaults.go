// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudemanagedagents

import (
	"context"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// credentialExpiryWindow is how far ahead an mcp_oauth credential's expires_at is treated
// as "expiring soon" (a governance heads-up before the platform's refresh has to succeed).
const credentialExpiryWindow = 24 * time.Hour

// mcpServerResourceKind is the inventory resource-kind for an MCP server (matches
// modules/inventory rkMCPServer = "mcp.server"). A vault credential authorizes an agent to
// reach this external server, so binding the credential edge to it links the CMA vault into
// the MCP access topology rather than stranding it in a CMA-only namespace.
const mcpServerResourceKind = "mcp.server"

// Vault is a CMA credential vault (vlt_...). It is WORKSPACE-SCOPED: any API key in the
// workspace can reference it at session creation, so it is a lateral-movement surface. The
// connector ingests the vlt_ REFERENCE + metadata only, never credential material.
type Vault struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	WorkspaceID string `json:"workspace_id"` // not always present on the record; falls back to config
	ArchivedAt  string `json:"archived_at"`
	CreatedAt   string `json:"created_at"`
}

// archived reports whether the vault has been archived (secrets purged, record retained).
func (v Vault) archived() bool { return v.ArchivedAt != "" }

// VaultCredential is one credential in a vault (vcrd_...). Secret fields (token/
// access_token/refresh_token/client_secret) are WRITE-ONLY at the platform and never
// returned; the connector reads the vcrd_ REFERENCE, the credential TYPE and the bound
// mcp_server_url + expiry metadata only (docs/SECURITY-HARDENING.md).
type VaultCredential struct {
	ID         string `json:"id"`
	ArchivedAt string `json:"archived_at"`
	Auth       struct {
		Type         string `json:"type"` // mcp_oauth | static_bearer
		MCPServerURL string `json:"mcp_server_url"`
		ExpiresAt    string `json:"expires_at"` // mcp_oauth only; metadata, not a secret
	} `json:"auth"`
}

// listPage is the standard Anthropic list-pagination envelope (data + cursor). CMA list
// endpoints are "paginated, newest first"; after_id walks older pages.
type vaultPage struct {
	Data    []Vault `json:"data"`
	HasMore bool    `json:"has_more"`
	LastID  string  `json:"last_id"`
}

type credentialPage struct {
	Data    []VaultCredential `json:"data"`
	HasMore bool              `json:"has_more"`
	LastID  string            `json:"last_id"`
}

// fetchVaults lists the workspace's (non-archived) vaults, refs + metadata only.
func (c *client) fetchVaults(ctx context.Context) ([]Vault, error) {
	var out []Vault
	after := ""
	for i := 0; i < c.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		var page vaultPage
		if err := c.getJSON(ctx, "/v1/vaults", listQuery("after_id", after), &page); err != nil {
			return out, err
		}
		out = append(out, page.Data...)
		if !page.HasMore || page.LastID == "" {
			break
		}
		after = page.LastID
	}
	return out, nil
}

// fetchCredentials lists a vault's (non-archived) credentials, refs + type + bound server
// + expiry only.
func (c *client) fetchCredentials(ctx context.Context, vaultID string) ([]VaultCredential, error) {
	var out []VaultCredential
	after := ""
	path := "/v1/vaults/" + vaultID + "/credentials"
	for i := 0; i < c.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		var page credentialPage
		if err := c.getJSON(ctx, path, listQuery("after_id", after), &page); err != nil {
			return out, err
		}
		out = append(out, page.Data...)
		if !page.HasMore || page.LastID == "" {
			break
		}
		after = page.LastID
	}
	return out, nil
}

// vaultEdge places a vault under its workspace in the access map (workspace → vault). The
// workspace ref is the operator-configured workspace, or the literal "workspace" when none
// is configured (the vault is still workspace-scoped, just unnamed).
func vaultEdge(v Vault, workspaceRef string, at time.Time) model.EdgeObservation {
	return model.EdgeObservation{
		OriginKind:   originWorkspace,
		OriginRef:    labelRef(workspaceRef, "workspace"),
		ResourceKind: kindVault,
		ResourceRef:  redact.Clean(v.ID),
		Mode:         model.ModeRead,
		Source:       model.SignalCMA,
		Confidence:   model.ConfidenceAttributed,
		ObservedAt:   at,
	}
}

// vaultLateralFinding is the posture finding for a workspace-scoped vault: any API key in
// the workspace can reference it at session creation, so its third-party credentials are a
// lateral-movement surface (ASI03). It carries the vlt_ ref only.
func vaultLateralFinding(v Vault, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingPosture,
		Severity:    model.SeverityMedium,
		SubjectKind: kindVault,
		SubjectRef:  redact.Clean(v.ID),
		Title:       "Workspace-scoped CMA vault is a lateral-movement surface",
		DetailHash:  redact.Hash("vault=" + v.ID + " ws=" + v.WorkspaceID + " any workspace api key can reference this vault at session creation; its third-party credentials are reachable workspace-wide (CMA vaults)"),
		OWASPASI:    []string{asiIdentityAbuse},
		OccurredAt:  at,
	}
}

// credentialEdge binds a vault credential to the external MCP server it authorizes —
// the PERMITTED side of the diff: a credential is a DECLARED GRANT (the holder may
// reach the server), not an observation of traffic. It travels as model.SignalPolicy
// with the credential itself as the IDENTITY origin, so the access map materializes
// the credential Identity and lands the grant on the permitted side: a credential
// whose server is never observed in use surfaces as an unused grant (the
// over-provisioned-credential least-privilege signal). The carrying vault rides
// ToolRef for provenance.
//
// Corrections to the shape: (1) the edge was SignalCMA — mislabeling the
// grant as observed activity; (2) its origin was the vault (OriginKind
// "anthropic.vault"), which modules/access-map does not route (only agent/session/
// identity enter the graph) — the edge silently never became a graph edge at all.
// ok is false when the credential names no server (nothing to connect).
func credentialEdge(vaultID string, cred VaultCredential, at time.Time) (model.EdgeObservation, bool) {
	server := redact.SanitizeURL(cred.Auth.MCPServerURL)
	if server == "" || cred.ID == "" {
		return model.EdgeObservation{}, false
	}
	return model.EdgeObservation{
		OriginKind:   "identity",
		OriginRef:    redact.Clean(cred.ID),
		ResourceKind: mcpServerResourceKind,
		ResourceRef:  server,
		Mode:         model.ModeReadWrite, // the credential grants the holder read+write to the server
		Source:       model.SignalPolicy,
		Confidence:   model.ConfidenceAttributed,
		ToolRef:      redact.Clean(vaultID),
		ObservedAt:   at,
	}, true
}

// credentialInventoryEdge places the credential itself under its vault so module I
// inventories every vault credential as a first-class governed object (vcrd_ ref +
// type metadata only — never the secret, which is write-only at the platform). The
// vault origin is an inventory carrier (the access map does not route it; the resource
// side is what materializes). ok is false when the credential has no id.
func credentialInventoryEdge(vaultID string, cred VaultCredential, at time.Time) (model.EdgeObservation, bool) {
	if cred.ID == "" {
		return model.EdgeObservation{}, false
	}
	return model.EdgeObservation{
		OriginKind:   kindVault,
		OriginRef:    redact.Clean(vaultID),
		ResourceKind: kindVaultCred,
		ResourceRef:  redact.Clean(cred.ID),
		Mode:         model.ModeRead,
		Source:       model.SignalCMA,
		Confidence:   model.ConfidenceAttributed,
		ObservedAt:   at,
	}, true
}

// credentialFindings returns governance/posture findings for a vault credential: a
// static_bearer that never rotates, and an mcp_oauth credential that is expired or expiring
// inside the window. now is the reference time for the expiry comparison (injectable).
func credentialFindings(vaultID string, cred VaultCredential, now, at time.Time) []model.FindingReport {
	var out []model.FindingReport
	server := redact.SanitizeURL(cred.Auth.MCPServerURL)
	switch cred.Auth.Type {
	case "static_bearer":
		out = append(out, model.FindingReport{
			Kind:        findingPosture,
			Severity:    model.SeverityLow,
			SubjectKind: kindVaultCred,
			SubjectRef:  redact.Clean(cred.ID),
			Title:       "CMA static_bearer credential has no rotation/expiry",
			DetailHash:  redact.Hash("cred=" + cred.ID + " vault=" + vaultID + " server=" + server + " type=static_bearer; a fixed bearer never rotates and has no expiry — rotate via archive+recreate (CMA vaults)"),
			OWASPASI:    []string{asiIdentityAbuse},
			OccurredAt:  at,
		})
	case "mcp_oauth":
		if exp := parseTime(cred.Auth.ExpiresAt); !exp.IsZero() {
			if sev, label, ok := expiryVerdict(exp, now); ok {
				out = append(out, model.FindingReport{
					Kind:        findingGovernance,
					Severity:    sev,
					SubjectKind: kindVaultCred,
					SubjectRef:  redact.Clean(cred.ID),
					Title:       "CMA mcp_oauth credential " + label,
					DetailHash:  redact.Hash(fmt.Sprintf("cred=%s vault=%s server=%s type=mcp_oauth expires_at=%s now=%s; a failed refresh surfaces as vault_credential.refresh_failed (CMA vaults)", cred.ID, vaultID, server, exp.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))),
					OWASPASI:    []string{asiIdentityAbuse},
					OccurredAt:  at,
				})
			}
		}
	}
	return out
}

// expiryVerdict classifies an mcp_oauth credential's expires_at relative to now: expired
// (Medium) or expiring within the window (Low). ok is false when it is comfortably valid.
func expiryVerdict(exp, now time.Time) (model.Severity, string, bool) {
	switch {
	case !exp.After(now):
		return model.SeverityMedium, "is expired", true
	case exp.Before(now.Add(credentialExpiryWindow)):
		return model.SeverityLow, "expires within 24h", true
	default:
		return model.SeverityInfo, "", false
	}
}
