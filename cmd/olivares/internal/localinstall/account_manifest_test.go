// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package localinstall

import "testing"

func imageManifest() *Manifest {
	m := legacyManifest()
	m.Account = Account{User: "olivares-svc", Group: "olivares-svc", UserCreated: true, GroupCreated: true}
	m.Files = append(m.Files, File{
		Path: "/etc/systemd/system/olivares.service.d/05-olivares-service-account.conf",
		Role: "account-dropin", Mode: "0644", Managed: true,
	})
	return m
}

func TestValidateImageAccountManifest(t *testing.T) {
	for _, unit := range []string{"/usr/lib/systemd/system/olivares.service", "/etc/systemd/system/olivares.service"} {
		t.Run(unit, func(t *testing.T) {
			m := imageManifest()
			m.Files[2].Path = unit
			if err := Validate(m, "/root"); err != nil {
				t.Fatalf("canonical image manifest refused: %v", err)
			}
			m.Files = append(m.Files, File{Path: unit + ".d/agentops.conf", Role: "dropin", Mode: "0644"})
			if err := Validate(m, "/root"); err != nil {
				t.Fatalf("independent AgentOps drop-in refused: %v", err)
			}
		})
	}
}

func TestValidateImageAccountManifestRefusals(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Manifest)
	}{
		{"missing account drop-in", func(m *Manifest) { m.Files = m.Files[:3] }},
		{"wrong account drop-in role", func(m *Manifest) { m.Files[3].Role = "dropin" }},
		{"vendor account drop-in path", func(m *Manifest) {
			m.Files[3].Path = "/usr/lib/systemd/system/olivares.service.d/05-olivares-service-account.conf"
		}},
		{"different basename", func(m *Manifest) { m.Files[3].Path = "/etc/systemd/system/olivares.service.d/99-account.conf" }},
		{"duplicate path", func(m *Manifest) { m.Files = append(m.Files, m.Files[3]) }},
		{"second account drop-in", func(m *Manifest) {
			m.Files = append(m.Files, File{Path: "/etc/systemd/system/olivares.service.d/06-account.conf", Role: "account-dropin"})
		}},
		{"different group", func(m *Manifest) { m.Account.Group = "olivares" }},
		{"arbitrary user", func(m *Manifest) { m.Account.User = "alice" }},
		{"legacy account with split drop-in", func(m *Manifest) { m.Account.User, m.Account.Group = "olivares", "olivares" }},
		{"openrc split", func(m *Manifest) { m.Init = "openrc"; m.Files[2].Path = "/etc/init.d/olivares" }},
		{"custom split data", func(m *Manifest) {
			m.Layout = "custom"
			m.DataDir = "/srv/olivares"
			m.ManifestPath = "/srv/olivares/install-manifest.json"
		}},
		{"wrong config tuple", func(m *Manifest) {
			m.Config = "/Library/Preferences/dev.olivares.olivares.env"
			m.Files[1].Path = m.Config
		}},
		{"user split", func(m *Manifest) { m.Mode = "user" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := imageManifest()
			tc.change(m)
			if err := Validate(m, "/root"); err == nil {
				t.Fatal("unsafe image manifest accepted")
			}
		})
	}
}

func TestValidateAccountCompatibility(t *testing.T) {
	m := legacyManifest()
	if err := Validate(m, "/root"); err != nil {
		t.Fatal(err)
	}
	m.Init = "openrc"
	m.Files[2].Path = "/etc/init.d/olivares"
	if err := Validate(m, "/root"); err != nil {
		t.Fatal(err)
	}
	m = launchdAccountManifest()
	if err := Validate(m, "/root"); err != nil {
		t.Fatal(err)
	}
	m.Account.GroupCreated = true
	if err := Validate(m, "/root"); err == nil {
		t.Fatal("launchd adopted staff")
	}
	m = &Manifest{
		Schema: ManifestSchema, Mode: "user", Init: "systemd", DataDir: "/home/alice/.local/share/olivares",
		Config: "/home/alice/.config/olivares/olivares.env", ManifestPath: "/home/alice/.local/share/olivares/install-manifest.json",
		Files: []File{{Path: "/home/alice/.local/bin/olivares", Role: "binary"}, {Path: "/home/alice/.config/olivares/olivares.env", Role: "config"}, {Path: "/home/alice/.config/systemd/user/olivares.service", Role: "unit"}},
	}
	if err := Validate(m, "/home/alice"); err != nil {
		t.Fatal(err)
	}
	m.Account = Account{User: "olivares-svc", Group: "olivares-svc"}
	if err := Validate(m, "/home/alice"); err == nil {
		t.Fatal("user install claimed system identity")
	}
}

func launchdAccountManifest() *Manifest {
	return &Manifest{
		Schema: ManifestSchema, Mode: "system", Init: "launchd", DataDir: "/Library/Application Support/Olivares",
		Config: "/Library/Preferences/dev.olivares.olivares.env", ManifestPath: "/Library/Application Support/Olivares/install-manifest.json",
		Account: Account{User: "_olivares", Group: "staff"},
		Files:   []File{{Path: "/usr/local/bin/olivares", Role: "binary"}, {Path: "/Library/Preferences/dev.olivares.olivares.env", Role: "config"}, {Path: "/Library/LaunchDaemons/dev.olivares.olivares.plist", Role: "unit"}, {Path: "/Library/Application Support/Olivares/launchd-run.sh", Role: "wrapper"}},
	}
}
