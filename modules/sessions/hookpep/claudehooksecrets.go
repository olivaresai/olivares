// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hookpep

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/connectors/redact"
	"github.com/olivaresai/olivares/core/auth"
)

// Only transport authentication is shared with the SOC writer. EvidenceRedactor
// is carried directly on each PDP request, never in a context key or a setting.
type claudeHookProofKey struct{}
type claudeHookProof struct {
	bearer          string
	principal       auth.Principal
	scope           auth.SessionScope
	err             error
	retentionMu     sync.Mutex
	retentionCtx    context.Context
	retentionCancel context.CancelFunc
	retentionClosed bool
}

// One shared cleanup deadline covers all retention after a helper disconnect;
// cancellation never removes the generation check or extends the live gate.
func (p *claudeHookProof) retentionContext(ctx context.Context) context.Context {
	if ctx.Err() == nil {
		return ctx
	}
	p.retentionMu.Lock()
	defer p.retentionMu.Unlock()
	if p.retentionClosed {
		return ctx
	}
	if p.retentionCtx == nil {
		p.retentionCtx, p.retentionCancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	}
	return p.retentionCtx
}

func (p *claudeHookProof) closeRetention() {
	p.retentionMu.Lock()
	defer p.retentionMu.Unlock()
	p.retentionClosed = true
	if p.retentionCancel != nil {
		p.retentionCancel()
	}
}

const withheldHookInput = "[tool input withheld: session secret redaction unavailable or access changed]"
const withheldHookResource = "[resource reference withheld: tool input contains a session secret]"

// Resolve the hook bearer once, keeping the exact proof for the decision and
// the connector's later auditor. The MCP route has its own existing adapter.
func (d *Decider) withHookProof(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		var bearer string
		if len(header) > 7 && strings.EqualFold(header[:7], "Bearer ") {
			bearer = strings.TrimSpace(header[7:])
		}
		if r.Method == http.MethodPost {
			var p auth.Principal
			var scope auth.SessionScope
			var err error
			if credentials, ok := d.Authr.(SessionCredentials); ok && strings.HasPrefix(bearer, SessionTokenPrefix) {
				p, scope, err = credentials.Resolve(r.Context(), bearer)
			} else {
				p, err = d.Authr.Authenticate(r.Context(), bearer)
			}
			proof := &claudeHookProof{bearer: bearer, principal: p, scope: scope, err: err}
			defer proof.closeRetention()
			r = r.WithContext(context.WithValue(r.Context(), claudeHookProofKey{}, proof))
		}
		next.ServeHTTP(w, r)
	})
}

// The cache is keyed by run_ref. Check the original bearer AFTER reading it so
// a resumed run's newer cache cannot sanitize an older request. Never read the
// vault again, and never return unproven text when the credential names secrets.
func (d *Decider) exactHookMask(ctx context.Context, text string) (string, []redact.GeneratedMaskSpan, error) {
	proof, _ := ctx.Value(claudeHookProofKey{}).(*claudeHookProof)
	if proof == nil {
		return text, nil, nil
	}
	if proof.err != nil {
		return "", nil, errors.New(withheldHookInput)
	}
	if proof.scope.SecretEnv == "" {
		return text, nil, nil
	}
	credentials, ok := d.Authr.(SessionCredentials)
	if !ok || proof.err != nil || d.RedactSecrets == nil {
		return "", nil, errors.New(withheldHookInput)
	}
	masked, spans, supervised := d.RedactSecrets(proof.scope.TenantID, proof.scope.RunRef, []byte(text))
	if !supervised {
		return "", nil, errors.New(withheldHookInput)
	}
	_, current, err := credentials.Resolve(proof.retentionContext(ctx), proof.bearer)
	if err != nil || current.SessionRef != proof.scope.SessionRef || current.RunRef != proof.scope.RunRef || current.Fence != proof.scope.Fence || current.SecretEnv != proof.scope.SecretEnv {
		return "", nil, errors.New(withheldHookInput)
	}
	generated := make([]redact.GeneratedMaskSpan, len(spans))
	for i, span := range spans {
		generated[i] = redact.GeneratedMaskSpan{Start: span.Start, End: span.End}
	}
	return string(masked), generated, nil
}

func (d *Decider) cleanHookText(ctx context.Context, text string) (string, error) {
	masked, spans, err := d.exactHookMask(ctx, text)
	if err != nil {
		return "", err
	}
	return redact.CleanMasked(masked, spans)
}

func (d *Decider) retainedHookText(ctx context.Context, text string) string {
	clean, err := d.cleanHookText(ctx, text)
	if err != nil {
		return withheldHookInput
	}
	return clean
}

// Derived references may already have dropped part of a secret (URL queries,
// shell arguments, or a bounded projection). Inspect the full original input
// first. Withhold that retained reference rather than matching only its suffix.
// The returned adapter never changes the request the live PDP evaluates.
func (d *Decider) hookInputContainsSecret(ctx context.Context, in claude.HookDecisionInput) (bool, error) {
	proof, _ := ctx.Value(claudeHookProofKey{}).(*claudeHookProof)
	if proof == nil {
		return false, nil
	}
	if proof.err != nil {
		return false, errors.New(withheldHookInput)
	}
	if proof.scope.SecretEnv == "" {
		return false, nil
	}
	full, err := json.Marshal(struct {
		Tool      string
		ToolUseID string
		Identity  claude.HookIdentity
		Input     map[string]any
	}{Tool: in.Tool, ToolUseID: in.ToolUseID, Identity: in.Identity, Input: in.RewriteBase()})
	if err != nil {
		return false, errors.New(withheldHookInput)
	}
	_, generated, err := d.exactHookMask(ctx, string(full))
	return len(generated) > 0, err
}

func (d *Decider) hookEvidenceRedactor(ctx context.Context, in claude.HookDecisionInput, projectedRefs ...string) func(string) string {
	proof, _ := ctx.Value(claudeHookProofKey{}).(*claudeHookProof)
	if proof == nil || (proof.err == nil && proof.scope.SecretEnv == "") {
		return redact.Clean
	}
	matched, err := d.hookInputContainsSecret(ctx, in)
	var replace *strings.Replacer
	if matched || err != nil {
		pairs := []string{}
		for _, ref := range append([]string{in.ResourceRef}, projectedRefs...) {
			if ref != "" {
				pairs = append(pairs, ref, withheldHookResource)
			}
		}
		if len(pairs) > 0 {
			replace = strings.NewReplacer(pairs...)
		}
	}
	return func(text string) string {
		if replace != nil {
			text = replace.Replace(text)
		}
		return d.retainedHookText(ctx, text)
	}
}

func (d *Decider) retainedHookInput(ctx context.Context, in claude.HookDecisionInput) claude.HookDecisionInput {
	clean := d.hookEvidenceRedactor(ctx, in)
	in.Event = clean(in.Event)
	in.Tool = clean(in.Tool)
	in.ToolUseID = clean(in.ToolUseID)
	in.ResourceRef = clean(in.ResourceRef)
	return in
}
