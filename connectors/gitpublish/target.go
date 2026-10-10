// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// Publication row keys are shared by the three first-party target kinds.
const (
	PublicationCredentialKey   = "publish_credential"
	PublicationRepositoriesKey = "publish_repositories"
	PublicationWorkspacesKey   = "publish_workspaces"
	PublicationRemoteKey       = "remote"
)

// ErrTargetRepository means the row does not approve the repository path.
// Custody maps it to its own binding refusal; endpoint errors stay distinct.
var ErrTargetRepository = errors.New("gitpublish: repository not approved")

// TargetCapabilities are the facts of a kind, independent of credentials.
// Returned slices belong to the caller.
type TargetCapabilities struct {
	Effects      []Effect
	MergeMethods []string
	CIPaths      []string
	NarrowRead   bool
}

// Supports reports whether the kind can express the effect and merge method.
func (c TargetCapabilities) Supports(effect Effect, method string) bool {
	return slices.Contains(c.Effects, effect) && (effect != EffectMerge || slices.Contains(c.MergeMethods, method))
}

// TargetKind owns row interpretation, host construction and capability facts.
// Lookup is closed to the three existing adapters; it never discovers a host.
type TargetKind struct {
	name    string
	adapter targetKindAdapter
}

type targetKindAdapter interface {
	bind(map[string]string) (TargetRow, error)
	capabilities() TargetCapabilities
	diffSource() DiffSource
}

// LookupTargetKind recognizes the roster's exact, case-sensitive kind.
func LookupTargetKind(name string) (TargetKind, bool) {
	var adapter targetKindAdapter
	switch name {
	case "github":
		adapter = githubTargetKind{}
	case "gitlab":
		adapter = gitlabTargetKind{}
	case "git":
		adapter = plainTargetKind{}
	default:
		return TargetKind{}, false
	}
	return TargetKind{name: name, adapter: adapter}, true
}

func (k TargetKind) Name() string                     { return k.name }
func (k TargetKind) Capabilities() TargetCapabilities { return k.adapter.capabilities() }
func (k TargetKind) NewDiffSource() DiffSource        { return k.adapter.diffSource() }

// TargetCapabilitiesFor preserves the publication module's historical GitHub
// capability defaults for an unspecified/custom custody kind. Row admission
// still uses LookupTargetKind and refuses unknown first-party roster kinds.
func TargetCapabilitiesFor(name string) TargetCapabilities {
	kind, ok := LookupTargetKind(name)
	if !ok {
		return githubTargetKind{}.capabilities()
	}
	return kind.Capabilities()
}

// Bind interprets an approved row without resolving its secret. Only a plain
// remote is validated here. API endpoints are checked at Repository, and
// credential configuration at Open, preserving the custody admission stages.
func (k TargetKind) Bind(cfg map[string]string) (TargetRow, error) { return k.adapter.bind(cfg) }

// TargetRow holds one interpretation of a row; a plain remote is parsed once.
// Tenant, enabled/plugin, secret-reference policy and binding pins stay with
// custody. The row does not read stores or create managed repositories.
type TargetRow struct {
	credentialRef string
	workspaces    []string
	owner         string
	binding       targetBinding
}

type targetBinding interface {
	repository(string) (TargetRepository, error)
	open(TargetRepository, Secret, HostDependencies) (Host, error)
}

func targetRow(cfg map[string]string, owner string, binding targetBinding) TargetRow {
	return TargetRow{credentialRef: cfg[PublicationCredentialKey], workspaces: targetList(cfg[PublicationWorkspacesKey]), owner: strings.TrimSpace(owner), binding: binding}
}

func (r TargetRow) CredentialRef() string { return r.credentialRef }
func (r TargetRow) Owner() string         { return r.owner }
func (r TargetRow) AllowsWorkspace(workspace string) bool {
	return len(r.workspaces) == 0 || slices.Contains(r.workspaces, workspace)
}
func (r TargetRow) Repository(path string) (TargetRepository, error) {
	return r.binding.repository(path)
}

// HostDependencies are the existing transports the composition root owns.
type HostDependencies struct {
	HTTP Doer
	Git  *Executor
}

// Open constructs the existing host adapter for an approved repository. No
// credential is minted and no external effect runs during construction.
func (r TargetRow) Open(path string, credential Secret, deps HostDependencies) (Host, error) {
	repo, err := r.Repository(path)
	if err != nil {
		return nil, err
	}
	return r.binding.open(repo, credential, deps)
}

// TargetRepository is the host identity, retaining the original path's case.
type TargetRepository struct{ ID, Owner, Name string }

func targetRepository(host, path string) TargetRepository {
	cut := strings.LastIndex(path, "/")
	return TargetRepository{ID: host + "/" + strings.ToLower(path), Owner: path[:cut], Name: path[cut+1:]}
}

type apiTargetBinding struct {
	base         string
	repositories []string
}

func apiTarget(cfg map[string]string, defaultBase string) apiTargetBinding {
	base := strings.TrimRight(strings.TrimSpace(cfg["api_base"]), "/")
	if base == "" {
		base = defaultBase
	}
	return apiTargetBinding{base: base, repositories: targetList(cfg[PublicationRepositoriesKey])}
}

func (b apiTargetBinding) repository(path string) (TargetRepository, error) {
	if !ValidRepoPath(path) || !slices.Contains(b.repositories, path) {
		return TargetRepository{}, ErrTargetRepository
	}
	u, err := url.Parse(b.base)
	if err != nil {
		return TargetRepository{}, fmt.Errorf("%w: unparsable", ErrEndpoint)
	}
	host := strings.ToLower(u.Hostname())
	if err := ValidateEndpoint(b.base, []string{host}); err != nil {
		return TargetRepository{}, err
	}
	return targetRepository(host, path), nil
}

func targetList(raw string) []string {
	var out []string
	for _, v := range strings.Split(raw, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
