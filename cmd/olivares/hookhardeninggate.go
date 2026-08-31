// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"time"

	"github.com/olivaresai/olivares/connectors/managedsettings"
)

// hookhardeninggate.go is the AGPL composition-root seam for the commercial hooks-hardening
// admin operations (legs 2 + 3): the fleet "deployed-verified" attestation and the
// conformance certificate against the real claude binary. The verb LOGIC lives in the closed
// enterprise/hookhardening module, reached through this build-neutral seam (the inspector half of
// the add-on is wired separately in claudehookfirewall.go). The `olivares hooks` command is always
// registered (main.go); in the default AGPL build the seam is nil, so each verb fails HONESTLY
// rather than pretending to work.
//
// The seam names only AGPL/Apache types (managedsettings.Policy, time, bytes) so it is nameable in
// BOTH builds; the closed types (CanonicalBundle, FleetAttestation, ConformanceCert) never cross
// it — the engine returns rendered JSON + a signed blob.

// hookHardeningEngine is the narrow seam the CLI depends on. The enterprise build supplies a real
// implementation (hookhardening_enterprise.go); the default build supplies nil
// (hookhardening_noenterprise.go).
type hookHardeningEngine interface {
	// FleetAttest renders the canonical managed-settings bundle for the authored policy, attests
	// the node reports (a JSON array of node {node_id, present, sha256, claude_version}) against
	// it, and — when a signing key is given — returns the signed roll-up blob. summaryJSON is the
	// human-readable attestation (always returned).
	FleetAttest(policy managedsettings.Policy, version string, nodeReportsJSON []byte, signingKeyB64 string, now time.Time) (signedBlob string, summaryJSON []byte, err error)
	// Conform runs the conformance harness against the real claude binary (honest not_run when it
	// is absent) and returns the certificate JSON plus — when a signing key is given — the signed
	// cert blob. behavioral opts into the gold-standard tier (drive the real binary against a mock
	// model and assert the PreToolUse PEP blocks a tool-call in flight).
	Conform(ctx context.Context, policy managedsettings.Policy, behavioral bool, signingKeyB64 string, now time.Time) (certJSON []byte, signedBlob string, err error)
}

var errHookHardeningNotActive = errors.New(
	"hooks-hardening add-on not available: " + enterpriseEditionHint +
		"; this build ships the governed hooks PEP, but not the DLP firewall, fleet attestation or conformance cert")

// resolveHookHardening returns the engine, or an honest error when the add-on is not in this build.
func resolveHookHardening() (hookHardeningEngine, error) {
	eng := newHookHardening()
	if eng == nil {
		return nil, errHookHardeningNotActive
	}
	return eng, nil
}
