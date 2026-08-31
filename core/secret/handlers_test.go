// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secret_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/secret"
)

func TestEnvHandler(t *testing.T) {
	h := secret.EnvHandler{Lookup: func(k string) (string, bool) {
		switch k {
		case "SET":
			return "value", true
		case "EMPTY":
			return "", true
		default:
			return "", false
		}
	}}
	if v, err := h.Resolve(context.Background(), "SET"); err != nil || string(v) != "value" {
		t.Errorf("SET = (%q,%v)", v, err)
	}
	if _, err := h.Resolve(context.Background(), "EMPTY"); err == nil {
		t.Error("empty env var should fail closed")
	}
	if _, err := h.Resolve(context.Background(), "MISSING"); err == nil {
		t.Error("missing env var should fail closed")
	}
}

func TestFileHandler(t *testing.T) {
	dir := t.TempDir()
	withNL := filepath.Join(dir, "tok")
	if err := os.WriteFile(withNL, []byte("s3cr3t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := secret.FileHandler{}
	if v, err := h.Resolve(context.Background(), withNL); err != nil || string(v) != "s3cr3t" {
		t.Errorf("trailing newline not trimmed: (%q,%v)", v, err)
	}
	if _, err := h.Resolve(context.Background(), empty); err == nil {
		t.Error("empty file should fail closed")
	}
	if _, err := h.Resolve(context.Background(), filepath.Join(dir, "nope")); err == nil {
		t.Error("missing file should fail closed")
	}
}
