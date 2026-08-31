// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"log/slog"
	"strings"
	"testing"
)

// the SAFE arm of the artifact-cut counterfactual
// (an internal design note (not shipped) §7). Its exempt arm is
// TestArtifactCut_ExemptArm_LoginPostureSurvivesButStopsApplying in core/auth.
//
// `audit-worm-archive` (pack self_hosted.business.addons.regulated,
// PRICING-CANON.md:279-284) is declared SAFE TO CUT, and the reason is measured
// here rather than argued: an operator who selected an ENTERPRISE WORM sink kind
// and receives an artifact that does not carry enterprise/wormsinks cannot end up
// with silently-disabled archival, because the composition root REFUSES TO BOOT
// (auditarchive.go:167-169 and :226-228, propagated at boot.go:1406-1408). No
// operator state is left orphaned: there is no running process to orphan it in.
//
// This also fixes a claim that had propagated through two documents. The seam
// comment at wire_noenterprise.go:216-217 describes its own return value as
// leaving "archival OFF", and an internal design note (not shipped) §7 quoted that
// phrase as the verdict "PIERDE EVIDENCIA". The surrounding code refutes it. If
// someone ever softens this refusal into a warning, this test fails.
func TestArtifactCut_SafeArm_EnterpriseArchiveKindRefusesToBoot(t *testing.T) {
	log := slog.Default()

	// The operator selected the Azure immutable-LOCKED sink — a real kind, resolved
	// only by enterpriseArchiveSink under -tags enterprise (wire_noenterprise.go:218).
	cfg := auditArchiveConfig{sink: "azureblobworm", interval: defaultAuditArchiveInterval, retainDays: defaultAuditArchiveRetainDays}

	sink, err := buildAuditArchiveSink(cfg, log)
	if err == nil {
		t.Fatal("an artifact without the enterprise WORM sinks must REFUSE a sink kind it cannot serve, " +
			"not fall back to archival OFF — got a nil error")
	}
	if sink != nil {
		t.Fatalf("a refused sink selection must return no sink, got %T", sink)
	}
	if !strings.Contains(err.Error(), auditArchiveSinkEnv) {
		t.Fatalf("the refusal must name %s so the operator can find what to change: %v", auditArchiveSinkEnv, err)
	}
	if !strings.Contains(err.Error(), "refusing to start") {
		t.Fatalf("the refusal must say it is refusing to start, not that archival is off: %v", err)
	}

	// The control: the SHIPPED default (no sink selected) is not a refusal. Without
	// this the test above would pass on a build that refuses everything, which would
	// be a different — and worse — defect.
	off, offErr := newAuditArchiveLoop(auditArchiveConfig{}, nil, nil, nil, log)
	if offErr != nil {
		t.Fatalf("no sink selected is the shipped default and must not fail the boot: %v", offErr)
	}
	if off != nil {
		t.Fatalf("no sink selected must yield no loop, got %T", off)
	}
}
