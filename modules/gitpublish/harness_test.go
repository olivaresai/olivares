// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	shaBase   = "1111111111111111111111111111111111111111"
	shaCommit = "2222222222222222222222222222222222222222"
	shaTree   = "3333333333333333333333333333333333333333"
	shaOther  = "4444444444444444444444444444444444444444"
	shaMerged = "5555555555555555555555555555555555555555"
)

// fakeHost is a controllable Git host. Writes can be held; every call is
// counted so tests can assert that nothing was dispatched twice.
type fakeHost struct {
	mu            sync.Mutex
	defaultBranch string
	protected     map[string]bool
	refs          map[string]string
	changes       []gp.Change
	mints         int
	releases      int
	creates       int
	merges        int
	mintErr       error
	refErr        map[string]error
	hidden        map[string]bool
	lookupErr     error
	createRes     *gp.Result
	mergeRes      *gp.Result
	onCreate      func(h *fakeHost)
	onMerge       func(h *fakeHost)
	release       error
	mintSecret    string // returned by Mint when set (the ssh key, say)
	sshTarget     bool   // PushTarget answers the ssh transport
}

func newFakeHost() *fakeHost {
	return &fakeHost{defaultBranch: "main", protected: map[string]bool{}, refs: map[string]string{"main": shaBase}, refErr: map[string]error{}, hidden: map[string]bool{}}
}

func (h *fakeHost) Mint(context.Context, gp.Effect) (gp.Token, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.mintErr != nil {
		return gp.Token{}, h.mintErr
	}
	h.mints++
	return gp.NewToken(gp.NewSecret(h.mintSecret)), nil
}

func (h *fakeHost) Release(context.Context, gp.Token) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.releases++
	return h.release
}

func (h *fakeHost) Ref(_ context.Context, _ gp.Token, b string) (gp.RefObservation, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.refErr[b]; err != nil {
		return gp.RefObservation{}, err
	}
	if h.hidden[b] {
		return gp.RefObservation{State: gp.RefNotFoundOrHidden}, nil
	}
	if s, ok := h.refs[b]; ok {
		return gp.RefObservation{State: gp.RefPresent, SHA: s}, nil
	}
	return gp.RefObservation{State: gp.RefNotFoundOrHidden}, nil
}

func (h *fakeHost) Branch(_ context.Context, _ gp.Token, b string) (gp.BranchInfo, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, exists := h.refs[b]
	return gp.BranchInfo{Default: h.defaultBranch, Exists: exists, Protected: h.protected[b]}, nil
}

func (h *fakeHost) ref(b string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.refs[b]
}

func (h *fakeHost) CommitTree(context.Context, gp.Token, string) (string, error) {
	return shaTree, nil
}

func (h *fakeHost) OpenChanges(_ context.Context, _ gp.Token, head, base string) ([]gp.Change, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lookupErr != nil {
		return nil, h.lookupErr
	}
	// Like a host whose filter is looser than asked, this returns closed
	// changes too: the module must never adopt or be blocked by one.
	var out []gp.Change
	for _, c := range h.changes {
		if c.HeadRef == head && c.BaseRef == base {
			out = append(out, c)
		}
	}
	return out, nil
}

func (h *fakeHost) GetChange(_ context.Context, _ gp.Token, n int) (gp.Change, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.changes {
		if c.Number == n {
			return c, nil
		}
	}
	return gp.Change{}, gp.ErrHostUnavailable
}

func (h *fakeHost) CreateChange(_ context.Context, _ gp.Token, s gp.ChangeSpec) (gp.Change, gp.Result) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.creates++
	if h.onCreate != nil {
		h.onCreate(h)
	}
	if h.createRes != nil {
		return gp.Change{}, *h.createRes
	}
	c := gp.Change{Number: 100 + h.creates, Open: true, HeadRef: s.Head, HeadSHA: h.refs[s.Head], BaseRef: s.Base, CreatedAt: time.Now()}
	h.changes = append(h.changes, c)
	return c, gp.Result{Class: gp.Applied}
}

func (h *fakeHost) MergeChange(_ context.Context, _ gp.Token, n int, sha, _ string) (gp.MergeOutcome, gp.Result) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.merges++
	if h.onMerge != nil {
		h.onMerge(h)
	}
	if h.mergeRes != nil {
		return gp.MergeOutcome{}, *h.mergeRes
	}
	for i, c := range h.changes {
		if c.Number == n {
			if c.HeadSHA != sha {
				return gp.MergeOutcome{}, gp.Result{Class: gp.Rejected, Reason: "head_mismatch", Host: gp.HostError{Status: 409, Code: "conflict"}}
			}
			h.changes[i].Open, h.changes[i].Merged, h.changes[i].MergeCommitSHA = false, true, shaMerged
			h.refs[c.BaseRef] = shaMerged
			return gp.MergeOutcome{Merged: true, MergeCommitSHA: shaMerged}, gp.Result{Class: gp.Applied}
		}
	}
	return gp.MergeOutcome{}, gp.Result{Class: gp.Rejected, Reason: "not_found"}
}

func (h *fakeHost) PushTarget(gp.Token) (string, string, gp.Secret) {
	if h.sshTarget {
		return "ssh://git@git.example/srv/git/widgets.git", "ssh", gp.Secret{}
	}
	return "https://git.example/acme/widgets.git", "https", gp.Secret{}
}

// fakeGit applies pushes to the fake host with lease semantics. A held push
// blocks until released; if the dispatcher gives up first, the held write
// still lands when released, as a real host may complete a request late.
type fakeGit struct {
	host         *fakeHost
	mu           sync.Mutex
	pushes       int
	hold         chan struct{}
	workflows    bool
	checkedPaths []string
	treeFor      map[string]string
	localErr     error
	lastReq      gp.PushRequest
	afterApply   func()
	// sessionTrees is what a session folder holds: a fetch adds it to treeFor.
	sessionTrees map[string]string
	fetches      []string
	fetchErr     error
	fetchBlocks  bool // the fetch runs until the deadline, then git is killed
}

func (g *fakeGit) Fetch(ctx context.Context, repo, source, commit string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.fetches = append(g.fetches, repo+"|"+source+"|"+commit)
	if g.fetchBlocks {
		<-ctx.Done()
		return gp.ErrContent
	}
	if g.fetchErr != nil {
		return g.fetchErr
	}
	if t, ok := g.sessionTrees[commit]; ok {
		g.treeFor[commit] = t
		return nil
	}
	return gp.ErrContent
}

func (g *fakeGit) CommitTree(_ context.Context, _ string, c string) (string, error) {
	if t, ok := g.treeFor[c]; ok {
		return t, nil
	}
	return "", gp.ErrContent
}

func (g *fakeGit) PathsChanged(_ context.Context, _, _, _ string, paths []string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.checkedPaths = append(g.checkedPaths, paths...)
	return g.workflows, nil
}

func (g *fakeGit) apply(r gp.PushRequest) gp.Result {
	g.host.mu.Lock()
	defer g.host.mu.Unlock()
	b := r.Ref[len("refs/heads/"):]
	cur, ok := g.host.refs[b]
	if (r.ExpectedOld == "" && ok) || (r.ExpectedOld != "" && cur != r.ExpectedOld) {
		return gp.Result{Class: gp.Rejected, Reason: "stale_lease"}
	}
	g.host.refs[b] = r.Commit
	return gp.Result{Class: gp.Applied}
}

func (g *fakeGit) Push(ctx context.Context, r gp.PushRequest) (gp.Result, error) {
	g.mu.Lock()
	if g.localErr != nil {
		g.mu.Unlock()
		return gp.Result{}, g.localErr
	}
	g.pushes++
	g.lastReq = r
	hold := g.hold
	g.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			go func() { <-hold; g.apply(r) }()
			return gp.Result{Class: gp.Ambiguous, Reason: "transport"}, nil
		}
	}
	res := g.apply(r)
	if g.afterApply != nil {
		g.afterApply()
	}
	return res, nil
}

func (g *fakeGit) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.pushes
}

// fakeCustody approves one credential binding and one repository binding.
type fakeCustody struct {
	mu        sync.Mutex
	host      *fakeHost
	cbVer     int64
	rbVer     int64
	owners    []string
	hostKind  string
	tenant    model.TenantID
	workspace model.ID
	approved  map[string]bool
}

// inScope approves bindings only in the harness tenant and workspace; an
// empty scope (not configured) approves everywhere.
func (c *fakeCustody) inScope(tn model.TenantID, ws model.ID) bool {
	return c.tenant == "" || (tn == c.tenant && ws == c.workspace)
}

func (c *fakeCustody) CredentialBinding(_ context.Context, tn model.TenantID, ws model.ID, id string) (CredentialBinding, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.approved[id] || !c.inScope(tn, ws) {
		return CredentialBinding{}, ErrBindingNotApproved
	}
	kind := c.hostKind
	if kind == "" {
		kind = "github"
	}
	return CredentialBinding{ID: id, Version: c.cbVer, Host: kind, AllowedOwners: c.owners}, nil
}

func (c *fakeCustody) RepositoryBinding(_ context.Context, tn model.TenantID, ws model.ID, id string) (RepositoryBinding, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.approved[id] || !c.inScope(tn, ws) {
		return RepositoryBinding{}, ErrBindingNotApproved
	}
	return RepositoryBinding{ID: id, Version: c.rbVer, RepoID: "R_1", Owner: "acme", Name: "widgets", LocalPath: "/srv/olivares/gitpublish/r1.git"}, nil
}

func (c *fakeCustody) OpenHost(context.Context, model.TenantID, CredentialBinding, RepositoryBinding) (gp.Host, error) {
	return c.host, nil
}

// fakeAuthority admits by principal; tests inject denials and an A4 hook.
type fakeAuthority struct {
	mu       sync.Mutex
	admits   int
	locks    int
	deny     map[model.ID]error // by workspace
	recheck  func() error
	stepUpAt int
}

type fakeAdmission struct {
	a   *fakeAuthority
	sub Subject
}

func (a *fakeAuthority) Admit(_ context.Context, p auth.Principal, _ model.TenantID, q Question) (Admission, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.admits++
	if err := a.deny[q.Workspace]; err != nil {
		return nil, err
	}
	if q.MinimumAAL > p.AAL {
		return nil, auth.ErrStepUpRequired
	}
	return fakeAdmission{a: a, sub: SubjectOf(p)}, nil
}

func (f fakeAdmission) Subject() Subject { return f.sub }
func (f fakeAdmission) Lock(context.Context, store.Scope, time.Time) error {
	f.a.mu.Lock()
	defer f.a.mu.Unlock()
	f.a.locks++
	return nil
}
func (f fakeAdmission) Recheck(context.Context, View, time.Time) error {
	if f.a.recheck != nil {
		return f.a.recheck()
	}
	return nil
}

type harness struct {
	t       *testing.T
	m       *Module
	host    *fakeHost
	git     *fakeGit
	custody *fakeCustody
	authz   *fakeAuthority
	tenant  model.TenantID
	other   model.TenantID
	ws      model.ID
	target  Target
	st      store.Store // set by newScopeHarnessOn, for a test that reopens it
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	h := &harness{t: t, host: newFakeHost(), authz: &fakeAuthority{deny: map[model.ID]error{}}}
	h.git = &fakeGit{host: h.host, treeFor: map[string]string{shaCommit: shaTree, shaBase: shaTree, shaOther: shaTree}}
	h.custody = &fakeCustody{host: h.host, cbVer: 1, rbVer: 1, owners: []string{"acme"}, approved: map[string]bool{"cb1": true, "rb1": true, "rb2": true}}
	h.m = New(Options{Custody: h.custody, Git: h.git, Authority: h.authz, DispatchTimeout: 2 * time.Second})
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, h.m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		a, e := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		if e != nil {
			return e
		}
		b, e := sys.CreateOrg(ctx, model.Org{Name: "beta", Slug: "beta", Status: model.StatusActive})
		if e != nil {
			return e
		}
		h.tenant, h.other = a.TenantID, b.TenantID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h.m.UseData(api.NewModuleData(st))
	h.ws = model.NewID()
	h.custody.tenant, h.custody.workspace = h.tenant, h.ws
	tg, err := h.m.CreateTarget(ctx, h.admin(), TargetInput{Workspace: h.ws, CredentialBinding: "cb1", RepositoryBinding: "rb1", PushPrefix: "olivares/", MergeBases: []string{"main"}})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	h.target = tg
	return h
}

func (h *harness) caller(kind auth.PrincipalKind, user, cred model.ID, aal int) Caller {
	return Caller{Tenant: h.tenant, Principal: auth.Principal{Kind: kind, UserID: user, CredID: cred, AAL: aal}}
}

var (
	userA  = model.NewID()
	credA  = model.NewID()
	adminU = model.NewID()
)

func (h *harness) user() Caller  { return h.caller(auth.KindUser, userA, credA, 1) }
func (h *harness) admin() Caller { return h.caller(auth.KindUser, adminU, model.NewID(), 3) }

func (h *harness) push(c Caller, op, ref, old string) (Receipt, error) {
	return h.m.Push(context.Background(), c, PushInput{Target: h.target.ID, OperationID: op, Ref: ref, ExpectedOld: old, Commit: shaCommit, Tree: shaTree})
}

func codeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	if err == nil {
		return ""
	}
	return "untyped:" + err.Error()
}

func (h *harness) balanced() {
	h.t.Helper()
	h.host.mu.Lock()
	defer h.host.mu.Unlock()
	if h.host.mints != h.host.releases {
		h.t.Fatalf("tokens minted %d, released %d", h.host.mints, h.host.releases)
	}
}
