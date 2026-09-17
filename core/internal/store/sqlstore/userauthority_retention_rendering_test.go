// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"fmt"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// THE PURE HALF of the retention rendering equivalence.
//
// schemaInvariantDefinitionMatches decides which complete catalog definitions the core
// retention invariant accepts, and it is a pure function precisely so its whole gate — engine,
// namespace, the three key fields, the declared revision, and the tgfoid-derived handler
// identity — runs without a server. What it CANNOT prove is that PostgreSQL renders the bytes
// these constants transcribe; that is
// TestPostgresUserAuthorityRetentionRenderingPinsBothMeasuredForms, which reads them back out
// of a real 16.15 catalog through dialect.SchemaTriggers and compares them BYTE FOR BYTE with
// the strings below. Neither half is sufficient alone.

// The exact catalog renderings measured on PostgreSQL 16.15 (Debian 16.15-1.pgdg12+2,
// server_version_num 160015). The trigger texts are pg_get_triggerdef(oid,false); the function
// text is pg_get_functiondef of the OID pg_trigger.tgfoid holds, and it is ONE constant because
// the two renderings share it byte for byte — that identity is the whole finding.
const (
	measuredRetentionFunctionDef = "CREATE OR REPLACE FUNCTION public.olivares_retain_user_authority()\n" +
		" RETURNS trigger\n LANGUAGE plpgsql\n SET search_path TO 'pg_catalog'\n" +
		"AS $function$\nBEGIN\n  RAISE EXCEPTION 'User authority is permanent';\nEND;\n$function$\n"

	measuredRetentionTriggerCanonical = "CREATE TRIGGER core_user_authority_no_delete BEFORE DELETE OR TRUNCATE " +
		"ON public.core_user_authority FOR EACH STATEMENT EXECUTE FUNCTION olivares_retain_user_authority()"

	measuredRetentionTriggerQualified = "CREATE TRIGGER core_user_authority_no_delete BEFORE DELETE OR TRUNCATE " +
		"ON public.core_user_authority FOR EACH STATEMENT EXECUTE FUNCTION public.olivares_retain_user_authority()"
)

// framedRetentionDefinition mirrors dialect.postgresTriggerDefinition, which is unexported in
// another package. Mirroring a format is normally how two copies drift apart, so it is not left
// as an assumption: the PostgreSQL leg asserts that a real SchemaTriggers read produces exactly
// this string for both renderings, which fails loudly if either side changes.
func framedRetentionDefinition(triggerDef, functionDef string) string {
	return fmt.Sprintf("trigger:%d:%sfunction:%d:%s",
		len(triggerDef), triggerDef, len(functionDef), functionDef)
}

// coreRetentionKey is the full catalog identity of the invariant, as the self-test keys it.
func coreRetentionKey() dialect.TriggerKey {
	return dialect.TriggerKey{
		Schema: dialect.EngineSchema,
		Table:  userAuthorityRetentionTable,
		Name:   userAuthorityRetentionTriggerName,
	}
}

// coreRetentionInvariant is the DECLARATION under test, taken from the compiled table rather
// than retyped, so a revision that remeasures the canonical digest reaches these cases.
func coreRetentionInvariant() registeredSchemaTrigger {
	return registeredSchemaTrigger{
		namespace:     coreSchemaInvariantNamespace,
		SchemaTrigger: userAuthoritySchemaInvariants()[store.EnginePostgres][0],
	}
}

// coreRetentionInfo is a live catalog reading whose structural handler identity is the bound
// zero-input routine — the state every accepted form must also satisfy.
func coreRetentionInfo(definition string) dialect.TriggerInfo {
	return dialect.TriggerInfo{
		Function:       "public.olivares_retain_user_authority()",
		FunctionSchema: dialect.EngineSchema,
		FunctionName:   userAuthorityRetentionFunction,
		CanExecute:     true,
		EnableState:    dialect.TriggerFiresAlways,
		Definition:     definition,
	}
}

func acceptsRetentionDefinition(t *testing.T, info dialect.TriggerInfo) bool {
	t.Helper()
	return schemaInvariantDefinitionMatches(
		store.EnginePostgres, coreRetentionKey(), coreRetentionInvariant(),
		info, digestOf(info.Definition))
}

// TestUserAuthorityRetentionMeasuredRenderingsHashToTheDeclaredPair is the transcription check.
//
// Everything else in this file is written in terms of the two measured strings, so a mistyped
// byte would quietly turn each following case into a test of a definition PostgreSQL never
// emits. This case fails first and says so.
func TestUserAuthorityRetentionMeasuredRenderingsHashToTheDeclaredPair(t *testing.T) {
	t.Parallel()
	canonical := framedRetentionDefinition(measuredRetentionTriggerCanonical, measuredRetentionFunctionDef)
	qualified := framedRetentionDefinition(measuredRetentionTriggerQualified, measuredRetentionFunctionDef)

	if got := digestOf(canonical); got != postgresRetentionMeasuredRevisionDigest {
		t.Errorf("the canonical rendering hashes to %s, and the measured revision is %s",
			got, postgresRetentionMeasuredRevisionDigest)
	}
	if got := digestOf(qualified); got != postgresRetentionMeasuredRevisionQualifiedDigest {
		t.Errorf("the qualified rendering hashes to %s, and the companion constant is %s",
			got, postgresRetentionMeasuredRevisionQualifiedDigest)
	}
	// The byte lengths are asserted because they are what the frame carries, and because the
	// pair being SEVEN bytes apart — exactly `public.` — is the finding in its smallest form.
	if len(measuredRetentionTriggerCanonical) != 169 || len(measuredRetentionTriggerQualified) != 176 {
		t.Errorf("the measured trigger renderings are %d and %d bytes, want 169 and 176",
			len(measuredRetentionTriggerCanonical), len(measuredRetentionTriggerQualified))
	}
	if len(measuredRetentionFunctionDef) != 220 {
		t.Errorf("the measured function rendering is %d bytes, want 220", len(measuredRetentionFunctionDef))
	}
	if postgresRetentionMeasuredRevisionDigest == postgresRetentionMeasuredRevisionQualifiedDigest {
		t.Fatal("the two measured digests are the same value: there would be nothing to accept")
	}
}

// TestUserAuthorityRetentionComparatorAcceptsExactlyTheTwoMeasuredForms is the closure claim:
// two renderings in, everything else out.
//
// The six negatives are the measured catalog states from the same probe, each of them taken
// WITH the zero-callable overload present — so they are not "the old rejection still works",
// they are "the accepted qualification environment still rejects a replaced object".
func TestUserAuthorityRetentionComparatorAcceptsExactlyTheTwoMeasuredForms(t *testing.T) {
	t.Parallel()
	const foreignSchemaFunctionDef = "CREATE OR REPLACE FUNCTION review_wrong.olivares_retain_user_authority()\n" +
		" RETURNS trigger\n LANGUAGE plpgsql\n SET search_path TO 'pg_catalog'\n" +
		"AS $function$\nBEGIN\n  RAISE EXCEPTION 'User authority is permanent';\nEND;\n$function$\n"
	const foreignNameFunctionDef = "CREATE OR REPLACE FUNCTION public.review_wrong_handler()\n" +
		" RETURNS trigger\n LANGUAGE plpgsql\n SET search_path TO 'pg_catalog'\n" +
		"AS $function$\nBEGIN\n  RAISE EXCEPTION 'User authority is permanent';\nEND;\n$function$\n"
	const noopFunctionDef = "CREATE OR REPLACE FUNCTION public.olivares_retain_user_authority()\n" +
		" RETURNS trigger\n LANGUAGE plpgsql\n SET search_path TO 'pg_catalog'\n" +
		"AS $function$\nBEGIN\n  RETURN NULL;\nEND;\n$function$\n"

	for _, tc := range []struct {
		name       string
		triggerDef string
		function   string
		want       bool
	}{
		{
			name:       "the canonical rendering",
			triggerDef: measuredRetentionTriggerCanonical,
			function:   measuredRetentionFunctionDef,
			want:       true,
		},
		{
			name:       "the same object rendered with its schema qualifier",
			triggerDef: measuredRetentionTriggerQualified,
			function:   measuredRetentionFunctionDef,
			want:       true,
		},
		{
			name:       "a same-body handler in another schema",
			triggerDef: "CREATE TRIGGER core_user_authority_no_delete BEFORE DELETE OR TRUNCATE ON public.core_user_authority FOR EACH STATEMENT EXECUTE FUNCTION review_wrong.olivares_retain_user_authority()",
			function:   foreignSchemaFunctionDef,
		},
		{
			name:       "a same-body handler under another name",
			triggerDef: "CREATE TRIGGER core_user_authority_no_delete BEFORE DELETE OR TRUNCATE ON public.core_user_authority FOR EACH STATEMENT EXECUTE FUNCTION review_wrong_handler()",
			function:   foreignNameFunctionDef,
		},
		{
			name:       "the bound handler's body replaced with a no-op",
			triggerDef: measuredRetentionTriggerQualified,
			function:   noopFunctionDef,
		},
		{
			name:       "an added trigger argument",
			triggerDef: "CREATE TRIGGER core_user_authority_no_delete BEFORE DELETE OR TRUNCATE ON public.core_user_authority FOR EACH STATEMENT EXECUTE FUNCTION public.olivares_retain_user_authority('public.extra')",
			function:   measuredRetentionFunctionDef,
		},
		{
			name:       "AFTER timing, which cannot refuse the statement it observes",
			triggerDef: "CREATE TRIGGER core_user_authority_no_delete AFTER DELETE OR TRUNCATE ON public.core_user_authority FOR EACH STATEMENT EXECUTE FUNCTION public.olivares_retain_user_authority()",
			function:   measuredRetentionFunctionDef,
		},
		{
			name:       "WHEN (false), which never fires",
			triggerDef: "CREATE TRIGGER core_user_authority_no_delete BEFORE DELETE OR TRUNCATE ON public.core_user_authority FOR EACH STATEMENT WHEN (false) EXECUTE FUNCTION public.olivares_retain_user_authority()",
			function:   measuredRetentionFunctionDef,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			info := coreRetentionInfo(framedRetentionDefinition(tc.triggerDef, tc.function))
			if got := acceptsRetentionDefinition(t, info); got != tc.want {
				t.Fatalf("the comparator accepted=%v, want %v, for digest %s",
					got, tc.want, digestOf(info.Definition))
			}
		})
	}
}

// TestUserAuthorityRetentionRequiresTheBoundIdentityForBothAcceptedForms is the arm root asked
// for by name, and the reason the branch does not begin with a canonical-digest shortcut.
//
// The structural fields come from the SAME tgfoid join that produced the deparsed text. A
// contradictory or empty reading means the catalog is not describing the object this invariant
// declares, and that verdict must not depend on WHICH of the two renderings arrived.
func TestUserAuthorityRetentionRequiresTheBoundIdentityForBothAcceptedForms(t *testing.T) {
	t.Parallel()
	for _, form := range []struct {
		name       string
		triggerDef string
	}{
		{name: "canonical rendering", triggerDef: measuredRetentionTriggerCanonical},
		{name: "qualified rendering", triggerDef: measuredRetentionTriggerQualified},
	} {
		for _, identity := range []struct {
			name           string
			functionSchema string
			functionName   string
		}{
			{name: "no structural identity at all"},
			{name: "an empty schema", functionName: userAuthorityRetentionFunction},
			{name: "an empty name", functionSchema: dialect.EngineSchema},
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
			t.Run(form.name+", "+identity.name, func(t *testing.T) {
				t.Parallel()
				info := coreRetentionInfo(framedRetentionDefinition(form.triggerDef, measuredRetentionFunctionDef))
				info.FunctionSchema = identity.functionSchema
				info.FunctionName = identity.functionName
				if acceptsRetentionDefinition(t, info) {
					t.Fatal("a complete definition was accepted while the catalog reported a different bound handler")
				}
			})
		}
	}
}

// TestUserAuthorityRetentionAlternativeIsGatedToTheMeasuredRevision proves the alternative is
// not borrowable.
//
// Each case changes exactly ONE term of the gate and keeps the qualified rendering, which is
// the only rendering the alternative decides. The canonical rendering is checked alongside so
// the cases show the gate closing the alternative rather than breaking the ordinary rule.
//
// The last case alters a COPY of the declaration, which is a gate-term control and NOT a
// compiled-revision causal: before the correction it passed while the product still carried the
// old qualified body forward, because a real revision bump moved the gate's operand with it.
// The revision fence itself is measured in
// TestUserAuthorityRetentionMeasuredPairIsIndependentOfTheCurrentDeclaration.
func TestUserAuthorityRetentionAlternativeIsGatedToTheMeasuredRevision(t *testing.T) {
	t.Parallel()
	qualified := framedRetentionDefinition(measuredRetentionTriggerQualified, measuredRetentionFunctionDef)
	canonical := framedRetentionDefinition(measuredRetentionTriggerCanonical, measuredRetentionFunctionDef)

	for _, tc := range []struct {
		name     string
		engine   store.Engine
		key      dialect.TriggerKey
		required registeredSchemaTrigger
	}{
		{
			name:   "a module namespace declaring the same key and digest",
			engine: store.EnginePostgres,
			key:    coreRetentionKey(),
			required: registeredSchemaTrigger{
				namespace:     "module",
				SchemaTrigger: userAuthoritySchemaInvariants()[store.EnginePostgres][0],
			},
		},
		{
			name:     "another schema",
			engine:   store.EnginePostgres,
			key:      dialect.TriggerKey{Schema: "review_wrong", Table: userAuthorityRetentionTable, Name: userAuthorityRetentionTriggerName},
			required: coreRetentionInvariant(),
		},
		{
			name:     "another table",
			engine:   store.EnginePostgres,
			key:      dialect.TriggerKey{Schema: dialect.EngineSchema, Table: "review_other", Name: userAuthorityRetentionTriggerName},
			required: coreRetentionInvariant(),
		},
		{
			name:     "another trigger name",
			engine:   store.EnginePostgres,
			key:      dialect.TriggerKey{Schema: dialect.EngineSchema, Table: userAuthorityRetentionTable, Name: "core_user_authority_review"},
			required: coreRetentionInvariant(),
		},
		{
			name:     "SQLite, which renders no qualifier and declares its own digest",
			engine:   store.EngineSQLite,
			key:      dialect.TriggerKey{Schema: "main", Table: userAuthorityRetentionTable, Name: userAuthorityRetentionTriggerName},
			required: coreRetentionInvariant(),
		},
		{
			name:     "a future revision that remeasured the canonical form",
			engine:   store.EnginePostgres,
			key:      coreRetentionKey(),
			required: retentionDeclarationForRevision(digestOf("a later retention revision")),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if schemaInvariantDefinitionMatches(
				tc.engine, tc.key, tc.required, coreRetentionInfo(qualified), digestOf(qualified)) {
				t.Error("the qualified rendering was accepted outside the gate root scoped it to")
			}
			// The control: with the same altered term, the ordinary exact rule still
			// decides, so these cases are not passing because the comparator went blind.
			// The expectation is the ordinary rule itself — the declared digest against
			// the live one — not a constant that could drift with the declaration.
			wantCanonical := tc.required.DefinitionSHA256 == digestOf(canonical)
			if got := schemaInvariantDefinitionMatches(
				tc.engine, tc.key, tc.required, coreRetentionInfo(canonical), digestOf(canonical)); got != wantCanonical {
				t.Errorf("the canonical rendering accepted=%v, want %v under the ordinary exact rule", got, wantCanonical)
			}
		})
	}
}

// TestUserAuthorityRetentionQualifiedFormFailsThePreCorrectionRule is the causal, written as an
// assertion rather than as a claim in a report.
//
// The rule this change replaced was one line: hash the complete definition and compare it with
// the single declared digest. Reproduced here over the SAME measured bytes, it refuses the
// qualified rendering and accepts the canonical one — which is exactly the boot refusal the
// zero-callable overload produced, and exactly what removing the companion constant restores.
func TestUserAuthorityRetentionQualifiedFormFailsThePreCorrectionRule(t *testing.T) {
	t.Parallel()
	declared := coreRetentionInvariant().DefinitionSHA256
	canonical := digestOf(framedRetentionDefinition(measuredRetentionTriggerCanonical, measuredRetentionFunctionDef))
	qualified := digestOf(framedRetentionDefinition(measuredRetentionTriggerQualified, measuredRetentionFunctionDef))

	if canonical != declared {
		t.Fatalf("the pre-correction rule refused the CANONICAL rendering too (%s != %s): the fixture is wrong, not the rule",
			canonical, declared)
	}
	if qualified == declared {
		t.Fatal("the qualified rendering already equalled the declared digest: there was never a refusal to correct")
	}
}

// TestUserAuthorityRetentionGateMatchesTheRegisteredCoreInvariant closes the loop between the
// gate's literals and the object the store actually registers.
//
// The gate names public/core_user_authority/core_user_authority_no_delete/core as literals
// deliberately — deriving them from the registry would make it accept whatever a future
// declaration borrowed the name for. The cost of literals is that they can drift away from the
// registration, and this is the case that would fail if they did.
func TestUserAuthorityRetentionGateMatchesTheRegisteredCoreInvariant(t *testing.T) {
	t.Parallel()
	reg := newRegistry()
	if err := reg.registerCoreUserAuthorityInvariants(); err != nil {
		t.Fatalf("register core's own invariants: %v", err)
	}
	invariants := reg.schemaInvariants(store.EnginePostgres)
	if len(invariants) != 1 {
		t.Fatalf("core registered %d PostgreSQL invariants, want exactly the retention guard", len(invariants))
	}
	required := invariants[0]
	key := dialect.TriggerKey{Schema: dialect.EngineSchema, Table: required.Table, Name: required.Name}
	if !isMeasuredPostgresUserAuthorityRetentionRevision(store.EnginePostgres, key, required) {
		t.Fatalf("the registered core invariant %s.%s (namespace %q, digest %s) is not the one the comparator's gate names",
			required.Table, required.Name, required.namespace, required.DefinitionSHA256)
	}
	// And the same registration on SQLite is NOT in the pair's scope: that engine renders no
	// qualifier and declares its own digest.
	sqliteInvariants := reg.schemaInvariants(store.EngineSQLite)
	if len(sqliteInvariants) != 1 {
		t.Fatalf("core registered %d SQLite invariants, want exactly the retention guard", len(sqliteInvariants))
	}
	sqliteKey := dialect.TriggerKey{Schema: "main", Table: sqliteInvariants[0].Table, Name: sqliteInvariants[0].Name}
	if isMeasuredPostgresUserAuthorityRetentionRevision(store.EngineSQLite, sqliteKey, sqliteInvariants[0]) {
		t.Fatal("the SQLite retention invariant fell inside a PostgreSQL rendering equivalence")
	}
}

// TestUserAuthorityRetentionCompiledHandlerIdentityMatchesTheGate reads the identity the gate
// requires out of the DDL the migration executes, through the inventory's own statement reader.
//
// This is the non-circular half: the gate's constants are compared with the routine core v10
// actually creates rather than with themselves. If postgresUserAuthorityRetentionDDL ever moves
// schema, changes name, or stops being zero-input, the equivalence would be describing an
// object the boot no longer installs.
func TestUserAuthorityRetentionCompiledHandlerIdentityMatchesTheGate(t *testing.T) {
	t.Parallel()
	objects, err := managedStatementObjects(postgresUserAuthorityRetentionDDL)
	if err != nil {
		t.Fatalf("read the compiled retention DDL: %v", err)
	}
	if len(objects) != 1 || objects[0].class != managedClassRoutine {
		t.Fatalf("the compiled retention statement names %d object(s), want exactly one routine", len(objects))
	}
	signature, err := normalizeRoutineArgTypes(objects[0].args)
	if err != nil {
		t.Fatalf("normalize the compiled retention signature: %v", err)
	}
	schema, err := compiledRoutineSchema(postgresUserAuthorityRetentionDDL)
	if err != nil {
		t.Fatalf("read the compiled retention schema: %v", err)
	}
	if schema != dialect.EngineSchema {
		t.Errorf("the compiled retention handler is declared in schema %q; the gate requires %q",
			schema, dialect.EngineSchema)
	}
	if objects[0].name != userAuthorityRetentionFunction {
		t.Errorf("the compiled retention handler is named %q; the gate requires %q",
			objects[0].name, userAuthorityRetentionFunction)
	}
	if signature != "" {
		t.Errorf("the compiled retention handler takes %q; the equivalence is about a ZERO-INPUT handler "+
			"whose name stopped being unambiguous", signature)
	}
}
