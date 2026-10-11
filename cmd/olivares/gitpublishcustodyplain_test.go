// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7 are disclaimers of warranty: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/modules/gitpublish"
)

// newPlainCustody builds a custody with one git-kind row and a real closed
// executor, so OpenHost opens the plain-git adapter itself.
func newPlainCustody(t *testing.T, config map[string]string) (*gitpublishCustody, *fakeGitpublishSecrets, *fakeGitpublishInit) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	abs, err := filepath.Abs(git)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	x, err := gp.NewExecutor(abs, home)
	if err != nil {
		t.Fatal(err)
	}
	def := gitpublishTestSource("gt", "git", config)
	secrets := &fakeGitpublishSecrets{values: map[string]string{"git-host/deploy-key": "-----BEGIN " + "OPENSSH PRIVATE KEY-----"}}
	inits := &fakeGitpublishInit{}
	root := filepath.Join(dir, "repositories")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return &gitpublishCustody{sources: fakeGitpublishSources{"gt": def}, secrets: secrets, repos: inits, exec: x, root: root, doer: gp.NewHTTPClient(gitpublishHTTPTimeout)}, secrets, inits
}

func plainGitRow() map[string]string {
	return map[string]string{
		gp.PublicationRemoteKey:     "ssh://git@lan.example:2222/srv/git/tools.git",
		gp.PublicationCredentialKey: "store:git-host/deploy-key",
	}
}

func TestGitpublishCustodySelectsPlainGitBindings(t *testing.T) {
	ctx := context.Background()
	c, secrets, inits := newPlainCustody(t, plainGitRow())

	cb, err := c.CredentialBinding(ctx, gitpublishTestTenant, gitpublishTestWorkspace, "gt")
	if err != nil || cb.ID != "gt" || cb.Host != "git" || len(cb.AllowedOwners) != 1 || cb.AllowedOwners[0] != "srv/git" {
		t.Fatalf("credential binding = %+v %v", cb, err)
	}
	rb, err := c.RepositoryBinding(ctx, gitpublishTestTenant, gitpublishTestWorkspace, "gt:srv/git/tools.git")
	if err != nil || rb.ID != "gt:srv/git/tools.git" || rb.Owner != "srv/git" || rb.Name != "tools.git" || rb.RepoID != "lan.example/srv/git/tools.git" {
		t.Fatalf("repository binding = %+v %v", rb, err)
	}
	if filepath.Dir(rb.LocalPath) != c.root {
		t.Fatalf("server repository %q is not engine-owned under %q", rb.LocalPath, c.root)
	}
	if len(inits.paths) != 1 || inits.paths[0] != rb.LocalPath {
		t.Fatalf("server repositories created = %v, want %s once", inits.paths, rb.LocalPath)
	}
	host, err := c.OpenHost(ctx, gitpublishTestTenant, cb, rb)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := host.(*gp.PlainGit); !ok {
		t.Fatalf("plain-git host = %T", host)
	}
	if len(secrets.reads) != 1 || secrets.reads[0] != auth.GlobalSecretScope.String()+"|git-host/deploy-key" {
		t.Fatalf("secret reads = %v, want only the deployment-scope git-host credential", secrets.reads)
	}
	// Only the remote's own path is that row's repository.
	if _, err := c.RepositoryBinding(ctx, gitpublishTestTenant, gitpublishTestWorkspace, "gt:srv/git/other.git"); !errors.Is(err, gitpublish.ErrBindingNotApproved) {
		t.Fatalf("another repository = %v, want ErrBindingNotApproved", err)
	}
}

func TestGitpublishCustodySelectsHTTPSPlainGitBindings(t *testing.T) {
	ctx := context.Background()
	c, _, _ := newPlainCustody(t, map[string]string{
		gp.PublicationRemoteKey:     "https://git.example.com/acme/tools.git",
		gp.PublicationCredentialKey: "store:git-host/deploy-key",
	})
	cb, err := c.CredentialBinding(ctx, gitpublishTestTenant, gitpublishTestWorkspace, "gt")
	if err != nil || cb.AllowedOwners[0] != "acme" {
		t.Fatalf("credential binding = %+v %v", cb, err)
	}
	rb, err := c.RepositoryBinding(ctx, gitpublishTestTenant, gitpublishTestWorkspace, "gt:acme/tools.git")
	if err != nil || rb.RepoID != "git.example.com/acme/tools.git" || rb.Owner != "acme" || rb.Name != "tools.git" {
		t.Fatalf("repository binding = %+v %v", rb, err)
	}
}

func TestGitpublishCustodyRefusesAMovedPlainGitPin(t *testing.T) {
	ctx := context.Background()
	c, _, _ := newPlainCustody(t, plainGitRow())
	cb, err := c.CredentialBinding(ctx, gitpublishTestTenant, gitpublishTestWorkspace, "gt")
	if err != nil {
		t.Fatal(err)
	}
	rb, err := c.RepositoryBinding(ctx, gitpublishTestTenant, gitpublishTestWorkspace, "gt:srv/git/tools.git")
	if err != nil {
		t.Fatal(err)
	}
	// The row moved after admission: a new version and a new remote must
	// refuse the pinned pair, not open the new destination.
	def := c.sources.(fakeGitpublishSources)["gt"]
	def.Version = 4
	def.Config[gp.PublicationRemoteKey] = "ssh://git@lan.example:2222/srv/git/other.git"
	c.sources.(fakeGitpublishSources)["gt"] = def
	if _, err := c.OpenHost(ctx, gitpublishTestTenant, cb, rb); !errors.Is(err, errGitpublishBindingChanged) {
		t.Fatalf("moved pin = %v, want errGitpublishBindingChanged", err)
	}
}

func TestGitpublishCustodyRefusesBadPlainGitRemotes(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct{ name, remote string }{
		{"missing", ""},
		{"not git", "github://acme/tools"},
		{"http", "http://git.example.com/acme/tools.git"},
		{"file", "file:///srv/git/tools.git"},
		{"ssh without user", "ssh://lan.example/srv/git/tools.git"},
		{"ssh with password", "ssh://git:pw@lan.example/srv/git/tools.git"},
		{"query", "ssh://git@lan.example/srv/git/tools.git?x=1"},
		{"https ip literal", "https://10.0.0.7/acme/tools.git"},
		{"https other port", "https://git.example.com:8443/acme/tools.git"},
	} {
		t.Run(c.name, func(t *testing.T) {
			config := plainGitRow()
			config[gp.PublicationRemoteKey] = c.remote
			custody, secrets, _ := newPlainCustody(t, config)
			if _, err := custody.CredentialBinding(ctx, gitpublishTestTenant, gitpublishTestWorkspace, "gt"); !errors.Is(err, gitpublish.ErrBindingNotApproved) {
				t.Fatalf("credential binding err = %v, want ErrBindingNotApproved", err)
			}
			if _, err := custody.RepositoryBinding(ctx, gitpublishTestTenant, gitpublishTestWorkspace, "gt:srv/git/tools.git"); !errors.Is(err, gitpublish.ErrBindingNotApproved) {
				t.Fatalf("repository binding err = %v, want ErrBindingNotApproved", err)
			}
			if len(secrets.reads) != 0 {
				t.Fatalf("a refused row read secrets %v", secrets.reads)
			}
		})
	}
}
