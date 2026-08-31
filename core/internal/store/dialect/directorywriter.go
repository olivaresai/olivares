// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dialect

import "fmt"

// The directory-writer control is a raw engine-owned capability, not an entity.
// These names are its durable ABI: old and new binaries meet at the database
// objects and must not be able to spell different controls accidentally.
const (
	DirectoryWriterControlTable  = "directory_writer_control"
	DirectoryWriterMarkerTable   = "directory_writer_marker"
	DirectoryWriterControlKey    = "core.directory.writer"
	DirectoryWriterGenerationGUC = "app.directory_writer_generation"
	DirectoryWriterGuardFunction = "olivares_directory_writer_guard"
)

const directoryWriterInitialGeneration int64 = 1

func (d postgresDialect) DirectoryWriterControlStmts() []string {
	return []string{
		`CREATE TABLE ` + EngineSchema + `.` + DirectoryWriterControlTable + ` (
  control_key pg_catalog.text COLLATE pg_catalog."C" NOT NULL PRIMARY KEY,
  mode pg_catalog.text COLLATE pg_catalog."C" NOT NULL,
  expected_generation pg_catalog.int8 NOT NULL,
  CHECK (control_key OPERATOR(pg_catalog.=) '` + DirectoryWriterControlKey + `'),
  CHECK (mode OPERATOR(pg_catalog.=) 'staged' OR mode OPERATOR(pg_catalog.=) 'enforced'),
  CHECK (expected_generation OPERATOR(pg_catalog.>) 0)
)`,
		fmt.Sprintf(`INSERT INTO %s.%s(control_key, mode, expected_generation) VALUES ('%s', 'staged', %d)`,
			EngineSchema, DirectoryWriterControlTable, DirectoryWriterControlKey, directoryWriterInitialGeneration),
		pgDirectoryWriterControlACLUnlessOwner(d.appRole),
	}
}

// pgDirectoryWriterControlACLUnlessOwner closes the split-owner boundary in
// the SAME transaction that creates v7. Waiting for the per-boot reconcile
// would publish a crash window in which the application role could mutate the
// control. In a single-role deployment the application role owns the table;
// revoking there would brick the engine and would not form an independent
// boundary anyway, so that case is deliberately left as a capability posture.
func pgDirectoryWriterControlACLUnlessOwner(role string) string {
	// Both identifiers are compile-time ABI constants; keep this renderer local
	// to dialect rather than depending on sqlstore's identifier helper.
	qualified := `"public"."directory_writer_control"`
	body := fmt.Sprintf(`
DECLARE
  target pg_catalog.text := %s;
  relid pg_catalog.oid := pg_catalog.to_regclass(%s);
  can_select pg_catalog.bool;
  can_insert pg_catalog.bool;
  can_update pg_catalog.bool;
  can_delete pg_catalog.bool;
  can_truncate pg_catalog.bool;
  can_references pg_catalog.bool;
  can_trigger pg_catalog.bool;
  can_insert_column pg_catalog.bool;
  can_update_column pg_catalog.bool;
  can_references_column pg_catalog.bool;
BEGIN
  IF relid IS NULL THEN
    RAISE EXCEPTION 'directory writer control relation is absent';
  END IF;
  EXECUTE 'REVOKE ALL PRIVILEGES ON TABLE %s FROM PUBLIC';
  IF NOT EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles
    WHERE rolname OPERATOR(pg_catalog.=) target
  ) THEN
    RAISE EXCEPTION 'directory writer application role %% is absent', target;
  END IF;
  IF EXISTS (
    SELECT 1
    FROM pg_catalog.pg_class c
    JOIN pg_catalog.pg_roles r ON r.oid OPERATOR(pg_catalog.=) c.relowner
    WHERE c.oid OPERATOR(pg_catalog.=) relid
      AND r.rolname OPERATOR(pg_catalog.=) target
  ) THEN
    RETURN;
  END IF;
  EXECUTE pg_catalog.format('REVOKE ALL PRIVILEGES ON TABLE %s FROM %%I', target);
  EXECUTE pg_catalog.format('GRANT SELECT ON TABLE %s TO %%I', target);
  SELECT pg_catalog.has_table_privilege(target, relid, 'SELECT'),
         pg_catalog.has_table_privilege(target, relid, 'INSERT'),
         pg_catalog.has_table_privilege(target, relid, 'UPDATE'),
         pg_catalog.has_table_privilege(target, relid, 'DELETE'),
         pg_catalog.has_table_privilege(target, relid, 'TRUNCATE'),
         pg_catalog.has_table_privilege(target, relid, 'REFERENCES'),
         pg_catalog.has_table_privilege(target, relid, 'TRIGGER'),
         pg_catalog.has_any_column_privilege(target, relid, 'INSERT'),
         pg_catalog.has_any_column_privilege(target, relid, 'UPDATE'),
         pg_catalog.has_any_column_privilege(target, relid, 'REFERENCES')
    INTO can_select, can_insert, can_update, can_delete, can_truncate,
         can_references, can_trigger, can_insert_column, can_update_column,
         can_references_column;
  IF NOT can_select OR can_insert OR can_update OR can_delete OR can_truncate
     OR can_references OR can_trigger OR can_insert_column OR can_update_column
     OR can_references_column THEN
    RAISE EXCEPTION 'directory writer application role %% is not SELECT-only on %%',
      target, relid::pg_catalog.regclass::pg_catalog.text;
  END IF;
END `, pgDollarQuote(role), pgDollarQuote(EngineSchema+"."+DirectoryWriterControlTable), qualified, qualified, qualified)
	tag := pgDollarTagNotIn(body)
	return "DO " + tag + body + tag
}

func (sqliteDialect) DirectoryWriterControlStmts() []string {
	return []string{
		`CREATE TABLE ` + DirectoryWriterControlTable + ` (
  control_key TEXT COLLATE BINARY NOT NULL PRIMARY KEY,
  mode TEXT COLLATE BINARY NOT NULL,
  expected_generation INTEGER NOT NULL,
  CHECK (control_key = '` + DirectoryWriterControlKey + `'),
  CHECK (mode IN ('staged', 'enforced')),
  CHECK (expected_generation > 0)
)`,
		fmt.Sprintf(`INSERT INTO main.%s(control_key, mode, expected_generation) VALUES ('%s', 'staged', %d)`,
			DirectoryWriterControlTable, DirectoryWriterControlKey, directoryWriterInitialGeneration),
		`CREATE TABLE ` + DirectoryWriterMarkerTable + ` (
  control_key TEXT COLLATE BINARY NOT NULL PRIMARY KEY,
  generation INTEGER NOT NULL,
  CHECK (control_key = '` + DirectoryWriterControlKey + `'),
  CHECK (generation > 0)
)`,
	}
}
