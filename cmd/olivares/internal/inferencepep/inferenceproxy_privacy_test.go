// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inferencepep

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
)

// TestProxySignedLedgerStoresFingerprintsNotInferenceContent attacks the final
// proxy-to-ledger hop with distinct request and response canaries. The evidence
// must be Ed25519-signed and commit to both byte fingerprints without persisting
// either raw body in the event or its canonical metadata.
func TestProxySignedLedgerStoresFingerprintsNotInferenceContent(t *testing.T) {
	const (
		requestCanary  = "prompt alice.s373@example.com secret=REQUEST-AUDIT-CANARY"
		responseCanary = "completion SSN 078-05-1120 RESPONSE-AUDIT-CANARY"
		requestRef     = "0123456789abcdef0123456789abcdef"
	)
	ctx := context.Background()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	ipx := inferenceproxy.New()
	st, tenant := provisionTenantWithConfig(t, ipx, "", store.Config{
		Engine: store.EngineSQLite, DSN: ":memory:", SignEvent: signer.SignEvent,
	})

	reqBody := []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"` + requestCanary + `"}]}`)
	respBody := []byte(`{"content":[{"type":"text","text":"` + responseCanary + `"}]}`)
	reqSHA := sha256.Sum256(reqBody)
	respSHA := sha256.Sum256(respBody)
	out := claudeapi.ProxyForwardResult{
		Response: claudeapi.MessageResponse{
			Model:   "claude-opus-4-8",
			Content: []claudeapi.ContentBlock{claudeapi.TextBlock(responseCanary)},
		},
		ReqSHA: reqSHA[:], ReqBytes: int64(len(reqBody)),
		RespSHA: respSHA[:], RespBytes: int64(len(respBody)), UpstreamStatus: 200,
	}
	sess := &proxySession{
		tenant: tenant, actor: "user:u1", actorKind: "user",
		modelRef: "claude-opus-4-8", requestRef: requestRef,
	}
	d := &Decider{
		Surface: "direct", Store: st,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	d.anchorOutcome(ctx, sess, out, "allow")

	wantHash := proxyOutcomeHash(
		requestRef, "direct", tenant.String(), sess.modelRef, "allow",
		out.ReqBytes, out.RespBytes, reqSHA[:], respSHA[:], sess.inputDigest, sess.effectiveDigest,
	)
	found := 0
	var sigReport audit.EventSigReport
	err = st.View(ctx, tenant, func(sc store.Scope) error {
		walker, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			t.Fatal("audit log does not expose canonical metadata")
		}
		if err := walker.WalkCanonical(ctx, 1, func(ev model.AuditEvent, meta string, _ []byte) error {
			if ev.Action != "inference.proxy.recorded" {
				return nil
			}
			found++
			if !bytes.Equal(ev.PayloadHash, wantHash) {
				t.Errorf("payload fingerprint = %x, want %x", ev.PayloadHash, wantHash)
			}
			if len(ev.PayloadHash) != sha256.Size {
				t.Errorf("payload fingerprint length = %d, want %d", len(ev.PayloadHash), sha256.Size)
			}
			if len(ev.Sig) == 0 {
				t.Error("inference.proxy.recorded event is not Ed25519-signed")
			}
			blob, marshalErr := json.Marshal(ev)
			if marshalErr != nil {
				return marshalErr
			}
			persisted := string(blob) + meta
			for _, canary := range []string{requestCanary, responseCanary} {
				if strings.Contains(persisted, canary) {
					t.Fatalf("signed proxy ledger leaked raw inference content %q: %s", canary, persisted)
				}
			}
			return nil
		}); err != nil {
			return err
		}
		var verifyErr error
		sigReport, verifyErr = audit.VerifyEvents(ctx, sc.Audit(), pub)
		return verifyErr
	})
	if err != nil {
		t.Fatalf("inspect proxy ledger: %v", err)
	}
	if found != 1 {
		t.Fatalf("inference.proxy.recorded events = %d, want 1", found)
	}
	if !sigReport.OK || sigReport.Events == 0 || sigReport.Events != sigReport.Signed {
		t.Fatalf("signed audit verification failed: %+v", sigReport)
	}
}

type privacyTraceDecider struct{}

func (privacyTraceDecider) Authorize(_ context.Context, req claudeapi.MessageRequest, _ string) claudeapi.ProxyDecision {
	return claudeapi.ProxyDecision{Allow: true, Request: req, Session: struct{}{}}
}

func (privacyTraceDecider) Finalize(context.Context, any, claudeapi.ProxyForwardResult) claudeapi.ProxyResponseVerdict {
	return claudeapi.ProxyResponseVerdict{}
}

func (privacyTraceDecider) AuthorizeBatch(context.Context, []claudeapi.BatchRequest, string) claudeapi.ProxyBatchDecision {
	return claudeapi.ProxyBatchDecision{Allow: false, Status: http.StatusForbidden}
}

func (privacyTraceDecider) FinalizeBatch(context.Context, any, claudeapi.ProxyBatchForwardResult) {}
