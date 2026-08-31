// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/release"
)

// THE SERVED CHANNEL MUST BE THE REQUESTED CHANNEL, and the reproduction is not
// contrived — it is the enterprise gate exactly as it ships.
//
// commercial/license-worker/src/download/gate.ts reads three query parameters (token, os,
// arch) and NOTHING ELSE, so `?kind=manifest&channel=security` is answered with whatever the
// stable path holds. The fixture below models that faithfully: its /download route switches
// on `kind` and ignores `channel`, which is what the real gate does today.
//
// Before this change the CLI took that answer. The signature verifies — the manifest is
// genuinely ours — and every other guard passes, so `upgrade --channel security` proceeded
// against the STABLE line. An operator applying an out-of-band security release would have
// been told the upgrade succeeded, and the fix would simply not be in the binary. Nothing in
// the output said channel security had not been served; the value was printed, never compared.
//
// The external contrast on the channel plane raised this as H-01 and it is still open on the
// branch that builds the plane, which is why it is closed HERE, on the client, where the
// decision is actually made: a client that checks does not depend on every server being
// right.
func TestUpgradeRefusesAManifestSignedForAnotherChannel(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available to build stub binaries")
	}
	v1 := buildStub(t, "26.7.0")
	v2 := buildStub(t, "26.8.0")

	dataDir := t.TempDir()
	installDevLicense(t, dataDir)
	// The fixture signs and serves a STABLE manifest; the gate route ignores `channel`.
	f := newUpdFixture(t, "26.8.0", "26.6.0", v2)
	target := writeTarget(t, v1)

	out, err := runUpgradeCmd(t, "--enterprise", "--token", "tkn", "--endpoint", f.server.URL,
		"--pubkey", f.pubB64, "--data-dir", dataDir, "--target", target,
		"--channel", release.ChannelSecurity,
		"--os", "linux", "--arch", "amd64", "--yes")

	if err == nil {
		t.Fatalf("a stable manifest satisfied --channel security and the upgrade PROCEEDED; output:\n%s", out)
	}
	msg := err.Error() + "\n" + out
	if !strings.Contains(msg, release.ChannelSecurity) || !strings.Contains(msg, release.ChannelStable) {
		t.Fatalf("the refusal must name BOTH the asked-for and the served channel, got: %s", msg)
	}
	// It must be clear this is NOT a signature problem, or the operator will chase the wrong
	// thing — rotating keys for what is a routing mistake.
	if !strings.Contains(strings.ToLower(msg), "signature is valid") {
		t.Fatalf("the refusal must say the signature is valid, so nobody debugs a forgery that is not there: %s", msg)
	}
	// AND THE BINARY MUST NOT HAVE MOVED. A refusal that still swapped the file would be the
	// worst outcome of the three, and only this assertion can tell the difference.
	if got := runsVersion(t, target); !strings.Contains(got, "26.7.0") {
		t.Fatalf("the target was replaced despite the refusal: %q", got)
	}
}

// THE NOT-FIRING DIRECTION. Without it, "refuses the wrong channel" is satisfied just as well
// by a command that refuses every upgrade, which is an outage wearing a security fix's clothes.
func TestUpgradeStillAcceptsTheDefaultStableChannel(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available to build stub binaries")
	}
	v1 := buildStub(t, "26.7.0")
	v2 := buildStub(t, "26.8.0")

	dataDir := t.TempDir()
	installDevLicense(t, dataDir)
	f := newUpdFixture(t, "26.8.0", "26.6.0", v2)
	target := writeTarget(t, v1)

	// No --channel at all: the default is stable and the fixture serves stable.
	if _, err := runUpgradeCmd(t, "--enterprise", "--token", "tkn", "--endpoint", f.server.URL,
		"--pubkey", f.pubB64, "--data-dir", dataDir, "--target", target,
		"--os", "linux", "--arch", "amd64", "--yes"); err != nil {
		t.Fatalf("the default channel was refused by the new binding: %v", err)
	}
	if got := runsVersion(t, target); !strings.Contains(got, "26.8.0") {
		t.Fatalf("target not upgraded: %q", got)
	}
}
