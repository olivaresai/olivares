// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package netfolder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		name, source string
		want         Spec
		wantErr      bool
	}{
		{"files", "//fileserver/projects", Spec{Name: "files", Type: "cifs", What: "//fileserver/projects"}, false},
		{"files", "//10.0.0.5/team/sub", Spec{Name: "files", Type: "cifs", What: "//10.0.0.5/team/sub"}, false},
		{"files", "fileserver:/export/data", Spec{Name: "files", Type: "nfs4", What: "fileserver:/export/data"}, false},
		{"Files", "//fileserver/projects", Spec{}, true},              // letters lower case and digits only: a hyphen would change the unit name
		{"my-files", "//fileserver/projects", Spec{}, true},           // same
		{"", "//fileserver/projects", Spec{}, true},                   // no name
		{"files", "fileserver", Spec{}, true},                         // neither //server/share nor server:/export
		{"files", "//fileserver/pro jects", Spec{}, true},             // a space would split the unit's What=
		{"files", "//fileserver/p,x", Spec{}, true},                   // a comma would add a mount option
		{"files", "//file=server/projects", Spec{}, true},             // so would an equals sign
		{"files", "fileserver:export", Spec{}, true},                  // an export is an absolute path
		{"files", "//fileserver/../etc", Spec{}, true},                // no parent path elements
		{"files", "//fileserver/projects\nExecStart=x", Spec{}, true}, // a newline would add a unit line
	}
	for _, c := range cases {
		got, err := Parse(c.name, c.source)
		if (err != nil) != c.wantErr {
			t.Errorf("Parse(%q, %q) error = %v, want error %v", c.name, c.source, err, c.wantErr)
			continue
		}
		if err == nil && got != c.want {
			t.Errorf("Parse(%q, %q) = %+v, want %+v", c.name, c.source, got, c.want)
		}
	}
}

type fakeHost struct {
	Host
	calls  [][]string
	active map[string]bool
	alive  bool
}

func newFakeHost(t *testing.T) *fakeHost {
	t.Helper()
	root := t.TempDir()
	f := &fakeHost{active: map[string]bool{}, alive: true}
	f.Host = Host{
		UnitDir:    filepath.Join(root, "units"),
		CredDir:    filepath.Join(root, "cred"),
		MountRoot:  filepath.Join(root, "mnt"),
		EngineUser: "olivares",
		Systemctl:  func(args ...string) error { f.calls = append(f.calls, args); return nil },
		Escape:     func(path string) (string, error) { return strings.ReplaceAll(strings.Trim(path, "/"), "/", "-"), nil },
		IsActive:   func(unit string) bool { return f.active[unit] },
		Responds:   func(string) bool { return f.alive },
	}
	for _, d := range []string{f.UnitDir, f.CredDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInstallSMBWritesUnitsAndCredentialWithoutLeakingThePassword(t *testing.T) {
	f := newFakeHost(t)
	spec, err := Parse("files", "//fileserver/projects")
	if err != nil {
		t.Fatal(err)
	}
	const password = "s3cret-pw-value"
	if err := f.Install(spec, "svc-olivares", password); err != nil {
		t.Fatal(err)
	}
	unit := strings.ReplaceAll(strings.Trim(f.MountRoot, "/"), "/", "-") + "-files"
	mount := read(t, filepath.Join(f.UnitDir, unit+".mount"))
	automount := read(t, filepath.Join(f.UnitDir, unit+".automount"))
	cred := filepath.Join(f.CredDir, "files.cred")

	for _, want := range []string{
		"What=//fileserver/projects", "Where=" + filepath.Join(f.MountRoot, "files"), "Type=cifs",
		"Options=credentials=" + cred + ",uid=olivares,gid=olivares,vers=3.1.1,nodev,nosuid,soft,echo_interval=5",
		"TimeoutSec=30",
	} {
		if !strings.Contains(mount, want) {
			t.Errorf("mount unit lacks %q:\n%s", want, mount)
		}
	}
	for _, want := range []string{"[Automount]", "Where=" + filepath.Join(f.MountRoot, "files"), "TimeoutIdleSec=0", "WantedBy=remote-fs.target"} {
		if !strings.Contains(automount, want) {
			t.Errorf("automount unit lacks %q:\n%s", want, automount)
		}
	}
	if got := read(t, cred); got != "username=svc-olivares\npassword="+password+"\n" {
		t.Errorf("credential file = %q", got)
	}
	if info, _ := os.Stat(cred); info.Mode().Perm() != 0o600 {
		t.Errorf("credential file mode = %v, want 0600", info.Mode().Perm())
	}
	for _, text := range []string{mount, automount} {
		if strings.Contains(text, password) {
			t.Error("a unit contains the password")
		}
	}
	for _, call := range f.calls {
		if strings.Contains(strings.Join(call, " "), password) {
			t.Errorf("systemctl was called with the password: %v", call)
		}
	}
	want := [][]string{{"daemon-reload"}, {"enable", "--now", unit + ".automount"}}
	if len(f.calls) != 2 || strings.Join(f.calls[0], " ") != "daemon-reload" || strings.Join(f.calls[1], " ") != "enable --now "+unit+".automount" {
		t.Errorf("systemctl calls = %v, want %v", f.calls, want)
	}
	if info, err := os.Stat(filepath.Join(f.MountRoot, "files")); err != nil || !info.IsDir() {
		t.Errorf("mount point not created: %v", err)
	}
}

func TestInstallNFSNeedsNoCredential(t *testing.T) {
	f := newFakeHost(t)
	spec, _ := Parse("data", "fileserver:/export")
	if err := f.Install(spec, "", ""); err != nil {
		t.Fatal(err)
	}
	unit := strings.ReplaceAll(strings.Trim(f.MountRoot, "/"), "/", "-") + "-data"
	mount := read(t, filepath.Join(f.UnitDir, unit+".mount"))
	for _, want := range []string{"What=fileserver:/export", "Type=nfs4", "Options=nodev,nosuid,vers=4.2,soft,timeo=50,retrans=2"} {
		if !strings.Contains(mount, want) {
			t.Errorf("mount unit lacks %q:\n%s", want, mount)
		}
	}
	if _, err := os.Stat(filepath.Join(f.CredDir, "data.cred")); !os.IsNotExist(err) {
		t.Errorf("an NFS folder wrote a credential file: %v", err)
	}
}

func TestInstallRefusals(t *testing.T) {
	smb, _ := Parse("files", "//fileserver/projects")
	f := newFakeHost(t)
	if err := f.Install(smb, "", "pw"); err == nil {
		t.Error("an SMB folder without a user was installed")
	}
	if err := f.Install(smb, "svc", ""); err == nil {
		t.Error("an SMB folder without a password was installed")
	}
	if err := f.Install(smb, "svc\nx", "pw"); err == nil {
		t.Error("a user with a newline was installed")
	}
	if len(f.calls) != 0 {
		t.Errorf("a refused install called systemctl: %v", f.calls)
	}
	if err := f.Install(smb, "svc", "pw"); err != nil {
		t.Fatal(err)
	}
	if err := f.Install(smb, "other", "pw2"); err == nil {
		t.Error("an existing folder name was overwritten")
	}
	if got := read(t, filepath.Join(f.CredDir, "files.cred")); !strings.Contains(got, "username=svc\n") {
		t.Errorf("the refused second install changed the credential: %q", got)
	}
}

func TestInstallLabelsTheMountForSELinux(t *testing.T) {
	f := newFakeHost(t)
	f.SELinuxContext = "system_u:object_r:olivares_workspace_t:s0"
	spec, _ := Parse("data", "fileserver:/export")
	if err := f.Install(spec, "", ""); err != nil {
		t.Fatal(err)
	}
	unit := strings.ReplaceAll(strings.Trim(f.MountRoot, "/"), "/", "-") + "-data"
	if mount := read(t, filepath.Join(f.UnitDir, unit+".mount")); !strings.Contains(mount, ",context=system_u:object_r:olivares_workspace_t:s0\n") {
		t.Errorf("mount unit has no SELinux context:\n%s", mount)
	}
}

func TestListShowsTheStateOfEachFolder(t *testing.T) {
	f := newFakeHost(t)
	smb, _ := Parse("files", "//fileserver/projects")
	nfs, _ := Parse("data", "fileserver:/export")
	if err := f.Install(smb, "svc", "pw"); err != nil {
		t.Fatal(err)
	}
	if err := f.Install(nfs, "", ""); err != nil {
		t.Fatal(err)
	}
	prefix := strings.ReplaceAll(strings.Trim(f.MountRoot, "/"), "/", "-") + "-"
	// A unit that is not ours stays out of the list.
	if err := os.WriteFile(filepath.Join(f.UnitDir, "mnt-other.automount"), []byte("[Automount]\nWhere=/mnt/other\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f.active[prefix+"files.mount"] = true
	f.alive = false
	got, err := f.List()
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, e := range got {
		states[e.Name] = e.State
	}
	if len(got) != 2 || states["files"] != StateNotResponding || states["data"] != StateUnmounted {
		t.Errorf("List = %+v, want files %q and data %q only", got, StateNotResponding, StateUnmounted)
	}
	f.alive = true
	got, _ = f.List()
	for _, e := range got {
		if e.Name == "files" && (e.State != StateMounted || e.What != "//fileserver/projects" || e.Where != filepath.Join(f.MountRoot, "files")) {
			t.Errorf("files entry = %+v", e)
		}
	}
}
