// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package flux

import "strings"

// Flux spreads its API across THREE distinct groups, one per controller. This
// connector accepts all three rather than hardcoding one group/version: a GitOps
// estate exports objects of all three kinds together, and the posture (Ready /
// drift) lives in byte-identical status paths across them. Each prefix is matched
// loosely (a HasPrefix on the group) so a future served version of the same group
// (e.g. a v1beta-something the cluster still serves) is still recognized. Verified
// against fluxcd.io: source-controller GitRepository, kustomize-controller
// Kustomization, helm-controller HelmRelease.
const (
	groupSource    = "source.toolkit.fluxcd.io/"    // GitRepository (current: /v1)
	groupKustomize = "kustomize.toolkit.fluxcd.io/" // Kustomization (current: /v1)
	groupHelm      = "helm.toolkit.fluxcd.io/"      // HelmRelease (current: /v2)
)

// The three Flux kinds this connector handles. Any other kind in a mixed manifest
// (a stray ConfigMap, a HelmRepository source, a Bucket) is skipped.
const (
	kindGitRepository = "GitRepository"
	kindKustomization = "Kustomization"
	kindHelmRelease   = "HelmRelease"
)

// The subjectKind value per CRD, used as FindingReport.SubjectKind so a finding says
// exactly which Flux object kind it is about.
const (
	subjectGitRepository = "flux.gitrepository"
	subjectKustomization = "flux.kustomization"
	subjectHelmRelease   = "flux.helmrelease"
)

// defaultNamespace is the namespace assumed when a Flux object omits
// metadata.namespace. Flux objects conventionally live in the flux-system
// namespace; the value is recorded into the subject ref, never inferred into a
// posture guess.
const defaultNamespace = "flux-system"

// readyConditionType is the status condition every Flux object publishes to report
// whether it is reconciled. Its .status is "True" / "False" / "Unknown" — the truth
// of reconciliation, taken verbatim. Verified against the fluxcd.io API references
// (each kind's status.conditions[] is a []metav1.Condition with a Ready entry).
const readyConditionType = "Ready"

// The metav1.Condition.Status string values. "True" means reconciled; "False" means
// reconciliation is failing; anything else (including absent) is Unknown.
const (
	condTrue  = "True"
	condFalse = "False"
)

// document is the minimal envelope of a Flux CRD manifest the connector reads.
// apiVersion+kind dispatch the decode; metadata carries name/namespace AND
// generation (compared against status.observedGeneration to detect that the
// controller has not yet observed the latest spec); status carries ONLY the
// reconciliation posture. The connector reads NOTHING from spec (a HelmRelease
// spec.values can embed secrets; a GitRepository spec.url/secretRef name an endpoint
// and a secret — payload-adjacent, docs/SECURITY-HARDENING.md): only the observed posture.
type document struct {
	APIVersion string     `yaml:"apiVersion"`
	Kind       string     `yaml:"kind"`
	Metadata   objectMeta `yaml:"metadata"`
	Status     fluxStatus `yaml:"status"`
}

type objectMeta struct {
	Name       string `yaml:"name"`
	Namespace  string `yaml:"namespace"`
	Generation int64  `yaml:"generation"`
}

// fluxStatus is the subset of a Flux object status the connector reads — the union of
// the fields the three kinds publish. A field absent on a given kind stays zero and is
// simply not used for that kind (e.g. Artifact is GitRepository-only,
// LastAppliedRevision is Kustomization-only, History is HelmRelease-only). Each field
// is a structural posture value; the revision strings are read only to compute drift
// in memory and are NEVER placed into a finding.
type fluxStatus struct {
	Conditions         []condition `yaml:"conditions"`
	ObservedGeneration int64       `yaml:"observedGeneration"`

	// GitRepository: the current artifact (the pinned revision / commit SHA).
	Artifact artifact `yaml:"artifact"`

	// Kustomization: the applied vs the last attempted revision (drift when unequal).
	LastAppliedRevision   string `yaml:"lastAppliedRevision"`
	LastAttemptedRevision string `yaml:"lastAttemptedRevision"`

	// HelmRelease: the applied revision is the head of the history (NOT
	// lastAppliedRevision, which is a Kustomization concept on HelmRelease v2).
	History []historyEntry `yaml:"history"`
}

// artifact is GitRepository status.artifact. Only revision is read (the commit SHA
// the source controller pinned — OpenGitOps principle 2 evidence). It is read to know
// the source produced an artifact; it is never emitted.
type artifact struct {
	Revision string `yaml:"revision"`
}

// historyEntry is one HelmRelease status.history[] snapshot. The head (index 0) is
// the most recent release; its chartVersion is the APPLIED revision the connector
// compares against status.lastAttemptedRevision to detect drift. Only chartVersion is
// read (a version token), never the values/config the snapshot may reference.
type historyEntry struct {
	ChartVersion string `yaml:"chartVersion"`
}

// condition is one status.conditions[] entry (a metav1.Condition). The connector reads
// type (to find the Ready condition), status ("True"/"False"/"Unknown") and reason (a
// short machine token such as ReconciliationSucceeded / ArtifactFailed). It NEVER reads
// the condition message: a Flux message can embed a registry URL, a chart path or a
// YAML build error fragment — payload-adjacent, docs/SECURITY-HARDENING.md.
type condition struct {
	Type   string `yaml:"type"`
	Status string `yaml:"status"`
	Reason string `yaml:"reason"`
}

// isFluxDoc reports whether a decoded document is one of the three Flux CRDs this
// connector handles, dispatching on BOTH the API group prefix and the kind so a
// kind name reused by another group (e.g. a "Kustomization" from kustomize.config.k8s.io,
// the kustomize.yaml format) is not mistaken for a Flux object.
func isFluxDoc(d document) bool {
	return subjectKindOf(d) != ""
}

// subjectKindOf returns the FindingReport.SubjectKind for a document, or "" if the
// document is not a Flux CRD this connector handles. The (group prefix, kind) pair
// must BOTH match — accepting all three Flux groups without hardcoding a version.
func subjectKindOf(d document) string {
	switch {
	case strings.HasPrefix(d.APIVersion, groupSource) && d.Kind == kindGitRepository:
		return subjectGitRepository
	case strings.HasPrefix(d.APIVersion, groupKustomize) && d.Kind == kindKustomization:
		return subjectKustomization
	case strings.HasPrefix(d.APIVersion, groupHelm) && d.Kind == kindHelmRelease:
		return subjectHelmRelease
	default:
		return ""
	}
}

// namespaceOf returns the object's namespace, defaulting to the flux-system namespace
// when omitted.
func namespaceOf(d document) string {
	if ns := strings.TrimSpace(d.Metadata.Namespace); ns != "" {
		return ns
	}
	return defaultNamespace
}

// subjectRef is the stable "<namespace>/<name>" reference a finding's SubjectRef and
// DetailHash converge on.
func subjectRef(d document) string {
	return namespaceOf(d) + "/" + strings.TrimSpace(d.Metadata.Name)
}

// readyState is the classified reconciliation truth of a Flux object, derived VERBATIM
// from status.conditions[?(@.type=="Ready")].status.
type readyState int

const (
	// readyUnknown: no Ready condition reported yet, or a Ready.status outside
	// True/False. Classified honestly as Unknown, never silently Healthy.
	readyUnknown readyState = iota
	// readyTrue: Ready.status == "True" — reconciled.
	readyTrue
	// readyFalse: Ready.status == "False" — reconciliation failing.
	readyFalse
)

// readyOf extracts the Ready condition's classified state and its reason token from a
// document's status.conditions. It is tolerant of the conditions list being absent,
// empty or holding other condition types (a Flux object also publishes Stalled,
// Reconciling, Released, etc.) — it scans for the FIRST entry whose type is "Ready"
// (case-insensitive on the well-known token), and treats a missing Ready as Unknown.
// The reason is returned only so a failing finding can name WHY (a short machine
// token); a True/Unknown result carries no reason.
func readyOf(d document) (readyState, string) {
	for _, c := range d.Status.Conditions {
		if !strings.EqualFold(strings.TrimSpace(c.Type), readyConditionType) {
			continue
		}
		switch strings.TrimSpace(c.Status) {
		case condTrue:
			return readyTrue, ""
		case condFalse:
			return readyFalse, strings.TrimSpace(c.Reason)
		default:
			return readyUnknown, ""
		}
	}
	return readyUnknown, ""
}

// appliedRevision returns the revision a Flux object has APPLIED, for the kind-specific
// drift comparison. It is read in-memory to compare against the attempted revision and
// is NEVER emitted. For a Kustomization it is status.lastAppliedRevision; for a
// HelmRelease it is the head of status.history (history[0].chartVersion — the applied
// chart version, NOT lastAppliedRevision on HelmRelease v2). A GitRepository has no
// applied/attempted pair (it is the source, not an applier), so it returns "".
func appliedRevision(d document) string {
	switch subjectKindOf(d) {
	case subjectKustomization:
		return strings.TrimSpace(d.Status.LastAppliedRevision)
	case subjectHelmRelease:
		if len(d.Status.History) > 0 {
			return strings.TrimSpace(d.Status.History[0].ChartVersion)
		}
	}
	return ""
}

// attemptedRevision returns the revision a Flux object last ATTEMPTED to apply, for the
// drift comparison. Like appliedRevision it is read only in-memory and never emitted.
// Both Kustomization and HelmRelease publish status.lastAttemptedRevision; a
// GitRepository does not, so it returns "".
func attemptedRevision(d document) string {
	switch subjectKindOf(d) {
	case subjectKustomization, subjectHelmRelease:
		return strings.TrimSpace(d.Status.LastAttemptedRevision)
	}
	return ""
}

// isDrifted reports whether a reconciled object is nonetheless drifted: the applied
// revision differs from the last attempted revision (the controller is mid-flight or
// failed to converge a newer attempt), OR status.observedGeneration lags
// metadata.generation (the controller has not yet observed the latest spec). The
// revisions are compared in-memory only; neither value is emitted. A comparison is
// only meaningful when both sides are present — a missing side is NOT treated as drift
// (absence is not a finding; ARCHITECTURE.md).
func isDrifted(d document) bool {
	applied := appliedRevision(d)
	attempted := attemptedRevision(d)
	if applied != "" && attempted != "" && applied != attempted {
		return true
	}
	// observedGeneration lagging the spec generation. generation==0 means the export
	// did not carry it (a manifest without metadata.generation), so the comparison is
	// skipped rather than fabricating drift.
	if d.Metadata.Generation > 0 && d.Status.ObservedGeneration > 0 &&
		d.Status.ObservedGeneration != d.Metadata.Generation {
		return true
	}
	return false
}
