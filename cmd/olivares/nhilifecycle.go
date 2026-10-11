// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"log/slog"
	"strings"

	claudewif "github.com/olivaresai/olivares/connectors/claude-wif"
	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/vault"
	"github.com/olivaresai/olivares/modules/governance"
)

// nhilifecycle.go wires the governed NHI lifecycle: the per-(tenant,source)
// write-capable LifecycleActuators the module invokes once an approval is granted.
// The LifecycleGate adapter over the approval bridge (so rotation/offboarding
// inherit the CRITICAL two-person floor and break-glass) is
// internal/approvalbridge/lifecycle.go.
//
// Both degrade honestly: with no bridge the gate is the module's deny-closed
// default (no actuation proceeds); with no actuator config the module's rotation
// endpoints report "unavailable" and emit a coverage finding — never a fabricated
// rotation. The actuator credentials are WRITE-capable (a Vault token allowed to
// mint AppRole secret-ids / disable entities; an Anthropic admin key allowed to
// update key status) and live in the operator config, never the store, never logged.

// --- write-capable LifecycleActuators ----------------------------------------

// nhiActuatorsConfig is the operator's OPT-IN provisioning of write-capable lifecycle
// actuators, per tenant. Absent ⇒ no actuators ⇒ the module degrades honestly.
type nhiActuatorsConfig struct {
	Tenants []nhiActuatorTenant `json:"tenants"`
}

// nhiActuatorTenant carries one tenant's actuator credentials. Each is a SEPARATE,
// write-capable credential — distinct from the read-only roster connector tokens —
// so the read-first guarantee of the Source types stays intact (the actuators are
// independent types). Secrets live here, never the store, never logged.
type nhiActuatorTenant struct {
	Tenant string `json:"tenant"`
	// Vault: a token allowed to mint AppRole secret-ids and toggle entity disabled.
	VaultBaseURL   string `json:"vault_base_url,omitempty"`
	VaultToken     string `json:"vault_token,omitempty"`
	VaultNamespace string `json:"vault_namespace,omitempty"`
	// Anthropic: an admin key (sk-ant-admin…) allowed to update API-key status.
	AnthropicBaseURL  string `json:"anthropic_base_url,omitempty"`
	AnthropicAdminKey string `json:"anthropic_admin_key,omitempty"`
	// CyberArk Conjur (COMMERCIAL — wired only under -tags enterprise via
	// the nhiActuator edition port): a login + API key with `update` privilege on the
	// target hosts, allowed to rotate a host's API key. These are plain config
	// fields (no enterprise import); the actuator is constructed only in the
	// build-tag-gated seam, so the default AGPL binary never links the connector.
	ConjurBaseURL string `json:"conjur_base_url,omitempty"`
	ConjurAccount string `json:"conjur_account,omitempty"`
	ConjurLogin   string `json:"conjur_login,omitempty"`
	ConjurAPIKey  string `json:"conjur_api_key,omitempty"`
}

// loadNHIActuatorsConfig reads OLIVARES_NHI_ACTUATORS_CONFIG. A missing path yields
// an empty config (no actuators — honest degrade); a supplied path must be readable and
// contain valid JSON or startup fails closed.
func loadNHIActuatorsConfig(_ *slog.Logger) (nhiActuatorsConfig, error) {
	path := osGetenv("OLIVARES_NHI_ACTUATORS_CONFIG")
	if path == "" {
		return nhiActuatorsConfig{}, nil
	}
	var cfg nhiActuatorsConfig
	if err := loadOperatorJSONConfig("OLIVARES_NHI_ACTUATORS_CONFIG", path, &cfg); err != nil {
		return nhiActuatorsConfig{}, err
	}
	return cfg, nil
}

// buildNHIActuatorBindings constructs the per-(tenant,source) LifecycleActuator
// bindings from the config. Each actuator is built only when its credential is
// present, so a tenant may wire Vault but not Anthropic (or neither) — the module
// degrades honestly for any source without an actuator. The source keys MUST match
// the roster identity Provider (identitysource.SourceKind strings).
func buildNHIActuatorBindings(cfg nhiActuatorsConfig, log *slog.Logger) []governance.LifecycleActuatorBinding {
	var out []governance.LifecycleActuatorBinding
	for _, t := range cfg.Tenants {
		tenant := strings.TrimSpace(t.Tenant)
		if tenant == "" {
			continue
		}
		if strings.TrimSpace(t.VaultToken) != "" {
			out = append(out, governance.LifecycleActuatorBinding{
				Source: string(identitysource.SourceVault), TenantRef: tenant,
				Actuator: vault.NewActuator(t.VaultBaseURL, t.VaultToken, t.VaultNamespace, nil),
			})
			log.Info("nhi-lifecycle: Vault rotation/offboarding actuator wired", "tenant", tenant)
		}
		if strings.TrimSpace(t.AnthropicAdminKey) != "" {
			out = append(out, governance.LifecycleActuatorBinding{
				Source: string(identitysource.SourceAnthropic), TenantRef: tenant,
				Actuator: claudewif.NewActuator(t.AnthropicBaseURL, t.AnthropicAdminKey, nil),
			})
			log.Info("nhi-lifecycle: Anthropic key-status actuator wired", "tenant", tenant)
		}
		// the COMMERCIAL CyberArk Conjur rotation actuator, wired only under
		// -tags enterprise (the nhiActuator port); the default AGPL build has none
		// and degrades honestly.
		if thisEdition.nhiActuator != nil {
			if binding, ok := thisEdition.nhiActuator(t, tenant, log); ok {
				out = append(out, binding)
			}
		}
	}
	return out
}
