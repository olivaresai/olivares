// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package helperschema

import (
	"encoding/hex"
	"errors"
	"io"
)

// operationIDBytes is the size of an operation id: 128 random bits.
const operationIDBytes = 16

// NewOperationID mints an operation id from random: 128 bits in lowercase hexadecimal. The
// client that asks for a change mints it when the operator confirms, and every mutating
// document carries it, so a helper's record and its log name the operation. It is not the
// request id a consumer of an act authorization keeps for itself.
func NewOperationID(random io.Reader) (string, error) {
	var raw [operationIDBytes]byte
	if _, err := io.ReadFull(random, raw[:]); err != nil {
		return "", errors.New("no random bytes for an operation id")
	}
	return hex.EncodeToString(raw[:]), nil
}

// operationIDShape reports whether s has an operation id's shape: 32 lowercase hexadecimal
// digits.
func operationIDShape(s string) bool { return lowerHex(s, 2*operationIDBytes) }

// requireOperationID refuses a mutating document whose operation id is missing or malformed.
func requireOperationID(id string) error {
	switch {
	case id == "":
		return refuse("$.operation_id", "required: the operation id the client minted")
	case !operationIDShape(id):
		return refuse("$.operation_id", "expected 32 lowercase hexadecimal digits")
	}
	return nil
}

// Power's closed set: reboot and shut down this host. Nothing else.
const (
	PowerReboot   = "reboot"
	PowerShutdown = "shutdown"
)

// PowerRequest is olivares-portal-power's document:
// {"verb": "reboot" | "shutdown", "operation_id": "<32 hex>"}. Both acts are mutating.
type PowerRequest struct {
	Verb        string `json:"verb"`
	OperationID string `json:"operation_id"`
}

// Subcommand implements Request.
func (r PowerRequest) Subcommand() string { return r.Verb }

// Operation implements Request.
func (r PowerRequest) Operation() string { return r.OperationID }

// Validate implements Request.
func (r PowerRequest) Validate() error {
	switch r.Verb {
	case PowerReboot, PowerShutdown:
		return requireOperationID(r.OperationID)
	case "":
		return refuse("$.verb", "required: reboot or shutdown")
	}
	return refuse("$.verb", "expected reboot or shutdown")
}

// The support-bundle helper's closed set. produce writes a bundle into the helper's own spool
// and answers the nonce it issued for it; fetch answers the bundle of a nonce it issued.
const (
	SupportBundleProduce = "produce"
	SupportBundleFetch   = "fetch"
)

// SupportBundleRequest is the support-bundle helper's document:
// {"op": "produce", "operation_id": "<32 hex>"} or {"op": "fetch", "nonce": "<a nonce the helper
// issued>"}. produce is mutating and carries its operation id; fetch changes nothing and
// carries none.
type SupportBundleRequest struct {
	Op          string `json:"op"`
	Nonce       string `json:"nonce,omitempty"`
	OperationID string `json:"operation_id,omitempty"`
}

// Subcommand implements Request.
func (r SupportBundleRequest) Subcommand() string { return r.Op }

// Operation implements Request.
func (r SupportBundleRequest) Operation() string { return r.OperationID }

// Validate implements Request. A nonce is a nonce's shape or it is refused: a file name, a path
// or anything else never reaches the helper's spool.
func (r SupportBundleRequest) Validate() error {
	switch r.Op {
	case SupportBundleProduce:
		if r.Nonce != "" {
			return refuse("$.nonce", "produce takes no nonce: the helper issues it")
		}
		return requireOperationID(r.OperationID)
	case SupportBundleFetch:
		if !nonceShape(r.Nonce) {
			return refuse("$.nonce", "expected a nonce this helper issued: 64 lowercase hexadecimal digits")
		}
		if r.OperationID != "" {
			return refuse("$.operation_id", "fetch changes nothing, so it carries no operation id")
		}
		return nil
	case "":
		return refuse("$.op", "required: produce or fetch")
	}
	return refuse("$.op", "expected produce or fetch")
}
