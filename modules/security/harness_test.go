// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// harness wires a fully-usable security plane: a real in-memory store, the module
// bound to a bus, an HTTP server, and a signer whose public key the module uses to
// verify checkpoints (so forensic integrity is exercisable).
type harness struct {
	t        *testing.T
	srv      *api.Server
	st       store.Store
	bus      eventbus.Bus
	signer   *audit.Signer
	setupTok string

	findMu   sync.Mutex
	findings []sdkmodel.FindingReport
}

// newHarness builds the harness. optFn (optional) maps the harness signer's public
// key to module options so a test can wire the correct (or a wrong) checkpoint key.
func newHarness(t *testing.T, optFn func(signerPub ed25519.PublicKey) []Option) *harness {
	var fn func(*audit.Signer) []Option
	if optFn != nil {
		fn = func(s *audit.Signer) []Option { return optFn(s.PublicKey()) }
	}
	return newHarnessSigner(t, nil, fn)
}

// newHarnessSigner additionally takes audit signer options and hands the whole
// SIGNER to optFn, so a test can run the ledger with an OFF-BOX checkpoint key
// and wire WithCheckpointVerifierSource(signer.CheckpointVerifier) — the
// integrity coverage for off-box-signed checkpoints.
func newHarnessSigner(t *testing.T, signerOpts []audit.Option, optFn func(s *audit.Signer) []Option) *harness {
	t.Helper()
	ctx := context.Background()
	h := &harness{t: t}

	_, priv, _ := ed25519.GenerateKey(nil)
	signer, err := audit.NewSigner(priv, signerOpts...)
	if err != nil {
		t.Fatal(err)
	}
	h.signer = signer

	var opts []Option
	if optFn != nil {
		opts = optFn(signer)
	} else {
		opts = []Option{WithCheckpointKey(signer.PublicKey())}
	}
	mod := New(opts...)

	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, mod.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	h.st = st
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	mod.UseData(api.NewModuleData(st))

	bus := eventbus.NewInProc(eventbus.Options{})
	h.bus = bus
	if _, err := bus.Subscribe([]event.Type{event.TypeFindingReported}, func(_ context.Context, e event.Event) error {
		if f, ok := event.FindingOf(e); ok {
			h.findMu.Lock()
			h.findings = append(h.findings, f)
			h.findMu.Unlock()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rt := runtime.New(runtime.Options{Bus: bus})
	if err := rt.AddModule(mod, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Stop(ctx); _ = bus.Close() })

	tok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	plaintext, _, err := tok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(api.Options{
		Store: st, Authenticator: auth.NewAuthenticator(st, nil), Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: tok, Version: "test", Modules: []api.Module{mod},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.srv, h.setupTok = srv, plaintext
	return h
}

type resp struct {
	code int
	body map[string]any
	raw  string
}

func (h *harness) do(method, path, token string, body any, hdr map[string]string) resp {
	h.t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.RemoteAddr = "10.0.0.1:1234"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	out := resp{code: rec.Code, raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

func tenantHdr(t model.TenantID) map[string]string {
	return map[string]string{"X-Olivares-Tenant": t.String()}
}

func (h *harness) adminLogin() string {
	h.t.Helper()
	if r := h.do("POST", "/v1/setup", "", map[string]any{"token": h.setupTok, "email": "root@x.io", "password": "supersecret1"}, nil); r.code != http.StatusCreated {
		h.t.Fatalf("setup = %d %s", r.code, r.raw)
	}
	r := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "root@x.io", "password": "supersecret1"}, nil)
	if r.code != http.StatusOK {
		h.t.Fatalf("login = %d %s", r.code, r.raw)
	}
	return r.body["token"].(string)
}

func (h *harness) createOrg(token, slug string) model.TenantID {
	h.t.Helper()
	r := h.do("POST", "/v1/system/orgs", token, map[string]any{"name": slug, "slug": slug}, nil)
	if r.code != http.StatusCreated {
		h.t.Fatalf("create org %s = %d %s", slug, r.code, r.raw)
	}
	return model.TenantID(r.body["tenant_id"].(string))
}

func (h *harness) roleToken(admin string, tenant model.TenantID, email, role string) string {
	h.t.Helper()
	r := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": "memberpass1"}, nil)
	if r.code != http.StatusCreated {
		h.t.Fatalf("create user = %d %s", r.code, r.raw)
	}
	uid := r.body["id"].(string)
	if r := h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tenant.String(), "role": role}, nil); r.code != http.StatusCreated {
		h.t.Fatalf("grant = %d %s", r.code, r.raw)
	}
	r = h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "memberpass1"}, nil)
	if r.code != http.StatusOK {
		h.t.Fatalf("login = %d %s", r.code, r.raw)
	}
	return r.body["token"].(string)
}

// publishFinding emits a FindingReport on the bus from a non-module source, so the
// module's anomaly reactor processes it (e.g. an anti_evasion mark).
func (h *harness) publishFinding(tenant model.TenantID, kind string, sev sdkmodel.Severity, subjectKind, subjectRef, title string) {
	h.t.Helper()
	_ = h.bus.Publish(context.Background(), event.FromObservation(tenant.String(), "connector:test", sdkmodel.FindingReport{
		Kind: kind, Severity: sev, SubjectKind: subjectKind, SubjectRef: subjectRef, Title: title,
		DetailHash: hashHex(kind + "|" + subjectRef), OccurredAt: time.Now(),
	}))
}

// waitForFinding polls the captured bus findings for one of the given kind.
func (h *harness) waitForFinding(kind string) bool {
	h.t.Helper()
	for i := 0; i < 200; i++ {
		h.findMu.Lock()
		for _, f := range h.findings {
			if f.Kind == kind {
				h.findMu.Unlock()
				return true
			}
		}
		h.findMu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// checkpointTenant signs a checkpoint over the tenant's current ledger head.
func (h *harness) checkpointTenant(tenant model.TenantID) {
	h.t.Helper()
	if _, _, err := h.signer.Checkpoint(context.Background(), h.st, tenant); err != nil {
		h.t.Fatalf("checkpoint = %v", err)
	}
}
