// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package agentcore

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

const (
	exportPlanHashVersion = "agentcore-export-plan-v1"

	exportOpCreate = "create"
	exportOpUpdate = "update"
	exportOpDelete = "delete"
)

// ExportPlan is the local dry-run diff for one AgentCore policy engine. It is
// intentionally independent of apply/HITL: Decision D3 says dry-run is a
// local comparison, while AgentCore analyzer validation happens only on write.
type ExportPlan struct {
	PlanHash    string
	EngineID    string
	Tenant      string
	Creates     []PlannedChange
	Updates     []PlannedChange
	Deletes     []PlannedChange
	Unchanged   []string
	Unmanaged   []string
	Unsupported []UnsupportedItem
}

// PlannedChange is one create/update/delete candidate. RemoteFingerprint is
// computed from remote Cedar statement content only; marker-vs-content drift is
// reported by the posture path, not by this planner.
type PlannedChange struct {
	Op                    string
	Name                  string
	PolicyID              string
	Statement             string
	Description           string
	EnforcementMode       string
	RemoteEnforcementMode string
	RemoteFingerprint     string
}

type remotePolicyState struct {
	item            policyItem
	fingerprint     string
	enforcementMode string
}

// BuildExportPlan diffs desired rendered policies against remote AgentCore
// policy rows for one tenant. Unsupported is empty here because the ratified
// signature accepts only desired+remote; use BuildExportPlanWithUnsupported
// when carrying the translator's unsupported bucket through to the response.
func BuildExportPlan(engineID, tenant string, desired []RenderedPolicy, remote []policyItem) ExportPlan {
	return buildExportPlan(engineID, tenant, desired, nil, remote)
}

// BuildExportPlanWithUnsupported is the same deterministic planner with the
// translator's unsupported bucket threaded through. The unsupported rows are
// report-only and do not affect PlanHash per decision D3.
func BuildExportPlanWithUnsupported(engineID, tenant string, desired []RenderedPolicy, unsupported []UnsupportedItem, remote []policyItem) ExportPlan {
	return buildExportPlan(engineID, tenant, desired, unsupported, remote)
}

func buildExportPlan(engineID, tenant string, desired []RenderedPolicy, unsupported []UnsupportedItem, remote []policyItem) ExportPlan {
	desiredByName := make(map[string]RenderedPolicy, len(desired))
	for _, p := range desired {
		p.EnforcementMode = normalizeEnforcementMode(p.EnforcementMode)
		desiredByName[p.Name] = p
	}

	managed := make(map[string]remotePolicyState)
	unmanaged := make([]string, 0)
	remoteSorted := append([]policyItem(nil), remote...)
	sort.SliceStable(remoteSorted, func(i, j int) bool {
		if remoteSorted[i].Name == remoteSorted[j].Name {
			return remoteSorted[i].PolicyID < remoteSorted[j].PolicyID
		}
		return remoteSorted[i].Name < remoteSorted[j].Name
	})
	for _, p := range remoteSorted {
		if st, ok := managedRemotePolicy(tenant, p); ok {
			managed[p.Name] = st
			continue
		}
		unmanaged = append(unmanaged, p.Name)
	}

	plan := ExportPlan{
		EngineID:    engineID,
		Tenant:      tenant,
		Unmanaged:   sortedCopy(unmanaged),
		Unsupported: append([]UnsupportedItem(nil), unsupported...),
	}
	sort.SliceStable(plan.Unsupported, func(i, j int) bool {
		return unsupportedSortKey(plan.Unsupported[i]) < unsupportedSortKey(plan.Unsupported[j])
	})

	desiredNames := make([]string, 0, len(desiredByName))
	for name := range desiredByName {
		desiredNames = append(desiredNames, name)
	}
	sort.Strings(desiredNames)

	for _, name := range desiredNames {
		want := desiredByName[name]
		remoteState, ok := managed[name]
		if !ok {
			plan.Creates = append(plan.Creates, PlannedChange{
				Op:              exportOpCreate,
				Name:            want.Name,
				Statement:       want.Statement,
				Description:     want.Description,
				EnforcementMode: want.EnforcementMode,
			})
			continue
		}
		wantFingerprint := sha256Hex(want.Statement)
		if remoteState.fingerprint != wantFingerprint || remoteState.enforcementMode != want.EnforcementMode {
			plan.Updates = append(plan.Updates, PlannedChange{
				Op:                    exportOpUpdate,
				Name:                  want.Name,
				PolicyID:              remoteState.item.PolicyID,
				Statement:             want.Statement,
				Description:           want.Description,
				EnforcementMode:       want.EnforcementMode,
				RemoteEnforcementMode: remoteState.enforcementMode,
				RemoteFingerprint:     remoteState.fingerprint,
			})
			continue
		}
		plan.Unchanged = append(plan.Unchanged, name)
	}

	for name, st := range managed {
		if _, ok := desiredByName[name]; ok {
			continue
		}
		plan.Deletes = append(plan.Deletes, PlannedChange{
			Op:                    exportOpDelete,
			Name:                  name,
			PolicyID:              st.item.PolicyID,
			RemoteEnforcementMode: st.enforcementMode,
			RemoteFingerprint:     st.fingerprint,
		})
	}

	sortChanges(plan.Creates)
	sortChanges(plan.Updates)
	sortChanges(plan.Deletes)
	sort.Strings(plan.Unchanged)
	sort.Strings(plan.Unmanaged)
	plan.PlanHash = computeExportPlanHash(engineID, tenant, append(append(append([]PlannedChange(nil), plan.Creates...), plan.Updates...), plan.Deletes...))
	return plan
}

func managedRemotePolicy(tenant string, p policyItem) (remotePolicyState, bool) {
	markerTenant, _, ok := parseExportMarker(p.Description)
	if !strings.HasPrefix(p.Name, "olv_") || !ok || markerTenant != tenant {
		return remotePolicyState{}, false
	}
	return remotePolicyState{
		item:            p,
		fingerprint:     remotePolicyFingerprint(p),
		enforcementMode: normalizeRemoteEnforcementMode(p.EnforcementMode),
	}, true
}

func remotePolicyFingerprint(p policyItem) string {
	if p.Definition.kind() != "cedar" {
		return ""
	}
	var s Source
	return sha256Hex(s.getCedarPolicyContent(p))
}

func normalizeRemoteEnforcementMode(mode string) string {
	mode = strings.ToUpper(strings.TrimSpace(mode))
	if mode == "" {
		return enforcementModeActive
	}
	return mode
}

func sortChanges(changes []PlannedChange) {
	sort.SliceStable(changes, func(i, j int) bool {
		if changes[i].Name == changes[j].Name {
			return changes[i].Op < changes[j].Op
		}
		return changes[i].Name < changes[j].Name
	})
}

func computeExportPlanHash(engineID, tenant string, changes []PlannedChange) string {
	sorted := append([]PlannedChange(nil), changes...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Op == sorted[j].Op {
			return sorted[i].Name < sorted[j].Name
		}
		return sorted[i].Op < sorted[j].Op
	})

	h := sha256.New()
	for _, part := range []string{exportPlanHashVersion, engineID, tenant} {
		writeLengthPrefixedHashPart(h, part)
	}
	for _, ch := range sorted {
		statementHash := ""
		enforcementMode := ""
		if ch.Op != exportOpDelete {
			statementHash = sha256Hex(ch.Statement)
			enforcementMode = normalizeEnforcementMode(ch.EnforcementMode)
		}
		remoteEnforcementMode := ""
		if ch.Op == exportOpUpdate || ch.Op == exportOpDelete {
			remoteEnforcementMode = normalizeRemoteEnforcementMode(ch.RemoteEnforcementMode)
		}
		for _, part := range []string{ch.Op, ch.Name, statementHash, enforcementMode, remoteEnforcementMode} {
			writeLengthPrefixedHashPart(h, part)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}
