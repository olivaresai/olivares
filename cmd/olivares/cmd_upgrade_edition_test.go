// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Business X.Y is cut from the same version as Community X.Y, so the how-to's in-place path
// (`upgrade --enterprise` on a Community build) has nothing newer to find. The installed
// edition is part of "up to date": these tests run the real command against the gate double
// with a Community build at 0.3 and a signed Business 0.3 behind it.

// editionScript is a stand-in binary that answers `version` like a release does and
// `version -o json` with its edition, so the updater can measure what is installed.
func editionScript(version, edition string) []byte {
	return []byte("#!/bin/sh\ncase \"$*\" in\n" +
		"*json*) echo '{\"version\":\"" + version + "\",\"edition\":\"" + edition + "\"}';;\n" +
		"*) echo \"olivares " + version + " (commit t, built t, " + edition + " witness)\";;\nesac\n")
}

// editionUpgradeArgs is the licensed route against f, as the how-to spells it.
func editionUpgradeArgs(f *updFixture, dataDir, target string, extra ...string) []string {
	return append([]string{"--enterprise", "--download-protocol", "legacy", "--token", "tkn", "--endpoint", f.server.URL,
		"--pubkey", f.pubB64, "--data-dir", dataDir, "--target", target, "--os", "linux", "--arch", "amd64"}, extra...)
}

func TestUpgradeEnterpriseMovesACommunityBuildToTheSameVersionOfBusiness(t *testing.T) {
	t.Run("a Community build at 0.3 installs Business 0.3", func(t *testing.T) {
		dataDir := t.TempDir()
		installDevLicense(t, dataDir)
		f := newUpdFixture(t, "0.3", "", editionScript("0.3", "enterprise"))
		target := writeTarget(t, editionScript("0.3", "community"))

		out, err := runUpgradeCmd(t, editionUpgradeArgs(f, dataDir, target, "--yes")...)
		if err != nil {
			t.Fatalf("upgrade --enterprise: %v", err)
		}
		if strings.Contains(out, "nothing to do") {
			t.Fatalf("a Community build must not be told Business of the same version is nothing to do:\n%s", out)
		}
		if got := runsVersion(t, target); !strings.Contains(got, "enterprise witness") {
			t.Fatalf("the target is still not the Business build: %q", got)
		}
		if b, _ := filepath.Glob(target + ".bak-*"); len(b) != 1 || !strings.Contains(runsVersion(t, b[0]), "community witness") {
			t.Fatalf("the Community build must be kept as the backup: %v", b)
		}

		// The Business build now installed reports its edition: the same command again is up to date.
		out, err = runUpgradeCmd(t, editionUpgradeArgs(f, dataDir, target, "--yes")...)
		if err != nil || !strings.Contains(out, "nothing to do") {
			t.Fatalf("the second run must find Business 0.3 installed (err=%v):\n%s", err, out)
		}
		if b, _ := filepath.Glob(target + ".bak-*"); len(b) != 1 {
			t.Fatalf("the second run must not swap again: %v", b)
		}
	})

	t.Run("a Community build at 0.2 installs Business 0.3 and names both moves", func(t *testing.T) {
		dataDir := t.TempDir()
		installDevLicense(t, dataDir)
		f := newUpdFixture(t, "0.3", "", editionScript("0.3", "enterprise"))
		target := writeTarget(t, editionScript("0.2", "community"))

		out, err := runUpgradeCmd(t, editionUpgradeArgs(f, dataDir, target, "--yes")...)
		if err != nil {
			t.Fatalf("upgrade --enterprise: %v", err)
		}
		for _, want := range []string{"current:   0.2", "available: 0.3", "edition:   community -> enterprise", "status:    upgrade available"} {
			if !strings.Contains(out, want) {
				t.Errorf("the plan does not say %q:\n%s", want, out)
			}
		}
		if got := runsVersion(t, target); !strings.Contains(got, "enterprise witness") {
			t.Fatalf("the target is still not the Business build: %q", got)
		}
	})

	t.Run("a Business build at 0.3 is up to date and untouched", func(t *testing.T) {
		dataDir := t.TempDir()
		installDevLicense(t, dataDir)
		f := newUpdFixture(t, "0.3", "", editionScript("0.3", "enterprise"))
		target := writeTarget(t, editionScript("0.3", "enterprise"))

		out, err := runUpgradeCmdAs(t, "enterprise", editionUpgradeArgs(f, dataDir, target, "--yes")...)
		if err != nil {
			t.Fatalf("upgrade --enterprise: %v", err)
		}
		if !strings.Contains(out, "nothing to do") || !strings.Contains(out, "status:    up to date") {
			t.Fatalf("Business 0.3 over Business 0.3 is up to date:\n%s", out)
		}
		if b, _ := filepath.Glob(target + ".bak-*"); len(b) != 0 {
			t.Fatalf("an up-to-date run must not swap: %v", b)
		}
	})

	t.Run("an older Business release is still refused as a rollback", func(t *testing.T) {
		dataDir := t.TempDir()
		installDevLicense(t, dataDir)
		f := newUpdFixture(t, "0.2", "", editionScript("0.2", "enterprise"))
		target := writeTarget(t, editionScript("0.3", "community"))

		_, err := runUpgradeCmd(t, editionUpgradeArgs(f, dataDir, target, "--yes")...)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "anti-rollback") {
			t.Fatalf("Business 0.2 over Community 0.3 must be blocked by anti-rollback, got %v", err)
		}
		if got := runsVersion(t, target); !strings.Contains(got, "community witness") {
			t.Fatalf("a refused rollback must leave the target untouched: %q", got)
		}
	})

	t.Run("min_version stays armed across the edition move", func(t *testing.T) {
		dataDir := t.TempDir()
		installDevLicense(t, dataDir)
		f := newUpdFixture(t, "0.3", "0.2", editionScript("0.3", "enterprise"))
		target := writeTarget(t, editionScript("0.1", "community"))

		_, err := runUpgradeCmd(t, editionUpgradeArgs(f, dataDir, target, "--yes")...)
		if err == nil || !strings.Contains(err.Error(), "cannot jump directly") {
			t.Fatalf("a Community 0.1 below min_version 0.2 must be refused, got %v", err)
		}
		if got := runsVersion(t, target); !strings.Contains(got, "community witness") {
			t.Fatalf("a refused jump must leave the target untouched: %q", got)
		}
	})

	t.Run("--check names the edition move and installs nothing", func(t *testing.T) {
		dataDir := t.TempDir()
		installDevLicense(t, dataDir)
		f := newUpdFixture(t, "0.3", "", editionScript("0.3", "enterprise"))
		target := writeTarget(t, editionScript("0.3", "community"))

		out, err := runUpgradeCmd(t, editionUpgradeArgs(f, dataDir, target, "--check")...)
		if err != nil {
			t.Fatalf("upgrade --enterprise --check: %v", err)
		}
		for _, want := range []string{"edition:   community -> enterprise", "status:    upgrade available"} {
			if !strings.Contains(out, want) {
				t.Errorf("--check does not say %q:\n%s", want, out)
			}
		}
		if got := runsVersion(t, target); !strings.Contains(got, "community witness") {
			t.Fatalf("--check must not swap: %q", got)
		}
	})

	t.Run("the default release-v1 route installs it too", func(t *testing.T) {
		dataDir := t.TempDir()
		installDevLicense(t, dataDir)
		f := newV1Fixture(t, "0.3", "biz+reg", editionScript("0.3", "enterprise"))
		target := writeTarget(t, editionScript("0.3", "community"))

		_, err := runUpgradeCmd(t, "--enterprise", "--token", "tkn", "--endpoint", f.server.URL, "--pubkey", f.pubB64,
			"--data-dir", dataDir, "--target", target, "--os", "linux", "--arch", "amd64", "--yes")
		if err != nil {
			t.Fatalf("release-v1 upgrade --enterprise: %v", err)
		}
		if got := runsVersion(t, target); !strings.Contains(got, "enterprise witness") {
			t.Fatalf("the target is still not the Business build: %q", got)
		}
	})

	// A target that does not say which edition it is cannot be told from Community, and --enterprise
	// asked for Business: the signed Business build is installed (and the plan says why).
	for _, tc := range []struct{ name, script string }{
		{"output that is not JSON (a build from before `version -o json`)", string(l4Script("0.3"))},
		{"JSON without an edition", "#!/bin/sh\ncase \"$*\" in\n*json*) echo '{\"version\":\"0.3\"}';;\n*) echo \"olivares 0.3 (commit t, built t, old witness)\";;\nesac\n"},
		{"an edition that is not one of the two names", string(editionScript("0.3", "community\\u001b[2Jx"))},
	} {
		t.Run("an installed edition that was not read is moved to Business: "+tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			installDevLicense(t, dataDir)
			f := newUpdFixture(t, "0.3", "", editionScript("0.3", "enterprise"))
			target := writeTarget(t, []byte(tc.script))

			out, err := runUpgradeCmd(t, editionUpgradeArgs(f, dataDir, target, "--yes")...)
			if err != nil {
				t.Fatalf("upgrade --enterprise: %v", err)
			}
			if !strings.Contains(out, "edition:   unknown -> enterprise") || strings.Contains(out, "nothing to do") || strings.Contains(out, "\x1b") {
				t.Fatalf("an unread edition must be named, moved, and never echoed:\n%q", out)
			}
			if !strings.Contains(out, "did not report its edition") {
				t.Fatalf("the plan must say why the installed edition is unknown:\n%s", out)
			}
			if got := runsVersion(t, target); !strings.Contains(got, "enterprise witness") {
				t.Fatalf("the target is still not the Business build: %q", got)
			}
		})
	}

	t.Run("a declared --current-version still sees the edition move", func(t *testing.T) {
		dataDir := t.TempDir()
		installDevLicense(t, dataDir)
		f := newUpdFixture(t, "0.3", "", editionScript("0.3", "enterprise"))
		target := writeTarget(t, editionScript("0.3", "community"))

		if _, err := runUpgradeCmd(t, editionUpgradeArgs(f, dataDir, target, "--current-version", "0.3", "--yes")...); err != nil {
			t.Fatalf("upgrade --enterprise --current-version: %v", err)
		}
		if got := runsVersion(t, target); !strings.Contains(got, "enterprise witness") {
			t.Fatalf("the target is still not the Business build: %q", got)
		}
	})
}

// Only the licensed route delivers Business. The public channel and an air-gap bundle make no
// edition claim: a Community build at the channel's version stays up to date there.
func TestUpgradeMakesNoEditionClaimOffTheLicensedRoute(t *testing.T) {
	t.Run("public channel", func(t *testing.T) {
		f := newUpdFixture(t, "0.3", "", editionScript("0.3", "community"))
		target := writeTarget(t, editionScript("0.3", "community"))

		out, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64, "--target", target,
			"--os", "linux", "--arch", "amd64", "--yes", "--data-dir", t.TempDir())
		if err != nil {
			t.Fatalf("upgrade: %v", err)
		}
		if !strings.Contains(out, "nothing to do") || strings.Contains(out, "edition:") {
			t.Fatalf("the public channel makes no edition claim:\n%s", out)
		}
	})

	t.Run("public channel -o json carries no edition", func(t *testing.T) {
		dataDir := t.TempDir()
		t.Setenv("OLIVARES_DATA_DIR", dataDir)
		f := newUpdFixture(t, "0.3", "", editionScript("0.3", "community"))
		target := writeTarget(t, editionScript("0.3", "community"))

		out, _, err := runLeafCLI(t, "-o", "json", "upgrade", "--endpoint", f.server.URL, "--pubkey", f.pubB64, "--target", target,
			"--os", "linux", "--arch", "amd64", "--check", "--data-dir", dataDir)
		if err != nil {
			t.Fatalf("upgrade -o json: %v", err)
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("not one JSON document: %v\n%s", err, out)
		}
		for _, key := range []string{"edition_current", "edition_available"} {
			if got, ok := doc[key].(string); !ok || got != "" {
				t.Errorf("%s = %#v, want the empty string off the licensed route", key, doc[key])
			}
		}
	})

	t.Run("air-gap bundle", func(t *testing.T) {
		f := newUpdFixture(t, "0.3", "", editionScript("0.3", "enterprise"))
		target := writeTarget(t, editionScript("0.3", "community"))

		out, err := runUpgradeCmd(t, "--enterprise", "--bundle", f.writeBundle(t), "--pubkey", f.pubB64, "--target", target,
			"--os", "linux", "--arch", "amd64", "--check", "--data-dir", t.TempDir())
		if err != nil {
			t.Fatalf("upgrade --bundle --check: %v", err)
		}
		if !strings.Contains(out, "nothing to do") || strings.Contains(out, "edition:") {
			t.Fatalf("a bundle makes no edition claim:\n%s", out)
		}
	})
}

// Every run without --yes asks first, and the question names the edition move: "0.3 -> 0.3"
// alone reads as a no-op.
func TestUpgradeConfirmPromptNamesTheEditionMove(t *testing.T) {
	asEdition(t, "community")
	dataDir := t.TempDir()
	installDevLicense(t, dataDir)
	t.Setenv("OLIVARES_DATA_DIR", dataDir)
	f := newUpdFixture(t, "0.3", "", editionScript("0.3", "enterprise"))
	target := writeTarget(t, editionScript("0.3", "community"))

	cmd := newUpgradeCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetIn(strings.NewReader("n\n"))
	cmd.SetArgs(editionUpgradeArgs(f, dataDir, target))
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "aborted by operator") {
		t.Fatalf("answering n must abort, got %v\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "(0.3 community -> 0.3 enterprise)?") {
		t.Fatalf("the question does not name the edition move:\n%s", buf.String())
	}
	if got := runsVersion(t, target); !strings.Contains(got, "community witness") {
		t.Fatalf("an aborted run must leave the target untouched: %q", got)
	}
}

// The running executable IS this build, so it answers for itself without being executed.
func TestInstalledEditionOfTheRunningExecutableIsThisBuild(t *testing.T) {
	for _, edition := range []string{"community", "enterprise"} {
		asEdition(t, edition)
		if got := installedEdition(t.Context(), "", true); got != edition {
			t.Errorf("installedEdition(self) = %q, want %q", got, edition)
		}
	}
}

func TestUpgradeJSONNamesTheEditionMove(t *testing.T) {
	dataDir := t.TempDir()
	installDevLicense(t, dataDir)
	f := newUpdFixture(t, "0.3", "", editionScript("0.3", "enterprise"))
	target := writeTarget(t, editionScript("0.3", "community"))
	t.Setenv("OLIVARES_DATA_DIR", dataDir)

	out, _, err := runLeafCLI(t, append([]string{"-o", "json", "upgrade"}, editionUpgradeArgs(f, dataDir, target, "--check")...)...)
	if err != nil {
		t.Fatalf("upgrade --enterprise --check -o json: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("the result is not one JSON document: %v\n%s", err, out)
	}
	for key, want := range map[string]string{
		"status": upgradeStatusAvailable, "action": upgradeActionChecked, "current": "0.3", "available": "0.3",
		"edition_current": "community", "edition_available": "enterprise",
	} {
		if got, _ := doc[key].(string); got != want {
			t.Errorf("%s = %q, want %q (document: %s)", key, got, want, out)
		}
	}
	if got := runsVersion(t, target); !strings.Contains(got, "community witness") {
		t.Fatalf("--check must not swap: %q", got)
	}
	if b, _ := filepath.Glob(target + ".bak-*"); len(b) != 0 {
		t.Fatalf("--check must not leave a backup: %v", b)
	}
}

// The two edition keys say what was read: both names for a Business build that is up to date,
// "unknown" and the Business name when the target did not say (an install is pending), so an
// unread edition is never the empty string a run off the licensed route carries.
func TestUpgradeJSONEditionKeysWhenUpToDateOrUnread(t *testing.T) {
	for _, tc := range []struct {
		name, target, status, current string
	}{
		{"business up to date", string(editionScript("0.3", "enterprise")), upgradeStatusUpToDate, "enterprise"},
		{"edition unread", string(l4Script("0.3")), upgradeStatusAvailable, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			installDevLicense(t, dataDir)
			t.Setenv("OLIVARES_DATA_DIR", dataDir)
			f := newUpdFixture(t, "0.3", "", editionScript("0.3", "enterprise"))
			target := writeTarget(t, []byte(tc.target))

			out, _, err := runLeafCLI(t, append([]string{"-o", "json", "upgrade"}, editionUpgradeArgs(f, dataDir, target, "--check")...)...)
			if err != nil {
				t.Fatalf("upgrade --enterprise --check -o json: %v", err)
			}
			var doc map[string]any
			if err := json.Unmarshal([]byte(out), &doc); err != nil {
				t.Fatalf("not one JSON document: %v\n%s", err, out)
			}
			if doc["status"] != tc.status {
				t.Errorf("status = %v, want %s", doc["status"], tc.status)
			}
			if got, _ := doc["edition_current"].(string); got != tc.current {
				t.Errorf("edition_current = %q, want %q", got, tc.current)
			}
			if got, _ := doc["edition_available"].(string); got != "enterprise" {
				t.Errorf("edition_available = %q, want enterprise", got)
			}
		})
	}
}

// The edition is part of the verdict, so it is read after the fingerprint the pre-swap
// re-check compares against: a target replaced while it is asked for its edition is refused.
func TestUpgradeRefusesATargetReplacedWhileItsEditionIsRead(t *testing.T) {
	dataDir := t.TempDir()
	installDevLicense(t, dataDir)
	f := newUpdFixture(t, "0.3", "", editionScript("0.3", "enterprise"))

	intruder := filepath.Join(t.TempDir(), "intruder")
	if err := os.WriteFile(intruder, []byte("#!/bin/sh\necho \"olivares 0.3 (commit t, built t, intruder witness)\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Answers `version` like a Community build; asked `version -o json` it is replaced, as a
	// package manager or an image rollout would, and still reports Community.
	target := writeTarget(t, []byte("#!/bin/sh\ncase \"$*\" in\n"+
		"*json*) mv "+intruder+" \"$0\"; echo '{\"version\":\"0.3\",\"edition\":\"community\"}';;\n"+
		"*) echo \"olivares 0.3 (commit t, built t, community witness)\";;\nesac\n"))

	out, err := runUpgradeCmd(t, editionUpgradeArgs(f, dataDir, target, "--yes")...)
	if err == nil || !strings.Contains(err.Error(), "CHANGED") {
		t.Fatalf("a target replaced while its edition was read must be refused as changed, got err=%v\n%s", err, out)
	}
	if got := runsVersion(t, target); !strings.Contains(got, "intruder witness") {
		t.Fatalf("the refusal must leave what replaced the target in place: %q", got)
	}
	if b, _ := filepath.Glob(target + ".bak-*"); len(b) != 0 {
		t.Fatalf("a refusal before the swap must leave no backup: %v", b)
	}
}

// Version and edition are asked the same way: the target answers for itself under the probe
// name, so an image binary does not hand over to an applied release in the data volume and
// report that release's answer for its own.
func TestProbeCommandAsksTheTargetUnderTheProbeName(t *testing.T) {
	cmd := probeCommand(t.Context(), "/usr/local/bin/olivares", "version", "-o", "json")
	if cmd.Path != "/usr/local/bin/olivares" || cmd.Args[0] != probeArg0 {
		t.Fatalf("probe runs %q as argv[0]=%q, want the target as %q", cmd.Path, cmd.Args[0], probeArg0)
	}
	if got := strings.Join(cmd.Args[1:], " "); got != "version -o json" {
		t.Fatalf("probe arguments = %q, want %q", got, "version -o json")
	}
}

// The edition is asked under the probe name too: a binary that answers differently when it is
// started under another name (an image binary that hands over to the applied release) is read
// as the file it is, not as the release it would start.
func TestInstalledEditionIsAskedUnderTheProbeName(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available to build the stub binary")
	}
	dir := t.TempDir()
	src := "package main\nimport (\"fmt\"; \"os\")\nfunc main(){\n" +
		"if len(os.Args) < 2 || os.Args[1] != \"version\" { os.Exit(2) }\n" +
		"ed := \"community\"\nif os.Args[0] == " + strconvQuote(probeArg0) + " { ed = \"enterprise\" }\n" +
		"fmt.Printf(\"{\\\"edition\\\":%q}\\n\", ed)\n}\n"
	srcPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "stub")
	build := exec.Command("go", "build", "-o", bin, srcPath)
	build.Env = append(os.Environ(), "GOFLAGS=-p=1")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build stub: %v\n%s", err, out)
	}
	if got := installedEdition(t.Context(), bin, false); got != "enterprise" {
		t.Fatalf("installedEdition = %q: the target was not asked under %q", got, probeArg0)
	}
}
