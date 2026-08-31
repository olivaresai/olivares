// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/egress"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/notify"
	"github.com/olivaresai/olivares/sdk"
)

// scopeStubConn is a minimal OutputConnector so a dispatcher can be exercised
// without opening a real destination.
type scopeStubConn struct{ notified int }

func (c *scopeStubConn) Descriptor() sdk.Descriptor             { return sdk.Descriptor{Name: "stub"} }
func (c *scopeStubConn) Open(context.Context, sdk.Config) error { return nil }
func (c *scopeStubConn) Close(context.Context) error            { return nil }
func (c *scopeStubConn) Notify(context.Context, sdk.Notification) error {
	c.notified++
	return nil
}

// TestTenantScopeGovernsDeliveryAndDiscovery covers the PRODUCTION dispatcher, which
// had no test of its own: the only tenant-scope test in the tree exercised a double
// in modules/notify that reimplements the rule, so a divergence between the double
// and the real thing was invisible.
func TestTenantScopeGovernsDeliveryAndDiscovery(t *testing.T) {
	a, b := model.NewTenantID(), model.NewTenantID()
	d := newConnectorDispatcher([]notifyDestinationSpec{
		{Name: "soc-a", Kind: "webhook", Tenants: []string{a.String()}},
		{Name: "shared", Kind: "webhook"},                      // undeclared: every tenant
		{Name: "nobody", Kind: "webhook", Tenants: []string{}}, // declared empty: no one
	}, nil, discardLog())
	for _, n := range []string{"soc-a", "shared", "nobody"} {
		d.conns[n] = &scopeStubConn{}
	}

	if got := d.DestinationsFor(a); len(got) != 2 {
		t.Errorf("tenant A sees %v, want its own plus the unscoped one", got)
	}
	if got := d.DestinationsFor(b); len(got) != 1 || got[0] != "shared" {
		t.Errorf("tenant B sees %v, want only the unscoped one", got)
	}
	if err := d.Deliver(context.Background(), b, "soc-a", sdk.Notification{}); !errors.Is(err, notify.ErrUnknownDestination) {
		t.Errorf("tenant B delivered to tenant A's destination: %v", err)
	}
	if err := d.Deliver(context.Background(), a, "soc-a", sdk.Notification{}); err != nil {
		t.Errorf("tenant A could not reach its own destination: %v", err)
	}
	// A DECLARED but empty list means nobody, and it is a legitimate thing for an
	// operator to write. The distinction from an UNDECLARED list is the whole point of
	// the tri-state and it is invisible in Go's zero value, so it is pinned here.
	if err := d.Deliver(context.Background(), a, "nobody", sdk.Notification{}); !errors.Is(err, notify.ErrUnknownDestination) {
		t.Errorf("a declared-empty scope was addressable: %v", err)
	}
}

// TestTenantScopeSurvivesAReload is the regression this file exists for.
//
// The scope used to be built ONCE in the constructor and never republished, while
// SIGHUP adds, reloads and removes destinations. Every one of those paths therefore
// failed OPEN, because an absent scope entry means "addressable by every tenant":
// a destination added by reload was addressable by everyone, narrowing one was a
// silent no-op, and a removed destination left a stale entry that decided the fate of
// the next destination to reuse its name.
func TestTenantScopeSurvivesAReload(t *testing.T) {
	a, b := model.NewTenantID(), model.NewTenantID()
	d := newConnectorDispatcher(nil, nil, discardLog())
	log := discardLog()

	// ADD: a destination that appears after construction must arrive scoped.
	d.mu.Lock()
	d.conns["soc"] = &scopeStubConn{}
	d.setTenantScope(notifyDestinationSpec{Name: "soc", Tenants: []string{a.String()}}, log)
	d.mu.Unlock()
	if err := d.Deliver(context.Background(), b, "soc", sdk.Notification{}); !errors.Is(err, notify.ErrUnknownDestination) {
		t.Errorf("a destination added after construction was addressable by an unrelated tenant: %v", err)
	}

	// NARROW: moving the destination from A to B must revoke A.
	d.mu.Lock()
	d.setTenantScope(notifyDestinationSpec{Name: "soc", Tenants: []string{b.String()}}, log)
	d.mu.Unlock()
	if err := d.Deliver(context.Background(), a, "soc", sdk.Notification{}); !errors.Is(err, notify.ErrUnknownDestination) {
		t.Errorf("narrowing the scope did not revoke the previous tenant: %v", err)
	}
	if err := d.Deliver(context.Background(), b, "soc", sdk.Notification{}); err != nil {
		t.Errorf("the newly scoped tenant could not reach it: %v", err)
	}

	// WIDEN: an undeclared list must restore "every tenant", not keep the old set.
	d.mu.Lock()
	d.setTenantScope(notifyDestinationSpec{Name: "soc"}, log)
	d.mu.Unlock()
	if err := d.Deliver(context.Background(), a, "soc", sdk.Notification{}); err != nil {
		t.Errorf("widening to an undeclared scope did not restore access: %v", err)
	}

	// REMOVE then RE-ADD under a different owner: no stale entry may survive.
	d.mu.Lock()
	d.setTenantScope(notifyDestinationSpec{Name: "soc", Tenants: []string{a.String()}}, log)
	delete(d.conns, "soc")
	delete(d.tenantScope, "soc")
	d.conns["soc"] = &scopeStubConn{}
	d.setTenantScope(notifyDestinationSpec{Name: "soc", Tenants: []string{b.String()}}, log)
	d.mu.Unlock()
	if err := d.Deliver(context.Background(), a, "soc", sdk.Notification{}); !errors.Is(err, notify.ErrUnknownDestination) {
		t.Errorf("a stale scope entry survived a remove and re-add: %v", err)
	}
}

// TestTheFingerprintNoticesAScopeChange. Without this the reconcile reports
// "unchanged" for a narrowed destination and never calls the reload that republishes
// the scope, so the revoked tenant keeps delivering.
func TestTheFingerprintNoticesAScopeChange(t *testing.T) {
	a, b := model.NewTenantID().String(), model.NewTenantID().String()
	base := notifyDestinationSpec{Name: "soc", Kind: "webhook", Config: map[string]string{"url": "https://x"}}

	wide := base
	narrow := base
	narrow.Tenants = []string{a}
	moved := base
	moved.Tenants = []string{b}
	reordered := base
	reordered.Tenants = []string{b, a}
	sameSet := base
	sameSet.Tenants = []string{a, b}

	if fingerprintExternal(wide) == fingerprintExternal(narrow) {
		t.Error("narrowing an unscoped destination did not change the fingerprint")
	}
	if fingerprintExternal(narrow) == fingerprintExternal(moved) {
		t.Error("moving a destination to another tenant did not change the fingerprint")
	}
	// Order must NOT matter: a reordered list is the same policy, and churning a
	// subprocess for it would be a reload nobody asked for.
	if fingerprintExternal(reordered) != fingerprintExternal(sameSet) {
		t.Error("reordering the tenants list changed the fingerprint")
	}
	// And an empty declared list is not the same as an absent one.
	empty := base
	empty.Tenants = []string{}
	if fingerprintExternal(empty) == fingerprintExternal(wide) {
		t.Error("a declared-empty list fingerprints the same as an absent one")
	}
}

// TestCLIGuardedClientReachesANameAndHonoursThePin. The CLI used to build its own
// client, and it failed the way the engine's pin first failed: it refused any dial
// address that was not already an IP literal, while an http.Transport hands its
// dialer "hostname:port". Every hostname destination was unreachable — and the
// documented example in the command's own help is a hostname.
//
// It is now the engine's client, so this also pins that the two cannot drift again.
func TestCLIGuardedClientReachesANameAndHonoursThePin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLIVARES_EVENTING_ALLOW_LOOPBACK", "1")

	get := func(pin ...net.IP) error {
		req, rerr := http.NewRequestWithContext(
			egress.WithPin(context.Background(), pin), http.MethodGet, "http://localhost:"+port+"/", nil)
		if rerr != nil {
			t.Fatal(rerr)
		}
		resp, derr := cliGuardedClient().Do(req)
		if derr == nil {
			_ = resp.Body.Close()
		}
		return derr
	}
	if err := get(net.ParseIP("127.0.0.1")); err != nil {
		t.Fatalf("the CLI client could not reach a hostname destination: %v", err)
	}
	// The pin is enforced, so the success above is not simply an unguarded client.
	if err := get(net.ParseIP("203.0.113.9")); err == nil {
		t.Error("the CLI client connected to an address the authorization did not cover")
	}
}

// TestDuplicateDestinationNamesAreRefused. Neither "first wins" nor "last wins"
// described the old behavior, which is what made it dangerous rather than untidy:
// openAll let the first successfully opened connector win while the scope map let the
// last SCOPED definition win, so one tenant's authorization could be attached to
// another tenant's connector. And a later duplicate with no tenants key was skipped
// rather than clearing the earlier scope, giving a third rule again.
func TestDuplicateDestinationNamesAreRefused(t *testing.T) {
	a, b := model.NewTenantID().String(), model.NewTenantID().String()
	err := checkDuplicateDestinationNames([]notifyDestinationSpec{
		{Name: "soc", Kind: "webhook", Tenants: []string{a}},
		{Name: "soc", Kind: "webhook", Tenants: []string{b}},
	})
	if err == nil {
		t.Fatal("a duplicate destination name was accepted; connector identity and tenant " +
			"authorization would come from different entries")
	}
	if err := checkDuplicateDestinationNames([]notifyDestinationSpec{
		{Name: "soc-a"}, {Name: "soc-b"},
	}); err != nil {
		t.Errorf("distinct names were refused: %v", err)
	}
}

// TestAFailedReloadStillRevokes. The old connector stays live when a replacement
// cannot be installed, and that is deliberate. The old AUTHORIZATION must not: the
// operator's edit may have been a revocation, and leaving a revoked tenant able to
// send because an unrelated plugin digest failed to admit couples a security change
// to a supply-chain outcome.
//
// Narrowing is by INTERSECTION, so a failed reload can revoke and can never grant.
func TestAFailedReloadStillRevokes(t *testing.T) {
	a, b := model.NewTenantID(), model.NewTenantID()
	log := discardLog()
	d := newConnectorDispatcher(nil, nil, log)
	d.conns["soc"] = &scopeStubConn{}

	// Unscoped today; the operator narrows it to A and the replacement fails.
	d.mu.Lock()
	d.narrowTenantScope(notifyDestinationSpec{Name: "soc", Tenants: []string{a.String()}}, log)
	d.mu.Unlock()
	if err := d.Deliver(context.Background(), b, "soc", sdk.Notification{}); !errors.Is(err, notify.ErrUnknownDestination) {
		t.Errorf("a revocation was not applied because the connector reload failed: %v", err)
	}
	if err := d.Deliver(context.Background(), a, "soc", sdk.Notification{}); err != nil {
		t.Errorf("the tenant the operator kept lost access: %v", err)
	}

	// Now a MOVE from A to B with the replacement failing: neither authorization is
	// installable, so the intersection is deny-all. It must never GRANT B.
	d.mu.Lock()
	d.narrowTenantScope(notifyDestinationSpec{Name: "soc", Tenants: []string{b.String()}}, log)
	d.mu.Unlock()
	for _, tn := range []model.TenantID{a, b} {
		if err := d.Deliver(context.Background(), tn, "soc", sdk.Notification{}); !errors.Is(err, notify.ErrUnknownDestination) {
			t.Errorf("a failed reload granted access it should only be able to remove: %v", err)
		}
	}
}

// TestRevertingAFailedReloadRestoresAccess. A failed reload publishes the intersection
// of the old and desired scopes — correct, because it must never grant on failure. But
// if the operator then REVERTS the file, the connector's fingerprint matches again, the
// reconcile calls it unchanged, and nothing republishes the scope: the narrowed set
// sticks and a tenant the operator has restored stays denied.
//
// This is the recovery half of the deny-on-failure property, and it is what makes that
// property a control rather than a trap.
func TestRevertingAFailedReloadRestoresAccess(t *testing.T) {
	a, b := model.NewTenantID(), model.NewTenantID()
	log := discardLog()
	d := newConnectorDispatcher(nil, nil, log)
	d.conns["soc"] = &scopeStubConn{}

	specA := notifyDestinationSpec{Name: "soc", Tenants: []string{a.String()}}
	specB := notifyDestinationSpec{Name: "soc", Tenants: []string{b.String()}}

	d.mu.Lock()
	d.setTenantScope(specA, log) // live: tenant A
	d.narrowTenantScope(specB, log)
	d.mu.Unlock()
	// A -> B failed: the intersection is empty, so nobody may send. That is the point.
	for _, tn := range []model.TenantID{a, b} {
		if err := d.Deliver(context.Background(), tn, "soc", sdk.Notification{}); err == nil {
			t.Fatalf("a failed reload granted access to %v", tn)
		}
	}

	// The operator reverts to A. The connector never changed, so the reconcile takes
	// the unchanged branch — which must still republish the authorization.
	d.mu.Lock()
	d.setTenantScope(specA, log)
	d.mu.Unlock()
	if err := d.Deliver(context.Background(), a, "soc", sdk.Notification{}); err != nil {
		t.Errorf("reverting the file did not restore the tenant's access: %v", err)
	}
	if err := d.Deliver(context.Background(), b, "soc", sdk.Notification{}); err == nil {
		t.Error("reverting to A granted B")
	}
}
