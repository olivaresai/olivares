// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"errors"
	"fmt"
)

// Refusal kinds. The CLI maps them to exit codes; the kind is also printed so a
// script never has to parse prose to learn why an install did not happen.
const (
	KindInvalidRequest          = "invalid_request"
	KindUnsupportedProvider     = "unsupported_provider"
	KindUnsupportedPlatform     = "unsupported_platform"
	KindUnsupportedSource       = "unsupported_source"
	KindVersionUnknown          = "version_unknown"
	KindVerificationUnavailable = "verification_unavailable"
	KindSignatureInvalid        = "signature_invalid"
	KindManifestInvalid         = "manifest_invalid"
	KindResponseTooLarge        = "response_too_large"
	KindTransport               = "transport"
	KindDigestMismatch          = "digest_mismatch"
	KindSizeMismatch            = "size_mismatch"
	KindProbeFailed             = "probe_failed"
	KindProbeMismatch           = "probe_mismatch"
	KindDestinationUnsafe       = "destination_unsafe"
	KindDestinationNotWritable  = "destination_not_writable"
	KindLocked                  = "locked"
	KindDamaged                 = "damaged"
	KindConflict                = "conflict"
	KindPlanChanged             = "plan_changed"
)

// Refusal is a typed reason the engine declined to proceed. Every refusal
// leaves prior releases untouched; the message says what was refused and why.
type Refusal struct {
	Kind string
	Err  error
}

func (r *Refusal) Error() string {
	if r.Err == nil {
		return r.Kind
	}
	return r.Kind + ": " + r.Err.Error()
}

func (r *Refusal) Unwrap() error { return r.Err }

func refuse(kind string, format string, args ...any) error {
	return &Refusal{Kind: kind, Err: fmt.Errorf(format, args...)}
}

// KindOf returns the refusal kind carried by err, or "" for an ordinary error.
func KindOf(err error) string {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Kind
	}
	return ""
}
