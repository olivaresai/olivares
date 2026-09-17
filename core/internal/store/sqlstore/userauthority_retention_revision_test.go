// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// THE REVISION FENCE of the retention rendering equivalence.
//
// The equivalence accepts a SECOND complete framed definition for one invariant, and that
// permission belongs to ONE measured revision of the retention body. The fence is what stops it
// from outliving that revision: when the compiled canonical declaration becomes a body nobody
// measured, the old qualified companion must stop being eligible AND the new declaration must
// fall back to ordinary exact equality.
//
// Before the correction the fence was a tautology — the gate compared the declaration with the
// very constant that produced it, so a revision bump moved both operands together and the old
// qualified body stayed accepted. The cases here are written against the ACTUAL registered
// declaration and the ACTUAL caller (schemaInvariantViolation), because that coupling is
// invisible to a comparator called with a hand-built descriptor.

// measuredRetentionNextRevisionFunctionDef is a PLAUSIBLE NEXT retention body: the same handler
// under the same name, with the message changed. It stands for "the revision after the measured
// one" and nothing more — no migration renders it and no catalog was measured for it, which is
// exactly the state the fence has to be safe in.
const measuredRetentionNextRevisionFunctionDef = "CREATE OR REPLACE FUNCTION public.olivares_retain_user_authority()\n" +
	" RETURNS trigger\n LANGUAGE plpgsql\n SET search_path TO 'pg_catalog'\n" +
	"AS $function$\nBEGIN\n  RAISE EXCEPTION 'User authority is permanent and cannot be removed';\nEND;\n$function$\n"

// registeredCoreRetentionDeclaration is the declaration the store REGISTERS, taken through
// core's own registration path rather than retyped or hand-built. A future revision reaches
// these cases by changing the compiled constant, which is the whole point of reading it here.
func registeredCoreRetentionDeclaration(t *testing.T) registeredSchemaTrigger {
	t.Helper()
	reg := newRegistry()
	if err := reg.registerCoreUserAuthorityInvariants(); err != nil {
		t.Fatalf("register core's own invariants: %v", err)
	}
	invariants := reg.schemaInvariants(store.EnginePostgres)
	if len(invariants) != 1 {
		t.Fatalf("core registered %d PostgreSQL invariants, want exactly the retention guard", len(invariants))
	}
	return invariants[0]
}

// retentionDeclarationForRevision is the compiled core declaration with exactly ONE field
// replaced: the canonical digest a build compiles in. Everything else — namespace, table,
// trigger name — is the registered object, so a case built from it differs from production in
// the one way a new retention revision differs.
func retentionDeclarationForRevision(canonical string) registeredSchemaTrigger {
	required := coreRetentionInvariant()
	required.DefinitionSHA256 = canonical
	return required
}

// retentionSelfTestVerdict runs the REAL caller over one live catalog reading of the retention
// trigger, so the verdict is the one a boot would get rather than the comparator's boolean.
func retentionSelfTestVerdict(required registeredSchemaTrigger, info dialect.TriggerInfo) error {
	return schemaInvariantViolation(
		dialect.EngineSchema,
		dialect.RolePosture{},
		map[dialect.TriggerKey]dialect.TriggerInfo{coreRetentionKey(): info},
		[]registeredSchemaTrigger{required},
		nil,
		store.EnginePostgres,
		false,
	)
}

// TestUserAuthorityRetentionMeasuredPairIsIndependentOfTheCurrentDeclaration is THE CAUSAL, and
// it is written so that the compiled declaration itself is the variable.
//
// Run against the delivered build, the compiled declaration IS the measured revision, and the
// case asserts the positive: the qualified companion is accepted through the registered
// declaration and the real caller.
//
// Run against a build whose canonical constant was changed — a simulated next compiled revision,
// which is the ONLY way to move the declaration without editing a copy of it — the same case
// asserts the fence: the old qualified body must be refused as tampered. Under the
// pre-correction rule that assertion fails with error=<nil>, because the gate compared the
// declaration with the constant that produced it.
func TestUserAuthorityRetentionMeasuredPairIsIndependentOfTheCurrentDeclaration(t *testing.T) {
	t.Parallel()
	required := registeredCoreRetentionDeclaration(t)
	qualified := framedRetentionDefinition(measuredRetentionTriggerQualified, measuredRetentionFunctionDef)
	info := coreRetentionInfo(qualified)
	err := retentionSelfTestVerdict(required, info)

	if required.DefinitionSHA256 == postgresRetentionMeasuredRevisionDigest {
		if err != nil {
			t.Fatalf("CURRENT_REVISION refused its own measured qualified companion: declared=%s live=%s error=%v",
				required.DefinitionSHA256, digestOf(qualified), err)
		}
		t.Logf("CURRENT_REVISION|declared=%s|live=%s|accepted", required.DefinitionSHA256, digestOf(qualified))
		return
	}
	if !errors.Is(err, store.ErrSchemaTriggerTampered) {
		t.Fatalf("CHANGED_COMPILED_REVISION accepted old qualified body: declared=%s old=%s error=%v",
			required.DefinitionSHA256, digestOf(qualified), err)
	}
	t.Logf("CHANGED_COMPILED_REVISION|declared=%s|old=%s|refused=%v",
		required.DefinitionSHA256, digestOf(qualified), err)
}

// TestUserAuthorityRetentionNextCompiledRevisionFallsBackToExactEquality states what a new
// declared revision GETS, not only what it loses.
//
// It is a fence property rather than a causal — the pre-correction build also passes it, because
// changing a copy of the declaration is not the coupling that failed. It is here so the fence
// cannot later be widened (to the trigger key alone, say) without a red test, and because
// "falls back to ordinary exact equality" is a claim that has to be measured on all four
// readings a revision bump can produce.
func TestUserAuthorityRetentionNextCompiledRevisionFallsBackToExactEquality(t *testing.T) {
	t.Parallel()
	nextCanonical := framedRetentionDefinition(measuredRetentionTriggerCanonical, measuredRetentionNextRevisionFunctionDef)
	nextQualified := framedRetentionDefinition(measuredRetentionTriggerQualified, measuredRetentionNextRevisionFunctionDef)
	oldCanonical := framedRetentionDefinition(measuredRetentionTriggerCanonical, measuredRetentionFunctionDef)
	oldQualified := framedRetentionDefinition(measuredRetentionTriggerQualified, measuredRetentionFunctionDef)

	// The fixture would be meaningless if the simulated body collided with a measured one.
	for _, collision := range []string{
		postgresRetentionMeasuredRevisionDigest,
		postgresRetentionMeasuredRevisionQualifiedDigest,
	} {
		if digestOf(nextCanonical) == collision || digestOf(nextQualified) == collision {
			t.Fatalf("the simulated next revision hashes to a measured value (%s): the fixture is wrong", collision)
		}
	}
	required := retentionDeclarationForRevision(digestOf(nextCanonical))

	for _, tc := range []struct {
		name       string
		definition string
		wantErr    error
	}{
		{
			name:       "the new declared body, canonical rendering",
			definition: nextCanonical,
		},
		{
			name: "the new declared body, qualified rendering — no companion has been measured " +
				"for this revision yet",
			definition: nextQualified,
			wantErr:    store.ErrSchemaTriggerTampered,
		},
		{
			name:       "the OLD qualified companion, which must not be carried forward",
			definition: oldQualified,
			wantErr:    store.ErrSchemaTriggerTampered,
		},
		{
			name:       "the OLD canonical body, which is now simply a different definition",
			definition: oldCanonical,
			wantErr:    store.ErrSchemaTriggerTampered,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := retentionSelfTestVerdict(required, coreRetentionInfo(tc.definition))
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("the new declaration refused its own body: declared=%s live=%s error=%v",
						required.DefinitionSHA256, digestOf(tc.definition), err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("declared=%s live=%s error=%v, want %v",
					required.DefinitionSHA256, digestOf(tc.definition), err, tc.wantErr)
			}
		})
	}
}

// TestUserAuthorityRetentionBothAcceptedFormsKeepTheirIdentityThroughTheCaller is the ratified
// both-form structural identity, asserted at the caller rather than only at the pure comparator.
//
// The tgfoid-derived handler identity is required for the canonical rendering exactly as it is
// for the qualified one. Asserting it here as well matters because it is the caller that turns a
// false into ErrSchemaTriggerTampered, and because a refusal on identity alone is the one arm
// whose diagnostic prints two equal digests.
func TestUserAuthorityRetentionBothAcceptedFormsKeepTheirIdentityThroughTheCaller(t *testing.T) {
	t.Parallel()
	required := registeredCoreRetentionDeclaration(t)
	for _, form := range []struct {
		name       string
		triggerDef string
	}{
		{name: "canonical rendering", triggerDef: measuredRetentionTriggerCanonical},
		{name: "qualified rendering", triggerDef: measuredRetentionTriggerQualified},
	} {
		t.Run(form.name, func(t *testing.T) {
			t.Parallel()
			definition := framedRetentionDefinition(form.triggerDef, measuredRetentionFunctionDef)
			if err := retentionSelfTestVerdict(required, coreRetentionInfo(definition)); err != nil {
				t.Fatalf("the measured %s was refused: %v", form.name, err)
			}
			for _, identity := range []struct {
				name           string
				functionSchema string
				functionName   string
			}{
				{name: "no structural identity at all"},
				{
					name:           "a handler bound in another schema",
					functionSchema: "review_wrong",
					functionName:   userAuthorityRetentionFunction,
				},
				{
					name:           "a handler bound under another name",
					functionSchema: dialect.EngineSchema,
					functionName:   "review_wrong_handler",
				},
			} {
				info := coreRetentionInfo(definition)
				info.FunctionSchema = identity.functionSchema
				info.FunctionName = identity.functionName
				err := retentionSelfTestVerdict(required, info)
				if !errors.Is(err, store.ErrSchemaTriggerTampered) {
					t.Errorf("%s with %s: error=%v, want %v",
						form.name, identity.name, err, store.ErrSchemaTriggerTampered)
				}
			}
		})
	}
}

// TestUserAuthorityRetentionCurrentDeclarationIsTheMeasuredRevision is the TRIPWIRE, and it is
// meant to fail the day someone bumps the retention body.
//
// The equivalence is only sound while the compiled declaration is the revision the pair was
// measured on. The fence makes an un-remeasured bump SAFE — the alternative stops applying — but
// silently losing the equivalence would also silently reintroduce the boot refusal a
// zero-callable overload causes. So the bump has to be noticed: this case names the two
// constants a new revision must be remeasured against.
func TestUserAuthorityRetentionCurrentDeclarationIsTheMeasuredRevision(t *testing.T) {
	t.Parallel()
	required := registeredCoreRetentionDeclaration(t)
	if required.DefinitionSHA256 != postgresRetentionMeasuredRevisionDigest {
		t.Fatalf("the compiled retention declaration is %s and the measured revision is %s: "+
			"remeasure BOTH renderings of the new body on PostgreSQL 16.15 and register the pair "+
			"deliberately, or drop the equivalence",
			required.DefinitionSHA256, postgresRetentionMeasuredRevisionDigest)
	}
	if !isMeasuredPostgresUserAuthorityRetentionRevision(store.EnginePostgres, coreRetentionKey(), required) {
		t.Fatal("the registered core declaration is not inside the measured revision's gate")
	}
	next := retentionDeclarationForRevision(digestOf("a later retention revision"))
	if isMeasuredPostgresUserAuthorityRetentionRevision(store.EnginePostgres, coreRetentionKey(), next) {
		t.Fatal("a declaration of another revision was admitted to the measured revision's gate")
	}
}
