// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// itemReadHost answers the GitHub calls reading an item makes: the installation
// token mint, the issue read and the revocation.
type itemReadHost struct {
	mu   sync.Mutex
	reqs []gitReadRequest
}

func (h *itemReadHost) Do(r *http.Request) (*http.Response, error) {
	req := gitReadRequest{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization")}
	if r.Body != nil {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &req.body)
	}
	h.mu.Lock()
	h.reqs = append(h.reqs, req)
	h.mu.Unlock()
	status, body := http.StatusNotFound, `{}`
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/app/installations/34/access_tokens":
		status, body = http.StatusCreated, `{"token":"`+gitReadTestToken+`","expires_at":"2026-10-06T12:00:00Z","repositories":[{"name":"widgets"}]}`
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/issues/17":
		status, body = http.StatusOK, `{"number":17,"title":"Crash on save","body":"Steps:\n1. save","html_url":"https://github.com/acme/widgets/issues/17"}`
	case r.Method == http.MethodDelete && r.URL.Path == "/installation/token":
		status, body = http.StatusNoContent, ``
	}
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader([]byte(body))), Request: r}, nil
}

func (h *itemReadHost) requests() []gitReadRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]gitReadRequest(nil), h.reqs...)
}

// An item is read with a token that can read issues and pull requests and
// nothing else, for the one repository, and the token is revoked after.
func TestSessionItemReadMintsItemReadOnlyAndRevokes(t *testing.T) {
	ctx := context.Background()
	c, secrets, inits := newTestGitpublishCustody(t, gitpublishTestSource("gh", "github", githubPublicationConfig()))
	secrets.values["git-host/acme-app"] = gitReadAppKey(t)
	host := &itemReadHost{}
	c.doer = host

	item, err := c.ReadSessionItem(ctx, gitpublishTestTenant, gitpublishTestWorkspace, "acme", "widgets", 17)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := sessions.SourceItem{Number: 17, Title: "Crash on save", Body: "Steps:\n1. save", URL: "https://github.com/acme/widgets/issues/17"}
	if item != want {
		t.Fatalf("item = %+v; want %+v", item, want)
	}
	reqs := host.requests()
	if len(reqs) != 3 || reqs[0].method != http.MethodPost || reqs[1].method != http.MethodGet || reqs[2].method != http.MethodDelete {
		t.Fatalf("requests = %+v; want mint, read, revoke", reqs)
	}
	perms, _ := reqs[0].body["permissions"].(map[string]any)
	if len(perms) != 2 || perms["issues"] != "read" || perms["pull_requests"] != "read" {
		t.Fatalf("minted permissions = %v; want issues and pull_requests, read only", perms)
	}
	if reqs[1].auth != "Bearer "+gitReadTestToken || reqs[2].auth != "Bearer "+gitReadTestToken {
		t.Fatalf("read and revoke used %q and %q; want the minted token", reqs[1].auth, reqs[2].auth)
	}
	if len(inits.paths) != 0 {
		t.Fatalf("reading an item created server repositories %v", inits.paths)
	}
}

// A repository no approved row lists for this tenant and workspace is not read,
// and no secret is opened for it.
func TestSessionItemReadRefusesWhatNoBindingApproves(t *testing.T) {
	ctx := context.Background()
	narrowed := githubPublicationConfig()
	narrowed[gp.PublicationWorkspacesKey] = "ws-2"
	gitlab := gitpublishTestSource("gl", "gitlab",
		map[string]string{"group": "acme", gp.PublicationCredentialKey: "store:git-host/acme-bot", gp.PublicationRepositoriesKey: "acme/widgets"})
	cases := map[string]struct {
		defs   []model.SourceDef
		tenant model.TenantID
		repo   string
	}{
		"repository not approved": {[]model.SourceDef{gitpublishTestSource("gh", "github", githubPublicationConfig())}, gitpublishTestTenant, "secret"},
		"another workspace's row": {[]model.SourceDef{gitpublishTestSource("gh", "github", narrowed)}, gitpublishTestTenant, "widgets"},
		"another tenant":          {[]model.SourceDef{gitpublishTestSource("gh", "github", githubPublicationConfig())}, model.TenantID("tenant-b"), "widgets"},
		"a GitLab row only":       {[]model.SourceDef{gitlab}, gitpublishTestTenant, "widgets"},
		"no rows":                 {nil, gitpublishTestTenant, "widgets"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c, secrets, _ := newTestGitpublishCustody(t, tc.defs...)
			host := &itemReadHost{}
			c.doer = host
			if _, err := c.ReadSessionItem(ctx, tc.tenant, gitpublishTestWorkspace, "acme", tc.repo, 17); !errors.Is(err, sessions.ErrGitReadNotApproved) {
				t.Fatalf("read = %v; want ErrGitReadNotApproved", err)
			}
			if len(secrets.reads) != 0 || len(host.requests()) != 0 {
				t.Fatalf("secret reads %v, host requests %d; want none", secrets.reads, len(host.requests()))
			}
		})
	}
}

// An issue the repository does not have is its own answer, and the token is
// revoked all the same.
func TestSessionItemReadNamesAMissingItemAndStillRevokes(t *testing.T) {
	ctx := context.Background()
	c, secrets, _ := newTestGitpublishCustody(t, gitpublishTestSource("gh", "github", githubPublicationConfig()))
	secrets.values["git-host/acme-app"] = gitReadAppKey(t)
	host := &itemReadHost{}
	c.doer = host

	if _, err := c.ReadSessionItem(ctx, gitpublishTestTenant, gitpublishTestWorkspace, "acme", "widgets", 99); !errors.Is(err, sessions.ErrSourceItemNotFound) {
		t.Fatalf("read = %v; want ErrSourceItemNotFound", err)
	}
	reqs := host.requests()
	if len(reqs) != 3 || reqs[2].method != http.MethodDelete {
		t.Fatalf("requests = %+v; want the token revoked after the failed read", reqs)
	}
}
