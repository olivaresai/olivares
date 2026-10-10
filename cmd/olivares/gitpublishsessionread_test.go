// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

const gitReadTestToken = "ghs_SESSIONREAD_canary_0123456789"

// gitReadHost answers the two GitHub calls a session read credential makes:
// the installation token mint and its revocation.
type gitReadHost struct {
	mu   sync.Mutex
	reqs []gitReadRequest
}

type gitReadRequest struct {
	method, path, auth string
	body               map[string]any
}

func (h *gitReadHost) Do(r *http.Request) (*http.Response, error) {
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
	case r.Method == http.MethodDelete && r.URL.Path == "/installation/token":
		status, body = http.StatusNoContent, ``
	}
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader([]byte(body))), Request: r}, nil
}

func (h *gitReadHost) requests() []gitReadRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]gitReadRequest(nil), h.reqs...)
}

func gitReadAppKey(t *testing.T) string {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
}

// A session's read credential is a contents:read installation token for the one
// bound repository, minted and revoked through the adapter; the App key stays.
func TestSessionGitReadMintsContentsReadOnlyAndRevokes(t *testing.T) {
	ctx := context.Background()
	c, secrets, inits := newTestGitpublishCustody(t, gitpublishTestSource("gh", "github", githubPublicationConfig()))
	key := gitReadAppKey(t)
	secrets.values["git-host/acme-app"] = key
	host := &gitReadHost{}
	c.doer = host

	cred, err := c.MintSessionGitRead(ctx, gitpublishTestTenant, gitpublishTestWorkspace, "gh:acme/widgets")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if cred.RepoURL != "https://github.com/acme/widgets.git" || cred.Token != gitReadTestToken ||
		!cred.ExpiresAt.Equal(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("credential = %q expires %v", cred.RepoURL, cred.ExpiresAt)
	}
	if strings.Contains(cred.Token, "PRIVATE KEY") || cred.Token == key {
		t.Fatal("the App key reached the session credential")
	}
	reqs := host.requests()
	if len(reqs) != 1 || reqs[0].method != http.MethodPost {
		t.Fatalf("requests = %+v, want one mint", reqs)
	}
	perms, _ := reqs[0].body["permissions"].(map[string]any)
	if len(perms) != 1 || perms["contents"] != "read" {
		t.Fatalf("minted permissions = %v, want contents:read only", perms)
	}
	if repos, _ := reqs[0].body["repositories"].([]any); len(repos) != 1 || repos[0] != "widgets" {
		t.Fatalf("minted repositories = %v, want [widgets]", reqs[0].body["repositories"])
	}
	if len(inits.paths) != 0 {
		t.Fatalf("a read credential created server repositories %v", inits.paths)
	}

	if err := cred.Release(ctx); err != nil {
		t.Fatalf("release: %v", err)
	}
	reqs = host.requests()
	if len(reqs) != 2 || reqs[1].method != http.MethodDelete || reqs[1].path != "/installation/token" ||
		reqs[1].auth != "Bearer "+gitReadTestToken {
		t.Fatalf("release requests = %+v, want DELETE /installation/token with the session token", reqs)
	}
}

// A GitLab binding gives a session nothing: the shared bot token is not even read.
func TestSessionGitReadGivesAGitLabSessionNoCredential(t *testing.T) {
	ctx := context.Background()
	c, secrets, _ := newTestGitpublishCustody(t, gitpublishTestSource("gl", "gitlab",
		map[string]string{"group": "acme", gp.PublicationCredentialKey: "store:git-host/acme-bot", gp.PublicationRepositoriesKey: "acme/tools"}))
	host := &gitReadHost{}
	c.doer = host

	cred, err := c.MintSessionGitRead(ctx, gitpublishTestTenant, gitpublishTestWorkspace, "gl:acme/tools")
	if !errors.Is(err, sessions.ErrGitReadUnsupported) {
		t.Fatalf("gitlab mint = %v, want ErrGitReadUnsupported", err)
	}
	if cred.Token != "" || cred.Release != nil {
		t.Fatalf("a GitLab session received a credential: %+v", cred)
	}
	if len(secrets.reads) != 0 || len(host.requests()) != 0 {
		t.Fatalf("secret reads %v, host requests %d; want none", secrets.reads, len(host.requests()))
	}
}

func TestSessionGitReadRefusesUnapprovedBindings(t *testing.T) {
	ctx := context.Background()
	narrowed := githubPublicationConfig()
	narrowed[gp.PublicationWorkspacesKey] = "ws-2"
	foreignOwner := githubPublicationConfig()
	foreignOwner[gp.PublicationRepositoriesKey] = "other/widgets"
	cases := map[string]struct {
		config map[string]string
		id     string
	}{
		"no separator":              {githubPublicationConfig(), "gh"},
		"unknown binding":           {githubPublicationConfig(), "nope:acme/widgets"},
		"repository not approved":   {githubPublicationConfig(), "gh:acme/secret"},
		"another workspace's row":   {narrowed, "gh:acme/widgets"},
		"owner outside the binding": {foreignOwner, "gh:other/widgets"},
		"path traversal":            {githubPublicationConfig(), "gh:acme/../widgets"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c, secrets, _ := newTestGitpublishCustody(t, gitpublishTestSource("gh", "github", tc.config))
			host := &gitReadHost{}
			c.doer = host
			if _, err := c.MintSessionGitRead(ctx, gitpublishTestTenant, gitpublishTestWorkspace, tc.id); !errors.Is(err, sessions.ErrGitReadNotApproved) {
				t.Fatalf("mint %q = %v, want ErrGitReadNotApproved", tc.id, err)
			}
			if len(secrets.reads) != 0 || len(host.requests()) != 0 {
				t.Fatalf("secret reads %v, host requests %d; want none", secrets.reads, len(host.requests()))
			}
		})
	}
	// A row of another tenant is not approved either.
	c, _, _ := newTestGitpublishCustody(t, gitpublishTestSource("gh", "github", githubPublicationConfig()))
	if _, err := c.MintSessionGitRead(ctx, model.TenantID("tenant-b"), gitpublishTestWorkspace, "gh:acme/widgets"); !errors.Is(err, sessions.ErrGitReadNotApproved) {
		t.Fatalf("foreign tenant = %v, want ErrGitReadNotApproved", err)
	}
}

// An approval opened for a launch without git_read cannot be spent on one with it,
// and the approver reads which repository the session will read.
func TestSessionLaunchPlanBindsGitRead(t *testing.T) {
	base := sessions.LaunchIntent{Action: sessions.LaunchActionCreate, RunRef: "r1", PermissionMode: "default"}
	with := base
	with.GitRead = "gh:acme/widgets"
	if sessionLaunchPlanHash(base) == sessionLaunchPlanHash(with) {
		t.Fatal("the plan hash does not bind git_read")
	}
	other := with
	other.GitRead = "gh:acme/gears"
	if sessionLaunchPlanHash(with) == sessionLaunchPlanHash(other) {
		t.Fatal("the plan hash does not bind which repository")
	}
	if got := describeLaunch(with); !strings.Contains(got, "reads GitHub repository gh:acme/widgets") {
		t.Fatalf("approval receipt = %q, want the repository named", got)
	}
	if strings.Contains(describeLaunch(base), "GitHub") {
		t.Fatalf("a launch without git_read is described with one: %q", describeLaunch(base))
	}
}
