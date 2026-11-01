// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestFingerprintsAllowAdditiveDeclarations(t *testing.T) {
	const filename = "core/internal/store/sqlstore/migrations.go"
	const before = `package sqlstore

import "github.com/olivaresai/olivares/core/migrate"

type Dialect interface { Guard() []string }

func buildCoreMigrations(d Dialect) []migrate.Migration {
	return []migrate.Migration{{Version: 1, Name: "one", Stmts: d.Guard()}}
}
`
	const additive = before + `
type Other struct{}

func (Other) Guard() []string { return nil }

func buildCoreMigrationV2() migrate.Migration {
	return migrate.Migration{Version: 2, Name: "two"}
}
`
	changed := strings.Replace(before, `"one"`, `"changed"`, 1)
	moved := strings.Replace(before, "Stmts: d.Guard()", "Stmts: derive()", 1) + "\nfunc derive() []string { return nil }\n"
	movedEdited := strings.Replace(moved, "return nil", "return []string{\"changed\"}", 1)
	shadowed := additive + "\nfunc min(a, b int) int { return a }\n"
	input, err := json.Marshal([]map[string]string{{filename: before}, {filename: additive}, {filename: changed}, {filename: moved}, {filename: movedEdited}, {filename: shadowed}})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(bytes.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	var hashes []map[string]string
	if err := json.NewDecoder(&output).Decode(&hashes); err != nil {
		t.Fatal(err)
	}
	if hashes[0]["core/1"] != hashes[1]["core/1"] {
		t.Fatal("an unrelated additive declaration changed a historical fingerprint")
	}
	if hashes[1]["core/2"] == "" {
		t.Fatal("a new migration was not fingerprinted")
	}
	if hashes[0]["core/1"] == hashes[2]["core/1"] {
		t.Fatal("an existing migration edit did not change its fingerprint")
	}
	if hashes[3]["core/1"] == hashes[4]["core/1"] {
		t.Fatal("editing a helper added after the baseline did not change the fingerprint")
	}
	if hashes[0]["core/1"] == hashes[5]["core/1"] {
		t.Fatal("a package-level builtin shadow did not change the fingerprint")
	}
}
