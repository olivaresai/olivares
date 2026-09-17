// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/modules/models"
)

const (
	envModelGatewayProfiles = "OLIVARES_MODEL_GATEWAY_PROFILES_CONFIG"
	modelGatewayProfilesV1  = "olivares.model-gateway-profiles.v1"
	profileRevisionDomain   = "olivares.model-gateway-profile.revision.v1"
)

type modelGatewayProfilesDocument struct {
	SchemaVersion string                      `json:"schema_version"`
	Profiles      []modelGatewayProfileConfig `json:"profiles"`
}

// modelGatewayProfileConfig is operator-owned immutable configuration. CredentialRef
// is a reference only; this C2A registry validates its recognized grammar but never
// asks a resolver for the credential value.
type modelGatewayProfileConfig struct {
	Ref                string `json:"ref"`
	Revision           string `json:"revision"`
	TenantRef          string `json:"tenant_ref"`
	Action             string `json:"action"`
	Protocol           string `json:"protocol"`
	AdapterID          string `json:"adapter_id"`
	AdapterVersion     string `json:"adapter_version"`
	ProviderRef        string `json:"provider_ref"`
	ModelRef           string `json:"model_ref"`
	Endpoint           string `json:"endpoint"`
	Surface            string `json:"surface"`
	InferenceGeo       string `json:"inference_geo"`
	CredentialAudience string `json:"credential_audience"`
	AuthScheme         string `json:"auth_scheme"`
	CredentialRef      string `json:"credential_ref"`
	AllowHTTP          bool   `json:"allow_http"`
	MaxRequestBytes    int64  `json:"max_request_bytes"`
	MaxResponseBytes   int64  `json:"max_response_bytes"`
	TimeoutMS          int64  `json:"timeout_ms"`
}

type modelGatewayProfileKey struct {
	tenant   model.TenantID
	ref      string
	revision string
}

// modelGatewayProfileRegistry is immutable after construction. Values contain only
// scalar snapshots, and Resolve returns a new value, so callers cannot alter a later
// lookup. The retained credential reference is not projected into the models module.
type modelGatewayProfileRegistry struct {
	profiles map[modelGatewayProfileKey]modelGatewayProfileConfig
}

func loadModelGatewayProfiles(getenv func(string) string, log *slog.Logger) (*modelGatewayProfileRegistry, error) {
	path := strings.TrimSpace(getenv(envModelGatewayProfiles))
	if path == "" {
		return nil, nil
	}
	// C2A must remain local and credential-free. Read the selected JSON directly;
	// unlike general sealed operator configuration, this path cannot invoke a KMS.
	b, err := os.ReadFile(path) //nolint:gosec // operator-selected local config path
	if err != nil {
		return nil, fmt.Errorf("%s is set but the profile file cannot be read: %w", envModelGatewayProfiles, err)
	}
	doc, err := decodeModelGatewayProfiles(b)
	if err != nil {
		return nil, fmt.Errorf("%s is set but the profile file is invalid: %w", envModelGatewayProfiles, err)
	}
	registry, err := newModelGatewayProfileRegistry(doc)
	if err != nil {
		return nil, fmt.Errorf("%s is set but the profile file is invalid: %w", envModelGatewayProfiles, err)
	}
	if log != nil {
		log.Info("models: immutable model-gateway execution profiles loaded", "profiles", len(registry.profiles))
	}
	return registry, nil
}

func decodeModelGatewayProfiles(b []byte) (modelGatewayProfilesDocument, error) {
	if !utf8.Valid(b) {
		return modelGatewayProfilesDocument{}, errors.New("profile JSON is not valid UTF-8")
	}
	if err := rejectUnsupportedJSONMembers(b); err != nil {
		return modelGatewayProfilesDocument{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var doc modelGatewayProfilesDocument
	if err := dec.Decode(&doc); err != nil {
		return modelGatewayProfilesDocument{}, fmt.Errorf("decode profile JSON: %w", err)
	}
	if err := requireJSONEOF(dec); err != nil {
		return modelGatewayProfilesDocument{}, err
	}
	if doc.SchemaVersion != modelGatewayProfilesV1 {
		return modelGatewayProfilesDocument{}, fmt.Errorf("schema_version must be %q", modelGatewayProfilesV1)
	}
	if doc.Profiles == nil {
		return modelGatewayProfilesDocument{}, errors.New("profiles must be present (an empty array is permitted)")
	}
	return doc, nil
}

func newModelGatewayProfileRegistry(doc modelGatewayProfilesDocument) (*modelGatewayProfileRegistry, error) {
	registry := &modelGatewayProfileRegistry{profiles: make(map[modelGatewayProfileKey]modelGatewayProfileConfig, len(doc.Profiles))}
	audiences := make(map[string]string)
	refs := make(map[string]struct{})
	for i, raw := range doc.Profiles {
		profile, tenant, err := validateModelGatewayProfile(raw)
		if err != nil {
			return nil, fmt.Errorf("profiles[%d]: %w", i, err)
		}
		key := modelGatewayProfileKey{tenant: tenant, ref: profile.Ref, revision: profile.Revision}
		refKey := tenant.String() + "\x00" + profile.Ref
		if _, exists := registry.profiles[key]; exists {
			return nil, fmt.Errorf("profiles[%d]: duplicate tenant/ref/revision", i)
		}
		if _, exists := refs[refKey]; exists {
			return nil, fmt.Errorf("profiles[%d]: ref must be unique within its tenant", i)
		}
		if profile.CredentialRef != "" {
			if audience, exists := audiences[profile.CredentialRef]; exists && audience != profile.CredentialAudience {
				return nil, fmt.Errorf("profiles[%d]: one credential_ref cannot name different credential_audience values", i)
			}
			audiences[profile.CredentialRef] = profile.CredentialAudience
		}
		refs[refKey] = struct{}{}
		registry.profiles[key] = profile
	}
	return registry, nil
}

func validateModelGatewayProfile(raw modelGatewayProfileConfig) (modelGatewayProfileConfig, model.TenantID, error) {
	p := normalizedModelGatewayProfile(raw)
	tenant, err := model.ParseTenantID(p.TenantRef)
	if err != nil || tenant.IsZero() || tenant.IsSystem() {
		return modelGatewayProfileConfig{}, "", errors.New("tenant_ref must be a business tenant UUID")
	}
	p.TenantRef = tenant.String()
	if p.Ref == "" {
		return modelGatewayProfileConfig{}, "", errors.New("ref is required")
	}
	if p.Action != models.ExecutionActionTextGenerate {
		return modelGatewayProfileConfig{}, "", errors.New("action is unsupported")
	}
	if p.Protocol != models.ExecutionProtocolChatTextV1 || p.AdapterID != models.ExecutionAdapterModelProviderChat ||
		p.AdapterVersion != models.ExecutionAdapterVersion1 {
		return modelGatewayProfileConfig{}, "", errors.New("protocol or adapter is unsupported")
	}
	if p.ProviderRef == "" || p.ModelRef == "" || p.Surface == "" || p.InferenceGeo == "" {
		return modelGatewayProfileConfig{}, "", errors.New("provider_ref, model_ref, surface and inference_geo are required")
	}
	origin, err := profileEndpointOrigin(p.Endpoint, p.AllowHTTP)
	if err != nil {
		return modelGatewayProfileConfig{}, "", err
	}
	if p.CredentialAudience == "" || p.CredentialAudience != origin {
		return modelGatewayProfileConfig{}, "", errors.New("credential_audience must exactly equal the endpoint origin")
	}
	switch p.AuthScheme {
	case "bearer":
		if !secret.IsReference(p.CredentialRef) {
			return modelGatewayProfileConfig{}, "", errors.New("credential_ref must use a recognized secret-reference scheme for bearer auth")
		}
	case "none":
		if p.CredentialRef != "" {
			return modelGatewayProfileConfig{}, "", errors.New("credential_ref must be empty for auth_scheme none")
		}
	default:
		return modelGatewayProfileConfig{}, "", errors.New("auth_scheme must be bearer or none")
	}
	maxInt := int64(^uint(0) >> 1)
	if p.MaxRequestBytes <= 0 || p.MaxRequestBytes > maxInt || p.MaxResponseBytes <= 0 ||
		p.MaxResponseBytes > maxInt || p.MaxResponseBytes == math.MaxInt64 {
		return modelGatewayProfileConfig{}, "", errors.New("max_request_bytes and max_response_bytes must be positive supported integers")
	}
	if p.TimeoutMS <= 0 || p.TimeoutMS > math.MaxInt64/int64(time.Millisecond) {
		return modelGatewayProfileConfig{}, "", errors.New("timeout_ms must be a positive duration representable by time.Duration")
	}
	want, err := calculateModelGatewayProfileRevision(p)
	if err != nil {
		return modelGatewayProfileConfig{}, "", err
	}
	if !validSHA256Revision(p.Revision) || p.Revision != want {
		return modelGatewayProfileConfig{}, "", errors.New("revision must be the sha256 digest of the complete canonical profile")
	}
	return p, tenant, nil
}

func normalizedModelGatewayProfile(p modelGatewayProfileConfig) modelGatewayProfileConfig {
	p.Ref = strings.TrimSpace(p.Ref)
	p.Revision = strings.TrimSpace(p.Revision)
	p.TenantRef = strings.TrimSpace(p.TenantRef)
	p.Action = strings.TrimSpace(p.Action)
	p.Protocol = strings.TrimSpace(p.Protocol)
	p.AdapterID = strings.TrimSpace(p.AdapterID)
	p.AdapterVersion = strings.TrimSpace(p.AdapterVersion)
	p.ProviderRef = strings.TrimSpace(p.ProviderRef)
	p.ModelRef = strings.TrimSpace(p.ModelRef)
	p.Endpoint = strings.TrimSpace(p.Endpoint)
	p.Surface = strings.TrimSpace(p.Surface)
	p.InferenceGeo = strings.TrimSpace(p.InferenceGeo)
	p.CredentialAudience = strings.TrimSpace(p.CredentialAudience)
	p.AuthScheme = strings.TrimSpace(p.AuthScheme)
	p.CredentialRef = strings.TrimSpace(p.CredentialRef)
	return p
}

// calculateModelGatewayProfileRevision is the pure content-addressing helper used by
// validation and mirrored by the operator example. It hashes domain + NUL followed by
// ordered name/NUL/UTF-8-byte-length/NUL/value frames. Length framing is unambiguous and
// does not depend on a language's JSON escaping. Revision and resolved secret values are
// necessarily excluded.
func calculateModelGatewayProfileRevision(p modelGatewayProfileConfig) (string, error) {
	p = normalizedModelGatewayProfile(p)
	fields := [][2]string{
		{"ref", p.Ref},
		{"tenant_ref", p.TenantRef},
		{"action", p.Action},
		{"protocol", p.Protocol},
		{"adapter_id", p.AdapterID},
		{"adapter_version", p.AdapterVersion},
		{"provider_ref", p.ProviderRef},
		{"model_ref", p.ModelRef},
		{"endpoint", p.Endpoint},
		{"surface", p.Surface},
		{"inference_geo", p.InferenceGeo},
		{"credential_audience", p.CredentialAudience},
		{"auth_scheme", p.AuthScheme},
		{"credential_ref", p.CredentialRef},
		{"allow_http", strconv.FormatBool(p.AllowHTTP)},
		{"max_request_bytes", strconv.FormatInt(p.MaxRequestBytes, 10)},
		{"max_response_bytes", strconv.FormatInt(p.MaxResponseBytes, 10)},
		{"timeout_ms", strconv.FormatInt(p.TimeoutMS, 10)},
	}
	h := sha256.New()
	_, _ = h.Write([]byte(profileRevisionDomain))
	_, _ = h.Write([]byte{0})
	for _, field := range fields {
		value := []byte(field[1])
		_, _ = h.Write([]byte(field[0]))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(strconv.Itoa(len(value))))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(value)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func validSHA256Revision(s string) bool {
	if len(s) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(s, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(s, "sha256:"))
	return err == nil
}

func profileEndpointOrigin(endpoint string, allowHTTP bool) (string, error) {
	if endpoint == "" || strings.ContainsAny(endpoint, "?#") {
		return "", errors.New("endpoint must be one complete URL without query or fragment")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Opaque != "" || u.Hostname() == "" || u.User != nil ||
		(u.Scheme != "https" && !(allowHTTP && u.Scheme == "http")) {
		return "", errors.New("endpoint must be an absolute HTTPS URL (HTTP requires allow_http)")
	}
	if strings.HasSuffix(u.Host, ":") {
		return "", errors.New("endpoint has an invalid port")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", errors.New("endpoint has an invalid port")
		}
	}
	return u.Scheme + "://" + u.Host, nil
}

func (r *modelGatewayProfileRegistry) ResolveExecutionProfile(_ context.Context, tenant model.TenantID, ref, revision string) (models.ExecutionProfile, error) {
	if r == nil {
		return models.ExecutionProfile{}, models.ErrExecutionProfileUnavailable
	}
	p, ok := r.profiles[modelGatewayProfileKey{tenant: tenant, ref: ref, revision: revision}]
	if !ok {
		return models.ExecutionProfile{}, models.ErrExecutionProfileUnavailable
	}
	return models.ExecutionProfile{
		Tenant: tenant, Ref: p.Ref, Revision: p.Revision, Action: p.Action,
		Protocol: p.Protocol, AdapterID: p.AdapterID, AdapterVersion: p.AdapterVersion,
		ProviderRef: p.ProviderRef, ModelRef: p.ModelRef, Endpoint: p.Endpoint,
		Surface: p.Surface, InferenceGeo: p.InferenceGeo,
		CredentialAudience: p.CredentialAudience, AuthScheme: p.AuthScheme,
		TransportKey: tenant.String() + "\x00" + p.Ref + "\x00" + p.Revision,
		AllowHTTP:    p.AllowHTTP, MaxRequestBytes: int(p.MaxRequestBytes),
		MaxResponseBytes: int(p.MaxResponseBytes), Timeout: time.Duration(p.TimeoutMS) * time.Millisecond,
	}, nil
}

// modelGatewayProfileJSONShape describes one object shape this configuration document
// actually has: the exact member spellings that shape accepts, and, for a member whose
// value carries further objects, the shape of those objects. Only the document root and
// one profile entry are described. It is a statement of the two real shapes, not a schema
// engine. A nil shape means no shape is declared at that position, where member names are
// judged for exact repetition only.
type modelGatewayProfileJSONShape struct {
	members map[string]struct{}
	nested  map[string]*modelGatewayProfileJSONShape
}

// member reports whether name is supported at this shape, spelled exactly, and returns
// the shape of any objects its value carries.
func (s *modelGatewayProfileJSONShape) member(name string) (*modelGatewayProfileJSONShape, bool) {
	if s == nil {
		return nil, true
	}
	if _, supported := s.members[name]; !supported {
		return nil, false
	}
	return s.nested[name], true
}

// modelGatewayProfileJSONMembers reads the exact member spellings a configuration struct
// declares. Deriving them from the same tags encoding/json reads is what keeps the strict
// scanner and the decoded document from drifting apart. A field without an explicit name
// would still be decodable under a spelling this set does not contain, so it fails closed
// at package initialization instead of rejecting a legitimate configuration later.
func modelGatewayProfileJSONMembers(v any) map[string]struct{} {
	t := reflect.TypeOf(v)
	members := make(map[string]struct{}, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			panic("model gateway profile field " + field.Name + " has no explicit JSON member name")
		}
		members[name] = struct{}{}
	}
	return members
}

var (
	modelGatewayProfileEntryShape = &modelGatewayProfileJSONShape{
		members: modelGatewayProfileJSONMembers(modelGatewayProfileConfig{}),
	}
	modelGatewayProfileDocumentShape = &modelGatewayProfileJSONShape{
		members: modelGatewayProfileJSONMembers(modelGatewayProfilesDocument{}),
		nested:  map[string]*modelGatewayProfileJSONShape{"profiles": modelGatewayProfileEntryShape},
	}
)

// rejectUnsupportedJSONMembers walks the document before struct decoding and refuses any
// member name the document does not support exactly, together with any exactly repeated
// member name at any depth. Neither check alone is sufficient. encoding/json matches
// struct fields case-insensitively, so the decoder accepts "PROFILES", or a second "REF"
// beside "ref", as another spelling of a supported member and lets the last one win; a
// scanner that only compares member names to each other sees two unrelated members. Names
// are compared byte for byte and are never normalized into a supported spelling.
func rejectUnsupportedJSONMembers(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := consumeStrictJSONValue(dec, modelGatewayProfileDocumentShape); err != nil {
		return err
	}
	return requireJSONEOF(dec)
}

// consumeStrictJSONValue reads one value. shape describes an object at this position, or
// the elements of an array at this position, and is nil where the document declares no
// shape.
func consumeStrictJSONValue(dec *json.Decoder, shape *modelGatewayProfileJSONShape) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("decode profile JSON: %w", err)
	}
	delim, composite := tok.(json.Delim)
	if !composite {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return fmt.Errorf("decode profile JSON object: %w", err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("profile JSON object contains a non-string member name")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("profile JSON contains duplicate member %q", key)
			}
			nested, supported := shape.member(key)
			if !supported {
				return fmt.Errorf("profile JSON contains unsupported member %q; supported member names are matched exactly", key)
			}
			seen[key] = struct{}{}
			if err := consumeStrictJSONValue(dec, nested); err != nil {
				return err
			}
		}
		if end, err := dec.Token(); err != nil || end != json.Delim('}') {
			return errors.New("profile JSON object is not closed")
		}
	case '[':
		for dec.More() {
			if err := consumeStrictJSONValue(dec, shape); err != nil {
				return err
			}
		}
		if end, err := dec.Token(); err != nil || end != json.Delim(']') {
			return errors.New("profile JSON array is not closed")
		}
	default:
		return errors.New("profile JSON contains an unexpected delimiter")
	}
	return nil
}

func requireJSONEOF(dec *json.Decoder) error {
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("profile JSON contains trailing data")
		}
		return fmt.Errorf("profile JSON contains trailing data: %w", err)
	}
	return nil
}
