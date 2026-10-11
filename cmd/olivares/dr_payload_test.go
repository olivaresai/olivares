// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDRDumpExcludesExactCoordinationRelation(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	bin := filepath.Join(dir, "pg_dump")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$DRW_DUMP_ARGS\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DRW_DUMP_ARGS", argsFile)
	if err := runPgDump(t.Context(), bin, "dbname=fixture", filepath.Join(dir, "dump")); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains("\n"+string(args), "\n--exclude-table=\"public\".\"olv_dr_restore_control_v1\"\n") {
		t.Fatalf("dump must exclude the exact qualified control relation: %s", args)
	}
	if strings.Contains(string(args), "*") {
		t.Fatal("dump exclusion must not discard similarly named customer objects")
	}
}
