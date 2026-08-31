// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileTokenSourceDenyClosedWhenUnconfigured(t *testing.T) {
	src := NewFileTokenSource(FileTokenConfig{}) // empty path
	if _, ok := src.(DenyCredentialSource); !ok {
		t.Fatalf("an unconfigured file token source must be the deny-closed default, got %T", src)
	}
}

func TestFileTokenSourceMintsAndScopes(t *testing.T) {
	dir := t.TempDir()
	// per-(env,mode) token files written by an external refresher
	writeTok := func(env, mode, tok string) {
		if err := os.WriteFile(filepath.Join(dir, env+"-"+mode+".token"), []byte(tok+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeTok("prod", "write", "SHORT-LIVED-PROD-WRITE")
	writeTok("prod", "read", "SHORT-LIVED-PROD-READ")

	src := NewFileTokenSource(FileTokenConfig{PathTemplate: filepath.Join(dir, "{env}-{mode}.token"), TTL: time.Hour, Scheme: "vault-agent"})

	cw, err := src.Mint(context.Background(), MintRequest{Environment: "prod", Mode: ModeWrite})
	if err != nil {
		t.Fatal(err)
	}
	if cw.Token != "SHORT-LIVED-PROD-WRITE" {
		t.Fatalf("write token = %q", cw.Token)
	}
	if cw.Expired(nowFunc()) {
		t.Fatalf("freshly minted token must not be expired")
	}
	// the ID is a non-sensitive fingerprint, NOT the token material
	if strings.Contains(cw.ID, "SHORT-LIVED") {
		t.Fatalf("credential ID must not contain the token material: %q", cw.ID)
	}
	cr, err := src.Mint(context.Background(), MintRequest{Environment: "prod", Mode: ModeRead})
	if err != nil || cr.Token != "SHORT-LIVED-PROD-READ" {
		t.Fatalf("read token scoping failed: tok=%q err=%v", cr.Token, err)
	}
}

func TestFileTokenSourceFailsClosedOnMissingFile(t *testing.T) {
	dir := t.TempDir()
	src := NewFileTokenSource(FileTokenConfig{PathTemplate: filepath.Join(dir, "{env}.token"), TTL: time.Hour})
	_, err := src.Mint(context.Background(), MintRequest{Environment: "staging", Mode: ModeWrite})
	if !errors.Is(err, ErrNoCredentialSource) {
		t.Fatalf("a missing token file must fail closed (no default key), got %v", err)
	}
}

func TestFileTokenSourcePathTraversalContained(t *testing.T) {
	dir := t.TempDir()
	// a crafted environment must not escape the token directory
	src := NewFileTokenSource(FileTokenConfig{PathTemplate: filepath.Join(dir, "{env}.token"), TTL: time.Hour})
	_, err := src.Mint(context.Background(), MintRequest{Environment: "../../etc/passwd", Mode: ModeRead})
	if err == nil {
		t.Fatalf("a traversal env must not resolve to a file outside the token dir")
	}
}
