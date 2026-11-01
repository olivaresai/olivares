// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

// The application server key is the engine's, not a file of its own: the same data directory
// signs as the same server after a restart, and no second key file appears to back up.
func TestPushSenderKeyFollowsTheEventingKey(t *testing.T) {
	dir := t.TempDir()
	first, err := newPushSender(dir, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	again, err := newPushSender(dir, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if first.PublicKey() == "" || first.PublicKey() != again.PublicKey() {
		t.Fatalf("a restart changed the application server key: %q then %q", first.PublicKey(), again.PublicKey())
	}
	// The directory's own VCS exclusion marker is the engine's, not a key: what must be
	// absent is a key file of the push server's own.
	var keys []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != ".gitignore" {
			keys = append(keys, e.Name())
		}
	}
	if len(keys) != 1 || keys[0] != eventingSecretKeyFile {
		t.Fatalf("the data directory holds %v, want only the eventing key file", keys)
	}

	// Another engine, another key: a push key is not shared between deployments.
	other, err := newPushSender(t.TempDir(), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if other.PublicKey() == first.PublicKey() {
		t.Fatal("two engines share one application server key")
	}
}

// The HA path: every node holds the same env key, so every node is the same application
// server and a browser subscribed through one is reachable from another.
func TestPushSenderKeyFollowsTheSharedEnvKey(t *testing.T) {
	shared := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	env := func(name string) string {
		if name == eventingSecretKeyEnv {
			return shared
		}
		return ""
	}
	a, err := newPushSender(t.TempDir(), env)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newPushSender(t.TempDir(), env)
	if err != nil {
		t.Fatal(err)
	}
	if a.PublicKey() != b.PublicKey() {
		t.Fatal("two nodes with one shared key are two application servers")
	}
	if _, err := newPushSender(t.TempDir(), func(string) string { return "not base64 of 32 bytes" }); err == nil {
		t.Fatal("a malformed shared key was accepted")
	}
}

// An unreadable key is an error the caller logs and survives, never a sender signing with a
// key nobody can reproduce.
func TestPushSenderRefusesAnOverPermissiveKeyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, eventingSecretKeyFile), []byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := newPushSender(dir, noEnv); err == nil {
		t.Fatal("a world-readable key file was used")
	}
}
