// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/residency"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
	"github.com/olivaresai/olivares/modules/models"
	"github.com/olivaresai/olivares/sdk"
)

// countTokensDoer answers any POST (the /v1/messages/count_tokens pre-flight) with a tiny
// input-token count, so the context-window gate passes without a real upstream.
type countTokensDoer struct{}

func (countTokensDoer) Do(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"input_tokens":5}`)), Header: http.Header{}}, nil
}

// provisionTenant opens an in-memory store with the inferenceproxy schema and creates one
// org with the given residency pin (empty = unpinned).
func provisionTenant(t *testing.T, ipx *inferenceproxy.Module, dataRegion string) (store.Store, model.TenantID) {
	return provisionTenantWithConfig(t, ipx, dataRegion, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"})
}

func provisionTenantWithConfig(t *testing.T, ipx *inferenceproxy.Module, dataRegion string, cfg store.Config) (store.Store, model.TenantID) {
	t.Helper()
	ctx := context.Background()
	st, err := coreengine.Open(ctx, cfg, ipx.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive, DataRegion: dataRegion})
		if e != nil {
			return e
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}
	ipx.UseData(api.NewModuleData(st))
	return st, tenant
}

func provisionTenantThenReopenDegraded(
	t *testing.T,
	ipx *inferenceproxy.Module,
	dataRegion string,
) (store.Store, model.TenantID) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "inferenceproxy-anchor-degrade.db")
	seed, tenant := provisionTenantWithConfig(t, ipx, dataRegion, store.Config{
		Engine: store.EngineSQLite, DSN: dsn,
	})
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}
	st, err := coreengine.Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: dsn,
		AuditSpoolMaxBytes: 1, AuditSpoolOnFull: store.AuditSpoolDegrade,
	}, ipx.RegisterSchema)
	if err != nil {
		t.Fatalf("reopen degraded store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ipx.UseData(api.NewModuleData(st))
	return st, tenant
}

func TestAnchorIntentRequiresPersistedEvidence(t *testing.T) {
	eff := sha256.Sum256([]byte("frozen-forward-bytes"))
	newSubject := func(tenant model.TenantID) *proxySession {
		return &proxySession{
			tenant: tenant, actor: "user:u1", actorKind: "user",
			modelRef: "claude-opus-4-8", requestRef: model.NewID().String(),
			// The effective digest is the F3 forward-bytes digest; it is the
			// EffectDigest the F9 evidence binds to (Authorize always sets it before
			// anchorIntent). Without it the binding is invalid and the call fails closed.
			effectiveDigest: eff[:],
		}
	}

	// pendingDrops reads the GLOBAL durable loss-accounting counter. Provisioning happens
	// before the tiny budget is enabled because org.create now requires persisted evidence;
	// the assertion remains a DELTA across the anchor call so only that call can satisfy it.
	pendingDrops := func(st store.Store) int64 {
		t.Helper()
		status, _, serr := st.(store.AuditSpoolStatuser).AuditSpoolStatus(context.Background())
		if serr != nil {
			t.Fatalf("spool status: %v", serr)
		}
		return status.PendingDrops
	}

	// This is the F9 regression for the rollback bug: a DEGRADE-mode drop must COMMIT
	// the durable loss accounting AND deny evidence-or-refuse. The buggy idiom (return the
	// sentinel from INSIDE Mutate on Seq==0) rolled the transaction back, so the drop the
	// anchor itself caused never persisted and the signed gap marker never sealed — while
	// the caller still denied, masking the lost accounting. The DELTA catches exactly that.
	t.Run("degrade drop commits the gap then refuses", func(t *testing.T) {
		ipx := inferenceproxy.New()
		st, tenant := provisionTenantThenReopenDegraded(t, ipx, "")
		d := &inferenceProxyDecider{surface: "direct", store: st}
		before := pendingDrops(st)
		err := d.anchorIntent(context.Background(), newSubject(tenant))
		// (a) evidence-or-refuse: the mandatory recording is denied with the classified fault.
		var refused errEvidenceRefused
		if !errors.As(err, &refused) || refused.fault != sdk.EvidenceFaultSpoolDegraded {
			t.Fatalf("anchorIntent error = %v, want errEvidenceRefused{spool_degraded}", err)
		}
		// (b) the FIX: the anchor's OWN drop is durable — the counter advanced by this call,
		// not rolled back (the :869 bug would leave after == before).
		if after := pendingDrops(st); after <= before {
			t.Fatalf("declared gap was rolled back (regression of the :869 bug): PendingDrops before=%d after=%d, want after>before", before, after)
		}
	})

	// The batch intent leg shares the discipline; assert its delta too.
	t.Run("batch degrade drop commits the gap then refuses", func(t *testing.T) {
		ipx := inferenceproxy.New()
		st, tenant := provisionTenantThenReopenDegraded(t, ipx, "")
		d := &inferenceProxyDecider{surface: "direct", store: st}
		before := pendingDrops(st)
		err := d.anchorBatchIntent(context.Background(), newSubject(tenant), 3)
		var refused errEvidenceRefused
		if !errors.As(err, &refused) || refused.fault != sdk.EvidenceFaultSpoolDegraded {
			t.Fatalf("anchorBatchIntent error = %v, want errEvidenceRefused{spool_degraded}", err)
		}
		if after := pendingDrops(st); after <= before {
			t.Fatalf("batch declared gap rolled back: PendingDrops before=%d after=%d, want after>before", before, after)
		}
	})

	t.Run("healthy store passes", func(t *testing.T) {
		ipx := inferenceproxy.New()
		st, tenant := provisionTenant(t, ipx, "")
		d := &inferenceProxyDecider{surface: "direct", store: st}
		if err := d.anchorIntent(context.Background(), newSubject(tenant)); err != nil {
			t.Fatalf("anchorIntent error = %v, want nil", err)
		}
	})

	t.Run("no ledger deny-closed", func(t *testing.T) {
		d := &inferenceProxyDecider{surface: "direct", store: nil}
		if err := d.anchorIntent(context.Background(), newSubject("t-nostore")); !errors.Is(err, ErrNoLedger) {
			t.Fatalf("nil store anchorIntent error = %v, want ErrNoLedger", err)
		}
	})
}

func storeBackedDecider(st store.Store, tenant model.TenantID, ipx *inferenceproxy.Module, surfaceGeo string, reg *residency.Registry, inf *claudeapi.Inference) *inferenceProxyDecider {
	return &inferenceProxyDecider{
		surface: "direct", surfaceGeo: surfaceGeo,
		inf:        inf,
		authr:      fakeProxyAuthr{p: auth.ScopedPrincipal(model.ID("u1"), "u1", tenant, "editor")},
		models:     &fakeProxyModels{v: models.ModelAccessVerdict{Allowed: true}},
		budget:     &fakeProxyBudget{bc: finops.BudgetCheck{Allowed: true}},
		killSwitch: fakeProxyKill{},
		policy:     ipx,
		residency:  reg,
		store:      st,
		bus:        nil,
		clock:      time.Now,
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// TestProxyResidencyBlocksCrossRegion proves the residency gate denies pre-forward when
// the tenant is pinned to a region the surface's geo does not match — against a REAL org
// pin read from the store (the only gate that needs one). residency.InferenceGeoCompatible
// is strict: an "eu"-pinned tenant on a "us" surface is incompatible.
func TestProxyResidencyBlocksCrossRegion(t *testing.T) {
	ipx := inferenceproxy.New()
	st, tenant := provisionTenant(t, ipx, "eu")
	reg, err := residency.NewRegistry("us", []string{"us", "eu"})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	if !reg.Enforces() {
		t.Fatal("registry must enforce when a home region is set")
	}
	// inf is nil: residency (gate 2) runs before the context-window gate (gate 4), so a
	// cross-region request is denied before count_tokens is ever reached.
	d := storeBackedDecider(st, tenant, ipx, "us", reg, nil)
	dec := d.Authorize(context.Background(), userReq("hello", false), "bearer")
	if dec.Allow || dec.Status != http.StatusForbidden {
		t.Fatalf("eu-pinned tenant on a us surface must be denied 403; got allow=%v status=%d reason=%q", dec.Allow, dec.Status, dec.Reason)
	}
}

// TestProxyResidencyAllowsInRegion confirms the gate does NOT block when the surface geo
// matches the pin.
func TestProxyResidencyAllowsInRegion(t *testing.T) {
	ipx := inferenceproxy.New()
	st, tenant := provisionTenant(t, ipx, "eu")
	reg, _ := residency.NewRegistry("eu", []string{"us", "eu"})
	inf := claudeapi.NewInference(claudeapi.InferenceConfig{BaseURL: "https://api.anthropic.com", APIKey: "op", Gateway: "direct", Doer: countTokensDoer{}})
	d := storeBackedDecider(st, tenant, ipx, "eu", reg, inf)
	dec := d.Authorize(context.Background(), userReq("hello", false), "bearer")
	if !dec.Allow {
		t.Fatalf("eu-pinned tenant on an eu surface must be allowed; got deny status=%d reason=%q", dec.Status, dec.Reason)
	}
}

// TestProxyDefaultDLPBlocksPlaintextAndBase64Secrets attacks a stock tenant with no
// DLP rows. Both a direct credential shape and the same bytes base64-wrapped must be
// denied before forwarding.
func TestProxyDefaultDLPBlocksPlaintextAndBase64Secrets(t *testing.T) {
	ipx := inferenceproxy.New()
	st, tenant := provisionTenant(t, ipx, "")
	inf := claudeapi.NewInference(claudeapi.InferenceConfig{BaseURL: "https://api.anthropic.com", APIKey: "op", Gateway: "direct", Doer: countTokensDoer{}})
	d := storeBackedDecider(st, tenant, ipx, "", nil, inf)

	for name, prompt := range map[string]string{
		"plaintext":        "use AKIAIOSFODNN7EXAMPLE for deploy",
		"base64":           "QUtJQUlPU0ZPRE5ON0VYQU1QTEU=",
		"base64-key-value": "dG9rZW49czM3Mw==",
		"url":              "api%5Fkey%3DAUDITsupersecretvalue",
	} {
		t.Run(name, func(t *testing.T) {
			dec := d.Authorize(context.Background(), userReq(prompt, false), "bearer")
			if dec.Allow || dec.Status != http.StatusForbidden {
				t.Fatalf("stock DLP forwarded %s secret: allow=%v status=%d reason=%q", name, dec.Allow, dec.Status, dec.Reason)
			}
		})
	}
}

// TestProxyDefaultResponseDLPBuffersAndBlocksSecret pins the preventive response
// posture: a streamed response is buffered before relay and a secret completion is
// withheld.
func TestProxyDefaultResponseDLPBuffersAndBlocksSecret(t *testing.T) {
	ipx := inferenceproxy.New()
	st, tenant := provisionTenant(t, ipx, "")
	inf := claudeapi.NewInference(claudeapi.InferenceConfig{BaseURL: "https://api.anthropic.com", APIKey: "op", Gateway: "direct", Doer: countTokensDoer{}})
	d := storeBackedDecider(st, tenant, ipx, "", nil, inf)

	for name, completion := range map[string]string{
		"plaintext": "credential AKIAIOSFODNN7EXAMPLE",
		"base64":    "QUtJQUlPU0ZPRE5ON0VYQU1QTEU=",
	} {
		t.Run(name, func(t *testing.T) {
			dec := d.Authorize(context.Background(), userReq("summarize the public changelog", true), "bearer")
			if !dec.Allow {
				t.Fatalf("clean request denied: status=%d reason=%q", dec.Status, dec.Reason)
			}
			if !dec.BufferResponse {
				t.Fatal("stock streaming response was not buffered for preventive DLP")
			}
			verdict := d.Finalize(context.Background(), dec.Session, claudeapi.ProxyForwardResult{
				Response: claudeapi.MessageResponse{Content: []claudeapi.ContentBlock{claudeapi.TextBlock(completion)}},
			})
			if !verdict.Block || verdict.Status != http.StatusForbidden {
				t.Fatalf("%s secret response escaped default buffer: block=%v status=%d", name, verdict.Block, verdict.Status)
			}
		})
	}
}

// TestProxyLedgerAnchorIsMinimalData proves the tamper-evident recording: a completed call
// is anchored to the ledger by PayloadHash, and the persisted audit event carries NO
// prompt/response text — only fingerprints + non-sensitive metadata (docs/SECURITY-HARDENING.md).
func TestProxyLedgerAnchorIsMinimalData(t *testing.T) {
	ctx := context.Background()
	ipx := inferenceproxy.New()
	st, tenant := provisionTenant(t, ipx, "") // unpinned ⇒ residency inert
	inf := claudeapi.NewInference(claudeapi.InferenceConfig{BaseURL: "https://api.anthropic.com", APIKey: "op", Gateway: "direct", Doer: countTokensDoer{}})
	d := storeBackedDecider(st, tenant, ipx, "", nil, inf)

	const privateContent = "alice.s373-ledger@example.com"
	dec := d.Authorize(ctx, userReq("contact "+privateContent, false), "bearer")
	if !dec.Allow {
		t.Fatalf("expected stock secret-only DLP to allow contact-class PII; got deny status=%d reason=%q", dec.Status, dec.Reason)
	}
	out := claudeapi.ProxyForwardResult{
		Response:       claudeapi.MessageResponse{Model: "claude-opus-4-8", Content: []claudeapi.ContentBlock{claudeapi.TextBlock("response also has " + privateContent)}, Usage: claudeapi.MessageUsage{InputTokens: 5, OutputTokens: 2}},
		ReqSHA:         []byte("reqsha-32-bytes-padding-aaaaaaaa"),
		ReqBytes:       42,
		RespSHA:        []byte("respsha-32-bytes-padding-bbbbbbb"),
		RespBytes:      18,
		UpstreamStatus: 200,
	}
	if v := d.Finalize(ctx, dec.Session, out); v.Block {
		t.Fatal("Finalize must not block a clean response with no DLP rules")
	}

	// Walk the ledger: there must be exactly one inference.proxy.recorded event, anchored
	// by a 32-byte PayloadHash, whose serialized form contains NO trace of the secret.
	found := 0
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(ctx, 0, func(ev model.AuditEvent) error {
			if ev.Action != "inference.proxy.recorded" {
				return nil
			}
			found++
			if len(ev.PayloadHash) != 32 {
				t.Errorf("anchor PayloadHash len = %d, want 32", len(ev.PayloadHash))
			}
			blob, _ := json.Marshal(ev)
			if strings.Contains(string(blob), privateContent) {
				t.Fatalf("LEDGER LEAKED raw prompt/response PII: %s", string(blob))
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("walk ledger: %v", err)
	}
	if found != 1 {
		t.Fatalf("expected exactly 1 inference.proxy.recorded anchor, got %d", found)
	}
}
