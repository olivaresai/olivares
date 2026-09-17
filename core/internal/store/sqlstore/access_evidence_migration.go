// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// access_evidence_migration.go is core v9: the forward-only migration that makes the four
// access-evidence relations exist on every database, and the read-only classification that
// decides — before a single statement runs — which of the three authored things it may do.
//
// WHY A MIGRATION AND NOT THE RECONCILER. The four relations are APPEND-ONLY, so
// registering them changes registry.appendOnlyTables(), which changes the guard manifest's
// code_sha256, which changes the rollout id every bootstrap receipt already in a database
// derives from. Creating them in the generic additive reconciler would have meant a
// database whose ledger says one edition and whose census says another, with nothing in
// between to authorize the move. So the relations arrive with an EDITION: v9 either seals
// the edition v6 already bootstrapped, or crosses exactly one compiled access-evidence
// edge, and in both cases the objects, the ledger rows and the tracking row commit
// together or not at all.
//
// WHY THE CLASSIFICATION IS SEPARATE AND EARLIER. v9 runs after v1-v8, so by the time its
// transaction opens, the database has already been changed by this boot — v6 may have
// bootstrapped, v7 may have crossed the directory edge. A prestate read there could not
// tell "this database arrived pre-v6" from "this boot made it look that way". The
// classifier therefore runs under the migration lock BEFORE any migration, records one
// closed start class, and v9 re-derives its action transaction-locally and refuses if the
// two disagree.

const (
	coreAccessEvidenceMigrationVersion = 9
	coreAccessEvidenceMigrationName    = "access_evidence_foundation"
)

// accessEvidenceRelationNames is v9's creation order, fixed here rather than read off
// coreDescriptors: the order is durable migration behavior, and a later registry reorder
// must not reorder v9's DDL or its failure boundary.
var accessEvidenceRelationNames = [...]string{
	policyArtifactTable,
	authorityTransitionTable,
	actionObservationTable,
	authorizationDecisionTable,
}

// exactAccessEvidenceDescriptors selects exactly v9's four relations from the full core
// census and fixes their order and kinds.
func exactAccessEvidenceDescriptors(descs []model.EntityDescriptor) ([]model.EntityDescriptor, error) {
	required := []struct {
		kind  model.Kind
		table string
	}{
		{model.PolicyArtifactKind, policyArtifactTable},
		{model.AuthorityTransitionKind, authorityTransitionTable},
		{model.ActionObservationKind, actionObservationTable},
		{model.AuthorizationDecisionKind, authorizationDecisionTable},
	}
	byTable := make(map[string]model.EntityDescriptor, len(descs))
	for _, desc := range descs {
		if _, duplicate := byTable[desc.Table]; duplicate {
			return nil, fmt.Errorf("core access evidence v9: descriptor table %q is duplicated", desc.Table)
		}
		byTable[desc.Table] = desc
	}
	ordered := make([]model.EntityDescriptor, 0, len(required))
	for _, want := range required {
		desc, ok := byTable[want.table]
		if !ok {
			return nil, fmt.Errorf("core access evidence v9: required descriptor %s/%s is absent", want.kind, want.table)
		}
		if desc.Kind != want.kind {
			return nil, fmt.Errorf("core access evidence v9: table %q has kind %q, want %q", want.table, desc.Kind, want.kind)
		}
		if !desc.AppendOnly {
			return nil, fmt.Errorf("core access evidence v9: table %q is not declared append-only, so it is not the relation this edition guards", want.table)
		}
		ordered = append(ordered, desc)
	}
	return ordered, nil
}

// accessEvidenceRelationDisposition is all-or-none by construction. A subset is never a
// retry state: v9 is transactional on both engines, so no execution this binary authored
// can commit one, and treating a subset as resumable would launder out-of-band damage.
type accessEvidenceRelationDisposition bool

const (
	accessEvidenceInitiallyAbsent  accessEvidenceRelationDisposition = false
	accessEvidenceInitiallyPresent accessEvidenceRelationDisposition = true
)

func (d accessEvidenceRelationDisposition) String() string {
	if d == accessEvidenceInitiallyPresent {
		return "present"
	}
	return "absent"
}

func inspectAccessEvidenceDisposition(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
) (accessEvidenceRelationDisposition, error) {
	var have, missing []string
	for _, table := range accessEvidenceRelationNames {
		_, exists, err := inspectCoreDirectoryRelation(ctx, tx, dia, table)
		if err != nil {
			return accessEvidenceInitiallyAbsent,
				fmt.Errorf("inspect access-evidence relation %q: %w", table, err)
		}
		if exists {
			have = append(have, table)
		} else {
			missing = append(missing, table)
		}
	}
	switch len(have) {
	case 0:
		return accessEvidenceInitiallyAbsent, nil
	case len(accessEvidenceRelationNames):
		return accessEvidenceInitiallyPresent, nil
	default:
		return accessEvidenceInitiallyAbsent, fmt.Errorf(
			"partial access-evidence relation set (present=%v, absent=%v); no transactional v9 can commit that state",
			have, missing)
	}
}

// accessEvidenceV9Action is the one thing v9 may do in a given transaction.
type accessEvidenceV9Action string

const (
	// accessEvidenceV9AlreadyComplete: v9 is tracked. Nothing is applied; the per-boot
	// verifier is the only thing that runs.
	accessEvidenceV9AlreadyComplete accessEvidenceV9Action = "already-complete"
	// accessEvidenceV9FreshVerify: current v2 created the four relations. v9 proves they
	// are exact and appends the completion seal.
	accessEvidenceV9FreshVerify accessEvidenceV9Action = "fresh-verify-and-seal"
	// accessEvidenceV9DirectMaterialize: a <=v5 legacy source whose v6 bootstrap already
	// declared this edition. v9 creates the relations and appends the completion seal.
	accessEvidenceV9DirectMaterialize accessEvidenceV9Action = "direct-materialize-and-seal"
	// accessEvidenceV9GuardedTransition: an existing guarded database at E2/E3/E4. v9
	// creates the relations and commits exactly one same-base access-evidence edge.
	accessEvidenceV9GuardedTransition accessEvidenceV9Action = "guarded-transition"
)

// accessEvidenceStartClass is the one closed state the read-only classifier accepts.
type accessEvidenceStartClass string

const (
	accessEvidenceStartFreshEmpty        accessEvidenceStartClass = "fresh-empty"
	accessEvidenceStartFreshPreV2        accessEvidenceStartClass = "fresh-pre-v2"
	accessEvidenceStartFreshCurrentPreV6 accessEvidenceStartClass = "fresh-current-pre-v6"
	accessEvidenceStartLegacyPreV6       accessEvidenceStartClass = "legacy-pre-v6"
	accessEvidenceStartDirectPendingV7   accessEvidenceStartClass = "direct-v6-pending-v7"
	accessEvidenceStartFreshPendingV7    accessEvidenceStartClass = "fresh-v6-pending-v7"
	accessEvidenceStartGuardedPendingV7  accessEvidenceStartClass = "guarded-e1-pending-v7"
	accessEvidenceStartDirectPendingV9   accessEvidenceStartClass = "direct-v7/8-pending-v9"
	accessEvidenceStartFreshPendingV9    accessEvidenceStartClass = "fresh-v7/8-pending-v9"
	accessEvidenceStartGuardedPendingV9  accessEvidenceStartClass = "guarded-legacy-pending-v9"
	accessEvidenceStartV9Complete        accessEvidenceStartClass = "v9-complete"
)

// accessEvidenceBootPlan is the classifier's closed answer.
//
// It is NOT permission based on existence. It names one start class and the exact v9
// start expected after the intervening migrations have run, and v9 reclassifies inside
// its own transaction and refuses if what it finds is not what this plan predicted.
type accessEvidenceBootPlan struct {
	Class  accessEvidenceStartClass
	Action accessEvidenceV9Action
	// PredecessorEpoch and TargetEpoch are set only for a guarded transition. The target
	// is NOT always the current edition: a pre-Slice-F sessions database at E2 upgrading
	// to E7 crosses E2 -> E5 in v9 and reaches E6 and E7 through the module seam.
	PredecessorEpoch int64
	TargetEpoch      int64
	// CurrentEpoch and BaseSHA256 are the closed census this plan was built against.
	CurrentEpoch int64
	BaseSHA256   [32]byte
	// StandingEpoch is the edition the durable history was standing at when the
	// classifier ran, or 0 when there is no guard history yet.
	StandingEpoch int64
	// CoreVersions is the exact tracked core history, for diagnostics and for v9's
	// transaction-local correlation.
	CoreVersions []int64
	Directory    coreDirectoryInitialDisposition
	Evidence     accessEvidenceRelationDisposition
	// FreshBootstrap is set only for the max0 class, and carries what the pre-v1 admission
	// proved so the rollout classification transaction can re-prove the same thing locally
	// rather than trusting a read that happened in another transaction.
	FreshBootstrap *freshBootstrapAdmission
}

func (p accessEvidenceBootPlan) String() string {
	return fmt.Sprintf("class=%s action=%s standing=%d predecessor=%d target=%d current=%d",
		p.Class, p.Action, p.StandingEpoch, p.PredecessorEpoch, p.TargetEpoch, p.CurrentEpoch)
}

// accessEvidenceFamilyExpectation is what one start class requires of one relation
// family, and it has exactly two values because R2 §5.2's table has exactly two.
//
// "present" is NOT one of them, and that omission is the correction this file needed.
// The table's cell says `all exact` — "their tables, columns, CHECKs, indexes, tenant
// controls and append-only controls for that dialect" — and a classifier that accepted
// existence admitted a damaged checkpoint as an authored one, then committed six schema
// versions before a later fence noticed.
type accessEvidenceFamilyExpectation string

const (
	accessEvidenceFamilyAbsent accessEvidenceFamilyExpectation = "absent"
	accessEvidenceFamilyExact  accessEvidenceFamilyExpectation = "all exact"
)

// accessEvidenceStartShape is one row of R2 §5.2: what a class requires of the guard
// control plane, of the two directory tombstones plus the directory epoch, and of the four
// access-evidence relations.
//
// It is a TABLE rather than a chain of conditions because the contract is a table, and
// because the defect it replaces was a chain that fell through to a class after testing
// only some of its cells.
type accessEvidenceStartShape struct {
	ControlPlane bool
	Directory    accessEvidenceFamilyExpectation
	Evidence     accessEvidenceFamilyExpectation
	// LegacySource marks the one class whose SOURCE profile is allowlisted rather than
	// produced by this binary, and therefore the one that must prove it.
	LegacySource bool
}

func accessEvidenceStartShapeFor(class accessEvidenceStartClass) (accessEvidenceStartShape, bool) {
	shapes := map[accessEvidenceStartClass]accessEvidenceStartShape{
		accessEvidenceStartFreshEmpty:        {ControlPlane: false, Directory: accessEvidenceFamilyAbsent, Evidence: accessEvidenceFamilyAbsent},
		accessEvidenceStartFreshPreV2:        {ControlPlane: false, Directory: accessEvidenceFamilyAbsent, Evidence: accessEvidenceFamilyAbsent},
		accessEvidenceStartFreshCurrentPreV6: {ControlPlane: false, Directory: accessEvidenceFamilyExact, Evidence: accessEvidenceFamilyExact},
		accessEvidenceStartLegacyPreV6:       {ControlPlane: false, Directory: accessEvidenceFamilyAbsent, Evidence: accessEvidenceFamilyAbsent, LegacySource: true},
		accessEvidenceStartDirectPendingV7:   {ControlPlane: true, Directory: accessEvidenceFamilyAbsent, Evidence: accessEvidenceFamilyAbsent},
		accessEvidenceStartFreshPendingV7:    {ControlPlane: true, Directory: accessEvidenceFamilyExact, Evidence: accessEvidenceFamilyExact},
		accessEvidenceStartGuardedPendingV7:  {ControlPlane: true, Directory: accessEvidenceFamilyAbsent, Evidence: accessEvidenceFamilyAbsent},
		accessEvidenceStartDirectPendingV9:   {ControlPlane: true, Directory: accessEvidenceFamilyExact, Evidence: accessEvidenceFamilyAbsent},
		accessEvidenceStartFreshPendingV9:    {ControlPlane: true, Directory: accessEvidenceFamilyExact, Evidence: accessEvidenceFamilyExact},
		accessEvidenceStartGuardedPendingV9:  {ControlPlane: true, Directory: accessEvidenceFamilyExact, Evidence: accessEvidenceFamilyAbsent},
		accessEvidenceStartV9Complete:        {ControlPlane: true, Directory: accessEvidenceFamilyExact, Evidence: accessEvidenceFamilyExact},
	}
	shape, ok := shapes[class]
	return shape, ok
}

// verifyAccessEvidenceStartShape is the read-only proof that the state really is the class
// the tracker and the control plane suggested.
//
// IT READS AND IT COMMITS NOTHING. On SQLite it compares the complete stored object set of
// each relation against what the descriptor renders. On PostgreSQL the exact oracle is a
// short-lived probe relation rendered from the same descriptor on the same server, created
// and rolled back inside a savepoint of the caller's read transaction — the same mechanism
// core v7 already uses for its fresh branch, and the reason it is a rollback rather than a
// DROP is the deny-closed event fence, which correctly refuses removing a guard.
//
// IT DOES NOT REPLACE THE LATER FENCES, and it must not be read as doing so. v9 still
// re-derives its action transaction-locally, and verifyAccessEvidenceRelationsExact still
// runs before the generic reconciler on every boot. What this adds is the guarantee R2 §5.1
// actually promised: the refusal happens before ANY migration, not before the last one.
func verifyAccessEvidenceStartShape(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	descs []model.EntityDescriptor,
	class accessEvidenceStartClass,
	directory coreDirectoryInitialDisposition,
	evidence accessEvidenceRelationDisposition,
	controlPlane bool,
) error {
	shape, ok := accessEvidenceStartShapeFor(class)
	if !ok {
		return fmt.Errorf("%w: start class %q has no declared shape", ErrGuardManifestNoEdge, class)
	}
	if controlPlane != shape.ControlPlane {
		return fmt.Errorf("%w: start class %q requires the guard control plane %s and it is %s",
			ErrGuardManifestNoEdge, class,
			accessEvidencePresenceWord(shape.ControlPlane), accessEvidencePresenceWord(controlPlane))
	}

	wantDirectoryPresent := shape.Directory == accessEvidenceFamilyExact
	if (directory == coreDirectoryInitiallyPresent) != wantDirectoryPresent {
		return fmt.Errorf("%w: start class %q requires its directory relations %s and they are %s",
			ErrGuardManifestNoEdge, class, shape.Directory, directory)
	}
	wantEvidencePresent := shape.Evidence == accessEvidenceFamilyExact
	if (evidence == accessEvidenceInitiallyPresent) != wantEvidencePresent {
		return fmt.Errorf("%w: start class %q requires its access-evidence relations %s and they are %s",
			ErrGuardManifestNoEdge, class, shape.Evidence, evidence)
	}

	if shape.Directory == accessEvidenceFamilyExact {
		if err := verifyCoreDirectoryRelationsExact(ctx, tx, dia, descs); err != nil {
			return fmt.Errorf("%w: start class %q requires the exact directory contract: %v",
				ErrGuardManifestNoEdge, class, err)
		}
	}
	if shape.Evidence == accessEvidenceFamilyExact {
		ordered, oerr := exactAccessEvidenceDescriptors(descs)
		if oerr != nil {
			return oerr
		}
		if err := verifyAccessEvidenceRelationsInTx(ctx, tx, dia, ordered); err != nil {
			return fmt.Errorf("%w: start class %q requires the exact access-evidence contract: %v",
				ErrGuardManifestNoEdge, class, err)
		}
	}
	return nil
}

func accessEvidencePresenceWord(present bool) string {
	if present {
		return "present"
	}
	return "absent"
}

// verifyAllowlistedLegacySourceProfile is the check R2 §5.2 requires of the ONE start
// class whose source this binary did not produce.
//
// THE PROBLEM IT SOLVES, reproduced by the independent review: the direct <=v5 path was
// entered on nothing but "the tracker stops at or below 5 and neither relation family
// exists". Nothing looked at WHICH source that was. A build registering a module the
// source never had therefore reached the direct bootstrap, committed v9, and created the
// module's table — activating a census on a database that had never carried it, through
// the one path that exists for the opposite reason.
//
// Two conditions, and they are separate facts rather than one restated:
//
//  1. THE ALLOWLISTED PROFILE. The only ratified real <=v5 source is core-only,
//     `register == nil`, commit c166ed79. A build whose closed registry declares module
//     descriptors is not that profile, and R2 says a released module-bearing <=v5 source
//     "requires its own source-native registrar fixture and allowlist entry before
//     ratification or must refuse". This does not remove modularity: it says the direct
//     bootstrap is not where a module census arrives.
//  2. THE MODULE CENSUS OF THE DATABASE. Every currently registered module table must
//     already have BOTH an applied_module_tables row and a physical relation, and there
//     must be no tracking row outside that set. A missing one is activation; an extra one
//     is a source this allowlist does not name. Checking it separately is what makes the
//     refusal say which relation, instead of only which class.
//
// A genuinely fresh database is exempt, and deliberately so: fresh-empty and
// fresh-current-pre-v6 prove the CURRENT binary owns first creation, so there is no older
// registrar whose census could be expanded behind its back.
func verifyAllowlistedLegacySourceProfile(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	reg *registry,
) error {
	if reg == nil {
		return fmt.Errorf("%w: the legacy <=v5 start cannot be verified without the closed registry",
			ErrGuardManifestNoEdge)
	}
	modules := reg.moduleDescriptors()

	tracked, err := accessEvidenceModuleTrackingRows(ctx, tx, dia)
	if err != nil {
		return err
	}
	registered := make(map[string]bool, len(modules))
	var activating, absentRelations []string
	for _, desc := range modules {
		registered[desc.Table] = true
		_, exists, ierr := inspectCoreDirectoryRelation(ctx, tx, dia, desc.Table)
		if ierr != nil {
			return fmt.Errorf("sqlstore: inspect module relation %q: %w", desc.Table, ierr)
		}
		if !tracked[desc.Table] {
			activating = append(activating, desc.Table)
		}
		if !exists {
			absentRelations = append(absentRelations, desc.Table)
		}
	}
	var extra []string
	for table := range tracked {
		if !registered[table] {
			extra = append(extra, table)
		}
	}
	sort.Strings(activating)
	sort.Strings(absentRelations)
	sort.Strings(extra)

	if len(activating) != 0 || len(absentRelations) != 0 {
		return fmt.Errorf(
			"%w: this build registers module relations a <=v5 source does not carry (no %s row: %v; no physical relation: %v). Installing them from the direct bootstrap would activate a census this database never had; module activation is a separate authorized transition",
			ErrGuardManifestNoEdge, moduleTablesTracking, activating, absentRelations)
	}
	if len(extra) != 0 {
		return fmt.Errorf(
			"%w: the database records module tables %v that this build does not declare; the allowlisted <=v5 profile is core-only and names none",
			ErrGuardManifestNoEdge, extra)
	}
	if len(modules) != 0 {
		names := make([]string, 0, len(modules))
		for _, desc := range modules {
			names = append(names, desc.Table)
		}
		sort.Strings(names)
		return fmt.Errorf(
			"%w: the only ratified <=v5 source is the core-only profile of commit %s, and this build declares module relations %v; a released module-bearing <=v5 source needs its own source-native registrar fixture and allowlist entry before it can be upgraded from here",
			ErrGuardManifestNoEdge, accessEvidenceAllowlistedLegacySourceCommit, names)
	}
	return nil
}

// accessEvidenceAllowlistedLegacySourceCommit is R2 §5.2's one ratified real <=v5 profile:
// core-only, `register == nil`, the parent immediately before core v6 was introduced.
//
// It is a CONSTANT and not a lookup because widening it is a root decision. The historical
// fixture generated from this exact commit is a separate delivery; until it lands, this
// constant is what the refusal cites, and citing it is not the same as having run it.
const accessEvidenceAllowlistedLegacySourceCommit = "c166ed79efc66e3a8922c007a74defb0ad1228c8"

// accessEvidenceModuleTrackingRows reads applied_module_tables, treating an absent relation
// as an empty set: a <=v5 core-only source never created it.
func accessEvidenceModuleTrackingRows(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
) (map[string]bool, error) {
	_, exists, err := inspectCoreDirectoryRelation(ctx, tx, dia, moduleTablesTracking)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: inspect %s: %w", moduleTablesTracking, err)
	}
	if !exists {
		return map[string]bool{}, nil
	}
	rows, err := tx.QueryContext(ctx, "SELECT table_name FROM "+moduleTablesTracking) // #nosec G202 -- internal constant
	if err != nil {
		return nil, fmt.Errorf("sqlstore: read %s: %w", moduleTablesTracking, err)
	}
	defer rows.Close() //nolint:errcheck // joined through rows.Err below
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("sqlstore: read %s: %w", moduleTablesTracking, err)
		}
		out[name] = true
	}
	return out, rows.Err()
}

// classifyAccessEvidenceBoot is step 9 of the normative Open order: read-only, under the
// migration lock, after the tracked-v7/directory/history and lineage preflights and
// before classifyRolloutControls, v6 DDL, any migration or reconcileColumns.
//
// IT COMMITS NOTHING, and that is the accurate claim rather than "reads only": for the max0
// class it calls verifyFreshBootstrapAdmission, which executes probe writes inside savepoints of
// this transaction and rolls them back. Every refusal it produces still happens before ANY
// migration, and the plan it returns is a prediction the v9 transaction must be able to confirm.
//
// THE TWO CORRECTIONS THIS FUNCTION CARRIES, both reproduced with causal controls by the
// independent review of b2b1b54d before they were made:
//
//   - It used to name a class from PRESENCE and fall through to it without proving the
//     rest of that class's row. A current v2 checkpoint with one append-only guard removed
//     was accepted as `fresh-current-pre-v6`, and six schema versions committed before a
//     later fence refused; a v1 checkpoint with the four access-evidence relations
//     precreated was accepted as `fresh-pre-v2`, whose row requires both families absent,
//     and reached conflicting v2 DDL. Now every class is verified against its complete
//     row — control plane, directory, access evidence — and the `all exact` cells are
//     proved by the exact descriptor contract, not by existence.
//   - It named the legacy <=v5 class "the allowlisted legacy profile" in a comment and
//     never checked it. A build registering a module the source never had reached the
//     direct bootstrap, committed v9 and created that module's table. Now the source
//     profile and the module/tracker/catalog census are proved before any write.
//
// It does NOT replace the later fences and must not be read as doing so: v9 re-derives its
// action transaction-locally, and the pre-reconcile verification still runs on every boot.
func classifyAccessEvidenceBoot(
	ctx context.Context,
	mdb dialect.Execer,
	dia dialect.Dialect,
	graph guardEditionGraph,
	descs []model.EntityDescriptor,
	reg *registry,
	modulePlans []moduleFileMigrationPlan,
	fence guardEventFenceFacts,
) (accessEvidenceBootPlan, error) {
	plan := accessEvidenceBootPlan{CurrentEpoch: graph.Current, BaseSHA256: graph.BaseSHA256}

	tracked, err := coreTrackingRelationExists(ctx, mdb, dia)
	if err != nil {
		return plan, fmt.Errorf("sqlstore: access-evidence boot classification: inspect %s: %w", coreTrackingTable, err)
	}
	var maxVersion int64
	if tracked {
		versions, verr := readCanonicalCoreTrackingVersions(ctx, mdb, dia)
		if verr != nil {
			return plan, verr
		}
		for version := range versions {
			plan.CoreVersions = append(plan.CoreVersions, version)
			if version > maxVersion {
				maxVersion = version
			}
		}
		sort.Slice(plan.CoreVersions, func(i, j int) bool { return plan.CoreVersions[i] < plan.CoreVersions[j] })
		// A gap is not a resumable state on either engine: every core version commits
		// with its own tracking row, so a missing intermediate row means the history was
		// edited, not interrupted. "Contiguous" means an ordered prefix of the COMPILED
		// plan, not of the integers: v12 is reserved and unregistered, so 1..11 then 13
		// is the legitimate sequence (ROOT-CONSTRUCTION-R5-1 §2).
		order := compiledCoreMigrationVersionOrder(dia)
		for i, version := range plan.CoreVersions {
			if i >= len(order) || version != order[i] {
				return plan, fmt.Errorf(
					"sqlstore: access-evidence boot classification: core migration history %v is not a contiguous prefix; refusing to infer a start class from an edited history",
					plan.CoreVersions)
			}
		}
	}

	// The physical dispositions. Both are all-or-none, and a partial set of either is a
	// refusal here, before anything is created.
	tx, err := mdb.BeginTx(ctx, nil)
	if err != nil {
		return plan, fmt.Errorf("sqlstore: access-evidence boot classification: begin read: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // the classifier commits nothing
	directory, err := inspectCoreDirectoryInitialDisposition(ctx, tx, dia)
	if err != nil {
		return plan, fmt.Errorf("sqlstore: access-evidence boot classification: %w", err)
	}
	evidence, err := inspectAccessEvidenceDisposition(ctx, tx, dia)
	if err != nil {
		return plan, fmt.Errorf("sqlstore: access-evidence boot classification: %w", err)
	}
	plan.Directory, plan.Evidence = directory, evidence
	controlPlane, err := guardControlPlaneRelationsPresent(ctx, tx, dia)
	if err != nil {
		return plan, fmt.Errorf("sqlstore: access-evidence boot classification: %w", err)
	}
	// THE TRANSACTION STAYS OPEN through the whole classification. The exact-shape proof
	// below needs it: on PostgreSQL the only version-local oracle for a rendered contract is a
	// probe relation created and rolled back inside a savepoint of this transaction. It is
	// rolled back at the end and commits nothing either way.
	//
	// WHAT IT IS NOT: a single consistent snapshot of everything this function reads. The
	// tracker versions above are read BEFORE this transaction begins, and BeginTx(ctx, nil)
	// takes the engine's default isolation — READ COMMITTED on PostgreSQL, not RepeatableRead.
	// An earlier comment here claimed one consistent view for every read, and it was wrong.
	// The migration lock and the deployment fence exclude cooperating writers; they do not
	// prove an external owner committed no DDL in between, which is why the rollout
	// classification transaction re-proves its own prestate.

	// finish is the single exit: whichever class the tracker and the control plane
	// suggest, it is proved against that class's complete row before it is returned.
	// Naming the class is a hypothesis; this is where it stops being one.
	finish := func(class accessEvidenceStartClass, action accessEvidenceV9Action) (accessEvidenceBootPlan, error) {
		plan.Class, plan.Action = class, action
		if err := verifyAccessEvidenceStartShape(
			ctx, tx, dia, descs, class, directory, evidence, controlPlane); err != nil {
			return plan, fmt.Errorf("sqlstore: access-evidence boot classification: %w", err)
		}
		// THE MAX0 FRONTIER, and it is proved HERE because here is the last moment before
		// anything can commit. `fresh-empty` used to be decided by an empty tracker and three
		// absent families, which is not the ratified sentence: a malformed or precreated
		// managed object was admitted, and the boot went on to commit the rollout tables and
		// alter the tracker before any later fence could look. See freshBootstrapAdmission.
		if class == accessEvidenceStartFreshEmpty {
			admission, ferr := verifyFreshBootstrapAdmission(ctx, tx, dia, descs, reg, modulePlans, fence)
			if ferr != nil {
				return plan, fmt.Errorf("sqlstore: access-evidence boot classification: %w", ferr)
			}
			plan.FreshBootstrap = &admission
		}
		if shape, ok := accessEvidenceStartShapeFor(class); ok && shape.LegacySource {
			if err := verifyAllowlistedLegacySourceProfile(ctx, tx, dia, reg); err != nil {
				return plan, fmt.Errorf("sqlstore: access-evidence boot classification: %w", err)
			}
		}
		if action == accessEvidenceV9GuardedTransition {
			target, terr := accessEvidenceEdgeTarget(graph, plan.PredecessorEpoch)
			if terr != nil {
				return plan, terr
			}
			plan.TargetEpoch = target
		} else {
			plan.TargetEpoch = graph.Current
		}
		return plan, nil
	}

	if maxVersion >= coreAccessEvidenceMigrationVersion {
		return finish(accessEvidenceStartV9Complete, accessEvidenceV9AlreadyComplete)
	}

	// PRE-v6: no guard control plane exists, so there is no history to read and the class
	// follows from the tracked prefix and the two physical dispositions alone.
	if maxVersion < guardControlPlaneVersion {
		if controlPlane {
			return plan, fmt.Errorf(
				"%w: the guard control-plane relations exist while core v%d is not recorded",
				ErrGuardControlPlaneBootstrapInconsistent, guardControlPlaneVersion)
		}
		switch {
		case maxVersion == 0:
			// An empty history and either family present is not a checkpoint: the
			// relations are created by current v2, whose row does not exist.
			return finish(accessEvidenceStartFreshEmpty, accessEvidenceV9FreshVerify)
		case maxVersion == 1:
			// v1 is tenancy only. Its row requires BOTH families absent, and the
			// verification below is what enforces that rather than the case label —
			// a v1 checkpoint with the access-evidence relations precreated used to
			// be admitted here and then met conflicting v2 DDL.
			return finish(accessEvidenceStartFreshPreV2, accessEvidenceV9FreshVerify)
		case directory == coreDirectoryInitiallyPresent && evidence == accessEvidenceInitiallyPresent:
			return finish(accessEvidenceStartFreshCurrentPreV6, accessEvidenceV9FreshVerify)
		case directory == coreDirectoryInitiallyAbsent && evidence == accessEvidenceInitiallyAbsent:
			// The allowlisted legacy profile: an old core-only registrar whose v2
			// created neither the directory nor the access-evidence relations. v6
			// records its direct-v7 start, v7 completes it, and v9 materializes.
			// verifyAllowlistedLegacySourceProfile is what makes that sentence true
			// rather than a comment.
			return finish(accessEvidenceStartLegacyPreV6, accessEvidenceV9DirectMaterialize)
		default:
			return plan, fmt.Errorf(
				"%w: a pre-v6 database with directory relations %s and access-evidence relations %s is not an authored start; no migration of this product commits that pair",
				ErrGuardManifestNoEdge, directory, evidence)
		}
	}

	// v6 or later: the control plane exists and its history decides.
	if !controlPlane {
		return plan, fmt.Errorf(
			"%w: core v%d is recorded and the guard control-plane relations are absent",
			ErrGuardControlPlaneBootstrapInconsistent, guardControlPlaneVersion)
	}
	// ON THE READ TRANSACTION, not on mdb. Two reasons, and the first is a deadlock this
	// function met the moment its transaction stopped being closed early: SQLite boots
	// with MaxConns=1, so a query issued on the pool while this transaction holds the one
	// connection waits for a connection that only this transaction can return. The second
	// is the reason to prefer it anyway — every fact this classification is built from
	// then comes from one consistent view.
	standing, history, err := selectGuardEditionStanding(ctx, tx, dia, graph)
	if err != nil {
		return plan, err
	}
	plan.StandingEpoch = standing

	if maxVersion < coreDirectoryMigrationVersion {
		// v6 committed, v7 has not.
		switch {
		case history.Kind == guardEditionHistoryDirectStarted && standing == graph.Current &&
			directory == coreDirectoryInitiallyAbsent && evidence == accessEvidenceInitiallyAbsent:
			return finish(accessEvidenceStartDirectPendingV7, accessEvidenceV9DirectMaterialize)
		case history.Kind == guardEditionHistoryCurrent && standing == graph.Current &&
			directory == coreDirectoryInitiallyPresent && evidence == accessEvidenceInitiallyPresent:
			return finish(accessEvidenceStartFreshPendingV7, accessEvidenceV9FreshVerify)
		case history.Kind == guardEditionHistoryPredecessor && standing == 1 &&
			directory == coreDirectoryInitiallyAbsent && evidence == accessEvidenceInitiallyAbsent:
			// K2-style: an epoch-1 bootstrap whose v7 has not run. The history is read
			// from edition 2's perspective, which is why its kind is "predecessor" and
			// its standing is 1. v7 will cross the directory edge into edition 2, and
			// v9 then has edition 2 as the exact source of its access-evidence edge.
			plan.PredecessorEpoch = 2
			return finish(accessEvidenceStartGuardedPendingV7, accessEvidenceV9GuardedTransition)
		default:
			return plan, fmt.Errorf(
				"%w: a v6-tracked database standing at edition %d with history %q, directory %s and access-evidence %s is not an authored pre-v7 start",
				ErrGuardManifestNoEdge, standing, history.Kind, directory, evidence)
		}
	} else {
		// v7 committed, with v8 either next or complete, and v9 absent.
		if directory != coreDirectoryInitiallyPresent {
			return plan, fmt.Errorf(
				"%w: core v%d is recorded and its directory relations are %s",
				ErrGuardManifestNoEdge, coreDirectoryMigrationVersion, directory)
		}
		switch {
		case history.Kind == guardEditionHistoryDirectCompleted && standing == graph.Current &&
			evidence == accessEvidenceInitiallyAbsent:
			return finish(accessEvidenceStartDirectPendingV9, accessEvidenceV9DirectMaterialize)
		case history.Kind == guardEditionHistoryCurrentCompleted && standing == graph.Current &&
			evidence == accessEvidenceInitiallyPresent:
			return finish(accessEvidenceStartFreshPendingV9, accessEvidenceV9FreshVerify)
		case guardEditionHistoryCompletesV7(history.Kind) && standing < graph.Current &&
			evidence == accessEvidenceInitiallyAbsent:
			plan.PredecessorEpoch = standing
			return finish(accessEvidenceStartGuardedPendingV9, accessEvidenceV9GuardedTransition)
		case history.Kind == guardEditionHistoryCurrentV9Completed ||
			history.Kind == guardEditionHistoryDirectV9Completed:
			// A v9 completion seal with no v9 tracking row. Deleting the row is what
			// this seal exists to expose, so it is named rather than replayed.
			return plan, fmt.Errorf(
				"%w: the ledger carries a v9 completion seal and core v%d is not recorded; a completed migration cannot be replayed",
				ErrGuardManifestNoEdge, coreAccessEvidenceMigrationVersion)
		default:
			return plan, fmt.Errorf(
				"%w: a v7-tracked database standing at edition %d with history %q and access-evidence relations %s is not an authored pre-v9 start",
				ErrGuardManifestNoEdge, standing, history.Kind, evidence)
		}
	}
}

// accessEvidenceEdgeTarget names the one node an access-evidence edge from a source
// reaches: the source's membership plus exactly the DA delta.
//
// It refuses when that node is not in this binary's graph, and the refusal is the
// cross-profile one: a source whose module census differs from the target's has no
// same-base edge at all, and widening this is the "quietly broaden the epochs to make it
// pass" move the contract forbids.
func accessEvidenceEdgeTarget(graph guardEditionGraph, source int64) (int64, error) {
	membership, ok := guardEditionMembershipForEpoch(source)
	if !ok {
		return 0, fmt.Errorf("%w: edition %d is not a compiled edition", ErrGuardManifestNoEdge, source)
	}
	if membership.has(guardDeltaAccessEvidence) {
		return 0, fmt.Errorf("%w: edition %d already carries the access-evidence delta, so no v9 edge starts there",
			ErrGuardManifestNoEdge, source)
	}
	target, ok := guardEditionEpochForMembership(membership.with(guardDeltaAccessEvidence))
	if !ok {
		return 0, fmt.Errorf("%w: edition %d has no compiled access-evidence successor", ErrGuardManifestNoEdge, source)
	}
	if _, present := graph.node(target); !present {
		return 0, fmt.Errorf(
			"%w: this binary's census reaches edition %d, and the recorded edition %d upgrades only to edition %d, which its census does not declare; a module census change is a separate authorized transition, not an access-evidence edge",
			ErrGuardManifestNoEdge, graph.Current, source, target)
	}
	return target, nil
}

// selectGuardEditionStanding answers "which compiled edition is this database standing
// at", using the same durable selector every other consumer uses.
//
// The walk is newest-first and stops at the first edition whose history verifies, and a
// predecessor history is reported as standing at its PARENT rather than at the node whose
// perspective read it. That distinction is what the boot classifier needs: "this database
// is at edition 2" and "edition 5 can see edition 2 below it" are the same reading and
// different facts.
func selectGuardEditionStanding(
	ctx context.Context,
	q guardEditionQuerier,
	dia dialect.Dialect,
	graph guardEditionGraph,
) (int64, guardEditionHistory, error) {
	var attempts []string
	for _, epoch := range graph.epochsDescending() {
		if epoch < 2 {
			continue
		}
		node, _ := graph.node(epoch)
		history, err := verifyGuardEditionHistory(ctx, q, dia, node.Manifest)
		if err != nil {
			attempts = append(attempts, fmt.Sprintf("epoch %d: %v", epoch, err))
			continue
		}
		standing := epoch
		if history.Kind == guardEditionHistoryPredecessor || history.Kind == guardEditionHistoryPredecessorV7 {
			standing = history.ParentEpoch
		}
		return standing, history, nil
	}
	return 0, guardEditionHistory{}, fmt.Errorf(
		"%w: the guard history matches no compiled edition of this binary (%s)",
		ErrGuardManifestNoEdge, strings.Join(attempts, "; "))
}

// guardControlPlaneRelationsPresent reports whether all three control-plane relations
// exist. It reads only; the all-or-none refusal belongs to preflightGuardControlPlane,
// which runs a few statements later in the same lock.
func guardControlPlaneRelationsPresent(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
) (bool, error) {
	present := 0
	var have, missing []string
	for _, table := range dialect.GuardControlPlaneTables() {
		_, exists, err := inspectCoreDirectoryRelation(ctx, tx, dia, table)
		if err != nil {
			return false, fmt.Errorf("inspect guard control-plane relation %q: %w", table, err)
		}
		if exists {
			present++
			have = append(have, table)
		} else {
			missing = append(missing, table)
		}
	}
	switch present {
	case 0:
		return false, nil
	case len(dialect.GuardControlPlaneTables()):
		return true, nil
	default:
		return false, fmt.Errorf("%w: partial guard control plane (present=%v, absent=%v)",
			ErrGuardControlPlaneBootstrapInconsistent, have, missing)
	}
}

// coreAccessEvidenceMigration is core v9.
//
// Exec-driven for the same reason v7 is: the correct action depends on an all-or-none
// PHYSICAL prestate that only a read inside the migration transaction can establish. It is
// transactional (never NonTransactional), has no DownStmts, and its expand phase is the
// honest default — reversing it would mean dropping an evidence relation, and migrate.Revert
// refuses a forward-only migration loudly instead of pretending.
func coreAccessEvidenceMigration(
	dia dialect.Dialect,
	descs []model.EntityDescriptor,
	graph guardEditionGraph,
	boot accessEvidenceBootPlan,
) migrate.Migration {
	return migrate.Migration{
		Version: coreAccessEvidenceMigrationVersion,
		Name:    coreAccessEvidenceMigrationName,
		Exec: func(ctx context.Context, tx *sql.Tx) error {
			return ensureAccessEvidenceRelationsAndEdition(ctx, tx, dia, descs, graph, boot)
		},
	}
}

// ensureAccessEvidenceRelationsAndEdition inspects every precondition before issuing the
// first target statement, then performs exactly one authored action.
func ensureAccessEvidenceRelationsAndEdition(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	descs []model.EntityDescriptor,
	graph guardEditionGraph,
	boot accessEvidenceBootPlan,
) error {
	ordered, err := exactAccessEvidenceDescriptors(descs)
	if err != nil {
		return err
	}
	current, ok := graph.currentNode()
	if !ok {
		return fmt.Errorf("sqlstore: core v9: the compiled graph has no current edition %d", graph.Current)
	}
	if boot.CurrentEpoch != graph.Current || boot.BaseSHA256 != graph.BaseSHA256 {
		return fmt.Errorf(
			"%w: core v9 was planned against edition %d base %s and is running against edition %d base %s",
			ErrGuardManifestNoEdge, boot.CurrentEpoch, hexDigest(boot.BaseSHA256),
			graph.Current, hexDigest(graph.BaseSHA256))
	}

	// PRESTATE, transaction-local, before any statement.
	evidence, err := inspectAccessEvidenceDisposition(ctx, tx, dia)
	if err != nil {
		return fmt.Errorf("sqlstore: core v9: %w", err)
	}
	if err := requireCoreVersionTracked(ctx, tx, dia, coreLineageMigrationVersion); err != nil {
		return fmt.Errorf("sqlstore: core v9: %w", err)
	}
	if tracked, terr := coreVersionIsTracked(ctx, tx, dia, coreAccessEvidenceMigrationVersion); terr != nil {
		return fmt.Errorf("sqlstore: core v9: %w", terr)
	} else if tracked {
		return fmt.Errorf("%w: core v9 is already recorded; a completed migration is never replayed",
			ErrGuardManifestNoEdge)
	}

	switch boot.Action {
	case accessEvidenceV9FreshVerify:
		return accessEvidenceFreshVerifyAndSeal(ctx, tx, dia, ordered, current.Manifest, evidence)
	case accessEvidenceV9DirectMaterialize:
		return accessEvidenceDirectMaterializeAndSeal(ctx, tx, dia, ordered, current.Manifest, evidence)
	case accessEvidenceV9GuardedTransition:
		return accessEvidenceGuardedTransition(ctx, tx, dia, ordered, graph, boot, evidence)
	default:
		return fmt.Errorf("%w: core v9 has no authored action for boot plan %s", ErrGuardManifestNoEdge, boot)
	}
}

func accessEvidenceFreshVerifyAndSeal(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	ordered []model.EntityDescriptor,
	current guardManifest,
	evidence accessEvidenceRelationDisposition,
) error {
	if evidence != accessEvidenceInitiallyPresent {
		return fmt.Errorf(
			"%w: the fresh v9 action requires the four access-evidence relations current v2 created, and they are %s; a fresh history with the relations deleted is damage, not a retry state",
			ErrGuardManifestNoEdge, evidence)
	}
	history, err := verifyGuardEditionHistory(ctx, tx, dia, current)
	if err != nil {
		return err
	}
	if history.Kind != guardEditionHistoryCurrentCompleted {
		return fmt.Errorf("%w: the fresh v9 action requires history %q, got %q",
			ErrGuardManifestNoEdge, guardEditionHistoryCurrentCompleted, history.Kind)
	}
	if err := verifyAccessEvidenceRelationsInTx(ctx, tx, dia, ordered); err != nil {
		return err
	}
	return appendAccessEvidenceV9Seal(ctx, tx, dia, current, history,
		guardEditionHistoryCurrentV9Completed, false)
}

func accessEvidenceDirectMaterializeAndSeal(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	ordered []model.EntityDescriptor,
	current guardManifest,
	evidence accessEvidenceRelationDisposition,
) error {
	if evidence != accessEvidenceInitiallyAbsent {
		return fmt.Errorf(
			"%w: the direct v9 action requires the access-evidence relations to be absent and they are %s; no authored direct path creates them before v9",
			ErrGuardManifestNoEdge, evidence)
	}
	history, err := verifyGuardEditionHistory(ctx, tx, dia, current)
	if err != nil {
		return err
	}
	if history.Kind != guardEditionHistoryDirectCompleted {
		return fmt.Errorf("%w: the direct v9 action requires history %q, got %q",
			ErrGuardManifestNoEdge, guardEditionHistoryDirectCompleted, history.Kind)
	}
	if err := createAccessEvidenceRelations(ctx, tx, dia, ordered); err != nil {
		return err
	}
	return appendAccessEvidenceV9Seal(ctx, tx, dia, current, history,
		guardEditionHistoryDirectV9Completed, true)
}

func accessEvidenceGuardedTransition(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	ordered []model.EntityDescriptor,
	graph guardEditionGraph,
	boot accessEvidenceBootPlan,
	evidence accessEvidenceRelationDisposition,
) error {
	if evidence != accessEvidenceInitiallyAbsent {
		return fmt.Errorf(
			"%w: a guarded access-evidence transition requires the four relations to be absent and they are %s; one to four of them present is never a retry state, because a transactional v9 could not have committed it",
			ErrGuardManifestNoEdge, evidence)
	}
	target, ok := graph.node(boot.TargetEpoch)
	if !ok {
		return fmt.Errorf("%w: the planned access-evidence target edition %d is not in the compiled graph",
			ErrGuardManifestNoEdge, boot.TargetEpoch)
	}
	edge, err := graph.edge(boot.PredecessorEpoch, boot.TargetEpoch)
	if err != nil {
		return err
	}
	delta, err := guardEditionEdgeDelta(edge.From.CodeEpoch, edge.To.CodeEpoch)
	if err != nil {
		return err
	}
	if delta != guardDeltaAccessEvidence {
		return fmt.Errorf("%w: core v9 may only cross an access-evidence edge, and %d -> %d adds %s",
			ErrGuardManifestNoEdge, edge.From.CodeEpoch, edge.To.CodeEpoch, delta)
	}
	// THE PRE-STATE IS RE-VALIDATED HERE, in v9's own transaction, against the target
	// node's perspective. The predecessor's rollout readiness was established before this
	// transaction opened (see convergeGuardEditionPredecessorForV9); this is the re-read
	// that correlates it under the same lock.
	history, err := verifyGuardEditionHistory(ctx, tx, dia, target.Manifest)
	if err != nil {
		return err
	}
	if history.Kind != guardEditionHistoryPredecessorV7 {
		return fmt.Errorf("%w: a guarded access-evidence transition requires predecessor history %q, got %q",
			ErrGuardManifestNoEdge, guardEditionHistoryPredecessorV7, history.Kind)
	}
	if history.ParentEpoch != boot.PredecessorEpoch {
		return fmt.Errorf("%w: the durable history stands at edition %d and this boot planned its access-evidence edge from %d",
			ErrGuardManifestNoEdge, history.ParentEpoch, boot.PredecessorEpoch)
	}
	if err := createAccessEvidenceRelations(ctx, tx, dia, ordered); err != nil {
		return err
	}

	var plans []guardUnitPlan
	openCurrent := dia.Name() == store.EnginePostgres
	if openCurrent {
		if err := verifyGuardFenceCapability(ctx, tx); err != nil {
			return err
		}
		keys := make([]guardKey, 0, len(target.Manifest.Specs))
		for _, spec := range target.Manifest.Specs {
			keys = append(keys, spec.Key)
		}
		observed, perr := projectGuardCatalogBatch(ctx, tx, keys)
		if perr != nil {
			return perr
		}
		var refusals []guardPlanRefusal
		plans, refusals, perr = buildGuardUnitPlans(target.Manifest, observed)
		if perr != nil {
			return perr
		}
		if len(refusals) != 0 {
			return fmt.Errorf("sqlstore: core v9 cannot open the epoch-%d rollout: %d of %d targets have no authorized plan (first: %s)",
				target.Epoch, len(refusals), len(target.Manifest.Specs), refusals[0])
		}
	}
	return transitionGuardEditionInTx(ctx, tx, dia, target.Manifest, edge, history, plans, openCurrent)
}

// createAccessEvidenceRelations renders the four relations from the SAME descriptor
// renderer v2 uses and then reads back the complete contract before returning. Successful
// DDL is not taken on faith: the transaction proves what it made before the tracking row
// or any ledger row can become durable.
func createAccessEvidenceRelations(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	ordered []model.EntityDescriptor,
) error {
	for _, desc := range ordered {
		for statement, stmt := range dia.CreateTableStmts(desc) {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("core access evidence v9: create %q statement %d: %w",
					desc.Table, statement+1, err)
			}
		}
	}
	return verifyAccessEvidenceRelationsInTx(ctx, tx, dia, ordered)
}

// verifyAccessEvidenceRelationsInTx is the exact, non-healing contract check: the relation
// exists, its columns are exactly the descriptor's, and every object columns cannot speak
// for — CHECKs, indexes, tenant isolation controls and the append-only guard — matches
// what the descriptor renders on this engine.
//
// It reuses the verifier v7 already uses for the directory relations rather than growing a
// second implementation. An expectation written separately from the DDL it expects is free
// to drift, and the first symptom of that drift is a correct database refusing to boot.
func verifyAccessEvidenceRelationsInTx(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	ordered []model.EntityDescriptor,
) error {
	for _, desc := range ordered {
		shape, exists, err := inspectCoreDirectoryRelation(ctx, tx, dia, desc.Table)
		if err != nil {
			return fmt.Errorf("core access evidence v9: inspect %q: %w", desc.Table, err)
		}
		if !exists {
			return fmt.Errorf("core access evidence v9: relation %q is absent", desc.Table)
		}
		if err := verifyCoreDirectoryRelationShape(dia, desc, shape); err != nil {
			return fmt.Errorf("core access evidence v9: relation %q is malformed: %w", desc.Table, err)
		}
		if err := verifyCoreDirectoryRelationContract(ctx, tx, dia, desc); err != nil {
			return fmt.Errorf("core access evidence v9: relation %q violates its descriptor contract: %w",
				desc.Table, err)
		}
	}
	return nil
}

func appendAccessEvidenceV9Seal(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	current guardManifest,
	history guardEditionHistory,
	want guardEditionHistoryKind,
	followsDirectStart bool,
) error {
	seal, err := guardAccessEvidenceV9Seal(current, history.TerminalReceiptID, followsDirectStart)
	if err != nil {
		return err
	}
	if _, err := insertGuardReceipt(ctx, tx, dia, seal); err != nil {
		return err
	}
	post, err := verifyGuardEditionHistory(ctx, tx, dia, current)
	if err != nil {
		return fmt.Errorf("sqlstore: verify v9 completion before commit: %w", err)
	}
	if post.Kind != want {
		return fmt.Errorf("%w: v9 completion produced history %q, want %q",
			ErrGuardManifestNoEdge, post.Kind, want)
	}
	return nil
}

// coreVersionIsTracked answers whether one core version has a tracking row, on the
// caller's transaction.
func coreVersionIsTracked(
	ctx context.Context,
	q dialect.Querier,
	dia dialect.Dialect,
	version int,
) (bool, error) {
	rows, err := q.QueryContext(ctx, dia.Rebind(
		"SELECT COUNT(*) FROM "+coreTrackingRelation(dia)+" WHERE version = ?"), version) // #nosec G202 -- internal constants
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return false, rows.Err()
	}
	var count int
	if err := rows.Scan(&count); err != nil {
		return false, err
	}
	return count == 1, rows.Err()
}

func requireCoreVersionTracked(
	ctx context.Context,
	q dialect.Querier,
	dia dialect.Dialect,
	version int,
) error {
	tracked, err := coreVersionIsTracked(ctx, q, dia, version)
	if err != nil {
		return err
	}
	if !tracked {
		return fmt.Errorf("%w: core v%d must be recorded before v%d opens its transaction",
			ErrGuardManifestNoEdge, version, coreAccessEvidenceMigrationVersion)
	}
	return nil
}

// verifyAccessEvidenceRelationsExact is the per-boot, non-healing verification. It runs in
// the read-only pre-reconcile section of every Open, BEFORE reconcileColumns, so an absent
// or altered relation is reported against v9 rather than silently recreated as ordinary
// schema growth.
//
// Generic reconciliation may continue for other descriptors; it must never be the
// access-evidence creation or repair path once v9 is tracked, and running this first is
// what makes that true rather than merely intended.
func verifyAccessEvidenceRelationsExact(
	ctx context.Context,
	db dialect.Execer,
	dia dialect.Dialect,
	descs []model.EntityDescriptor,
) error {
	ordered, err := exactAccessEvidenceDescriptors(descs)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("core access evidence per-boot verification: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // verification commits no state
	for _, desc := range ordered {
		shape, exists, ierr := inspectCoreDirectoryRelation(ctx, tx, dia, desc.Table)
		if ierr != nil {
			return fmt.Errorf("core access evidence per-boot verification: inspect %q: %w", desc.Table, ierr)
		}
		if !exists {
			return fmt.Errorf("core access evidence per-boot verification: relation %q is absent; core v%d recorded its creation and this boot does not recreate it",
				desc.Table, coreAccessEvidenceMigrationVersion)
		}
		if verr := verifyCoreDirectoryRelationShape(dia, desc, shape); verr != nil {
			return fmt.Errorf("core access evidence per-boot verification: relation %q is malformed: %w", desc.Table, verr)
		}
		if verr := verifyCoreDirectoryRelationContract(ctx, tx, dia, desc); verr != nil {
			return fmt.Errorf("core access evidence per-boot verification: relation %q violates its descriptor contract: %w",
				desc.Table, verr)
		}
	}
	return nil
}

// convergeGuardEditionPredecessorForV9 is amendment R2-G1: the predecessor-readiness
// barrier that runs between core v8 and core v9, under the SAME migration lock.
//
// THE DEFECT IT EXISTS FOR, measured on the real K2 path. Core v7 completes the directory
// edge E1 -> E2 inside its own transaction, and on PostgreSQL that transaction commits the
// activations and the E2 seal together with a PENDING E2 gate — not a verified E2 rollout.
// Core v8 does not close it. So the next edge arrives with its immediate predecessor
// pending, and the guards this contract deliberately keeps refuse exactly that state for
// crossing another edge. Completing the v7 MIGRATION and completing the E2 ROLLOUT it
// opened are two different facts, and only the first had happened.
//
// WHAT IT MAY AND MAY NOT DO. It admits only the pending gate the exact recorded history
// wrote, and refers it to the ordinary coordinator — the same units, roles, ACL checks and
// fence a normal boot would use. It never fabricates readiness, never moves the full
// rollout closure inside the v9 transaction, and never touches the TARGET census, whose
// access-evidence relations do not exist yet. A ready label with no closing checkpoint, or
// a pending gate that the recorded history did not write, refuses here.
//
// Fresh and direct plans deliberately do NOT reach this barrier. They cross no edge from an
// earlier edition, and a direct upgrade must create its relations in v9 BEFORE its guards
// can be observed at all — so a barrier that tried to converge them would be asking a
// rollout to attest objects no migration has made.
func convergeGuardEditionPredecessorForV9(
	ctx context.Context,
	mdb dialect.Execer,
	dia dialect.Dialect,
	graph guardEditionGraph,
	plan accessEvidenceBootPlan,
	observerDSN string,
	hardened bool,
	session func(ctx context.Context) (rowQuerier, func(), error),
) error {
	if plan.Action != accessEvidenceV9GuardedTransition {
		return nil
	}
	node, ok := graph.node(plan.PredecessorEpoch)
	if !ok {
		return fmt.Errorf("%w: the planned access-evidence predecessor edition %d is not in the compiled graph",
			ErrGuardManifestNoEdge, plan.PredecessorEpoch)
	}
	history, err := verifyGuardEditionHistory(ctx, mdb, dia, node.Manifest)
	if err != nil {
		return fmt.Errorf("sqlstore: core v9 predecessor barrier: edition %d does not verify: %w",
			plan.PredecessorEpoch, err)
	}
	if !guardEditionHistoryCompletesV7(history.Kind) {
		return fmt.Errorf("%w: core v9 predecessor barrier: edition %d has history %q, which carries no completed v7 witness",
			ErrGuardManifestNoEdge, plan.PredecessorEpoch, history.Kind)
	}
	if dia.Name() != store.EnginePostgres {
		// SQLite has no gate and no unit runner. Its edition activation and seal are
		// still each their own transaction, and the history check above is the exact
		// witness this barrier can obtain; inventing a rollout here would be a label.
		return nil
	}

	gate := history.Gate
	if gate.Phase != gatePhaseReady || gate.Condition != gateConditionVerified {
		// Only the pending gate this exact history wrote may be continued. Anything
		// else — a blocked rollout, a condition this engine never writes, a ready
		// label without its closing checkpoint — is refused rather than repaired.
		if gate.Phase != gatePhasePending || gate.Condition != gateConditionClean {
			return fmt.Errorf("%w: core v9 predecessor barrier: edition %d rollout is %s/%s; only the clean pending gate its own transition wrote may be continued",
				ErrGuardManifestNoEdge, plan.PredecessorEpoch, gate.Phase, gate.Condition)
		}
		tables := make([]string, 0, len(node.Manifest.Specs))
		for _, spec := range node.Manifest.Specs {
			tables = append(tables, spec.Key.Relation)
		}
		if _, rerr := runAppendOnlyGuardUnits(ctx, mdb, dia, tables, observerDSN, hardened, session); rerr != nil {
			return fmt.Errorf("sqlstore: core v9 predecessor barrier: converge the edition-%d rollout: %w",
				plan.PredecessorEpoch, rerr)
		}
		history, err = verifyGuardEditionHistory(ctx, mdb, dia, node.Manifest)
		if err != nil {
			return fmt.Errorf("sqlstore: core v9 predecessor barrier: re-read edition %d after convergence: %w",
				plan.PredecessorEpoch, err)
		}
		gate = history.Gate
	}

	// RE-READ AND CORRELATE UNDER THE LOCK, through the same historical checkpoint
	// verifier the edge itself will use. A ready label alone is not evidence: this
	// rebuilds the bijective unit enumeration, re-reads every receipt, re-projects every
	// terminal object and compares both attested stream heads.
	rollout, err := guardBootstrapRollout(node.Manifest, history.Revision, history.Retained)
	if err != nil {
		return err
	}
	q, ok := mdb.(guardEditionQuerier)
	if !ok {
		return fmt.Errorf("sqlstore: core v9 predecessor barrier: the migration executor is %T, which cannot read the guard ledger", mdb)
	}
	if err := verifyGuardHistoricalCheckpoint(ctx, q, dia, node.Manifest, rollout, gate); err != nil {
		return fmt.Errorf("sqlstore: core v9 predecessor barrier: %w", err)
	}
	return nil
}
