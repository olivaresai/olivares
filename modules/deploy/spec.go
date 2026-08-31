// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package deploy

import (
	"encoding/json"
	"strings"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// deploySpec is the TYPED desired state of a deployment. Like policy specs,
// it never round-trips operator JSON: it is decoded with DisallowUnknownFields,
// validated, and RE-SERIALIZED from the struct (canonical, with sorted map keys),
// so the stored spec and its hash are deterministic and a client cannot smuggle a
// field the struct does not declare. It carries image/command/resource refs and
// SECRET REFERENCES only — never a cleartext secret (docs/SECURITY-HARDENING.md,§4).
type deploySpec struct {
	// Image is the container image / artifact ref to deploy (non-sensitive).
	Image string `json:"image,omitempty"`
	// Command optionally overrides the entrypoint (non-sensitive; no secrets).
	Command string `json:"command,omitempty"`
	// Replicas is the desired replica count (0 = single instance / runtime default).
	Replicas int `json:"replicas,omitempty"`
	// Resources are non-sensitive compute requests (e.g. {"cpu":"500m","mem":"512Mi"}).
	Resources map[string]string `json:"resources,omitempty"`
	// EnvRefs are environment values supplied BY REFERENCE to a secret-store.
	EnvRefs []envRef `json:"env_refs,omitempty"`
	// Wirings are the declared PERMITTED connections agent→resource this deployment
	// needs. Applying the spec materializes them (the PERMITTED feed).
	Wirings []wiringSpec `json:"wirings,omitempty"`
	// Identity declares the per-agent NHI identity the deployment should run as
	// (the binding intent that closes the attribution bridge).
	Identity *identitySpec `json:"identity,omitempty"`
}

// envRef is an environment value provided by secret-store reference.
type envRef struct {
	Name      string `json:"name"`
	SecretRef string `json:"secret_ref"`
}

// wiringSpec is one declared connection from the deployment's subject agent to an
// enterprise resource. A deployment has a single subject (the agent/MCP being
// deployed); every wiring attributes to it, running as the deployment's declared
// identity.
type wiringSpec struct {
	// ResourceKind/ResourceRef name the enterprise resource reached (redacted
	// natural ref), e.g. "postgres.table"/"public.customers", "r2.bucket"/"x".
	ResourceKind string `json:"resource_kind"`
	ResourceRef  string `json:"resource_ref"`
	// Mode is the permitted access mode: "read" | "write" | "readwrite".
	Mode string `json:"mode"`
	// SecretRef is the credential the agent uses to reach the resource, BY
	// REFERENCE to a secret-store — never the secret itself.
	SecretRef string `json:"secret_ref,omitempty"`
}

// identitySpec declares the per-agent NHI identity intent.
type identitySpec struct {
	// IdentityRef is the directory/NHI reference the agent runs as. Empty + Mint
	// asks to mint a fresh per-agent NHI.
	IdentityRef string `json:"identity_ref,omitempty"`
	// Mint provisions a fresh per-agent identity rather than binding an existing one.
	Mint bool `json:"mint,omitempty"`
}

// validate checks the spec is well-formed and carries no inline credential,
// returning a non-empty message on the first problem. It is the structural
// guarantee (with the typed shape) that no cleartext secret is ever persisted.
func (s *deploySpec) validate() string {
	if len(s.Image) > maxSpecStrLen || len(s.Command) > maxSpecStrLen {
		return "image/command too long"
	}
	if containsInlineCredential(s.Image) || containsInlineCredential(s.Command) {
		return "image/command must not contain a credential"
	}
	if s.Replicas < 0 || s.Replicas > 10000 {
		return "replicas out of range"
	}
	for k, v := range s.Resources {
		if len(k) > maxNameLen || len(v) > maxNameLen {
			return "resource key/value too long"
		}
		if containsInlineCredential(k) || containsInlineCredential(v) {
			return "resources must not contain a credential"
		}
	}
	if len(s.EnvRefs) > maxEnvRefs {
		return "too many env_refs"
	}
	for _, e := range s.EnvRefs {
		if strings.TrimSpace(e.Name) == "" || len(e.Name) > maxNameLen {
			return "env_ref name is required and must be short"
		}
		if containsInlineCredential(e.Name) {
			return "env_ref name must not contain a credential"
		}
		if msg := validateSecretRef(e.SecretRef); msg != "" {
			return "env_ref " + e.Name + ": " + msg
		}
	}
	if len(s.Wirings) > maxWirings {
		return "too many wirings"
	}
	for i := range s.Wirings {
		if msg := s.Wirings[i].validate(); msg != "" {
			return msg
		}
	}
	if s.Identity != nil {
		if len(s.Identity.IdentityRef) > maxRefLen {
			return "identity.identity_ref too long"
		}
		if containsInlineCredential(s.Identity.IdentityRef) {
			return "identity.identity_ref must be a reference, not a credential"
		}
		if s.Identity.IdentityRef == "" && !s.Identity.Mint {
			return "identity requires either identity_ref or mint=true"
		}
	}
	return ""
}

// validate checks one wiring spec entry.
func (w *wiringSpec) validate() string {
	w.ResourceKind = strings.TrimSpace(w.ResourceKind)
	w.ResourceRef = strings.TrimSpace(w.ResourceRef)
	w.Mode = strings.ToLower(strings.TrimSpace(w.Mode))
	if w.ResourceKind == "" || w.ResourceRef == "" {
		return "wiring requires resource_kind and resource_ref"
	}
	if len(w.ResourceKind) > maxNameLen || len(w.ResourceRef) > maxRefLen {
		return "wiring fields too long"
	}
	switch sdkmodel.AccessMode(w.Mode) {
	case sdkmodel.ModeRead, sdkmodel.ModeWrite, sdkmodel.ModeReadWrite:
		// ok — a declared wiring states a definite access; "unknown" is rejected.
	default:
		return "wiring mode must be one of read, write, readwrite"
	}
	if containsInlineCredential(w.ResourceKind) || containsInlineCredential(w.ResourceRef) {
		return "wiring refs must not contain a credential"
	}
	if msg := validateSecretRef(w.SecretRef); msg != "" {
		return "wiring " + w.ResourceRef + ": " + msg
	}
	return ""
}

// canonical re-serializes the validated spec deterministically (Go's json sorts
// map keys), so the stored spec and its hash are stable regardless of how the
// operator ordered the input. It returns the canonical JSON and its hex hash.
func (s *deploySpec) canonical() (string, string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", "", err
	}
	return string(b), hashHex(string(b)), nil
}

// parseSpec decodes raw JSON into a typed spec, rejecting unknown fields, then
// validates it. It returns the spec and a non-empty client message on failure.
func parseSpec(raw json.RawMessage) (deploySpec, string) {
	var s deploySpec
	if len(raw) == 0 {
		return s, "spec is required"
	}
	if len(raw) > maxSpecBytes {
		return s, "spec too large"
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return s, "invalid spec: " + err.Error()
	}
	if msg := s.validate(); msg != "" {
		return s, msg
	}
	return s, ""
}
