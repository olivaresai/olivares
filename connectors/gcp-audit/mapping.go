// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gcpaudit

import (
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/identity"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// signalGCP marks a Cloud Resource Manager / IAM INVENTORY edge: a discovery of
// the org-management topology (organization owns folder/project, project owns
// service-account) via the read-only Resource Manager and IAM APIs. It is
// distinct from the Cloud Audit Logs activity feed (signalGCPAudit) so the
// consumer can separate inventory topology from observed control-plane access
// (the same split connectors/aws draws between signal "aws" and "cloudtrail").
const signalGCP = model.SignalSource("gcp")

// signalGCPAudit marks a Cloud Audit Logs ACTIVITY edge: a principal called an
// org/project management API. It is the same open SignalSource value the
// service-level gcpkms observer stamps — both are Cloud Audit Logs — and the
// disjoint resource namespace (gcp.api here vs gcp.kms.key/gcp.secret there)
// keeps the org management plane separable from the per-service data plane.
const signalGCPAudit = model.SignalSource("gcp_audit")

// Origin and resource kinds emitted by this connector. Inventory edges are
// containment/topology (org ⊳ folder ⊳ project ⊳ service-account); activity edges
// are control-plane (identity called an org/project-level API). The "gcp.api"
// namespace keeps Cloud Audit Logs management activity disjoint from the
// per-service data-plane resources own (gcs.object, gcp.kms.key, …).
const (
	originOrganization = "gcp.organization"
	originFolder       = "gcp.folder"
	originProject      = "gcp.project"

	resFolder         = "gcp.folder"
	resProject        = "gcp.project"
	resServiceAccount = "gcp.service_account"
	resGCPAPI         = "gcp.api"
)

// SubjectKind values used in health findings, one per enabled service.
const (
	subjectInventory = "gcp.inventory"
	subjectAudit     = "gcp.audit"
)

// inventoryEdge builds one Resource Manager / IAM topology edge. A containment
// edge is NOT an access: Mode is unknown and Confidence attributed (we observed
// it directly via the read-only API). Refs are scrubbed defensively; an org/
// project id or service-account email never carries a secret, but a uniform
// scrub keeps the minimal-data invariant identical across connectors.
func inventoryEdge(originKind, originRef, resKind, resRef string, at time.Time) model.EdgeObservation {
	return model.EdgeObservation{
		OriginKind:   originKind,
		OriginRef:    redact.Clean(originRef),
		ResourceKind: resKind,
		ResourceRef:  redact.Clean(resRef),
		Mode:         model.ModeUnknown,
		Source:       signalGCP,
		Confidence:   model.ConfidenceAttributed,
		ObservedAt:   at,
	}
}

// activityEdge builds one Cloud Audit Logs control-plane activity edge: a
// principal (its principalEmail) called an org/project management API. The
// resource is the serviceName:methodName pair (mirroring the AWS
// eventSource:eventName shape) and the tool is the serviceName. Mode and
// Confidence are decided by the caller from the audit log category and the
// attributability of the principal. OriginKind is always "identity" (the audit
// attributes a credential, never a resolved agent — identity↔agent is module VI).
func activityEdge(principal, resRef, toolRef string, mode model.AccessMode, conf model.Confidence, at time.Time) model.EdgeObservation {
	return model.EdgeObservation{
		OriginKind:   identity.OriginKind,
		OriginRef:    redact.Clean(principal),
		ResourceKind: resGCPAPI,
		ResourceRef:  redact.Clean(resRef),
		Mode:         mode,
		Source:       signalGCPAudit,
		Confidence:   conf,
		ToolRef:      redact.Clean(toolRef),
		ObservedAt:   at,
	}
}

// healthFinding reports an enabled service that could not be reached/listed. The
// error detail is hashed, never embedded, so a token or URL that slipped into an
// error string is not persisted (minimal-data). A gap is a signal, not silence:
// the connector emits this and continues with the other service.
func healthFinding(subjectKind, subjectRef, title string, err error, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        "health",
		Severity:    model.SeverityMedium,
		SubjectKind: subjectKind,
		SubjectRef:  subjectRef,
		Title:       title,
		DetailHash:  redact.Hash(err.Error()),
		OccurredAt:  at,
	}
}

// coverageFinding reports that a paginated read stopped at the max_pages bound
// while the API still had more data — an HONEST signal that the result is
// partial (raise max_pages for full coverage), never a silent truncation
// (docs/SECURITY-HARDENING.md; the "no silent caps" invariant). The title carries the
// non-sensitive, displayable detail; there is nothing sensitive to hash.
func coverageFinding(subjectKind, subjectRef, title string, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        "health",
		Severity:    model.SeverityLow,
		SubjectKind: subjectKind,
		SubjectRef:  subjectRef,
		Title:       title,
		OccurredAt:  at,
	}
}

// auditCategory is the Cloud Audit Logs log type, parsed from a LogEntry's
// logName suffix. The category determines how an entry is classified: Admin
// Activity entries are administrative writes by definition; Data Access entries
// are read or write per the method verb; System Event / Policy Denied are
// handled distinctly (a denied attempt is NOT an observed access).
type auditCategory int

const (
	catUnknown auditCategory = iota
	catAdminActivity
	catDataAccess
	catSystemEvent
	catPolicyDenied
)

// categoryFromLogName maps a Cloud Audit Logs logName to its category. The
// logName is e.g. "projects/p/logs/cloudaudit.googleapis.com%2Factivity"; the
// suffix after the URL-encoded slash is the audit log type. An unrecognized
// logName is catUnknown (the entry is then classified by method verb only).
//
// A logName that is not a Cloud Audit Log is catUnknown up front: the
// entries:list filter already restricts the pull to google.cloud.audit.AuditLog,
// but this guard keeps the category (which forces ModeWrite for Admin Activity)
// from ever firing on an unrelated log whose name merely ends in "/activity".
func categoryFromLogName(logName string) auditCategory {
	if !strings.Contains(logName, "cloudaudit.googleapis.com") {
		return catUnknown
	}
	switch {
	case strings.HasSuffix(logName, "%2Factivity"), strings.HasSuffix(logName, "/activity"):
		return catAdminActivity
	case strings.HasSuffix(logName, "%2Fdata_access"), strings.HasSuffix(logName, "/data_access"):
		return catDataAccess
	case strings.HasSuffix(logName, "%2Fsystem_event"), strings.HasSuffix(logName, "/system_event"):
		return catSystemEvent
	case strings.HasSuffix(logName, "%2Fpolicy"), strings.HasSuffix(logName, "/policy"):
		return catPolicyDenied
	default:
		return catUnknown
	}
}

// shortMethod returns the final dotted segment of a Cloud Audit Logs methodName,
// which carries the verb. GCP methodNames follow AIP-136 standard-method naming:
// e.g. "google.iam.admin.v1.CreateServiceAccount" → "CreateServiceAccount";
// "storage.objects.get" → "get"; "v1.compute.instances.insert" → "insert".
func shortMethod(method string) string {
	if i := strings.LastIndexByte(method, '.'); i >= 0 {
		return method[i+1:]
	}
	return method
}

// readVerbs and writeVerbs classify a Cloud Audit Logs method by its standard
// verb (AIP-136 + common custom verbs). They are matched case-insensitively
// against the PREFIX of the short method so both "get" and "GetServiceAccount"
// resolve. A verb in neither set yields ModeUnknown — the read/write nature is
// never guessed (ARCHITECTURE.md). The classification is honest, not exhaustive: an
// unrecognized method is an explicit "unknown", not a coerced read.
var readVerbs = []string{
	"get", "list", "aggregatedlist", "batchget", "search", "query", "read",
	"check", "test", "lookup", "export", "fetch", "watch", "describe",
}

var writeVerbs = []string{
	"create", "insert", "update", "patch", "delete", "set", "add", "remove",
	"write", "import", "start", "stop", "restart", "reset", "enable", "disable",
	"attach", "detach", "bind", "unbind", "destroy", "undelete", "move", "merge",
	"replace", "apply", "deactivate", "activate", "rotate", "expand", "resize",
}

// classifyMethod maps a Cloud Audit Logs entry to an access mode given its
// category. Admin Activity entries are administrative modifications by the log
// type's own definition (Google: "API calls or other actions that modify the
// configuration or metadata of resources") → ModeWrite, never guessed. Data
// Access (and Unknown-category) entries are classified by the method verb —
// read verbs → ModeRead, write verbs → ModeWrite, anything else → ModeUnknown.
func classifyMethod(cat auditCategory, method string) model.AccessMode {
	if cat == catAdminActivity {
		return model.ModeWrite
	}
	short := strings.ToLower(shortMethod(method))
	for _, v := range writeVerbs {
		if strings.HasPrefix(short, v) {
			return model.ModeWrite
		}
	}
	for _, v := range readVerbs {
		if strings.HasPrefix(short, v) {
			return model.ModeRead
		}
	}
	return model.ModeUnknown
}
