// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// THE H LOCK'S SINGLE-NAME AUTHORITY CONTRACT, JUDGED BEFORE THIS BOOT CAN COMMIT ANYTHING.
//
// Core v10 creates `public.olivares_lock_core_user_authority(text)` and then attests it. Every
// step of that attestation, and every runtime call, resolves the routine BY PUBLIC NAME and not
// by the OID the migration just created:
//
//   - verifyPostgresUserAuthorityLock -> verifyPostgresAuthorityFunctionDefinition ->
//     projectGuardFunction(ctx, q, dialect.EngineSchema, "olivares_lock_core_user_authority"),
//     which selects on schema and name alone and REFUSES more than one row, because one
//     identity carries one footprint (guardcatalog.go, decodeGuardFunctionProjection);
//   - the owner/EXECUTE-ACL queries in directoryinventory_postgres.go, likewise by name;
//   - the read and writer paths, whose SQL text is
//     `SELECT public.olivares_lock_core_user_authority($1)` with no explicit input cast
//     (userauthority.go, userauthority_writer.go).
//
// So a SECOND routine under that public name — whatever its signature — makes this build's own
// final authority verifier unusable the moment the migration creates its own. That is a
// postcondition this boot cannot satisfy, and it is knowable from the catalog before the boot
// changes anything.
//
// WHAT WENT WRONG WITHOUT THIS CHECK, and it is the whole reason the check exists: the managed
// object set resolves a routine by name AND exact input type signature, which is correct — a
// foreign `olivares_lock_core_user_authority(boolean)` is a different function that this build
// never creates and `CREATE OR REPLACE FUNCTION` never replaces, so the max0 census does not
// match it and must not. The admission therefore returned `fresh-empty`, classifyRolloutControls
// committed the three rollout relations, the core migrations committed through v9, and only then
// did v10's verifier refuse on `more than one overload`. The v10 transaction rolled back; the
// transactions before it could not. A known impossible postcondition was admitted too early.
//
// WHAT THIS IS NOT, and the distinction is the contract root ratified:
//
//   - It is NOT managed-object membership. A foreign routine under this name is not an object
//     this build administers, is not adopted, is not compared for adoption, is not dropped, and
//     has neither its owner nor its ACL touched. It is a named operational INCOMPATIBILITY, and
//     the answer to it is to refuse while the estate is still exactly as it was found.
//   - It is NOT an authority witness. A sole existing routine of the exact compiled signature is
//     PERMITTED HERE and adopted NOWHERE: at max0 the managed census still refuses it as an
//     exact twin, and on every other source the complete definition, signature, owner, effective
//     ACL, trigger and runtime checks still run afterwards and still decide. This check asks one
//     question — is the NAME free for the routine this build is going to create and then resolve
//     by name — and answers nothing else.
//   - It is NOT a name ban. A different name, or this name in a different schema, is untouched:
//     the queries and calls above say `public` explicitly, so `review.olivares_lock_core_user_
//     authority(text)` is another owner's routine and no concern of this boot's. No prefix rule
//     follows from any of this.
//   - It is NOT a shared-projector change. projectGuardFunction, the directory writer guard's
//     wider attachment census, the retention trigger's tgfoid binding, the optional inventory
//     routine, the event fence and the logical restore ceremony all keep their own contracts. In
//     particular the measured `(text)` and `VARIADIC text[]` retention overloads stay legal on
//     ordinary Open. A zero-callable DEFAULT overload changes trigger-definition rendering and
//     is a separate unresolved compatibility case, despite retaining the same handler OID.
//
// IF THE COMPILED IDENTITY EVER MOVES, BOTH ENDS MOVE TOGETHER. The identity below is read from
// postgresUserAuthorityLockDDL — the same constant core v10 executes — through the same finite
// statement reader the managed inventory uses. Nothing here restates a name, a schema or a
// signature, so a change to that DDL cannot leave this check measuring the previous object. What
// a change to that DDL DOES require is a deliberate look at the by-name consumers listed at the
// top of this comment: they are why the restriction exists, and a signature change that made them
// resolve by OID would make it unnecessary.

// ErrUserAuthorityLockNameIncompatible is the pre-effect refusal for a routine that occupies the
// User authority lock's public name with an identity this build does not declare.
//
// It is its own sentinel rather than one of the admission's: the admission answers "is this a
// fresh managed namespace", which this is not about. A caller distinguishing the two is
// distinguishing "an object of mine already exists" from "the name my object needs is occupied
// by somebody else's".
var ErrUserAuthorityLockNameIncompatible = errors.New(
	"sqlstore: the User authority lock's public name already carries a routine this build does not declare")

// postgresRoutineIdentity is a routine as PostgreSQL itself identifies one: schema, name and the
// exact input type signature, in the spelling proargtypes projects.
type postgresRoutineIdentity struct {
	schema    string
	name      string
	signature string
}

func (i postgresRoutineIdentity) String() string {
	return fmt.Sprintf("%s.%s(%s)", i.schema, i.name, i.signature)
}

// compiledUserAuthorityLockIdentity derives the H lock's identity from the DDL constant the
// migration executes.
//
// It goes through managedStatementObjects and normalizeRoutineArgTypes — the inventory's own
// deny-closed reader — so a statement form neither can name is an ERROR here rather than a
// guess. A wrong expected signature would be worse than no check at all: it would refuse the
// routine this build creates and admit the one it does not.
func compiledUserAuthorityLockIdentity() (postgresRoutineIdentity, error) {
	objects, err := managedStatementObjects(postgresUserAuthorityLockDDL)
	if err != nil {
		return postgresRoutineIdentity{}, err
	}
	if len(objects) != 1 || objects[0].class != managedClassRoutine {
		return postgresRoutineIdentity{}, fmt.Errorf(
			"%w: the compiled User authority lock statement names %d object(s) and not one routine",
			errManagedStatementUnreadable, len(objects))
	}
	signature, err := normalizeRoutineArgTypes(objects[0].args)
	if err != nil {
		return postgresRoutineIdentity{}, err
	}
	schema, err := compiledRoutineSchema(postgresUserAuthorityLockDDL)
	if err != nil {
		return postgresRoutineIdentity{}, err
	}
	// The schema is READ and then CHECKED against the one the engine resolves, rather than
	// assumed to be it. The downstream projection passes dialect.EngineSchema and the runtime
	// SQL says `public.` in its text: a compiled DDL that ever named another schema would make
	// this check measure a different object from the one the boot goes on to create and call.
	if schema != dialect.EngineSchema {
		return postgresRoutineIdentity{}, fmt.Errorf(
			"%w: the compiled User authority lock is declared in schema %q, and its verifier and its callers resolve %q",
			errManagedStatementUnreadable, schema, dialect.EngineSchema)
	}
	return postgresRoutineIdentity{schema: schema, name: objects[0].name, signature: signature}, nil
}

// compiledRoutineSchema reads the schema qualifier of a rendered CREATE FUNCTION statement.
//
// managedIdentifier deliberately DISCARDS the qualifier once it has validated it, because the
// catalogs the inventory censuses are already bound to one schema. This check needs the value
// itself, so it is read here and validated by its caller against the schema the engine resolves.
func compiledRoutineSchema(stmt string) (string, error) {
	text := stripSQLComments(stmt)
	open := strings.IndexByte(text, '(')
	if open < 0 {
		return "", fmt.Errorf("%w: routine declaration without an argument list", errManagedStatementUnreadable)
	}
	fields := strings.Fields(text[:open])
	if len(fields) == 0 {
		return "", fmt.Errorf("%w: routine declaration without a name", errManagedStatementUnreadable)
	}
	reference := fields[len(fields)-1]
	dot := strings.LastIndexByte(reference, '.')
	if dot < 0 {
		return "", fmt.Errorf("%w: routine declaration %q is not schema-qualified", errManagedStatementUnreadable, reference)
	}
	return strings.Trim(reference[:dot], `"`), nil
}

// userAuthorityLockNameCensusSQL lists EVERY pg_proc row under one schema and name, with the
// input type signature projected from proargtypes and the kind the catalog records.
//
// It is deliberately unfiltered on prokind. A foreign PROCEDURE or AGGREGATE under this name is
// seen by the by-name projection this check protects, so filtering it away here would report a
// friendlier database than the one the verifier is about to read.
//
// The signature comes from proargtypes and NOT from pg_get_function_identity_arguments, for the
// reason the managed census records: measured on PostgreSQL 16 the latter renders `review text`,
// the argument's NAME and its type, and an argument name is not part of a function's identity.
// Defaulted and variadic parameters appear in proargtypes as ordinary input types, so an
// `(text, extra text DEFAULT …)` or a variadic form is a different signature here and is refused
// as such — no call-resolution experiment is needed to reach that verdict, and none is performed.
const userAuthorityLockNameCensusSQL = `SELECT COALESCE(pg_catalog.array_to_string(ARRAY(
         SELECT pg_catalog.format_type(t, NULL)
         FROM pg_catalog.unnest(p.proargtypes) AS t), ', '), ''),
       p.prokind::text
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1 AND p.proname = $2`

// postgresRoutineKindFunction is the prokind of an ordinary function, which is what
// postgresUserAuthorityLockDDL renders and what the final verifier's canonical form requires.
const postgresRoutineKindFunction = "f"

// preflightPostgresUserAuthorityLockCompatibility refuses, before this boot commits anything, a
// database whose H lock name this build cannot end up owning alone.
//
// WHERE IT RUNS AND WHY THERE. Open calls it inside the migration lock, AFTER
// preflightCoreMigrationVersion — an older binary must not be told about a name conflict when
// the real answer is that the database's core history is ahead of it — and BEFORE
// classifyRolloutControls, which is the first step of that callback that can commit. Everything
// between those two lines is a catalog read, so a refusal produced here leaves the schema
// trackers, every relation, every receipt and the foreign routine exactly as this boot found
// them.
//
// It covers all three sources on purpose, because the incompatibility does not depend on which:
// a max0 database, an existing pre-v10 one whose v10 migration is still ahead of it, and an
// already-v10 estate whose single H entry is this build's own and therefore passes.
//
// A FAILED READ IS A REFUSAL. "I could not look" is not "there is nothing there", and this is
// the one boundary where the difference costs the estate.
//
// IT EXECUTES NO FOREIGN BODY. The census reads catalog columns; no candidate is called, and no
// probe of PostgreSQL's own call resolution is performed against somebody else's function.
func preflightPostgresUserAuthorityLockCompatibility(
	ctx context.Context,
	q dialect.Querier,
	dia dialect.Dialect,
) error {
	if dia.Name() != store.EnginePostgres {
		return nil
	}
	want, err := compiledUserAuthorityLockIdentity()
	if err != nil {
		return fmt.Errorf("sqlstore: User authority lock compatibility preflight: %w", err)
	}
	rows, err := q.QueryContext(ctx, userAuthorityLockNameCensusSQL, want.schema, want.name)
	if err != nil {
		return fmt.Errorf("%w: reading %s.%s from pg_proc failed, and a read this boot could not perform is not an empty result: %w",
			ErrUserAuthorityLockNameIncompatible, want.schema, want.name, err)
	}
	defer rows.Close() //nolint:errcheck // joined through rows.Err below
	var incompatible []string
	expected := 0
	for rows.Next() {
		var signature, kind string
		if err := rows.Scan(&signature, &kind); err != nil {
			return fmt.Errorf("%w: reading %s.%s from pg_proc failed, and a read this boot could not perform is not an empty result: %w",
				ErrUserAuthorityLockNameIncompatible, want.schema, want.name, err)
		}
		// The compiled statement is a CREATE FUNCTION, so the kind is part of the identity it
		// declares: an aggregate or a procedure wearing the exact input signature is not the
		// routine this build creates, and the final verifier's canonical form says the same
		// thing with `Kind: "f"`.
		if signature == want.signature && kind == postgresRoutineKindFunction {
			expected++
			continue
		}
		incompatible = append(incompatible, fmt.Sprintf("%s.%s(%s) [prokind %s]", want.schema, want.name, signature, kind))
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: reading %s.%s from pg_proc failed, and a read this boot could not perform is not an empty result: %w",
			ErrUserAuthorityLockNameIncompatible, want.schema, want.name, err)
	}
	// More than one row of the exact compiled identity is not something PostgreSQL can hold —
	// name and input types are unique within a schema — so this arm exists to be deny-closed
	// about a catalog that surprises us rather than to describe a reachable state.
	if expected > 1 {
		return fmt.Errorf(
			"%w: %s exists %d times, which this build cannot resolve. Nothing has been created, altered or dropped",
			ErrUserAuthorityLockNameIncompatible, want, expected)
	}
	if len(incompatible) == 0 {
		return nil
	}
	sort.Strings(incompatible)
	return fmt.Errorf(
		"%w: %d routine(s) already carry that name: %s. This build creates %s and then verifies it, grants EXECUTE on it and calls it BY NAME — `SELECT %s.%s($1)` with no explicit input cast — so its public name has to resolve to exactly one routine, and it would not. The foreign routine is NOT an object this build administers: it has not been read for adoption, compared, dropped, re-owned or re-granted, and its body has not been executed. Nothing has been created, altered or dropped, and this database's migration history is untouched: point this deployment at a destination this build does not administer, or resolve that routine deliberately",
		ErrUserAuthorityLockNameIncompatible, len(incompatible), strings.Join(incompatible, ", "),
		want, want.schema, want.name)
}
