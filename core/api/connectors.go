// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/auth"
)

// Console connector ONBOARDING: the descriptor-driven authoring surface that
// lets an operator add/configure/test/remove a connector AND its credentials from
// the embedded console — sealed and persisted in the database — instead of editing
// the boot config file by hand. It composes the two surfaces the runtime already
// owns rather than introducing a third secret path:
//
//   - sealed secret store: a credential the operator types INLINE is sealed at
//     rest and the source row keeps only a `store:<name>` REFERENCE to it — never
//     the literal. This preserves invariant that the durable roster is
//     non-secret-bearing (model.SourceDef never carries a secret value).
//   - live source roster: the reference-only source definition is persisted and
//     applied to the running engine WITHOUT a restart (the existing PutSource path),
//     so a connector added here ingests immediately.
//
// Like SourceRoster and the secret store this is SUPERADMIN-gated (a deployment-wide
// ingestion change must not be editable by a single tenant's admin); writes and the
// connectivity test additionally require an AAL3 step-up (secret-bearing,
// privilege-shaped). A read (the catalog) carries no secret, so it skips AAL3.
//
// The interface is implemented in the composition root (cmd/olivares), the only
// place that may construct connectors (the license boundary keeps core/api free of
// the connector dependency trees); core/api owns only the transport and the auth
// gate, exactly as it does for SourceRoster.

// errConnectorOnboardingUnavailable is returned when the onboarding surface is not
// wired (an embedder/test that did not opt in). Mapped to 501 (honest seam), like
// the secret store and the source roster.
var errConnectorOnboardingUnavailable = errors.New("api: connector onboarding unavailable")

// ErrConnectorTestFailed is the SAFE result of a failed connectivity test: the
// candidate connector could not be opened with the supplied configuration. Its
// message is deliberately generic — a connector's own Open error runs against the
// RESOLVED config and can embed a live credential, so the detail is logged at Debug
// in the composition root and never surfaced. Mapped to 422 (the request was
// understood; the connection did not succeed).
var ErrConnectorTestFailed = errors.New("the connector could not be opened with the supplied configuration")

// ConnectorOnboarding is the live connector-onboarding surface the console drives.
// Every method is deny-closed and honest: an inline credential is sealed before the
// source row is written (the roster never holds a literal); a connector that cannot
// be built/opened is reported with a SAFE reason (never echoing a resolved secret);
// and a write that persists but whose live apply is rejected says so (the
// publish-then-activate honesty).
type ConnectorOnboarding interface {
	// ListConnectors returns the connector kinds this build can wire as live
	// observation sources, each annotated for descriptor-driven form rendering. It
	// never constructs nor contacts anything external. Only kinds the live roster
	// can actually wire are listed (identity/roster providers and knowledge document
	// sources are read once at boot — a restart domain — so they are not onboardable
	// here, and are honestly absent rather than faked).
	ListConnectors(ctx context.Context) ([]ConnectorInfo, error)
	// TestConnector builds a candidate connector from the input — resolving its
	// secret references and using a just-typed inline value directly — and Opens it
	// (then Closes it) WITHOUT persisting anything or wiring it live, so the operator
	// can confirm connectivity before saving. A failure is a SAFE reason, never the
	// raw connector error (which can embed a resolved credential).
	TestConnector(ctx context.Context, actor auth.Principal, in ConnectorOnboardInput) error
	// PutConnector seals each inline secret value into the secret store (under a
	// deterministic onboarding-owned name), rewrites the source config to carry only
	// the resulting `store:<name>` references, then persists the reference-only source
	// definition and applies it to the running engine (the PutSource path). A
	// blank secret field keeps the stored value; a value that is already a reference
	// is used verbatim (so an operator may point at a pre-existing or external secret).
	PutConnector(ctx context.Context, actor auth.Principal, in ConnectorOnboardInput) (SourceApplyResult, error)
	// DeleteConnector removes the source definition and stops it in the running
	// engine (the DeleteSource path), then deletes the onboarding-OWNED sealed
	// secrets it created (best-effort; an operator-supplied or external reference is
	// left untouched — only auto-owned `store:source/<name>/<field>` secrets are
	// removed).
	DeleteConnector(ctx context.Context, actor auth.Principal, name string) (SourceApplyResult, error)
}

// The ConnectorInfo.Hosting vocabulary. Closed on the wire (the console renders a
// badge per value) but read defensively: an unrecognized value must be shown as
// unknown, never silently as vendor_hosted.
const (
	// HostingSelfHosted is a system the OPERATOR runs (its declared endpoint default
	// is on the loopback host).
	HostingSelfHosted = "self_hosted"
	// HostingVendorHosted is a vendor's cloud API (its declared endpoint default is a
	// routable vendor URL).
	HostingVendorHosted = "vendor_hosted"
	// HostingUnknown is the honest third answer: nothing was declared to derive it
	// from. NOT a synonym for vendor_hosted.
	HostingUnknown = "unknown"
)

// ConnectorInfo describes one available connector kind so the console can render a
// configuration form from its declared fields. It is non-secret metadata.
type ConnectorInfo struct {
	// Kind is the operator-facing connector kind (the value stored on a source).
	Kind string `json:"kind"`
	// Title is the connector's short human label (from its Descriptor).
	Title string `json:"title,omitempty"`
	// Description is the connector's one-line summary (from its Descriptor).
	Description string `json:"description,omitempty"`
	// Transport is how the engine runs it: "in_process" (the host knows its fields)
	// or "plugin" (out-of-process — the host does NOT statically know its fields, so
	// the form falls back to free-form key/value and connectivity is validated on
	// save, not by the in-process test).
	Transport string `json:"transport"`
	// FieldsKnown reports whether Fields is the connector's real declared schema
	// (true for in-process kinds) or empty because the host cannot introspect an
	// out-of-process connector without launching it (false for plugin kinds).
	FieldsKnown bool `json:"fields_known"`
	// Hosting says WHERE the observed system runs, so an operator can tell a
	// self-hosted deployment from a vendor's cloud AT A GLANCE:
	//
	//	"self_hosted"   — the connector's OWN declared endpoint default points at the
	//	                  loopback host, i.e. it ships expecting you to run the thing.
	//	"vendor_hosted" — its declared endpoint default is a vendor URL on a routable
	//	                  host.
	//	"unknown"       — it declares no endpoint default (a local-config observer, or
	//	                  an out-of-process plugin the host cannot introspect).
	//
	// It is DERIVED from the connector descriptor, never a hand-maintained opinion
	// list: see hostingFromFields in the composition root. "unknown" is a real third
	// answer and never a synonym for vendor_hosted — claiming a vendor runs something
	// the operator runs themselves is the error this field exists to prevent.
	Hosting string `json:"hosting"`
	// Fields is the connector's declared configuration schema, projected from its
	// Descriptor's ConfigFields. Empty when FieldsKnown is false.
	Fields []ConnectorField `json:"fields,omitempty"`
}

// ConnectorField is the form-render projection of an sdk.ConfigField: enough for the
// console to render the right control and mask a secret. It never carries a value.
type ConnectorField struct {
	// Key is the configuration setting key.
	Key string `json:"key"`
	// Type is the declared value type: "string" | "int" | "bool" | "duration".
	Type string `json:"type"`
	// Required marks a setting that must be provided.
	Required bool `json:"required"`
	// Secret marks a credential field: the console masks it, the operator enters it
	// inline, and the engine seals it into the secret store on save (storing only a
	// reference). A blank Secret field on edit keeps the stored sealed value.
	Secret bool `json:"secret"`
	// Default is the value used when the setting is absent (non-secret fields only).
	Default string `json:"default,omitempty"`
	// Description is a short human explanation.
	Description string `json:"description,omitempty"`
}

// ConnectorOnboardInput is the console's create/update/test payload. NON-secret
// settings go in Config; the connector's SECRET-declared fields go in Secrets, where
// the value is interpreted as: "" = keep the stored sealed value (on edit);
// a `<scheme>:<locator>` reference = used verbatim (reuse an existing/external
// secret); any other value = a literal the engine seals into the store and
// replaces with a `store:<name>` reference before the source is persisted. The
// durable roster therefore never holds a literal credential.
type ConnectorOnboardInput struct {
	// Name is the operator-facing unique source handle.
	Name string `json:"name"`
	// Kind selects the connector (must be a kind ListConnectors offers).
	Kind string `json:"kind"`
	// Tenant is the business tenant its observations are stamped with.
	Tenant string `json:"tenant"`
	// PollSeconds re-runs a batch source every interval (0 = run once / streaming).
	PollSeconds int `json:"poll_seconds,omitempty"`
	// Enabled gates whether the source is wired into the running engine.
	Enabled bool `json:"enabled"`
	// Config is the connector's NON-secret settings.
	Config map[string]string `json:"config,omitempty"`
	// Secrets is the connector's SECRET-declared fields, keyed by field key, carrying
	// the inline value (or a reference, or blank-to-keep) — see the type doc.
	Secrets map[string]string `json:"secrets,omitempty"`
}
