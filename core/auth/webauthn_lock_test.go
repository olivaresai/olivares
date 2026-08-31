// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md.

package auth

import (
	"os"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestWebAuthnLockKeyIsPerUser(t *testing.T) {
	a, b := model.NewID(), model.NewID()
	ka, kb := webAuthnUserLockKey(a), webAuthnUserLockKey(b)
	if ka == kb {
		t.Fatal("two users must not contend on the same key: a global key serializes every registration in the estate behind one another")
	}
	if !strings.Contains(ka, a.String()) {
		t.Fatalf("the key must carry the user it serializes, got %q", ka)
	}
	if ka == "" || ka == a.String() {
		t.Fatalf("the key must be namespaced, got %q", ka)
	}
}

// TestRegistrationLocksBeforeItChecks pins the ORDER, which is the whole defect:
// taking the lock AFTER reading "does this user have credentials" serializes the
// writes but not the decision, so both racers would still have read zero.
//
// The haystack is cut to FinishWebAuthnRegistration on purpose. Asserting the two
// literals over the whole file would be satisfied by any other function that
// happens to contain them — and this file has a second credential path
// (persistWebAuthnCredential) that legitimately reads WebAuthnCredentials.
func TestRegistrationLocksBeforeItChecks(t *testing.T) {
	src, err := os.ReadFile("webauthn.go")
	if err != nil {
		t.Fatalf("read webauthn.go: %v", err)
	}
	const start = "func (a *Authenticator) FinishWebAuthnRegistration("
	i := strings.Index(string(src), start)
	if i < 0 {
		t.Fatal("FinishWebAuthnRegistration is gone: this guard names a function that no longer exists, so it is measuring nothing")
	}
	rest := string(src)[i+len(start):]
	if j := strings.Index(rest, "\nfunc "); j >= 0 {
		rest = rest[:j]
	}

	lock := strings.Index(rest, "lockAuthTransaction(ctx, as, webAuthnUserLockKey(")
	check := strings.Index(rest, "WebAuthnCredentials().List(")
	if lock < 0 {
		t.Fatal("FinishWebAuthnRegistration must take the per-user transaction lock: without it two concurrent first registrations both read zero credentials on Postgres and the AAL3 step-up is bypassed")
	}
	if check < 0 {
		t.Fatal("the guard no longer reads the user's existing credentials, so this test is pinning an order that no longer exists")
	}
	if lock > check {
		t.Fatal("the lock must be taken BEFORE the existing-credential check, or both racers read zero before either is serialized")
	}
}
