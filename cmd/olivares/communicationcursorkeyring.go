// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/modules/sessions"
)

const (
	envCommunicationCursorKeyringFile = "OLIVARES_COMMUNICATION_CURSOR_KEYRING_FILE"
	communicationCursorKeyringFormat  = "olivares.communication-cursor-keyring.v1"
	communicationCursorKeyringMaxKeys = 32
)

var errCommunicationCursorKeyring = errors.New("invalid communication cursor keyring")

// communicationCursorKeyringStatus is the loggable, material-free result of
// loading the cursor keyring: which key signs, which keys verify, and how many
// retired entries fell outside the retention window.
type communicationCursorKeyringStatus struct {
	SigningKID       string   `json:"signing_kid"`
	VerificationKIDs []string `json:"verification_kids"`
	DroppedRetired   int      `json:"dropped_retired"`
}

type communicationCursorKeyringDocument struct {
	Format     string                            `json:"format"`
	CurrentKID string                            `json:"current_kid"`
	Keys       []communicationCursorKeyringEntry `json:"keys"`
}

type communicationCursorKeyringEntry struct {
	KID       string `json:"kid"`
	KeyBase64 string `json:"key_base64"`
	RetiredAt string `json:"retired_at,omitempty"`
}

// loadCommunicationCursorKeyring loads the explicit operator path through the
// SAME custody mechanism as the content keyring: the strict descriptor
// permission and size boundary, and the operator-config CMEK envelope when the
// file is sealed. It has no default path, no auto-mint and no reload: a
// restart never invents a key, so a token minted before a restart still
// verifies after it and rotation is an operator edit of this file.
//
// Retention: a key may carry retired_at. A retired key stays in the
// verification set for the token retention window (TTL plus clock skew) and
// is dropped once every token it could have signed is dead. The current key
// may not be retired.
func loadCommunicationCursorKeyring(
	ctx context.Context,
	path string,
	unwrap communicationContentKeyringUnwrap,
	now time.Time,
) (*sessions.CommunicationCursorTokenKeyring, communicationCursorKeyringStatus, error) {
	if path == "" {
		return nil, communicationCursorKeyringStatus{}, nil
	}
	if err := communicationContentContextError(ctx); err != nil {
		return nil, communicationCursorKeyringStatus{}, err
	}
	raw, err := readCommunicationContentKeyring(path)
	if err != nil {
		return nil, communicationCursorKeyringStatus{}, fmt.Errorf("%w: %w", errCommunicationCursorKeyring, err)
	}
	defer wipeCommunicationContentBytes(raw)

	document := raw
	if secure.IsSealedEnvelope(raw) {
		if unwrap == nil {
			return nil, communicationCursorKeyringStatus{}, fmt.Errorf(
				"%w: %w: keyring is sealed but no custody unwrap was supplied",
				errCommunicationCursorKeyring, errCommunicationContentCustody)
		}
		plaintext, unwrapErr := unwrap(ctx, append([]byte(nil), raw...))
		defer wipeCommunicationContentBytes(plaintext)
		if unwrapErr != nil {
			return nil, communicationCursorKeyringStatus{}, fmt.Errorf(
				"%w: %w: unwrap keyring: %w", errCommunicationCursorKeyring, errCommunicationContentCustody, unwrapErr)
		}
		document = append([]byte(nil), plaintext...)
		defer wipeCommunicationContentBytes(document)
	}
	keyring, status, err := decodeCommunicationCursorKeyring(document, now)
	if err != nil {
		return nil, communicationCursorKeyringStatus{}, err
	}
	if err := keyring.SelfTest(now); err != nil {
		return nil, communicationCursorKeyringStatus{}, fmt.Errorf(
			"%w: self-test: %w", errCommunicationCursorKeyring, err)
	}
	return keyring, status, nil
}

func decodeCommunicationCursorKeyring(
	raw []byte,
	now time.Time,
) (*sessions.CommunicationCursorTokenKeyring, communicationCursorKeyringStatus, error) {
	if len(raw) == 0 || len(raw) > communicationContentMaxKeyring {
		return nil, communicationCursorKeyringStatus{}, fmt.Errorf(
			"%w: encoded keyring size is outside 1..%d bytes", errCommunicationCursorKeyring, communicationContentMaxKeyring)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var document communicationCursorKeyringDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, communicationCursorKeyringStatus{}, fmt.Errorf("%w: %v", errCommunicationCursorKeyring, err)
	}
	if err := decoder.Decode(new(struct{})); err != io.EOF {
		return nil, communicationCursorKeyringStatus{}, fmt.Errorf("%w: trailing JSON", errCommunicationCursorKeyring)
	}
	if document.Format != communicationCursorKeyringFormat {
		return nil, communicationCursorKeyringStatus{}, fmt.Errorf(
			"%w: unsupported format %q", errCommunicationCursorKeyring, document.Format)
	}
	if document.CurrentKID == "" || len(document.Keys) == 0 || len(document.Keys) > communicationCursorKeyringMaxKeys {
		return nil, communicationCursorKeyringStatus{}, fmt.Errorf(
			"%w: current_kid and 1..%d keys are required", errCommunicationCursorKeyring, communicationCursorKeyringMaxKeys)
	}
	if now.IsZero() {
		now = time.Now()
	}
	keys := make([]sessions.CommunicationCursorTokenKey, 0, len(document.Keys))
	defer func() {
		for _, key := range keys {
			wipeCommunicationContentBytes(key.Material)
		}
	}()
	seen := make(map[string]struct{}, len(document.Keys))
	status := communicationCursorKeyringStatus{}
	for _, entry := range document.Keys {
		if entry.KID == "" || entry.KeyBase64 == "" {
			return nil, communicationCursorKeyringStatus{}, fmt.Errorf(
				"%w: each key needs kid and key_base64", errCommunicationCursorKeyring)
		}
		if _, duplicate := seen[entry.KID]; duplicate {
			return nil, communicationCursorKeyringStatus{}, fmt.Errorf(
				"%w: duplicate kid %q", errCommunicationCursorKeyring, entry.KID)
		}
		seen[entry.KID] = struct{}{}
		material, err := base64.StdEncoding.Strict().DecodeString(entry.KeyBase64)
		if err != nil || len(material) < 32 || len(material) > 4096 {
			return nil, communicationCursorKeyringStatus{}, fmt.Errorf(
				"%w: key %q must be 32..4096 canonical base64 bytes", errCommunicationCursorKeyring, entry.KID)
		}
		if entry.RetiredAt != "" {
			retiredAt, err := time.Parse(time.RFC3339, entry.RetiredAt)
			if err != nil {
				wipeCommunicationContentBytes(material)
				return nil, communicationCursorKeyringStatus{}, fmt.Errorf(
					"%w: key %q retired_at is not RFC 3339", errCommunicationCursorKeyring, entry.KID)
			}
			if entry.KID == document.CurrentKID {
				wipeCommunicationContentBytes(material)
				return nil, communicationCursorKeyringStatus{}, fmt.Errorf(
					"%w: current key %q is retired", errCommunicationCursorKeyring, entry.KID)
			}
			if now.Sub(retiredAt) > sessions.CommunicationCursorTokenRetentionWindow {
				wipeCommunicationContentBytes(material)
				status.DroppedRetired++
				continue
			}
		}
		keys = append(keys, sessions.CommunicationCursorTokenKey{KID: entry.KID, Material: material})
	}
	keyring, err := sessions.NewCommunicationCursorTokenKeyring(document.CurrentKID, keys)
	if err != nil {
		return nil, communicationCursorKeyringStatus{}, fmt.Errorf("%w: %w", errCommunicationCursorKeyring, err)
	}
	status.SigningKID = keyring.SigningKID()
	status.VerificationKIDs = keyring.VerificationKIDs()
	return keyring, status, nil
}
