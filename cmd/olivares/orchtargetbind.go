// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/modules/orchestration"
)

// orchtargetbind.go (D-06, item 6) wires the composition-root half of the
// target-binding integrity control the orchestration module cannot provide
// itself: the dedicated HMAC key (custody + HA sharing) and the effective-
// dispatcher-config GENERATION that folds the operator-owned target (image/
// command/URL/skill/headers) into the fingerprint. Without both, the shipped
// binary would either block every workflow acting step (no key) or freeze only
// the module-visible subject/route while the real effect target stayed mutable.

const (
	// envTargetBindingKey / envTargetBindingKeyFile externalize the dedicated
	// target-binding HMAC key. It is NOT the audit signing key. Share it across
	// nodes for HA (a per-node ephemeral key would fork the binding at failover
	// and block every pending run after a restart) — mount the same secret, or
	// point the FILE at a shared 0600 path the first node mints.
	envTargetBindingKey     = "OLIVARES_TARGET_BINDING_KEY"
	envTargetBindingKeyFile = "OLIVARES_TARGET_BINDING_KEY_FILE"
)

// loadTargetBindingKey resolves the target-binding HMAC key, fail-closed:
//
//  1. OLIVARES_TARGET_BINDING_KEY (inline material, any length — domain-hashed
//     to a 32-byte key): the BYOK path.
//  2. OLIVARES_TARGET_BINDING_KEY_FILE: a mounted shared secret; if the path is
//     set but ABSENT, a 32-byte key is minted and persisted 0600 (single node /
//     first boot), so restarts reuse it and pending runs still verify.
//  3. Neither: NO key — ok=false. The module keeps its deny-closed default and
//     every workflow acting step BLOCKS. That is the safe posture, never an
//     invented ephemeral key; the operator must provision a shared key.
func loadTargetBindingKey(getenv func(string) string, log *slog.Logger) (orchestration.MACKeyProvider, bool) {
	if raw := strings.TrimSpace(getenv(envTargetBindingKey)); raw != "" {
		key := deriveTargetBindingKey([]byte(raw))
		return orchestration.NewStaticMACKey(key, targetBindingKeyID(key)), true
	}
	if path := strings.TrimSpace(getenv(envTargetBindingKeyFile)); path != "" {
		key, ok := loadOrMintKeyFile(path, log)
		if !ok {
			return nil, false
		}
		return orchestration.NewStaticMACKey(key, targetBindingKeyID(key)), true
	}
	log.Warn("target-binding: no key configured (" + envTargetBindingKey + "[_FILE]); workflow acting steps BLOCK deny-closed — provision a shared key")
	return nil, false
}

// loadOrMintKeyFile resolves the SAME 32-byte key deterministically across
// restarts and nodes (item 6a): a first writer mints it with an ATOMIC,
// EXCLUSIVE create (O_CREATE|O_EXCL — never ReadFile-then-WriteFile, which races
// two HA nodes into two different keys), and every later boot / other node reads
// exactly that key. The file stores the key as hex; the SAME bytes are used on
// mint and on reload (the earlier bug minted the raw bytes but reloaded them
// through a second derivation, forking the key and invalidating every pending
// binding after a restart). An operator-mounted BYOK secret of any other shape
// is derived deterministically instead, so it too is stable across restarts.
func loadOrMintKeyFile(path string, log *slog.Logger) ([]byte, bool) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		defer f.Close()
		key := make([]byte, 32)
		if _, rerr := rand.Read(key); rerr != nil {
			log.Warn("target-binding: cannot mint key; workflow acting steps will BLOCK", "err", rerr)
			return nil, false
		}
		if _, werr := f.Write([]byte(hex.EncodeToString(key))); werr != nil {
			log.Warn("target-binding: cannot persist minted key; workflow acting steps will BLOCK", "path", path, "err", werr)
			return nil, false
		}
		log.Warn("target-binding: minted a NEW key — back it up and SHARE it across nodes for HA", "path", path)
		return key, true
	}
	if !os.IsExist(err) {
		log.Warn("target-binding: cannot open key file; workflow acting steps will BLOCK", "path", path, "err", err)
		return nil, false
	}
	// The file already exists (a prior boot, or another HA node just minted it).
	b, rerr := os.ReadFile(path)
	if rerr != nil || len(b) == 0 {
		log.Warn("target-binding: key file unreadable; workflow acting steps will BLOCK", "path", path, "err", rerr)
		return nil, false
	}
	return keyFromFileContent(b), true
}

// keyFromFileContent returns the SAME 32-byte key on every read: our minted
// hex-32 is decoded and used directly; any other operator BYOK material (any
// length) is derived deterministically.
func keyFromFileContent(b []byte) []byte {
	s := strings.TrimSpace(string(b))
	if raw, err := hex.DecodeString(s); err == nil && len(raw) == 32 {
		return raw
	}
	return deriveTargetBindingKey(b)
}

// deriveTargetBindingKey domain-separates arbitrary key material to a 32-byte
// HMAC key (accepts a hex/base64/raw secret of any length).
func deriveTargetBindingKey(material []byte) []byte {
	sum := sha256.Sum256(append([]byte("olivares.target-binding.key.v1|"), material...))
	return sum[:]
}

// targetBindingKeyID is a stable, non-secret id of the key (its truncated
// public digest) so a rotation is visible in bindings without exposing the key.
func targetBindingKeyID(key []byte) string {
	sum := sha256.Sum256(append([]byte("olivares.target-binding.keyid.v1|"), key...))
	return "tbk-" + hex.EncodeToString(sum[:6])
}

// dispatcherGeneration folds the EFFECTIVE operator dispatcher config per subject
// into the target fingerprint (deferral #4): a per-subject digest of the
// runtime image/command/resources/env-refs/wirings OR the A2A url/skill/text/
// trust — so re-pointing a subject to an attacker image/URL/skill changes the
// generation and voids a pending approval, even though the module-visible
// schedule subject is unchanged. Built once at boot from the loaded config; a
// restart with a CHANGED config yields a different generation (pending runs
// block), while an unchanged config yields the SAME generation (they proceed).
type dispatcherGeneration struct {
	bySubject map[string]string
}

// newDispatcherGeneration digests each provisioned target's effect-bearing
// config. Secret VALUES are never in the config (only secret-store REFERENCES),
// so the digest carries references + versions, never a cleartext secret.
//
// It MIRRORS the dispatcher's own build (newOrchestrationDispatcher): it applies
// the SAME validity filters — a runtime with empty subject_ref/runtime and an A2A
// agent with empty subject_ref/url are SKIPPED — and the SAME precedence (runtime
// after A2A, so runtime overwrites for a subject present in both). If the
// generation instead digested raw config, an operator could append an INVALID
// duplicate the dispatcher ignores yet which overwrites the frozen generation,
// then restart with a valid EVIL target for the same subject: the dispatcher acts
// on evil while the generation is unchanged and the old approval passes (item 6, Codex review 3). Deriving from the same filtered/deduplicated snapshot
// closes that: the frozen generation always describes exactly what Fire picks.
func newDispatcherGeneration(cfg orchDispatchConfig) *dispatcherGeneration {
	m := map[string]string{}
	// A2A FIRST so a subject present in BOTH is OVERWRITTEN by its runtime
	// generation below — matching orchdispatch.Fire, which resolves runtimes[key]
	// before agents[key] (orchdispatch.go): the fingerprint must describe the
	// backend the dispatcher actually picks, not a different one (item 6d).
	for _, a := range cfg.A2A.Agents {
		kind := orDefaultStr(a.SubjectKind, "agent")
		// Same skip as newOrchestrationDispatcher (orchdispatch_load.go): an invalid
		// entry is not in d.agents and must not contribute to the generation.
		if strings.TrimSpace(a.SubjectRef) == "" || strings.TrimSpace(a.URL) == "" {
			continue
		}
		parts := []string{"a2a", a.Authority, a.URL, a.Skill, a.Text, a.WellKnownPath}
		for _, scope := range canonicalA2AScopes(a.Scopes) {
			parts = append(parts, "scope:"+scope)
		}
		policy, err := resolveProtocolRuntimePolicy(
			a.ProtocolRuleRefs, a.ProtocolPermissionProfileRef, a2aOutboundRuntimePolicy,
		)
		if err != nil {
			// newOrchRemoteExecutor refuses the same malformed K5 target. The
			// legacy dispatcher may still consume it, so bind an explicit invalid
			// marker instead of silently hashing a different policy tuple.
			parts = append(parts, "protocol-policy:invalid")
		} else {
			for _, ruleRef := range policy.ruleRefs {
				parts = append(parts, "protocol-rule:"+ruleRef)
			}
			parts = append(parts, "protocol-permission:"+policy.permissionProfileRef)
		}
		// The RESOLVED trust anchor content (inline JWKS wins, else the FILE's
		// content) — exactly what resolveTrustAnchor feeds the client, so a JWK-set
		// rotation under the same path changes the generation while an unused file
		// does not cause a false block (item 6d).
		parts = append(parts, "trust:"+resolvedTrustDigest(a))
		for _, k := range sortedStrKeys(a.Headers) {
			// Header NAME + a digest of the value (a rotation under the same name
			// must change the generation), never the raw header value. Each sub-field
			// is length-prefixed (canonJoin) so no "=" in a name/value can shift a
			// boundary to collide with a different header (item 1, review 3).
			parts = append(parts, "hdr:"+canonJoin(k, genDigest([]string{a.Headers[k]})))
		}
		m[subjectKey(kind, a.SubjectRef)] = genDigest(parts)
	}
	for _, t := range cfg.Runtime.Targets {
		kind := orDefaultStr(t.SubjectKind, "agent")
		if strings.TrimSpace(t.SubjectRef) == "" || strings.TrimSpace(t.Runtime) == "" {
			continue
		}
		parts := []string{
			"runtime", t.Runtime, t.Target, t.Environment, t.Name, t.Image, t.Command,
			"replicas:" + strconv.Itoa(t.Replicas),
		}
		// Every inner tuple is length-prefixed (canonJoin) — flattening with "="/"/"
		// let {"cpu=x":"y"} and {"cpu":"x=y"} (or an env Name/SecretRef split) collide
		// to the same preimage, a substitution WITHOUT a race (item 1, review 3).
		for _, e := range t.EnvRefs {
			parts = append(parts, "env:"+canonJoin(e.Name, e.SecretRef))
		}
		for _, w := range t.Wirings {
			parts = append(parts, "wire:"+canonJoin(w.ResourceKind, w.ResourceRef, w.Mode, w.SecretRef))
		}
		for _, k := range sortedStrKeys(t.Resources) {
			parts = append(parts, "res:"+canonJoin(k, t.Resources[k]))
		}
		m[subjectKey(kind, t.SubjectRef)] = genDigest(parts)
	}
	return &dispatcherGeneration{bySubject: m}
}

// resolvedTrustDigest mirrors resolveTrustAnchor: inline JWKS wins, else the
// file's CONTENT, else empty — so the generation folds exactly the anchor the
// dispatcher verifies against (a rotation of the used source changes it; an
// unused source does not).
func resolvedTrustDigest(a orchA2AAgentJSON) string {
	if s := strings.TrimSpace(a.TrustJWKS); s != "" {
		sum := sha256.Sum256([]byte(s))
		return "inline:" + hex.EncodeToString(sum[:])
	}
	if p := strings.TrimSpace(a.TrustJWKSFile); p != "" {
		return "file:" + fileContentDigest(p)
	}
	return ""
}

// canonJoin length-prefixes each field into one unambiguous preimage segment, so
// no controllable field can shift a delimiter boundary to collide with a
// different tuple. Nesting is safe: the output is itself a single field the
// caller length-prefixes again (canonical-hashing discipline).
func canonJoin(fields ...string) string {
	var b strings.Builder
	for _, f := range fields {
		b.WriteString(strconv.Itoa(len(f)))
		b.WriteByte(':')
		b.WriteString(f)
	}
	return b.String()
}

// sortedKeys returns a map's keys in deterministic order (canonical preimage).
func sortedStrKeys(mm map[string]string) []string {
	ks := make([]string, 0, len(mm))
	for k := range mm {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// fileContentDigest returns a stable digest of a file's CONTENT (empty for an
// empty path or an unreadable file — an unreadable trust anchor already denies
// at dispatch, and a stable empty digest keeps the generation deterministic).
func fileContentDigest(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Generation returns the config generation for a subject. A subject with NO
// provisioned target returns "" — the module's own subject/route fingerprint
// still binds, and an unwired dispatcher declares rather than acts.
func (d *dispatcherGeneration) Generation(subjectKind, subjectRef string) string {
	if d == nil {
		return ""
	}
	return d.bySubject[subjectKey(subjectKind, subjectRef)]
}

func genDigest(parts []string) string {
	// Length-prefixed canonical preimage: a controllable operator field
	// (image name, URL, header value) cannot shift a boundary to collide with a
	// different config — a substitution WITHOUT a race.
	var b strings.Builder
	b.WriteString("olivares.dispatch.generation.v2")
	for _, p := range parts {
		b.WriteString(strconv.Itoa(len(p)))
		b.WriteByte(':')
		b.WriteString(p)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

var _ orchestration.DispatcherGeneration = (*dispatcherGeneration)(nil)
