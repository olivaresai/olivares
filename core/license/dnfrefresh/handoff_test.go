// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package dnfrefresh

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
)

func handoffDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "olivares-dnf-handoff")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func issuedDocument(t *testing.T) Document {
	t.Helper()
	a, err := CheckAnswer(answerFixture(t, nil, nil), testBinding, testNow)
	if err != nil {
		t.Fatal(err)
	}
	inv := "4f1c0a2b9e8d7c6b5a4f3e2d1c0b9a88"
	return Issued(validCycle, &inv, a, testNow)
}

// TestHandoffDirectoryNotMode0700WritesNothing: the directory must be mode 0700, owned by the
// running uid. Any other mode writes nothing.
func TestHandoffDirectoryNotMode0700WritesNothing(t *testing.T) {
	if HandoffDir != "/run/olivares-dnf-handoff" {
		t.Fatalf("HandoffDir = %q", HandoffDir)
	}
	dir := handoffDir(t)
	doc := issuedDocument(t)
	if err := Publish(dir, doc); err != nil {
		t.Fatalf("mode 0700: %v", err)
	}
	if got := listDir(t, dir); strings.Join(got, ",") != validCycle+".json" {
		t.Fatalf("directory holds %v", got)
	}
	for _, mode := range []os.FileMode{0o755, 0o750, 0o770, 0o777, 0o700 | os.ModeSetgid} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := handoffDir(t)
			if err := os.Chmod(dir, mode); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			if err := Publish(dir, issuedDocument(t)); !errors.Is(err, ErrHandoffCustody) {
				t.Fatalf("Publish = %v, want ErrHandoffCustody", err)
			}
			_ = os.Chmod(dir, 0o700)
			if got := listDir(t, dir); len(got) != 0 {
				t.Fatalf("mode %s wrote %v", mode, got)
			}
		})
	}
}

// TestHandoffSchemaIsTheDnfDocument: the file is olivares.ai/dnf-credential-handoff/v1, with the
// apt handoff's fields, and credential holds the DNF credential only when the outcome is issued.
func TestHandoffSchemaIsTheDnfDocument(t *testing.T) {
	if Schema != "olivares.ai/dnf-credential-handoff/v1" {
		t.Fatalf("Schema = %q", Schema)
	}
	doc := issuedDocument(t)
	data, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > MaxHandoffBytes {
		t.Fatalf("handoff is %d bytes, bound %d", len(data), MaxHandoffBytes)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if doc.Credential == nil || m["schema"] != Schema || m["outcome"] != "issued" || m["credential"] != *doc.Credential {
		t.Fatalf("issued document schema=%v outcome=%v", m["schema"], m["outcome"])
	}
	refused, err := NotIssued(validCycle, nil, OutcomeRefused, "authority_denied", testNow).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var r map[string]any
	if err := json.Unmarshal(refused, &r); err != nil {
		t.Fatal(err)
	}
	if r["schema"] != Schema || r["outcome"] != "refused" || r["code"] != "authority_denied" || r["credential"] != nil {
		t.Fatalf("refused document: %#v", r)
	}
}

func TestPublishWritesOnlyTheCycleFile(t *testing.T) {
	dir := handoffDir(t)
	doc := issuedDocument(t)
	if err := Publish(dir, doc); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, validCycle+".json")
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	st := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || st.Nlink != 1 || int(st.Uid) != os.Getuid() {
		t.Fatalf("handoff mode %04o links %d uid %d", info.Mode().Perm(), st.Nlink, st.Uid)
	}
	want, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("handoff bytes differ")
	}
}

func TestPublishRefusesAPlantedTemporaryFile(t *testing.T) {
	dir := handoffDir(t)
	tmp := filepath.Join(dir, "."+validCycle+".tmp")
	if err := os.WriteFile(tmp, []byte("planted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Publish(dir, issuedDocument(t)); !errors.Is(err, ErrHandoffCustody) {
		t.Fatalf("Publish = %v, want ErrHandoffCustody", err)
	}
	if b, _ := os.ReadFile(tmp); string(b) != "planted" {
		t.Fatalf("planted file changed: %q", b)
	}
	if _, err := os.Lstat(filepath.Join(dir, validCycle+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a handoff was published past the planted temporary file")
	}
}
