// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"github.com/olivaresai/olivares/modules/sessions"
	"strings"
	"time"
)

func remoteDigest(parts ...string) []byte {
	h := sha256.New()
	var size [8]byte
	for _, part := range parts {
		binary.BigEndian.PutUint64(size[:], uint64(len(part)))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(part))
	}
	return h.Sum(nil)
}
func remoteDigestHex(parts ...string) string { return hex.EncodeToString(remoteDigest(parts...)) }
func validProtocolReconcileHash(value string) bool {
	value = strings.TrimSpace(value)
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == value
}
func sameProtocolReconcileAnchor(left, right sessions.ProtocolBinding) bool {
	return left.ID == right.ID && left.TenantID == right.TenantID &&
		left.WorkspaceID == right.WorkspaceID && left.Version == right.Version &&
		left.BindingSpecID == right.BindingSpecID &&
		left.BindingSpecGeneration == right.BindingSpecGeneration &&
		left.WorkItemID == right.WorkItemID && left.AttemptID == right.AttemptID &&
		left.Protocol == right.Protocol && left.ProtocolVersion == right.ProtocolVersion &&
		left.PeerAuthority == right.PeerAuthority &&
		left.RemoteResourceRef == right.RemoteResourceRef &&
		left.Generation == right.Generation && left.SyntheticSID == right.SyntheticSID &&
		left.ExternalKind == right.ExternalKind && left.ExternalID == right.ExternalID &&
		left.ContextID == right.ContextID && left.ExternalMessageID == right.ExternalMessageID &&
		left.LocalState == right.LocalState && left.RemoteState == right.RemoteState &&
		left.RemoteRevision == right.RemoteRevision && left.Terminal == right.Terminal &&
		left.CancelRequested == right.CancelRequested
}
func protocolReconcileResult(
	binding sessions.ProtocolBinding,
	verdict sessions.ProtocolObservationVerdict,
	code string,
	observedAt time.Time,
	replayed bool,
	checks []sessions.ProtocolBindingRemoteCheck,
) sessions.ProtocolBindingReconcileResult {
	return sessions.ProtocolBindingReconcileResult{
		Verdict: verdict, Code: strings.TrimSpace(code), ObservedAt: observedAt.UTC(),
		Checks:  append([]sessions.ProtocolBindingRemoteCheck(nil), checks...),
		Binding: binding, Replayed: replayed,
	}
}
func orDefaultStr(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}
