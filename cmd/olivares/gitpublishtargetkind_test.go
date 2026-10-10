// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"testing"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/modules/gitpublish"
	"github.com/olivaresai/olivares/modules/sessions"
)

// Binding admission deliberately precedes endpoint and adapter validation.
// An invalid endpoint still permits a credential binding; an incomplete App
// still permits its repository binding. Moving kind facts must keep those stages.
func TestPublicationKindCustodyValidationStages(t *testing.T) {
	for _, tc := range []struct {
		name, kind, ownerKey, owner string
	}{
		{"GitHub", "github", "org", " Acme "},
		{"GitLab", "gitlab", "group", " Acme/sub "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := githubPublicationConfig()
			cfg[tc.ownerKey] = tc.owner
			cfg[gp.PublicationRepositoriesKey] = "Acme/sub/Tools"
			cfg["api_base"] = "http://host.example"
			c, secrets, _ := newTestGitpublishCustody(t, gitpublishTestSource("row", tc.kind, cfg))
			cb, err := c.CredentialBinding(context.Background(), gitpublishTestTenant, gitpublishTestWorkspace, "row")
			if err != nil || len(cb.AllowedOwners) != 1 || cb.AllowedOwners[0] != tc.owner[1:len(tc.owner)-1] {
				t.Fatalf("credential admission = %+v, %v", cb, err)
			}
			_, err = c.RepositoryBinding(context.Background(), gitpublishTestTenant, gitpublishTestWorkspace, "row:Acme/sub/Tools")
			if !errors.Is(err, gitpublish.ErrBindingNotApproved) || !errors.Is(err, gp.ErrEndpoint) || len(secrets.reads) != 0 {
				t.Fatalf("endpoint validation = %v, secret reads %d", err, len(secrets.reads))
			}
			cfg["api_base"] = " https://HOST.example/api/v3/ "
			cb, err = c.CredentialBinding(context.Background(), gitpublishTestTenant, gitpublishTestWorkspace, "row")
			if err != nil {
				t.Fatal(err)
			}
			rb, err := c.RepositoryBinding(context.Background(), gitpublishTestTenant, gitpublishTestWorkspace, "row:Acme/sub/Tools")
			if err != nil || rb.RepoID != "host.example/acme/sub/tools" || rb.Owner != "Acme/sub" || rb.Name != "Tools" {
				t.Fatalf("repository identity = %+v, %v", rb, err)
			}
			if _, err := c.RepositoryBinding(context.Background(), gitpublishTestTenant, gitpublishTestWorkspace, "row:acme/sub/tools"); !errors.Is(err, gitpublish.ErrBindingNotApproved) {
				t.Fatalf("repository list case sensitivity = %v", err)
			}
			delete(cfg, "app_id")
			host, err := c.OpenHost(context.Background(), gitpublishTestTenant, cb, rb)
			if tc.kind == "github" && err == nil {
				t.Fatalf("incomplete App = %T, %v", host, err)
			}
			if tc.kind == "gitlab" && err != nil {
				t.Fatalf("bot adapter = %v", err)
			}
			if len(secrets.reads) != 1 {
				t.Fatalf("adapter stage secret reads = %d", len(secrets.reads))
			}
		})
	}
}

func TestPublicationKindPlainSessionReadIsUnsupportedBeforeSecrets(t *testing.T) {
	c, secrets, _ := newPlainCustody(t, plainGitRow())
	cred, err := c.MintSessionGitRead(context.Background(), gitpublishTestTenant, gitpublishTestWorkspace, "gt:srv/git/tools.git")
	if !errors.Is(err, sessions.ErrGitReadUnsupported) || cred.Token != "" || cred.Release != nil || len(secrets.reads) != 0 {
		t.Fatalf("plain session read = %v, credential present %v, secret reads %d", err, cred.Token != "", len(secrets.reads))
	}
}
