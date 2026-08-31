// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/eventing"
)

// eventingwire.go is the composition-root half of the eventing platform:
// the construction options read from the environment, and the SecretSealer
// adapter that encrypts subscription signing secrets at rest under an
// ENGINE-HELD key — the module itself never sees key material (its port takes
// sealed strings). The dispatch pump lives in eventingpump.go.
const (
	// eventingAllowLoopbackEnv ("1") permits loopback webhook endpoints —
	// single-box development ONLY; the production default refuses them (SSRF).
	eventingAllowLoopbackEnv = "OLIVARES_EVENTING_ALLOW_LOOPBACK"
	// eventingRetentionEnv is a Go duration for the event-log retention (= the
	// replay window); default 168h. Invalid values keep the default.
	eventingRetentionEnv = "OLIVARES_EVENTING_RETENTION"
	// eventingSecretKeyEnv supplies the 32-byte base64 sealer key directly (the
	// HA path: every node must seal/open with the SAME key, like the shared
	// audit signing key). Unset => a per-node key file in the data dir.
	eventingSecretKeyEnv = "OLIVARES_EVENTING_SECRET_KEY"
	// eventingSecretKeyFile is the on-disk key minted on first boot (0600,
	// fail-closed on wider permissions — the secure package's posture).
	eventingSecretKeyFile = "eventing-secret.key"
)

// loadEventingOptions builds the eventing module's construction options from
// the environment. The authorizer and sealer are late-bound by boot()
// (UseAuthorizer/UseSecretSealer) — they need the composed evaluator and the
// data dir, which exist only there.
// It returns an error for the egress destination policy and only for that: a
// retention value that does not parse warns and keeps the default, because the
// default is safe, while a destination policy that does not parse must NOT fall back
// to "no policy". Booting less governed than the operator asked for, with a green
// log to suggest otherwise, is the failure this whole family of loaders exists to
// prevent (loadOperatorJSONConfig says the same thing in its own comment).
func loadEventingOptions(getenv func(string) string, log *slog.Logger) ([]eventing.Option, error) {
	var opts []eventing.Option
	if getenv(eventingAllowLoopbackEnv) == "1" {
		log.Warn("eventing: loopback webhook endpoints ENABLED (" + eventingAllowLoopbackEnv + "=1) — development posture, do not run in production")
		opts = append(opts, eventing.WithAllowLoopback(true))
	}
	if raw := strings.TrimSpace(getenv(eventingRetentionEnv)); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			opts = append(opts, eventing.WithRetention(d))
		} else {
			log.Warn("eventing: "+eventingRetentionEnv+" is not a valid positive duration; using the default", "value", raw)
		}
	}
	pol, err := loadEventingEgressPolicy(getenv)
	if err != nil {
		return nil, err
	}
	if pol != nil {
		log.Info("eventing: egress destination policy IN FORCE; a subscription may only point at a destination it permits",
			"source", envEventingEgressPolicy)
		opts = append(opts, eventing.WithEgressPolicy(pol))
	} else {
		// Stated rather than silent. An operator reading the boot log should be able
		// to tell "no policy is configured" from "a policy is configured and permits
		// this", because the two look identical from every delivery that succeeds.
		//
		// What it CANNOT say here is what an absent policy means on this deployment:
		// since unit G that depends on the durable disposition, which is read after
		// the store opens (logEventingEgressRollout). So this line reports the
		// configuration and the other reports the effect — and the effect is the one an
		// operator actually needs.
		log.Warn("eventing: NO egress destination policy configured (" + envEventingEgressPolicy + " unset); whether that permits or denies depends on this deployment's rollout disposition, reported below")
	}
	return opts, nil
}

// eventingAllowLoopback reports the loopback posture for callers outside the module
// wiring (the CLI's endpoint check), so the two read the same switch.
func eventingAllowLoopback(getenv func(string) string) bool {
	return getenv(eventingAllowLoopbackEnv) == "1"
}

// newEventingSealer builds the AES-256-GCM sealer over the engine-held key:
// from the environment (HA shared key) or from a 0600 key file in the data dir
// (minted on first boot). Fail-closed: a malformed env key or an unreadable/
// over-permissive key file is an error, never a silent downgrade.
func newEventingSealer(dataDir string, getenv func(string) string) (eventing.SecretSealer, error) {
	var key []byte
	if raw := strings.TrimSpace(getenv(eventingSecretKeyEnv)); raw != "" {
		k, err := base64.StdEncoding.DecodeString(raw)
		if err != nil || len(k) != 32 {
			return nil, fmt.Errorf("eventing: %s must be 32 base64-encoded bytes", eventingSecretKeyEnv)
		}
		key = k
	} else {
		k, err := loadOrCreateAEADKey(filepath.Join(dataDir, eventingSecretKeyFile))
		if err != nil {
			return nil, err
		}
		key = k
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("eventing: sealer cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("eventing: sealer aead: %w", err)
	}
	return &eventingSealer{aead: aead}, nil
}

// loadOrCreateAEADKey loads the 32-byte sealer key at path, or mints and
// persists one (0600) on first boot. Like the secure package's secret reads, it
// fails closed if an existing key file is more permissive than owner-only.
func loadOrCreateAEADKey(path string) ([]byte, error) {
	if fi, err := os.Stat(path); err == nil {
		if fi.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("eventing: key file %q is group/world accessible (%v); refusing to use it", path, fi.Mode().Perm())
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("eventing: read key file: %w", err)
		}
		k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(k) != 32 {
			return nil, fmt.Errorf("eventing: %q is not a valid sealer key file", path)
		}
		return k, nil
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, fmt.Errorf("eventing: mint sealer key: %w", err)
	}
	// This runs BEFORE auditBoot on the `eventing subscriptions create/test` paths,
	// so it can be the first thing to put key material in the data directory. Ensure
	// the directory's VCS exclusion here too, or a failure between this write and the
	// boot would leave a private key with nothing excluding it (sol-max
	// contrast). Cheap and idempotent when the marker is already there.
	if err := secure.EnsureDataDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	enc := base64.StdEncoding.EncodeToString(k) + "\n"
	if err := os.WriteFile(path, []byte(enc), 0o600); err != nil {
		return nil, fmt.Errorf("eventing: persist sealer key: %w", err)
	}
	return k, nil
}

// eventingSealer is AES-256-GCM with the tenant bound as AAD, so a sealed
// secret cannot be replayed across tenants. The sealed form is versioned
// ("v1:" + base64(nonce||ciphertext)) for future agility.
type eventingSealer struct{ aead cipher.AEAD }

const eventingSealPrefix = "v1:"

// aad binds a ciphertext to its tenant and purpose.
func (s *eventingSealer) aad(tenant model.TenantID) []byte {
	return []byte("eventing.subscription.secret|" + tenant.String())
}

func (s *eventingSealer) Seal(_ context.Context, tenant model.TenantID, plaintext []byte) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("eventing: seal nonce: %w", err)
	}
	ct := s.aead.Seal(nil, nonce, plaintext, s.aad(tenant))
	return eventingSealPrefix + base64.StdEncoding.EncodeToString(append(nonce, ct...)), nil
}

func (s *eventingSealer) Open(_ context.Context, tenant model.TenantID, sealed string) ([]byte, error) {
	raw, ok := strings.CutPrefix(sealed, eventingSealPrefix)
	if !ok {
		return nil, fmt.Errorf("eventing: unknown sealed-secret version")
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(b) < s.aead.NonceSize() {
		return nil, fmt.Errorf("eventing: malformed sealed secret")
	}
	nonce, ct := b[:s.aead.NonceSize()], b[s.aead.NonceSize():]
	pt, err := s.aead.Open(nil, nonce, ct, s.aad(tenant))
	if err != nil {
		return nil, fmt.Errorf("eventing: sealed secret does not open (wrong key or tenant)")
	}
	return pt, nil
}

// logEventingEgressRollout states, in one line, what the egress destination control
// actually DOES on this deployment.
//
// It exists because every other signal is ambiguous. "No policy configured" is a
// permit on an upgrade and a deny on a fresh install; a green boot looks the same
// either way; and the difference is invisible from any delivery that succeeds. An
// operator who cannot read the effective posture out of the boot log has to infer it,
// and the whole campaign is a record of what inference costs.
func logEventingEgressRollout(ctx context.Context, log *slog.Logger, src eventing.EgressRolloutSource, policyConfigured bool) {
	st, err := src.EgressRollout(ctx)
	if err != nil {
		log.Error("eventing: the durable rollout disposition of the egress destination control could NOT be read; every destination will be refused RETRYABLY until it can be",
			"control", eventing.EgressRolloutControlKey, "err", err)
		return
	}
	switch {
	case st.CurrentMode == store.RolloutEnforced && !policyConfigured:
		log.Warn("eventing: egress destinations are ENFORCED DENY-ALL — this deployment enforces the control and no policy has been authored, so no subscription can deliver until a platform operator authorizes destinations",
			"control", eventing.EgressRolloutControlKey, "env", envEventingEgressPolicy, "enforcement_committed", st.EnforcementCommitted)
	case st.CurrentMode == store.RolloutEnforced:
		log.Info("eventing: egress destinations are ENFORCED by the authored policy; a destination it does not permit cannot deliver",
			"control", eventing.EgressRolloutControlKey, "enforcement_committed", st.EnforcementCommitted)
	case st.CurrentMode == store.RolloutLegacyCompat:
		// IT NO LONGER ASSERTS WHY. This line used to say "this deployment predates the
		// control", which is an inference from the classification, not something this
		// process knows — and when the classification is wrong the line CORROBORATES the
		// error instead of exposing it. That is not hypothetical: a state row lost on a
		// control that was never transitioned used to be re-derived to compatibility on a
		// green boot, and this warning then told the operator, every boot, that their fresh
		// install predated a control it did not predate. What is reported now is what is
		// durably recorded — the mode and the witness the classification rested on — and the
		// operator is pointed at the record rather than at a conclusion drawn from it.
		log.Warn("eventing: egress destinations are in COMPATIBILITY mode, so the destinations this deployment already had keep working and no policy is required. That disposition was decided ONCE, from the witness below, and is recorded durably — if it does not match what you expect of this deployment, the record is the thing to check. Run `olivares eventing egress status` to see what actuating would block",
			"control", eventing.EgressRolloutControlKey,
			"classified_mode", string(st.ClassifiedMode), "classified_at", st.ClassifiedAt,
			"witness", st.WitnessKind+" "+st.WitnessDetail)
	case st.CurrentMode == store.RolloutPolicyOptional:
		log.Warn("eventing: the egress destination control is POLICY-OPTIONAL by a recorded operator decision — with no policy authored, a subscription may point at any public HTTPS host",
			"control", eventing.EgressRolloutControlKey, "decided_by", st.DecidedBy, "reason", st.DecidedReason)
	}
}

// logEventingWriterFence states whether a writer that does not carry the egress gate can still
// author a destination against this database (unit H).
//
// It is a separate line from the destination control's because it answers a different question. An
// operator reading "egress destinations are ENFORCED" would reasonably assume every writer is
// gated; until the fence is armed, that assumption is wrong, and the only honest place to say so
// is next to the claim.
func logEventingWriterFence(ctx context.Context, log *slog.Logger, src eventing.EgressWriterFenceSource) {
	st, err := src.EgressWriterFence(ctx)
	if err != nil {
		log.Error("eventing: the durable disposition of the egress WRITER FENCE could not be read; whether an un-upgraded writer can author a destination is unknown",
			"control", eventing.EgressWriterFenceControlKey, "err", err)
		return
	}
	switch st.CurrentMode {
	case store.RolloutEnforced:
		log.Info("eventing: the egress WRITER FENCE is ARMED — a binary that does not declare the egress capability cannot introduce or move a destination",
			"control", eventing.EgressWriterFenceControlKey,
			"required_capability", eventing.EgressWriterCapability, "generation", st.Generation)
	default:
		log.Warn("eventing: the egress WRITER FENCE is DORMANT — a node that predates the egress gate can still author a destination against this database. Arm it once every authoring node is replaced",
			"control", eventing.EgressWriterFenceControlKey, "mode", string(st.CurrentMode),
			"classified", st.WitnessKind+" "+st.WitnessDetail)
	}
}
