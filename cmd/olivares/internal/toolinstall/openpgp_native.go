// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"golang.org/x/crypto/openpgp"                  //nolint:staticcheck // frozen but complete for RSA v4 detached signatures
	"golang.org/x/crypto/openpgp/armor"            //nolint:staticcheck
	pgperrors "golang.org/x/crypto/openpgp/errors" //nolint:staticcheck
	"golang.org/x/crypto/openpgp/packet"           //nolint:staticcheck
)

// NativeOpenPGPVerifier checks a detached OpenPGP signature in process, for a
// node without a gpg binary (the release container images are distroless). It
// applies the GPG verifier's rules: the key material must carry exactly the
// pinned primary fingerprint, the signature must verify under that key, and an
// expired or revoked key or an expired signature is refused. Nothing is read
// from or written to a keyring.
type NativeOpenPGPVerifier struct{}

func (NativeOpenPGPVerifier) Describe() string { return "openpgp/go-x-crypto" }

func (NativeOpenPGPVerifier) Verify(ctx context.Context, key []byte, wantFingerprint string, signature, data []byte) (SignatureReport, error) {
	if err := ctx.Err(); err != nil {
		return SignatureReport{}, err
	}
	want := normalizeFingerprint(wantFingerprint)
	if len(want) != 40 {
		return SignatureReport{}, refuse(KindVerificationUnavailable, "pinned fingerprint %q is not a 40-hex-digit OpenPGP v4 fingerprint", wantFingerprint)
	}
	ring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(key))
	if err != nil || len(ring) != 1 {
		return SignatureReport{}, refuse(KindVerificationUnavailable, "pinned key material is not exactly one OpenPGP public key")
	}
	entity := ring[0]
	if got := strings.ToUpper(hex.EncodeToString(entity.PrimaryKey.Fingerprint[:])); got != want {
		return SignatureReport{}, refuse(KindVerificationUnavailable,
			"pinned key material carries primary fingerprint %s, not the pinned %s; the key embedded in this binary and its pin disagree, so no signature can be trusted", got, want)
	}
	now := time.Now()
	for _, identity := range entity.Identities {
		if identity.SelfSignature != nil && identity.SelfSignature.KeyExpired(now) {
			return SignatureReport{}, refuse(KindSignatureInvalid, "the signing key has expired; an expired key is not accepted")
		}
	}
	if len(entity.Revocations) > 0 {
		return SignatureReport{}, refuse(KindSignatureInvalid, "the signing key is revoked; a revoked key is not accepted")
	}
	sig, err := readDetachedSignature(signature)
	if err != nil {
		return SignatureReport{}, refuse(KindSignatureInvalid, "the signature file is not an OpenPGP detached signature: %v", err)
	}
	if sig.SigLifetimeSecs != nil && *sig.SigLifetimeSecs != 0 &&
		now.After(sig.CreationTime.Add(time.Duration(*sig.SigLifetimeSecs)*time.Second)) {
		return SignatureReport{}, refuse(KindSignatureInvalid, "the manifest signature has expired")
	}
	if _, err := openpgp.CheckDetachedSignature(ring, bytes.NewReader(data), bytes.NewReader(rawSignature(signature))); err != nil {
		if errors.Is(err, pgperrors.ErrUnknownIssuer) {
			return SignatureReport{}, refuse(KindSignatureInvalid, "the signature was made by a key that is not the pinned release key")
		}
		return SignatureReport{}, refuse(KindSignatureInvalid, "the manifest signature does not verify: the manifest or its signature was altered")
	}
	signer := want
	if sig.IssuerKeyId != nil && *sig.IssuerKeyId != entity.PrimaryKey.KeyId {
		for _, sub := range entity.Subkeys {
			if sub.PublicKey.KeyId == *sig.IssuerKeyId {
				signer = strings.ToUpper(hex.EncodeToString(sub.PublicKey.Fingerprint[:]))
			}
		}
	}
	return SignatureReport{
		PrimaryFingerprint: want,
		SigningFingerprint: signer,
		Created:            sig.CreationTime.UTC(),
		PubkeyAlgo:         fmt.Sprintf("openpgp-pk-%d", sig.PubKeyAlgo),
		HashAlgo:           fmt.Sprintf("openpgp-hash-%d", hashID(sig)),
		Verifier:           NativeOpenPGPVerifier{}.Describe(),
	}, nil
}

// rawSignature dearmors an armored signature; binary input is returned as is.
func rawSignature(signature []byte) []byte {
	if block, err := armorDecode(signature); err == nil {
		return block
	}
	return signature
}

func readDetachedSignature(signature []byte) (*packet.Signature, error) {
	p, err := packet.Read(bytes.NewReader(rawSignature(signature)))
	if err != nil {
		return nil, err
	}
	sig, ok := p.(*packet.Signature)
	if !ok {
		return nil, errors.New("first packet is not a v4 signature")
	}
	return sig, nil
}

// hashID reports the OpenPGP hash algorithm number of sig (RFC 4880 §9.4).
func hashID(sig *packet.Signature) int {
	for id, h := range map[int]string{2: "SHA-1", 8: "SHA-256", 9: "SHA-384", 10: "SHA-512", 11: "SHA-224"} {
		if sig.Hash.String() == h {
			return id
		}
	}
	return 0
}

func armorDecode(b []byte) ([]byte, error) {
	block, err := armor.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(block.Body, 1<<20))
}
