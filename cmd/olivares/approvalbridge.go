// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"log/slog"

	"github.com/olivaresai/olivares/cmd/olivares/internal/approvalbridge"
)

// The OUTBOUND approval bridge lives in internal/approvalbridge. This file wires it:
// it reads the operator configuration and builds the bridge with this binary's
// business-tenant parser. The names below are the ones the composition root's
// gates already use.
type (
	approvalBridge       = approvalbridge.Bridge
	approvalBridgeConfig = approvalbridge.Config
	approvalBridgeTenant = approvalbridge.Tenant
	serviceCred          = approvalbridge.ServiceCred
	approverEvidence     = approvalbridge.ApproverEvidence
)

const (
	nbPending    = approvalbridge.Pending
	nbApproved   = approvalbridge.Approved
	nbRejected   = approvalbridge.Rejected
	nbCanceled   = approvalbridge.Canceled
	nbExpired    = approvalbridge.Expired
	nbNoGate     = approvalbridge.NoGate
	nbBreakGlass = approvalbridge.BreakGlass

	noGateRefPrefix     = approvalbridge.NoGateRefPrefix
	breakGlassRefPrefix = approvalbridge.BreakGlassRefPrefix
	planBindingMarker   = approvalbridge.PlanBindingMarker
)

// loadApprovalBridgeConfig reads the optional OUTBOUND-gate provisioning file named by
// OLIVARES_APPROVAL_BRIDGE_CONFIG (a JSON approvalBridgeConfig), the same operator-
// secret pattern as OLIVARES_HITL_CONFIG / OLIVARES_NOTIFY_CONFIG: the service tokens
// the bridge proposes as live here by value, never in the store. An unset path yields
// an empty config for the local approval service; a supplied path must be readable
// and contain valid JSON or startup fails.
func loadApprovalBridgeConfig(_ *slog.Logger) (approvalBridgeConfig, error) {
	path := osGetenv("OLIVARES_APPROVAL_BRIDGE_CONFIG")
	if path == "" {
		return approvalBridgeConfig{}, nil
	}
	var cfg approvalBridgeConfig
	if err := loadOperatorJSONConfig("OLIVARES_APPROVAL_BRIDGE_CONFIG", path, &cfg); err != nil {
		return approvalBridgeConfig{}, err
	}
	return cfg, nil
}

// newApprovalBridge builds the bridge from the operator config (see approvalbridge.New).
func newApprovalBridge(cfg approvalBridgeConfig, log *slog.Logger) *approvalBridge {
	return approvalbridge.New(cfg, parseBusinessTenant, log)
}
