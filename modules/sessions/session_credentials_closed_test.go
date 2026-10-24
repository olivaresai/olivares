// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"
)

// The core takes nothing a broker hands back on trust: a launch that asked for a credential
// starts with working helpers or not at all, and every helper a broker started is released
// when the launch is refused.

// A broker that answers success and names no variable gave the child nothing to find its
// helper with: the session would start without the access it asked for.
func TestCredentials_ASuccessThatNamesNoHelperStartsNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, broker := credentialHarness(t)
	broker.noEnv = true
	if _, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true)); statusOf(err) != http.StatusServiceUnavailable {
		t.Fatalf("a broker that located no helper: create = %v, want 503", err)
	}
	broker.awaitRelease(t)
	if got := launchCount(fr); got != 0 {
		t.Fatalf("a launch with no helper location reached the runner %d times", got)
	}
}

// A release handed back with an error is still run: the contract says the broker undoes
// its own work on an error, and a broker that does not must not leak a helper for the life
// of the engine.
func TestCredentials_AReleaseReturnedWithAnErrorIsStillRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, broker := credentialHarness(t)
	broker.err, broker.releaseWithErr = errors.New("the second helper did not start"), true
	if _, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true)); statusOf(err) != http.StatusServiceUnavailable {
		t.Fatalf("a broker that failed halfway: create = %v, want 503", err)
	}
	broker.awaitRelease(t)
	if got := launchCount(fr); got != 0 {
		t.Fatalf("a launch whose broker failed reached the runner %d times", got)
	}
}

// The files a broker lets a confined child read are absolute and clean: a relative path,
// the root, or a path that climbs out of where it starts would widen the child's read
// access beyond the broker's own files.
func TestCredentials_ABrokerMayGrantOnlyAbsoluteCleanReadPaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, path := range map[string]string{
		"a relative path":   "helpers/ssh_config",
		"the root":          "/",
		"a parent element":  "/run/helpers/../vault/keys",
		"a doubled slash":   "/run//helpers/ssh_config",
		"a trailing slash":  "/run/helpers/",
		"an empty path":     "",
		"a dot element":     "/run/./helpers",
		"a bare parent dir": "..",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m, tenant, fr, broker := credentialHarness(t)
			broker.read = []string{path}
			if _, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true)); statusOf(err) != http.StatusServiceUnavailable {
				t.Fatalf("a broker that granted read of %q: create = %v, want 503", path, err)
			}
			broker.awaitRelease(t)
			if got := launchCount(fr); got != 0 {
				t.Fatalf("a launch granting read of %q reached the runner", path)
			}
		})
	}
}

// A stored value that cannot be read is a conflict the caller can act on (start a new
// session), not an outage: a queued resume treats an unclassified error as retryable and
// would otherwise try again until the engine stops.
func TestCredentials_AnUnreadableStoredValueIsAConflictNotARetry(t *testing.T) {
	t.Parallel()
	for name, stored := range map[string]string{"not JSON": "{not json", "an empty list": "[]", "null": "null"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := decodeCredentialNames(stored)
			if statusOf(err) != http.StatusConflict {
				t.Fatalf("decodeCredentialNames(%q) = %v, want a 409", stored, err)
			}
			if retryableApprovalWait(err) {
				t.Fatalf("a queued resume would retry a stored value that is %s", name)
			}
		})
	}
}

// The approval a launch asks for names the credentials it will be given, on create and on
// resume: an approval opened for fewer cannot be spent on more.
func TestCredentials_TheLaunchGateSeesTheNamesOnCreateAndResume(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	gate := &spyGate{inner: LaunchDecision{Allowed: true}}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()), WithLaunchGate(gate))
	stopModuleAtCleanup(t, m)
	broker := newFakeCredentialBroker(t)
	broker.st = st
	m.CredentialBroker = broker

	names := []string{"prod-db", "build-host"}
	dto, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true, names...))
	if err != nil {
		t.Fatal(err)
	}
	if got := gate.last(t).Credentials; !slices.Equal(got, names) {
		t.Fatalf("the create approval named %v, want %v", got, names)
	}
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	broker.awaitRelease(t)
	if _, err := m.resumeRunAs(ctx, tenant, dto.RunRef, actorU, actorKindU, "", true); err != nil {
		t.Fatal(err)
	}
	if got := gate.last(t).Credentials; !slices.Equal(got, names) {
		t.Fatalf("the resume approval named %v, want %v", got, names)
	}
}
