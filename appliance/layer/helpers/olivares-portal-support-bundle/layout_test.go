// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

func TestSupportBundle_KeepsActsAndOutUnderItsStateHome(t *testing.T) {
	if spoolDir != "/var/lib/olivares-support-bundle" {
		t.Fatalf("the helper's state home is %s, not /var/lib/olivares-support-bundle", spoolDir)
	}
	facts := func() ([]byte, error) { return []byte(`{"schema_version": "olivares-support-bundle/v1"}`), nil }
	console := helperschema.Peer{UID: 0, Account: "root", Unit: helperschema.RepairConsole.Unit, TTY: "/dev/tty1", Attested: true}
	produce := &helperschema.SupportBundleRequest{Op: helperschema.SupportBundleProduce}
	random := bytes.NewReader(bytes.Repeat([]byte{0x42}, 1024))

	t.Run("acts and out are the home's own directories, 0700, and a bundle goes to out", func(t *testing.T) {
		home := t.TempDir()
		if err := os.Chmod(home, 0o700); err != nil {
			t.Fatal(err)
		}
		h := helper(spool{dir: home, random: random, facts: facts})
		produced := h.Perform(context.Background(), console, produce)
		if produced.Result != helperschema.ResultPerformed {
			t.Fatalf("produce answered %+v", produced)
		}
		for _, sub := range []string{"acts", "out"} {
			info, err := os.Lstat(filepath.Join(home, sub))
			if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
				t.Fatalf("%s/%s is %v (%v), want a directory of mode 0700", home, sub, info, err)
			}
		}
		entries, err := os.ReadDir(home)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		if !slices.Equal(names, []string{"acts", "out", "spool.key"}) {
			t.Fatalf("the state home holds %q, want acts, out and the spool key only", names)
		}
		if _, err := os.Lstat(filepath.Join(home, "out", produced.Nonce+".bundle.json")); err != nil {
			t.Fatalf("the bundle is not in out/ under its nonce: %v", err)
		}
		fetched := h.Perform(context.Background(), console, &helperschema.SupportBundleRequest{Op: helperschema.SupportBundleFetch, Nonce: produced.Nonce})
		if fetched.Result != helperschema.ResultAnswered {
			t.Fatalf("fetch answered %+v", fetched)
		}
	})

	t.Run("an out directory that others can read is refused, and nothing is written", func(t *testing.T) {
		home := t.TempDir()
		if err := os.Chmod(home, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(home, "out"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(home, "out"), 0o755); err != nil {
			t.Fatal(err)
		}
		h := helper(spool{dir: home, random: random, facts: facts})
		if answer := h.Perform(context.Background(), console, produce); answer.Result != helperschema.ResultFailed {
			t.Fatalf("produce into an out/ of mode 0755 answered %+v", answer)
		}
		if entries, _ := os.ReadDir(filepath.Join(home, "out")); len(entries) != 0 {
			t.Fatalf("out/ of mode 0755 received %d files", len(entries))
		}
	})
}
