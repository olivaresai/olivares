// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package localinstall

import (
	"encoding/json"
	"errors"
	"os/user"
	"testing"
)

func freshImageInput() ServiceAccountInput {
	return ServiceAccountInput{
		ImageProfile:  &ImageProfile{Content: []byte("service_account=olivares-svc\n"), Regular: true, Mode: 0644},
		PackageFormat: "rpm", PackageArgument: "1",
		DataDir:      DataDirectoryState{Exists: true, Empty: true},
		ImageAccount: AccountAbsent,
	}
}

func TestServiceAccountDecisionRows(t *testing.T) {
	cases := []struct {
		name  string
		input ServiceAccountInput
		want  ServiceAccountChoice
	}{
		{"split without declaration", ServiceAccountInput{Manifest: imageManifest()}, ServiceAccountChoice{"olivares-svc", "olivares-svc", "image-split"}},
		{"legacy without declaration", ServiceAccountInput{Manifest: legacyManifest()}, ServiceAccountChoice{"olivares", "olivares", "legacy"}},
		{"new image", freshImageInput(), ServiceAccountChoice{"olivares-svc", "olivares-svc", "image-new"}},
		{"non-image", ServiceAccountInput{}, ServiceAccountChoice{"olivares", "olivares", "non-image"}},
	}
	split := freshImageInput()
	split.Manifest = imageManifest()
	split.PackageArgument = "2"
	split.DataDir.Empty = false
	split.DataDir.UID, split.DataDir.GID = 987, 986
	split.ImageAccount = AccountPresent
	cases = append(cases, struct {
		name  string
		input ServiceAccountInput
		want  ServiceAccountChoice
	}{
		"split with declaration is authoritative", split, ServiceAccountChoice{"olivares-svc", "olivares-svc", "image-split"},
	})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SelectServiceAccount(tc.input)
			if err != nil || got != tc.want {
				t.Fatalf("choice = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
	for _, format := range []string{"deb", "rpm", "apk"} {
		got, err := SelectServiceAccount(ServiceAccountInput{PackageFormat: format, PackageArgument: "upgrade"})
		if err != nil || got.User != "olivares" || got.Class != "non-image" {
			t.Fatalf("non-image %s changed: %+v, %v", format, got, err)
		}
	}
}

func TestServiceAccountAmbiguousImageStates(t *testing.T) {
	cases := []struct {
		name   string
		change func(*ServiceAccountInput)
	}{
		{"user manifest", func(in *ServiceAccountInput) { in.Manifest = &Manifest{Mode: "user"} }},
		{"launchd manifest", func(in *ServiceAccountInput) { in.Manifest = launchdAccountManifest() }},
		{"malformed manifest", func(in *ServiceAccountInput) { in.Manifest = &Manifest{} }},
		{"legacy manifest", func(in *ServiceAccountInput) { in.Manifest = legacyManifest() }},
		{"unknown manifest identity", func(in *ServiceAccountInput) { in.Manifest = legacyManifest(); in.Manifest.Account.User = "alice" }},
		{"rpm upgrade", func(in *ServiceAccountInput) { in.PackageArgument = "2" }},
		{"rpm erased upgrade", func(in *ServiceAccountInput) { in.PackageArgument = "0" }},
		{"rpm missing argument", func(in *ServiceAccountInput) { in.PackageArgument = "" }},
		{"rpm malformed argument", func(in *ServiceAccountInput) { in.PackageArgument = "01" }},
		{"deb configure", func(in *ServiceAccountInput) { in.PackageFormat = "deb"; in.PackageArgument = "configure" }},
		{"apk post-install", func(in *ServiceAccountInput) { in.PackageFormat = "apk"; in.PackageArgument = "post-install" }},
		{"unknown package", func(in *ServiceAccountInput) { in.PackageFormat = "" }},
		{"nonempty data", func(in *ServiceAccountInput) { in.DataDir.Empty = false }},
		{"nonroot data user", func(in *ServiceAccountInput) { in.DataDir.UID = 1000 }},
		{"nonroot data group", func(in *ServiceAccountInput) { in.DataDir.GID = 1000 }},
		{"missing packaged data", func(in *ServiceAccountInput) { in.DataDir.Exists = false }},
		{"preexisting service identity", func(in *ServiceAccountInput) { in.ImageAccount = AccountPresent }},
		{"unmeasured service identity", func(in *ServiceAccountInput) { in.ImageAccount = AccountUnknown }},
		{"unknown presence value", func(in *ServiceAccountInput) { in.ImageAccount = "unexpected" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := freshImageInput()
			tc.change(&in)
			got, err := SelectServiceAccount(in)
			if !errors.Is(err, ErrAmbiguousAccount) || got != (ServiceAccountChoice{}) {
				t.Fatalf("choice = %+v, error = %v; want AMBIGUOUS_ACCOUNT and no identity", got, err)
			}
		})
	}
}

func TestServiceAccountInvalidDeclarationAlwaysRefuses(t *testing.T) {
	for _, content := range []string{"", "service_account=olivares-svc", "service_account=olivares\n", "service_account=olivares-svc\r\n", "service_account=olivares-svc\n\n", " service_account=olivares-svc\n", "service_account=olivares-svc\n# comment\n", "service_account=olivares-svc\x00\n"} {
		for _, m := range []*Manifest{nil, legacyManifest(), imageManifest()} {
			in := freshImageInput()
			in.Manifest = m
			in.ImageProfile.Content = []byte(content)
			got, err := SelectServiceAccount(in)
			if !errors.Is(err, ErrImageProfileInvalid) || got != (ServiceAccountChoice{}) {
				t.Fatalf("content %q, manifest %v: %+v, %v", content, m, got, err)
			}
		}
	}
	for _, change := range []func(*ImageProfile){
		func(p *ImageProfile) { p.Regular = false },
		func(p *ImageProfile) { p.UID = 1000 },
		func(p *ImageProfile) { p.GID = 1000 },
		func(p *ImageProfile) { p.Mode = 0664 },
	} {
		in := freshImageInput()
		change(in.ImageProfile)
		if _, err := SelectServiceAccount(in); !errors.Is(err, ErrImageProfileInvalid) {
			t.Fatalf("untrusted declaration: %v", err)
		}
	}
}

func TestServiceAccountPlanUsesOnlyRecordedIdentity(t *testing.T) {
	in := freshImageInput()
	in.Manifest = imageManifest()
	before, _ := json.Marshal(in)
	lookups := 0
	got, err := ServiceAccount(in, func(name, group string) (*user.User, *user.Group, error) {
		lookups++
		if name != "olivares-svc" || group != "olivares-svc" {
			t.Fatalf("human identity requested: %q:%q", name, group)
		}
		return &user.User{Username: "olivares-svc", Uid: "987", Gid: "986"}, &user.Group{Name: "olivares-svc", Gid: "986"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema":"olivares.ai/service-account/v1","user":"olivares-svc","group":"olivares-svc","uid":987,"gid":986,"class":"image-split"}`
	if string(body) != want {
		t.Fatalf("plan = %s; want %s", body, want)
	}
	after, _ := json.Marshal(in)
	if lookups != 1 || string(before) != string(after) {
		t.Fatalf("plan changed its input or repeated lookup: %d", lookups)
	}
}

func TestServiceAccountPlanRefusesUnvalidatedLookup(t *testing.T) {
	cases := []struct {
		name  string
		user  *user.User
		group *user.Group
		err   error
	}{
		{"human collision", &user.User{Username: "olivares", Uid: "1000", Gid: "1000"}, &user.Group{Name: "olivares", Gid: "1000"}, nil},
		{"wrong group", &user.User{Username: "olivares-svc", Uid: "987", Gid: "986"}, &user.Group{Name: "wheel", Gid: "986"}, nil},
		{"primary group mismatch", &user.User{Username: "olivares-svc", Uid: "987", Gid: "985"}, &user.Group{Name: "olivares-svc", Gid: "986"}, nil},
		{"missing user", nil, &user.Group{Name: "olivares-svc", Gid: "986"}, nil},
		{"missing group", &user.User{Username: "olivares-svc", Uid: "987", Gid: "986"}, nil, nil},
		{"lookup error", nil, nil, errors.New("directory unavailable")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ServiceAccount(ServiceAccountInput{Manifest: imageManifest()}, func(string, string) (*user.User, *user.Group, error) { return tc.user, tc.group, tc.err })
			if !errors.Is(err, ErrAmbiguousAccount) || got != (ServiceAccountPlan{}) {
				t.Fatalf("unsafe lookup: %+v, %v", got, err)
			}
		})
	}
	for _, id := range []string{"", "-1", "abc", "4294967296", "0"} {
		for _, field := range []string{"uid", "gid"} {
			u := &user.User{Username: "olivares-svc", Uid: "987", Gid: "986"}
			g := &user.Group{Name: "olivares-svc", Gid: "986"}
			if field == "uid" {
				u.Uid = id
			} else {
				u.Gid, g.Gid = id, id
			}
			if _, err := ServiceAccount(ServiceAccountInput{Manifest: imageManifest()}, func(string, string) (*user.User, *user.Group, error) { return u, g, nil }); !errors.Is(err, ErrAmbiguousAccount) {
				t.Fatalf("%s=%q accepted: %v", field, id, err)
			}
		}
	}
}

func TestServiceAccountPlanRequiresInstalledManifest(t *testing.T) {
	for _, in := range []ServiceAccountInput{{}, freshImageInput()} {
		_, err := ServiceAccount(in, func(string, string) (*user.User, *user.Group, error) {
			t.Fatal("lookup before manifest")
			return nil, nil, nil
		})
		if !errors.Is(err, ErrAmbiguousAccount) {
			t.Fatalf("uninstalled plan: %v", err)
		}
	}
	in := ServiceAccountInput{Manifest: legacyManifest()}
	got, err := ServiceAccount(in, func(name, group string) (*user.User, *user.Group, error) {
		if name != "olivares" || group != "olivares" {
			t.Fatal("legacy identity changed")
		}
		return &user.User{Username: name, Uid: "1001", Gid: "1002"}, &user.Group{Name: group, Gid: "1002"}, nil
	})
	if err != nil || got != (ServiceAccountPlan{Schema: "olivares.ai/service-account/v1", User: "olivares", Group: "olivares", UID: 1001, GID: 1002, Class: "legacy"}) {
		t.Fatalf("legacy plan: %+v, %v", got, err)
	}
}

func TestServiceAccountManifestRefusalBeforeLookup(t *testing.T) {
	for _, change := range []func(*Manifest){
		func(m *Manifest) { m.Schema = "unknown" },
		func(m *Manifest) { m.Account.User = "alice" },
		func(m *Manifest) { m.Account.Group = "olivares" },
		func(m *Manifest) { m.Files = m.Files[:3] },
		func(m *Manifest) { m.Files[3].Path = "/tmp/account.conf" },
	} {
		m := imageManifest()
		change(m)
		_, err := ServiceAccount(ServiceAccountInput{Manifest: m}, func(string, string) (*user.User, *user.Group, error) {
			t.Fatal("invalid manifest reached lookup")
			return nil, nil, nil
		})
		if !errors.Is(err, ErrAmbiguousAccount) {
			t.Fatalf("invalid manifest: %v", err)
		}
	}
	in := freshImageInput()
	in.ImageProfile.Content = []byte("invalid")
	in.Manifest = imageManifest()
	_, err := ServiceAccount(in, func(string, string) (*user.User, *user.Group, error) {
		t.Fatal("invalid declaration reached lookup")
		return nil, nil, nil
	})
	if !errors.Is(err, ErrImageProfileInvalid) {
		t.Fatalf("invalid declaration: %v", err)
	}
}

func TestServiceAccountLaunchdAndUserMode(t *testing.T) {
	got, err := ServiceAccount(ServiceAccountInput{Manifest: launchdAccountManifest()}, func(name, group string) (*user.User, *user.Group, error) {
		if name != "_olivares" || group != "staff" {
			t.Fatalf("launchd identity changed: %s:%s", name, group)
		}
		return &user.User{Username: name, Uid: "499", Gid: "20"}, &user.Group{Name: group, Gid: "20"}, nil
	})
	if err != nil || got.Class != "legacy" || got.User != "_olivares" {
		t.Fatalf("launchd: %+v, %v", got, err)
	}
	_, err = ServiceAccount(ServiceAccountInput{Manifest: &Manifest{Mode: "user"}}, func(string, string) (*user.User, *user.Group, error) {
		t.Fatal("user mode reached system lookup")
		return nil, nil, nil
	})
	if !errors.Is(err, ErrUserMode) {
		t.Fatalf("user plan: %v", err)
	}
}
