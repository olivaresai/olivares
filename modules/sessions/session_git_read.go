// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/olivaresai/olivares/core/model"
)

// A SESSION'S GITHUB READ CREDENTIAL.
//
// A launch may name ONE approved publication repository binding
// ("<binding id>:<owner>/<name>", the id gitpublish targets use) in git_read. The
// engine mints a GitHub App installation token for that repository with
// contents:read only (valid about one hour), writes it to a file under
// run/<run_ref> the child may read, and points git at it with a credential helper
// for that repository's URL. The child's environment carries the helper and the
// file path, never the token. The token is revoked when the launch ends, and the
// ledger's launch event names the binding and the expiry.
//
// The App key (the publication credential) never leaves the engine. A kind
// without a narrow read capability (GitLab or plain git) is refused. A launch
// whose network is confined to its provider (a record-bound tool measured behind
// the egress proxy) is refused too: the proxy denies the Git host. Who may: the
// tenant administration secret_env needs, asked again on resume. A tool home whose
// own git configuration stores credentials (credential.helper=store) keeps a copy
// of the token after a clone; the revocation at stop makes it inert.
//
// ponytail: the revocation runs when the launch's context ends; an engine killed
// or exiting first leaves the token (and its 0600 file under the engine's data
// directory) valid until GitHub expires it, within the hour. An unconfined child
// can read every file the engine user can, other runs' tokens included, as it can
// their secret_env values. Upgrade trigger: sessions that outlive a token or a
// deployment that runs sessions unconfined.

// SessionGitReadSource mints a session's read credential from the approved
// publication custody. The composition root binds it.
type SessionGitReadSource interface {
	MintSessionGitRead(ctx context.Context, tenant model.TenantID, workspace model.ID, binding string) (GitReadCredential, error)
}

// GitReadCredential is one minted installation token. Release revokes it.
type GitReadCredential struct {
	// RepoURL is the repository's https clone URL.
	RepoURL   string
	Token     string
	ExpiresAt time.Time
	Release   func(context.Context) error
}

var (
	// ErrGitReadNotApproved: the binding is not an approved repository in the
	// session's workspace.
	ErrGitReadNotApproved = errors.New("sessions: git_read binding not approved")
	// ErrGitReadUnsupported: the binding's host has no session read credential.
	ErrGitReadUnsupported = errors.New("sessions: git_read host unsupported")
)

const (
	// gitReadHostTimeout bounds the mint and the revocation, each one host call.
	gitReadHostTimeout = 30 * time.Second
	// gitReadMark names the read token where the output would have carried it.
	gitReadMark = "git_read"
	// gitReadRedactName keys the token in the output redactor; no variable has it.
	gitReadRedactName = "\x00git_read"
	// gitReadUsername is the user name GitHub takes with an installation token.
	gitReadUsername = "x-access-token"
)

// refuseGitReadFor checks a launch's git_read before anything durable.
func (m *Module) refuseGitReadFor(p CreateRunParams) error {
	if p.GitRead == "" {
		return nil
	}
	if len(p.GitRead) > 600 || !strings.Contains(p.GitRead, ":") || strings.IndexFunc(p.GitRead, unicode.IsSpace) >= 0 ||
		strings.IndexFunc(p.GitRead, unicode.IsControl) >= 0 {
		return badRequest("git_read is not a repository binding (<binding id>:<owner>/<name>)")
	}
	if !p.MayUseSecretEnv {
		return forbiddenErr("giving a session a repository read credential needs tenant administration")
	}
	if m.rt.GitRead == nil || !filepath.IsAbs(m.rt.GitReadDataDir) || strings.ContainsRune(m.rt.GitReadDataDir, '\'') {
		return &runErr{http.StatusServiceUnavailable,
			"this session names a git_read repository, and no Git-host custody is wired on this node (the launch is denied)"}
	}
	return validateGitReadEnv(p.EnvAllow, p.SecretEnv)
}

// configureGitRead mints the credential for this launch, writes it for the
// child and sets git's credential helper. The release runs when runCtx ends.
func (m *Module) configureGitRead(ctx, runCtx context.Context, tenant model.TenantID, runRef string, p *CreateRunParams, spec *LaunchSpec) error {
	if p.GitRead == "" {
		return nil
	}
	if err := m.refuseGitReadFor(*p); err != nil {
		return err
	}
	// A confined launch reaches only its provider and the engine's relays: git
	// could not use the token, so none is minted.
	if spec.NetworkPolicy != nil {
		return &runErr{http.StatusUnprocessableEntity,
			"git_read: this session's network reaches only its provider, and git_read needs its Git host; no credential was minted"}
	}
	// A failure of this node or the host is logged with its cause and answered
	// 503: the caller can do nothing about it but retry or tell the operator.
	unavailable := func(what string, cause error) error {
		m.warnf("sessions: the repository read credential "+what, "run_ref", runRef, "binding", p.GitRead, "err", redactErr(cause))
		return &runErr{http.StatusServiceUnavailable, "the repository read credential " + what + "; the launch is denied"}
	}
	if runRef == "" || filepath.Base(runRef) != runRef || strings.ContainsAny(runRef, "\\'\r\n\x00") {
		return unavailable("could not be prepared", errors.New("invalid run reference"))
	}
	rec, err := m.loadRun(ctx, tenant, runRef)
	if err != nil {
		return unavailable("could not be prepared", err)
	}
	mctx, cancel := context.WithTimeout(ctx, gitReadHostTimeout)
	cred, err := m.rt.GitRead.MintSessionGitRead(mctx, tenant, model.ID(rec.String(colRunAuthzWorkspaceID)), p.GitRead)
	cancel()
	switch {
	case errors.Is(err, ErrGitReadNotApproved):
		return &runErr{http.StatusUnprocessableEntity, fmt.Sprintf(
			"git_read: %s is not an approved repository binding in this session's workspace", p.GitRead)}
	case errors.Is(err, ErrGitReadUnsupported):
		return &runErr{http.StatusUnprocessableEntity,
			"git_read: only GitHub repositories get a session read credential; a GitLab session gets none"}
	case err != nil:
		return unavailable("could not be minted", err)
	}
	release := func() {
		if cred.Release == nil {
			return
		}
		rctx, cancel := context.WithTimeout(context.Background(), gitReadHostTimeout)
		defer cancel()
		if err := cred.Release(rctx); err != nil {
			m.warnf("sessions: the repository read credential could not be revoked; it expires on its own",
				"run_ref", runRef, "binding", p.GitRead, "expires_at", cred.ExpiresAt.UTC().Format(time.RFC3339), "err", redactErr(err))
		}
	}
	u, err := url.Parse(cred.RepoURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		strings.ContainsAny(cred.RepoURL+cred.Token, "\r\n\x00") || cred.Token == "" {
		release()
		return unavailable("could not be prepared", errors.New("the custody returned a malformed credential"))
	}
	path, err := writeGitReadFile(filepath.Join(m.rt.GitReadDataDir, "run", runRef), cred.Token)
	if err != nil {
		release()
		return unavailable("could not be prepared", err)
	}
	context.AfterFunc(runCtx, func() {
		_ = os.Remove(path)
		release()
	})
	// git runs a "!" helper through the shell with the action as its argument; this
	// one answers only "get" (store and erase have nothing to change). Two URL keys:
	// git matches the path exactly up to ".git", and both clone forms are common.
	helper := "!f() { test \"$1\" = get && cat '" + path + "'; }; f"
	repo := strings.TrimSuffix(strings.TrimRight(cred.RepoURL, "/"), ".git")
	spec.Env = append(spec.Env,
		EnvVar{Name: "GIT_CONFIG_COUNT", Value: "2"},
		EnvVar{Name: "GIT_CONFIG_KEY_0", Value: "credential." + repo + ".helper"},
		EnvVar{Name: "GIT_CONFIG_VALUE_0", Value: helper},
		EnvVar{Name: "GIT_CONFIG_KEY_1", Value: "credential." + repo + ".git.helper"},
		EnvVar{Name: "GIT_CONFIG_VALUE_1", Value: helper},
	)
	spec.AllowRead(path)
	p.gitReadToken = cred.Token
	p.gitReadDetail = "git_read " + p.GitRead + " contents:read until " + cred.ExpiresAt.UTC().Format(time.RFC3339)
	return nil
}

// writeGitReadFile writes the credential in git's credential format, mode 0600.
func writeGitReadFile(dir, token string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(dir, "git-credential-*")
	if err != nil {
		return "", err
	}
	path := file.Name()
	_, werr := file.WriteString("username=" + gitReadUsername + "\npassword=" + token + "\n")
	if err := errors.Join(werr, file.Close()); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// outputRedactor withholds the run's vault secret values and its repository
// read token from the output the plane stores and streams: the token, and the
// Basic authorization git sends with it (visible in git's curl traces).
func (p CreateRunParams) outputRedactor() *secretRedactor {
	if p.gitReadToken == "" {
		return newSecretRedactor(p.SecretEnv, p.secretEnvValues)
	}
	refs := append(slices.Clone(p.SecretEnv), SecretEnvRef{Env: gitReadRedactName, Secret: gitReadMark})
	basic := base64.StdEncoding.EncodeToString([]byte(gitReadUsername + ":" + p.gitReadToken))
	values := append(slices.Clone(p.secretEnvValues),
		EnvVar{Name: gitReadRedactName, Value: p.gitReadToken}, EnvVar{Name: gitReadRedactName, Value: basic})
	return newSecretRedactor(refs, values)
}
