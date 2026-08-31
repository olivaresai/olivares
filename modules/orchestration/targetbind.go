// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/olivaresai/olivares/core/model"
)

// targetbind.go (D-06) — the HMAC target-binding fingerprint. It freezes
// the effect-bearing target a human approved so execution can BLOCK on any
// change (a re-pointed schedule/route, a rotated secret). The digest is an
// HMAC-SHA-256, NOT a bare SHA-256: URLs, images and command names are
// low-entropy and predictable, and notify/A2A configs carry secrets/auth by
// value, so a naked hash would be dictionary-attackable. The key is a dedicated
// 'target-binding' key (NOT the audit signing key); custody, rotation and the
// shared-across-nodes requirement are the composition root's.

// MACKeyProvider supplies the dedicated target-binding HMAC key and its id. When
// no key is available the acting step BLOCKS (deny-closed) — never a
// match-by-omission. A per-process ephemeral key would break HA/restart, so the
// composition root MUST wire a shared key from the secret store/KMS; the
// unwired default refuses rather than inventing one.
type MACKeyProvider interface {
	// MAC returns HMAC-SHA-256(msg) under the target-binding key and the key id,
	// or ok=false when no key is available.
	MAC(msg []byte) (mac []byte, keyID string, ok bool)
}

// unwiredMACKey is the deny-closed default: no key ⇒ acting steps block.
type unwiredMACKey struct{}

func (unwiredMACKey) MAC([]byte) ([]byte, string, bool) { return nil, "", false }

// staticMACKey is a fixed HMAC key (an operator secret on a single node, or a
// test). HA deployments wire a KMS/secret-store-backed provider instead.
type staticMACKey struct {
	key []byte
	id  string
}

// NewStaticMACKey builds a MACKeyProvider from a fixed key + key id.
func NewStaticMACKey(key []byte, id string) MACKeyProvider { return staticMACKey{key: key, id: id} }

func (k staticMACKey) MAC(msg []byte) ([]byte, string, bool) {
	if len(k.key) == 0 {
		return nil, "", false
	}
	h := hmac.New(sha256.New, k.key)
	h.Write(msg)
	return h.Sum(nil), k.id, true
}

// DispatcherGeneration reports the CURRENT generation of the operator-owned
// dispatcher configuration (the effective image/command/URL/skill/headers the
// dispatcher reloads — orchdispatch_load.go). Including it in the target
// fingerprint makes an operator config change void an approval. The unwired
// default returns "" (module-visible target only): the composition root wires a
// hash of the loaded dispatch config to close the operator-config vector.
type DispatcherGeneration interface {
	Generation(subjectKind, subjectRef string) string
}

type unwiredDispatcherGeneration struct{}

func (unwiredDispatcherGeneration) Generation(string, string) string { return "" }

// targetHMAC HMACs a canonical target STRING (the exact same length-prefixed
// preimage the plan hash binds — see resolveTargets), so the frozen binding and
// the approved plan describe ONE identical target and cannot diverge. ok=false ⇒
// no key ⇒ the caller BLOCKS the effect (never matches by omission).
func (m *Module) targetHMAC(canonical string) (fp, keyID string, ok bool) {
	mac, id, ok := m.macKey.MAC([]byte(canonical))
	if !ok {
		return "", "", false
	}
	return hex.EncodeToString(mac), id, true
}

// scheduleTargetString is the canonical (length-prefixed) description of a
// schedule-fire target: subject + cadence + status + the effective dispatcher
// generation. It is the SINGLE source used by resolveTargets (the plan hash),
// the run-creation freeze and the execution-time verify, so all three agree
// byte-for-byte on what "this target" is.
func (m *Module) scheduleTargetString(rec model.Record) string {
	subjectKind := rec.String(colSubjectKind)
	subjectRef := rec.String(colSubjectRef)
	return canonicalFields("schedule",
		subjectKind, subjectRef, rec.String(colCadenceSpec),
		strconv.FormatInt(rec.Int(colExpectedIvl), 10), rec.String(colDesiredStat),
		m.dispatchGen.Generation(subjectKind, subjectRef))
}

// routeTargetString is the canonical description of a notify-test target: the
// route's own opaque fingerprint (which the composition-root notifier must make
// include the operator connector config — Flag B).
func routeTargetString(routeFp string) string {
	return canonicalFields("route", routeFp)
}
