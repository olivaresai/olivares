// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package netguard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNetrestore_RemovesOnlyARunProfileDerivedFromFilename(t *testing.T) {
	baseline := "/etc/NetworkManager/system-connections/confirmed.nmconnection"
	for _, name := range []string{"/tmp/input", "/run/NetworkManager/system-connections/../other", "/etc/NetworkManager/system-connections/other.nmconnection", "/run/NetworkManager/system-connections/"} {
		if _, err := RestoreFiles(baseline, name); err == nil {
			t.Fatalf("caller-like path accepted: %s", name)
		}
	}
	remove, err := RestoreFiles(baseline, "/run/NetworkManager/system-connections/shadow.nmconnection")
	if err != nil || remove != "/run/NetworkManager/system-connections/shadow.nmconnection" {
		t.Fatal(remove, err)
	}
	if remove, err := RestoreFiles(baseline, baseline); err != nil || remove != "" {
		t.Fatal("baseline was selected for removal", remove, err)
	}
}
func TestNetrestore_NeverLoadsTheDeletedRunFile(t *testing.T) {
	baseline := "/etc/NetworkManager/system-connections/confirmed.nmconnection"
	remove, err := RestoreFiles(baseline, "/run/NetworkManager/system-connections/shadow.nmconnection")
	if err != nil {
		t.Fatal(err)
	}
	if remove == baseline || !persistentFilename(baseline) {
		t.Fatal("wrong reload source")
	}
	if _, err := RestoreFiles(remove, remove); err == nil {
		t.Fatal("runtime shadow used as persistent baseline")
	}
}
func TestRootLedger_CompletionMustMatchTheExactDurableIntent(t *testing.T) {
	dir := t.TempDir()
	ledger := rootLedger{dir: dir, uid: uint32(os.Getuid()), gid: uint32(os.Getgid())}
	call := pendingCall()
	if err := ledger.intent(call); err != nil {
		t.Fatal(err)
	}
	wrong := call
	wrong.Target.StartTime++
	if err := ledger.complete(wrong); err == nil {
		t.Fatal("completion for another process accepted")
	}
	call.Settled = true
	call.Success = true
	call.Settlement = "correlated_reply"
	if err := ledger.complete(call); err != nil {
		t.Fatal(err)
	}
	calls, err := ledger.calls()
	if err != nil || len(calls) != 1 || !calls[0].Settled {
		t.Fatal(calls, err)
	}
}
func TestRootLedger_HelperExitDoesNotSettleReloadOrLoad(t *testing.T) {
	for _, method := range []Method{ReloadConnections, LoadConnections} {
		t.Run(string(method), func(t *testing.T) {
			dir := t.TempDir()
			ledger := rootLedger{dir: dir, uid: uint32(os.Getuid()), gid: uint32(os.Getgid())}
			call := pendingCall()
			call.Method = method
			if err := ledger.intent(call); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "finished.json"), []byte(`{"finished":true}`), 0640); err != nil {
				t.Fatal(err)
			}
			calls, err := ledger.calls()
			if err != nil || len(calls) != 1 || calls[0].Settled {
				t.Fatal("helper exit settled a call", calls, err)
			}
		})
	}
}
