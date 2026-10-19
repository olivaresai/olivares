// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package deploy

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
