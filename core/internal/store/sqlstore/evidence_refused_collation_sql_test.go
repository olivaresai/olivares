// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import "testing"

// pgAttributeCollationIdentityGolden is the exact catalog expression the core v11
// calibration and inventory queries rendered for pg_attribute collations before the
// r107 SAST correction. Any change to it changes the calibrated collation identities.
const pgAttributeCollationIdentityGolden = `COALESCE((SELECT cn.nspname || '.' || co.collname FROM pg_catalog.pg_collation co
		JOIN pg_catalog.pg_namespace cn ON cn.oid = co.collnamespace WHERE co.oid = a.attcollation), '')`

func TestPGCollationIdentitySQLIsByteIdentical(t *testing.T) {
	if got := pgCollationIdentitySQL("a.attcollation"); got != pgAttributeCollationIdentityGolden {
		t.Fatalf("pgCollationIdentitySQL(\"a.attcollation\") changed:\n got %q\nwant %q", got, pgAttributeCollationIdentityGolden)
	}
	if pgAttributeCollationIdentitySQL != pgAttributeCollationIdentityGolden {
		t.Fatalf("the compile-time pg_attribute collation identity diverged from the calibrated expression:\n got %q\nwant %q",
			pgAttributeCollationIdentitySQL, pgAttributeCollationIdentityGolden)
	}
	const indexKey = `COALESCE((SELECT cn.nspname || '.' || co.collname FROM pg_catalog.pg_collation co
		JOIN pg_catalog.pg_namespace cn ON cn.oid = co.collnamespace WHERE co.oid = kc.coll), '')`
	if got := pgCollationIdentitySQL("kc.coll"); got != indexKey {
		t.Fatalf("pgCollationIdentitySQL(\"kc.coll\") changed:\n got %q\nwant %q", got, indexKey)
	}
}
