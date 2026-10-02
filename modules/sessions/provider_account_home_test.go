// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The home-creation verb, on BOTH engines (profileBackends adds the PostgreSQL
// leg whenever OLIVARES_TEST_POSTGRES_SUPERUSER_DSN is set, and logs it as NOT
// exercised otherwise).
//
// Preservation controls: new homes use the stated modes and canonical paths;
// failures before reservation have no filesystem effects; only the ledger
// exposes absolute paths. Durable interrupted recovery is measured separately.
// Against the original pre-home slice, UseAccountsRoot is the FIRST reflective
// assertion to fail; the historical 405-first claim was incorrect.

// acctHomeDriver is the driver every case here creates an account for. The
// module operates it, so a created account can be resolved for a launch.
const acctHomeDriver = "claude"

// acctUseAccountsRoot hands the module the directory it creates account homes
// under, the way the composition root does at boot.
//
// It resolves the seam by NAME on purpose. A tree without the home builder has
// no such seam, and calling it directly would stop this whole package from
// compiling and take every unrelated test in it down with a link error; resolved
// this way the absence is one failed assertion in one test, which is what a
// red-first commit is supposed to produce.
func acctUseAccountsRoot(t *testing.T, m *Module, root string) {
	t.Helper()
	method := reflect.ValueOf(m).MethodByName("UseAccountsRoot")
	if !method.IsValid() {
		t.Fatalf("the sessions module has no UseAccountsRoot seam, so the composition root cannot tell it where account homes live")
	}
	use, ok := method.Interface().(func(string))
	if !ok {
		t.Fatalf("UseAccountsRoot is %s, want func(string)", method.Type())
	}
	use(root)
}

// acctHomeRoot makes a fresh accounts root for one test and binds it.
func acctHomeRoot(t *testing.T, m *Module) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "accounts")
	acctUseAccountsRoot(t, m, root)
	return root
}

// create asks the server for a NEW account: the product builds its home, so the
// request names a driver and, at most, a name.
func (a *acctAPI) create(driver, name string) acctResp {
	body := map[string]any{"driver": driver}
	if name != "" {
		body["name"] = name
	}
	return a.call(http.MethodPost, "/provider-accounts", body)
}

// acctCustodyDir is where an account's home lives under root: the stable tuple
// of tenant, execution environment and account reference. A name is a label and
// is deliberately not part of it.
func acctCustodyDir(root string, tenant model.TenantID, ref string) string {
	return filepath.Join(root, tenant.String(), testEnvRef, ref)
}

// acctEntries lists the directory's entry names, or nothing at all when the
// directory does not exist — which is the answer this file asks for most often.
func acctEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// acctMode reports one directory's permission bits.
func acctMode(t *testing.T, dir string) fs.FileMode {
	t.Helper()
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if !st.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}
	return st.Mode().Perm()
}

// acctStagingEntries lists durable operation custody records. Each completed
// operation retains its marker; refused reservations add no staging record.
func acctStagingEntries(t *testing.T, root string) []string {
	t.Helper()
	return acctEntries(t, filepath.Join(root, ".staging"))
}

// acctFailingData makes the account row's transaction fail while everything the
// builder did on disk has already happened. It is how a test reaches "the
// directory is in place and the write did not land" without waiting for a crash.
type acctFailingData struct {
	inner api.ModuleData
	mu    sync.Mutex
	fail  error
}

func (d *acctFailingData) failWith(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.fail = err
}

func (d *acctFailingData) current() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.fail
}

func (d *acctFailingData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.inner.View(ctx, tenant, fn)
}

func (d *acctFailingData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if err := d.current(); err != nil {
		return err
	}
	return d.inner.Mutate(ctx, tenant, fn)
}

// acctAuditedMeta is one sealed event of the account plane, with the metadata as
// the ledger STORES it. The in-memory map is dropped on every read path, so the
// stored canonical string is the only place a test can read what was recorded.
type acctAuditedMeta struct {
	targetKind model.Kind
	targetID   model.ID
	meta       map[string]any
}

func acctAuditsOf(t *testing.T, m *Module, tenant model.TenantID, action string) []acctAuditedMeta {
	t.Helper()
	ctx := context.Background()
	var out []acctAuditedMeta
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		walker, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			return errors.New("this audit log does not expose the canonical metadata it stored")
		}
		return walker.WalkCanonical(ctx, 1, func(ev model.AuditEvent, canonical string, _ []byte) error {
			if ev.Action != action {
				return nil
			}
			meta := map[string]any{}
			if err := json.Unmarshal([]byte(canonical), &meta); err != nil {
				return err
			}
			out = append(out, acctAuditedMeta{targetKind: ev.TargetKind, targetID: ev.TargetID, meta: meta})
			return nil
		})
	})
	if err != nil {
		t.Fatalf("read the %s events: %v", action, err)
	}
	return out
}

// A profile is published only after both leaves exist. A name conflict or a
// reservation-store refusal precedes all filesystem effects. Interruption AFTER
// reservation deliberately retains custody, as the recovery controls verify.
func TestProviderAccount_CreateIsTwoPhaseAndLeavesNothingOnFailure(t *testing.T) {
	for _, be := range profileBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "create-"+be.name)
			a := newAcctAPI(m, st, tenant)
			root := acctHomeRoot(t, m)
			environmentDir := filepath.Join(root, tenant.String(), testEnvRef)

			// One successful create establishes what "in place" looks like.
			r := a.create(acctHomeDriver, "claude-a1")
			if r.code != http.StatusCreated {
				t.Fatalf("create = %d %s, want 201", r.code, r.raw)
			}
			ref, _ := r.body["account_ref"].(string)
			if ref == "" {
				t.Fatalf("create answered no account reference: %s", r.raw)
			}
			home := acctCustodyDir(root, tenant, ref)
			if got := acctEntries(t, home); !reflect.DeepEqual(got, []string{accountCustodyMarker, "config", "home"}) {
				t.Fatalf("the account home holds %v, want custody marker, configuration home and user home", got)
			}
			for dir, want := range map[string]fs.FileMode{
				root:                                 0o700,
				filepath.Join(root, tenant.String()): 0o711,
				environmentDir:                       0o711,
				home:                                 0o700,
				filepath.Join(home, "config"):        0o700,
				filepath.Join(home, "home"):          0o700,
			} {
				if got := acctMode(t, dir); got != want {
					t.Fatalf("%s is %#o, want %#o", dir, got, want)
				}
			}
			if got := acctStagingEntries(t, root); len(got) != 1 {
				t.Fatalf("expected one durable custody record, found %v", got)
			}
			settled := acctEntries(t, environmentDir)
			if !reflect.DeepEqual(settled, []string{ref}) {
				t.Fatalf("the environment directory holds %v, want only %s", settled, ref)
			}

			// Failure one, with no fault injected at all: the name the operator
			// asked for is already taken, so reservation is refused before the build.
			r = a.create(acctHomeDriver, "claude-a1")
			if r.code != http.StatusConflict || !strings.Contains(r.raw, "claude-a1") {
				t.Fatalf("create with a taken name = %d %s, want 409 naming it", r.code, r.raw)
			}
			if got := acctEntries(t, environmentDir); !reflect.DeepEqual(got, settled) {
				t.Fatalf("a refused create left %v in place, want only the account that exists: %v", got, settled)
			}
			if got := acctStagingEntries(t, root); len(got) != 1 {
				t.Fatalf("refused create changed durable custody records: %v", got)
			}
			if got := acctNames(a.list("").items()); !reflect.DeepEqual(got, []string{"claude-a1"}) {
				t.Fatalf("accounts after a refused create = %v, want only [claude-a1]", got)
			}

			// Failure two: the store refuses the initial reservation. Nothing new
			// may appear on disk before that admitted reservation commits.
			failing := &acctFailingData{inner: m.data}
			m.data = failing
			failing.failWith(errors.New("the store refused this account row"))
			r = a.create(acctHomeDriver, "claude-a2")
			if r.code == http.StatusCreated {
				t.Fatalf("create = %d %s, want a refusal while the store is failing", r.code, r.raw)
			}
			m.data = failing.inner
			if got := acctEntries(t, environmentDir); !reflect.DeepEqual(got, settled) {
				t.Fatalf("a create whose write failed left %v in place, want only %v", got, settled)
			}
			if got := acctStagingEntries(t, root); len(got) != 1 {
				t.Fatalf("reservation failure changed custody records: %v", got)
			}
			if got := acctNames(a.list("").items()); !reflect.DeepEqual(got, []string{"claude-a1"}) {
				t.Fatalf("accounts after a failed write = %v, want only [claude-a1]", got)
			}

			// And the verb still works afterwards: the failure left no lock, no
			// reserved name and no directory anyone has to clean up by hand.
			r = a.create(acctHomeDriver, "")
			if r.code != http.StatusCreated || r.body["name"] != "claude" {
				t.Fatalf("create after a failure = %d %s, want 201 named claude", r.code, r.raw)
			}
			if got := len(acctEntries(t, environmentDir)); got != 2 {
				t.Fatalf("the environment directory holds %d homes, want 2", got)
			}
			if got := acctStagingEntries(t, root); len(got) != 2 {
				t.Fatalf("expected two durable custody records: %v", got)
			}
		})
	}
}

// There is ONE home resolver. The builder stores the output of the module's own
// canonicalHome, so revalidateHome — the check the launch path runs on every
// launch and every resume — accepts the stored value unchanged. A builder that
// stored the path it had joined itself would disagree the moment any component
// of the accounts root were a symbolic link, and every launch of the account it
// had just created would be refused as a home that moved.
func TestProviderAccount_CreateStoresCanonicalHomeSoRevalidateAgrees(t *testing.T) {
	for _, be := range profileBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			ctx := context.Background()
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "canonical-"+be.name)
			a := newAcctAPI(m, st, tenant)

			// The accounts root is reached through a symbolic link, which is the
			// ordinary shape of a data directory an operator has moved to another
			// volume, and the shape that tells the two resolvers apart.
			base := t.TempDir()
			real := filepath.Join(base, "volume", "accounts")
			if err := os.MkdirAll(real, 0o700); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(base, "accounts")
			if err := os.Symlink(real, link); err != nil {
				t.Fatal(err)
			}
			resolved, err := filepath.EvalSymlinks(real)
			if err != nil {
				t.Fatal(err)
			}
			acctUseAccountsRoot(t, m, link)

			r := a.create(acctHomeDriver, "claude-c1")
			if r.code != http.StatusCreated {
				t.Fatalf("create = %d %s, want 201", r.code, r.raw)
			}
			ref, _ := r.body["account_ref"].(string)
			row := acctRow(t, m, tenant, ref)
			storedConfig, storedUser := row.String(colPPConfigHome), row.String(colPPUserHome)

			// What the row holds is exactly what canonicalHome answers for the
			// directory the builder published, field name included.
			for _, c := range []struct{ field, stored, built string }{
				{"config_home", storedConfig, filepath.Join(link, tenant.String(), testEnvRef, ref, "config")},
				{"user_home", storedUser, filepath.Join(link, tenant.String(), testEnvRef, ref, "home")},
			} {
				want, cerr := canonicalHome(c.field, c.built)
				if cerr != nil {
					t.Fatalf("canonicalHome(%s, %s): %v", c.field, c.built, cerr)
				}
				if c.stored != want {
					t.Fatalf("the row stores %s = %q, want canonicalHome's own answer %q", c.field, c.stored, want)
				}
				// The same fact said the other way round: the launch path's check
				// accepts the stored value with no complaint at all.
				if rerr := revalidateHome(c.field, c.stored); rerr != nil {
					t.Fatalf("revalidateHome(%s, %q) = %v, want the launch path to agree", c.field, c.stored, rerr)
				}
				if !strings.HasPrefix(c.stored, resolved+string(os.PathSeparator)) {
					t.Fatalf("the stored %s %q is not under the resolved accounts root %q", c.field, c.stored, resolved)
				}
			}
			if storedConfig == storedUser {
				t.Fatalf("the configuration home and the user home are one directory: %q", storedConfig)
			}

			// And the whole launch resolution agrees, which is the property the
			// account was created for: same homes, no conflict, no second answer.
			snap, _, err := m.resolveLaunchProfile(ctx, tenant, ref)
			if err != nil {
				t.Fatalf("resolve the launch of a freshly created account = %v, want it to resolve", err)
			}
			if snap.ConfigHome != storedConfig || snap.UserHome != storedUser {
				t.Fatalf("the launch resolved %q/%q, want the stored %q/%q",
					snap.ConfigHome, snap.UserHome, storedConfig, storedUser)
			}
			if snap.Driver != acctHomeDriver || snap.EnvironmentRef != testEnvRef {
				t.Fatalf("the launch resolved driver %q on %q, want %q on %q",
					snap.Driver, snap.EnvironmentRef, acctHomeDriver, testEnvRef)
			}

			// The row is a MANAGED home of this environment, and it keeps the home
			// slot the profile plane already guards.
			if got := row.String(colPPHomeMode); got != AccountHomeManaged {
				t.Fatalf("home_mode = %q, want %q", got, AccountHomeManaged)
			}
			if got := row.String(colPPHomeSlot); got != activeHomeSlot(testEnvRef, acctHomeDriver, storedConfig) {
				t.Fatalf("home_slot = %q, want the profile plane's own encoding of the canonical home", got)
			}
			if got := row.String(colPPIsolationLevel); got != AccountIsolationShared {
				t.Fatalf("isolation_level = %q, want %q until a dedicated user is wired", got, AccountIsolationShared)
			}
			if !row.IsNull(colPPOSUser) {
				t.Fatalf("os_user = %v on a shared home", row[colPPOSUser])
			}
		})
	}
}

// The absolute home path goes to the ledger, which is the one reader entitled to
// it, and to nothing else. Every account a caller reads carries the home as a
// RELATIVE path under the accounts root — enough to say which directory it is
// without saying where the node keeps it.
func TestProviderAccount_CreateAuditsWithTheAbsolutePathAndTheDtoDoesNot(t *testing.T) {
	for _, be := range profileBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "audit-"+be.name)
			a := newAcctAPI(m, st, tenant)
			root := acctHomeRoot(t, m)

			r := a.create(acctHomeDriver, "claude-d1")
			if r.code != http.StatusCreated {
				t.Fatalf("create = %d %s, want 201", r.code, r.raw)
			}
			ref, _ := r.body["account_ref"].(string)
			row := acctRow(t, m, tenant, ref)
			storedConfig, storedUser := row.String(colPPConfigHome), row.String(colPPUserHome)
			if storedConfig == "" || !filepath.IsAbs(storedConfig) {
				t.Fatalf("the row stores config_home = %q, want an absolute path", storedConfig)
			}

			// The ledger: exactly one creation event, naming both absolute homes.
			events := acctAuditsOf(t, m, tenant, "sessions.provider_account.create")
			if len(events) != 1 {
				t.Fatalf("the ledger holds %d creation events, want exactly 1", len(events))
			}
			ev := events[0]
			if ev.targetKind != providerProfileKind || ev.targetID != model.ID(row.String(model.ColID)) {
				t.Fatalf("the creation event targets %s %s, want %s %s",
					ev.targetKind, ev.targetID, providerProfileKind, row.String(model.ColID))
			}
			for key, want := range map[string]string{
				"profile_ref":     ref,
				"name":            "claude-d1",
				"driver":          acctHomeDriver,
				"environment_ref": testEnvRef,
				"home_mode":       AccountHomeManaged,
				"isolation_level": AccountIsolationShared,
				"config_home":     storedConfig,
				"user_home":       storedUser,
			} {
				if got, _ := ev.meta[key].(string); got != want {
					t.Fatalf("the creation event records %s = %q, want %q (meta %v)", key, got, want, ev.meta)
				}
			}
			if _, ok := ev.meta["os_user"]; ok {
				t.Fatalf("the creation event records an os_user on a shared home: %v", ev.meta)
			}

			// Every read: the relative home, and no absolute path anywhere.
			wantRelative := strings.Join([]string{tenant.String(), testEnvRef, ref}, "/")
			get := a.call(http.MethodGet, "/provider-accounts/"+ref, nil)
			if get.code != http.StatusOK {
				t.Fatalf("get = %d %s", get.code, get.raw)
			}
			list := a.list("")
			for _, read := range []struct {
				what string
				resp acctResp
			}{{"the create answer", r}, {"the get", get}, {"the list", list}} {
				if strings.Contains(read.resp.raw, storedConfig) || strings.Contains(read.resp.raw, storedUser) ||
					strings.Contains(read.resp.raw, root) {
					t.Fatalf("%s carries an absolute home path: %s", read.what, read.resp.raw)
				}
			}
			for _, read := range []struct {
				what string
				body map[string]any
			}{{"the create answer", r.body}, {"the get", get.body}, {"the listed row", list.items()[0]}} {
				got, _ := read.body["home_relative"].(string)
				if got != wantRelative {
					t.Fatalf("%s carries home_relative = %q, want %q", read.what, got, wantRelative)
				}
				if filepath.IsAbs(got) {
					t.Fatalf("%s carries an absolute home_relative %q", read.what, got)
				}
			}

			// An ADOPTED home is the operator's own directory somewhere else, so it
			// has no path under the accounts root and the field stays empty rather
			// than being invented.
			adopted := acctProfiles(t, m, tenant, acctHomeDriver, 1)[0]
			ar := a.adopt(adopted.Ref, "claude-d2")
			if ar.code != http.StatusOK {
				t.Fatalf("adopt = %d %s", ar.code, ar.raw)
			}
			if got, _ := ar.body["home_relative"].(string); got != "" {
				t.Fatalf("an adopted account carries home_relative = %q, want none", got)
			}
			if got, _ := ar.body["home_mode"].(string); got != AccountHomeAdopted {
				t.Fatalf("an adopted account records home_mode = %q, want %q", got, AccountHomeAdopted)
			}
		})
	}
}

// ARCH smoke on refresh 03b: an account made through /provider-accounts left its
// profile with no auth source, so a session started from it was refused like
// HU-01. Its profile now signs in with the tool's own login in the account's homes.
func TestProviderAccount_CreateGivesAProfileThatSignsInWithTheToolsOwnLogin(t *testing.T) {
	be := profileBackends(t)[0]
	m, st := openProfileModule(t, be, nil)
	tenant := ensureTenant(t, st, "account-auth-source")
	a := newAcctAPI(m, st, tenant)
	acctUseAccountsRoot(t, m, t.TempDir())
	r := a.create(acctHomeDriver, "claude-own-login")
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s, want 201", r.code, r.raw)
	}
	ref, _ := r.body["account_ref"].(string)
	if got := acctRow(t, m, tenant, ref).String(colPPAuthSource); got != AuthSourceAccountHome {
		t.Fatalf("account profile auth_source = %q, want %q", got, AuthSourceAccountHome)
	}
	if err := requireAuthSourceForDriver(acctHomeDriver, AuthSourceAccountHome); err != nil {
		t.Fatalf("the account profile still cannot launch: %v", err)
	}
}
