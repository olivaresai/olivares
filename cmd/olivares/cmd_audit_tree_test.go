// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func runAuditTree(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return runAuditTreeInput(t, nil, args...)
}

func runAuditTreeInput(t *testing.T, input io.Reader, args ...string) (string, error) {
	t.Helper()
	cmd := newAuditCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(input)
	cmd.SetArgs(append([]string{"tree"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func TestAuditTreeCheckpointPostgresSplitRoles(t *testing.T) {
	pg := enginetest.IsolatedPostgresSplitOwner(t)
	dir := t.TempDir()
	eng, err := boot(t.Context(), bootConfig{
		DataDir: dir, Engine: "postgres", DSN: pg.App, OwnerDSN: pg.Owner,
		AdminDSN: pg.Admin, DemoSeed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	tenant := eng.demoTenant.String()
	pub := base64.StdEncoding.EncodeToString(eng.signer.PublicKey())
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	// Complete the documented post-migration DBA step before reopening without
	// an admin pool. This installs the closed inventory, not a weaker boot guard.
	spec := store.PgProvisionSpec{Database: pg.Database,
		App: store.PgRole{Name: appRoleOf(t, pg.App)}, Owner: store.PgRole{Name: appRoleOf(t, pg.Owner)}}
	if err := installTenantInventory(t.Context(), pg.Superuser, spec); err != nil {
		t.Fatalf("install fixture tenant inventory: %v", err)
	}
	// Use the same private DSN files an operator supplies, without putting
	// passwords in the command's arguments or its failure output.
	app := "file:" + writeTemp(t, "app.dsn", pg.App)
	owner := "file:" + writeTemp(t, "owner.dsn", pg.Owner)
	flags := []string{"--engine", "postgres", "--dsn", app, "--owner-dsn", owner,
		"--data-dir", dir, "--tenant", tenant}
	checkpoint, err := runAuditTree(t, append([]string{"checkpoint"}, flags...)...)
	if err != nil {
		t.Fatalf("split-role tree checkpoint: %v\n%s", err, checkpoint)
	}
	if !strings.HasPrefix(checkpoint, "olivares.ai/audit/"+tenant+"\n") {
		t.Fatalf("checkpoint is not a signed note: %q", checkpoint)
	}
	// Verify the note through stdin with '-'. The verifier must accept the note
	// using the same role pair, rather than merely accepting the new flag.
	out, err := runAuditTreeInput(t, strings.NewReader(checkpoint), "verify", "--engine", "postgres",
		"--dsn", app, "--owner-dsn", owner, "--data-dir", dir, "--tenant", tenant,
		"--checkpoint", "-", "--pubkey", pub)
	if err != nil || !strings.Contains(out, "signature ok") {
		t.Fatalf("split-role tree verify: %v\n%s", err, out)
	}
	// Model an upgraded ledger whose v24 table exists but has not caught up.
	// Only derived tree rows are removed, in this test's isolated database;
	// the signed audit events and their append-only protections remain intact.
	maintenance, err := sql.Open("pgx", pg.Superuser)
	if err != nil {
		t.Fatal(err)
	}
	defer maintenance.Close()
	if _, err := maintenance.ExecContext(t.Context(), "TRUNCATE public.audit_tree"); err != nil {
		t.Fatal(err)
	}
	checkpoint, err = runAuditTree(t, append([]string{"checkpoint"}, flags...)...)
	if err != nil {
		t.Fatalf("split-role legacy tree checkpoint: %v\n%s", err, checkpoint)
	}
	args := append([]string{"verify"}, flags...)
	args = append(args, "--checkpoint", "-", "--pubkey", pub)
	out, err = runAuditTreeInput(t, strings.NewReader(checkpoint), args...)
	if err != nil || !strings.Contains(out, "signature ok") {
		t.Fatalf("split-role caught-up tree verify: %v\n%s", err, out)
	}
}

func TestAuditTreeStdinSizeLimit(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"verify", "prove"} {
		t.Run(command, func(t *testing.T) {
			args := []string{command, "--tenant", model.SystemTenantID.String(), "--checkpoint", "-",
				"--pubkey", base64.StdEncoding.EncodeToString(pub)}
			if command == "prove" {
				args = append(args, "--seq", "1")
			}
			_, err := runAuditTreeInput(t, strings.NewReader(strings.Repeat("x", maxTreeFileBytes+1)), args...)
			if err == nil || !strings.Contains(err.Error(), "larger than") {
				t.Fatalf("oversized checkpoint stdin not refused at the input limit: %v", err)
			}
		})
	}
}

func writeTemp(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestAuditTreeSavedCheckpointVerifiesOffline is the operator journey: publish a
// checkpoint, keep only that file and the public key, let the ledger grow, then
// verify offline against the ledger and against one event's proof.
func TestAuditTreeSavedCheckpointVerifiesOffline(t *testing.T) {
	dir := t.TempDir()
	eng, err := boot(context.Background(), bootConfig{DataDir: dir, Engine: "sqlite", Logger: slog.Default(), DemoSeed: true})
	if err != nil {
		t.Fatal(err)
	}
	tenant := eng.demoTenant.String()
	pub := base64.StdEncoding.EncodeToString(eng.signer.PublicKey())
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}

	checkpoint, err := runAuditTree(t, "checkpoint", "--tenant", tenant, "--data-dir", dir)
	if err != nil {
		t.Fatalf("tree checkpoint: %v\n%s", err, checkpoint)
	}
	origin := "olivares.ai/audit/" + tenant
	if !strings.HasPrefix(checkpoint, origin+"\n") || !strings.Contains(checkpoint, "\n\n— "+origin+" ") {
		t.Fatalf("checkpoint is not a C2SP signed note: %q", checkpoint)
	}
	cpFile := writeTemp(t, "saved.checkpoint", checkpoint)
	size, err := strconv.Atoi(strings.Split(checkpoint, "\n")[1])
	if err != nil || size < 1 {
		t.Fatalf("checkpoint size line: %v", err)
	}

	// The ledger grows after the checkpoint was saved; the checkpoint still holds.
	eng, err = boot(context.Background(), bootConfig{DataDir: dir, Engine: "sqlite", Logger: slog.Default(), DemoSeed: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.store.Mutate(context.Background(), eng.demoTenant, func(sc store.Scope) error {
		for i := 0; i < 3; i++ {
			if _, err := sc.Audit().Append(context.Background(), model.AuditDraft{
				Actor: "user:1", ActorKind: model.ActorUser, Action: "agent.update", Meta: map[string]any{"i": i},
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	dbBefore, err := os.ReadFile(filepath.Join(dir, "olivares.db"))
	if err != nil {
		t.Fatal(err)
	}

	out, err := runAuditTree(t, "verify", "--tenant", tenant, "--data-dir", dir, "--checkpoint", cpFile, "--pubkey", pub)
	if err != nil || !strings.Contains(out, "signature ok") {
		t.Fatalf("verify against the grown ledger: %v\n%s", err, out)
	}
	proof, err := runAuditTree(t, "prove", "--tenant", tenant, "--data-dir", dir, "--checkpoint", cpFile, "--pubkey", pub, "--seq", "1")
	if err != nil {
		t.Fatalf("prove: %v\n%s", err, proof)
	}
	proofFile := writeTemp(t, "seq1.proof", proof)
	// --proof needs no data directory at all.
	out, err = runAuditTree(t, "verify", "--tenant", tenant, "--checkpoint", cpFile, "--pubkey", pub, "--proof", proofFile,
		"--data-dir", filepath.Join(t.TempDir(), "absent"))
	if err != nil || !strings.Contains(out, "claimed as seq 1, is in the tree") {
		t.Fatalf("offline inclusion proof: %v\n%s", err, out)
	}
	dbAfter, err := os.ReadFile(filepath.Join(dir, "olivares.db"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(dbBefore, dbAfter) {
		t.Fatal("verifying and proving changed the store")
	}

	// What it must refuse: another key, and a checkpoint whose size was edited.
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := runAuditTree(t, "verify", "--tenant", tenant, "--data-dir", dir, "--checkpoint", cpFile,
		"--pubkey", base64.StdEncoding.EncodeToString(otherPub)); err == nil {
		t.Fatal("a checkpoint verified under a key that did not sign it")
	}
	lines := strings.Split(checkpoint, "\n")
	lines[1] = strconv.Itoa(size + 1)
	edited := writeTemp(t, "edited.checkpoint", strings.Join(lines, "\n"))
	if _, err := runAuditTree(t, "verify", "--tenant", tenant, "--data-dir", dir, "--checkpoint", edited, "--pubkey", pub); err == nil {
		t.Fatal("an edited checkpoint verified")
	}
}
