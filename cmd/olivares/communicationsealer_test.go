// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

type communicationContentTestRoot struct {
	version string
	root    []byte
}

func communicationContentTestKeyring(
	t *testing.T,
	currentSeal string,
	currentDigest string,
	roots ...communicationContentTestRoot,
) []byte {
	t.Helper()
	document := struct {
		Format               string `json:"format"`
		CurrentSealVersion   string `json:"current_seal_version"`
		CurrentDigestVersion string `json:"current_digest_version"`
		Keys                 []struct {
			Version       string `json:"version"`
			RootKeyBase64 string `json:"root_key_base64"`
		} `json:"keys"`
	}{
		Format:               communicationContentKeyringFormat,
		CurrentSealVersion:   currentSeal,
		CurrentDigestVersion: currentDigest,
	}
	for _, root := range roots {
		document.Keys = append(document.Keys, struct {
			Version       string `json:"version"`
			RootKeyBase64 string `json:"root_key_base64"`
		}{
			Version: root.version, RootKeyBase64: base64.StdEncoding.EncodeToString(root.root),
		})
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal test keyring: %v", err)
	}
	return raw
}

func communicationContentTestRootBytes(value byte) []byte {
	return bytes.Repeat([]byte{value}, 32)
}

func communicationContentMustSealer(
	t *testing.T,
	currentSeal string,
	currentDigest string,
	roots ...communicationContentTestRoot,
) *communicationContentSealer {
	t.Helper()
	sealer, err := newCommunicationContentSealer(
		communicationContentTestKeyring(t, currentSeal, currentDigest, roots...),
	)
	if err != nil {
		t.Fatalf("construct communication content sealer: %v", err)
	}
	return sealer
}

func communicationContentTestAAD() sessions.ContentAAD {
	return sessions.ContentAAD{
		TenantID:             model.NewTenantID(),
		WorkspaceID:          model.NewID(),
		ChannelID:            model.NewID(),
		EntityKind:           model.Kind("sessions.message"),
		EntityID:             model.NewID(),
		Schema:               "communication.message.v1",
		ProtectionGeneration: 7,
	}
}

func TestCommunicationContentSealerRoundTripAndWireContract(t *testing.T) {
	sealer := communicationContentMustSealer(t, "seal-v3", "digest-v8",
		communicationContentTestRoot{"seal-v3", communicationContentTestRootBytes(0x31)},
		communicationContentTestRoot{"digest-v8", communicationContentTestRootBytes(0x82)},
	)
	aad := communicationContentTestAAD()
	plaintext := []byte(`{"subject":"deployment","body":"ready"}`)

	first, sealVersion, err := sealer.Seal(context.Background(), aad, plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	second, secondVersion, err := sealer.Seal(context.Background(), aad, plaintext)
	if err != nil {
		t.Fatalf("second Seal: %v", err)
	}
	if sealVersion != "seal-v3" || secondVersion != sealVersion {
		t.Fatalf("seal versions = %q, %q", sealVersion, secondVersion)
	}
	if bytes.Equal(first, second) {
		t.Fatal("two seals reused a nonce")
	}
	if bytes.Contains(first, plaintext) {
		t.Fatal("ciphertext contains plaintext")
	}
	if !bytes.HasPrefix(first, []byte("OCC\x01")) {
		t.Fatalf("envelope prefix = %x, want OCC\\x01", first[:4])
	}
	versionLength := int(binary.BigEndian.Uint16(first[4:6]))
	if versionLength != len(sealVersion) || string(first[6:6+versionLength]) != sealVersion {
		t.Fatalf("envelope version frame = %d/%q", versionLength, first[6:6+versionLength])
	}
	opened, err := sealer.Open(context.Background(), aad, first, sealVersion)
	if err != nil || !bytes.Equal(opened, plaintext) {
		t.Fatalf("Open = %q, %v", opened, err)
	}

	digest, digestVersion, err := sealer.Digest(context.Background(), aad, plaintext)
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if digestVersion != "digest-v8" || len(digest) != 32 {
		t.Fatalf("digest version/size = %q/%d", digestVersion, len(digest))
	}
	verified, err := sealer.VerifyDigest(
		context.Background(), aad, plaintext, digest, digestVersion,
	)
	if err != nil || !verified {
		t.Fatalf("VerifyDigest = %v, %v", verified, err)
	}
	wrong := append([]byte(nil), digest...)
	wrong[len(wrong)-1] ^= 1
	verified, err = sealer.VerifyDigest(
		context.Background(), aad, plaintext, wrong, digestVersion,
	)
	if err != nil || verified {
		t.Fatalf("VerifyDigest(tampered) = %v, %v", verified, err)
	}
}

func TestCommunicationContentSealerBindsEveryAADField(t *testing.T) {
	sealer := communicationContentMustSealer(t, "v1", "v1",
		communicationContentTestRoot{"v1", communicationContentTestRootBytes(0x11)},
	)
	aad := communicationContentTestAAD()
	plaintext := []byte(`{"message":"bound"}`)
	ciphertext, version, err := sealer.Seal(context.Background(), aad, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	digest, digestVersion, err := sealer.Digest(context.Background(), aad, plaintext)
	if err != nil {
		t.Fatal(err)
	}

	mutations := map[string]func(*sessions.ContentAAD){
		"tenant": func(value *sessions.ContentAAD) { value.TenantID = model.NewTenantID() },
		"workspace": func(value *sessions.ContentAAD) {
			value.WorkspaceID = model.NewID()
		},
		"channel": func(value *sessions.ContentAAD) { value.ChannelID = model.NewID() },
		"entity_kind": func(value *sessions.ContentAAD) {
			value.EntityKind = model.Kind("sessions.message_ack")
		},
		"entity_id":  func(value *sessions.ContentAAD) { value.EntityID = model.NewID() },
		"schema":     func(value *sessions.ContentAAD) { value.Schema = "communication.ack.v1" },
		"generation": func(value *sessions.ContentAAD) { value.ProtectionGeneration++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := aad
			mutate(&changed)
			if _, err := sealer.Open(context.Background(), changed, ciphertext, version); err == nil {
				t.Fatal("Open accepted a different AAD field")
			}
			verified, err := sealer.VerifyDigest(
				context.Background(), changed, plaintext, digest, digestVersion,
			)
			if err != nil {
				t.Fatalf("VerifyDigest: %v", err)
			}
			if verified {
				t.Fatal("digest accepted a different AAD field")
			}
		})
	}
	if opened, err := sealer.Open(context.Background(), aad, ciphertext, version); err != nil ||
		!bytes.Equal(opened, plaintext) {
		t.Fatalf("unchanged AAD control = %q, %v", opened, err)
	}
}

func TestCommunicationContentSealerIndependentVersionsAndRotation(t *testing.T) {
	oldRoot := communicationContentTestRoot{"seal-v1", communicationContentTestRootBytes(0x21)}
	newRoot := communicationContentTestRoot{"seal-v2", communicationContentTestRootBytes(0x22)}
	old := communicationContentMustSealer(t, "seal-v1", "seal-v1", oldRoot, newRoot)
	aad := communicationContentTestAAD()
	plaintext := []byte(`{"rotation":1}`)
	ciphertext, oldSealVersion, err := old.Seal(context.Background(), aad, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	digest, oldDigestVersion, err := old.Digest(context.Background(), aad, plaintext)
	if err != nil {
		t.Fatal(err)
	}

	independent := communicationContentMustSealer(t, "seal-v2", "seal-v1", oldRoot, newRoot)
	_, sealVersion, err := independent.Seal(context.Background(), aad, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	_, digestVersion, err := independent.Digest(context.Background(), aad, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if sealVersion != "seal-v2" || digestVersion != "seal-v1" {
		t.Fatalf("independent current versions = %q/%q", sealVersion, digestVersion)
	}

	rotated := communicationContentMustSealer(t, "seal-v2", "seal-v2", oldRoot, newRoot)
	opened, err := rotated.Open(context.Background(), aad, ciphertext, oldSealVersion)
	if err != nil || !bytes.Equal(opened, plaintext) {
		t.Fatalf("historic Open = %q, %v", opened, err)
	}
	verified, err := rotated.VerifyDigest(
		context.Background(), aad, plaintext, digest, oldDigestVersion,
	)
	if err != nil || !verified {
		t.Fatalf("historic VerifyDigest = %v, %v", verified, err)
	}

	removed := communicationContentMustSealer(t, "seal-v2", "seal-v2", newRoot)
	if _, err := removed.Open(context.Background(), aad, ciphertext, oldSealVersion); !errors.Is(err, errCommunicationContentKeyVersion) {
		t.Fatalf("removed historic Open error = %v", err)
	}
	if _, err := removed.VerifyDigest(
		context.Background(), aad, plaintext, digest, oldDigestVersion,
	); !errors.Is(err, errCommunicationContentKeyVersion) {
		t.Fatalf("removed historic VerifyDigest error = %v", err)
	}

	if _, err := rotated.Open(context.Background(), aad, ciphertext, "seal-v2"); !errors.Is(err, errCommunicationContentEnvelope) {
		t.Fatalf("envelope/column mismatch error = %v", err)
	}
	coordinated := append([]byte(nil), ciphertext...)
	copy(coordinated[6:6+len("seal-v1")], "seal-v2")
	if _, err := rotated.Open(context.Background(), aad, coordinated, "seal-v2"); !errors.Is(err, errCommunicationContentAuthentication) {
		t.Fatalf("coordinated known-version tamper error = %v", err)
	}
	unknown := append([]byte(nil), ciphertext...)
	copy(unknown[6:6+len("seal-v1")], "seal-v9")
	if _, err := rotated.Open(context.Background(), aad, unknown, "seal-v9"); !errors.Is(err, errCommunicationContentKeyVersion) {
		t.Fatalf("unknown exact-version error = %v", err)
	}
}

func TestCommunicationContentSealerRejectsEnvelopeTamperAndTruncation(t *testing.T) {
	sealer := communicationContentMustSealer(t, "v1", "v1",
		communicationContentTestRoot{"v1", communicationContentTestRootBytes(0x31)},
	)
	aad := communicationContentTestAAD()
	ciphertext, version, err := sealer.Seal(context.Background(), aad, []byte(`{"x":1}`))
	if err != nil {
		t.Fatal(err)
	}
	for end := 0; end < len(ciphertext); end++ {
		if _, err := sealer.Open(context.Background(), aad, ciphertext[:end], version); err == nil {
			t.Fatalf("truncation at %d/%d opened", end, len(ciphertext))
		}
	}
	mutations := map[string]func([]byte) []byte{
		"magic":  func(value []byte) []byte { value[0] ^= 1; return value },
		"format": func(value []byte) []byte { value[3]++; return value },
		"version_length": func(value []byte) []byte {
			value[4], value[5] = 0, 0
			return value
		},
		"version": func(value []byte) []byte { value[6] = '\n'; return value },
		"nonce": func(value []byte) []byte {
			value[6+len(version)] ^= 1
			return value
		},
		"body":   func(value []byte) []byte { value[len(value)-1] ^= 1; return value },
		"append": func(value []byte) []byte { return append(value, 0) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := mutate(append([]byte(nil), ciphertext...))
			if _, err := sealer.Open(context.Background(), aad, changed, version); err == nil {
				t.Fatal("tampered envelope opened")
			}
		})
	}
	if _, err := sealer.Open(
		context.Background(), aad, make([]byte, communicationContentMaxEnvelope+1), version,
	); err == nil {
		t.Fatal("oversized envelope opened")
	}
	if _, _, err := sealer.Seal(
		context.Background(), aad, make([]byte, communicationContentMaxPlaintext+1),
	); err == nil {
		t.Fatal("oversized plaintext sealed")
	}
	if _, _, err := sealer.Digest(
		context.Background(), aad, make([]byte, communicationContentMaxPlaintext+1),
	); err == nil {
		t.Fatal("oversized plaintext digested")
	}
}

func TestCommunicationContentSealerKDFKnownAnswerAndPurposeSeparation(t *testing.T) {
	root := make([]byte, 32)
	for index := range root {
		root[index] = byte(index)
	}
	seal := communicationContentDeriveKey(root, communicationContentSealKDFLabel, "v7")
	digest := communicationContentDeriveKey(root, communicationContentDigestKDFLabel, "v7")
	if got := hex.EncodeToString(seal[:]); got !=
		"897d838fc16953808f7b8efaabbaa81f88e8107c75e7ab0c1bdad1a4dd38b369" {
		t.Fatalf("seal KDF KAT = %s", got)
	}
	if got := hex.EncodeToString(digest[:]); got !=
		"c9ddb8512dbcb3f040ced8ab87469b5a8ba014157e9feffeadcb43a4a9412dfd" {
		t.Fatalf("digest KDF KAT = %s", got)
	}
	if bytes.Equal(seal[:], digest[:]) {
		t.Fatal("seal and digest purposes derived the same key")
	}
	otherVersion := communicationContentDeriveKey(root, communicationContentSealKDFLabel, "v8")
	if bytes.Equal(seal[:], otherVersion[:]) {
		t.Fatal("two durable versions derived the same key")
	}
}

func TestCommunicationContentSealerEnvelopeAndDigestKnownAnswers(t *testing.T) {
	root := make([]byte, 32)
	for index := range root {
		root[index] = byte(index)
	}
	sealer := communicationContentMustSealer(t, "v7", "v7",
		communicationContentTestRoot{"v7", root},
	)
	aad := communicationContentSelfTestAAD()
	plaintext := []byte(`{"kat":true}`)
	nonce := []byte{0xa0, 0xa1, 0xa2, 0xa3, 0xa4, 0xa5, 0xa6, 0xa7, 0xa8, 0xa9, 0xaa, 0xab}
	ciphertext, err := sealer.sealWithVersion(
		context.Background(), aad, plaintext, "v7", bytes.NewReader(nonce),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(ciphertext); got !=
		"4f43430100027637a0a1a2a3a4a5a6a7a8a9aaabc493311dfcca734fe9fd14350986dad5e1c50b1019892a591b542cf4" {
		t.Fatalf("deterministic envelope KAT = %s", got)
	}
	digest, err := sealer.digestWithVersion(context.Background(), aad, plaintext, "v7")
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(digest); got !=
		"acf36c00d8ae78344a4e43995e271a83e86b7b491c871e247e872dd2114751b5" {
		t.Fatalf("digest KAT = %s", got)
	}
}

func TestCommunicationContentSealerStrictBoundedKeyring(t *testing.T) {
	root := base64.StdEncoding.EncodeToString(communicationContentTestRootBytes(1))
	root2 := base64.StdEncoding.EncodeToString(communicationContentTestRootBytes(2))
	valid := fmt.Sprintf(
		`{"format":%q,"current_seal_version":"v1","current_digest_version":"v1",`+
			`"keys":[{"version":"v1","root_key_base64":%q}]}`,
		communicationContentKeyringFormat, root,
	)
	cases := map[string]string{
		"empty":               "",
		"object_empty":        `{}`,
		"unknown_top":         strings.TrimSuffix(valid, "}") + `,"extra":true}`,
		"trailing":            valid + `{}`,
		"duplicate_format":    strings.Replace(valid, `"format":`, `"format":"x","format":`, 1),
		"duplicate_current":   strings.Replace(valid, `"current_seal_version":`, `"current_seal_version":"v1","current_seal_version":`, 1),
		"duplicate_keys":      strings.TrimSuffix(valid, "}") + `,"keys":[]}`,
		"unknown_key_field":   strings.Replace(valid, `"version":"v1"`, `"version":"v1","x":1`, 1),
		"duplicate_key_field": strings.Replace(valid, `"version":"v1"`, `"version":"v1","version":"v1"`, 1),
		"missing_root":        strings.Replace(valid, `,"root_key_base64":"`+root+`"`, "", 1),
		"empty_keys":          strings.Replace(valid, `[{"version":"v1","root_key_base64":"`+root+`"}]`, `[]`, 1),
		"wrong_root_size": strings.Replace(valid, root,
			base64.StdEncoding.EncodeToString(make([]byte, 31)), 1),
		"noncanonical_base64": strings.Replace(valid, root, root+`\n`, 1),
		"missing_current":     strings.Replace(valid, `"current_seal_version":"v1",`, "", 1),
		"unknown_current":     strings.Replace(valid, `"current_seal_version":"v1"`, `"current_seal_version":"v2"`, 1),
		"unsupported_format":  strings.Replace(valid, communicationContentKeyringFormat, "future", 1),
		"invalid_version":     strings.Replace(valid, `"version":"v1"`, `"version":" v1"`, 1),
		"keys_not_array":      strings.Replace(valid, `"keys":[`, `"keys":{`, 1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := newCommunicationContentSealer([]byte(raw)); !errors.Is(err, errCommunicationContentKeyring) {
				t.Fatalf("error = %v, want keyring sentinel", err)
			}
		})
	}
	for name, raw := range map[string][]byte{
		"raw_invalid_utf8": bytes.ReplaceAll(
			[]byte(valid), []byte(`"v1"`), []byte{'"', 0xff, '"'},
		),
		"lone_high_surrogate": []byte(fmt.Sprintf(
			`{"format":%q,"current_seal_version":"\ud800","current_digest_version":"\ud800",`+
				`"keys":[{"version":"\ud800","root_key_base64":%q}]}`,
			communicationContentKeyringFormat, root,
		)),
		"lone_low_surrogate": []byte(fmt.Sprintf(
			`{"format":%q,"current_seal_version":"\udc00","current_digest_version":"\udc00",`+
				`"keys":[{"version":"\udc00","root_key_base64":%q}]}`,
			communicationContentKeyringFormat, root,
		)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := newCommunicationContentSealer(raw); !errors.Is(err, errCommunicationContentKeyring) {
				t.Fatalf("error = %v, want keyring sentinel", err)
			}
		})
	}
	for name, escapedVersion := range map[string]string{
		"paired_surrogate": `\ud83d\ude00`,
		"replacement_rune": `v-\ufffd`,
	} {
		t.Run(name, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(
				`{"format":%q,"current_seal_version":"%s","current_digest_version":"%s",`+
					`"keys":[{"version":"%s","root_key_base64":%q}]}`,
				communicationContentKeyringFormat, escapedVersion, escapedVersion, escapedVersion, root,
			))
			if _, err := newCommunicationContentSealer(raw); err != nil {
				t.Fatalf("strict Unicode control rejected: %v", err)
			}
		})
	}

	duplicateVersion := fmt.Sprintf(
		`{"format":%q,"current_seal_version":"v1","current_digest_version":"v1",`+
			`"keys":[{"version":"v1","root_key_base64":%q},`+
			`{"version":"v1","root_key_base64":%q}]}`,
		communicationContentKeyringFormat, root, root2,
	)
	if _, err := newCommunicationContentSealer([]byte(duplicateVersion)); !errors.Is(err, errCommunicationContentKeyring) {
		t.Fatalf("duplicate version error = %v", err)
	}
	duplicateRoot := strings.Replace(duplicateVersion, `"version":"v1","root_key_base64":"`+root2+`"`,
		`"version":"v2","root_key_base64":"`+root+`"`, 1)
	if _, err := newCommunicationContentSealer([]byte(duplicateRoot)); !errors.Is(err, errCommunicationContentKeyring) {
		t.Fatalf("duplicate root error = %v", err)
	}
	if _, err := newCommunicationContentSealer(
		bytes.Repeat([]byte{' '}, communicationContentMaxKeyring+1),
	); !errors.Is(err, errCommunicationContentKeyring) {
		t.Fatalf("oversized raw keyring error = %v", err)
	}

	many := make([]communicationContentTestRoot, 0, communicationContentMaxKeys+1)
	for index := 0; index <= communicationContentMaxKeys; index++ {
		rootBytes := make([]byte, 32)
		binary.BigEndian.PutUint32(rootBytes[28:], uint32(index+1))
		many = append(many, communicationContentTestRoot{
			version: fmt.Sprintf("v-%03d", index), root: rootBytes,
		})
	}
	tooMany := communicationContentTestKeyring(t, many[0].version, many[0].version, many...)
	if _, err := newCommunicationContentSealer(tooMany); !errors.Is(err, errCommunicationContentKeyring) {
		t.Fatalf("129-key error = %v", err)
	}
	maximal := communicationContentTestKeyring(
		t, many[0].version, many[communicationContentMaxKeys-1].version,
		many[:communicationContentMaxKeys]...,
	)
	if _, err := newCommunicationContentSealer(maximal); err != nil {
		t.Fatalf("128-key control: %v", err)
	}
	maxVersion := strings.Repeat("v", communicationContentMaxVersion)
	if _, err := newCommunicationContentSealer(communicationContentTestKeyring(
		t, maxVersion, maxVersion,
		communicationContentTestRoot{maxVersion, communicationContentTestRootBytes(0x91)},
	)); err != nil {
		t.Fatalf("512-byte version rejected: %v", err)
	}
	overVersion := maxVersion + "v"
	if _, err := newCommunicationContentSealer(communicationContentTestKeyring(
		t, overVersion, overVersion,
		communicationContentTestRoot{overVersion, communicationContentTestRootBytes(0x92)},
	)); !errors.Is(err, errCommunicationContentKeyring) {
		t.Fatalf("513-byte version error = %v", err)
	}
	oversizedHeader := make([]byte,
		len(communicationContentEnvelopeMagic)+2+communicationContentMaxVersion+1+
			communicationContentNonceSize+communicationContentTagSize,
	)
	copy(oversizedHeader, communicationContentEnvelopeMagic)
	binary.BigEndian.PutUint16(oversizedHeader[len(communicationContentEnvelopeMagic):],
		communicationContentMaxVersion+1)
	copy(oversizedHeader[len(communicationContentEnvelopeMagic)+2:], overVersion)
	if _, _, _, _, err := communicationParseContentEnvelope(oversizedHeader); !errors.Is(err, errCommunicationContentEnvelope) {
		t.Fatalf("513-byte envelope version error = %v", err)
	}
}

type communicationContentCountingReader struct {
	remaining atomic.Int64
	read      atomic.Int64
}

func (r *communicationContentCountingReader) Read(dst []byte) (int, error) {
	remaining := r.remaining.Load()
	if remaining < int64(len(dst)) {
		return 0, io.ErrUnexpectedEOF
	}
	for index := range dst {
		dst[index] = byte(index + 1)
	}
	r.remaining.Add(-int64(len(dst)))
	r.read.Add(int64(len(dst)))
	return len(dst), nil
}

func TestCommunicationContentSealerConstructorSelfTestsAllKeysAndCachesReadiness(t *testing.T) {
	random := &communicationContentCountingReader{}
	random.remaining.Store(3 * communicationContentNonceSize)
	raw := communicationContentTestKeyring(t, "v1", "v2",
		communicationContentTestRoot{"v1", communicationContentTestRootBytes(0x41)},
		communicationContentTestRoot{"v2", communicationContentTestRootBytes(0x42)},
		communicationContentTestRoot{"historic", communicationContentTestRootBytes(0x43)},
	)
	sealer, err := newCommunicationContentSealerWithRandom(raw, random)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	if got := random.read.Load(); got != 3*communicationContentNonceSize {
		t.Fatalf("self-test random bytes = %d, want %d", got, 3*communicationContentNonceSize)
	}
	for index := 0; index < 100; index++ {
		ready, err := sealer.CommunicationContentSealerReady(context.Background())
		if err != nil || !ready {
			t.Fatalf("ready call %d = %v, %v", index, ready, err)
		}
	}
	if got := random.read.Load(); got != 3*communicationContentNonceSize {
		t.Fatalf("readiness consumed randomness: %d", got)
	}
	if _, err := newCommunicationContentSealerWithRandom(raw, &communicationContentCountingReader{}); err == nil {
		t.Fatal("constructor accepted a failed all-key RNG self-test")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if ready, err := sealer.CommunicationContentSealerReady(canceled); ready || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled readiness = %v, %v", ready, err)
	}
}

func TestCommunicationContentSealerReadinessUsesCachedResultOnly(t *testing.T) {
	source, err := os.ReadFile("communicationsealer.go") //nolint:gosec // structural mutant guard
	if err != nil {
		t.Fatal(err)
	}
	start := bytes.Index(source, []byte(
		"func (s *communicationContentSealer) CommunicationContentSealerReady(",
	))
	if start < 0 {
		t.Fatal("CommunicationContentSealerReady method is missing")
	}
	endOffset := bytes.Index(source[start:], []byte("\nfunc "))
	if endOffset < 0 {
		t.Fatal("cannot delimit CommunicationContentSealerReady method")
	}
	body := source[start : start+endOffset]
	expected := []byte(`func (s *communicationContentSealer) CommunicationContentSealerReady(
	ctx context.Context,
) (bool, error) {
	if s == nil || len(s.keys) == 0 {
		return false, fmt.Errorf("%w: empty implementation", errCommunicationContentSealer)
	}
	if err := communicationContentContextError(ctx); err != nil {
		return false, err
	}
	return s.selfTestReady, s.selfTestErr
}
`)
	if !bytes.Equal(body, expected) {
		t.Fatal("readiness body changed from the nil/context checks and cached-result-only implementation")
	}
}

type communicationContentStagedContext struct {
	calls    atomic.Int32
	cancelAt int32
}

func (c *communicationContentStagedContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *communicationContentStagedContext) Done() <-chan struct{}       { return nil }
func (c *communicationContentStagedContext) Value(any) any               { return nil }
func (c *communicationContentStagedContext) Err() error {
	if c.calls.Add(1) >= c.cancelAt {
		return context.Canceled
	}
	return nil
}

func TestCommunicationContentSealerChecksCancellationAfterCrypto(t *testing.T) {
	sealer := communicationContentMustSealer(t, "v1", "v1",
		communicationContentTestRoot{"v1", communicationContentTestRootBytes(0x51)},
	)
	aad := communicationContentTestAAD()
	plaintext := []byte(`{"cancel":"after"}`)
	ciphertext, version, err := sealer.Seal(context.Background(), aad, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	digest, digestVersion, err := sealer.Digest(context.Background(), aad, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		ctx  *communicationContentStagedContext
		call func(context.Context) error
	}{
		{
			name: "seal-post-encryption", ctx: &communicationContentStagedContext{cancelAt: 3},
			call: func(ctx context.Context) error {
				_, _, err := sealer.Seal(ctx, aad, plaintext)
				return err
			},
		},
		{
			name: "open-post-decryption", ctx: &communicationContentStagedContext{cancelAt: 2},
			call: func(ctx context.Context) error {
				_, err := sealer.Open(ctx, aad, ciphertext, version)
				return err
			},
		},
		{
			name: "digest-post-hmac", ctx: &communicationContentStagedContext{cancelAt: 2},
			call: func(ctx context.Context) error {
				_, _, err := sealer.Digest(ctx, aad, plaintext)
				return err
			},
		},
		{
			name: "verify-post-compare", ctx: &communicationContentStagedContext{cancelAt: 3},
			call: func(ctx context.Context) error {
				_, err := sealer.VerifyDigest(ctx, aad, plaintext, digest, digestVersion)
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(test.ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
		})
	}
	opened, err := sealer.Open(
		&communicationContentStagedContext{cancelAt: 2}, aad, ciphertext, version,
	)
	if !errors.Is(err, context.Canceled) || opened != nil {
		t.Fatalf("canceled Open returned plaintext %q, error %v", opened, err)
	}
}

func TestCommunicationContentSealerConcurrentSnapshot(t *testing.T) {
	sealer := communicationContentMustSealer(t, "seal", "digest",
		communicationContentTestRoot{"old", communicationContentTestRootBytes(0x61)},
		communicationContentTestRoot{"seal", communicationContentTestRootBytes(0x62)},
		communicationContentTestRoot{"digest", communicationContentTestRootBytes(0x63)},
	)
	aad := communicationContentTestAAD()
	const workers = 48
	const rounds = 12
	errorsFound := make(chan error, workers)
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			for round := 0; round < rounds; round++ {
				plaintext := []byte(fmt.Sprintf(`{"worker":%d,"round":%d}`, worker, round))
				ciphertext, sealVersion, err := sealer.Seal(context.Background(), aad, plaintext)
				if err != nil {
					errorsFound <- err
					return
				}
				opened, err := sealer.Open(context.Background(), aad, ciphertext, sealVersion)
				if err != nil || !bytes.Equal(opened, plaintext) {
					errorsFound <- fmt.Errorf("open = %q, %w", opened, err)
					return
				}
				digest, digestVersion, err := sealer.Digest(context.Background(), aad, plaintext)
				if err != nil {
					errorsFound <- err
					return
				}
				ok, err := sealer.VerifyDigest(
					context.Background(), aad, plaintext, digest, digestVersion,
				)
				if err != nil || !ok {
					errorsFound <- fmt.Errorf("verify = %v, %w", ok, err)
					return
				}
			}
		}(worker)
	}
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Fatalf("concurrent operation: %v", err)
	}
}

func TestCommunicationContentSealerDeepCopiesConfigAndCryptoSlices(t *testing.T) {
	raw := communicationContentTestKeyring(t, "v1", "v1",
		communicationContentTestRoot{"v1", communicationContentTestRootBytes(0xa1)},
	)
	sealer, err := newCommunicationContentSealer(raw)
	if err != nil {
		t.Fatal(err)
	}
	wipeCommunicationContentBytes(raw)
	aad := communicationContentTestAAD()
	original := []byte(`{"alias":"original"}`)
	plaintext := append([]byte(nil), original...)
	ciphertext, version, err := sealer.Seal(context.Background(), aad, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	wipeCommunicationContentBytes(plaintext)
	opened, err := sealer.Open(context.Background(), aad, ciphertext, version)
	if err != nil || !bytes.Equal(opened, original) {
		t.Fatalf("Seal retained plaintext input = %q, %v", opened, err)
	}
	wipeCommunicationContentBytes(opened)
	reopened, err := sealer.Open(context.Background(), aad, ciphertext, version)
	if err != nil || !bytes.Equal(reopened, original) {
		t.Fatalf("Open output aliased internal/input storage = %q, %v", reopened, err)
	}

	firstDigest, digestVersion, err := sealer.Digest(context.Background(), aad, original)
	if err != nil {
		t.Fatal(err)
	}
	digestControl := append([]byte(nil), firstDigest...)
	wipeCommunicationContentBytes(firstDigest)
	secondDigest, secondVersion, err := sealer.Digest(context.Background(), aad, original)
	if err != nil || secondVersion != digestVersion || !bytes.Equal(secondDigest, digestControl) {
		t.Fatalf("Digest output aliased sealer state = %x/%q, %v", secondDigest, secondVersion, err)
	}

	ciphertextInput := append([]byte(nil), ciphertext...)
	opened, err = sealer.Open(context.Background(), aad, ciphertextInput, version)
	if err != nil {
		t.Fatal(err)
	}
	wipeCommunicationContentBytes(ciphertextInput)
	if !bytes.Equal(opened, original) {
		t.Fatal("Open output aliased ciphertext input")
	}
}

func TestCommunicationContentSealerUsesHMACEqual(t *testing.T) {
	source, err := os.ReadFile("communicationsealer.go") //nolint:gosec // package-local structural mutant guard
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(source, []byte("hmac.Equal(want, digest)")) {
		t.Fatal("VerifyDigest no longer uses hmac.Equal")
	}
	if bytes.Contains(source, []byte("bytes.Equal(want, digest)")) {
		t.Fatal("VerifyDigest uses a non-constant-time digest comparison")
	}
}

func TestCommunicationContentSealerFailsClosedForNilAndInvalidInputs(t *testing.T) {
	var nilSealer *communicationContentSealer
	aad := communicationContentTestAAD()
	if _, _, err := nilSealer.Seal(context.Background(), aad, nil); err == nil {
		t.Fatal("nil sealer Seal succeeded")
	}
	if _, err := nilSealer.Open(context.Background(), aad, nil, "v1"); err == nil {
		t.Fatal("nil sealer Open succeeded")
	}
	if _, _, err := nilSealer.Digest(context.Background(), aad, nil); err == nil {
		t.Fatal("nil sealer Digest succeeded")
	}
	if ok, err := nilSealer.VerifyDigest(context.Background(), aad, nil, nil, "v1"); ok || err == nil {
		t.Fatal("nil sealer VerifyDigest succeeded")
	}
	if ready, err := nilSealer.CommunicationContentSealerReady(context.Background()); ready || err == nil {
		t.Fatal("nil sealer readiness succeeded")
	}

	sealer := communicationContentMustSealer(t, "v1", "v1",
		communicationContentTestRoot{"v1", communicationContentTestRootBytes(0x71)},
	)
	invalidAAD := aad
	invalidAAD.EntityID = ""
	if _, _, err := sealer.Seal(context.Background(), invalidAAD, nil); err == nil {
		t.Fatal("Seal accepted invalid AAD")
	}
	if _, _, err := sealer.Digest(context.Background(), invalidAAD, nil); err == nil {
		t.Fatal("Digest accepted invalid AAD")
	}
	if _, _, err := sealer.Seal(nil, aad, nil); err == nil {
		t.Fatal("Seal accepted nil context")
	}
}
