// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package argocd

import "strings"

// apiGroupPrefix is the Argo CD API group. A manifest whose apiVersion does not
// begin with this (a stray ConfigMap, a Service, a cert-manager object sharing the
// same directory) is ignored. Argo CD ships the Application API as
// argoproj.io/v1alpha1 (the GA-as-alpha name, stable since the project graduated
// CNCF 2022-12-06). Verified against argo-cd.readthedocs.io/operator-manual/.
const apiGroupPrefix = "argoproj.io/"

// kindApplication is the only kind this connector handles. Any other kind in a
// mixed manifest is skipped.
const kindApplication = "Application"

// defaultNamespace is the namespace assumed when an Application omits
// metadata.namespace (Argo Applications conventionally live in the argocd
// namespace, but Kubernetes defaults an omitted namespace to "default"; the value
// is recorded into the subject ref, never inferred into a posture guess).
const defaultNamespace = "argocd"

// The sync-status values Argo CD reports on status.sync.status. Verbatim from
// argo-cd.readthedocs.io: "Synced" = the desired state matches live, "OutOfSync" =
// a mismatch (drift), "Unknown" = could not be determined.
const (
	syncSynced    = "Synced"
	syncOutOfSync = "OutOfSync"
)

// The health-status values Argo CD reports on status.health.status. Verbatim from
// the health page priority hierarchy (most→least healthy): Healthy, Suspended,
// Progressing, Missing, Degraded, Unknown.
const (
	healthHealthy     = "Healthy"
	healthProgressing = "Progressing"
	healthDegraded    = "Degraded"
	healthSuspended   = "Suspended"
	healthMissing     = "Missing"
)

// The operation phases Argo CD reports on status.operationState.phase. Verbatim
// from the notifications/triggers doc: Running, Succeeded, Error, Failed.
const (
	phaseError  = "Error"
	phaseFailed = "Failed"
)

// document is the minimal envelope of an Argo CD Application manifest the connector
// reads. apiVersion+kind dispatch the decode; metadata carries name/namespace;
// status carries ONLY the sync/health/operation posture. The connector reads
// NOTHING from spec (which can carry Helm values / plugin env that embed secrets —
// payload-adjacent, docs/SECURITY-HARDENING.md): only the observed reconciliation posture.
type document struct {
	APIVersion string     `yaml:"apiVersion"`
	Kind       string     `yaml:"kind"`
	Metadata   objectMeta `yaml:"metadata"`
	Status     appStatus  `yaml:"status"`
}

type objectMeta struct {
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"`
}

// appStatus is the subset of an Application status the connector reads. Each field
// is a structural posture value, never payload.
type appStatus struct {
	Sync           syncStatus     `yaml:"sync"`
	Health         healthStatus   `yaml:"health"`
	OperationState operationState `yaml:"operationState"`
}

type syncStatus struct {
	Status string `yaml:"status"`
}

type healthStatus struct {
	Status string `yaml:"status"`
}

type operationState struct {
	Phase string `yaml:"phase"`
}

// isApplicationDoc reports whether a decoded document is an Argo CD Application this
// connector handles.
func isApplicationDoc(d document) bool {
	if !strings.HasPrefix(d.APIVersion, apiGroupPrefix) {
		return false
	}
	return d.Kind == kindApplication
}

// namespaceOf returns the object's namespace, defaulting to the argocd namespace
// when omitted.
func namespaceOf(d document) string {
	if ns := strings.TrimSpace(d.Metadata.Namespace); ns != "" {
		return ns
	}
	return defaultNamespace
}

// subjectRef is the stable "<namespace>/<name>" reference a finding's SubjectRef
// and DetailHash converge on.
func subjectRef(d document) string {
	return namespaceOf(d) + "/" + strings.TrimSpace(d.Metadata.Name)
}
