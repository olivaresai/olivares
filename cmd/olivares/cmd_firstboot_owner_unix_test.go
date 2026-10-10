// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/secure"
)

// A REPLACEMENT TOKEN MUST BELONG TO THE ACCOUNT THE ENGINE RUNS AS (#514).
//
// On a deb/rpm install the data directory is olivares:olivares 0750 and the engine runs
// as olivares, so the natural way to run the printed remedy is `sudo olivares first-boot
// --new-token`. It used to write setup.token as root:root 0600: the engine could stat it
// (so setup stayed pending) but not read it, every POST /v1/setup answered 403, and the
// previous token was already retired. These tests run the command as an effective uid
// that is not the engine's, which is the condition a test process can set without being
// root.

// withFirstBootEUID makes runFirstBoot see euid as its effective uid for one test.
// Callers do not use t.Parallel: the seam is package state.
func withFirstBootEUID(t *testing.T, euid int) {
	t.Helper()
	saved := firstBootEUID
	firstBootEUID = func() int { return euid }
	t.Cleanup(func() { firstBootEUID = saved })
}

// withEngineAccount makes runFirstBoot see uid:gid as the engine's account for one
// test, whoever owns console.json. Same rule: no t.Parallel.
func withEngineAccount(t *testing.T, uid, gid int) {
	t.Helper()
	saved := engineAccount
	engineAccount = func(string) (int, int, bool, error) { return uid, gid, true, nil }
	t.Cleanup(func() { engineAccount = saved })
}

func ownerOf(t *testing.T, path string) (uid, gid int) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	st := info.Sys().(*syscall.Stat_t)
	return int(st.Uid), int(st.Gid)
}

// foreignGID is a group this process is not a member of: a non-root process cannot
// give a file to it, so a run that really changes the owner fails visibly.
func foreignGID(t *testing.T) int {
	t.Helper()
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatalf("getgroups: %v", err)
	}
	for gid := 0; gid < 1<<16; gid++ {
		if gid != os.Getgid() && !slices.Contains(groups, gid) {
			return gid
		}
	}
	t.Fatal("no group outside this process's groups")
	return -1
}

// A NON-ROOT ACCOUNT THAT IS NOT THE ENGINE'S IS REFUSED BEFORE THE OLD TOKEN IS RETIRED.
// It cannot give a file away, so anything it minted would be unreadable to the engine.
// The refusal names the command that works, and the current token is left as it is.
func TestFirstBootNewTokenAsAnotherAccountRefusesAndKeepsTheToken(t *testing.T) {
	dir := seedConsoleState(t, false)
	tok := secure.NewSetupToken(filepath.Join(dir, "setup.token"))
	first, created, err := tok.Ensure()
	if err != nil || !created {
		t.Fatalf("mint the first token: created=%v err=%v", created, err)
	}
	// Two non-root accounts, whatever account the test runs as: a root-owned record
	// means a root engine, which reads the token whoever owns it.
	const engineUID, other = 1000, 1001
	withEngineAccount(t, engineUID, engineUID)
	withFirstBootEUID(t, other)

	var out bytes.Buffer
	err = runFirstBoot(&out, dir, true)
	if err == nil {
		t.Fatalf("--new-token as uid %d for an engine running as uid %d reported success:\n%s", other, engineUID, out.String())
	}
	if code := exitcode.From(err); code != exitcode.Usage {
		t.Errorf("exit code = %d, want %d (the invocation runs as the wrong account)", code, exitcode.Usage)
	}
	if want := "sudo -u '#" + strconv.Itoa(engineUID) + "' olivares first-boot --data-dir " + dir + " --new-token"; !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal does not name the command that works (%q): %v", want, err)
	}
	if strings.Contains(out.String(), "olst_") {
		t.Errorf("a token was printed by a refused run:\n%s", out.String())
	}
	if !tok.Verify(first) {
		t.Error("the refused run retired the previous token")
	}
}

// ROOT WRITES THE REPLACEMENT AS THE ENGINE'S ACCOUNT, as the appliance's own first-boot
// delivery does (appliance/layer/base/adapters.go SetupTokenDelivery). The engine's
// account here is this process's uid with a group it is not in, so the ownership change
// is real: a non-root test process is refused by the kernel and must report that without
// printing a token; a root test process makes it, and the file must carry that group.
func TestFirstBootNewTokenAsRootWritesTheEngineAccount(t *testing.T) {
	dir := seedConsoleState(t, true)
	withFirstBootEUID(t, 0)
	engineUID, engineGID := os.Getuid(), foreignGID(t)
	withEngineAccount(t, engineUID, engineGID)
	tokenPath := filepath.Join(dir, "setup.token")

	var out bytes.Buffer
	err := runFirstBoot(&out, dir, true)
	if os.Geteuid() != 0 {
		if err == nil {
			t.Fatalf("the replacement was not given to %d:%d (a non-root process cannot), yet the run succeeded:\n%s", engineUID, engineGID, out.String())
		}
		if strings.Contains(out.String(), "olst_") {
			t.Errorf("a token the engine could not read was printed:\n%s", out.String())
		}
		if !strings.Contains(err.Error(), "restart the engine") {
			t.Errorf("the error does not say how to get a fresh token: %v", err)
		}
		if _, statErr := os.Lstat(tokenPath); !os.IsNotExist(statErr) {
			t.Errorf("a token the engine could not read was left in place (lstat: %v)", statErr)
		}
		return
	}
	if err != nil {
		t.Fatalf("runFirstBoot --new-token as root: %v", err)
	}
	if uid, gid := ownerOf(t, tokenPath); uid != engineUID || gid != engineGID {
		t.Errorf("setup.token is %d:%d, want the engine's %d:%d", uid, gid, engineUID, engineGID)
	}
}

// A ROOT ENGINE READS A TOKEN OF ANY OWNER. `sudo olivares serve` leaves console.json
// root-owned; a later --new-token by a non-root account that can write the data
// directory worked before #514 and must still work: the replacement is minted as that
// account, the run exits 0, and the printed token verifies. The engine's group is one
// this process is not in, so a run that gave the file to the engine is caught: the
// kernel refuses it without root, and with root the file would carry that group.
func TestFirstBootNewTokenForARootEngineMintsAsTheInvokingAccount(t *testing.T) {
	dir := seedConsoleState(t, true)
	withFirstBootEUID(t, 1001)
	withEngineAccount(t, 0, foreignGID(t))
	tokenPath := filepath.Join(dir, "setup.token")

	var out bytes.Buffer
	if err := runFirstBoot(&out, dir, true); err != nil {
		t.Fatalf("--new-token as uid 1001 for an engine running as root: exit %d: %v", exitcode.From(err), err)
	}
	report := out.String()
	i := strings.Index(report, "olst_")
	if i < 0 {
		t.Fatalf("no replacement token was printed:\n%s", report)
	}
	if !secure.NewSetupToken(tokenPath).Verify(strings.Fields(report[i:])[0]) {
		t.Error("the engine would not accept the replacement token")
	}
	if uid, gid := ownerOf(t, tokenPath); uid != os.Geteuid() || gid != os.Getegid() {
		t.Errorf("setup.token is %d:%d, want the invoking process's %d:%d", uid, gid, os.Geteuid(), os.Getegid())
	}
}

// ROOT OVER A REAL ENGINE RECORD: the replacement belongs to the account that wrote
// console.json, and the printed token verifies.
func TestFirstBootNewTokenAsRootUsesTheConsoleRecordOwner(t *testing.T) {
	dir := seedConsoleState(t, true)
	wantUID, wantGID := ownerOf(t, filepath.Join(dir, consoleStateFile))
	withFirstBootEUID(t, 0)

	var out bytes.Buffer
	if err := runFirstBoot(&out, dir, true); err != nil {
		t.Fatalf("runFirstBoot --new-token as root: %v", err)
	}
	tokenPath := filepath.Join(dir, "setup.token")
	if uid, gid := ownerOf(t, tokenPath); uid != wantUID || gid != wantGID {
		t.Errorf("setup.token is %d:%d, want %d:%d", uid, gid, wantUID, wantGID)
	}
	report := out.String()
	i := strings.Index(report, "olst_")
	if i < 0 {
		t.Fatalf("no replacement token was printed:\n%s", report)
	}
	if !secure.NewSetupToken(tokenPath).Verify(strings.Fields(report[i:])[0]) {
		t.Error("the engine would not accept the replacement token")
	}
}

// A SYMLINKED console.json DOES NOT CHOOSE THE ACCOUNT ROOT WRITES AS. The engine account
// controls the data directory; root refuses before anything is retired.
func TestFirstBootNewTokenAsRootRefusesASymlinkedConsoleRecord(t *testing.T) {
	dir := seedConsoleState(t, true)
	record := filepath.Join(dir, consoleStateFile)
	moved := filepath.Join(t.TempDir(), consoleStateFile)
	if err := os.Rename(record, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, record); err != nil {
		t.Fatal(err)
	}
	withFirstBootEUID(t, 0)
	before, err := os.ReadFile(filepath.Join(dir, "setup.token"))
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runFirstBoot(&out, dir, true); err == nil {
		t.Fatalf("root minted with the account taken through a symlink:\n%s", out.String())
	}
	after, err := os.ReadFile(filepath.Join(dir, "setup.token"))
	if err != nil || !bytes.Equal(before, after) {
		t.Errorf("the current token changed on a refused run (err %v)", err)
	}
}

// ONLY ROOT NEEDS TO TRUST THE RECORD, because only root gives the file away. A run
// without root over a symlinked console.json mints as itself and exits 0, as it did
// before #514; refusing it would break a run that works and protect nothing.
func TestFirstBootNewTokenWithoutRootMintsAsItselfOverASymlinkedConsoleRecord(t *testing.T) {
	dir := seedConsoleState(t, true)
	record := filepath.Join(dir, consoleStateFile)
	moved := filepath.Join(t.TempDir(), consoleStateFile)
	if err := os.Rename(record, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, record); err != nil {
		t.Fatal(err)
	}
	withFirstBootEUID(t, 1001)

	var out bytes.Buffer
	if err := runFirstBoot(&out, dir, true); err != nil {
		t.Fatalf("--new-token without root over a symlinked record: exit %d: %v", exitcode.From(err), err)
	}
	report := out.String()
	i := strings.Index(report, "olst_")
	if i < 0 {
		t.Fatalf("no replacement token was printed:\n%s", report)
	}
	if !secure.NewSetupToken(filepath.Join(dir, "setup.token")).Verify(strings.Fields(report[i:])[0]) {
		t.Error("the engine would not accept the replacement token")
	}
}

// A SETUP STATE THE COMMAND CANNOT LOOK AT IS UNKNOWN, NEVER "COMPLETE" (#513).
//
// On a deb/rpm install the data directory is olivares:olivares 0750, so the help's own
// "local or systemd install" example, run by a normal user, cannot even stat
// setup.token. That used to read as "no token", and the report told an operator on a host
// that was never set up that an administrator exists and sent them to superadmin recovery
// (exit 0; --new-token added a false "refused ... one already exists here"). Both runs now
// stop with the could-not-look code before anything is printed or changed, and name the
// command that can look.
func TestFirstBootSetupStateItCannotReadIsUnknownNotComplete(t *testing.T) {
	t.Parallel()
	blockers := map[string]func(t *testing.T, dir string){
		// The real condition: a directory the caller has no search permission on.
		"permission denied": func(t *testing.T, dir string) {
			if os.Geteuid() == 0 {
				t.Skip("root searches any directory, so the data directory cannot be made unreadable to it")
			}
			if err := os.Chmod(dir, 0); err != nil {
				t.Fatalf("chmod: %v", err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		},
		// Any other stat failure is the same case; this one needs no privilege to build.
		"symlink loop": func(t *testing.T, dir string) {
			token := filepath.Join(dir, "setup.token")
			if err := os.Remove(token); err != nil {
				t.Fatalf("remove the token: %v", err)
			}
			if err := os.Symlink("setup.token", token); err != nil {
				t.Fatalf("symlink: %v", err)
			}
		},
	}
	for name, block := range blockers {
		for _, newToken := range []bool{false, true} {
			t.Run(name+"/new-token="+strconv.FormatBool(newToken), func(t *testing.T) {
				t.Parallel()
				dir := seedConsoleState(t, true)
				block(t, dir)

				var out bytes.Buffer
				err := runFirstBoot(&out, dir, newToken)
				if err == nil {
					t.Fatalf("a setup state that could not be read reported success:\n%s", out.String())
				}
				if code := exitcode.From(err); code != exitcode.Indeterminate {
					t.Errorf("exit code = %d, want %d (could not look)", code, exitcode.Indeterminate)
				}
				for _, claim := range []string{"COMPLETE", "PENDING", "refused"} {
					if strings.Contains(out.String(), claim) {
						t.Errorf("the report states %q about a state it could not read:\n%s", claim, out.String())
					}
				}
				wantRemedy := "sudo olivares first-boot --data-dir " + dir
				if newToken {
					wantRemedy += " --new-token"
				}
				if !strings.Contains(err.Error(), wantRemedy) {
					t.Errorf("the failure does not name the command that can look, %q: %v", wantRemedy, err)
				}
			})
		}
	}
}
