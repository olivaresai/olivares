// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package localinstall

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// runlevels stages an OpenRC runlevel tree the way rc-update leaves it: the
// default runlevel directory, holding a default/olivares link to the unit when
// enabled is true and nothing when the service was never added. The link is
// dangling on purpose — the unit is not staged — because that is what the
// entry looks like to Lstat, which is how rc-update del itself finds it.
// Nothing under the host's /etc is read or written.
func runlevels(t *testing.T, enabled bool) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "default"), 0o755); err != nil {
		t.Fatal(err)
	}
	if enabled {
		if err := os.Symlink("/etc/init.d/olivares", filepath.Join(dir, "default", "olivares")); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// realLookup answers through the real filesystem, over a staged tree.
var realLookup = runlevelLookup{stat: os.Stat, lstat: os.Lstat}

// recorder returns a runner that records every command and answers rc-service
// stop and rc-update del with the injected results.
func recorder(calls *[]string, stopErr, delErr error) func(string, ...string) error {
	return func(name string, args ...string) error {
		call := strings.Join(append([]string{name}, args...), " ")
		*calls = append(*calls, call)
		switch call {
		case "rc-service olivares stop":
			return stopErr
		case "rc-update del olivares default":
			return delErr
		}
		return fmt.Errorf("unexpected command %q", call)
	}
}

func TestInitAdapterStopAndReloadCommands(t *testing.T) {
	tests := []struct {
		name, mode, init string
		enabled          bool
		want             []string
	}{
		{"system systemd", "system", "systemd", false, []string{"systemctl disable --now olivares", "systemctl daemon-reload"}},
		{"user systemd", "user", "systemd", false, []string{"systemctl --user disable --now olivares", "systemctl --user daemon-reload"}},
		{"OpenRC enabled", "system", "openrc", true, []string{"rc-service olivares stop", "rc-update del olivares default"}},
		{"OpenRC never enabled", "system", "openrc", false, []string{"rc-service olivares stop"}},
		{"system launchd", "system", "launchd", false, []string{"launchctl bootout system/dev.olivares.olivares"}},
		{"user launchd", "user", "launchd", false, []string{fmt.Sprintf("launchctl bootout gui/%d/dev.olivares.olivares", os.Getuid())}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			run := func(name string, args ...string) error {
				got = append(got, strings.Join(append([]string{name}, args...), " "))
				return nil
			}
			m := &Manifest{Mode: tc.mode, Init: tc.init}
			if err := stopServiceWithOps(m, run, realLookup, runlevels(t, tc.enabled)); err != nil {
				t.Fatal(err)
			}
			if err := reloadServiceManager(m, run); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("adapter calls = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInitAdapterFailureIsNotReportedAsRemoved(t *testing.T) {
	err := stopService(&Manifest{Mode: "system", Init: "systemd"}, func(string, ...string) error {
		return fmt.Errorf("not available")
	})
	if err == nil || !strings.Contains(err.Error(), "stop/disable systemd") {
		t.Fatalf("adapter failure was not propagated with its phase: %v", err)
	}
}

// The measured defect (acceptance of 6005abc75f, D1): the signed adapter
// installs without enabling, real OpenRC's rc-update del exits 1 for a service
// that is not in the runlevel, and stopService read that as a fatal failure
// after the stop had already succeeded — preserve and purge could not proceed.
// What decides the disable is runlevel MEMBERSHIP, never whether the service is
// running: a never-enabled service that is running gets only the stop, an
// enabled one that is already inactive still gets the disable, an entry of any
// kind is membership, and a real failure of either command stays fatal in the
// order the commands run.
func TestOpenRCDisableFollowsRunlevelMembershipNotServiceState(t *testing.T) {
	stop, del := "rc-service olivares stop", "rc-update del olivares default"
	failing := fmt.Errorf("exit status 1")
	tests := []struct {
		name      string
		enabled   bool
		shape     func(t *testing.T, dir string) // reshapes the staged tree
		stopErr   error
		delErr    error
		wantCalls []string
		wantErr   string
	}{
		{name: "never enabled, running: the disable that would fail is not run",
			delErr: failing, wantCalls: []string{stop}},
		{name: "enabled but inactive: rc-service stop reports 0, the disable still runs",
			enabled: true, wantCalls: []string{stop, del}},
		{name: "enabled: a real disable failure stays fatal",
			enabled: true, delErr: failing, wantCalls: []string{stop, del}, wantErr: "disable OpenRC service: exit status 1"},
		{name: "enabled: a stop failure stays fatal and the disable is not attempted",
			enabled: true, stopErr: failing, wantCalls: []string{stop}, wantErr: "stop OpenRC service"},
		{name: "never enabled: a stop failure stays fatal",
			stopErr: failing, wantCalls: []string{stop}, wantErr: "stop OpenRC service"},
		{name: "an entry that is a regular file is still membership: the disable is attempted",
			shape: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "default", "olivares"), []byte("odd"), 0o644); err != nil {
					t.Fatal(err)
				}
			}, wantCalls: []string{stop, del}},
		{name: "a runlevel directory reached through a link resolves: the absent entry is proved",
			shape: func(t *testing.T, dir string) {
				real := filepath.Join(dir, "real-default")
				if err := os.Rename(filepath.Join(dir, "default"), real); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(real, filepath.Join(dir, "default")); err != nil {
					t.Fatal(err)
				}
			}, delErr: failing, wantCalls: []string{stop}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := runlevels(t, tc.enabled)
			if tc.shape != nil {
				tc.shape(t, dir)
			}
			var calls []string
			err := stopServiceWithOps(&Manifest{Mode: "system", Init: "openrc"}, recorder(&calls, tc.stopErr, tc.delErr), realLookup, dir)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("stopService: %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("error = %v, want one naming %q", err, tc.wantErr)
			}
			if !reflect.DeepEqual(calls, tc.wantCalls) {
				t.Fatalf("service-manager calls = %v, want %v", calls, tc.wantCalls)
			}
		})
	}
}

// An absent entry only means something under a runlevel directory that exists
// and resolves to a directory. Leaf ENOENT also occurs when the parent is
// missing, dangling or not a directory, and that is not a healthy membership
// state: it is fatal, the disable is not skipped, and the error names what
// could not be established. The stop has run, as today; nothing else has.
func TestOpenRCRunlevelParentMustBeProvenBeforeAbsence(t *testing.T) {
	stop := "rc-service olivares stop"
	tests := []struct {
		name    string
		shape   func(t *testing.T, dir string) string // returns the runlevel dir to use
		wantErr string
	}{
		{"no runlevel tree at all", func(t *testing.T, dir string) string {
			return filepath.Join(dir, "not-staged")
		}, "read OpenRC runlevel directory"},
		{"the default runlevel directory is missing", func(t *testing.T, dir string) string {
			if err := os.Remove(filepath.Join(dir, "default")); err != nil {
				t.Fatal(err)
			}
			return dir
		}, "read OpenRC runlevel directory"},
		{"the default runlevel is a regular file", func(t *testing.T, dir string) string {
			if err := os.Remove(filepath.Join(dir, "default")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "default"), []byte("not a directory\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return dir
		}, "is not a directory"},
		{"the default runlevel is a dangling link", func(t *testing.T, dir string) string {
			if err := os.Remove(filepath.Join(dir, "default")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(dir, "gone"), filepath.Join(dir, "default")); err != nil {
				t.Fatal(err)
			}
			return dir
		}, "read OpenRC runlevel directory"},
		{"the default runlevel is a link to a regular file", func(t *testing.T, dir string) string {
			if err := os.Remove(filepath.Join(dir, "default")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "file"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(dir, "file"), filepath.Join(dir, "default")); err != nil {
				t.Fatal(err)
			}
			return dir
		}, "is not a directory"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.shape(t, runlevels(t, false))
			var calls []string
			err := stopServiceWithOps(&Manifest{Mode: "system", Init: "openrc"}, recorder(&calls, nil, nil), realLookup, dir)
			if err == nil || !strings.Contains(err.Error(), "disable OpenRC service") || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("an unproven runlevel directory was not fatal, or the error does not say why: %v", err)
			}
			if !reflect.DeepEqual(calls, []string{stop}) {
				t.Fatalf("service-manager calls = %v, want only the stop", calls)
			}
		})
	}
}

// Errors a lookup can return that no staged tree produces deterministically —
// permission and I/O failures, at either level — are injected through the
// per-call seam. Every one is fatal and skips nothing; the two positive
// injections prove the seam itself carries the decision.
func TestOpenRCRunlevelLookupErrorsAreFatalNotAbsence(t *testing.T) {
	stop, del := "rc-service olivares stop", "rc-update del olivares default"
	dirInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	denied := func(op string) func(string) (os.FileInfo, error) {
		return func(name string) (os.FileInfo, error) {
			return nil, &os.PathError{Op: op, Path: name, Err: fs.ErrPermission}
		}
	}
	ioErr := func(op string) func(string) (os.FileInfo, error) {
		return func(name string) (os.FileInfo, error) {
			return nil, &os.PathError{Op: op, Path: name, Err: errors.New("input/output error")}
		}
	}
	healthy := func(string) (os.FileInfo, error) { return dirInfo, nil }
	absent := func(name string) (os.FileInfo, error) {
		return nil, &os.PathError{Op: "lstat", Path: name, Err: fs.ErrNotExist}
	}
	notCalled := func(t *testing.T) func(string) (os.FileInfo, error) {
		return func(name string) (os.FileInfo, error) {
			t.Fatalf("the entry %s was inspected under a runlevel directory that was not proven", name)
			return nil, nil
		}
	}
	tests := []struct {
		name      string
		look      runlevelLookup
		wantCalls []string
		wantErr   string
	}{
		{"runlevel directory: permission denied", runlevelLookup{stat: denied("stat"), lstat: notCalled(t)}, []string{stop}, "read OpenRC runlevel directory"},
		{"runlevel directory: I/O error", runlevelLookup{stat: ioErr("stat"), lstat: notCalled(t)}, []string{stop}, "read OpenRC runlevel directory"},
		{"entry: permission denied", runlevelLookup{stat: healthy, lstat: denied("lstat")}, []string{stop}, "read OpenRC runlevel entry"},
		{"entry: I/O error", runlevelLookup{stat: healthy, lstat: ioErr("lstat")}, []string{stop}, "read OpenRC runlevel entry"},
		{"control: proven directory and absent entry skip the disable", runlevelLookup{stat: healthy, lstat: absent}, []string{stop}, ""},
		{"control: proven directory and present entry run the disable", runlevelLookup{stat: healthy, lstat: healthy}, []string{stop, del}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			err := stopServiceWithOps(&Manifest{Mode: "system", Init: "openrc"}, recorder(&calls, nil, nil), tc.look, "/nowhere/runlevels")
			if tc.wantErr == "" && err != nil {
				t.Fatalf("stopService: %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), "disable OpenRC service") || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("lookup error was not fatal, or the error does not say what could not be read: %v", err)
			}
			if !reflect.DeepEqual(calls, tc.wantCalls) {
				t.Fatalf("service-manager calls = %v, want %v", calls, tc.wantCalls)
			}
		})
	}
}

// openrcEstate stages a live system OpenRC custom-layout installation under an
// offline root: the closed-layout files, the data tree, the manifest and a
// unit whose command_args= copies command_args_base= naming dataDir, exactly
// as the signed adapter renders it. A runlevel entry is staged under that
// root too, so the test can prove it is never consulted there.
func openrcEstate(t *testing.T, dataDir string) (*Manifest, string) {
	t.Helper()
	root := t.TempDir()
	m := &Manifest{
		Schema: ManifestSchema, Mode: "system", Init: "openrc", Layout: LayoutCustom,
		DataDir: dataDir, Config: "/etc/olivares/olivares.env", ManifestPath: filepath.Join(dataDir, "install-manifest.json"),
		Files: []File{
			{Path: "/usr/local/bin/olivares", Role: "binary", Mode: "0755", Managed: true},
			{Path: "/etc/olivares/olivares.env", Role: "config", Mode: "0640", Managed: true},
			{Path: "/etc/init.d/olivares", Role: "unit", Mode: "0755", Managed: true},
		},
		Account: Account{User: "olivares", Group: "olivares", UserCreated: true, GroupCreated: true},
	}
	files := map[string]string{
		"/usr/local/bin/olivares":    "binary",
		"/etc/olivares/olivares.env": "OLIVARES_EXTRA_ARGS=\n",
		"/etc/init.d/olivares": "#!/sbin/openrc-run\ncommand=\"/usr/local/bin/olivares\"\n" +
			"command_args_base=\"serve --data-dir=" + dataDir + " --listen=127.0.0.1:8443\"\ncommand_args=\"$command_args_base\"\n",
		dataDir + "/olivares.db":          "store",
		"/etc/runlevels/default/olivares": "a staged entry, never a host one",
	}
	for logical, body := range files {
		name := filepath.Join(root, logical[1:])
		if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, m.ManifestPath[1:]), body, 0o640); err != nil {
		t.Fatal(err)
	}
	return m, root
}

// Under an offline root no service-manager command runs and no runlevel entry
// is consulted — the plan discloses stop-disable for the live estate, preserve
// and purge remove what they promise, and the injected runner is never called.
func TestOfflineRootOpenRCEstateRunsNoServiceCommand(t *testing.T) {
	for _, op := range []Operation{Plan, Preserve, Purge} {
		t.Run(string(op), func(t *testing.T) {
			m, root := openrcEstate(t, "/srv/olivares-a")
			run := func(name string, args ...string) error {
				t.Fatalf("offline %s executed %s %s", op, name, strings.Join(args, " "))
				return nil
			}
			var out strings.Builder
			if err := Execute(m, Options{Operation: op, Root: root, Run: run, Out: &out}); err != nil {
				t.Fatalf("%s: %v\n%s", op, err, out.String())
			}
			if !strings.Contains(out.String(), "stop-disable service       openrc:olivares") {
				t.Fatalf("the live estate's service line is not disclosed:\n%s", out.String())
			}
			unitKept, binaryKept := exists(t, root, m.Unit()), exists(t, root, "/usr/local/bin/olivares")
			_, witness := witnessOnDisk(t, root, m)
			switch op {
			case Plan:
				if !unitKept || !binaryKept || witness {
					t.Fatalf("plan mutated the root: unit=%v binary=%v witness=%v", unitKept, binaryKept, witness)
				}
			case Preserve:
				if unitKept || binaryKept || !witness || !exists(t, root, m.DataDir) {
					t.Fatalf("preserve: unit=%v binary=%v witness=%v data=%v", unitKept, binaryKept, witness, exists(t, root, m.DataDir))
				}
			case Purge:
				if unitKept || binaryKept || witness || exists(t, root, m.DataDir) {
					t.Fatalf("purge: unit=%v binary=%v witness=%v data=%v", unitKept, binaryKept, witness, exists(t, root, m.DataDir))
				}
			}
			if !exists(t, root, "/etc/runlevels/default/olivares") {
				t.Fatal("the staged runlevel entry was touched under the offline root")
			}
		})
	}
}

// A fatal stop/disable leaves the estate exactly as it was: the software is not
// removed and no witness is written, so a retry starts from the same record.
// This is the Execute ordering every adapter shares; the OpenRC-specific
// verdicts that reach it are established by the stopService cases above.
func TestStopFailureLeavesSoftwareAndWritesNoWitness(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	m := userEstate(t, home, filepath.Join(home, "estate/data"))
	var calls int
	run := func(string, ...string) error {
		calls++
		return fmt.Errorf("exit status 1")
	}
	for _, op := range []Operation{Preserve, Purge} {
		err := Execute(m, Options{Operation: op, Run: run})
		if err == nil || !strings.Contains(err.Error(), "stop/disable user systemd service: exit status 1") {
			t.Fatalf("%s: stop failure not propagated with its phase: %v", op, err)
		}
		for _, role := range []string{"binary", "unit", "config"} {
			if !exists(t, "/", roleOf(t, m, role)) {
				t.Fatalf("%s removed the %s after the service manager failed", op, role)
			}
		}
		if _, witness := witnessOnDisk(t, "/", m); witness {
			t.Fatalf("%s wrote a witness after the service manager failed", op)
		}
		if !exists(t, "/", m.DataDir) {
			t.Fatalf("%s removed the data after the service manager failed", op)
		}
	}
	if calls != 2 {
		t.Fatalf("service manager called %d times, want once per operation", calls)
	}
}
