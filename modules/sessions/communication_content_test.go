// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

type communicationContentTestSealer struct {
	sealVersion   string
	digestVersion string
	sealKey       []byte
	digestKey     []byte
	sealCalls     int
	openCalls     int
	digestCalls   int
	verifyCalls   int
}

type communicationContentScratchSealer struct {
	*communicationContentTestSealer
	scratch []byte
}

func (s *communicationContentScratchSealer) Seal(
	ctx context.Context,
	aad ContentAAD,
	plaintext []byte,
) ([]byte, string, error) {
	ciphertext, version, err := s.communicationContentTestSealer.Seal(ctx, aad, plaintext)
	if err != nil {
		return nil, "", err
	}
	s.scratch = append(s.scratch[:0], ciphertext...)
	return s.scratch, version, nil
}

func (s *communicationContentScratchSealer) Digest(
	ctx context.Context,
	aad ContentAAD,
	plaintext []byte,
) ([]byte, string, error) {
	for index := range s.scratch {
		s.scratch[index] = 0
	}
	return s.communicationContentTestSealer.Digest(ctx, aad, plaintext)
}

func (s *communicationContentTestSealer) Seal(
	_ context.Context,
	aad ContentAAD,
	plaintext []byte,
) ([]byte, string, error) {
	s.sealCalls++
	prefix := communicationContentTestMAC(s.sealKey, aad, nil)
	return append(prefix, plaintext...), s.sealVersion, nil
}

func (s *communicationContentTestSealer) Open(
	_ context.Context,
	aad ContentAAD,
	ciphertext []byte,
	version string,
) ([]byte, error) {
	s.openCalls++
	if version != s.sealVersion || len(ciphertext) < sha256.Size {
		return nil, errors.New("unknown seal version")
	}
	prefix := communicationContentTestMAC(s.sealKey, aad, nil)
	if !hmac.Equal(prefix, ciphertext[:sha256.Size]) {
		return nil, errors.New("AAD mismatch")
	}
	return append([]byte(nil), ciphertext[sha256.Size:]...), nil
}

func (s *communicationContentTestSealer) Digest(
	_ context.Context,
	aad ContentAAD,
	plaintext []byte,
) ([]byte, string, error) {
	s.digestCalls++
	return communicationContentTestMAC(s.digestKey, aad, plaintext), s.digestVersion, nil
}

func (s *communicationContentTestSealer) VerifyDigest(
	_ context.Context,
	aad ContentAAD,
	plaintext []byte,
	digest []byte,
	version string,
) (bool, error) {
	s.verifyCalls++
	if version != s.digestVersion {
		return false, errors.New("unknown digest version")
	}
	want := communicationContentTestMAC(s.digestKey, aad, plaintext)
	return hmac.Equal(want, digest), nil
}

func communicationContentTestMAC(key []byte, aad ContentAAD, plaintext []byte) []byte {
	raw, err := json.Marshal(aad)
	if err != nil {
		panic(err)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(raw)
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(plaintext)
	return mac.Sum(nil)
}

func communicationContentTestAAD(slot ProtectedPayloadSlot) ContentAAD {
	schema, ok := slot.schema()
	if !ok {
		panic("unknown content slot")
	}
	return ContentAAD{
		TenantID:             model.NewTenantID(),
		WorkspaceID:          model.NewID(),
		ChannelID:            model.NewID(),
		EntityKind:           protectedPayloadEntityKind(slot),
		EntityID:             model.NewID(),
		Schema:               schema,
		ProtectionGeneration: 7,
	}
}

func communicationContentTestMessage() MessageContent {
	return MessageContent{
		Subject: "Deployment review",
		Blocks: []MessageContentBlock{{
			Type: ContentBlockText, Format: TextMarkdown, Text: "Ready for review.",
		}},
	}
}

func TestPrepareAndOpenProtectedPayloadPlainNeverUsesSealer(t *testing.T) {
	t.Parallel()

	aad := communicationContentTestAAD(PayloadSlotMessage)
	policy := ProtectedPayloadPolicy{
		Encoding: PayloadPlainJSON, ProtectionGeneration: aad.ProtectionGeneration,
	}
	payload, err := PrepareProtectedPayload(
		context.Background(), nil, PayloadSlotMessage, policy, aad,
		communicationContentTestMessage(),
	)
	if err != nil {
		t.Fatalf("prepare plain payload: %v", err)
	}
	if payload.Sealed != nil || payload.SealKeyVersion != "" || payload.DigestKeyVersion != "" {
		t.Fatalf("plain payload carries sealed metadata: %+v", payload)
	}
	wantDigest := sha256.Sum256(payload.PlainJSON)
	if !bytes.Equal(payload.Digest, wantDigest[:]) {
		t.Fatal("plain payload digest is not SHA-256 of canonical JSON")
	}
	plan, err := PlanProtectedPayloadOpen(payload, PayloadSlotMessage, policy, aad, aad)
	if err != nil {
		t.Fatalf("plan plain open: %v", err)
	}
	opened, err := OpenProtectedPayload(context.Background(), nil, plan)
	if err != nil {
		t.Fatalf("open plain payload: %v", err)
	}
	if !bytes.Equal(opened, payload.PlainJSON) {
		t.Fatalf("opened plain payload = %s, want %s", opened, payload.PlainJSON)
	}
	opened[0] ^= 1
	if bytes.Equal(opened, payload.PlainJSON) {
		t.Fatal("open returned an alias of persisted plain bytes")
	}
}

func TestPrepareAndOpenProtectedPayloadSealedPinsIndependentKeyVersions(t *testing.T) {
	t.Parallel()

	aad := communicationContentTestAAD(PayloadSlotMessage)
	policy := ProtectedPayloadPolicy{
		Encoding: PayloadSealedV1, ProtectionGeneration: aad.ProtectionGeneration,
	}
	sealer := &communicationContentTestSealer{
		sealVersion: "seal-v9", digestVersion: "digest-v12",
		sealKey: []byte("seal-key"), digestKey: []byte("digest-key"),
	}
	payload, err := PrepareProtectedPayload(
		context.Background(), sealer, PayloadSlotMessage, policy, aad,
		communicationContentTestMessage(),
	)
	if err != nil {
		t.Fatalf("prepare sealed payload: %v", err)
	}
	if payload.SealKeyVersion != "seal-v9" || payload.DigestKeyVersion != "digest-v12" ||
		payload.Sealed == nil || payload.Sealed.KeyVersion != "seal-v9" || len(payload.PlainJSON) != 0 {
		t.Fatalf("sealed versions/envelope = %+v", payload)
	}
	if sealer.sealCalls != 1 || sealer.digestCalls != 1 {
		t.Fatalf("prepare calls seal=%d digest=%d, want 1/1", sealer.sealCalls, sealer.digestCalls)
	}
	plan, err := PlanProtectedPayloadOpen(payload, PayloadSlotMessage, policy, aad, aad)
	if err != nil {
		t.Fatalf("plan sealed open: %v", err)
	}
	opened, err := OpenProtectedPayload(context.Background(), sealer, plan)
	if err != nil {
		t.Fatalf("open sealed payload: %v", err)
	}
	want, err := CanonicalProtectedPayloadSlot(PayloadSlotMessage, communicationContentTestMessage())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened, want) {
		t.Fatalf("opened sealed payload = %s, want %s", opened, want)
	}
	if sealer.openCalls != 1 || sealer.verifyCalls != 1 {
		t.Fatalf("open calls open=%d verify=%d, want 1/1", sealer.openCalls, sealer.verifyCalls)
	}
}

func TestProtectedPayloadSealerFailuresAreUnknownAndProduceNoFallback(t *testing.T) {
	t.Parallel()

	aad := communicationContentTestAAD(PayloadSlotMessage)
	policy := ProtectedPayloadPolicy{
		Encoding: PayloadSealedV1, ProtectionGeneration: aad.ProtectionGeneration,
	}
	content := communicationContentTestMessage()
	if _, err := PrepareProtectedPayload(
		context.Background(), nil, PayloadSlotMessage, policy, aad, content,
	); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("missing sealer error = %v, want evidence unknown", err)
	}

	sealer := &communicationContentTestSealer{
		sealVersion: "seal-v9", digestVersion: "digest-v12",
		sealKey: []byte("seal-key"), digestKey: []byte("digest-key"),
	}
	payload, err := PrepareProtectedPayload(
		context.Background(), sealer, PayloadSlotMessage, policy, aad, content,
	)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanProtectedPayloadOpen(payload, PayloadSlotMessage, policy, aad, aad)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("unknown persisted digest version", func(t *testing.T) {
		mutated := plan
		mutated.DigestKeyVersion = "digest-retired"
		if _, openErr := OpenProtectedPayload(context.Background(), sealer, mutated); !errors.Is(openErr, ErrCommunicationEvidenceUnknown) {
			t.Fatalf("open error = %v, want evidence unknown", openErr)
		}
	})
	t.Run("digest mismatch", func(t *testing.T) {
		mutated := plan
		mutated.Digest = append([]byte(nil), plan.Digest...)
		mutated.Digest[0] ^= 1
		if _, openErr := OpenProtectedPayload(context.Background(), sealer, mutated); !errors.Is(openErr, ErrCommunicationEvidenceUnknown) {
			t.Fatalf("open error = %v, want evidence unknown", openErr)
		}
	})
	t.Run("AAD drift cannot open the persisted ciphertext", func(t *testing.T) {
		mutated := plan
		mutated.AAD.ChannelID = model.NewID()
		if _, openErr := OpenProtectedPayload(context.Background(), sealer, mutated); !errors.Is(openErr, ErrCommunicationEvidenceUnknown) &&
			!errors.Is(openErr, ErrInvalidCommunicationModel) {
			t.Fatalf("open error = %v, want deny-closed model/evidence error", openErr)
		}
	})
	if bytes.Equal(plan.Digest, payload.Sealed.Ciphertext) {
		t.Fatalf("digest unexpectedly aliases ciphertext: %x", plan.Digest)
	}
}

func TestPrepareProtectedPayloadRejectsSealerOutputWithMissingVersions(t *testing.T) {
	t.Parallel()

	aad := communicationContentTestAAD(PayloadSlotMessage)
	sealer := &communicationContentTestSealer{
		sealKey: []byte("seal-key"), digestKey: []byte("digest-key"),
	}
	_, err := PrepareProtectedPayload(
		context.Background(), sealer, PayloadSlotMessage,
		ProtectedPayloadPolicy{
			Encoding: PayloadSealedV1, ProtectionGeneration: aad.ProtectionGeneration,
		},
		aad,
		communicationContentTestMessage(),
	)
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("malformed sealer output error = %v, want evidence unknown", err)
	}
}

func TestPrepareProtectedPayloadCopiesSealerScratchBeforeDigest(t *testing.T) {
	t.Parallel()

	aad := communicationContentTestAAD(PayloadSlotMessage)
	base := &communicationContentTestSealer{
		sealVersion: "seal-v9", digestVersion: "digest-v12",
		sealKey: []byte("seal-key"), digestKey: []byte("digest-key"),
	}
	sealer := &communicationContentScratchSealer{communicationContentTestSealer: base}
	payload, err := PrepareProtectedPayload(
		context.Background(), sealer, PayloadSlotMessage,
		ProtectedPayloadPolicy{
			Encoding: PayloadSealedV1, ProtectionGeneration: aad.ProtectionGeneration,
		},
		aad,
		communicationContentTestMessage(),
	)
	if err != nil {
		t.Fatalf("prepare with scratch sealer: %v", err)
	}
	plan, err := PlanProtectedPayloadRead(
		payload,
		PayloadSlotMessage,
		ProtectedPayloadPolicy{
			Encoding: PayloadSealedV1, ProtectionGeneration: aad.ProtectionGeneration,
		},
		aad,
		aad,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenProtectedPayload(context.Background(), sealer, plan); err != nil {
		t.Fatalf("scratch-backed ciphertext was not frozen before Digest: %v", err)
	}
}

func TestProtectedPayloadReadClassifiesMissingVersionsAndTypedNilAsUnknown(t *testing.T) {
	t.Parallel()

	aad := communicationContentTestAAD(PayloadSlotMessage)
	policy := ProtectedPayloadPolicy{
		Encoding: PayloadSealedV1, ProtectionGeneration: aad.ProtectionGeneration,
	}
	sealer := &communicationContentTestSealer{
		sealVersion: "seal-v9", digestVersion: "digest-v12",
		sealKey: []byte("seal-key"), digestKey: []byte("digest-key"),
	}
	payload, err := PrepareProtectedPayload(
		context.Background(), sealer, PayloadSlotMessage, policy, aad,
		communicationContentTestMessage(),
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*ProtectedPayload)
	}{
		{
			name: "seal version absent",
			mutate: func(value *ProtectedPayload) {
				value.SealKeyVersion = ""
				value.Sealed.KeyVersion = ""
			},
		},
		{
			name:   "digest version absent",
			mutate: func(value *ProtectedPayload) { value.DigestKeyVersion = "" },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutated := payload
			sealed := *payload.Sealed
			sealed.Ciphertext = append([]byte(nil), payload.Sealed.Ciphertext...)
			mutated.Sealed = &sealed
			test.mutate(&mutated)
			if _, planErr := PlanProtectedPayloadRead(
				mutated, PayloadSlotMessage, policy, aad, aad,
			); !errors.Is(planErr, ErrCommunicationEvidenceUnknown) {
				t.Fatalf("read plan error = %v, want evidence unknown", planErr)
			}
		})
	}

	plan, err := PlanProtectedPayloadRead(payload, PayloadSlotMessage, policy, aad, aad)
	if err != nil {
		t.Fatal(err)
	}
	plan.DigestKeyVersion = ""
	if _, err := OpenProtectedPayload(context.Background(), sealer, plan); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("missing-version open error = %v, want evidence unknown", err)
	}

	var typedNil *communicationContentTestSealer
	if _, err := PrepareProtectedPayload(
		context.Background(), typedNil, PayloadSlotMessage, policy, aad,
		communicationContentTestMessage(),
	); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("typed-nil prepare error = %v, want evidence unknown", err)
	}
	validPlan, err := PlanProtectedPayloadRead(payload, PayloadSlotMessage, policy, aad, aad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenProtectedPayload(context.Background(), typedNil, validPlan); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("typed-nil open error = %v, want evidence unknown", err)
	}
}
