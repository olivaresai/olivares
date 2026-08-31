// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

// THE OTHER DIRECTION OF THE FAIL-OPEN-BY-ZERO CLASS: does the new guard OVER-refuse?
//
// The class was closed by making two ignorances unrepresentable as benign values — an
// unresolved role is no longer a topology (guardrolefact_test.go), and a missing state
// row is no longer a first encounter (rolloutclassification_test.go). Both of those
// suites measure the direction the defect ran in: a guard that used to let something
// through must now stop it.
//
// Neither measures the direction a FIX runs in. A refusal that is too wide does not
// look like a bug in a test suite full of refusals — it looks like more of what the
// suite is asking for — and it fails on the estates that are working correctly, which
// is where it is most expensive. These two cases are the ones where the guards are one
// reordered condition away from refusing a healthy deployment, and neither is reachable
// from the existing tables.
//
// Both were mutation-verified in both directions; the mutations are recorded in
// sessions-c4-clase-fail-open-valor-cero.md.

// TestAnUnreadRoleWithNoOwnerConfiguredIsStillSingleRole is the row the topology table
// does not contain.
//
// Every Known:false case in guardrolefact_test.go carries OwnerConfigured:true, because
// that is where the defect lived. So nothing pins the answer when the app role is
// unreadable and NO owner was ever configured — and that combination is not exotic: it
// is a single-role deployment running with AllowPrivilegedRole on a catalog-hardened
// install that revoked SELECT on pg_roles, which is precisely the estate the degrade
// path in store.go exists for.
//
// The answer must be SingleRole, and the reason is that the question was never asked of
// a server: with no second pool there is no split to detect, so no role name is needed
// to settle it. Reaching guardTopologyUnknown here would refuse a deployment that is
// working, for failing to answer a question nobody put to it.
//
// guardMetadataTopologyOf returns SingleRole for !OwnerConfigured before it looks at
// either role, so this passes today. What it defends is the ORDER: hoisting the App
// check above the OwnerConfigured check — the obvious tidy-up, since the two role legs
// otherwise read alike — turns every such boot into a refusal, and leaves all eight
// rows of the existing table green.
func TestAnUnreadRoleWithNoOwnerConfiguredIsStillSingleRole(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		roles guardRoles
	}{{
		name:  "the app role was not resolved and no owner was configured",
		roles: guardRoles{App: guardRoleFact{Known: false}},
	}, {
		// Belt and braces on the same clause: a leftover Role string must not rescue
		// it either, and must not condemn it.
		name:  "an unresolved app role carrying a stale name, no owner configured",
		roles: guardRoles{App: guardRoleFact{Role: "leftover", Known: false}},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := guardMetadataTopologyOf(tc.roles); got != guardTopologySingleRole {
				t.Fatalf("guardMetadataTopologyOf(%+v) = %v, want %v — an unreadable role must not turn a deployment that never asked for the split into a refusal; nothing went unanswered, because nothing was asked",
					tc.roles, got, guardTopologySingleRole)
			}
		})
	}
}

// TestANewlyDeclaredControlIsNotRefusedOnAnEstablishedDatabase is the upgrade every
// release performs, and the one shape that makes the classification receipt dangerous.
//
// The receipt refuses a control that has no state row but DOES have a receipt. A
// control declared by a NEW module version has neither — on a database that has been
// running for years and whose receipt relation is populated for its siblings. If the
// guard ever asks "does this database hold receipts" instead of "does it hold a receipt
// for THIS key", every upgrade that adds a control stops booting, and it stops booting
// on exactly the long-lived estates the receipt was written to protect.
//
// Nothing in the existing suite reaches this: every rollout test opens with one control
// throughout, so the receipt table is only ever consulted for a key that either has a
// receipt or is on a database that has none at all.
func TestANewlyDeclaredControlIsNotRefusedOnAnEstablishedDatabase(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "new-control.db")

	// An established deployment: one control, classified, its receipt written.
	st, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, registerWidgetStaged)
	if err != nil {
		t.Fatalf("open the established deployment: %v", err)
	}
	_ = st.Close()

	// The next release. It declares a SECOND control, witnessed by the same table so the
	// only thing that can distinguish the two is the receipt, not the witness.
	st2, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, registerWidgetStagedPlusSecondControl)
	if err != nil {
		t.Fatalf("a newly declared control was REFUSED on an established database: %v\nThe receipt must answer for THIS control key, not for the relation as a whole; otherwise adding a control brakes every upgrade that ships one", err)
	}
	defer func() { _ = st2.Close() }()

	// And it has to be classified, not merely tolerated: a boot that silently skipped the
	// new control would also pass the assertion above.
	got := rolloutStateOf(t, st2, secondTestControlKey)
	if got.Generation != 1 {
		t.Fatalf("the new control seeded at generation %d, want 1", got.Generation)
	}
	// The witness table exists (the first boot created it), so this control is correctly
	// classified into its LEGACY disposition — the same reading the receipt guard exists
	// to stop being re-derived once it is recorded.
	if got.ClassifiedMode != store.RolloutLegacyCompat {
		t.Fatalf("the new control classified %q, want %q: the witness table is present on this database, so a control meeting it for the first time must grandfather what is already there",
			got.ClassifiedMode, store.RolloutLegacyCompat)
	}
	// The first control must be untouched by any of it.
	if first := rolloutStateOf(t, st2, testControlKey); first.Generation != 1 {
		t.Fatalf("the pre-existing control moved to generation %d while a sibling was being declared", first.Generation)
	}
}

// secondTestControlKey is a control this deployment has never seen, declared by a later
// release than the one that classified testControlKey.
const secondTestControlKey = "rrw.staged.v2"

// registerWidgetStagedPlusSecondControl is that later release: the same table, the same
// first control, and one more control nobody here has ever classified.
func registerWidgetStagedPlusSecondControl(reg store.ExtensionRegistry) error {
	if err := registerWidgetStaged(reg); err != nil {
		return err
	}
	return reg.RolloutControl(store.RolloutControl{
		Key:          secondTestControlKey,
		WitnessTable: widgetDescriptor.Table,
		LegacyMode:   store.RolloutLegacyCompat,
		FreshMode:    store.RolloutEnforced,
	})
}
