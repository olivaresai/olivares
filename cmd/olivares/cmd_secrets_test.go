// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func runSecrets(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newSecretsCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestSecretsListTextAndJSON(t *testing.T) {
	dir := initialisedDataDir(t)

	textOut, err := runSecrets(t, "ls", "--data-dir", dir)
	if err != nil {
		t.Fatalf("secrets ls text: %v\n%s", err, textOut)
	}
	if textOut != "no secrets stored\n" {
		t.Fatalf("secrets text output changed: %q", textOut)
	}

	jsonOut, err := runSecrets(t, "ls", "--data-dir", dir, "--format", "json")
	if err != nil {
		t.Fatalf("secrets ls json: %v\n%s", err, jsonOut)
	}
	var items []secretListItem
	if err := json.Unmarshal([]byte(jsonOut), &items); err != nil {
		t.Fatalf("secrets JSON is invalid: %v\n%s", err, jsonOut)
	}
	if len(items) != 0 {
		t.Fatalf("secrets JSON = %#v, want an empty list", items)
	}
}
