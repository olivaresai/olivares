//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package caep implements the wire layer for CAEP 1.0 (Continuous Access
// Evaluation Protocol) and RISC 1.0 (Risk Information Sharing and
// Coordination) Security Event Tokens. It defines the event URI taxonomy,
// subject identifier formats, and the event→action mapping. It is pure wire
// logic — it imports only the standard library. Cryptographic verification
// and the credential-cut actions live in core/auth.
//
// CAEP 1.0, RISC 1.0, and SSF 1.0 are OpenID Final specifications
// (2025-09-02). The event URIs below are committed against final text.
package caep

import (
	"encoding/json"
	"errors"
)

var ErrRequiresBusiness = errors.New("CAEP receiver requires Business")

func ActionForCAEPEvent(string) CAEPEventAction { return ActionIgnore }
func DecodeDeviceCompliance(json.RawMessage) (DeviceCompliancePayload, error) {
	return DeviceCompliancePayload{}, ErrRequiresBusiness
}
func (SubjectIdentifier) ResolveSubjectEmail() string            { return "" }
func (SubjectIdentifier) ResolveSubjectIssSub() (string, string) { return "", "" }
