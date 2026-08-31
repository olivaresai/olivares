// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"
	"strings"

	"github.com/olivaresai/olivares/connectors/agentcore"
	"github.com/olivaresai/olivares/modules/governance"
)

// agentcoreexportwiring.go is the composition-root bridge for the AgentCore
// Cedar exporter. It is opt-in and fail-closed: OLIVARES_AGENTCORE_EXPORT_CONFIG
// names an operator-secret JSON file carrying AWS write credentials by value
// (never in the store), per-tenant allowlists and mapping tables. Missing config
// means no exporters and governance returns 501; malformed or unusable tenant
// entries are visible warnings and skipped.

const agentCoreExportConfigEnv = "OLIVARES_AGENTCORE_EXPORT_CONFIG"

type agentCoreExportConfig struct {
	Tenants []agentCoreExportTenantConfig `json:"tenants"`
}

type agentCoreExportTenantConfig struct {
	Tenant          string                       `json:"tenant"`
	Region          string                       `json:"region"`
	AccountID       string                       `json:"account_id,omitempty"`
	AccessKeyID     string                       `json:"access_key_id"`
	SecretAccessKey string                       `json:"secret_access_key"`
	SessionToken    string                       `json:"session_token,omitempty"`
	Endpoint        string                       `json:"endpoint,omitempty"`
	PolicyEngineID  string                       `json:"policy_engine_id"`
	EnforcementMode string                       `json:"enforcement_mode,omitempty"`
	Allowlist       []string                     `json:"allowlist"`
	Mapping         agentCoreExportMappingConfig `json:"mapping"`
}

type agentCoreExportMappingConfig struct {
	WorkspaceGateways map[string][]string                          `json:"workspace_gateways"`
	SubjectClaims     map[string]agentCoreExportClaimBindingConfig `json:"subject_claims"`
	PermActions       map[string][]string                          `json:"perm_actions"`
	ModelActions      map[string][]string                          `json:"model_actions"`
	SourceActions     map[string][]string                          `json:"source_actions"`
	SourceReadActions map[string][]string                          `json:"source_read_actions"`
}

type agentCoreExportClaimBindingConfig struct {
	Tag   string `json:"tag"`
	Value string `json:"value"`
}

func loadAgentCoreExportConfig(getenv func(string) string, _ *slog.Logger) (agentCoreExportConfig, error) {
	path := strings.TrimSpace(getenv(agentCoreExportConfigEnv))
	if path == "" {
		return agentCoreExportConfig{}, nil
	}
	var cfg agentCoreExportConfig
	if err := loadOperatorJSONConfig(agentCoreExportConfigEnv, path, &cfg); err != nil {
		return agentCoreExportConfig{}, err
	}
	return cfg, nil
}

func newAgentCoreExportBinding(cfg agentCoreExportConfig, bridge *approvalBridge, providers []governance.AgentCoreExportProvider, log *slog.Logger) (governance.AgentCoreExportBinding, bool) {
	var out governance.AgentCoreExportBinding
	out.Providers = append([]governance.AgentCoreExportProvider(nil), providers...)
	for _, tc := range cfg.Tenants {
		tid, present, err := parseBusinessTenant("agentcore-export config: tenant", tc.Tenant)
		if err != nil || !present {
			log.Warn("agentcore-export: tenant entry has an invalid tenant id; skipped", "tenant", tc.Tenant)
			continue
		}
		if strings.TrimSpace(tc.PolicyEngineID) == "" {
			log.Warn("agentcore-export: tenant entry has no policy_engine_id; skipped", "tenant", tc.Tenant)
			continue
		}
		if strings.TrimSpace(tc.Region) == "" && strings.TrimSpace(tc.Endpoint) == "" {
			log.Warn("agentcore-export: tenant entry has no region or endpoint; skipped", "tenant", tc.Tenant)
			continue
		}
		if strings.TrimSpace(tc.AccessKeyID) == "" || strings.TrimSpace(tc.SecretAccessKey) == "" {
			log.Warn("agentcore-export: tenant entry has no AWS write credential; skipped", "tenant", tc.Tenant)
			continue
		}
		var gate *agentCoreExportApprovalAdapter
		if bridge != nil {
			gate = &agentCoreExportApprovalAdapter{b: bridge, tenant: tid}
		}
		exporter, err := agentcore.NewExporter(agentcore.ExporterConfig{
			Region:          tc.Region,
			AccountID:       tc.AccountID,
			AccessKeyID:     tc.AccessKeyID,
			SecretAccessKey: tc.SecretAccessKey,
			SessionToken:    tc.SessionToken,
			Endpoint:        tc.Endpoint,
			Allowlist:       agentcore.NewExportAllowlist(tc.Allowlist),
			Gate:            gate,
			Auditor:         slogAgentCoreExportAuditor{log: log},
		})
		if err != nil {
			log.Warn("agentcore-export: tenant exporter config invalid; skipped", "tenant", tc.Tenant, "err", err)
			continue
		}
		out.Tenants = append(out.Tenants, governance.AgentCoreExportTenantBinding{
			TenantRef:       tid.String(),
			Exporter:        &agentCoreExportRuntime{exporter: exporter, gate: gate},
			Mapping:         tc.Mapping.toAgentCoreMapping(),
			PolicyEngineID:  strings.TrimSpace(tc.PolicyEngineID),
			EnforcementMode: strings.TrimSpace(tc.EnforcementMode),
		})
	}
	if len(out.Tenants) == 0 {
		return governance.AgentCoreExportBinding{}, false
	}
	log.Info("agentcore-export: governed AgentCore Cedar exporter wired", "tenants", len(out.Tenants))
	return out, true
}

func (m agentCoreExportMappingConfig) toAgentCoreMapping() agentcore.ExportMapping {
	subjectClaims := make(map[string]agentcore.ClaimBinding, len(m.SubjectClaims))
	for key, v := range m.SubjectClaims {
		subjectClaims[key] = agentcore.ClaimBinding{Tag: v.Tag, Value: v.Value}
	}
	return agentcore.ExportMapping{
		WorkspaceGateways: copyStringSlices(m.WorkspaceGateways),
		SubjectClaims:     subjectClaims,
		PermActions:       copyStringSlices(m.PermActions),
		ModelActions:      copyStringSlices(m.ModelActions),
		SourceActions:     copyStringSlices(m.SourceActions),
		SourceReadActions: copyStringSlices(m.SourceReadActions),
	}
}

func copyStringSlices(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]string, len(in))
	for key, vals := range in {
		out[key] = append([]string(nil), vals...)
	}
	return out
}

type slogAgentCoreExportAuditor struct{ log *slog.Logger }

func (a slogAgentCoreExportAuditor) Record(_ context.Context, rec agentcore.ExportAuditRecord) {
	if a.log == nil {
		return
	}
	a.log.Info("agentcore-export: governed export decision",
		"tenant", rec.Tenant, "engine", rec.EngineID, "plan", rec.PlanHash,
		"allowed", rec.Allowed, "dual_control", rec.DualControl,
		"approvers", rec.ApproverCount, "approval_ref", rec.ApprovalRef,
		"creates", rec.Creates, "updates", rec.Updates, "deletes", rec.Deletes,
		"failed", rec.Failed, "requested_by", rec.RequestedBy, "reason", rec.Reason)
}
