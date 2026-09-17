// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dialect

import "fmt"

// DRControlContract is the compiled control family. Its declarations are shared
// by construction and verification; it is not a catalog-derived calibration.
// Registered preparation/exclusion must consume this same contract in batch 2.
type DRControlContract struct {
	Major   int
	Columns []DRRestoreControlColumn
	Checks  []DRControlCheck
	// Catalog queries include the closed shape predicates for this major. Parameters
	// are measured relation/owner OIDs and exact names, never arbitrary SQL. Keeping
	// these with DDL lets later registered-family admission use the same contract.
	RelationSQL, ColumnSQL, ConstraintSQL, IndexSQL, DependencySQL, SubordinateSQL string
}
type DRControlCheck struct {
	Suffix     string
	Expression string
	// Definition is the canonical PG deparse under pg_catalog resolution.
	Definition string
	Columns    string
}

// DRRestoreControlContract currently admits major 16. Other majors are explicitly
// unsupported for an enrolled control until a compiled contract and actual major
// acceptance are supplied. This does not classify an absent control as damage.
func DRRestoreControlContract(serverMajor int) (DRControlContract, error) {
	if serverMajor != 16 {
		return DRControlContract{}, fmt.Errorf("DR restore control contract: PostgreSQL major %d is unsupported (verified contract: 16)", serverMajor)
	}
	return DRControlContract{Major: serverMajor, Columns: DRRestoreControlColumns(), RelationSQL: `SELECT c.oid::pg_catalog.int8,c.relowner::pg_catalog.int8,r.rolname,
 pg_catalog.current_setting('server_version_num')::pg_catalog.int4 / 10000,
 c.relkind='r' AND c.relpersistence='p' AND NOT c.relispartition
 AND NOT c.relrowsecurity AND NOT c.relforcerowsecurity AND c.relreplident='d'
 AND c.relam=(SELECT oid FROM pg_catalog.pg_am WHERE amname='heap')
 AND c.reltablespace=0 AND c.reloptions IS NULL AND c.reloftype=0
 AND NOT c.relisshared AND c.relispopulated AND c.relrewrite=0 AND c.relpartbound IS NULL
 AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_inherits i WHERE i.inhrelid=c.oid OR i.inhparent=c.oid)
 AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_policy p WHERE p.polrelid=c.oid)
 AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_rewrite w WHERE w.ev_class=c.oid)
 AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_trigger t WHERE t.tgrelid=c.oid)
 AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_publication_rel p WHERE p.prrelid=c.oid)
 AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_statistic_ext s WHERE s.stxrelid=c.oid)
 AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_depend d WHERE d.classid='pg_catalog.pg_class'::pg_catalog.regclass AND d.objid=c.oid AND d.deptype='e')
 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
 JOIN pg_catalog.pg_roles r ON r.oid=c.relowner
 WHERE n.nspname=$1 AND c.relname=$2`, ColumnSQL: `SELECT a.attnum,a.attname,t.typname,n.nspname,a.attnotnull,
 a.atttypmod=-1 AND NOT a.attisdropped AND a.attislocal AND a.attinhcount=0
 AND NOT a.atthasdef AND NOT a.atthasmissing AND a.attmissingval IS NULL
 AND a.attidentity='' AND a.attgenerated='' AND a.attcompression=''
 AND a.attstorage=t.typstorage AND a.attlen=t.typlen AND a.attbyval=t.typbyval AND a.attalign=t.typalign
 AND a.attndims=0 AND a.attstattarget=-1 AND a.attoptions IS NULL AND a.attfdwoptions IS NULL
 AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_attrdef d WHERE d.adrelid=a.attrelid AND d.adnum=a.attnum)
 AND CASE WHEN t.typname='text' THEN a.attcollation='pg_catalog."C"'::pg_catalog.regcollation ELSE a.attcollation=0 END
 FROM pg_catalog.pg_attribute a
 LEFT JOIN pg_catalog.pg_type t ON t.oid=a.atttypid
 LEFT JOIN pg_catalog.pg_namespace n ON n.oid=t.typnamespace
 WHERE a.attrelid=$1 AND a.attnum>0 ORDER BY a.attnum`, ConstraintSQL: `SELECT con.conname,con.contype::pg_catalog.text,
 pg_catalog.pg_get_constraintdef(con.oid,false), pg_catalog.array_to_string(con.conkey,','),
 con.convalidated AND NOT con.condeferrable AND NOT con.condeferred AND con.conislocal
 AND con.coninhcount=0 AND con.conparentid=0 AND con.connoinherit=(con.contype='p')
 AND con.connamespace=(SELECT relnamespace FROM pg_catalog.pg_class WHERE oid=$1)
 AND con.contypid=0 AND con.confrelid=0
 AND CASE WHEN con.contype='p' THEN con.conindid=(SELECT oid FROM pg_catalog.pg_class WHERE relnamespace=con.connamespace AND relname=$2) ELSE con.conindid=0 END
 AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_depend d
  WHERE d.classid='pg_catalog.pg_constraint'::pg_catalog.regclass AND d.objid=con.oid AND
  ((d.refclassid='pg_catalog.pg_proc'::pg_catalog.regclass AND d.refobjid>=16384)
   OR (d.refclassid='pg_catalog.pg_operator'::pg_catalog.regclass AND d.refobjid>=16384)
   OR (d.refclassid='pg_catalog.pg_type'::pg_catalog.regclass AND d.refobjid>=16384)))
 FROM pg_catalog.pg_constraint con WHERE con.conrelid=$1 ORDER BY con.conname`, IndexSQL: `SELECT ic.relname,
 ic.relnamespace=c.relnamespace AND ic.relowner=$2 AND ic.relkind='i' AND ic.relpersistence='p'
 AND ic.reltablespace=0 AND ic.reloptions IS NULL AND NOT ic.relispartition
 AND ic.relacl IS NULL AND ic.reltype=0 AND ic.reloftype=0 AND ic.reltoastrelid=0
 AND NOT ic.relisshared AND NOT ic.relrowsecurity AND NOT ic.relforcerowsecurity
 AND ic.relnatts=1 AND ic.relchecks=0 AND ic.relrewrite=0 AND ic.relpartbound IS NULL
 AND (SELECT count(*) FROM pg_catalog.pg_attribute WHERE attrelid=ic.oid AND attnum>0)=1
 AND EXISTS (SELECT 1 FROM pg_catalog.pg_attribute a WHERE a.attrelid=ic.oid AND a.attnum=1
  AND a.attname='control_key' AND a.atttypid='pg_catalog.text'::pg_catalog.regtype
  AND a.atttypmod=-1 AND a.attcollation='pg_catalog."C"'::pg_catalog.regcollation
  AND NOT a.attisdropped AND NOT a.atthasdef AND NOT a.atthasmissing AND NOT a.attnotnull
  AND a.attidentity='' AND a.attgenerated='' AND a.attstattarget=-1 AND a.attoptions IS NULL
  AND a.attfdwoptions IS NULL AND a.attacl IS NULL)
 AND i.indisunique AND i.indisprimary AND NOT i.indisexclusion AND i.indimmediate
 AND NOT i.indisclustered AND i.indisvalid AND NOT i.indcheckxmin AND i.indisready AND i.indislive
 AND NOT i.indisreplident AND NOT i.indnullsnotdistinct
 AND i.indnatts=1 AND i.indnkeyatts=1 AND i.indkey::pg_catalog.text='1'
 AND i.indoption::pg_catalog.text='0' AND i.indexprs IS NULL AND i.indpred IS NULL
 AND i.indcollation[0]='pg_catalog."C"'::pg_catalog.regcollation
 AND am.amname='btree' AND oc.opcnamespace='pg_catalog'::pg_catalog.regnamespace
 AND oc.opcname='text_ops' AND oc.opcdefault AND oc.opcmethod=am.oid
 AND oc.opcintype='pg_catalog.text'::pg_catalog.regtype AND oc.opckeytype=0
 AND oc.opcfamily=(SELECT f.oid FROM pg_catalog.pg_opfamily f WHERE f.opfnamespace='pg_catalog'::pg_catalog.regnamespace AND f.opfname='text_ops' AND f.opfmethod=am.oid)
 AND EXISTS (SELECT 1 FROM pg_catalog.pg_constraint con WHERE con.conrelid=c.oid AND con.contype='p' AND con.conindid=ic.oid)
 FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class c ON c.oid=i.indrelid
 JOIN pg_catalog.pg_class ic ON ic.oid=i.indexrelid
 JOIN pg_catalog.pg_am am ON am.oid=ic.relam
 JOIN pg_catalog.pg_opclass oc ON oc.oid=i.indclass[0]
 WHERE i.indrelid=$1`, DependencySQL: `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_depend d
 JOIN pg_catalog.pg_class c ON c.oid=d.objid
 WHERE d.refclassid='pg_catalog.pg_class'::pg_catalog.regclass AND d.refobjid=$1
 AND d.classid='pg_catalog.pg_class'::pg_catalog.regclass AND c.relkind='S')
 OR EXISTS (SELECT 1 FROM pg_catalog.pg_constraint WHERE confrelid=$1)`, SubordinateSQL: `SELECT
 t.typtype='c' AND t.typcategory='C' AND t.typrelid=c.oid AND t.typname=c.relname
 AND t.typnamespace=c.relnamespace AND t.typowner=c.relowner AND t.typelem=0
 AND t.typbasetype=0 AND NOT t.typnotnull AND t.typcollation=0 AND t.typacl IS NULL
 AND t.typlen=-1 AND NOT t.typbyval AND NOT t.typispreferred AND t.typisdefined
 AND t.typdelim=',' AND t.typsubscript=0 AND t.typalign='d' AND t.typstorage='x'
 AND t.typtypmod=-1 AND t.typndims=0 AND t.typdefaultbin IS NULL AND t.typdefault IS NULL
 AND t.typinput='pg_catalog.record_in'::pg_catalog.regproc AND t.typoutput='pg_catalog.record_out'::pg_catalog.regproc
 AND t.typreceive='pg_catalog.record_recv'::pg_catalog.regproc AND t.typsend='pg_catalog.record_send'::pg_catalog.regproc
 AND t.typmodin=0 AND t.typmodout=0 AND t.typanalyze=0
 AND a.typelem=t.oid AND a.typtype='b' AND a.typcategory='A'
 AND a.typname='_'||c.relname AND a.typnamespace=c.relnamespace AND a.typowner=c.relowner
 AND a.typbasetype=0 AND NOT a.typnotnull AND a.typcollation=0 AND a.typacl IS NULL
 AND a.typlen=-1 AND NOT a.typbyval AND NOT a.typispreferred AND a.typisdefined
 AND a.typdelim=',' AND a.typsubscript='pg_catalog.array_subscript_handler'::pg_catalog.regproc
 AND a.typalign='d' AND a.typstorage='x' AND a.typrelid=0 AND a.typarray=0
 AND a.typtypmod=-1 AND a.typndims=0 AND a.typdefaultbin IS NULL AND a.typdefault IS NULL
 AND a.typinput='pg_catalog.array_in'::pg_catalog.regproc AND a.typoutput='pg_catalog.array_out'::pg_catalog.regproc
 AND a.typreceive='pg_catalog.array_recv'::pg_catalog.regproc AND a.typsend='pg_catalog.array_send'::pg_catalog.regproc
 AND a.typmodin=0 AND a.typmodout=0 AND a.typanalyze='pg_catalog.array_typanalyze'::pg_catalog.regproc
 AND toast.relname='pg_toast_'||c.oid::pg_catalog.text AND toast.relnamespace='pg_toast'::pg_catalog.regnamespace
 AND toast.relowner=c.relowner AND toast.relkind='t' AND toast.relpersistence='p'
 AND toast.relnatts=3 AND toast.relchecks=0 AND toast.reloptions IS NULL AND toast.relacl IS NULL
 AND toast.relam=(SELECT oid FROM pg_catalog.pg_am WHERE amname='heap')
 AND toast.reltype=0 AND toast.reloftype=0 AND toast.reltoastrelid=0 AND toast.reltablespace=0
 AND toast.relreplident='n' AND NOT toast.relisshared AND toast.relispopulated
 AND toast.relrewrite=0 AND toast.relpartbound IS NULL
 AND NOT toast.relrowsecurity AND NOT toast.relforcerowsecurity AND NOT toast.relispartition
 AND (SELECT count(*) FROM pg_catalog.pg_attribute WHERE attrelid=toast.oid AND attnum>0)=3
 AND NOT EXISTS (
  SELECT 1 FROM (VALUES (1,'chunk_id','pg_catalog.oid'::pg_catalog.regtype),
   (2,'chunk_seq','pg_catalog.int4'::pg_catalog.regtype),(3,'chunk_data','pg_catalog.bytea'::pg_catalog.regtype)) expected(num,name,typ)
  LEFT JOIN pg_catalog.pg_attribute x ON x.attrelid=toast.oid AND x.attnum=expected.num
  WHERE x.attnum IS NULL OR x.attname<>expected.name OR x.atttypid<>expected.typ
   OR x.atttypmod<>-1 OR x.attcollation<>0 OR x.attnotnull OR x.attisdropped
   OR x.attalign<>'i' OR x.attlen<>(CASE WHEN expected.num=3 THEN -1 ELSE 4 END)
   OR x.attbyval<>(expected.num<>3)
   OR NOT x.attislocal OR x.attinhcount<>0 OR x.atthasdef OR x.atthasmissing
   OR x.attmissingval IS NOT NULL OR x.attidentity<>'' OR x.attgenerated<>''
   OR x.attcompression<>'' OR x.attstorage<>'p' OR x.attndims<>0 OR x.attstattarget<>-1
   OR x.attacl IS NOT NULL OR x.attoptions IS NOT NULL OR x.attfdwoptions IS NOT NULL)
 AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_trigger WHERE tgrelid=toast.oid)
 AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_rewrite WHERE ev_class=toast.oid)
 AND (SELECT count(*) FROM pg_catalog.pg_index WHERE indrelid=toast.oid)=1
 AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_depend d
  WHERE d.refclassid='pg_catalog.pg_class'::pg_catalog.regclass AND d.refobjid=toast.oid
  AND NOT (d.classid='pg_catalog.pg_class'::pg_catalog.regclass
   AND d.objid IN (SELECT indexrelid FROM pg_catalog.pg_index WHERE indrelid=toast.oid)
   AND d.deptype IN ('a','i')))
 AND EXISTS (SELECT 1 FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class ic ON ic.oid=i.indexrelid
  WHERE i.indrelid=toast.oid AND ic.relname=toast.relname||'_index'
  AND ic.relnamespace=toast.relnamespace AND ic.relowner=c.relowner AND ic.relkind='i'
  AND ic.relpersistence='p' AND ic.reloptions IS NULL AND ic.relacl IS NULL
  AND ic.relam=(SELECT oid FROM pg_catalog.pg_am WHERE amname='btree') AND ic.reltablespace=0
  AND i.indclass[0]=(SELECT oid FROM pg_catalog.pg_opclass WHERE opcnamespace='pg_catalog'::pg_catalog.regnamespace AND opcname='oid_ops' AND opcmethod=ic.relam)
  AND i.indclass[1]=(SELECT oid FROM pg_catalog.pg_opclass WHERE opcnamespace='pg_catalog'::pg_catalog.regnamespace AND opcname='int4_ops' AND opcmethod=ic.relam)
  AND i.indisunique AND i.indisprimary AND i.indimmediate AND i.indisvalid AND i.indisready AND i.indislive
  AND NOT i.indisexclusion AND NOT i.indisclustered AND NOT i.indisreplident
  AND NOT i.indnullsnotdistinct AND NOT i.indcheckxmin
  AND i.indnatts=2 AND i.indnkeyatts=2 AND i.indkey::pg_catalog.text='1 2'
  AND i.indoption::pg_catalog.text='0 0' AND i.indcollation::pg_catalog.text='0 0'
  AND i.indexprs IS NULL AND i.indpred IS NULL)
 AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_depend d
  WHERE d.refclassid='pg_catalog.pg_class'::pg_catalog.regclass AND d.refobjid=c.oid
  AND NOT (
   (d.classid='pg_catalog.pg_type'::pg_catalog.regclass AND d.objid=t.oid AND d.deptype='i')
   OR (d.classid='pg_catalog.pg_class'::pg_catalog.regclass AND d.objid=toast.oid AND d.deptype='i')
   OR (d.classid='pg_catalog.pg_constraint'::pg_catalog.regclass AND d.objid IN (SELECT oid FROM pg_catalog.pg_constraint WHERE conrelid=c.oid) AND d.deptype IN ('a','n'))))
 AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_depend d WHERE d.refclassid='pg_catalog.pg_type'::pg_catalog.regclass
  AND d.refobjid IN (t.oid,a.oid) AND NOT (d.classid='pg_catalog.pg_type'::pg_catalog.regclass AND d.objid=a.oid AND d.refobjid=t.oid AND d.deptype='i'))
 AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_description d WHERE
  d.classoid='pg_catalog.pg_class'::pg_catalog.regclass AND (d.objoid IN (c.oid,toast.oid) OR d.objoid IN (SELECT indexrelid FROM pg_catalog.pg_index WHERE indrelid IN (c.oid,toast.oid)))
  OR d.classoid='pg_catalog.pg_type'::pg_catalog.regclass AND d.objoid IN (t.oid,a.oid)
  OR d.classoid='pg_catalog.pg_constraint'::pg_catalog.regclass AND d.objoid IN (SELECT oid FROM pg_catalog.pg_constraint WHERE conrelid=c.oid))
 AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_seclabel d WHERE d.classoid='pg_catalog.pg_class'::pg_catalog.regclass AND d.objoid=c.oid)
 FROM pg_catalog.pg_class c
 JOIN pg_catalog.pg_type t ON t.oid=c.reltype JOIN pg_catalog.pg_type a ON a.oid=t.typarray
 JOIN pg_catalog.pg_class toast ON toast.oid=c.reltoastrelid WHERE c.oid=$1`, Checks: []DRControlCheck{
		{"singleton", `control_key OPERATOR(pg_catalog.=) 'restore'`, `CHECK ((control_key = 'restore'::text))`, "1"},
		{"format", `format OPERATOR(pg_catalog.=) 1`, `CHECK ((format = 1))`, "2"},
		{"revision", `revision OPERATOR(pg_catalog.>) 0`, `CHECK ((revision > 0))`, "3"},
		{"state", `state OPERATOR(pg_catalog.=) 'pending' OR state OPERATOR(pg_catalog.=) 'indeterminate' OR state OPERATOR(pg_catalog.=) 'quarantined' OR state OPERATOR(pg_catalog.=) 'complete'`, `CHECK (((state = 'pending'::text) OR (state = 'indeterminate'::text) OR (state = 'quarantined'::text) OR (state = 'complete'::text)))`, "4"},
		{"op_id", `op_id OPERATOR(pg_catalog.~) '^[0-9a-f]{32}$'`, `CHECK ((op_id ~ '^[0-9a-f]{32}$'::text))`, "5"},
		{"plan_sha256", `pg_catalog.octet_length(plan_sha256) OPERATOR(pg_catalog.=) 32`, `CHECK ((octet_length(plan_sha256) = 32))`, "6"},
		{"destination_database", `pg_catalog.octet_length(destination_database) OPERATOR(pg_catalog.>) 0`, `CHECK ((octet_length(destination_database) > 0))`, "7"},
		{"destination_schema", `pg_catalog.octet_length(destination_schema) OPERATOR(pg_catalog.>) 0`, `CHECK ((octet_length(destination_schema) > 0))`, "8"},
		{"destination_system_identifier", `destination_system_identifier OPERATOR(pg_catalog.~) '^[1-9][0-9]{0,19}$'`, `CHECK ((destination_system_identifier ~ '^[1-9][0-9]{0,19}$'::text))`, "9"},
		{"keyset_sha256", `keyset_sha256 IS NULL OR pg_catalog.octet_length(keyset_sha256) OPERATOR(pg_catalog.=) 32`, `CHECK (((keyset_sha256 IS NULL) OR (octet_length(keyset_sha256) = 32)))`, "10"},
		{"complete_keyset", `(state OPERATOR(pg_catalog.=) 'complete') OPERATOR(pg_catalog.=) (keyset_sha256 IS NOT NULL)`, `CHECK (((state = 'complete'::text) = (keyset_sha256 IS NOT NULL)))`, "4,10"},
		{"report_sha256", `report_sha256 IS NULL OR pg_catalog.octet_length(report_sha256) OPERATOR(pg_catalog.=) 32`, `CHECK (((report_sha256 IS NULL) OR (octet_length(report_sha256) = 32)))`, "11"},
		{"observed_at", `pg_catalog.isfinite(observed_at)`, `CHECK (isfinite(observed_at))`, "12"},
	}}, nil
}
func (c DRControlContract) DDL() string {
	ddl := `CREATE TABLE ` + EngineSchema + `.` + DRRestoreControlTable + ` (`
	for _, col := range c.Columns {
		ddl += "\n  " + col.Name + " pg_catalog." + col.Type
		if col.Type == "text" {
			ddl += ` COLLATE pg_catalog."C"`
		}
		if col.NotNull {
			ddl += " NOT NULL"
		}
		ddl += ","
	}
	ddl += "\n  CONSTRAINT " + DRRestoreControlTable + "_pkey PRIMARY KEY (control_key)"
	for _, check := range c.Checks {
		ddl += ",\n  CONSTRAINT " + DRRestoreControlTable + "_" + check.Suffix + " CHECK (" + check.Expression + ")"
	}
	return ddl + "\n)"
}
