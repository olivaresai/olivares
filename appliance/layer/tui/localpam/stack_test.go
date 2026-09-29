// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux && cgo && olivares_pam && pamstack

package localpam

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

// The container job's fixture accounts: an administrator the job created with no usable credential,
// and one it locked. The case sets the administrator's credential itself, from fresh randomness, on
// chpasswd's standard input, so no credential is in an argument, a file name, the environment or a log.
const (
	fixtureAdmin  = "olivares-fixture"
	fixtureLocked = "locked-fixture"
)

// answer places line on the standard input the service's own terminal conversation reads.
func answer(t *testing.T, line string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	if err := syscall.Dup3(int(r.Fd()), 0, 0); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
}

func TestLocalPAM_TheLoginServiceChecksTheCredentialAndTheAccount(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Fatal("the container job runs this case as root, as the repair console runs")
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	credential := hex.EncodeToString(raw)
	set := exec.Command("chpasswd")
	set.Stdin = strings.NewReader(fixtureAdmin + ":" + credential + "\n")
	if err := set.Run(); err != nil {
		t.Fatalf("control: the fixture's credential could not be set: %v", err)
	}
	check := func(login, typed string) (auth, account error) {
		t.Helper()
		transaction, err := Stack{}.Start("login", login)
		if err != nil {
			t.Fatalf("the login service could not be opened for %s: %v", login, err)
		}
		defer transaction.Close()
		answer(t, typed)
		if auth = transaction.Authenticate(); auth != nil {
			return auth, nil
		}
		return nil, transaction.Account()
	}
	if auth, account := check(fixtureAdmin, credential); auth != nil || account != nil {
		t.Fatalf("the right credential: auth %v, account %v", auth, account)
	}
	if auth, _ := check(fixtureAdmin, credential+"x"); auth == nil {
		t.Fatal("a wrong credential was accepted")
	}
	if auth, _ := check(fixtureAdmin, ""); auth == nil {
		t.Fatal("an empty credential was accepted")
	}
	if auth, account := check(fixtureLocked, credential); auth == nil && account == nil {
		t.Fatal("a locked account was admitted")
	}
	if !Built {
		t.Fatal("the adapter under test is the refusing one")
	}
}
