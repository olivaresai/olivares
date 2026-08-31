// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/olivaresai/olivares/core/secure"
)

var errCommunicationContentCustody = errors.New("communication content key custody unavailable")

// communicationContentKeyringUnwrap is deliberately byte-oriented. The loader
// opens and permission-checks the operator-selected descriptor itself, so an
// injected custody implementation cannot reopen a swapped pathname. It is
// invoked exactly once at boot and never from Seal, Open, Digest, or readiness.
type communicationContentKeyringUnwrap func(context.Context, []byte) ([]byte, error)

// loadCommunicationContentSealer loads only the explicit path supplied by its
// caller. It has no environment lookup, default path, auto-mint, or reload
// behavior. A plaintext keyring and a custody envelope share the same strict
// descriptor permission and size boundary; a sealed envelope additionally
// requires the injected unwrap operation.
func loadCommunicationContentSealer(
	ctx context.Context,
	path string,
	unwrap communicationContentKeyringUnwrap,
) (*communicationContentSealer, error) {
	if path == "" {
		return nil, nil
	}
	if err := communicationContentContextError(ctx); err != nil {
		return nil, err
	}
	raw, err := readCommunicationContentKeyring(path)
	if err != nil {
		return nil, err
	}
	defer wipeCommunicationContentBytes(raw)

	keyringBytes := raw
	if secure.IsSealedEnvelope(raw) {
		if unwrap == nil {
			return nil, fmt.Errorf("%w: %w: keyring is sealed but no custody unwrap was supplied",
				errCommunicationContentSealer, errCommunicationContentCustody)
		}
		plaintext, unwrapErr := unwrap(ctx, append([]byte(nil), raw...))
		defer wipeCommunicationContentBytes(plaintext)
		if unwrapErr != nil {
			return nil, fmt.Errorf("%w: %w: unwrap keyring: %w",
				errCommunicationContentSealer, errCommunicationContentCustody, unwrapErr)
		}
		keyringBytes = append([]byte(nil), plaintext...)
		defer wipeCommunicationContentBytes(keyringBytes)
		if len(keyringBytes) == 0 || len(keyringBytes) > communicationContentMaxKeyring {
			return nil, fmt.Errorf("%w: %w: unwrapped keyring size is invalid",
				errCommunicationContentSealer, errCommunicationContentKeyring)
		}
	}

	sealer, err := newCommunicationContentSealerContext(ctx, keyringBytes)
	if err != nil {
		return nil, err
	}
	return sealer, nil
}

func readCommunicationContentKeyring(path string) ([]byte, error) {
	// O_NONBLOCK is inert for regular files but prevents a path naming (or being
	// swapped to) a FIFO from wedging boot before the same-descriptor fstat can
	// reject it. Symlinks remain supported for Kubernetes Secret projections;
	// fstat always judges the opened target, never a pre-open pathname snapshot.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // explicit operator path
	if err != nil {
		return nil, fmt.Errorf("%w: open keyring %s: %w", errCommunicationContentSealer, path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("%w: fstat keyring %s: %w", errCommunicationContentSealer, path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: keyring %s is not a regular file",
			errCommunicationContentSealer, path)
	}
	mode := info.Mode()
	permissions := mode.Perm()
	// Allow only 0400/0440 secret mounts and their owner-writable 0600/0640
	// counterparts. Every other ordinary or special permission shape is denied.
	if mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return nil, fmt.Errorf("%w: refusing keyring %s with special permission bits",
			errCommunicationContentSealer, path)
	}
	switch permissions {
	case 0o400, 0o440, 0o600, 0o640:
		// Exact allowlist: shared read-only Secret mounts and owner-writable
		// equivalents, with no execute or world access.
	default:
		return nil, fmt.Errorf("%w: refusing keyring %s with permissions %04o (use 0400, 0440, 0600, or 0640)",
			errCommunicationContentSealer, path, permissions)
	}
	if info.Size() <= 0 || info.Size() > communicationContentMaxKeyring {
		return nil, fmt.Errorf("%w: keyring %s size is outside 1..%d bytes",
			errCommunicationContentSealer, path, communicationContentMaxKeyring)
	}
	raw, err := io.ReadAll(io.LimitReader(f, communicationContentMaxKeyring+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read keyring %s: %w", errCommunicationContentSealer, path, err)
	}
	if len(raw) == 0 || len(raw) > communicationContentMaxKeyring {
		return nil, fmt.Errorf("%w: keyring %s changed to an invalid size while being read",
			errCommunicationContentSealer, path)
	}
	return raw, nil
}

// openCommunicationContentKeyringOperatorConfig reuses the existing
// operator-config CMEK envelope and the single bounded custody choke point. It
// is passed into the loader explicitly by future composition; defining it here
// neither reads a path nor wires or activates K3.
func openCommunicationContentKeyringOperatorConfig(
	ctx context.Context,
	raw []byte,
) ([]byte, error) {
	if err := communicationContentContextError(ctx); err != nil {
		return nil, err
	}
	envelope, err := secure.DecodeSealedEnvelope(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: decode operator-config envelope: %w",
			errCommunicationContentCustody, err)
	}
	cfg, err := loadKeyWrapConfig()
	if err != nil {
		return nil, fmt.Errorf("%w: resolve configured KEK: %w", errCommunicationContentCustody, err)
	}
	if cfg == nil {
		return nil, fmt.Errorf("%w: no configured KEK can open the keyring envelope",
			errCommunicationContentCustody)
	}
	plaintext, err := openSealedEnvelope(ctx, cfg, envelope, secure.PurposeOperatorConfig)
	if err != nil {
		return nil, fmt.Errorf("%w: open operator-config envelope: %w",
			errCommunicationContentCustody, err)
	}
	return plaintext, nil
}

func wipeCommunicationContentBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
