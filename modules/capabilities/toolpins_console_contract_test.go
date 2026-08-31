// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package capabilities_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/capabilities"
)

// THE ENGINE HALF OF THE CONSOLE CONTRACT.
//
// THE DEFECT THIS EXISTS TO CATCH, AND WHY EVERY EXISTING TEST MISSED IT. Before today
// the console sent `{tool, from_drift}` with no Idempotency-Key and no expected_version,
// and BOTH suites were green while saying opposite things:
//
//   - web/src/features/capabilities/capabilities.test.tsx asserted the mocked
//     `approveToolPin` was called with that body — a mock cannot 400, so it passed;
//   - toolpins_evidence_test.go:166-193 proved the engine 400s that exact body — it
//     passed too, because it builds its own bodies and never looks at a client.
//
// Neither measured the PAIR. Nothing in this repository connected the payload the
// console emits to the decoder that receives it, so the contract could be broken in
// production with a fully green tree. It was.
//
// WHAT THIS DOES INSTEAD. It replays the literal wire request the console produced —
// captured from the REAL client by web/src/features/capabilities/toolpin-wire.contract
// .test.ts and committed as a golden — through the REAL router, decoder and handler. The
// input comes from the console's code; the verdict comes from the engine's. Two sources,
// which is the whole point: an oracle derived from the thing under test cannot fail.
//
// If the console stops sending the preconditions, the engine answers 400 here and this
// goes red. If someone regenerates the golden to match a broken console, this still goes
// red — the golden is the console's claim, not the engine's.
//
// ⚠ WHAT IT IS NOT, because "replays verbatim" reads stronger than it is (named by the
// the model contrast). Two things are deliberately NOT the console's: the
// bearer and the tenant header, which are replaced with the harness's own because the
// capture holds placeholders that cannot authenticate — so this proves nothing about the
// client propagating auth. And the CAS it satisfies belongs to a synchronous fake seeded
// from the same fixture; the real durable applier is the absent enterprise overlay, so
// "the engine accepted it" means this contract, not that estate.

// The path the CONSOLE HALF actually writes. The two halves of this contract shipped pointing at
// different directories — the generator at testdata/, this reader at __fixtures__/ — so the
// fixture could never be found no matter how often it was regenerated, and the error message sent
// the reader to regenerate a file that was being written all along. Aligned on the writer's path,
// which is the one that has the file. A contract test whose two ends disagree on WHERE is not a
// missing fixture: it is two tests that can never meet.
const consoleWireFixture = "../../web/src/features/capabilities/testdata/toolpin-console-wire.json"

// wireRequest is one captured HTTP request. Header names arrive lowercased from the
// browser Headers object; http.Header.Set canonicalizes them, so the replay is faithful.
type wireRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

// wirePin is the pin row the console read before writing. Seeding the durable fake with
// exactly this is what makes the replay a ROUND TRIP: the preconditions in the body are
// checked against the state they were read from, not against a state chosen to fit them.
type wirePin struct {
	Tool             string    `json:"tool"`
	Fingerprint      string    `json:"fingerprint"`
	PinnedAt         time.Time `json:"pinned_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	PinCount         int       `json:"pin_count"`
	Version          int64     `json:"version"`
	DriftFingerprint string    `json:"drift_fingerprint"`
	DriftAt          time.Time `json:"drift_at"`
}

type wireFixture struct {
	Pin      wirePin                `json:"pin"`
	Requests map[string]wireRequest `json:"requests"`
}

func loadWireFixture(t *testing.T) wireFixture {
	t.Helper()
	raw, err := os.ReadFile(consoleWireFixture)
	if err != nil {
		t.Fatalf("read console wire fixture: %v (regenerate with OLIVARES_UPDATE_GOLDEN=1 "+
			"in web/src/features/capabilities/toolpin-wire.contract.test.ts)", err)
	}
	var fx wireFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("parse console wire fixture: %v", err)
	}
	if len(fx.Requests) == 0 || fx.Pin.Tool == "" {
		t.Fatal("console wire fixture is empty: it records nothing, so replaying it proves nothing")
	}
	return fx
}

// seedPin puts the console's read state into the durable fake, at its base version.
func seedPin(fake *fakePinAdmin, tenant model.TenantID, pin wirePin) {
	key := tenant.String() + "|" + pin.Tool
	fake.pins[key] = mcpc.PinSnapshot{
		Server: tenant.String(), Tool: pin.Tool, Fingerprint: pin.Fingerprint,
		PinnedAt: pin.PinnedAt, UpdatedAt: pin.UpdatedAt, PinCount: pin.PinCount,
		Version: pin.Version, DriftFingerprint: pin.DriftFingerprint, DriftAt: pin.DriftAt,
	}
	fake.version[key] = pin.Version
}

// replay serves the captured request verbatim. Only the two transport credentials are
// substituted — the capture holds a placeholder bearer and tenant that cannot
// authenticate against a fresh harness. Everything that carries the PIN PROTOCOL,
// Idempotency-Key above all, is replayed exactly as the console emitted it.
func (h *harness) replay(t *testing.T, req wireRequest, token string, tenant model.TenantID, bodyOverride []byte) resp {
	t.Helper()
	body := req.Body
	if bodyOverride != nil {
		body = bodyOverride
	}
	r := httptest.NewRequest(req.Method, req.Path, bytes.NewReader(body))
	r.RemoteAddr = "10.0.0.1:1234"
	for k, v := range req.Headers {
		switch strings.ToLower(k) {
		case "authorization", "x-olivares-tenant":
			continue // placeholders from the capture; the harness owns the real ones
		}
		r.Header.Set(k, v)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Olivares-Tenant", tenant.String())

	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, r)
	out := resp{code: rec.Code, raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

// TestConsolePayloadIsAcceptedByTheEngine is the acceptance criterion: the payload
// the console really emits, against the contract the engine really enforces.
func TestConsolePayloadIsAcceptedByTheEngine(t *testing.T) {
	fx := loadWireFixture(t)

	for _, name := range []string{"approve", "unpin"} {
		req, ok := fx.Requests[name]
		if !ok {
			t.Fatalf("console wire fixture has no %q request", name)
		}
		t.Run(name, func(t *testing.T) {
			// The header is asserted BY NAME, not merely relied on: a capture that lost it
			// would still fail below, but with "400 bad request" instead of the reason.
			var hasIdem bool
			for k, v := range req.Headers {
				if strings.EqualFold(k, "Idempotency-Key") && v != "" {
					hasIdem = true
				}
			}
			if !hasIdem {
				t.Fatalf("the console's %s request carries no Idempotency-Key header; "+
					"the engine requires one (toolpins.go:100-104) and every write would 400", name)
			}

			fake := newFakePinAdmin()
			h := newHarness(t, capabilities.WithToolPinAdmin(fake))
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "acme")
			editor := h.roleToken(admin, tenant, "e@x.io", "editor")
			seedPin(fake, tenant, fx.Pin)

			r := h.replay(t, req, editor, tenant, nil)
			if r.code != http.StatusAccepted {
				t.Fatalf("the console's %s payload was REFUSED by the engine: %d %s\n"+
					"body sent verbatim: %s", name, r.code, r.raw, req.Body)
			}
			// A 202 is not the whole contract: the console PARSES this body with a Zod
			// schema that requires every field, so an engine that stopped sending one
			// would turn an accepted write into a client-side throw. Asserting only the
			// status was a hole the the model contrast named.
			for _, k := range []string{"tool", "operation_id", "apply_state", "version", "evidence_ref"} {
				if _, ok := r.body[k]; !ok {
					t.Fatalf("the 202 body omits %q, which the console's response schema "+
						"requires (web/src/features/capabilities/api.ts): %s", k, r.raw)
				}
			}
			if _, ok := r.body["version"].(float64); !ok {
				t.Fatalf("the 202 body's `version` is not a number: %s", r.raw)
			}
		})
	}
}

// TestConsoleReadsThePreconditionFromTheEngine closes the other half, and it is the half
// the brief's own diagnosis was about: a precondition the client cannot OBTAIN is
// not a precondition, it is a closed door. Before this session toolPinDTO had seven
// fields and none was a version, so `expected_version is required` was unanswerable by
// construction and both verbs 400'd for every caller that ever existed.
//
// It deliberately does NOT hand-build a body: it compares what GET publishes with what
// the CONSOLE's captured body claims to have read. Rebuilding the body here would derive
// the oracle from the same place as the value under test and could never fail.
func TestConsoleReadsThePreconditionFromTheEngine(t *testing.T) {
	fx := loadWireFixture(t)
	fake := newFakePinAdmin()
	h := newHarness(t, capabilities.WithToolPinAdmin(fake))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	seedPin(fake, tenant, fx.Pin)

	r := h.do("GET", "/v1/m/capabilities/toolpins", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("list = %d %s", r.code, r.raw)
	}
	var listed struct {
		Items []struct {
			Tool    string `json:"tool"`
			Version *int64 `json:"version"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(r.raw), &listed); err != nil {
		t.Fatalf("parse list: %v", err)
	}
	if len(listed.Items) != 1 {
		t.Fatalf("list returned %d items, want 1", len(listed.Items))
	}
	// A pointer, so an ABSENT key is distinguishable from a legitimate 0. Absent is the
	// defect; 0 is a valid version and must still be published.
	if listed.Items[0].Version == nil {
		t.Fatalf("GET /toolpins publishes no `version`: the CAS precondition both write "+
			"verbs demand is unobtainable, so every approve/unpin 400s. body=%s", r.raw)
	}

	var sent struct {
		ExpectedVersion *int64 `json:"expected_version"`
	}
	if err := json.Unmarshal(fx.Requests["approve"].Body, &sent); err != nil {
		t.Fatalf("parse the console's captured body: %v", err)
	}
	if sent.ExpectedVersion == nil {
		t.Fatal("the console's approve body carries no expected_version")
	}
	if *sent.ExpectedVersion != *listed.Items[0].Version {
		t.Fatalf("the console wrote expected_version=%d but GET publishes version=%d: "+
			"the client is not echoing the value the engine hands it",
			*sent.ExpectedVersion, *listed.Items[0].Version)
	}
}

// TestConsolePayloadStillLosesAStaleCAS is the non-firing direction. Without it, the
// acceptance test above passes for an engine that waved everything through, and "the
// console's payload is accepted" would say nothing about whether the preconditions are
// load-bearing. The same captured request, with ONLY the base version moved off the
// durable row, must be refused with 409 — and must not write.
func TestConsolePayloadStillLosesAStaleCAS(t *testing.T) {
	fx := loadWireFixture(t)
	req := fx.Requests["approve"]

	var body map[string]any
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatalf("parse the console's captured body: %v", err)
	}
	body["expected_version"] = fx.Pin.Version + 1 // a version the durable row is not at
	stale, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal stale body: %v", err)
	}

	fake := newFakePinAdmin()
	h := newHarness(t, capabilities.WithToolPinAdmin(fake))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")
	seedPin(fake, tenant, fx.Pin)

	r := h.replay(t, req, editor, tenant, stale)
	if r.code != http.StatusConflict {
		t.Fatalf("approve with a base version the row is not at = %d %s, want 409", r.code, r.raw)
	}
	key := tenant.String() + "|" + fx.Pin.Tool
	if got := fake.pins[key].Fingerprint; got != fx.Pin.Fingerprint {
		t.Fatalf("a lost CAS still wrote: fingerprint = %q, want the untouched %q", got, fx.Pin.Fingerprint)
	}
}
