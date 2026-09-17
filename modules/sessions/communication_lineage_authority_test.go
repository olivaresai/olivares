// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// lineageAuthorityRelations is the closed authority inventory core/model
// derives its epoch kinds from. It is spelled out here on purpose: if core adds
// or renames a relation, this list stops matching model.LineageEpochKind and
// the guard below fails, instead of a K3 consumer silently ignoring a fact its
// producer now emits.
var lineageAuthorityRelations = []model.Kind{
	"core.session", "core.agent", "core.resource",
	"core.workspace", "core.agent_group", "core.agent_group_member",
}

func lineageAuthorityEpochKinds(t *testing.T) []model.Kind {
	t.Helper()
	kinds := make([]model.Kind, 0, len(lineageAuthorityRelations))
	for _, relation := range lineageAuthorityRelations {
		kind, declared := model.LineageEpochKind(relation)
		if !declared {
			t.Fatalf("core no longer declares a lineage epoch for %s", relation)
		}
		if !model.IsLineageEpochKind(kind) {
			t.Fatalf("core does not recognize its own epoch kind %s", kind)
		}
		kinds = append(kinds, kind)
	}
	return kinds
}

// TestCanonicalAuthorizationFactsAcceptsEveryClosedLineageEpochKind pins the
// producer/consumer agreement F1 was missing: every kind core/governance can
// actually emit is a kind K3 can pin, one by one and all at once, with its key
// and generation untouched.
func TestCanonicalAuthorizationFactsAcceptsEveryClosedLineageEpochKind(t *testing.T) {
	t.Parallel()

	tenant := model.TenantID(model.NewID())
	all := []store.AuthorizationFactRef{
		{Kind: model.DirectoryEpochKind, ID: model.ID(tenant), Version: 4},
		{Kind: model.AuthorizationEpochKind, ID: model.ID(tenant), Version: 3},
	}
	for index, kind := range lineageAuthorityEpochKinds(t) {
		fact := store.AuthorizationFactRef{
			Kind: kind, ID: model.ID(tenant), Version: int64(index + 1),
		}
		canonical, err := CanonicalAuthorizationFacts([]store.AuthorizationFactRef{fact})
		if err != nil {
			t.Fatalf("lineage epoch %s rejected: %v", kind, err)
		}
		if len(canonical) != 1 || canonical[0] != fact {
			t.Fatalf("lineage epoch %s = %#v, want %#v unchanged", kind, canonical, fact)
		}
		all = append(all, fact)
	}

	canonical, err := CanonicalAuthorizationFacts(all)
	if err != nil {
		t.Fatalf("complete lineage authority set rejected: %v", err)
	}
	if len(canonical) != len(all) {
		t.Fatalf("canonicalization dropped facts: %d in, %d out", len(all), len(canonical))
	}
	seen := make(map[model.Kind]store.AuthorizationFactRef, len(canonical))
	for index, fact := range canonical {
		if index > 0 && canonical[index-1].Kind > fact.Kind {
			t.Fatalf("facts are not in canonical kind order: %v", canonical)
		}
		seen[fact.Kind] = fact
	}
	for _, fact := range all {
		if seen[fact.Kind] != fact {
			t.Fatalf("fact %s changed through canonicalization: %#v", fact.Kind, seen[fact.Kind])
		}
	}
}

// TestCanonicalAuthorizationFactsRejectsLineageEpochLookalikes shows the set is
// closed by the core inventory rather than by a "core.*_lineage_epoch" spelling
// rule: a plausible name for a relation that does not exist is still refused.
func TestCanonicalAuthorizationFactsRejectsLineageEpochLookalikes(t *testing.T) {
	t.Parallel()

	tenant := model.TenantID(model.NewID())
	for _, lookalike := range []model.Kind{
		"core.membership_lineage_epoch",
		"core.identity_lineage_epoch",
		"core.user_group_member_lineage_epoch",
		"core.workspace_lineage_epochs",
		"core.workspace_lineage_Epoch",
		"core.workspaces_lineage_epoch",
		"workspace_lineage_epoch",
		"core._lineage_epoch",
		"core.lineage_epoch",
		"core.workspace_lineage_epoch ",
		"governance.workspace_lineage_epoch",
		"core.workspace",
		"core.anything",
	} {
		fact := store.AuthorizationFactRef{Kind: lookalike, ID: model.ID(tenant), Version: 1}
		if _, err := CanonicalAuthorizationFacts([]store.AuthorizationFactRef{fact}); err == nil {
			t.Fatalf("unrecognized authorization fact kind %q accepted", lookalike)
		}
	}
}

// TestCanonicalAuthorizationFactsKeepsLineageSortDedupAndBounds keeps the
// canonical grammar identical for the kinds F1 added: same sort, same duplicate
// refusal, same malformed-reference and 64-fact bounds.
func TestCanonicalAuthorizationFactsKeepsLineageSortDedupAndBounds(t *testing.T) {
	t.Parallel()

	tenant := model.TenantID(model.NewID())
	other := model.TenantID(model.NewID())
	workspaceEpoch, _ := model.LineageEpochKind("core.workspace")
	sessionEpoch, _ := model.LineageEpochKind("core.session")

	unsorted := []store.AuthorizationFactRef{
		{Kind: workspaceEpoch, ID: model.ID(tenant), Version: 9},
		{Kind: sessionEpoch, ID: model.ID(tenant), Version: 2},
	}
	canonical, err := CanonicalAuthorizationFacts(unsorted)
	if err != nil {
		t.Fatalf("two lineage epochs rejected: %v", err)
	}
	if canonical[0].Kind != sessionEpoch || canonical[1].Kind != workspaceEpoch {
		t.Fatalf("lineage epochs not sorted by kind: %v", canonical)
	}

	duplicate := []store.AuthorizationFactRef{unsorted[0], unsorted[0]}
	if _, err := CanonicalAuthorizationFacts(duplicate); err == nil {
		t.Fatal("duplicate lineage epoch accepted")
	}

	// Same kind, two tenants: canonicalization sorts them and leaves the tenant
	// correspondence to validateCommunicationAuthorityFacts and the store.
	crossTenant := []store.AuthorizationFactRef{
		{Kind: workspaceEpoch, ID: model.ID(tenant), Version: 9},
		{Kind: workspaceEpoch, ID: model.ID(other), Version: 1},
	}
	if _, err := CanonicalAuthorizationFacts(crossTenant); err != nil {
		t.Fatalf("two tenants of the same lineage kind rejected by the canonicalizer: %v", err)
	}

	for name, fact := range map[string]store.AuthorizationFactRef{
		"zero version":      {Kind: workspaceEpoch, ID: model.ID(tenant), Version: 0},
		"negative version":  {Kind: workspaceEpoch, ID: model.ID(tenant), Version: -1},
		"non-uuid id":       {Kind: workspaceEpoch, ID: "not-a-uuid", Version: 1},
		"empty id":          {Kind: workspaceEpoch, ID: "", Version: 1},
		"non-v7 id":         {Kind: workspaceEpoch, ID: "00000000-0000-4000-8000-000000000000", Version: 1},
		"uppercase uuid id": {Kind: workspaceEpoch, ID: "0198C0DE-0000-7000-8000-000000000001", Version: 1},
	} {
		if _, err := CanonicalAuthorizationFacts([]store.AuthorizationFactRef{fact}); err == nil {
			t.Fatalf("malformed lineage fact accepted (%s)", name)
		}
	}

	tooMany := make([]store.AuthorizationFactRef, 65)
	for i := range tooMany {
		tooMany[i] = store.AuthorizationFactRef{
			Kind: workspaceEpoch, ID: model.NewID(), Version: 1,
		}
	}
	if _, err := CanonicalAuthorizationFacts(tooMany); err == nil {
		t.Fatal("lineage facts beyond the 64 bound accepted")
	}
}

// TestCanonicalAuthorizationFactUnionKeepsLineageGenerationsExact covers the
// two union helpers that share this canonicalizer: a carrier gate and a
// transaction may merge fact sets, but never merge two generations of the same
// lineage relation into one snapshot.
func TestCanonicalAuthorizationFactUnionKeepsLineageGenerationsExact(t *testing.T) {
	t.Parallel()

	tenant := model.TenantID(model.NewID())
	workspaceEpoch, _ := model.LineageEpochKind("core.workspace")
	current := store.AuthorizationFactRef{Kind: workspaceEpoch, ID: model.ID(tenant), Version: 7}
	stale := store.AuthorizationFactRef{Kind: workspaceEpoch, ID: model.ID(tenant), Version: 6}

	union, err := canonicalAuthorizationFactUnion([]store.AuthorizationFactRef{current, current})
	if err != nil {
		t.Fatalf("identical lineage generations rejected: %v", err)
	}
	if len(union) != 1 || union[0] != current {
		t.Fatalf("union = %#v, want the single fact %#v", union, current)
	}
	if _, err := canonicalAuthorizationFactUnion(
		[]store.AuthorizationFactRef{current, stale},
	); err == nil {
		t.Fatal("two generations of one lineage relation merged into a snapshot")
	}

	complete, err := canonicalCommunicationTransactionAuthorityFacts(
		[]store.AuthorizationFactRef{{
			Kind: model.AuthorizationEpochKind, ID: model.ID(tenant), Version: 3,
		}},
		[]store.AuthorizationFactRef{current},
	)
	if err != nil {
		t.Fatalf("transaction authority set rejected: %v", err)
	}
	if len(complete) != 2 {
		t.Fatalf("transaction authority set = %#v, want both facts", complete)
	}
	if _, err := canonicalCommunicationTransactionAuthorityFacts(
		[]store.AuthorizationFactRef{current},
		[]store.AuthorizationFactRef{stale},
	); err == nil {
		t.Fatal("a stale lineage generation merged with the current one")
	}
}

func lineageAuthorityCleanEvidence(
	tenant model.TenantID,
	facts []store.AuthorizationFactRef,
) auth.AuthorizationEvidence {
	observed := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	return auth.AuthorizationEvidence{
		Outcome:        auth.EvidenceAllow,
		CorePermission: auth.CheckEvidence{Verdict: auth.CheckClean, Code: "scoped_grant_permitted"},
		ResourceGuard:  auth.CheckEvidence{Verdict: auth.CheckClean, Code: "guard_not_applicable"},
		ForbidAbsence:  auth.CheckEvidence{Verdict: auth.CheckClean, Code: "forbid_absence_clean"},
		Facts:          facts,
		ObservedAt:     observed,
		FreshUntil:     observed.Add(time.Minute),
	}
}

// TestValidateCommunicationCoreAuthorizationEvidenceConsumesExactLineageFacts is
// the consumer-side contract: the exact fact set core produced for an ALLOW is
// accepted unchanged, and every closure that made this integration safe in the
// first place still refuses.
func TestValidateCommunicationCoreAuthorizationEvidenceConsumesExactLineageFacts(t *testing.T) {
	t.Parallel()

	tenant := model.TenantID(model.NewID())
	other := model.TenantID(model.NewID())
	workspaceEpoch, _ := model.LineageEpochKind("core.workspace")
	sessionEpoch, _ := model.LineageEpochKind("core.session")
	directory := store.AuthorizationFactRef{
		Kind: model.DirectoryEpochKind, ID: model.ID(tenant), Version: 4,
	}
	authorization := store.AuthorizationFactRef{
		Kind: model.AuthorizationEpochKind, ID: model.ID(tenant), Version: 3,
	}
	workspace := store.AuthorizationFactRef{
		Kind: workspaceEpoch, ID: model.ID(tenant), Version: 3,
	}
	session := store.AuthorizationFactRef{
		Kind: sessionEpoch, ID: model.ID(tenant), Version: 11,
	}

	// The shape governance actually emits: the policy generation plus every
	// consulted lineage generation, already in canonical order.
	produced := []store.AuthorizationFactRef{authorization, directory, session, workspace}
	outcome, facts, err := validateCommunicationCoreAuthorizationEvidence(
		lineageAuthorityCleanEvidence(tenant, produced), tenant,
	)
	if err != nil {
		t.Fatalf("K3 rejected the exact facts core produced: %v", err)
	}
	if outcome != ReadAllow {
		t.Fatalf("outcome = %v, want allow", outcome)
	}
	if !equalCommunicationAuthorityFacts(facts, produced) {
		t.Fatalf("consumed facts = %#v, want %#v", facts, produced)
	}

	t.Run("tenant correspondence", func(t *testing.T) {
		t.Parallel()
		foreign := store.AuthorizationFactRef{
			Kind: workspaceEpoch, ID: model.ID(other), Version: 3,
		}
		evidence := lineageAuthorityCleanEvidence(tenant,
			mustCanonicalLineageFacts(t, authorization, directory, foreign))
		if _, _, err := validateCommunicationCoreAuthorizationEvidence(evidence, tenant); err == nil {
			t.Fatal("a lineage epoch keyed on another tenant was consumed")
		}
	})

	t.Run("workspace id is not a lineage key", func(t *testing.T) {
		t.Parallel()
		// A lineage epoch has one row per tenant. A fact keyed on the workspace
		// it happens to describe is not the fact core produced.
		misKeyed := store.AuthorizationFactRef{
			Kind: workspaceEpoch, ID: model.NewID(), Version: 3,
		}
		evidence := lineageAuthorityCleanEvidence(tenant,
			mustCanonicalLineageFacts(t, authorization, directory, misKeyed))
		if _, _, err := validateCommunicationCoreAuthorizationEvidence(evidence, tenant); err == nil {
			t.Fatal("a lineage epoch keyed on something other than the tenant was consumed")
		}
	})

	t.Run("no leased witness", func(t *testing.T) {
		t.Parallel()
		leased, err := store.NewLeaseFenceAuthorizationFactRef(
			workspaceEpoch, model.ID(tenant), 3, "subject", 1,
			model.NewTimestamp(time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)),
		)
		if err != nil {
			t.Fatalf("build leased reference: %v", err)
		}
		evidence := lineageAuthorityCleanEvidence(tenant,
			mustCanonicalLineageFacts(t, authorization, directory, leased))
		if _, _, err := validateCommunicationCoreAuthorizationEvidence(evidence, tenant); err == nil {
			t.Fatal("a leased lineage witness was consumed")
		}
	})

	t.Run("lineage does not replace the authority epochs", func(t *testing.T) {
		t.Parallel()
		for name, facts := range map[string][]store.AuthorizationFactRef{
			"without directory epoch":     {authorization, workspace},
			"without authorization epoch": {directory, workspace},
			"lineage only":                {session, workspace},
		} {
			evidence := lineageAuthorityCleanEvidence(tenant, mustCanonicalLineageFacts(t, facts...))
			if _, _, err := validateCommunicationCoreAuthorizationEvidence(evidence, tenant); err == nil {
				t.Fatalf("an ALLOW %s was consumed", name)
			}
		}
	})

	t.Run("unrecognized kind", func(t *testing.T) {
		t.Parallel()
		evidence := lineageAuthorityCleanEvidence(tenant, []store.AuthorizationFactRef{
			authorization, directory,
			{Kind: "core.membership_lineage_epoch", ID: model.ID(tenant), Version: 1},
		})
		if _, _, err := validateCommunicationCoreAuthorizationEvidence(evidence, tenant); err == nil {
			t.Fatal("an unrecognized lineage-shaped kind was consumed")
		}
	})

	t.Run("non-canonical order", func(t *testing.T) {
		t.Parallel()
		evidence := lineageAuthorityCleanEvidence(tenant, []store.AuthorizationFactRef{
			workspace, session, directory, authorization,
		})
		if _, _, err := validateCommunicationCoreAuthorizationEvidence(evidence, tenant); err == nil {
			t.Fatal("a fact set that is not already canonical was consumed")
		}
	})

	t.Run("unknown evidence carries no lineage proof", func(t *testing.T) {
		t.Parallel()
		unknown := auth.AuthorizationEvidence{
			Outcome:        auth.EvidenceUnknown,
			CorePermission: auth.CheckEvidence{Verdict: auth.CheckUnknown, Code: "unavailable"},
			ResourceGuard:  auth.CheckEvidence{Verdict: auth.CheckClean, Code: "guard_not_applicable"},
			ForbidAbsence:  auth.CheckEvidence{Verdict: auth.CheckClean, Code: "forbid_absence_clean"},
			Facts:          []store.AuthorizationFactRef{workspace},
		}
		if _, _, err := validateCommunicationCoreAuthorizationEvidence(unknown, tenant); err == nil {
			t.Fatal("an UNKNOWN core authority carrying a lineage fact was consumed")
		}
		unknown.Facts = nil
		outcome, facts, err := validateCommunicationCoreAuthorizationEvidence(unknown, tenant)
		if err != nil || outcome != ReadUnknown || len(facts) != 0 {
			t.Fatalf("UNKNOWN core authority = (%v, %#v, %v)", outcome, facts, err)
		}
	})

	t.Run("allow without a finite proof", func(t *testing.T) {
		t.Parallel()
		incomplete := lineageAuthorityCleanEvidence(tenant, nil)
		if _, _, err := validateCommunicationCoreAuthorizationEvidence(incomplete, tenant); err == nil {
			t.Fatal("an ALLOW with no facts at all was consumed")
		}
	})
}

func mustCanonicalLineageFacts(
	t *testing.T,
	facts ...store.AuthorizationFactRef,
) []store.AuthorizationFactRef {
	t.Helper()
	sorted := append([]store.AuthorizationFactRef(nil), facts...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].Kind < sorted[j-1].Kind; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	return sorted
}

// TestDirectNoticePublishAuthorityFactsLocksTheSameClosedLineageInventory is
// the send-path half of the same producer/consumer agreement. This path keeps
// its OWN, narrower copy of the list — it asserts what the store can lock — and
// that copy predated the lineage epochs, so a message-send stayed UNKNOWN even
// after the shared canonicalizer accepted them. The store registers each of
// these kinds as an authorization fact with its own lock order, which is what
// the assertion is about; everything else still falls through deny-closed.
func TestDirectNoticePublishAuthorityFactsLocksTheSameClosedLineageInventory(t *testing.T) {
	t.Parallel()

	scope := DirectoryScopeRef{TenantID: model.TenantID(model.NewID()), WorkspaceID: model.NewID()}
	const epoch = int64(4)
	rosterHash, err := CanonicalDirectoryRosterHash(scope, epoch, nil)
	if err != nil {
		t.Fatalf("roster hash: %v", err)
	}
	observed := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	snapshot := DirectorySnapshot{
		Scope: scope, Epoch: epoch, ObservedAt: observed,
		FreshUntil: observed.Add(time.Minute), RosterHash: rosterHash,
		Selectors: []AudienceSelector{{
			Kind: AudienceUserGroup, Ref: model.NewID().String(),
			Required: false, WakePolicy: WakeAll,
		}},
	}
	authorization := store.AuthorizationFactRef{
		Kind: model.AuthorizationEpochKind, ID: model.ID(scope.TenantID), Version: 3,
	}
	directory := store.AuthorizationFactRef{
		Kind: model.DirectoryEpochKind, ID: model.ID(scope.TenantID), Version: epoch,
	}
	preflight := func(extra ...store.AuthorizationFactRef) directNoticePublishPreflight {
		return directNoticePublishPreflight{
			Snapshot: snapshot,
			CoreWitness: ReadWitness{
				Facts: append([]store.AuthorizationFactRef{authorization, directory}, extra...),
			},
		}
	}

	for _, kind := range lineageAuthorityEpochKinds(t) {
		lineage := store.AuthorizationFactRef{
			Kind: kind, ID: model.ID(scope.TenantID), Version: 2,
		}
		facts, err := directNoticePublishAuthorityFacts(preflight(lineage))
		if err != nil {
			t.Fatalf("send-path authority facts rejected lineage epoch %s: %v", kind, err)
		}
		found := false
		for _, fact := range facts {
			found = found || fact == lineage
		}
		if !found {
			t.Fatalf("send-path authority facts dropped lineage epoch %s: %v", kind, facts)
		}
	}

	for _, unlockable := range []model.Kind{
		"core.membership", "core.user_group_member", "core.agent_group_member",
		"core.membership_lineage_epoch", "core.anything",
	} {
		fact := store.AuthorizationFactRef{
			Kind: unlockable, ID: model.ID(scope.TenantID), Version: 2,
		}
		if _, err := directNoticePublishAuthorityFacts(preflight(fact)); err == nil {
			t.Fatalf("send-path authority facts accepted %q", unlockable)
		}
	}
}
