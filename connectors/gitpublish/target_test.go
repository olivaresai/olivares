// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import (
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestTargetKindFacts(t *testing.T) {
	for _, tc := range []struct {
		kind, owner, path, identity string
		cfg                         map[string]string
		ci                          []string
		narrow, changes, rebase     bool
	}{
		{"github", "Acme", "Acme/Tools", "api.github.com/acme/tools", map[string]string{"org": " Acme ", "publish_repositories": "Acme/Tools, acme/gears"}, []string{".github/workflows"}, true, true, true},
		{"gitlab", "Acme/sub", "Acme/sub/Tools", "gitlab.com/acme/sub/tools", map[string]string{"group": " Acme/sub ", "publish_repositories": "Acme/sub/Tools"}, []string{".gitlab-ci.yml"}, false, true, false},
		{"git", "srv/git", "srv/git/Tools.git", "lan.example/srv/git/tools.git", map[string]string{"remote": " ssh://git@LAN.example:2222/srv/git/Tools.git ", "publish_repositories": "ignored/other"}, []string{".github/workflows", ".gitlab-ci.yml"}, false, false, false},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			kind, ok := LookupTargetKind(tc.kind)
			if !ok {
				t.Fatal("unknown kind")
			}
			cfg := tc.cfg
			cfg["publish_credential"] = "store:git-host/key"
			cfg["publish_workspaces"] = " ws-1, , ws-2 "
			row, err := kind.Bind(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if row.Owner() != tc.owner || row.CredentialRef() != "store:git-host/key" || !row.AllowsWorkspace("ws-1") || row.AllowsWorkspace("ws-3") {
				t.Fatal("row facts")
			}
			repo, err := row.Repository(tc.path)
			if err != nil || repo.ID != tc.identity || repo.Owner+"/"+repo.Name != tc.path {
				t.Fatalf("repository = %+v, %v", repo, err)
			}
			if _, err := row.Repository("other/repo"); !errors.Is(err, ErrTargetRepository) {
				t.Fatalf("unlisted repository = %v", err)
			}
			facts := kind.Capabilities()
			if !reflect.DeepEqual(facts.CIPaths, tc.ci) || facts.NarrowRead != tc.narrow || !facts.Supports(EffectPush, "") || facts.Supports(EffectPullRequest, "") != tc.changes || facts.Supports(EffectMerge, "rebase") != tc.rebase {
				t.Fatalf("capabilities = %+v", facts)
			}
			facts.CIPaths[0] = "changed"
			if !reflect.DeepEqual(kind.Capabilities().CIPaths, tc.ci) {
				t.Fatal("caller changed kind facts")
			}
		})
	}
	if _, ok := LookupTargetKind("GitHub"); ok {
		t.Fatal("kind lookup changed case sensitivity")
	}
	if _, ok := LookupTargetKind("other"); ok {
		t.Fatal("unknown kind admitted")
	}
}

func TestTargetKindValidationStages(t *testing.T) {
	for _, name := range []string{"github", "gitlab"} {
		t.Run(name, func(t *testing.T) {
			kind, _ := LookupTargetKind(name)
			row, err := kind.Bind(map[string]string{"api_base": "http://host.example", "publish_repositories": "Acme/Tools"})
			if err != nil {
				t.Fatalf("endpoint checked during row binding: %v", err)
			}
			if row.Owner() != "" || !row.AllowsWorkspace("any") {
				t.Fatal("empty owner or workspace changed")
			}
			if _, err := row.Repository("other/repo"); !errors.Is(err, ErrTargetRepository) || errors.Is(err, ErrEndpoint) {
				t.Fatalf("list refusal precedence = %v", err)
			}
			if _, err := row.Repository("Acme/Tools"); !errors.Is(err, ErrEndpoint) {
				t.Fatalf("endpoint refusal = %v", err)
			}
		})
	}
	kind, _ := LookupTargetKind("git")
	if _, err := kind.Bind(map[string]string{"remote": "file:///srv/git/tools.git"}); !errors.Is(err, ErrEndpoint) {
		t.Fatalf("remote validation = %v", err)
	}
}

func TestTargetKindSelfHostedRepository(t *testing.T) {
	for _, name := range []string{"github", "gitlab"} {
		kind, _ := LookupTargetKind(name)
		row, err := kind.Bind(map[string]string{"api_base": " https://HOST.example/api/v3/ ", "publish_repositories": "Acme/Tools"})
		if err != nil {
			t.Fatal(err)
		}
		repo, err := row.Repository("Acme/Tools")
		if err != nil || repo.ID != "host.example/acme/tools" {
			t.Fatalf("%s repository = %+v, %v", name, repo, err)
		}
		if _, err := row.Repository("acme/tools"); !errors.Is(err, ErrTargetRepository) {
			t.Fatalf("%s case sensitivity = %v", name, err)
		}
	}
}

func TestTargetKindConstruction(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	git, err = filepath.Abs(git)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(git, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"github", "gitlab", "git"} {
		t.Run(name, func(t *testing.T) {
			kind, _ := LookupTargetKind(name)
			cfg := map[string]string{"org": "acme", "group": "acme", "app_id": " 12 ", "installation_id": " 34 ", "api_base": "https://HOST.example/api/v3", "publish_repositories": "acme/tools", "remote": "https://HOST.example/acme/tools"}
			row, err := kind.Bind(cfg)
			if err != nil {
				t.Fatal(err)
			}
			host, err := row.Open("acme/tools", NewSecret("test-user:test-credential"), HostDependencies{HTTP: NewHTTPClient(0), Git: executor})
			if err != nil {
				t.Fatal(err)
			}
			push, scheme, _ := host.PushTarget(Token{})
			want := "https://host.example/acme/tools.git"
			if name == "gitlab" {
				want = "https://host.example/api/v3/acme/tools.git"
			}
			if name == "git" {
				want = "https://host.example/acme/tools"
			}
			if !strings.EqualFold(push, want) || scheme != "https" {
				t.Fatalf("push target = %q, %q", push, scheme)
			}
			if _, err := row.Open("other/repo", NewSecret("test"), HostDependencies{HTTP: NewHTTPClient(0), Git: executor}); !errors.Is(err, ErrTargetRepository) {
				t.Fatalf("unapproved construction = %v", err)
			}
		})
	}
}
