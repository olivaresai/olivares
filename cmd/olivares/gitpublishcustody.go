// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/gitpublish"
	"github.com/olivaresai/olivares/modules/sessions"
)

// J10-S3 custody: the approved host credentials and server repositories the
// publication module may use, selected from the operator's existing custody.
// Only a superadmin at AAL3 writes either store; no tenant route reaches them.
//
//   - A credential binding is a source-roster row (auth.SourceStore, the
//     deployment scope) of kind github, gitlab or git. It is enabled and has
//     no plugin, its tenant is the caller's tenant, and its config names a
//     publication credential. The binding id is the row's persistent id, so a
//     source deleted and recreated under the same name is a different binding.
//   - A repository binding of a github or gitlab row is one entry of that
//     row's publish_repositories, named "<binding id>:<owner>/<name>". A git
//     row names its one remote in config key "remote" (ssh:// or https://);
//     its repository binding is that remote's path. Either way the version is
//     the row's version, so any edit of the row changes both pins and A4
//     refuses a pinned intent.
//   - publish_workspaces, when set, narrows the row to those workspaces.
//   - The credential is read only as `store:git-host/<name>` from the sealed
//     secret store's deployment scope, never through the scheme resolver: no
//     env:, file:, vault or cloud reference and no other store name resolves.
//     A git row's credential is the SSH private key (ssh remote) or
//     "<user>:<token-or-password>" (https remote).
//   - The API host of a github or gitlab row is the row's api_base (the
//     observer's default when unset); the row is the superadmin's approval of
//     that host, so its host is the adapter's whole write allowlist (GitHub
//     Enterprise Server and self-managed GitLab publish) and every other
//     endpoint rule still applies. A git row has no API host: its remote is
//     the approved destination.
const (
	gitpublishSecretPrefix = "git-host/"
	gitpublishHTTPTimeout  = 60 * time.Second
)

var errGitpublishBindingChanged = errors.New("gitpublish custody: the binding changed after admission")

// gitpublishSourceReader is the roster read custody needs; *auth.SourceStore
// implements it.
type gitpublishSourceReader interface {
	GetByID(ctx context.Context, id model.ID) (model.SourceDef, bool, error)
}

// gitpublishSecretReader is the sealed secret read custody needs;
// *auth.SecretStore implements it.
type gitpublishSecretReader interface {
	Resolve(ctx context.Context, scope model.TenantID, name string) ([]byte, error)
}

// gitpublishRepositoryInit creates an engine-owned bare server repository;
// *gp.Executor implements it.
type gitpublishRepositoryInit interface {
	InitManaged(ctx context.Context, path string) error
}

// gitpublishCustody implements gitpublish.Custody.
type gitpublishCustody struct {
	sources gitpublishSourceReader
	secrets gitpublishSecretReader
	repos   gitpublishRepositoryInit
	exec    *gp.Executor // the closed executor a plain-git adapter reads through
	root    string       // <data dir>/gitpublish/repositories
	doer    gp.Doer
	mu      sync.Mutex // serializes server repository creation
}

var _ gitpublish.Custody = (*gitpublishCustody)(nil)

// approvedSource returns the roster row a binding id names when it is an
// approved publication credential of tenant.
type gitpublishSource struct {
	model.SourceDef
	kind gp.TargetKind
	row  gp.TargetRow
}

func (c *gitpublishCustody) approvedSource(ctx context.Context, tenant model.TenantID, id string) (gitpublishSource, error) {
	if id == "" || strings.ContainsAny(id, ": \t\r\n") {
		return gitpublishSource{}, gitpublish.ErrBindingNotApproved
	}
	def, ok, err := c.sources.GetByID(ctx, model.ID(id))
	if err != nil {
		return gitpublishSource{}, err
	}
	if !ok || def.Scope != auth.GlobalSourceScope || !def.Enabled || def.Plugin != nil ||
		tenant.IsZero() || strings.TrimSpace(def.Tenant) != tenant.String() {
		return gitpublishSource{}, gitpublish.ErrBindingNotApproved
	}
	kind, ok := gp.LookupTargetKind(def.Kind)
	if !ok {
		return gitpublishSource{}, gitpublish.ErrBindingNotApproved
	}
	if _, ok := gitpublishSecretName(def.Config[gp.PublicationCredentialKey]); !ok {
		return gitpublishSource{}, gitpublish.ErrBindingNotApproved
	}
	row, err := kind.Bind(def.Config)
	if err != nil {
		return gitpublishSource{}, gitpublish.ErrBindingNotApproved
	}
	return gitpublishSource{SourceDef: def, kind: kind, row: row}, nil
}

// approvedIn also requires workspace to be one the row allows.
func (c *gitpublishCustody) approvedIn(ctx context.Context, tenant model.TenantID, workspace model.ID, id string) (gitpublishSource, error) {
	def, err := c.approvedSource(ctx, tenant, id)
	if err != nil {
		return def, err
	}
	if !def.row.AllowsWorkspace(workspace.String()) {
		return gitpublishSource{}, gitpublish.ErrBindingNotApproved
	}
	return def, nil
}

// CredentialBinding implements gitpublish.Custody. The allowed owner is the
// row's org (GitHub) or group (GitLab); a row without one allows none. A git
// row has exactly one remote, so its owner is the remote path without the
// repository name, and the containment check stays meaningful.
func (c *gitpublishCustody) CredentialBinding(ctx context.Context, tenant model.TenantID, workspace model.ID, id string) (gitpublish.CredentialBinding, error) {
	def, err := c.approvedIn(ctx, tenant, workspace, id)
	if err != nil {
		return gitpublish.CredentialBinding{}, err
	}
	return def.credentialBinding(), nil
}

// credentialBinding is an approved row as a credential binding.
func (def gitpublishSource) credentialBinding() gitpublish.CredentialBinding {
	var owners []string
	if o := def.row.Owner(); o != "" {
		owners = []string{o}
	}
	return gitpublish.CredentialBinding{ID: def.ID.String(), Version: def.Version, Host: def.Kind, AllowedOwners: owners}
}

// RepositoryBinding implements gitpublish.Custody. The server repository is
// engine-owned and created empty on first use; no request names its path.
func (c *gitpublishCustody) RepositoryBinding(ctx context.Context, tenant model.TenantID, workspace model.ID, id string) (gitpublish.RepositoryBinding, error) {
	source, path, ok := strings.Cut(id, ":")
	if !ok {
		return gitpublish.RepositoryBinding{}, gitpublish.ErrBindingNotApproved
	}
	def, err := c.approvedIn(ctx, tenant, workspace, source)
	if err != nil {
		return gitpublish.RepositoryBinding{}, err
	}
	rb, err := c.repository(tenant, def, path)
	if err != nil {
		return gitpublish.RepositoryBinding{}, err
	}
	if err := c.ensure(ctx, rb.LocalPath); err != nil {
		return gitpublish.RepositoryBinding{}, err
	}
	return rb, nil
}

// repository resolves one approved repository of def. A github or gitlab row
// lists it in publish_repositories; a git row has the one remote, so only
// that remote's path resolves.
func (c *gitpublishCustody) repository(tenant model.TenantID, def gitpublishSource, path string) (gitpublish.RepositoryBinding, error) {
	repo, err := def.row.Repository(path)
	if errors.Is(err, gp.ErrTargetRepository) {
		return gitpublish.RepositoryBinding{}, gitpublish.ErrBindingNotApproved
	}
	if err != nil {
		return gitpublish.RepositoryBinding{}, fmt.Errorf("%w: %w", gitpublish.ErrBindingNotApproved, err)
	}
	sum := sha256.Sum256([]byte(tenant.String() + "\x00" + def.ID.String() + "\x00" + strings.ToLower(path)))
	return gitpublish.RepositoryBinding{
		ID:        def.ID.String() + ":" + path,
		Version:   def.Version,
		RepoID:    repo.ID,
		Owner:     repo.Owner,
		Name:      repo.Name,
		LocalPath: filepath.Join(c.root, hex.EncodeToString(sum[:16])+".git"),
	}, nil
}

// ensure creates the server repository once. An existing entry that is not a
// directory is refused; the executor checks the repository's state at each use.
func (c *gitpublishCustody) ensure(ctx context.Context, path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, err := os.Lstat(path)
	if err == nil {
		if !st.IsDir() {
			return errors.New("gitpublish custody: the server repository is not a directory")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return c.repos.InitManaged(ctx, path)
}

// OpenHost implements gitpublish.Custody. It reads the row again and refuses
// when either pin moved or the pair does not come from one row, then builds
// the adapter with the sealed credential.
func (c *gitpublishCustody) OpenHost(ctx context.Context, tenant model.TenantID, cb gitpublish.CredentialBinding, rb gitpublish.RepositoryBinding) (gp.Host, error) {
	source, path, ok := strings.Cut(rb.ID, ":")
	if !ok || source != cb.ID {
		return nil, gitpublish.ErrBindingNotApproved
	}
	def, err := c.approvedSource(ctx, tenant, cb.ID)
	if err != nil {
		return nil, err
	}
	if def.Version != cb.Version || def.Version != rb.Version || def.Kind != cb.Host {
		return nil, errGitpublishBindingChanged
	}
	current, err := c.repository(tenant, def, path)
	if err != nil {
		return nil, err
	}
	if current != rb {
		return nil, errGitpublishBindingChanged
	}
	name, _ := gitpublishSecretName(def.row.CredentialRef())
	value, err := c.secrets.Resolve(ctx, auth.GlobalSecretScope, name)
	if err != nil {
		return nil, errors.New("gitpublish custody: the publication credential is unavailable")
	}
	credential := gp.NewSecret(string(value))
	clear(value)
	return def.row.Open(path, credential, gp.HostDependencies{HTTP: c.doer, Git: c.exec})
}

// MintSessionGitRead implements sessions.SessionGitReadSource: a contents:read
// installation token for one approved GitHub repository binding in workspace,
// through the adapter's own Mint and Release. A kind without a narrow read
// capability is refused before any secret read or call. The App key never leaves.
func (c *gitpublishCustody) MintSessionGitRead(ctx context.Context, tenant model.TenantID, workspace model.ID, id string) (sessions.GitReadCredential, error) {
	source, path, ok := strings.Cut(id, ":")
	if !ok {
		return sessions.GitReadCredential{}, sessions.ErrGitReadNotApproved
	}
	def, err := c.approvedIn(ctx, tenant, workspace, source)
	if errors.Is(err, gitpublish.ErrBindingNotApproved) {
		return sessions.GitReadCredential{}, sessions.ErrGitReadNotApproved
	} else if err != nil {
		return sessions.GitReadCredential{}, err
	}
	if !def.kind.Capabilities().NarrowRead {
		return sessions.GitReadCredential{}, sessions.ErrGitReadUnsupported
	}
	cb := def.credentialBinding()
	rb, err := c.repository(tenant, def, path)
	if err != nil || !gitpublishContainsFold(cb.AllowedOwners, rb.Owner) {
		return sessions.GitReadCredential{}, sessions.ErrGitReadNotApproved
	}
	host, err := c.OpenHost(ctx, tenant, cb, rb)
	if err != nil {
		return sessions.GitReadCredential{}, err
	}
	tok, err := host.Mint(ctx, gp.EffectRead)
	if err != nil {
		return sessions.GitReadCredential{}, err
	}
	repoURL, _, _ := host.PushTarget(tok)
	return sessions.GitReadCredential{
		RepoURL: repoURL, Token: tok.Value().Reveal(), ExpiresAt: tok.ExpiresAt,
		Release: func(ctx context.Context) error { return host.Release(ctx, tok) },
	}, nil
}

// gitpublishSecretName accepts only `store:git-host/<name>`.
func gitpublishSecretName(ref string) (string, bool) {
	name, ok := strings.CutPrefix(strings.TrimSpace(ref), "store:")
	if !ok || !strings.HasPrefix(name, gitpublishSecretPrefix) || len(name) == len(gitpublishSecretPrefix) {
		return "", false
	}
	if auth.ValidateSecretName(name) != "" || strings.Contains(name, "..") || strings.Contains(name, "//") || strings.HasSuffix(name, "/") {
		return "", false
	}
	return name, true
}

// gitpublishContainsFold is the module's owner check (targets.go bindings).
func gitpublishContainsFold(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

// gitpublishLookPath finds the git executable to pin; tests replace it.
var gitpublishLookPath = exec.LookPath

// newGitPublication builds the module's custody and closed git executor: the
// git found on the engine's PATH, pinned by its absolute path, with an empty
// engine-owned home and server repositories under the data directory. It
// fails, and boot leaves both ports unbound (deny-closed), when the secret
// store has no sealer or no absolute git is found.
func newGitPublication(dataDir string, sources gitpublishSourceReader, secrets gitpublishSecretReader, sealerPresent bool) (*gitpublishCustody, *gp.Executor, error) {
	if !sealerPresent {
		return nil, nil, errors.New("the secret store has no sealer, so no publication credential can be read")
	}
	gitPath, err := gitpublishLookPath("git")
	if err != nil || !filepath.IsAbs(gitPath) {
		return nil, nil, errors.New("no git executable with an absolute path is on the engine's PATH")
	}
	dir, err := filepath.Abs(filepath.Join(dataDir, "gitpublish"))
	if err != nil {
		return nil, nil, err
	}
	home, root := filepath.Join(dir, "home"), filepath.Join(dir, "repositories")
	for _, d := range []string{home, root} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, nil, fmt.Errorf("create %s: %w", filepath.Base(d), err)
		}
	}
	x, err := gp.NewExecutor(gitPath, home)
	if err != nil {
		return nil, nil, err
	}
	return &gitpublishCustody{sources: sources, secrets: secrets, repos: x, exec: x, root: root, doer: gp.NewHTTPClient(gitpublishHTTPTimeout)}, x, nil
}

// The publication sweep, on the runtime's periodic scheduler: it settles
// stale dispatches and re-observes uncertain intents. SweepPump itself skips
// a standby node, so only the active writer sweeps.
const (
	gitpublishSweepJobName         = "gitpublish-sweep"
	gitpublishSweepIntervalEnv     = "OLIVARES_GITPUBLISH_SWEEP_INTERVAL"
	defaultGitpublishSweepInterval = time.Minute
)

// gitpublishSweepInterval parses the interval. An invalid value keeps the
// default; 0 disables the sweep and says what that leaves undone.
func gitpublishSweepInterval(raw string, log *slog.Logger) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultGitpublishSweepInterval, true
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		log.Warn("gitpublish-sweep: "+gitpublishSweepIntervalEnv+" is not a valid non-negative duration; using the default", "value", raw, "default", defaultGitpublishSweepInterval.String())
		return defaultGitpublishSweepInterval, true
	}
	if d == 0 {
		log.Warn("gitpublish-sweep: DISABLED (" + gitpublishSweepIntervalEnv + "=0): a publication whose dispatcher stopped stays dispatching, and an uncertain one is observed only when someone reconciles it")
		return 0, false
	}
	return d, true
}

// gitpublishSweepTenants lists the served business tenants and logs a failed
// enumeration, which SweepPump answers by skipping the tick.
func gitpublishSweepTenants(st store.Store, log *slog.Logger) func(context.Context) ([]model.TenantID, error) {
	return func(ctx context.Context) ([]model.TenantID, error) {
		tenants, err := servedWorkTenants(ctx, st)
		if err != nil {
			log.Warn("gitpublish-sweep: cannot enumerate orgs; skipping this tick", "err", err)
		}
		return tenants, err
	}
}
