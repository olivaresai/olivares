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
	"net/url"
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
)

// J10-S3 custody: the approved host credentials and server repositories the
// publication module may use, selected from the operator's existing custody.
// Only a superadmin at AAL3 writes either store; no tenant route reaches them.
//
//   - A credential binding is a source-roster row (auth.SourceStore, the
//     deployment scope) of kind github or gitlab. It is enabled and has no
//     plugin, its tenant is the caller's tenant, and its config names a
//     publication credential. The binding id is the row's persistent id, so a
//     source deleted and recreated under the same name is a different binding.
//   - A repository binding is one entry of that row's publish_repositories,
//     named "<binding id>:<owner>/<name>". Its version is the row's version, so
//     any edit of the row changes both pins and A4 refuses a pinned intent.
//   - publish_workspaces, when set, narrows the row to those workspaces.
//   - The credential is read only as `store:git-host/<name>` from the sealed
//     secret store's deployment scope, never through the scheme resolver: no
//     env:, file:, vault or cloud reference and no other store name resolves.
//   - The API host is the row's api_base (the observer's default when unset).
//     Only the public GitHub and GitLab API hosts pass the adapter's endpoint
//     rules, because no operator host allowlist is configured.
const (
	gitpublishCredentialKey   = "publish_credential"
	gitpublishRepositoriesKey = "publish_repositories"
	gitpublishWorkspacesKey   = "publish_workspaces"
	gitpublishSecretPrefix    = "git-host/"
	gitpublishHTTPTimeout     = 60 * time.Second
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
	root    string // <data dir>/gitpublish/repositories
	doer    gp.Doer
	mu      sync.Mutex // serializes server repository creation
}

var _ gitpublish.Custody = (*gitpublishCustody)(nil)

// approvedSource returns the roster row a binding id names when it is an
// approved publication credential of tenant.
func (c *gitpublishCustody) approvedSource(ctx context.Context, tenant model.TenantID, id string) (model.SourceDef, error) {
	if id == "" || strings.ContainsAny(id, ": \t\r\n") {
		return model.SourceDef{}, gitpublish.ErrBindingNotApproved
	}
	def, ok, err := c.sources.GetByID(ctx, model.ID(id))
	if err != nil {
		return model.SourceDef{}, err
	}
	if !ok || def.Scope != auth.GlobalSourceScope || !def.Enabled || def.Plugin != nil ||
		(def.Kind != "github" && def.Kind != "gitlab") ||
		tenant.IsZero() || strings.TrimSpace(def.Tenant) != tenant.String() {
		return model.SourceDef{}, gitpublish.ErrBindingNotApproved
	}
	if _, ok := gitpublishSecretName(def.Config[gitpublishCredentialKey]); !ok {
		return model.SourceDef{}, gitpublish.ErrBindingNotApproved
	}
	return def, nil
}

// approvedIn also requires workspace to be one the row allows.
func (c *gitpublishCustody) approvedIn(ctx context.Context, tenant model.TenantID, workspace model.ID, id string) (model.SourceDef, error) {
	def, err := c.approvedSource(ctx, tenant, id)
	if err != nil {
		return def, err
	}
	if ws := gitpublishList(def.Config[gitpublishWorkspacesKey]); len(ws) > 0 && !gitpublishContains(ws, workspace.String()) {
		return model.SourceDef{}, gitpublish.ErrBindingNotApproved
	}
	return def, nil
}

// CredentialBinding implements gitpublish.Custody. The allowed owner is the
// row's org (GitHub) or group (GitLab); a row without one allows none.
func (c *gitpublishCustody) CredentialBinding(ctx context.Context, tenant model.TenantID, workspace model.ID, id string) (gitpublish.CredentialBinding, error) {
	def, err := c.approvedIn(ctx, tenant, workspace, id)
	if err != nil {
		return gitpublish.CredentialBinding{}, err
	}
	owner := def.Config["org"]
	if def.Kind == "gitlab" {
		owner = def.Config["group"]
	}
	var owners []string
	if o := strings.TrimSpace(owner); o != "" {
		owners = []string{o}
	}
	return gitpublish.CredentialBinding{ID: def.ID.String(), Version: def.Version, Host: def.Kind, AllowedOwners: owners}, nil
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

// repository resolves one approved repository of def.
func (c *gitpublishCustody) repository(tenant model.TenantID, def model.SourceDef, path string) (gitpublish.RepositoryBinding, error) {
	if !gitpublishValidPath(path) || !gitpublishContains(gitpublishList(def.Config[gitpublishRepositoriesKey]), path) {
		return gitpublish.RepositoryBinding{}, gitpublish.ErrBindingNotApproved
	}
	host, err := gitpublishAPIHost(def)
	if err != nil {
		return gitpublish.RepositoryBinding{}, gitpublish.ErrBindingNotApproved
	}
	cut := strings.LastIndex(path, "/")
	sum := sha256.Sum256([]byte(tenant.String() + "\x00" + def.ID.String() + "\x00" + strings.ToLower(path)))
	return gitpublish.RepositoryBinding{
		ID:        def.ID.String() + ":" + path,
		Version:   def.Version,
		RepoID:    host + "/" + strings.ToLower(path),
		Owner:     path[:cut],
		Name:      path[cut+1:],
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
	name, _ := gitpublishSecretName(def.Config[gitpublishCredentialKey])
	value, err := c.secrets.Resolve(ctx, auth.GlobalSecretScope, name)
	if err != nil {
		return nil, errors.New("gitpublish custody: the publication credential is unavailable")
	}
	credential := gp.NewSecret(string(value))
	clear(value)
	base := gitpublishAPIBase(def)
	switch def.Kind {
	case "github":
		return gp.NewGitHub(gp.GitHubConfig{
			APIBase: base, AppID: strings.TrimSpace(def.Config["app_id"]), InstallationID: strings.TrimSpace(def.Config["installation_id"]),
			Key: credential, Owner: rb.Owner, Repo: rb.Name,
		}, c.doer)
	case "gitlab":
		return gp.NewGitLab(gp.GitLabConfig{APIBase: base, ProjectPath: path, Token: credential}, c.doer)
	}
	return nil, gitpublish.ErrBindingNotApproved
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

// gitpublishAPIBase is the row's api_base, or the observer's default.
func gitpublishAPIBase(def model.SourceDef) string {
	base := strings.TrimRight(strings.TrimSpace(def.Config["api_base"]), "/")
	if base != "" {
		return base
	}
	if def.Kind == "gitlab" {
		return "https://gitlab.com"
	}
	return "https://api.github.com"
}

// gitpublishAPIHost is the API host, after the adapter's endpoint rules.
func gitpublishAPIHost(def model.SourceDef) (string, error) {
	base := gitpublishAPIBase(def)
	if err := gp.ValidateEndpoint(base, nil); err != nil {
		return "", err
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	return strings.ToLower(u.Hostname()), nil
}

// gitpublishValidPath accepts owner/name paths (GitLab: group/…/project) of
// plain segments.
func gitpublishValidPath(path string) bool {
	segments := strings.Split(path, "/")
	if len(path) > 512 || len(segments) < 2 {
		return false
	}
	for _, s := range segments {
		if s == "" || s == "." || s == ".." {
			return false
		}
		for _, r := range s {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			default:
				return false
			}
		}
	}
	return true
}

func gitpublishList(raw string) []string {
	var out []string
	for _, v := range strings.Split(raw, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func gitpublishContains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
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
	return &gitpublishCustody{sources: sources, secrets: secrets, repos: x, root: root, doer: gp.NewHTTPClient(gitpublishHTTPTimeout)}, x, nil
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
