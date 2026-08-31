// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package siemwire

import (
	"encoding/json"
	"fmt"
	"time"
)

// OCSF (Open Cybersecurity Schema Framework) export, pinned to schema v1.8.0 — the
// version that introduced the `ai_operation` profile (ai_model + message_context),
// verified against https://schema.ocsf.io/1.8.0 and the ocsf-schema repo at the
// v1.8.0 tag (re-verified on 2026-07-05). It emits Olivares
// findings and the tamper-evident ledger as OCSF 1.8.0 JSON, which a SOC that
// ACCEPTS 1.8.0 reads without a bespoke parser (OBS-02).
//
// AMAZON SECURITY LAKE IS NOT ONE OF THEM, and this comment used to claim it
// was. AWS states the limit in one sentence — "For custom sources, Security Lake
// supports OCSF version 1.3 and earlier" — written as Apache Parquet under a
// partitioned prefix
// (https://docs.aws.amazon.com/security-lake/latest/userguide/custom-sources.html,
// consulted 2026-08-02 by re-checked 2026-08-06). Nothing in this file
// emits 1.3 and nothing in this repository writes Parquet: OCSFVersion below is
// the only version it speaks, and there is no downgrade path. Reaching Security
// Lake needs a transformation of the operator's own.
//
// The public reference page has declared that gap honestly since 2026-07-24, in
// all seven locales (docs-site/src/content/docs/reference/siem-telemetry-egress.md,
// the "AWS Security Lake accepts OCSF <= 1.3" caution). The code said the opposite
// in FIVE places, which is the harder failure — the reader who trusts a comment is
// the one holding the source. Found all five on 2026-08-02 (branch
// feature/S526b-security-lake, still unmerged) re-found and corrected them on
// main and added scripts/check-ocsf-claims.sh, which fails the build if any of the
// five drops the limit again.
//
// Modeling decisions, each verified against the official 1.8.0 schema (NOT assumed re-fetched the three generated class exports on 2026-07-05 and found them
// byte-identical to the vendored schemas):
//   - Profile registration (verified against the live schema AND the
//     ocsf-schema v1.8.0 tag): `ai_operation` is registered on EXACTLY three
//     classes — process_activity (1007), api_activity (6003) and
//     datastore_activity (6005). v1.8.0 added NO new event classes (the
//     CHANGELOG "Added > Event Classes" section is empty); AI support is the
//     profile applied to existing classes. The encoder supports all three;
//     the findings feed and the ledger stay on API Activity 6003 — their events
//     are agent/tool/API operations (CRUD on an API surface), not OS process
//     launches — and 6003 carries the profile in 1.8.0, so the mapping validates.
//   - `cloud` is NOT emitted: it belongs to the `cloud` profile, which these
//     events do not apply; under the generated 1.8.0 class schema
//     (additionalProperties:false) an un-profiled `cloud` field does not
//     validate. (fix; it was previously always emitted.)
//   - metadata.profiles declares ["ai_operation"] exactly when a profile
//     attribute (ai_model / message_context) is present, so a consumer knows
//     which optional attributes to expect.
//   - ai_model REQUIRES ai_provider AND one of name/uid (1.8.0 objects/ai_model:
//     ai_provider "required"; _entity at_least_one[name,uid]). The field is
//     `ai_provider`, NOT `vendor_name` (that field does not exist on ai_model).
//     An ai_model missing either is parked under `unmapped` rather than emitted
//     invalid — nothing is silently dropped.
//   - message_context REQUIRES at_least_one of application/service (1.8.0
//     objects/message_context constraint). ai_role_id = 4 is the OCSF-native
//     "Agent" role; token counts (prompt/completion/total) live INSIDE
//     message_context, not at event top level. A message_context that can name
//     neither an application nor a service is parked under `unmapped`.
//   - process_activity (1007) is a System Activity (category 1) class: it
//     REQUIRES `device` (one of hostname/ip/name/uid… plus type_id) and
//     `process` (one of cpid/pid/uid); it has NO `api`/`src_endpoint`/`cloud`
//     properties. The encoder fails closed (an error, never an invalid event)
//     when a caller selects it without those objects.
//   - src_endpoint is REQUIRED on api_activity and datastore_activity and must
//     satisfy at_least_one of its identifying fields; svc_name falls back to
//     the actor app / device product so the emitted endpoint always validates.
//   - actor.type_id = 99 "AI Agent" is an OWASP AOS extension, NOT a native OCSF
//     field, so it is carried under the OCSF `unmapped` container (the
//     AOS-prescribed placement) to keep the event OCSF-compliant.
//   - The OWASP AOS trace extension is a v0.1 public-preview Working Draft, not
//     a stable/GA standard. Its session, step and guardian-decision fields are
//     nested under `unmapped.aos`; they are NOT first-class OCSF attributes and
//     OCSF validation covers only their placement in the `unmapped` container.
//
// OCSF 1.8.0 is GA (2026-03-16), but the ITU ratification track is NOT a hard
// dependency; this encoder tracks the schema version constant only.
const OCSFVersion = "1.8.0"

// OCSFClass selects the event class the encoder emits. Only the three classes
// that REGISTER the ai_operation profile in 1.8.0 are supported (verified);
// the zero value is API Activity, the class both internal feeds use.
type OCSFClass int

const (
	// OCSFClassAPIActivity is API Activity (class_uid 6003, category 6
	// Application Activity) — agent/tool/API operations (the default).
	OCSFClassAPIActivity OCSFClass = iota
	// OCSFClassProcessActivity is Process Activity (class_uid 1007, category 1
	// System Activity) — for process-shaped AI events (launch/terminate/open/
	// inject). Requires Device and Process.
	OCSFClassProcessActivity
	// OCSFClassDatastoreActivity is Datastore Activity (class_uid 6005, category
	// 6 Application Activity) — for AI datastore/retrieval operations.
	OCSFClassDatastoreActivity
)

// ocsfClassInfo is the verified per-class base data (1.8.0).
type ocsfClassInfo struct {
	uid          int
	name         string
	categoryUID  int
	categoryName string
}

// classInfo resolves the class constants; an out-of-range value falls back to
// API Activity (the safe default; the value is internal, not operator input).
func (c OCSFClass) classInfo() ocsfClassInfo {
	switch c {
	case OCSFClassProcessActivity:
		return ocsfClassInfo{uid: 1007, name: "Process Activity", categoryUID: 1, categoryName: "System Activity"}
	case OCSFClassDatastoreActivity:
		return ocsfClassInfo{uid: 6005, name: "Datastore Activity", categoryUID: 6, categoryName: "Application Activity"}
	default:
		return ocsfClassInfo{uid: 6003, name: "API Activity", categoryUID: 6, categoryName: "Application Activity"}
	}
}

// activityValid reports whether id is in the class's verified 1.8.0
// activity_id enum (the enums are PER CLASS: 6003 tops at 4, 1007 at 5,
// 6005 at 10; 0 Unknown and 99 Other are common to all; re-verified
// 2026-07-05).
func (c OCSFClass) activityValid(id int) bool {
	if id == 0 || id == 99 {
		return true
	}
	switch c {
	case OCSFClassProcessActivity:
		return id >= 1 && id <= 5
	case OCSFClassDatastoreActivity:
		return id >= 1 && id <= 10
	default:
		return id >= 1 && id <= 4
	}
}

// AI message-context roles (OCSF-native ai_role_id enum, 1.8.0; verified full
// enum: 0 Unknown, 1 User, 2 Assistant, 3 Tool, 4 Agent, 5 Orchestrator,
// 6 Retriever, 99 Other; re-verified 2026-07-05).
const (
	OCSFRoleAgent = 4 // message_context.ai_role_id = 4 "Agent"
)

// OCSFAIModel is the OCSF `ai_model` object (ai_operation profile). The 1.8.0
// schema REQUIRES AIProvider and one of Name/UID; the encoder parks an
// incomplete ai_model under `unmapped` instead of emitting an invalid object.
type OCSFAIModel struct {
	Name       string `json:"name,omitempty"`
	AIProvider string `json:"ai_provider,omitempty"`
	UID        string `json:"uid,omitempty"`
	Version    string `json:"version,omitempty"`
}

// valid reports whether the object satisfies the verified 1.8.0 constraints
// (ai_provider required; at_least_one of name/uid).
func (m *OCSFAIModel) valid() bool {
	return m != nil && m.AIProvider != "" && (m.Name != "" || m.UID != "")
}

// OCSFApplication is the OCSF `application` object as used inside
// message_context (the initiating client application/agent framework). The
// schema requires at_least_one of name/uid.
type OCSFApplication struct {
	Name    string `json:"name,omitempty"`
	UID     string `json:"uid,omitempty"`
	Version string `json:"version,omitempty"`
}

// OCSFService is the OCSF `service` object as used inside message_context (the
// AI service/endpoint handling the request). The schema requires at_least_one
// of name/uid.
type OCSFService struct {
	Name    string `json:"name,omitempty"`
	UID     string `json:"uid,omitempty"`
	Version string `json:"version,omitempty"`
}

// OCSFMessageContext is the OCSF `message_context` object (ai_operation
// profile), carrying the AI role, the conversation/session reference and the
// token accounting. The 1.8.0 schema constrains it to at_least_one of
// Application/Service; the encoder parks a context that names neither under
// `unmapped` instead of emitting an invalid object.
type OCSFMessageContext struct {
	UID              string           `json:"uid,omitempty"`
	AIRole           string           `json:"ai_role,omitempty"`
	AIRoleID         int              `json:"ai_role_id,omitempty"`
	Application      *OCSFApplication `json:"application,omitempty"`
	Service          *OCSFService     `json:"service,omitempty"`
	PromptTokens     *int64           `json:"prompt_tokens,omitempty"`
	CompletionTokens *int64           `json:"completion_tokens,omitempty"`
	TotalTokens      *int64           `json:"total_tokens,omitempty"`
}

// valid reports whether the object satisfies the verified 1.8.0 constraint
// (at_least_one of application/service, each itself needing a name or uid).
func (m *OCSFMessageContext) valid() bool {
	if m == nil {
		return false
	}
	app := m.Application != nil && (m.Application.Name != "" || m.Application.UID != "")
	svc := m.Service != nil && (m.Service.Name != "" || m.Service.UID != "")
	return app || svc
}

// OCSFProcess is the minimal OCSF `process` object required by
// process_activity. The 1.8.0 schema requires at_least_one of CPID/PID/UID
// (name alone does NOT satisfy the constraint — verified).
type OCSFProcess struct {
	Name    string `json:"name,omitempty"`
	PID     *int64 `json:"pid,omitempty"`
	UID     string `json:"uid,omitempty"`
	CmdLine string `json:"cmd_line,omitempty"`
}

// OCSFDevice is the minimal OCSF `device` object required by process_activity.
// The 1.8.0 schema requires type_id plus at_least_one of
// hostname/ip/name/uid/instance_uid/interface_*.
type OCSFDevice struct {
	TypeID   int    `json:"type_id"`
	Type     string `json:"type,omitempty"`
	Name     string `json:"name,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	UID      string `json:"uid,omitempty"`
}

// OCSFDatabase is the minimal OCSF `database` object; datastore_activity
// REQUIRES at_least_one of database/databucket/table at the event root
// (verified 1.8.0 constraint — this encoder supports the database arm). The
// schema requires type_id plus one of name/uid; 1.8.0 added the AI types
// Vector (7) and Knowledge Graph (8).
type OCSFDatabase struct {
	TypeID int    `json:"type_id"`
	Type   string `json:"type,omitempty"`
	Name   string `json:"name,omitempty"`
	UID    string `json:"uid,omitempty"`
}

// AOSDecisionVerdict is the OWASP AOS v0.1 public-preview guardian decision
// enum. AOS is a Working Draft; these values are not presented as stable/GA.
type AOSDecisionVerdict string

const (
	AOSDecisionAllow  AOSDecisionVerdict = "allow"
	AOSDecisionDeny   AOSDecisionVerdict = "deny"
	AOSDecisionModify AOSDecisionVerdict = "modify"
)

// AOSAgent identifies the governed agent in the AOS trace context. Callers
// must use opaque identifiers and metadata, never a human identity or payload.
type AOSAgent struct {
	ID      string
	Name    string
	Version string
}

// AOSStep identifies one action in the AOS Session > Turn > Step hierarchy.
type AOSStep struct {
	ID            string
	Type          string
	TurnID        string
	OperationType string
}

// AOSRequestReference is the privacy-minimized representation of a modified
// request. It deliberately has no content, argument or credential fields: only
// an opaque reference and/or a SHA-256 fingerprint may leave the control plane.
type AOSRequestReference struct {
	Ref    string
	SHA256 string
}

// AOSDecision is an OWASP AOS guardian-agent decision. Reasoning, ReasonCode
// and Message must be policy metadata, not copied prompts, payloads or secrets.
// ModifiedRequest is emitted only when Decision is modify and contains only the
// privacy-minimized reference/fingerprint shape above.
type AOSDecision struct {
	Decision        AOSDecisionVerdict
	Reasoning       string
	ReasonCode      string
	Message         string
	ModifiedRequest *AOSRequestReference
}

// AOSTrace is the OWASP AOS v0.1 public-preview session/decision subset carried
// by this encoder. A non-nil value selects the AOS OCSF mapping: API Activity
// class 6003, category 6, activity_id 1 and type_uid 600301. All AOS richness is
// parked below `unmapped.aos` so it cannot introduce invalid OCSF attributes.
type AOSTrace struct {
	Agent     AOSAgent
	SessionID string
	Step      AOSStep
	Decision  *AOSDecision
}

func (a *AOSTrace) validate() error {
	if a.Agent.ID == "" || a.Agent.Name == "" || a.Agent.Version == "" {
		return fmt.Errorf("siemwire: AOS trace requires agent id, name and version (v0.1 public preview)")
	}
	if a.SessionID == "" {
		return fmt.Errorf("siemwire: AOS trace requires a session id (v0.1 public preview)")
	}
	if a.Step.ID == "" || a.Step.Type == "" || a.Step.TurnID == "" || a.Step.OperationType == "" {
		return fmt.Errorf("siemwire: AOS trace requires step id, type, turn_id and operation type (v0.1 public preview)")
	}
	if a.Decision == nil {
		return nil
	}
	switch a.Decision.Decision {
	case AOSDecisionAllow, AOSDecisionDeny, AOSDecisionModify:
	default:
		return fmt.Errorf("siemwire: AOS decision must be allow, deny or modify (v0.1 public preview)")
	}
	if a.Decision.Reasoning == "" || a.Decision.ReasonCode == "" || a.Decision.Message == "" {
		return fmt.Errorf("siemwire: AOS decision requires reasoning, reasonCode and message (v0.1 public preview)")
	}
	return nil
}

func (a *AOSTrace) unmappedObject() map[string]any {
	obj := map[string]any{
		"context": map[string]any{
			"agent": map[string]any{
				"id":      a.Agent.ID,
				"name":    a.Agent.Name,
				"version": a.Agent.Version,
			},
			"session": map[string]any{"id": a.SessionID},
		},
		"step": map[string]any{
			"id":      a.Step.ID,
			"type":    a.Step.Type,
			"turn_id": a.Step.TurnID,
			"operation": map[string]any{
				"type": a.Step.OperationType,
			},
		},
	}
	if a.Decision == nil {
		return obj
	}
	decision := map[string]any{
		"decision":   string(a.Decision.Decision),
		"reasoning":  a.Decision.Reasoning,
		"reasonCode": a.Decision.ReasonCode,
		"message":    a.Decision.Message,
	}
	if a.Decision.Decision == AOSDecisionModify && a.Decision.ModifiedRequest != nil {
		modified := make(map[string]any, 2)
		setNonEmpty(modified, "ref", a.Decision.ModifiedRequest.Ref)
		setNonEmpty(modified, "sha256", a.Decision.ModifiedRequest.SHA256)
		if len(modified) > 0 {
			decision["modifiedRequest"] = modified
		}
	}
	obj["decision"] = decision
	return obj
}

// OCSFInput is the focused, caller-neutral input to OCSF. Each caller (the
// findings encoder and the ledger export) populates it from its own data; the
// base-field computation (type_uid, class/category names, metadata, severity
// name) and the JSON shape live here ONCE, so the two feeds never drift.
type OCSFInput struct {
	// Class selects the event class; the zero value is API Activity (6003).
	Class OCSFClass
	// ActivityID is the class activity_id — the enum is PER CLASS (verified
	// 1.8.0): API Activity 0 Unknown, 1 Create, 2 Read, 3 Update, 4 Delete,
	// 99 Other; Process Activity 1 Launch, 2 Terminate, 3 Open, 4 Inject,
	// 5 Set User ID; Datastore Activity 1..10. An id outside the selected
	// class's enum is clamped to 99 "Other" with the original parked under
	// `unmapped` (never emitted invalid). ActivityName is the label.
	ActivityID   int
	ActivityName string
	// SeverityID is the OCSF severity_id (1 Informational..5 Critical, 6 Fatal,
	// 99 Other, 0 Unknown). StatusID is 1 Success / 2 Failure / 0 omit.
	SeverityID int
	StatusID   int
	Time       time.Time
	Message    string
	// Device backs metadata.product.
	Device Device
	// Operation is api.operation (the agent operation / tool / action verb).
	// The `api` object exists only on API Activity (6003); for the other
	// classes a non-empty Operation rides under `unmapped` so it is never lost.
	Operation string
	// ActorAppName backs actor.app_name; SrcName backs src_endpoint.svc_name
	// (e.g. the agent or session reference). src_endpoint is REQUIRED on 6003/
	// 6005, so svc_name falls back to the actor app / device product.
	ActorAppName string
	SrcName      string
	// AIModel / MessageContext are the ai_operation profile objects (nil to
	// omit). One that violates its verified schema constraint is parked under
	// `unmapped` (never emitted invalid, never silently dropped).
	AIModel        *OCSFAIModel
	MessageContext *OCSFMessageContext
	// Process / HostDevice are REQUIRED by process_activity (1007); Database is
	// REQUIRED by datastore_activity (6005, at_least_one of database/databucket/
	// table — this encoder supports the database arm). OCSF fails closed with an
	// error if a class is selected without its required objects.
	Process    *OCSFProcess
	HostDevice *OCSFDevice
	Database   *OCSFDatabase
	// AOS carries the OWASP AOS v0.1 public-preview session/decision trace
	// subset. It is encoded only under `unmapped.aos` and forces the verified
	// AOS OCSF base mapping (API Activity 6003 / activity 1 / type 600301).
	AOS *AOSTrace
	// Unmapped carries non-OCSF fields (including the OWASP AOS actor.type_id=99
	// "AI Agent" marker, added by OCSF itself) so nothing is silently dropped.
	Unmapped map[string]any
}

type ocsfProduct struct {
	Name       string `json:"name"`
	VendorName string `json:"vendor_name"`
	Version    string `json:"version,omitempty"`
}

type ocsfMetadata struct {
	Version  string      `json:"version"`
	Product  ocsfProduct `json:"product"`
	Profiles []string    `json:"profiles,omitempty"`
}

type ocsfActor struct {
	AppName string `json:"app_name,omitempty"`
}

type ocsfService struct {
	Name string `json:"name,omitempty"`
}

// ocsfAPI is the `api` object (api.operation is required by the schema). The
// service is a pointer: an empty `service:{}` violates its at_least_one
// (name|uid) constraint, so it is omitted entirely when there is no name.
type ocsfAPI struct {
	Operation string       `json:"operation"`
	Service   *ocsfService `json:"service,omitempty"`
}

type ocsfEndpoint struct {
	SvcName string `json:"svc_name,omitempty"`
}

type ocsfEvent struct {
	ActivityID     int                 `json:"activity_id"`
	ActivityName   string              `json:"activity_name,omitempty"`
	CategoryUID    int                 `json:"category_uid"`
	CategoryName   string              `json:"category_name"`
	ClassUID       int                 `json:"class_uid"`
	ClassName      string              `json:"class_name"`
	TypeUID        int                 `json:"type_uid"`
	Time           int64               `json:"time"`
	SeverityID     int                 `json:"severity_id"`
	Severity       string              `json:"severity,omitempty"`
	StatusID       int                 `json:"status_id,omitempty"`
	Message        string              `json:"message,omitempty"`
	Metadata       ocsfMetadata        `json:"metadata"`
	Actor          ocsfActor           `json:"actor"`
	API            *ocsfAPI            `json:"api,omitempty"`
	SrcEndpoint    *ocsfEndpoint       `json:"src_endpoint,omitempty"`
	Device         *OCSFDevice         `json:"device,omitempty"`
	Process        *OCSFProcess        `json:"process,omitempty"`
	Database       *OCSFDatabase       `json:"database,omitempty"`
	AIModel        *OCSFAIModel        `json:"ai_model,omitempty"`
	MessageContext *OCSFMessageContext `json:"message_context,omitempty"`
	Unmapped       map[string]any      `json:"unmapped,omitempty"`
}

// OCSF marshals an OCSF v1.8.0 event (default: API Activity 6003) with the
// ai_operation profile. It always attaches the OWASP AOS `actor.type_id=99
// "AI Agent"` marker under `unmapped` (merging, never overwriting, a caller's
// unmapped entries), parks a profile object that violates its verified schema
// constraint under `unmapped`, and fails closed (an error) when
// process_activity is selected without its required process/device objects.
func OCSF(in OCSFInput) ([]byte, error) {
	if in.AOS != nil {
		if in.Class != OCSFClassAPIActivity {
			return nil, fmt.Errorf("siemwire: AOS trace requires OCSF API Activity class 6003 (v0.1 public preview)")
		}
		if in.ActivityID != 0 && in.ActivityID != 1 {
			return nil, fmt.Errorf("siemwire: AOS trace requires activity_id 1 (v0.1 public preview)")
		}
		if err := in.AOS.validate(); err != nil {
			return nil, err
		}
	}

	// The device defaults PER FIELD (fix): actor.app_name and
	// src_endpoint.svc_name bottom out at dev.Product, and both objects are
	// REQUIRED with at_least_one constraints on 6003/6005 — a half-filled
	// Device (vendor only) must not cascade into an empty actor{}/src_endpoint{}
	// that fails the official schema.
	dev := in.Device
	if dev.Vendor == "" {
		dev.Vendor = DefaultDevice().Vendor
	}
	if dev.Product == "" {
		dev.Product = DefaultDevice().Product
	}
	actorApp := in.ActorAppName
	if actorApp == "" {
		actorApp = dev.Product
	}
	op := in.Operation
	if op == "" {
		op = in.ActivityName
	}
	if op == "" && in.AOS != nil {
		op = in.AOS.Step.OperationType
	}

	// OWASP AOS agent marker lives under unmapped (NOT a native actor field).
	unmapped := map[string]any{
		"actor.type_id": 99,
		"actor.type":    "AI Agent",
	}
	for k, v := range in.Unmapped {
		unmapped[k] = v
	}
	if in.AOS != nil {
		aos := in.AOS.unmappedObject()
		if existing, ok := unmapped["aos"]; ok {
			extension, ok := existing.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("siemwire: unmapped.aos must be an object when AOS trace fields are set")
			}
			// Preserve every caller-supplied AOS extension while adding the
			// canonical session/decision shape; caller values win on collision,
			// matching the existing Unmapped merge semantics above.
			mergeAOSObject(aos, extension)
		}
		unmapped["aos"] = aos
	}

	// Profile objects: emit only what validates; park the rest under unmapped
	// so the event is schema-valid AND nothing is silently dropped.
	aiModel := in.AIModel
	if aiModel != nil && !aiModel.valid() {
		setNonEmpty(unmapped, "ai_model.name", aiModel.Name)
		setNonEmpty(unmapped, "ai_model.uid", aiModel.UID)
		setNonEmpty(unmapped, "ai_model.version", aiModel.Version)
		setNonEmpty(unmapped, "ai_model.ai_provider", aiModel.AIProvider)
		aiModel = nil
	}
	msgCtx := in.MessageContext
	if msgCtx != nil && !msgCtx.valid() {
		setNonEmpty(unmapped, "message_context.uid", msgCtx.UID)
		setNonEmpty(unmapped, "message_context.ai_role", msgCtx.AIRole)
		if msgCtx.AIRoleID != 0 {
			unmapped["message_context.ai_role_id"] = msgCtx.AIRoleID
		}
		if msgCtx.PromptTokens != nil {
			unmapped["message_context.prompt_tokens"] = *msgCtx.PromptTokens
		}
		if msgCtx.CompletionTokens != nil {
			unmapped["message_context.completion_tokens"] = *msgCtx.CompletionTokens
		}
		if msgCtx.TotalTokens != nil {
			unmapped["message_context.total_tokens"] = *msgCtx.TotalTokens
		}
		msgCtx = nil
	} else if msgCtx != nil {
		// A VALID context may still carry an INVALID sub-object (a Version-only
		// application/service violates its own name|uid constraint): scrub it
		// from a copy (never mutating the caller's struct), parking its content.
		mc := *msgCtx
		if mc.Application != nil && mc.Application.Name == "" && mc.Application.UID == "" {
			setNonEmpty(unmapped, "message_context.application.version", mc.Application.Version)
			mc.Application = nil
		}
		if mc.Service != nil && mc.Service.Name == "" && mc.Service.UID == "" {
			setNonEmpty(unmapped, "message_context.service.version", mc.Service.Version)
			mc.Service = nil
		}
		msgCtx = &mc
	}

	// Enum clamps: an activity_id outside the SELECTED class's enum or a
	// severity_id outside [0..6,99] would fail the official schema; clamp to 99
	// "Other" and park the original under unmapped (never emitted invalid,
	// never silently dropped).
	actID, actName := in.ActivityID, in.ActivityName
	if in.AOS != nil {
		actID = 1
		if actName == "" {
			if in.AOS.Decision != nil {
				actName = "Agent Decision"
			} else {
				actName = "Agent Session"
			}
		}
	}
	if !in.Class.activityValid(actID) {
		unmapped["activity_id.original"] = actID
		setNonEmpty(unmapped, "activity_name.original", actName)
		actID, actName = 99, "Other"
	}
	sevID := in.SeverityID
	if (sevID < 0 || sevID > 6) && sevID != 99 {
		unmapped["severity_id.original"] = sevID
		sevID = 99
	}
	statID := in.StatusID
	if (statID < 0 || statID > 2) && statID != 99 {
		unmapped["status_id.original"] = statID
		statID = 99
	}

	ci := in.Class.classInfo()
	ev := ocsfEvent{
		ActivityID:   actID,
		ActivityName: actName,
		CategoryUID:  ci.categoryUID,
		CategoryName: ci.categoryName,
		ClassUID:     ci.uid,
		ClassName:    ci.name,
		TypeUID:      ci.uid*100 + actID,
		SeverityID:   sevID,
		Severity:     ocsfSeverityName(sevID),
		StatusID:     statID,
		Message:      in.Message,
		Metadata: ocsfMetadata{
			Version: OCSFVersion,
			Product: ocsfProduct{Name: dev.Product, VendorName: dev.Vendor, Version: dev.Version},
		},
		Actor:          ocsfActor{AppName: actorApp},
		AIModel:        aiModel,
		MessageContext: msgCtx,
		Unmapped:       unmapped,
	}
	if !in.Time.IsZero() {
		ev.Time = in.Time.UTC().UnixMilli()
	}
	// metadata.profiles declares the applied profile exactly when one of its
	// attributes is present, so a consumer knows what to expect.
	if aiModel != nil || msgCtx != nil {
		ev.Metadata.Profiles = []string{"ai_operation"}
	}

	// Class-specific shape (verified per-class property sets, 1.8.0).
	switch in.Class {
	case OCSFClassProcessActivity:
		// process + device are REQUIRED; api/src_endpoint are not properties.
		if in.Process == nil || (in.Process.PID == nil && in.Process.UID == "") {
			return nil, fmt.Errorf("siemwire: OCSF process_activity requires a process with pid or uid (1.8.0)")
		}
		if in.HostDevice == nil || in.HostDevice.TypeID == 0 ||
			(in.HostDevice.Name == "" && in.HostDevice.Hostname == "" && in.HostDevice.UID == "") {
			return nil, fmt.Errorf("siemwire: OCSF process_activity requires a device with type_id and a name/hostname/uid (1.8.0)")
		}
		ev.Process = in.Process
		ev.Device = in.HostDevice
		setNonEmpty(unmapped, "api.operation", in.Operation)
		setNonEmpty(unmapped, "src_endpoint.svc_name", in.SrcName)
	case OCSFClassDatastoreActivity:
		// src_endpoint required; the event root requires at_least_one of
		// database/databucket/table (this encoder supports the database arm);
		// the api object is not a property of this class.
		if in.Database == nil || in.Database.TypeID == 0 || (in.Database.Name == "" && in.Database.UID == "") {
			return nil, fmt.Errorf("siemwire: OCSF datastore_activity requires a database with type_id and a name/uid (1.8.0)")
		}
		ev.Database = in.Database
		ev.SrcEndpoint = &ocsfEndpoint{SvcName: firstNonEmptyWire(in.SrcName, actorApp, dev.Product)}
		setNonEmpty(unmapped, "api.operation", in.Operation)
	default: // API Activity
		ev.API = &ocsfAPI{Operation: op}
		if in.SrcName != "" {
			ev.API.Service = &ocsfService{Name: in.SrcName}
		}
		ev.SrcEndpoint = &ocsfEndpoint{SvcName: firstNonEmptyWire(in.SrcName, actorApp, dev.Product)}
	}

	return json.Marshal(ev)
}

// mergeAOSObject recursively merges src into dst. It is used only for a
// caller's existing unmapped.aos object; caller values take precedence, exactly
// like OCSFInput.Unmapped values do at the event's top-level unmapped container.
func mergeAOSObject(dst, src map[string]any) {
	for k, v := range src {
		srcMap, srcIsMap := v.(map[string]any)
		dstMap, dstIsMap := dst[k].(map[string]any)
		if srcIsMap && dstIsMap {
			mergeAOSObject(dstMap, srcMap)
			continue
		}
		dst[k] = v
	}
}

// setNonEmpty records k=v in m when v is non-empty.
func setNonEmpty(m map[string]any, k, v string) {
	if v != "" {
		m[k] = v
	}
}

// firstNonEmptyWire returns the first non-empty argument, or "".
func firstNonEmptyWire(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ocsfSeverityName maps the OCSF severity_id to its label (1.8.0 enum).
func ocsfSeverityName(id int) string {
	switch id {
	case 1:
		return "Informational"
	case 2:
		return "Low"
	case 3:
		return "Medium"
	case 4:
		return "High"
	case 5:
		return "Critical"
	case 6:
		return "Fatal"
	case 99:
		return "Other"
	default:
		return "Unknown"
	}
}
