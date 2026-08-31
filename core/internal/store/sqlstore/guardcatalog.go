// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// guardcatalog.go is the ONE projection of a guard from pg_catalog, and the ONE comparator
// that decides whether what it read is the object the manifest declares.
//
// "One" is the whole design. The alternative — a boolean computed in SQL for the per-unit
// path and a Go comparison for the bulk path — means the same question has two answers
// that must be kept in step by hand, and adding a field to the trigger profile means
// changing a SQL predicate and a Go predicate and hoping. Here the SQL returns DATA and
// never a verdict; every verdict is Go's, over a struct both paths decode identically.
//
// The projection is CATALOG-ONLY. It never names the target relation in a FROM clause, so
// running it takes no lock on the object it describes — which is what lets the runner
// refuse an unauthorized unit before acquiring anything, and what lets the same query be
// re-issued under the lock as the authoritative reading.

// guardCatalogRow is one target's whole reading: whether the relation and the guard exist,
// their structural projections, the raw enable state, and a few strictly diagnostic fields.
//
// WHAT IS DELIBERATELY ABSENT, because omitting it is safer than reading it here: the five
// has_table_privilege probes the design lists as diagnostics. They would have to name a
// role, and has_table_privilege raises 42704 for a role that does not exist — so a
// projection carrying them could fail the whole reading over a diagnostic nobody decides
// from. The effective privileges are read where the answer is meaningful anyway: from the
// APPLICATION pool, by verifyAppendOnlyACL, which is the authority on what that role may
// do. Duplicating them in the DDL session would add a failure mode and no decision.
type guardCatalogRow struct {
	Key guardKey

	RelationExists bool
	RelationOID    string
	Relation       guardRelationForm

	GuardExists bool
	TriggerOID  string
	Trigger     guardTriggerForm
	// EnableState is the RAW tgenabled character, never normalised. "O or A" left
	// unclassified was the defect in the verifier that predates this contract, and a
	// COALESCE to 'O' would invent a state for a trigger that is not there.
	EnableState string

	FunctionExists bool
	Function       guardFunctionForm

	// Diagnostics. Never part of any canonical comparison: a legitimate deployment may own
	// its schema with any role, so folding ownership into the golden would report drift on
	// every such deployment.
	FunctionOwner optText
	FunctionACL   optText
}

// guardCatalogColumns is the projection's column list, in the order scanGuardCatalogRow
// reads it.
//
// Spelled once and shared by the per-unit CTE and the bulk query, because a column list
// that exists twice is a column list that will disagree with itself. The relation columns
// come from an alias the caller provides (`r` in both shapes), so the same text serves a
// single-target LEFT JOIN and a many-target join.
const guardCatalogColumns = `r.relation_oid::text AS relation_oid,
       r.relation_schema,
       r.relation_name,
       r.trigger_name,
       r.relkind,
       r.relpersistence,
       r.relispartition,
       r.has_parent,
       r.has_child,

       t.oid::text AS trigger_oid,
       t.tgparentid::text AS tgparentid,
       t.tgtype,
       t.tgenabled::text AS tgenabled,
       t.tgisinternal,
       t.tgconstrrelid::text AS tgconstrrelid,
       t.tgconstrindid::text AS tgconstrindid,
       t.tgconstraint::text AS tgconstraint,
       t.tgdeferrable,
       t.tginitdeferred,
       t.tgnargs,
       COALESCE(pg_catalog.array_length(t.tgattr, 1), 0) AS tgattr_count,
       COALESCE(pg_catalog.octet_length(t.tgargs), 0) AS tgargs_bytes,
       t.tgqual::text AS tgqual,
       t.tgoldtable::text AS tgoldtable,
       t.tgnewtable::text AS tgnewtable,

       fn.nspname AS function_schema,
       p.proname,
       p.prokind::text AS prokind,
       rtn.nspname AS return_type_schema,
       rt.typname AS return_type_name,
       p.proretset,
       lang.lanname,
       p.pronargs,
       p.pronargdefaults,
       p.provariadic::text AS provariadic,
       COALESCE(pg_catalog.array_length(p.proargtypes, 1), 0) AS proargtypes_count,
       p.proallargtypes IS NULL AS proallargtypes_is_null,
       p.proargmodes IS NULL AS proargmodes_is_null,
       p.proargnames IS NULL AS proargnames_is_null,
       p.proargdefaults IS NULL AS proargdefaults_is_null,
       p.prosrc,
       p.probin,
       p.prosqlbody::text AS prosqlbody,
       p.prosecdef,
       p.proleakproof,
       p.proisstrict,
       p.provolatile::text AS provolatile,
       p.proparallel::text AS proparallel,
       p.procost,
       p.prorows,
       -- prosupport is declared regproc, and regproc's TEXT form for OID 0 is "-", not "0".
       -- Casting to oid first makes the projected value a number whose rendering does not
       -- depend on how a type chooses to print an invalid reference. Measured: the text form
       -- came back as "-" against 15.x and made every canonical comparison fail on this one
       -- field.
       p.prosupport::oid::text AS prosupport,
       p.protrftypes IS NULL AS protrftypes_is_null,
       p.proconfig IS NULL AS proconfig_is_null,

       CASE WHEN p.oid IS NULL THEN NULL
            ELSE pg_catalog.pg_get_userbyid(p.proowner) END AS function_owner,
       p.proacl::text AS function_acl`

// guardCatalogJoins is the join chain from a relation source aliased `r` to the trigger,
// its function, and the four catalogs that name the function's types and language.
//
// Every catalog is schema-qualified. This runs on a connection whose search_path a role
// could otherwise influence, and shadowing pg_trigger would let somebody choose the answer.
const guardCatalogJoins = `  LEFT JOIN pg_catalog.pg_trigger t
    ON t.tgrelid = r.relation_oid
   AND t.tgname = r.trigger_name
  LEFT JOIN pg_catalog.pg_proc p ON p.oid = t.tgfoid
  LEFT JOIN pg_catalog.pg_namespace fn ON fn.oid = p.pronamespace
  LEFT JOIN pg_catalog.pg_type rt ON rt.oid = p.prorettype
  LEFT JOIN pg_catalog.pg_namespace rtn ON rtn.oid = rt.typnamespace
  LEFT JOIN pg_catalog.pg_language lang ON lang.oid = p.prolang`

// guardCatalogSingleSQL projects ONE target: $1 schema, $2 relation, $3 trigger.
//
// The relation source starts from a singleton and LEFT JOINs the catalog, so a missing
// namespace or relation still produces exactly one row. That property is not cosmetic: a
// query that returned zero rows for an absent relation would make "the relation is not
// there" and "the query matched nothing for some other reason" the same observation, and
// the matrix needs those apart.
const guardCatalogSingleSQL = `WITH relations AS (
  SELECT c.oid AS relation_oid,
         $1::text AS relation_schema,
         $2::text AS relation_name,
         $3::text AS trigger_name,
         c.relkind::text AS relkind,
         c.relpersistence::text AS relpersistence,
         c.relispartition,
         EXISTS (SELECT 1 FROM pg_catalog.pg_inherits i WHERE i.inhrelid = c.oid) AS has_parent,
         EXISTS (SELECT 1 FROM pg_catalog.pg_inherits i WHERE i.inhparent = c.oid) AS has_child
  FROM (VALUES (1)) AS singleton(dummy)
  LEFT JOIN pg_catalog.pg_namespace n ON n.nspname = $1
  LEFT JOIN pg_catalog.pg_class c ON c.relnamespace = n.oid AND c.relname = $2
)
SELECT ` + guardCatalogColumns + `
FROM relations r
` + guardCatalogJoins

// scanGuardCatalogRow decodes guardCatalogColumns.
//
// Every nullable column is scanned into a nullable Go type and converted EXPLICITLY. There
// is no COALESCE anywhere in the projection for a semantic field, and that is the rule
// rather than an accident: a default invented in SQL is a reading the catalog never made,
// and prestate.validate() exists precisely to refuse such a reading. The one place a
// COALESCE appears is over array_length and octet_length, where the meaning of NULL — "no
// array at all" — really is zero elements.
func scanGuardCatalogRow(sc func(...any) error) (guardCatalogRow, error) {
	var (
		row guardCatalogRow

		relationOID                                sql.NullString
		relkind, relpersistence                    sql.NullString
		relispartition, hasParent, hasChild        sql.NullBool
		triggerOID, tgparentid                     sql.NullString
		tgtype                                     sql.NullInt64
		tgenabled                                  sql.NullString
		tgisinternal                               sql.NullBool
		tgconstrrelid, tgconstrindid, tgconstraint sql.NullString
		tgdeferrable, tginitdeferred               sql.NullBool
		tgnargs, tgattrCount, tgargsBytes          sql.NullInt64
		tgqual, tgoldtable, tgnewtable             sql.NullString

		fnSchema, proname, prokind           sql.NullString
		rtnSchema, rtName                    sql.NullString
		proretset                            sql.NullBool
		lanname                              sql.NullString
		pronargs, pronargdefaults            sql.NullInt64
		provariadic                          sql.NullString
		proargtypesCount                     sql.NullInt64
		allArgTypesNull, argModesNull        sql.NullBool
		argNamesNull, argDefaultsNull        sql.NullBool
		prosrc, probin, prosqlbody           sql.NullString
		prosecdef, proleakproof, proisstrict sql.NullBool
		provolatile, proparallel             sql.NullString
		procost, prorows                     sql.NullFloat64
		prosupport                           sql.NullString
		trfTypesNull, configNull             sql.NullBool
		functionOwner, functionACL           sql.NullString
	)
	if err := sc(&relationOID, &row.Key.Schema, &row.Key.Relation, &row.Key.Trigger,
		&relkind, &relpersistence, &relispartition, &hasParent, &hasChild,
		&triggerOID, &tgparentid, &tgtype, &tgenabled, &tgisinternal,
		&tgconstrrelid, &tgconstrindid, &tgconstraint,
		&tgdeferrable, &tginitdeferred, &tgnargs, &tgattrCount, &tgargsBytes,
		&tgqual, &tgoldtable, &tgnewtable,
		&fnSchema, &proname, &prokind, &rtnSchema, &rtName, &proretset, &lanname,
		&pronargs, &pronargdefaults, &provariadic, &proargtypesCount,
		&allArgTypesNull, &argModesNull, &argNamesNull, &argDefaultsNull,
		&prosrc, &probin, &prosqlbody,
		&prosecdef, &proleakproof, &proisstrict, &provolatile, &proparallel,
		&procost, &prorows, &prosupport, &trfTypesNull, &configNull,
		&functionOwner, &functionACL); err != nil {
		return guardCatalogRow{}, err
	}

	row.RelationExists = relationOID.Valid
	row.RelationOID = relationOID.String
	if row.RelationExists {
		row.Relation = guardRelationForm{
			Kind:        relkind.String,
			Persistence: relpersistence.String,
			IsPartition: relispartition.Bool,
			HasParent:   hasParent.Bool,
			HasChild:    hasChild.Bool,
		}
	}

	row.GuardExists = triggerOID.Valid
	row.TriggerOID = triggerOID.String
	if row.GuardExists {
		// The state is taken RAW. A fifth character — anything PostgreSQL might add, or
		// anything a corrupted catalog might hold — arrives untranslated and is refused
		// downstream by prestate.validate, rather than being mapped onto a state this
		// engine understands.
		row.EnableState = tgenabled.String
		row.Trigger = guardTriggerForm{
			ParentID:     tgparentid.String,
			Type:         tgtype.Int64,
			IsInternal:   tgisinternal.Bool,
			ConstrRelID:  tgconstrrelid.String,
			ConstrIndID:  tgconstrindid.String,
			Constraint:   tgconstraint.String,
			Deferrable:   tgdeferrable.Bool,
			InitDeferred: tginitdeferred.Bool,
			NArgs:        tgnargs.Int64,
			AttrCount:    tgattrCount.Int64,
			ArgsBytes:    tgargsBytes.Int64,
			Qual:         nullToOpt(tgqual),
			OldTable:     nullToOpt(tgoldtable),
			NewTable:     nullToOpt(tgnewtable),
		}
	}

	row.FunctionExists = proname.Valid
	if row.FunctionExists {
		row.Function = guardFunctionForm{
			Schema:           fnSchema.String,
			Name:             proname.String,
			Kind:             prokind.String,
			ReturnTypeSchema: rtnSchema.String,
			ReturnTypeName:   rtName.String,
			ReturnsSet:       proretset.Bool,
			Language:         lanname.String,
			NArgs:            pronargs.Int64,
			NArgDefaults:     pronargdefaults.Int64,
			Variadic:         provariadic.String,
			ArgTypesCount:    proargtypesCount.Int64,
			AllArgTypesNull:  allArgTypesNull.Bool,
			ArgModesNull:     argModesNull.Bool,
			ArgNamesNull:     argNamesNull.Bool,
			ArgDefaultsNull:  argDefaultsNull.Bool,
			Src:              prosrc.String,
			Bin:              nullToOpt(probin),
			SQLBody:          nullToOpt(prosqlbody),
			SecurityDefiner:  prosecdef.Bool,
			LeakProof:        proleakproof.Bool,
			Strict:           proisstrict.Bool,
			Volatile:         provolatile.String,
			Parallel:         proparallel.String,
			Cost:             procost.Float64,
			Rows:             prorows.Float64,
			Support:          prosupport.String,
			TransformsNull:   trfTypesNull.Bool,
			ConfigNull:       configNull.Bool,
		}
	}
	row.FunctionOwner = nullToOpt(functionOwner)
	row.FunctionACL = nullToOpt(functionACL)
	return row, nil
}

func nullToOpt(n sql.NullString) optText {
	if !n.Valid {
		return optText{}
	}
	return someText(n.String)
}

// definition is the row's structural projection, in the same type the manifest declares.
//
// Same type on both sides is what makes the comparison a comparison rather than a
// translation, and a translation is where a field quietly stops being compared.
func (r guardCatalogRow) definition() guardDefinition {
	return guardDefinition{Relation: r.Relation, Trigger: r.Trigger, Function: r.Function}
}

// eligible reports whether the relation may carry a managed guard at all, and names the
// reason when it may not.
//
// Two of the five checks exist because a shorter predicate is measurably wrong:
// relkind='r' does not exclude a LEAF partition, and inheritance in either direction means
// a lock on one relation is not a lock on the other.
func (r guardCatalogRow) eligible() (bool, string) {
	switch {
	case !r.RelationExists:
		return false, "the relation does not exist"
	case r.Relation.Kind != "r":
		return false, fmt.Sprintf("relkind is %q, not an ordinary table", r.Relation.Kind)
	case r.Relation.Persistence != "p":
		return false, fmt.Sprintf("relpersistence is %q, not permanent", r.Relation.Persistence)
	case r.Relation.IsPartition:
		return false, "the relation is a partition, and a lock on its parent is not a lock on it"
	case r.Relation.HasParent:
		return false, "the relation inherits from another"
	case r.Relation.HasChild:
		return false, "the relation has children, so a lock on it is not a lock on them"
	default:
		return true, ""
	}
}

// guardDefinitionDiff names every field in which two definitions disagree.
//
// It returns a LIST rather than a boolean because the boolean is the useless half of the
// answer: an operator told "not canonical" about a 40-field projection has to bisect it by
// hand, and the mutation regression that proves each field is compared needs to know which
// field it broke.
func guardDefinitionDiff(want, got guardDefinition) []string {
	var out []string
	add := func(what, w, g string) {
		if w != g {
			out = append(out, fmt.Sprintf("%s: want %s, got %s", what, w, g))
		}
	}
	addBool := func(what string, w, g bool) { add(what, fmt.Sprint(w), fmt.Sprint(g)) }
	addInt := func(what string, w, g int64) { add(what, fmt.Sprint(w), fmt.Sprint(g)) }

	add("relkind", want.Relation.Kind, got.Relation.Kind)
	add("relpersistence", want.Relation.Persistence, got.Relation.Persistence)
	addBool("relispartition", want.Relation.IsPartition, got.Relation.IsPartition)
	addBool("has_parent", want.Relation.HasParent, got.Relation.HasParent)
	addBool("has_child", want.Relation.HasChild, got.Relation.HasChild)

	add("tgparentid", want.Trigger.ParentID, got.Trigger.ParentID)
	addInt("tgtype", want.Trigger.Type, got.Trigger.Type)
	addBool("tgisinternal", want.Trigger.IsInternal, got.Trigger.IsInternal)
	add("tgconstrrelid", want.Trigger.ConstrRelID, got.Trigger.ConstrRelID)
	add("tgconstrindid", want.Trigger.ConstrIndID, got.Trigger.ConstrIndID)
	add("tgconstraint", want.Trigger.Constraint, got.Trigger.Constraint)
	addBool("tgdeferrable", want.Trigger.Deferrable, got.Trigger.Deferrable)
	addBool("tginitdeferred", want.Trigger.InitDeferred, got.Trigger.InitDeferred)
	addInt("tgnargs", want.Trigger.NArgs, got.Trigger.NArgs)
	// tgattr is the field that tells `BEFORE UPDATE OF column` from `BEFORE UPDATE`, and
	// tgtype does not: measured on 15.18, both are tgtype=27 and only tgattr differs
	// ({2} against empty). Without this comparison a lookalike watching ONE column would be
	// adopted as a legitimate guard while every other column stayed mutable.
	addInt("tgattr_count", want.Trigger.AttrCount, got.Trigger.AttrCount)
	addInt("tgargs_bytes", want.Trigger.ArgsBytes, got.Trigger.ArgsBytes)
	add("tgqual", want.Trigger.Qual.String(), got.Trigger.Qual.String())
	add("tgoldtable", want.Trigger.OldTable.String(), got.Trigger.OldTable.String())
	add("tgnewtable", want.Trigger.NewTable.String(), got.Trigger.NewTable.String())

	add("function schema", want.Function.Schema, got.Function.Schema)
	add("function name", want.Function.Name, got.Function.Name)
	add("prokind", want.Function.Kind, got.Function.Kind)
	add("return type schema", want.Function.ReturnTypeSchema, got.Function.ReturnTypeSchema)
	add("return type", want.Function.ReturnTypeName, got.Function.ReturnTypeName)
	addBool("proretset", want.Function.ReturnsSet, got.Function.ReturnsSet)
	add("language", want.Function.Language, got.Function.Language)
	addInt("pronargs", want.Function.NArgs, got.Function.NArgs)
	addInt("pronargdefaults", want.Function.NArgDefaults, got.Function.NArgDefaults)
	add("provariadic", want.Function.Variadic, got.Function.Variadic)
	addInt("proargtypes_count", want.Function.ArgTypesCount, got.Function.ArgTypesCount)
	addBool("proallargtypes is null", want.Function.AllArgTypesNull, got.Function.AllArgTypesNull)
	addBool("proargmodes is null", want.Function.ArgModesNull, got.Function.ArgModesNull)
	addBool("proargnames is null", want.Function.ArgNamesNull, got.Function.ArgNamesNull)
	addBool("proargdefaults is null", want.Function.ArgDefaultsNull, got.Function.ArgDefaultsNull)
	if want.Function.Src != got.Function.Src {
		// Quoted with %q so a difference in whitespace is visible. A body that differs only
		// by a reflowed newline IS a different body, and a diff that printed it raw would
		// look like two identical lines.
		out = append(out, fmt.Sprintf("prosrc: want %q, got %q", want.Function.Src, got.Function.Src))
	}
	add("probin", want.Function.Bin.String(), got.Function.Bin.String())
	add("prosqlbody", want.Function.SQLBody.String(), got.Function.SQLBody.String())
	addBool("prosecdef", want.Function.SecurityDefiner, got.Function.SecurityDefiner)
	addBool("proleakproof", want.Function.LeakProof, got.Function.LeakProof)
	addBool("proisstrict", want.Function.Strict, got.Function.Strict)
	add("provolatile", want.Function.Volatile, got.Function.Volatile)
	add("proparallel", want.Function.Parallel, got.Function.Parallel)
	add("procost", fmt.Sprint(want.Function.Cost), fmt.Sprint(got.Function.Cost))
	add("prorows", fmt.Sprint(want.Function.Rows), fmt.Sprint(got.Function.Rows))
	add("prosupport", want.Function.Support, got.Function.Support)
	addBool("protrftypes is null", want.Function.TransformsNull, got.Function.TransformsNull)
	addBool("proconfig is null", want.Function.ConfigNull, got.Function.ConfigNull)
	return out
}

// matchesCanonical reports whether the row is byte-for-byte the object spec declares.
//
// It returns the diff too, so the caller can put the reason in a durable diagnostic rather
// than a bare false. A guard that is present but not canonical is the case an operator
// most needs the detail for: it is either an object somebody edited or a lookalike
// somebody installed, and the field that differs is what tells those apart.
func (r guardCatalogRow) matchesCanonical(spec guardSpec) (bool, []string) {
	if !r.RelationExists || !r.GuardExists || !r.FunctionExists {
		return false, []string{"the relation, the guard or its function is absent"}
	}
	if ok, why := r.eligible(); !ok {
		return false, []string{why}
	}
	diff := guardDefinitionDiff(spec.Definition, r.definition())
	return len(diff) == 0, diff
}

// projectGuardCatalogRow reads ONE target through the shared projection.
func projectGuardCatalogRow(ctx context.Context, q rowQuerier, key guardKey) (guardCatalogRow, error) {
	rows, err := q.QueryContext(ctx, guardCatalogSingleSQL, key.Schema, key.Relation, key.Trigger)
	if err != nil {
		return guardCatalogRow{}, fmt.Errorf("sqlstore: project the guard catalog for %s: %w", key, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return guardCatalogRow{}, fmt.Errorf("sqlstore: project the guard catalog for %s: %w", key, err)
		}
		// The singleton LEFT JOIN guarantees a row. Reaching here means the query this
		// package authored no longer has the shape this package relies on, which is a
		// refusal rather than an absence: an absent relation already comes back as a row
		// with a NULL oid.
		return guardCatalogRow{}, fmt.Errorf("sqlstore: the guard catalog projection for %s returned no row, which its singleton join makes impossible", key)
	}
	row, err := scanGuardCatalogRow(rows.Scan)
	if err != nil {
		return guardCatalogRow{}, fmt.Errorf("sqlstore: decode the guard catalog for %s: %w", key, err)
	}
	if err := rows.Err(); err != nil {
		return guardCatalogRow{}, fmt.Errorf("sqlstore: project the guard catalog for %s: %w", key, err)
	}
	return row, nil
}

// guardFunctionProjectionSQL reads the shared trigger function ALONE, with no relation and
// no trigger.
//
// The bootstrap needs it before any guard of this edition exists: on a legacy database
// olivares_block_mutation is already there (the v1 migration created it), and the
// bootstrap must decide whether to reuse it or refuse. Asking through the guard projection
// would be asking about a trigger that may not exist yet.
const guardFunctionProjectionSQL = `SELECT
       fn.nspname AS function_schema,
       p.proname,
       p.prokind::text AS prokind,
       rtn.nspname AS return_type_schema,
       rt.typname AS return_type_name,
       p.proretset,
       lang.lanname,
       p.pronargs,
       p.pronargdefaults,
       p.provariadic::text AS provariadic,
       COALESCE(pg_catalog.array_length(p.proargtypes, 1), 0) AS proargtypes_count,
       p.proallargtypes IS NULL AS proallargtypes_is_null,
       p.proargmodes IS NULL AS proargmodes_is_null,
       p.proargnames IS NULL AS proargnames_is_null,
       p.proargdefaults IS NULL AS proargdefaults_is_null,
       p.prosrc,
       p.probin,
       p.prosqlbody::text AS prosqlbody,
       p.prosecdef,
       p.proleakproof,
       p.proisstrict,
       p.provolatile::text AS provolatile,
       p.proparallel::text AS proparallel,
       p.procost,
       p.prorows,
       -- prosupport is declared regproc, and regproc's TEXT form for OID 0 is "-", not "0".
       -- Casting to oid first makes the projected value a number whose rendering does not
       -- depend on how a type chooses to print an invalid reference. Measured: the text form
       -- came back as "-" against 15.x and made every canonical comparison fail on this one
       -- field.
       p.prosupport::oid::text AS prosupport,
       p.protrftypes IS NULL AS protrftypes_is_null,
       p.proconfig IS NULL AS proconfig_is_null
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace fn ON fn.oid = p.pronamespace
LEFT JOIN pg_catalog.pg_type rt ON rt.oid = p.prorettype
LEFT JOIN pg_catalog.pg_namespace rtn ON rtn.oid = rt.typnamespace
LEFT JOIN pg_catalog.pg_language lang ON lang.oid = p.prolang
WHERE fn.nspname = $1 AND p.proname = $2`

// projectGuardFunction reads the shared function's projection, reporting whether it exists
// at all.
//
// More than one row means more than one overload of the name, which this manifest cannot
// represent — one identity, one footprint — so it is refused rather than resolved by
// picking one.
func projectGuardFunction(ctx context.Context, q rowQuerier, schema, name string) (guardFunctionForm, bool, error) {
	rows, err := q.QueryContext(ctx, guardFunctionProjectionSQL, schema, name)
	if err != nil {
		return guardFunctionForm{}, false, fmt.Errorf("sqlstore: project the guard function %s.%s: %w", schema, name, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return guardFunctionForm{}, false, fmt.Errorf("sqlstore: project the guard function %s.%s: %w", schema, name, err)
		}
		return guardFunctionForm{}, false, nil
	}
	var (
		f                  guardFunctionForm
		probin, prosqlbody sql.NullString
		cost, rows64       sql.NullFloat64
	)
	if err := rows.Scan(&f.Schema, &f.Name, &f.Kind, &f.ReturnTypeSchema, &f.ReturnTypeName,
		&f.ReturnsSet, &f.Language, &f.NArgs, &f.NArgDefaults, &f.Variadic, &f.ArgTypesCount,
		&f.AllArgTypesNull, &f.ArgModesNull, &f.ArgNamesNull, &f.ArgDefaultsNull,
		&f.Src, &probin, &prosqlbody,
		&f.SecurityDefiner, &f.LeakProof, &f.Strict, &f.Volatile, &f.Parallel,
		&cost, &rows64, &f.Support, &f.TransformsNull, &f.ConfigNull); err != nil {
		return guardFunctionForm{}, false, fmt.Errorf("sqlstore: decode the guard function %s.%s: %w", schema, name, err)
	}
	f.Bin, f.SQLBody = nullToOpt(probin), nullToOpt(prosqlbody)
	f.Cost, f.Rows = cost.Float64, rows64.Float64
	if rows.Next() {
		return guardFunctionForm{}, false, fmt.Errorf("sqlstore: %s.%s has more than one overload, which this manifest cannot represent: one function identity carries one footprint", schema, name)
	}
	return f, true, rows.Err()
}

// guardFunctionDiff names the fields in which an observed function differs from the
// canonical one.
func guardFunctionDiff(want, got guardFunctionForm) []string {
	return guardDefinitionDiff(
		guardDefinition{Function: want},
		guardDefinition{Function: got},
	)
}

// postgresServerMajor reads the server's major version.
//
// server_version_num is an integer like 150018, which is the form that cannot be
// misparsed: the TEXT server_version carries vendor suffixes ("15.4 (Debian ...)") that a
// split on '.' turns into a plausible wrong answer.
func postgresServerMajor(ctx context.Context, q rowQuerier) (int, error) {
	var raw string
	if err := q.QueryRowContext(ctx, "SHOW server_version_num").Scan(&raw); err != nil {
		return 0, fmt.Errorf("sqlstore: read server_version_num: %w", err)
	}
	raw = strings.TrimSpace(raw)
	var n int
	if _, err := fmt.Sscanf(raw, "%d", &n); err != nil || n <= 0 {
		return 0, fmt.Errorf("sqlstore: server_version_num %q is not a positive integer", raw)
	}
	return n / 10000, nil
}
