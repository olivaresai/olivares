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
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
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
			if !planRow(out.String(), "stop-disable", "service", "openrc:olivares") {
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

// TestPlanRowReadsWholeCellsInTheirColumns is the regression for the predicate
// the plan assertions in this package are written on.
//
// It exists because a predicate that searches ordered SUBSTRINGS anywhere in a
// line accepts `remove-tree  system-user  olivares` for a request of `remove` +
// `system-user`. `remove-tree` is a different real action of this planner and an
// identity is only ever `remove`d, so an oracle that accepts it reports a wrong
// uninstall as the right one. A role read out of the PATH column passes the same
// way, and both are rows this planner can print.
//
// Every fixture is rendered by termrender rather than typed out, so what is under
// test is the predicate against the real grammar and not against a hand-spaced
// imitation of it. Each negative also names something the fixture must have
// PRINTED, so a case cannot pass because nothing was rendered at all.
func TestPlanRowReadsWholeCellsInTheirColumns(t *testing.T) {
	// The environment is answered rather than read: resolveWidth consults COLUMNS,
	// and a value inherited from the runner would decide between the table and its
	// record fallback instead of the width asked for here.
	render := func(width int, rows ...[]string) string {
		var out strings.Builder
		termrender.New(&out, termrender.Options{
			Width:     width,
			LookupEnv: func(string) (string, bool) { return "", false },
		}).Table(termrender.Table{
			Header: []string{"action", "role", "path", "note"},
			Rows:   rows,
		})
		return out.String()
	}
	live := render(0,
		[]string{"stop-disable", "service", "systemd:olivares", ""},
		[]string{"remove", "binary", "/usr/local/bin/olivares", ""},
		[]string{"record", "witness", "/etc/olivares/olivares-uninstall-witness-c3fd72b3f230ccd2.json", ""},
		[]string{"remove", "system-user", "olivares", ""},
		[]string{"remove", "system-group", "olivares", ""})
	// The same verdicts on an estate that needs no stop-disable: every column is
	// narrower, and the predicate must not notice.
	narrow := render(0,
		[]string{"keep", "config", "/etc/olivares/olivares.env", ""},
		[]string{"remove", "system-user", "olivares", ""})
	// Two paths outside ASCII, each with an ASCII identity row BENEATH it. Visible
	// columns and byte offsets part company at the first multibyte rune, so a row
	// read at byte offsets is cut inside the encoding, fails the rebuild — and
	// takes every row after it with it, leaving the identity assertion below
	// unanswered. The spellings are escaped rather than typed so the difference
	// between them is in the source and not in an editor's normalisation.
	accented := render(0,
		[]string{"keep", "workspace", "/mnt/caf\u00e9", ""}, // precomposed: 9 columns, 10 bytes
		[]string{"remove", "system-user", "olivares", ""})
	combining := render(0,
		[]string{"keep", "workspace", "/mnt/cafe\u0301", ""}, // decomposed: 9 columns, 11 bytes
		[]string{"remove", "system-user", "olivares", ""})

	for _, tc := range []struct {
		name    string
		out     string
		want    []string
		printed string // what the fixture must have rendered, so no case is vacuous
		match   bool
	}{
		{"the service row, action role and identity", live,
			[]string{"stop-disable", "service", "systemd:olivares"}, "", true},
		{"an identity row, action and role", live,
			[]string{"remove", "system-user"}, "", true},
		{"the same verdict in a narrower table", narrow,
			[]string{"remove", "system-user"}, "", true},
		{"a longer action that begins with the one asked for",
			render(0, []string{"remove-tree", "system-user", "olivares", ""}),
			[]string{"remove", "system-user"}, "remove-tree", false},
		{"a longer role that begins with the one asked for",
			render(0, []string{"remove", "system-user-shadow", "olivares", ""}),
			[]string{"remove", "system-user"}, "system-user-shadow", false},
		{"the role named in the path and not in the role column",
			render(0, []string{"remove", "data", "/tmp/system-user", ""}),
			[]string{"remove", "system-user"}, "/tmp/system-user", false},
		{"the role named in the note and not in the role column",
			render(0, []string{"remove", "data", "/srv/olivares", "left by the remove system-user pass"}),
			[]string{"remove", "system-user"}, "remove system-user pass", false},
		{"a different identity behind the same prefix",
			render(0, []string{"stop-disable", "service", "openrc:olivares-other", ""}),
			[]string{"stop-disable", "service", "openrc:olivares"}, "openrc:olivares-other", false},
		{"the action and the role swapped",
			render(0, []string{"service", "stop-disable", "openrc:olivares", ""}),
			[]string{"stop-disable", "service"}, "stop-disable", false},
		{"the row broken into a record block by a narrow terminal",
			render(40, []string{"stop-disable", "service", "systemd:olivares", ""}),
			[]string{"stop-disable", "service"}, "ACTION  stop-disable", false},
		{"a verdict this plan did not reach", live,
			[]string{"keep", "witness"}, "record", false},
		{"the header is not a row", live,
			[]string{"action", "role"}, "ACTION", false},
		{"nothing asked for", live, nil, "record", false},
		{"a path holding one space is still one cell",
			render(0, []string{"keep", "data", "/srv/olivares data", ""}),
			[]string{"keep", "data", "/srv/olivares data"}, "", true},
		// An empty cell prints as padding, so a whitespace split slides every later
		// cell one column left and the PATH answers for the ROLE.
		{"an empty role does not let the path answer for it",
			render(0, []string{"remove", "", "system-user", ""}),
			[]string{"remove", "system-user"}, "system-user", false},
		// cleanAbsolutePath admits consecutive spaces in a workspace path and Table
		// prints them verbatim, so the complete path has to match and its prefix
		// must not.
		{"a path holding two spaces matches itself",
			render(0, []string{"keep", "workspace", "/mnt/project  beta", ""}),
			[]string{"keep", "workspace", "/mnt/project  beta"}, "/mnt/project  beta", true},
		{"a path holding two spaces is not its own prefix",
			render(0, []string{"keep", "workspace", "/mnt/project  beta", ""}),
			[]string{"keep", "workspace", "/mnt/project"}, "/mnt/project  beta", false},
		{"a path outside ASCII matches its whole value", accented,
			[]string{"keep", "workspace", "/mnt/caf\u00e9"}, "/mnt/caf\u00e9", true},
		{"a path outside ASCII is not its truncation", accented,
			[]string{"keep", "workspace", "/mnt/caf"}, "/mnt/caf\u00e9", false},
		{"the ASCII row beneath a multibyte one is still read", accented,
			[]string{"remove", "system-user"}, "/mnt/caf\u00e9", true},
		{"a combining accent matches its whole value", combining,
			[]string{"keep", "workspace", "/mnt/cafe\u0301"}, "/mnt/cafe\u0301", true},
		// Two spellings of the same glyphs are two different paths, because they are
		// two different paths in the manifest this table printed.
		{"a combining accent is not the precomposed spelling", combining,
			[]string{"keep", "workspace", "/mnt/caf\u00e9"}, "/mnt/cafe\u0301", false},
		{"the ASCII row beneath a combining one is still read", combining,
			[]string{"remove", "system-user"}, "/mnt/cafe\u0301", true},
		// THE LIMIT, pinned where it can be read: the padding is spaces, so a cell
		// ending in one prints exactly like the same cell without it. The path below
		// really is "/mnt/project  " and this table cannot say so. A test that has to
		// see that reads BuildPlan's Items. If the renderer ever quotes its cells,
		// this case goes red and the limit can be lifted.
		{"a trailing space is not in the printed form at all",
			render(0, []string{"keep", "workspace", "/mnt/project  ", ""}),
			[]string{"keep", "workspace", "/mnt/project"}, "workspace", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.printed != "" && !strings.Contains(tc.out, tc.printed) {
				t.Fatalf("the fixture never printed %q, so this case proves nothing:\n%s", tc.printed, tc.out)
			}
			if got := planRow(tc.out, tc.want...); got != tc.match {
				t.Errorf("planRow(%q) = %v, want %v, in:\n%s", tc.want, got, tc.match, tc.out)
			}
		})
	}
}

// planColumns are the headings the uninstall plan prints, in order. The helpers
// below anchor on them, because they are what fixes the column positions of
// every row beneath.
var planColumns = []string{"ACTION", "ROLE", "PATH", "NOTE"}

// planRuneWidth is the renderer's own width model for the plain output these
// helpers read: visibleWidth counts a combining Mn/Me mark as zero columns and
// every other rune as one (termrender.go:203-228). Ranging over a string gives
// the same decoding, an invalid byte included, which that function also counts
// as one.
//
// Colour is off on this path — a Builder and a pipe are not terminals — so the
// escape-sequence half of that contract cannot arise. If one ever did, the
// columns would shift, the row would fail the rebuild below and be refused
// rather than misread.
func planRuneWidth(r rune) int {
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
		return 0
	}
	return 1
}

// planWidth is planRuneWidth over a whole cell.
func planWidth(s string) int {
	n := 0
	for _, r := range s {
		n += planRuneWidth(r)
	}
	return n
}

// planHeader returns the VISIBLE column each heading begins at, or nil if this
// line is not the plan's header.
//
// Visible, not byte: Table measures and pads in columns, so a row holding one
// multibyte cell puts every later column at a byte offset the ASCII header
// cannot give. The headings hold no spaces, so a plain scan finds them.
func planHeader(line string) []int {
	var names []string
	var starts []int
	col, at, atCol := 0, -1, 0
	for i, r := range line {
		if r == ' ' {
			if at >= 0 {
				names = append(names, line[at:i])
				starts = append(starts, atCol)
				at = -1
			}
		} else if at < 0 {
			at, atCol = i, col
		}
		col += planRuneWidth(r)
	}
	if at >= 0 {
		names = append(names, line[at:])
		starts = append(starts, atCol)
	}
	if !slices.Equal(names, planColumns) {
		return nil
	}
	return starts
}

// planVisibleSlice returns the bytes of line covering visible columns [from,to),
// whole runes only, with the padding removed. A negative to means "to the end".
//
// A zero-width mark stays with the rune it modifies: the slice only moves on at
// a rune that occupies a column of its own.
func planVisibleSlice(line string, from, to int) string {
	col, start, end := 0, -1, len(line)
	for i, r := range line {
		w := planRuneWidth(r)
		if w > 0 {
			if to >= 0 && col >= to {
				end = i
				break
			}
			if start < 0 && col >= from {
				start = i
			}
		}
		col += w
	}
	if start < 0 {
		return "" // the line was right-trimmed: this column is past its end
	}
	return strings.TrimRight(line[start:end], " ")
}

// planRowCells reads one line at the columns the header fixed.
func planRowCells(line string, starts []int) []string {
	cells := make([]string, len(starts))
	for c := range starts {
		to := -1
		if c+1 < len(starts) {
			// The next column begins two spaces after this one ends.
			to = starts[c+1] - 2
		}
		cells[c] = planVisibleSlice(line, starts[c], to)
	}
	return cells
}

// planRowLine rebuilds what those cells would print at those columns. A line
// that does not come back is not a row of this table and is not read as one.
func planRowLine(cells []string, starts []int) string {
	var line strings.Builder
	col := 0
	for c, cell := range cells {
		if pad := starts[c] - col; pad > 0 {
			line.WriteString(strings.Repeat(" ", pad))
			col += pad
		} else if c > 0 && pad < 0 {
			return "" // a cell wider than its column: not this table's row
		}
		line.WriteString(cell)
		col += planWidth(cell)
	}
	return strings.TrimRight(line.String(), " ")
}

// planRows reads the printed plan back into rows, cell by cell.
//
// The columns come from the HEADER, and they have to: a run of spaces inside a
// row is NOT a column boundary. An empty cell contributes only padding, which
// merges with the separator and shifts every later cell one column left, and a
// path may hold two consecutive spaces — cleanAbsolutePath admits them and Table
// prints them verbatim. Either shape makes a whitespace split read the wrong
// column.
//
// The record fallback a narrow terminal triggers prints one field per line and
// no header, so it yields no rows and every request is refused.
func planRows(out string) [][]string {
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		starts := planHeader(line)
		if starts == nil {
			continue
		}
		var rows [][]string
		for _, row := range lines[i+1:] {
			if strings.TrimSpace(row) == "" {
				break
			}
			cells := planRowCells(row, starts)
			if planRowLine(cells, starts) != strings.TrimRight(row, " ") {
				break
			}
			rows = append(rows, cells)
		}
		return rows
	}
	return nil
}

// planRow reports whether the plan printed ONE row whose leading cells are
// exactly the ones given: the action, then the role, then the path or identity
// when the caller asks for one. Only the padding Table adds is ignored, so a
// longer action or role that merely begins with the request, a value found in
// another column, the cells swapped and a row broken into a record block are all
// refused. Column widths move with the fixture and do not matter.
//
// Cells are compared as BYTES. Two spellings of the same accented path are two
// different paths here, because they are two different paths in the manifest
// this table printed; nothing is normalised on the way through.
//
// Asking for nothing is false: an empty question has no answer, and true would
// make a typo look like a pass.
//
// THE ONE DISTINCTION THIS CANNOT MAKE is the presentation's and not the
// predicate's: a cell whose content ENDS in spaces prints exactly like the same
// content without them, because the padding is spaces too. A path of "/srv/x  "
// is accepted for a request of "/srv/x". Interior spaces ARE distinguished. A
// test that has to see a trailing space reads BuildPlan's Items, which carry the
// path unrendered, instead of this table.
func planRow(out string, cells ...string) bool {
	if len(cells) == 0 {
		return false
	}
	for _, row := range planRows(out) {
		if len(row) >= len(cells) && slices.Equal(row[:len(cells)], cells) {
			return true
		}
	}
	return false
}
