// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"hash"
	"sort"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// principalAuthoritySealDomain is written verbatim as the first bytes of every
// v1 preimage. The trailing NUL is part of the domain, not a separator supplied
// by the encoder.
const principalAuthoritySealDomain = "olivares.auth.principal-authority-seal.v1\x00"

var errInvalidPrincipalAuthoritySeal = errors.New("auth: invalid principal authority seal input")

// The v1 encoding has a fixed field order and a closed set of type tags. Every
// variable-width value carries an unsigned 64-bit big-endian byte length; every
// collection carries an unsigned 64-bit big-endian element count. Integers are
// fixed-width two's-complement big-endian values. Maps and authority sets are
// copied and sorted before encoding. This is deliberately not JSON and does not
// use formatted text, so map insertion order, delimiters and formatter changes
// cannot alter or alias the preimage.
const (
	principalSealTagString byte = 0x01
	principalSealTagID     byte = 0x02
	principalSealTagTenant byte = 0x03
	principalSealTagBool   byte = 0x04
	principalSealTagInt64  byte = 0x05
	principalSealTagList   byte = 0x06
	principalSealTagMap    byte = 0x07
	principalSealTagSet    byte = 0x08
	principalSealTagNil    byte = 0x09
	principalSealTagTime   byte = 0x0a
	principalSealTagRef    byte = 0x0b
	principalSealTagFact   byte = 0x0c
)

// computePrincipalAuthoritySeal validates and seals the complete authority
// shape carried by a resolved Principal. The provenance seal bytes themselves
// are the sole excluded field: including them would make the digest recursive.
func computePrincipalAuthoritySeal(p Principal) ([sha256.Size]byte, error) {
	if !validPrincipalAuthorityShape(p) {
		return [sha256.Size]byte{}, errInvalidPrincipalAuthoritySeal
	}

	w := newPrincipalAuthoritySealWriter()
	w.str(string(p.Kind))
	w.id(p.UserID)
	w.id(p.CredID)
	w.boolean(p.Superadmin)
	w.str(p.DisplayName)
	w.i64(int64(p.AAL))
	w.stringSet(p.AMR)
	w.str(p.AgentIdentity)
	w.str(p.SessionIdentity)
	w.id(p.SessionWorkspaceID)
	w.str(p.SessionRunRef)
	w.i64(p.SessionFence)
	w.grantMap(p.grants)
	w.groupMap(p.groups)
	w.stringSet(p.audiences)
	w.id(p.actAs)
	w.confinementMap(p.confined)
	w.restrictionMap(p.restricted)
	w.str(p.localVia)
	w.str(p.localSubject)
	// A resolved principal must carry no local attribution. Encode the semantic
	// zero explicitly so adding a local shape to a future protocol requires a
	// new domain/version rather than silently changing v1.
	w.nilValue()
	w.boolean(p.localSystem)
	w.ref(p.credentialRef)
	w.tenant(p.evidence.tenant)
	w.ref(p.evidence.ref)
	w.fact(p.evidence.directoryEpoch)
	w.instant(p.evidence.observedAt)
	w.instant(p.evidence.freshUntil)
	return w.sum(), nil
}

func validPrincipalAuthoritySeal(p Principal) bool {
	want, err := computePrincipalAuthoritySeal(p)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(want[:], p.evidence.seal[:]) == 1
}

// validPrincipalAuthorityShape accepts exactly the three credential shapes the
// resolver can reconstruct: a human session, an ordinary bound token, or one of
// the two server-authored runtime tokens. Synthetic, delegated, superadmin and
// local principals cannot acquire a valid v1 seal.
func validPrincipalAuthorityShape(p Principal) bool {
	if p.localVia != "" || p.localSubject != "" || len(p.localMeta) != 0 || p.localSystem ||
		p.Superadmin || !validPrincipalAuthorityProvenanceShape(p) ||
		len(p.grants) != 1 {
		return false
	}
	role, admitted := p.grants[p.evidence.tenant]
	if !admitted {
		return false
	}

	switch p.Kind {
	case KindUser:
		return validSealedSessionPrincipal(p, role)
	case KindToken:
		if p.restricted == nil {
			return validSealedOrdinaryTokenPrincipal(p, role)
		}
		return validSealedWorkSessionPrincipal(p, role) ||
			validSealedCommunicationSessionPrincipal(p, role)
	default:
		return false
	}
}

func validPrincipalAuthorityProvenanceShape(p Principal) bool {
	evidence := p.evidence
	plainEpoch := store.AuthorizationFactRef{
		Kind: evidence.directoryEpoch.Kind, ID: evidence.directoryEpoch.ID,
		Version: evidence.directoryEpoch.Version,
	}
	return validPrincipalEvidenceTenant(evidence.tenant) &&
		validPrincipalRef(p.credentialRef) && p.credentialRef == evidence.ref &&
		p.credentialRef.kind == p.Kind && p.credentialRef.credentialID == p.CredID &&
		validPrincipalDirectoryEpochFact(evidence.tenant, evidence.directoryEpoch) &&
		evidence.directoryEpoch == plainEpoch &&
		!evidence.observedAt.IsZero() && evidence.freshUntil.After(evidence.observedAt)
}

func validSealedSessionPrincipal(p Principal, role string) bool {
	if !validPrincipalEvidenceID(p.UserID) || !IsRole(role) ||
		(p.AAL != AAL1 && p.AAL != AAL3) || !defensiveAMRValid(p.AMR) ||
		(p.AAL == AAL3 && !containsElevatedAMR(p.AMR)) ||
		p.AgentIdentity != "" || p.SessionIdentity != "" ||
		!p.SessionWorkspaceID.IsZero() || p.SessionRunRef != "" || p.SessionFence != 0 ||
		len(p.audiences) != 0 || !p.actAs.IsZero() || p.restricted != nil {
		return false
	}
	if !validSealedGroupSet(p.groups, p.evidence.tenant) {
		return false
	}
	return validSealedConfinement(p.confined, p.evidence.tenant, model.ID(""), false)
}

func validSealedOrdinaryTokenPrincipal(p Principal, role string) bool {
	return (p.UserID.IsZero() || validPrincipalEvidenceID(p.UserID)) && IsRole(role) &&
		p.AAL == 0 && len(p.AMR) == 0 && p.AgentIdentity == "" &&
		p.SessionIdentity == "" && p.SessionWorkspaceID.IsZero() &&
		p.SessionRunRef == "" && p.SessionFence == 0 && len(p.groups) == 0 &&
		len(p.audiences) == 0 && p.actAs.IsZero() && len(p.confined) == 0
}

func validSealedWorkSessionPrincipal(p Principal, role string) bool {
	if !p.UserID.IsZero() || role != workSessionRole || p.AAL != 0 || len(p.AMR) != 0 ||
		!validWorkSessionRef(p.SessionIdentity) || !p.SessionWorkspaceID.IsZero() ||
		!validWorkSessionRunRef(p.SessionRunRef) || p.SessionFence < 1 ||
		p.DisplayName != workSessionCredentialName(p.SessionRunRef, p.SessionFence) ||
		len(p.groups) != 0 || len(p.audiences) != 0 || !p.actAs.IsZero() ||
		len(p.confined) != 0 || !validRuntimeAgentIdentity(p.AgentIdentity) {
		return false
	}
	return exactRestrictedPermissionSet(p.restricted, p.evidence.tenant,
		WorkSessionLeaseWrite, WorkSessionWorkWrite)
}

func validSealedCommunicationSessionPrincipal(p Principal, role string) bool {
	if !p.UserID.IsZero() || role != communicationSessionRole || p.AAL != 0 || len(p.AMR) != 0 ||
		!validCommunicationSessionRef(p.SessionIdentity) ||
		!validCommunicationSessionWorkspaceID(p.SessionWorkspaceID) ||
		!validCommunicationSessionRunRef(p.SessionRunRef) || p.SessionFence < 1 ||
		p.DisplayName != communicationSessionName || len(p.groups) != 0 ||
		len(p.audiences) != 0 || !p.actAs.IsZero() ||
		!validCommunicationSessionAgentRef(p.AgentIdentity) ||
		!validSealedConfinement(p.confined, p.evidence.tenant, p.SessionWorkspaceID, true) {
		return false
	}
	return exactRestrictedPermissionSet(p.restricted, p.evidence.tenant,
		CommunicationSessionDeliveryRead,
		CommunicationSessionDeliveryWrite,
		CommunicationSessionMessageSendWrite,
		CommunicationSessionHandoffResponseWrite)
}

func validRuntimeAgentIdentity(identity string) bool {
	return len(identity) <= 512 && !containsSealControl(identity)
}

func containsSealControl(value string) bool {
	for _, r := range value {
		if r == '\x00' || r == '\n' || r == '\r' {
			return true
		}
	}
	return false
}

func validSealedGroupSet(groups map[model.TenantID][]string, tenant model.TenantID) bool {
	if len(groups) == 0 {
		return true
	}
	if len(groups) != 1 || len(groups[tenant]) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(groups[tenant]))
	for _, group := range groups[tenant] {
		id := model.ID(group)
		if !validPrincipalEvidenceID(id) || id.String() != group {
			return false
		}
		if _, duplicate := seen[group]; duplicate {
			return false
		}
		seen[group] = struct{}{}
	}
	return true
}

func validSealedConfinement(
	confined map[model.TenantID]model.ID,
	tenant model.TenantID,
	want model.ID,
	required bool,
) bool {
	if !required && len(confined) == 0 {
		return true
	}
	if len(confined) != 1 {
		return false
	}
	workspace, ok := confined[tenant]
	if !ok || !validPrincipalEvidenceID(workspace) {
		return false
	}
	return !required || workspace == want
}

func exactRestrictedPermissionSet(
	restricted map[model.TenantID]map[Permission]struct{},
	tenant model.TenantID,
	want ...Permission,
) bool {
	if len(restricted) != 1 {
		return false
	}
	set, ok := restricted[tenant]
	if !ok || len(set) != len(want) {
		return false
	}
	for _, permission := range want {
		if _, ok := set[permission]; !ok {
			return false
		}
	}
	return true
}

type principalAuthoritySealWriter struct {
	h       hash.Hash
	scratch [8]byte
}

func newPrincipalAuthoritySealWriter() *principalAuthoritySealWriter {
	w := &principalAuthoritySealWriter{h: sha256.New()}
	_, _ = w.h.Write([]byte(principalAuthoritySealDomain))
	return w
}

func (w *principalAuthoritySealWriter) tag(tag byte) {
	_, _ = w.h.Write([]byte{tag})
}

func (w *principalAuthoritySealWriter) length(length int) {
	binary.BigEndian.PutUint64(w.scratch[:], uint64(length))
	_, _ = w.h.Write(w.scratch[:])
}

func (w *principalAuthoritySealWriter) rawString(tag byte, value string) {
	w.tag(tag)
	w.length(len(value))
	_, _ = w.h.Write([]byte(value))
}

func (w *principalAuthoritySealWriter) str(value string) {
	w.rawString(principalSealTagString, value)
}

func (w *principalAuthoritySealWriter) id(value model.ID) {
	w.rawString(principalSealTagID, value.String())
}

func (w *principalAuthoritySealWriter) tenant(value model.TenantID) {
	w.rawString(principalSealTagTenant, value.String())
}

func (w *principalAuthoritySealWriter) boolean(value bool) {
	w.tag(principalSealTagBool)
	if value {
		_, _ = w.h.Write([]byte{1})
		return
	}
	_, _ = w.h.Write([]byte{0})
}

func (w *principalAuthoritySealWriter) i64(value int64) {
	w.tag(principalSealTagInt64)
	binary.BigEndian.PutUint64(w.scratch[:], uint64(value))
	_, _ = w.h.Write(w.scratch[:])
}

func (w *principalAuthoritySealWriter) collection(tag byte, length int) {
	w.tag(tag)
	w.length(length)
}

func (w *principalAuthoritySealWriter) nilValue() {
	w.tag(principalSealTagNil)
}

func (w *principalAuthoritySealWriter) stringSet(values []string) {
	canonical := append([]string(nil), values...)
	sort.Strings(canonical)
	w.collection(principalSealTagSet, len(canonical))
	for _, value := range canonical {
		w.str(value)
	}
}

func sortedPrincipalTenants[V any](values map[model.TenantID]V) []model.TenantID {
	keys := make([]model.TenantID, 0, len(values))
	for tenant := range values {
		keys = append(keys, tenant)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

func (w *principalAuthoritySealWriter) grantMap(values map[model.TenantID]string) {
	w.collection(principalSealTagMap, len(values))
	for _, tenant := range sortedPrincipalTenants(values) {
		w.tenant(tenant)
		w.str(values[tenant])
	}
}

func (w *principalAuthoritySealWriter) groupMap(values map[model.TenantID][]string) {
	w.collection(principalSealTagMap, len(values))
	for _, tenant := range sortedPrincipalTenants(values) {
		w.tenant(tenant)
		w.stringSet(values[tenant])
	}
}

func (w *principalAuthoritySealWriter) confinementMap(values map[model.TenantID]model.ID) {
	w.collection(principalSealTagMap, len(values))
	for _, tenant := range sortedPrincipalTenants(values) {
		w.tenant(tenant)
		w.id(values[tenant])
	}
}

func (w *principalAuthoritySealWriter) restrictionMap(
	values map[model.TenantID]map[Permission]struct{},
) {
	if values == nil {
		w.nilValue()
		return
	}
	w.collection(principalSealTagMap, len(values))
	for _, tenant := range sortedPrincipalTenants(values) {
		w.tenant(tenant)
		permissions := make([]string, 0, len(values[tenant]))
		for permission := range values[tenant] {
			permissions = append(permissions, string(permission))
		}
		w.stringSet(permissions)
	}
}

func (w *principalAuthoritySealWriter) ref(ref PrincipalRef) {
	w.tag(principalSealTagRef)
	w.str(string(ref.kind))
	w.id(ref.credentialID)
	w.i64(ref.version)
}

func (w *principalAuthoritySealWriter) fact(fact store.AuthorizationFactRef) {
	w.tag(principalSealTagFact)
	w.str(string(fact.Kind))
	w.id(fact.ID)
	w.i64(fact.Version)
}

func (w *principalAuthoritySealWriter) instant(value time.Time) {
	value = value.UTC()
	w.tag(principalSealTagTime)
	binary.BigEndian.PutUint64(w.scratch[:], uint64(value.Unix()))
	_, _ = w.h.Write(w.scratch[:])
	var nanos [4]byte
	binary.BigEndian.PutUint32(nanos[:], uint32(value.Nanosecond()))
	_, _ = w.h.Write(nanos[:])
}

func (w *principalAuthoritySealWriter) sum() [sha256.Size]byte {
	var out [sha256.Size]byte
	copy(out[:], w.h.Sum(nil))
	return out
}
