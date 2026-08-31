// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mysqlaudit

import (
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestTableOpToMode(t *testing.T) {
	cases := []struct {
		op   string
		mode model.AccessMode
		ok   bool
	}{
		{"READ", model.ModeRead, true},
		{"WRITE", model.ModeWrite, true},
		{"CREATE", model.ModeWrite, true},
		{"ALTER", model.ModeWrite, true},
		{"DROP", model.ModeWrite, true},
		{"RENAME", model.ModeWrite, true},
		{"QUERY", "", false},
		{"CONNECT", "", false},
	}
	for _, c := range cases {
		if mode, ok := tableOpToMode(c.op); mode != c.mode || ok != c.ok {
			t.Errorf("tableOpToMode(%q) = (%q,%v), want (%q,%v)", c.op, mode, ok, c.mode, c.ok)
		}
	}
}

func TestClassifyVerb(t *testing.T) {
	cases := []struct {
		sql  string
		mode model.AccessMode
		verb string
	}{
		{"SELECT 1", model.ModeRead, "SELECT"},
		{"'SELECT id, name FROM t'", model.ModeRead, "SELECT"},
		{"insert into t values (1)", model.ModeWrite, "INSERT"},
		{"UPDATE t SET a=1", model.ModeWrite, "UPDATE"},
		{"DELETE FROM t", model.ModeWrite, "DELETE"},
		{"CREATE TABLE t (id int)", model.ModeWrite, "CREATE"},
		{"SHOW TABLES", model.ModeRead, "SHOW"},
		{"CALL my_proc()", model.ModeUnknown, "CALL"},
		{"", model.ModeUnknown, "QUERY"},
	}
	for _, c := range cases {
		if mode, verb := classifyVerb(c.sql); mode != c.mode || verb != c.verb {
			t.Errorf("classifyVerb(%q) = (%q,%q), want (%q,%q)", c.sql, mode, verb, c.mode, c.verb)
		}
	}
}

func TestParseAuditLineCommaInQuery(t *testing.T) {
	line := `20260603 10:23:48,dbserver1,app_rw,10.0.0.5,42,1004,QUERY,salesdb,'SELECT id, name FROM customers WHERE id = 7',0`
	ev, ok := parseAuditLine(line)
	if !ok {
		t.Fatal("expected ok")
	}
	if ev.user != "app_rw" || ev.host != "10.0.0.5" || ev.operation != "QUERY" || ev.database != "salesdb" {
		t.Errorf("got %+v", ev)
	}
	if ev.object != "'SELECT id, name FROM customers WHERE id = 7'" {
		t.Errorf("object = %q (comma inside the query must be preserved, retcode stripped)", ev.object)
	}
}

func TestParseAuditLineTableEvent(t *testing.T) {
	ev, ok := parseAuditLine("20260603 10:23:45,dbserver1,app_rw,10.0.0.5,42,1001,READ,salesdb,customers,0")
	if !ok || ev.operation != "READ" || ev.object != "customers" || ev.database != "salesdb" {
		t.Errorf("got %+v ok=%v", ev, ok)
	}
}

func TestParseGeneralLine(t *testing.T) {
	e, ok := parseGeneralLine("2026-06-03T10:23:45.100000Z\t   42 Query\tSELECT 1")
	if !ok || e.connID != "42" || e.command != "Query" || e.argument != "SELECT 1" {
		t.Errorf("got %+v ok=%v", e, ok)
	}
	e2, ok := parseGeneralLine("2026-06-03T10:23:47.500000Z\t   43 Init DB\tanalytics")
	if !ok || e2.command != "Init DB" || e2.argument != "analytics" {
		t.Errorf("got %+v ok=%v", e2, ok)
	}
	// Header / continuation lines do not parse.
	if _, ok := parseGeneralLine("Time                 Id Command    Argument"); ok {
		t.Error("header line must not parse as an entry")
	}
	if _, ok := parseGeneralLine("  AND name = 'x'"); ok {
		t.Error("a statement-continuation line must not parse as an entry")
	}
}

func TestParseConnectArg(t *testing.T) {
	cases := []struct {
		arg, uh, db string
	}{
		{"app_rw@10.0.0.5 on salesdb", "app_rw@10.0.0.5", "salesdb"},
		{"root@localhost on  using SSL/TLS", "root@localhost", ""},
		{"svc@host on warehouse using SSL/TLS", "svc@host", "warehouse"},
		{"user@host", "user@host", ""},
	}
	for _, c := range cases {
		if uh, db := parseConnectArg(c.arg); uh != c.uh || db != c.db {
			t.Errorf("parseConnectArg(%q) = (%q,%q), want (%q,%q)", c.arg, uh, db, c.uh, c.db)
		}
	}
}

func TestDBFromUse(t *testing.T) {
	if db, ok := dbFromUse("USE warehouse"); !ok || db != "warehouse" {
		t.Errorf("dbFromUse(USE warehouse) = (%q,%v)", db, ok)
	}
	if db, ok := dbFromUse("USE `warehouse`;"); !ok || db != "warehouse" {
		// backtick quotes and a trailing semicolon are stripped.
		t.Errorf("dbFromUse(USE `warehouse`;) = (%q,%v)", db, ok)
	}
	if _, ok := dbFromUse("SELECT 1"); ok {
		t.Error("non-USE statement must return ok=false")
	}
}

func TestUserOf(t *testing.T) {
	if userOf("app_rw@10.0.0.5") != "app_rw" {
		t.Error("userOf should return the user before @")
	}
	if userOf("plainuser") != "plainuser" {
		t.Error("userOf without @ returns the whole string")
	}
}
