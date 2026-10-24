// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// `olivares upgrade --edition business` is the owner's one command from a Community build with a
// Business license: the licensed route (--enterprise) under the name the owner reads in the
// console, the portal and the docs. --enterprise stays: it is a published flag.

// editionFlagArgs is editionUpgradeArgs with --edition business where it had --enterprise.
func editionFlagArgs(f *updFixture, dataDir, target string, extra ...string) []string {
	args := editionUpgradeArgs(f, dataDir, target, extra...)
	for i, a := range args {
		if a == "--enterprise" {
			args[i] = "--edition=business"
		}
	}
	return args
}

func TestUpgradeEditionBusinessMovesACommunityBuildToBusiness(t *testing.T) {
	for _, edition := range []string{"business", "enterprise"} {
		t.Run(edition, func(t *testing.T) {
			dataDir := t.TempDir()
			installDevLicense(t, dataDir)
			f := newUpdFixture(t, "0.3", "", editionScript("0.3", "enterprise"))
			target := writeTarget(t, editionScript("0.3", "community"))
			args := editionFlagArgs(f, dataDir, target, "--yes")
			for i, a := range args {
				if a == "--edition=business" {
					args[i] = "--edition=" + edition
				}
			}

			out, err := runUpgradeCmd(t, args...)
			if err != nil {
				t.Fatalf("upgrade --edition %s: %v", edition, err)
			}
			if !strings.Contains(out, "edition:   community -> enterprise") {
				t.Errorf("the plan does not name the move:\n%s", out)
			}
			if got := runsVersion(t, target); !strings.Contains(got, "enterprise witness") {
				t.Fatalf("the target is still not the Business build: %q", got)
			}
		})
	}
}

func TestUpgradeEditionIgnoresCaseAndSpaces(t *testing.T) {
	cmd := newUpgradeCmd()
	if err := cmd.ParseFlags([]string{"--edition", " Business "}); err != nil {
		t.Fatal(err)
	}
	o := &upgradeOptions{edition: " Business "}
	if err := applyEditionFlag(cmd, o); err != nil || !o.enterprise {
		t.Fatalf("applyEditionFlag(Business) = %v, enterprise=%v", err, o.enterprise)
	}
}

func TestUpgradeEditionRefusesWhatIsNotALicensedBuild(t *testing.T) {
	for _, edition := range []string{"community", "pro", ""} {
		_, err := runUpgradeCmd(t, "--edition="+edition, "--check", "--data-dir", t.TempDir(), "--pubkey", ephemeralPubkey(t))
		if err == nil || exitcode.From(err) != exitcode.Usage {
			t.Fatalf("--edition=%q: err = %v, want a usage refusal", edition, err)
		}
		if !strings.Contains(err.Error(), "business") {
			t.Errorf("--edition=%q: %q does not name the edition it accepts", edition, err)
		}
	}
}

// The one command works with no token typed once this installation is connected: the data
// directory's own identity fetches the download token, as the console's update does.
func TestUpgradeLicensedRouteUsesTheConnectedIdentityWhenNoTokenIsGiven(t *testing.T) {
	hermeticUpgradeEnv(t)
	connected := connectedDir(t)
	for _, tc := range []struct {
		name        string
		dataDir     string
		token       string
		env         string
		bundle      string
		enterprise  bool
		wantConnect bool
	}{
		{"connected, no token", connected, "", "", "", true, true},
		{"connected, --token given", connected, "tkn", "", "", true, false},
		{"connected, token in the environment", connected, "", "tkn", "", true, false},
		{"connected, offline bundle", connected, "", "", "f.tar.gz", true, false},
		{"connected, not the licensed route", connected, "", "", "", false, false},
		{"not connected", t.TempDir(), "", "", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OLIVARES_UPGRADE_TOKEN", tc.env)
			o := upgradeOptions{enterprise: tc.enterprise, dataDir: tc.dataDir, token: tc.token, bundle: tc.bundle}
			preferConnectedToken(&o)
			if o.connect != tc.wantConnect {
				t.Fatalf("connect = %v, want %v", o.connect, tc.wantConnect)
			}
		})
	}
}

// Through the command: a connected installation with no token takes the connected route (its own
// failure here is the unreachable service, not the missing token), and one that is not connected
// is told where the token is.
func TestUpgradeEditionBusinessOnAConnectedInstallationNeedsNoToken(t *testing.T) {
	hermeticUpgradeEnv(t)
	dataDir := connectedDir(t)
	installDevLicense(t, dataDir)
	_, err := runUpgradeCmd(t, "--edition=business", "--check", "--data-dir", dataDir, "--pubkey", ephemeralPubkey(t),
		"--endpoint", "http://127.0.0.1:1", "--timeout", "3s")
	if err == nil {
		t.Fatal("an unreachable licensing service was accepted")
	}
	if strings.Contains(err.Error(), "download token") {
		t.Fatalf("a connected installation was asked for a token: %v", err)
	}
}

// A package-managed binary cannot be switched in place by the package route; the licensed route's
// refusal says what to add, not "use your package manager", which has no Business build to give.
func TestUpgradeEditionBusinessOnAPackageNamesTheTargetForm(t *testing.T) {
	hermeticUpgradeEnv(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	previous := installedPackageBinary
	t.Cleanup(func() { installedPackageBinary = previous })
	installedPackageBinary = exe
	previousIn := inContainer
	t.Cleanup(func() { inContainer = previousIn })
	inContainer = func() bool { return false }

	dataDir := t.TempDir()
	installDevLicense(t, dataDir)
	_, err = runUpgradeCmd(t, "--edition=business", "--data-dir", dataDir, "--pubkey", ephemeralPubkey(t), "--yes")
	if err == nil || exitcode.From(err) != exitcode.Usage {
		t.Fatalf("err = %v, want a usage refusal before any download", err)
	}
	for _, want := range []string{"--target", "--data-dir", "Business"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not name %s", err, want)
		}
	}
	// The public route keeps the sentence it had.
	_, err = runUpgradeCmd(t, "--data-dir", dataDir, "--pubkey", ephemeralPubkey(t), "--yes")
	if err == nil || !strings.Contains(err.Error(), "host's package manager") {
		t.Fatalf("Community upgrade on a package install: %v, want the package-manager route", err)
	}
}

// Without a token and without a connected identity the refusal is the exact next step, not
// "needs a token" alone: where the token is, and the one-time connection that replaces it.
func TestUpgradeLicensedRouteRefusalNamesTheNextStep(t *testing.T) {
	hermeticUpgradeEnv(t)
	dataDir := t.TempDir()
	installDevLicense(t, dataDir)
	_, err := runUpgradeCmd(t, "--edition=business", "--check", "--data-dir", dataDir, "--pubkey", ephemeralPubkey(t), "--timeout", "3s")
	if err == nil {
		t.Fatal("no token and no connected identity was accepted")
	}
	for _, want := range []string{"OLIVARES_UPGRADE_TOKEN", "olivares license connect start", "licenses.olivares.ai"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not name %s", err, want)
		}
	}
}
