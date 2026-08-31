// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

const (
	communicationContentKeyringFormat = "olivares.communication-content-keyring.v1"
	communicationContentMaxKeys       = 128
	communicationContentMaxVersion    = 512
	communicationContentMaxKeyring    = 1 << 20
	communicationContentMaxPlaintext  = 64 * 1024
	communicationContentMaxEnvelope   = 128 * 1024

	// Counter-mode SP 800-108 inputs are protocol constants. Changing any of
	// these bytes is a key rotation, not a refactor.
	communicationContentSealKDFLabel   = "olivares.sessions.communication.content-seal.v1"
	communicationContentDigestKDFLabel = "olivares.sessions.communication.content-digest.v1"

	communicationContentAADDomain     = "olivares.sessions.communication.content-aad.v1\x00"
	communicationContentSealDomain    = "olivares.sessions.communication.content-seal.v1\x00"
	communicationContentDigestDomain  = "olivares.sessions.communication.content-digest.v1\x00"
	communicationContentEnvelopeMagic = "OCC\x01"
	communicationContentNonceSize     = 12
	communicationContentTagSize       = 16
)

var (
	errCommunicationContentSealer         = errors.New("communication content sealer unavailable")
	errCommunicationContentKeyring        = errors.New("invalid communication content keyring")
	errCommunicationContentEnvelope       = errors.New("invalid communication content envelope")
	errCommunicationContentKeyVersion     = errors.New("unknown communication content key version")
	errCommunicationContentAuthentication = errors.New("communication content authentication failed")
)

// communicationContentKeyPair is derived once at construction. Arrays keep
// the immutable snapshot independent from the decoder's scratch buffers.
type communicationContentKeyPair struct {
	seal   [sha256.Size]byte
	digest [sha256.Size]byte
}

// communicationContentSealer is an immutable, process-local snapshot. Maps are
// safe for concurrent reads because neither the map nor its array values are
// exposed or changed after construction.
type communicationContentSealer struct {
	currentSealVersion   string
	currentDigestVersion string
	keys                 map[string]communicationContentKeyPair
	selfTestReady        bool
	selfTestErr          error
}

var _ sessions.CommunicationContentSealer = (*communicationContentSealer)(nil)

type communicationContentRoot struct {
	version string
	root    []byte
}

type communicationContentKeyring struct {
	currentSealVersion   string
	currentDigestVersion string
	roots                []communicationContentRoot
}

func newCommunicationContentSealer(raw []byte) (*communicationContentSealer, error) {
	return newCommunicationContentSealerContextWithRandom(context.Background(), raw, rand.Reader)
}

func newCommunicationContentSealerWithRandom(
	raw []byte,
	selfTestRandom io.Reader,
) (*communicationContentSealer, error) {
	return newCommunicationContentSealerContextWithRandom(
		context.Background(), raw, selfTestRandom,
	)
}

func newCommunicationContentSealerContext(
	ctx context.Context,
	raw []byte,
) (*communicationContentSealer, error) {
	return newCommunicationContentSealerContextWithRandom(ctx, raw, rand.Reader)
}

func newCommunicationContentSealerContextWithRandom(
	ctx context.Context,
	raw []byte,
	selfTestRandom io.Reader,
) (result *communicationContentSealer, resultErr error) {
	if err := communicationContentContextError(ctx); err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) > communicationContentMaxKeyring {
		return nil, communicationContentKeyringError("encoded keyring size is outside 1..%d bytes",
			communicationContentMaxKeyring)
	}
	if selfTestRandom == nil {
		return nil, fmt.Errorf("%w: self-test randomness is unavailable", errCommunicationContentSealer)
	}
	keyring, err := decodeCommunicationContentKeyring(raw)
	if err != nil {
		return nil, err
	}
	defer wipeCommunicationContentRoots(keyring.roots)
	if err := communicationContentContextError(ctx); err != nil {
		return nil, err
	}

	sealer := &communicationContentSealer{
		currentSealVersion:   keyring.currentSealVersion,
		currentDigestVersion: keyring.currentDigestVersion,
		keys:                 make(map[string]communicationContentKeyPair, len(keyring.roots)),
	}
	defer func() {
		if resultErr != nil {
			wipeCommunicationContentDerivedKeys(sealer.keys)
		}
	}()
	for index := range keyring.roots {
		if err := communicationContentContextError(ctx); err != nil {
			return nil, err
		}
		entry := &keyring.roots[index]
		sealer.keys[entry.version] = communicationContentKeyPair{
			seal: communicationContentDeriveKey(
				entry.root, communicationContentSealKDFLabel, entry.version,
			),
			digest: communicationContentDeriveKey(
				entry.root, communicationContentDigestKDFLabel, entry.version,
			),
		}
	}
	sealer.selfTestReady, sealer.selfTestErr = sealer.runCommunicationContentSealerSelfTest(
		ctx, selfTestRandom,
	)
	if sealer.selfTestErr != nil || !sealer.selfTestReady {
		if sealer.selfTestErr == nil {
			sealer.selfTestErr = errors.New("self-test returned false")
		}
		return nil, fmt.Errorf("%w: keyring self-test: %w",
			errCommunicationContentSealer, sealer.selfTestErr)
	}
	return sealer, nil
}

// decodeCommunicationContentKeyring is token-driven instead of relying only on
// DisallowUnknownFields: encoding/json otherwise accepts duplicate member names
// and silently lets the last root or active pointer win.
func decodeCommunicationContentKeyring(raw []byte) (communicationContentKeyring, error) {
	var out communicationContentKeyring
	if len(raw) == 0 || len(raw) > communicationContentMaxKeyring {
		return out, communicationContentKeyringError("encoded keyring size is outside 1..%d bytes",
			communicationContentMaxKeyring)
	}
	if !utf8.Valid(raw) || !communicationContentJSONSurrogatesValid(raw) {
		return out, communicationContentKeyringError("encoded keyring is not strict Unicode JSON")
	}
	decodeOK := false
	defer func() {
		if !decodeOK {
			wipeCommunicationContentRoots(out.roots)
		}
	}()
	dec := json.NewDecoder(bytes.NewReader(raw))

	if err := communicationExpectDelimiter(dec, '{'); err != nil {
		return out, communicationContentKeyringError("root must be an object: %v", err)
	}
	seen := make(map[string]struct{}, 4)
	format := ""
	for dec.More() {
		name, err := communicationReadObjectName(dec, seen)
		if err != nil {
			return out, communicationContentKeyringError("top-level member: %v", err)
		}
		switch name {
		case "format":
			if err := dec.Decode(&format); err != nil {
				return out, communicationContentKeyringError("format must be a string")
			}
		case "current_seal_version":
			if err := dec.Decode(&out.currentSealVersion); err != nil {
				return out, communicationContentKeyringError("current_seal_version must be a string")
			}
		case "current_digest_version":
			if err := dec.Decode(&out.currentDigestVersion); err != nil {
				return out, communicationContentKeyringError("current_digest_version must be a string")
			}
		case "keys":
			roots, err := communicationDecodeRoots(dec)
			if err != nil {
				return out, err
			}
			out.roots = roots
		default:
			return out, communicationContentKeyringError("unknown top-level member %q", name)
		}
	}
	if err := communicationExpectDelimiter(dec, '}'); err != nil {
		return out, communicationContentKeyringError("unterminated root object: %v", err)
	}
	if tok, err := dec.Token(); err != io.EOF {
		if err != nil {
			return out, communicationContentKeyringError("trailing JSON: %v", err)
		}
		return out, communicationContentKeyringError("trailing JSON value %v", tok)
	}

	if len(seen) != 4 || format == "" || out.currentSealVersion == "" ||
		out.currentDigestVersion == "" || out.roots == nil {
		return out, communicationContentKeyringError("format, current versions, and keys are required")
	}
	if format != communicationContentKeyringFormat {
		return out, communicationContentKeyringError("unsupported format %q", format)
	}
	if !communicationContentVersionValid(out.currentSealVersion) ||
		!communicationContentVersionValid(out.currentDigestVersion) {
		return out, communicationContentKeyringError("current key version is invalid")
	}
	versions := make(map[string]struct{}, len(out.roots))
	for index, root := range out.roots {
		if _, duplicate := versions[root.version]; duplicate {
			return out, communicationContentKeyringError("duplicate root version %q", root.version)
		}
		versions[root.version] = struct{}{}
		for prior := 0; prior < index; prior++ {
			if bytes.Equal(out.roots[prior].root, root.root) {
				return out, communicationContentKeyringError(
					"root versions %q and %q reuse root material",
					out.roots[prior].version, root.version,
				)
			}
		}
	}
	if _, ok := versions[out.currentSealVersion]; !ok {
		return out, communicationContentKeyringError(
			"current seal version %q has no root", out.currentSealVersion,
		)
	}
	if _, ok := versions[out.currentDigestVersion]; !ok {
		return out, communicationContentKeyringError(
			"current digest version %q has no root", out.currentDigestVersion,
		)
	}
	decodeOK = true
	return out, nil
}

func communicationDecodeRoots(dec *json.Decoder) ([]communicationContentRoot, error) {
	if err := communicationExpectDelimiter(dec, '['); err != nil {
		return nil, communicationContentKeyringError("keys must be an array: %v", err)
	}
	roots := make([]communicationContentRoot, 0)
	decodeOK := false
	defer func() {
		if !decodeOK {
			wipeCommunicationContentRoots(roots)
		}
	}()
	for dec.More() {
		if len(roots) == communicationContentMaxKeys {
			return nil, communicationContentKeyringError("keys exceeds %d entries", communicationContentMaxKeys)
		}
		if err := communicationExpectDelimiter(dec, '{'); err != nil {
			return nil, communicationContentKeyringError("root entry must be an object: %v", err)
		}
		seen := make(map[string]struct{}, 2)
		version := ""
		encodedRoot := ""
		for dec.More() {
			name, err := communicationReadObjectName(dec, seen)
			if err != nil {
				return nil, communicationContentKeyringError("root member: %v", err)
			}
			switch name {
			case "version":
				if err := dec.Decode(&version); err != nil {
					return nil, communicationContentKeyringError("root version must be a string")
				}
			case "root_key_base64":
				if err := dec.Decode(&encodedRoot); err != nil {
					return nil, communicationContentKeyringError("root_key_base64 must be a string")
				}
			default:
				return nil, communicationContentKeyringError("unknown root member %q", name)
			}
		}
		if err := communicationExpectDelimiter(dec, '}'); err != nil {
			return nil, communicationContentKeyringError("unterminated root entry: %v", err)
		}
		if len(seen) != 2 || !communicationContentVersionValid(version) || encodedRoot == "" {
			return nil, communicationContentKeyringError("each root needs a valid version and root_key_base64")
		}
		root, err := base64.StdEncoding.Strict().DecodeString(encodedRoot)
		if err != nil || len(root) != sha256.Size ||
			base64.StdEncoding.EncodeToString(root) != encodedRoot {
			wipeCommunicationContentBytes(root)
			return nil, communicationContentKeyringError("root %q must be exactly 32 canonical base64 bytes", version)
		}
		roots = append(roots, communicationContentRoot{version: version, root: root})
	}
	if err := communicationExpectDelimiter(dec, ']'); err != nil {
		return nil, communicationContentKeyringError("unterminated keys array: %v", err)
	}
	if len(roots) == 0 {
		return nil, communicationContentKeyringError("keys must contain at least one entry")
	}
	decodeOK = true
	return roots, nil
}

// communicationContentJSONSurrogatesValid rejects the one Unicode shape that
// encoding/json deliberately repairs instead of rejecting: an unpaired UTF-16
// surrogate escape. Literal U+FFFD remains valid; only malformed \uD800..DFFF
// escape structure is denied before the decoder can normalize it.
func communicationContentJSONSurrogatesValid(raw []byte) bool {
	inString := false
	for index := 0; index < len(raw); index++ {
		switch raw[index] {
		case '"':
			inString = !inString
		case '\\':
			if !inString || index+1 >= len(raw) {
				continue
			}
			index++
			if raw[index] != 'u' || index+4 >= len(raw) {
				continue
			}
			value, ok := communicationContentHex16(raw[index+1 : index+5])
			if !ok {
				continue
			}
			index += 4
			switch {
			case value >= 0xdc00 && value <= 0xdfff:
				return false
			case value >= 0xd800 && value <= 0xdbff:
				if index+6 >= len(raw) || raw[index+1] != '\\' || raw[index+2] != 'u' {
					return false
				}
				low, valid := communicationContentHex16(raw[index+3 : index+7])
				if !valid || low < 0xdc00 || low > 0xdfff {
					return false
				}
				index += 6
			}
		}
	}
	return true
}

func communicationContentHex16(raw []byte) (uint16, bool) {
	if len(raw) != 4 {
		return 0, false
	}
	var value uint16
	for _, digit := range raw {
		value <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			value |= uint16(digit - '0')
		case digit >= 'a' && digit <= 'f':
			value |= uint16(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			value |= uint16(digit-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

func wipeCommunicationContentRoots(roots []communicationContentRoot) {
	for index := range roots {
		wipeCommunicationContentBytes(roots[index].root)
	}
}

func wipeCommunicationContentDerivedKeys(keys map[string]communicationContentKeyPair) {
	for version, pair := range keys {
		wipeCommunicationContentBytes(pair.seal[:])
		wipeCommunicationContentBytes(pair.digest[:])
		keys[version] = pair
		delete(keys, version)
	}
}

func communicationReadObjectName(dec *json.Decoder, seen map[string]struct{}) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	name, ok := tok.(string)
	if !ok {
		return "", fmt.Errorf("member name is not a string")
	}
	if _, duplicate := seen[name]; duplicate {
		return "", fmt.Errorf("duplicate member %q", name)
	}
	seen[name] = struct{}{}
	return name, nil
}

func communicationExpectDelimiter(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != want {
		return fmt.Errorf("got %v, want %q", tok, want)
	}
	return nil
}

func communicationContentVersionValid(version string) bool {
	return len(version) > 0 && len(version) <= communicationContentMaxVersion &&
		utf8.ValidString(version) && strings.TrimSpace(version) == version &&
		!strings.ContainsAny(version, "\x00\r\n")
}

func communicationContentKeyringError(format string, args ...any) error {
	return fmt.Errorf("%w: %w: %s", errCommunicationContentSealer,
		errCommunicationContentKeyring, fmt.Sprintf(format, args...))
}

// communicationContentDeriveKey implements SP 800-108 counter mode with
// HMAC-SHA-256, r=32 and L=256:
//
//	PRF(root, [1]32 || Label || 0x00 || Context || [256]32)
//
// Context is frame32(keyring format) || frame32(version). Keeping version inside the
// KDF prevents equal root material under two durable versions from sharing a
// derived key; the distinct labels separate encryption and digest purposes.
func communicationContentDeriveKey(root []byte, label, version string) [sha256.Size]byte {
	contextFrame := communicationAppendFrame32(nil, []byte(communicationContentKeyringFormat))
	contextFrame = communicationAppendFrame32(contextFrame, []byte(version))

	mac := hmac.New(sha256.New, root)
	var counter [4]byte
	binary.BigEndian.PutUint32(counter[:], 1)
	_, _ = mac.Write(counter[:])
	_, _ = mac.Write([]byte(label))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(contextFrame)
	var outputBits [4]byte
	binary.BigEndian.PutUint32(outputBits[:], sha256.Size*8)
	_, _ = mac.Write(outputBits[:])
	var out [sha256.Size]byte
	copy(out[:], mac.Sum(nil))
	return out
}

func communicationContentAADFrame(aad sessions.ContentAAD) ([]byte, error) {
	if err := sessions.ValidateContentAAD(aad); err != nil {
		return nil, fmt.Errorf("%w: invalid AAD: %w", errCommunicationContentSealer, err)
	}
	out := make([]byte, 0, 256)
	out = append(out, communicationContentAADDomain...)
	for _, value := range []string{
		string(aad.TenantID), string(aad.WorkspaceID), string(aad.ChannelID),
		string(aad.EntityKind), string(aad.EntityID), aad.Schema,
	} {
		out = communicationAppendFrame32(out, []byte(value))
	}
	var generation [8]byte
	binary.BigEndian.PutUint64(generation[:], uint64(aad.ProtectionGeneration))
	out = append(out, generation[:]...)
	return out, nil
}

func communicationAppendFrame32(dst, value []byte) []byte {
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(value)))
	dst = append(dst, size[:]...)
	return append(dst, value...)
}

func communicationContentContextError(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", errCommunicationContentSealer)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", errCommunicationContentSealer, err)
	}
	return nil
}

func (s *communicationContentSealer) Seal(
	ctx context.Context, aad sessions.ContentAAD, plaintext []byte,
) ([]byte, string, error) {
	if s == nil {
		return nil, "", fmt.Errorf("%w: nil implementation", errCommunicationContentSealer)
	}
	ciphertext, err := s.sealWithVersion(ctx, aad, plaintext, s.currentSealVersion, rand.Reader)
	if err != nil {
		return nil, "", err
	}
	return ciphertext, s.currentSealVersion, nil
}

func (s *communicationContentSealer) sealWithVersion(
	ctx context.Context,
	aad sessions.ContentAAD,
	plaintext []byte,
	version string,
	random io.Reader,
) ([]byte, error) {
	if err := communicationContentContextError(ctx); err != nil {
		return nil, err
	}
	if len(plaintext) > communicationContentMaxPlaintext {
		return nil, fmt.Errorf("%w: plaintext exceeds %d bytes",
			errCommunicationContentSealer, communicationContentMaxPlaintext)
	}
	aadFrame, err := communicationContentAADFrame(aad)
	if err != nil {
		return nil, err
	}
	pair, ok := s.keys[version]
	if !ok {
		return nil, fmt.Errorf("%w: %w: %q", errCommunicationContentSealer,
			errCommunicationContentKeyVersion, version)
	}

	if random == nil {
		return nil, fmt.Errorf("%w: nonce randomness is unavailable", errCommunicationContentSealer)
	}
	header := make([]byte, 0, len(communicationContentEnvelopeMagic)+2+len(version))
	header = append(header, communicationContentEnvelopeMagic...)
	var versionSize [2]byte
	binary.BigEndian.PutUint16(versionSize[:], uint16(len(version)))
	header = append(header, versionSize[:]...)
	header = append(header, version...)

	block, err := aes.NewCipher(pair.seal[:])
	if err != nil {
		return nil, fmt.Errorf("%w: initialize AES-256: %w", errCommunicationContentSealer, err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || gcm.NonceSize() != communicationContentNonceSize {
		return nil, fmt.Errorf("%w: initialize AES-256-GCM", errCommunicationContentSealer)
	}
	nonce := make([]byte, communicationContentNonceSize)
	if _, err := io.ReadFull(random, nonce); err != nil {
		return nil, fmt.Errorf("%w: generate GCM nonce: %w", errCommunicationContentSealer, err)
	}
	if err := communicationContentContextError(ctx); err != nil {
		return nil, err
	}
	aeadAAD := append([]byte(nil), communicationContentSealDomain...)
	aeadAAD = communicationAppendFrame32(aeadAAD, aadFrame)
	aeadAAD = communicationAppendFrame32(aeadAAD, header)
	out := make([]byte, 0, len(header)+len(nonce)+len(plaintext)+gcm.Overhead())
	out = append(out, header...)
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, plaintext, aeadAAD)
	if err := communicationContentContextError(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *communicationContentSealer) Open(
	ctx context.Context, aad sessions.ContentAAD, ciphertext []byte, keyVersion string,
) ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: nil implementation", errCommunicationContentSealer)
	}
	if err := communicationContentContextError(ctx); err != nil {
		return nil, err
	}
	if len(ciphertext) > communicationContentMaxEnvelope {
		return nil, fmt.Errorf("%w: %w: envelope exceeds %d bytes",
			errCommunicationContentSealer, errCommunicationContentEnvelope,
			communicationContentMaxEnvelope)
	}
	aadFrame, err := communicationContentAADFrame(aad)
	if err != nil {
		return nil, err
	}
	header, nonce, body, envelopeVersion, err := communicationParseContentEnvelope(ciphertext)
	if err != nil {
		return nil, err
	}
	if !communicationContentVersionValid(keyVersion) || envelopeVersion != keyVersion {
		return nil, fmt.Errorf("%w: %w: envelope version does not match durable version",
			errCommunicationContentSealer, errCommunicationContentEnvelope)
	}
	pair, ok := s.keys[keyVersion]
	if !ok {
		return nil, fmt.Errorf("%w: %w: %q", errCommunicationContentSealer,
			errCommunicationContentKeyVersion, keyVersion)
	}
	block, err := aes.NewCipher(pair.seal[:])
	if err != nil {
		return nil, fmt.Errorf("%w: initialize AES-256: %w", errCommunicationContentSealer, err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || gcm.NonceSize() != communicationContentNonceSize {
		return nil, fmt.Errorf("%w: initialize AES-256-GCM", errCommunicationContentSealer)
	}
	aeadAAD := append([]byte(nil), communicationContentSealDomain...)
	aeadAAD = communicationAppendFrame32(aeadAAD, aadFrame)
	aeadAAD = communicationAppendFrame32(aeadAAD, header)
	plaintext, err := gcm.Open(nil, nonce, body, aeadAAD)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errCommunicationContentSealer,
			errCommunicationContentAuthentication)
	}
	if err := communicationContentContextError(ctx); err != nil {
		wipeCommunicationContentBytes(plaintext)
		return nil, err
	}
	return plaintext, nil
}

func communicationParseContentEnvelope(
	ciphertext []byte,
) (header, nonce, body []byte, version string, err error) {
	fixed := len(communicationContentEnvelopeMagic) + 2
	if len(ciphertext) < fixed+1+communicationContentNonceSize+communicationContentTagSize {
		return nil, nil, nil, "", fmt.Errorf("%w: %w: truncated envelope",
			errCommunicationContentSealer, errCommunicationContentEnvelope)
	}
	if !bytes.Equal(ciphertext[:len(communicationContentEnvelopeMagic)],
		[]byte(communicationContentEnvelopeMagic)) {
		return nil, nil, nil, "", fmt.Errorf("%w: %w: unsupported envelope format",
			errCommunicationContentSealer, errCommunicationContentEnvelope)
	}
	versionLength := int(binary.BigEndian.Uint16(ciphertext[fixed-2 : fixed]))
	headerLength := fixed + versionLength
	if versionLength < 1 || versionLength > communicationContentMaxVersion ||
		len(ciphertext) < headerLength+communicationContentNonceSize+communicationContentTagSize {
		return nil, nil, nil, "", fmt.Errorf("%w: %w: invalid version frame",
			errCommunicationContentSealer, errCommunicationContentEnvelope)
	}
	version = string(ciphertext[fixed:headerLength])
	if !communicationContentVersionValid(version) {
		return nil, nil, nil, "", fmt.Errorf("%w: %w: invalid envelope version",
			errCommunicationContentSealer, errCommunicationContentEnvelope)
	}
	header = ciphertext[:headerLength]
	nonce = ciphertext[headerLength : headerLength+communicationContentNonceSize]
	body = ciphertext[headerLength+communicationContentNonceSize:]
	return header, nonce, body, version, nil
}

func (s *communicationContentSealer) Digest(
	ctx context.Context, aad sessions.ContentAAD, plaintext []byte,
) ([]byte, string, error) {
	if s == nil {
		return nil, "", fmt.Errorf("%w: nil implementation", errCommunicationContentSealer)
	}
	digest, err := s.digestWithVersion(ctx, aad, plaintext, s.currentDigestVersion)
	if err != nil {
		return nil, "", err
	}
	return digest, s.currentDigestVersion, nil
}

func (s *communicationContentSealer) digestWithVersion(
	ctx context.Context, aad sessions.ContentAAD, plaintext []byte, version string,
) ([]byte, error) {
	if err := communicationContentContextError(ctx); err != nil {
		return nil, err
	}
	if len(plaintext) > communicationContentMaxPlaintext {
		return nil, fmt.Errorf("%w: plaintext exceeds %d bytes",
			errCommunicationContentSealer, communicationContentMaxPlaintext)
	}
	aadFrame, err := communicationContentAADFrame(aad)
	if err != nil {
		return nil, err
	}
	pair, ok := s.keys[version]
	if !ok {
		return nil, fmt.Errorf("%w: %w: %q", errCommunicationContentSealer,
			errCommunicationContentKeyVersion, version)
	}
	mac := hmac.New(sha256.New, pair.digest[:])
	_, _ = mac.Write([]byte(communicationContentDigestDomain))
	_, _ = mac.Write(communicationAppendFrame32(nil, aadFrame))
	_, _ = mac.Write(communicationAppendFrame32(nil, []byte(version)))
	_, _ = mac.Write(communicationAppendFrame32(nil, plaintext))
	digest := mac.Sum(nil)
	if err := communicationContentContextError(ctx); err != nil {
		return nil, err
	}
	return digest, nil
}

func (s *communicationContentSealer) VerifyDigest(
	ctx context.Context,
	aad sessions.ContentAAD,
	plaintext []byte,
	digest []byte,
	keyVersion string,
) (bool, error) {
	if s == nil {
		return false, fmt.Errorf("%w: nil implementation", errCommunicationContentSealer)
	}
	want, err := s.digestWithVersion(ctx, aad, plaintext, keyVersion)
	if err != nil {
		return false, err
	}
	verified := hmac.Equal(want, digest)
	if err := communicationContentContextError(ctx); err != nil {
		return false, err
	}
	return verified, nil
}

// CommunicationContentSealerReady reports the all-key self-test cached by the
// constructor. It performs no randomness, encryption, digest, or KMS work.
func (s *communicationContentSealer) CommunicationContentSealerReady(
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

func (s *communicationContentSealer) runCommunicationContentSealerSelfTest(
	ctx context.Context,
	random io.Reader,
) (bool, error) {
	if _, ok := s.keys[s.currentSealVersion]; !ok {
		return false, fmt.Errorf("%w: current seal root missing", errCommunicationContentSealer)
	}
	if _, ok := s.keys[s.currentDigestVersion]; !ok {
		return false, fmt.Errorf("%w: current digest root missing", errCommunicationContentSealer)
	}
	versions := make([]string, 0, len(s.keys))
	for version := range s.keys {
		versions = append(versions, version)
	}
	sort.Strings(versions)
	aad := communicationContentSelfTestAAD()
	plaintext := []byte(`{"self_test":"communication-content-sealer-v1"}`)
	for _, version := range versions {
		pair := s.keys[version]
		if hmac.Equal(pair.seal[:], pair.digest[:]) {
			return false, fmt.Errorf("%w: purpose separation failed for %q",
				errCommunicationContentSealer, version)
		}
		ciphertext, err := s.sealWithVersion(ctx, aad, plaintext, version, random)
		if err != nil {
			return false, fmt.Errorf("%w: seal self-test for %q: %w",
				errCommunicationContentSealer, version, err)
		}
		opened, err := s.Open(ctx, aad, ciphertext, version)
		if err != nil {
			return false, fmt.Errorf("%w: open self-test for %q: %w",
				errCommunicationContentSealer, version, err)
		}
		if !bytes.Equal(opened, plaintext) {
			return false, fmt.Errorf("%w: open self-test for %q failed",
				errCommunicationContentSealer, version)
		}
		tampered := append([]byte(nil), ciphertext...)
		tampered[len(tampered)-1] ^= 1
		if _, err := s.Open(ctx, aad, tampered, version); err == nil {
			return false, fmt.Errorf("%w: tamper self-test for %q failed",
				errCommunicationContentSealer, version)
		} else if !errors.Is(err, errCommunicationContentAuthentication) {
			return false, fmt.Errorf("%w: tamper self-test for %q returned an unexpected error: %w",
				errCommunicationContentSealer, version, err)
		}
		wrongAAD := aad
		wrongAAD.ProtectionGeneration++
		if _, err := s.Open(ctx, wrongAAD, ciphertext, version); err == nil {
			return false, fmt.Errorf("%w: AAD self-test for %q failed",
				errCommunicationContentSealer, version)
		} else if !errors.Is(err, errCommunicationContentAuthentication) {
			return false, fmt.Errorf("%w: AAD self-test for %q returned an unexpected error: %w",
				errCommunicationContentSealer, version, err)
		}
		mismatchVersion := version + "-mismatch"
		for _, candidate := range versions {
			if candidate != version {
				mismatchVersion = candidate
				break
			}
		}
		if _, err := s.Open(ctx, aad, ciphertext, mismatchVersion); err == nil {
			return false, fmt.Errorf("%w: durable-version self-test for %q failed",
				errCommunicationContentSealer, version)
		} else if !errors.Is(err, errCommunicationContentEnvelope) {
			return false, fmt.Errorf("%w: durable-version self-test for %q returned an unexpected error: %w",
				errCommunicationContentSealer, version, err)
		}
		digest, err := s.digestWithVersion(ctx, aad, plaintext, version)
		if err != nil {
			return false, fmt.Errorf("%w: digest self-test for %q: %w",
				errCommunicationContentSealer, version, err)
		}
		ok, err := s.VerifyDigest(ctx, aad, plaintext, digest, version)
		if err != nil {
			return false, fmt.Errorf("%w: verify self-test for %q: %w",
				errCommunicationContentSealer, version, err)
		}
		if !ok {
			return false, fmt.Errorf("%w: verify self-test for %q failed",
				errCommunicationContentSealer, version)
		}
		wrong := append([]byte(nil), digest...)
		wrong[0] ^= 1
		ok, err = s.VerifyDigest(ctx, aad, plaintext, wrong, version)
		if err != nil {
			return false, fmt.Errorf("%w: negative digest self-test for %q: %w",
				errCommunicationContentSealer, version, err)
		}
		if ok {
			return false, fmt.Errorf("%w: negative digest self-test for %q failed",
				errCommunicationContentSealer, version)
		}
	}
	return true, nil
}

func communicationContentSelfTestAAD() sessions.ContentAAD {
	return sessions.ContentAAD{
		TenantID:             model.TenantID("018f0c8e-7c52-7cc0-8000-000000000001"),
		WorkspaceID:          model.ID("018f0c8e-7c52-7cc0-8000-000000000002"),
		ChannelID:            model.ID("018f0c8e-7c52-7cc0-8000-000000000003"),
		EntityKind:           model.Kind("sessions.communication_sealer_self_test"),
		EntityID:             model.ID("018f0c8e-7c52-7cc0-8000-000000000004"),
		Schema:               "communication.sealer-self-test.v1",
		ProtectionGeneration: 1,
	}
}
