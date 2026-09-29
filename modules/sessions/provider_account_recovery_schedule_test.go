// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func accountRecoveryOperation(t *testing.T, m *Module, tenant model.TenantID, key string) model.Record {
	t.Helper()
	var result model.Record
	err := m.data.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(accountHomeOperationKind)
		if err != nil {
			return err
		}
		op, found, err := accountOperationByKey(context.Background(), repo, testEnvRef, key)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("durable account reservation missing")
		}
		result = op
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// Controlled interruption returns after the exact physical/committed boundary.
// Reopening the real store discards Module state; recovery uses only persisted
// reservation and filesystem ownership, not a retained Go build structure.
func TestProviderAccount_RecoveryAcrossRestart(t *testing.T) {
	for _, phase := range []string{"reserved", "staged", "published", "committed"} {
		t.Run(phase, func(t *testing.T) {
			for _, be := range profileBackends(t) {
				t.Run(be.name, func(t *testing.T) {
					m, st := openProfileModule(t, be, nil)
					tenant := ensureTenant(t, st, "restart")
					root := acctHomeRoot(t, m)
					m.accountHomeCheckpoint = func(at string) error {
						if at == phase {
							return errors.New("controlled interruption")
						}
						return nil
					}
					a := newAcctAPI(m, st, tenant)
					body := map[string]any{"driver": "claude", "idempotency_key": "restart-key"}
					interrupted := a.call("POST", "/provider-accounts", body)
					if interrupted.code != 503 {
						t.Fatalf("interruption=%d %s", interrupted.code, interrupted.raw)
					}
					op := accountRecoveryOperation(t, m, tenant, "restart-key")
					ref := op.String(colHORef)
					if phase != "committed" {
						if got := len(a.list("").items()); got != 0 {
							t.Fatalf("pending operation became launchable: %d accounts", got)
						}
					}
					if err := st.Close(); err != nil {
						t.Fatal(err)
					}
					fresh, reopened := openProfileModule(t, be, nil)
					fresh.UseAccountsRoot(root)
					got := newAcctAPI(fresh, reopened, tenant).call("POST", "/provider-accounts", body)
					if got.code != 201 || got.body["account_ref"] != ref {
						t.Fatalf("restart retry=%d %s, want the reserved %s", got.code, got.raw, ref)
					}
					if n := len(acctEntries(t, filepath.Join(root, tenant.String(), testEnvRef))); n != 1 {
						t.Fatalf("recovery produced %d homes", n)
					}
					if got := len(acctAuditsOf(t, fresh, tenant, "sessions.provider_account.create")); got != 1 {
						t.Fatalf("recovery create audits=%d, want one", got)
					}
				})
			}
		})
	}
}

func TestProviderAccount_UnownedPathsAndAliasRetarget(t *testing.T) {
	for _, fault := range []string{"unmarked-stage", "occupied-final", "root-alias-retarget"} {
		t.Run(fault, func(t *testing.T) {
			for _, be := range profileBackends(t) {
				t.Run(be.name, func(t *testing.T) {
					m, st := openProfileModule(t, be, nil)
					tenant := ensureTenant(t, st, "occupied")
					root := acctHomeRoot(t, m)
					configured := root
					if fault == "root-alias-retarget" {
						if err := os.Mkdir(root, 0700); err != nil {
							t.Fatal(err)
						}
						configured = filepath.Join(t.TempDir(), "root-link")
						if err := os.Symlink(root, configured); err != nil {
							t.Fatal(err)
						}
						m.UseAccountsRoot(configured)
					}
					m.accountHomeCheckpoint = func(phase string) error {
						if phase == "reserved" {
							return errors.New("park reservation")
						}
						return nil
					}
					body := map[string]any{"driver": "claude", "idempotency_key": "occupancy-key"}
					a := newAcctAPI(m, st, tenant)
					if r := a.call("POST", "/provider-accounts", body); r.code != 503 {
						t.Fatalf("reservation interruption=%d %s", r.code, r.raw)
					}
					op := accountRecoveryOperation(t, m, tenant, "occupancy-key")
					var sentinel string
					switch fault {
					case "unmarked-stage":
						sentinel = filepath.Join(root, ".staging", op.String(colHOToken), "sentinel")
					case "occupied-final":
						sentinel = filepath.Join(acctCustodyDir(root, tenant, op.String(colHORef)), "sentinel")
					case "root-alias-retarget":
						other := t.TempDir()
						if err := os.Remove(configured); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(other, configured); err != nil {
							t.Fatal(err)
						}
						sentinel = filepath.Join(other, "sentinel")
					}
					if err := os.MkdirAll(filepath.Dir(sentinel), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(sentinel, []byte("preserve"), 0600); err != nil {
						t.Fatal(err)
					}
					m.accountHomeCheckpoint = nil
					if r := a.call("POST", "/provider-accounts", body); r.code != 409 {
						t.Fatalf("%s=%d %s, want custody refusal", fault, r.code, r.raw)
					}
					if b, err := os.ReadFile(sentinel); err != nil || string(b) != "preserve" {
						t.Fatalf("sentinel changed: %q %v", b, err)
					}
					if got := len(a.list("").items()); got != 0 {
						t.Fatalf("ambiguous ownership registered %d accounts", got)
					}
				})
			}
		})
	}
}

// The callback pauses after publication while A owns the transaction lock. B
// has a bounded context; both its register and adopt paths must fail to acquire
// managed custody, and A's home must survive even if A returns an error.
func TestProviderAccount_PublishedHomeCannotBeStolen(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "parked")
			root := acctHomeRoot(t, m)
			// A legacy profile exists before the namespace is configured. Adoption must
			// recheck the namespace, rather than assuming its old row establishes custody.
			legacyHome := filepath.Join(root, "legacy")
			if err := os.MkdirAll(legacyHome, 0700); err != nil {
				t.Fatal(err)
			}
			m.UseAccountsRoot("")
			legacy := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "codex", ConfigHome: legacyHome, UserHome: legacyHome})
			m.UseAccountsRoot(root)
			published, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			var rendezvousExpired atomic.Bool
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(func() {
				unblock()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("account worker did not join at cleanup")
				}
			})
			m.accountHomeCheckpoint = func(phase string) error {
				if phase == "published" {
					close(published)
					select {
					case <-release:
						return errors.New("A interrupted after publication")
					case <-time.After(5 * time.Second):
						rendezvousExpired.Store(true)
						return errors.New("bounded rendezvous failed")
					}
				}
				return nil
			}
			go func() {
				defer close(done)
				_, err := m.CreateProviderAccount(context.Background(), testActor(), tenant, CreateProviderAccountInput{Driver: "claude", IdempotencyKey: "parked-key"})
				done <- err
			}()
			select {
			case <-published:
			case <-time.After(5 * time.Second):
				unblock()
				t.Fatal("A did not reach publication")
			}
			// The reservation committed before publication; use its directory listing
			// rather than a database read that SQLite correctly serializes behind A.
			refs := acctEntries(t, filepath.Join(root, tenant.String(), testEnvRef))
			if len(refs) != 1 {
				unblock()
				t.Fatalf("published homes=%v", refs)
			}
			home := acctCustodyDir(root, tenant, refs[0])
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			_, err := m.CreateProfile(ctx, tenant, CreateProfileInput{Driver: "codex", ConfigHome: filepath.Join(home, "config"), UserHome: filepath.Join(home, "home")})
			cancel()
			if err == nil {
				unblock()
				t.Fatal("B registered A's managed home")
			}
			ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
			_, err = m.AdoptProviderAccount(ctx, testActor(), tenant, legacy.Ref, "stolen")
			cancel()
			if err == nil {
				unblock()
				t.Fatal("B adopted a managed namespace row")
			}
			unblock()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("A interruption disappeared")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("A did not join")
			}
			m.accountHomeCheckpoint = nil
			if rendezvousExpired.Load() {
				t.Fatal("rendezvous timed out; no behavioral verdict")
			}
			// The canceled contenders above only exercise serialization. This completed
			// call is the causal custody assertion; a timeout is not counted as a refusal.
			_, err = m.CreateProfile(context.Background(), tenant, CreateProfileInput{Driver: "codex", ConfigHome: filepath.Join(home, "config"), UserHome: filepath.Join(home, "home")})
			if !errors.Is(err, errManagedHomeRegistration) {
				t.Fatalf("pending managed home registration = %v, want the custody refusal", err)
			}
			if _, err := os.Stat(filepath.Join(home, "config")); err != nil {
				t.Fatalf("A's failed transaction removed home: %v", err)
			}
			if _, err := m.AdoptProviderAccount(context.Background(), testActor(), tenant, legacy.Ref, "stolen"); !errors.Is(err, errManagedHomeRegistration) {
				t.Fatalf("pending namespace adoption = %v, want custody refusal", err)
			}
			got, err := m.CreateProviderAccount(context.Background(), testActor(), tenant, CreateProviderAccountInput{Driver: "claude", IdempotencyKey: "parked-key"})
			if err != nil || got.Ref != refs[0] {
				t.Fatalf("A retry=%+v %v", got, err)
			}
		})
	}
}

// An adapter can report an error after the real commit. The lifecycle must not
// remove the published home or mint another account when the client retries.
type accountCommitUnknownData struct {
	inner     api.ModuleData
	mutations int
}

func (d *accountCommitUnknownData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.inner.View(ctx, tenant, fn)
}
func (d *accountCommitUnknownData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	err := d.inner.Mutate(ctx, tenant, fn)
	d.mutations++
	if err == nil && d.mutations == 2 {
		return errors.New("commit reply lost")
	}
	return err
}
func TestProviderAccount_UnknownCommitKeepsHome(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "unknown")
			root := acctHomeRoot(t, m)
			wrapped := &accountCommitUnknownData{inner: m.data}
			m.data = wrapped
			a := newAcctAPI(m, st, tenant)
			body := map[string]any{"driver": "claude", "idempotency_key": "unknown-key"}
			if r := a.call("POST", "/provider-accounts", body); r.code != 503 {
				t.Fatalf("uncertain commit=%d %s", r.code, r.raw)
			}
			m.data = wrapped.inner
			op := accountRecoveryOperation(t, m, tenant, "unknown-key")
			r := a.call("POST", "/provider-accounts", body)
			if r.code != 201 || r.body["account_ref"] != op.String(colHORef) {
				t.Fatalf("uncertain retry=%d %s", r.code, r.raw)
			}
			if _, err := os.Stat(filepath.Join(acctCustodyDir(root, tenant, op.String(colHORef)), "home")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Separate operations remain independent despite sharing the name allocator.
func TestProviderAccount_ConcurrentGeneratedNames(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "concurrent-homes")
			acctHomeRoot(t, m)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			const workers = 4
			type result struct {
				account ProviderAccount
				err     error
			}
			results := make(chan result, workers)
			start := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(workers)
			for i := 0; i < workers; i++ {
				go func() {
					defer wg.Done()
					<-start
					account, err := m.CreateProviderAccount(ctx, testActor(), tenant, CreateProviderAccountInput{Driver: "claude"})
					results <- result{account, err}
				}()
			}
			joined := make(chan struct{})
			go func() { wg.Wait(); close(joined) }()
			t.Cleanup(func() {
				cancel()
				select {
				case <-joined:
				case <-time.After(5 * time.Second):
					t.Error("concurrent account writers did not join")
				}
			})
			close(start)
			names := map[string]bool{}
			refs := map[string]bool{}
			for i := 0; i < workers; i++ {
				select {
				case result := <-results:
					if result.err != nil {
						t.Fatalf("independent generated create: %v", result.err)
					}
					if names[result.account.Name] || refs[result.account.Ref] {
						t.Fatalf("two operations share account/name: %+v", result.account)
					}
					names[result.account.Name] = true
					refs[result.account.Ref] = true
				case <-ctx.Done():
					t.Fatal("concurrent account writers exceeded bound")
				}
			}
			if len(names) != workers {
				t.Fatalf("created names=%v", names)
			}
		})
	}
}

func TestProviderAccount_AliasRetargetAfterPublication(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "retarget-publish")
			root, other := t.TempDir(), t.TempDir()
			alias := filepath.Join(t.TempDir(), "accounts-link")
			if err := os.Symlink(root, alias); err != nil {
				t.Fatal(err)
			}
			m.UseAccountsRoot(alias)
			m.accountHomeCheckpoint = func(phase string) error {
				if phase != "published" {
					return nil
				}
				if err := os.Remove(alias); err != nil {
					return err
				}
				return os.Symlink(other, alias)
			}
			a := newAcctAPI(m, st, tenant)
			body := map[string]any{"driver": "claude", "idempotency_key": "retarget-after-publish"}
			r := a.call("POST", "/provider-accounts", body)
			if r.code != 409 {
				t.Fatalf("retarget after publication=%d %s", r.code, r.raw)
			}
			if n := len(a.list("").items()); n != 0 {
				t.Fatalf("retarget registered %d accounts", n)
			}
			if got := acctEntries(t, other); len(got) != 0 {
				t.Fatalf("alias target gained contents: %v", got)
			}
			op := accountRecoveryOperation(t, m, tenant, "retarget-after-publish")
			if _, err := os.Stat(filepath.Join(acctCustodyDir(root, tenant, op.String(colHORef)), "config")); err != nil {
				t.Fatalf("original owned home lost: %v", err)
			}
			if err := os.Remove(alias); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(root, alias); err != nil {
				t.Fatal(err)
			}
			m.accountHomeCheckpoint = nil
			r = a.call("POST", "/provider-accounts", body)
			if r.code != 201 || r.body["account_ref"] != op.String(colHORef) {
				t.Fatalf("restored alias retry=%d %s", r.code, r.raw)
			}
		})
	}
}
