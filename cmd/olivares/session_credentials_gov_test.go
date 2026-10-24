// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/modules/sessions"
)

// The managed credentials a launch names are part of what its approval approves: an
// approval opened for one set cannot be spent on a launch given more, and the receipt
// names them. A launch without credentials keeps the hash it always had.
func TestSessionLaunchPlanBindsCredentialNames(t *testing.T) {
	plain := sessions.LaunchIntent{Action: sessions.LaunchActionCreate, Actor: "user:a", PermissionMode: "default"}
	empty := plain
	empty.Credentials = []string{}
	if sessionLaunchPlanHash(plain) != sessionLaunchPlanHash(empty) {
		t.Fatal("a launch without credentials must keep the hash it always had")
	}
	one := plain
	one.Credentials = []string{"prod-db"}
	two := plain
	two.Credentials = []string{"prod-db", "build-host"}
	other := plain
	other.Credentials = []string{"prod-api"}
	hashes := map[string]bool{}
	for _, intent := range []sessions.LaunchIntent{plain, one, two, other} {
		hashes[sessionLaunchPlanHash(intent)] = true
	}
	if len(hashes) != 4 {
		t.Fatal("adding, widening or re-pointing a managed credential must invalidate the approval")
	}
	if got := describeLaunch(two); !strings.Contains(got, "uses managed credentials prod-db, build-host") {
		t.Fatalf("approval receipt = %q, want the credential names", got)
	}
}

// A session that holds a managed credential is known to hold a secret: the hook and the
// approval redact a tool input only when redactedSecretNames is not empty.
func TestRedactedSecretNamesNamesManagedCredentials(t *testing.T) {
	intent := sessions.LaunchIntent{Credentials: []string{"prod-db", "build-host"}}
	got := redactedSecretNames(intent)
	if want := []string{"credential<-prod-db", "credential<-build-host"}; !slices.Equal(got, want) {
		t.Fatalf("redactedSecretNames = %v, want %v", got, want)
	}
	if len(redactedSecretNames(sessions.LaunchIntent{})) != 0 {
		t.Fatal("a launch without secrets must name none")
	}
}
