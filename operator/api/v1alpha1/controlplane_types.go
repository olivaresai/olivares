// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package v1alpha1

import (
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Engine is the store engine the control plane runs against. It mirrors the
// Helm chart's `core.engine` value (deploy/helm/.../values.yaml §core.engine).
// +kubebuilder:validation:Enum=sqlite;postgres
type Engine string

const (
	// EngineSQLite is the default, zero-external-dependency single-node store.
	EngineSQLite Engine = "sqlite"
	// EnginePostgres is the multi-tenant store; requires a DSN via spec.postgres.
	EnginePostgres Engine = "postgres"
)

// HARoutingMode selects how an active-passive HA control plane exposes pod
// readiness and routes client traffic.
// +kubebuilder:validation:Enum=Legacy;LeaderRouting
type HARoutingMode string

const (
	// HARoutingLegacy is the historical layout: the container readinessProbe is
	// /readyz, which a STANDBY fails on purpose, so the headless Service
	// routes only to the leader. Its cost is structural: ReadyReplicas can never
	// reach desired, so the ControlPlane never reports Ready, and a StatefulSet
	// rolling update WEDGES at the first replaced standby (a never-Ready pod never
	// satisfies the update barrier). Safe, but not upgradeable in place.
	HARoutingLegacy HARoutingMode = "Legacy"
	// HARoutingLeader is the leader-routing (Patroni-style) layout: pod health and
	// leader eligibility become two different signals. The readinessProbe becomes
	// the leader-agnostic /pod-readyz, so every healthy replica is Ready and the
	// rolling update progresses; the engine publishes `ops.olivares.ai/role=leader`
	// on its own pod after it wins the election, and a dedicated
	// `Service/<name>-leader` selects that label, so client traffic still reaches
	// exactly one active writer. Every application request additionally re-checks
	// leadership in the engine, so a briefly stale label costs a retryable 503, not
	// a second writer.
	//
	// REQUIREMENTS (the operator cannot verify them, which is why this is an
	// explicit opt-in, not a silent default):
	//   - spec.image must be an engine that serves /pod-readyz and publishes the
	//     role label (olivares >= 26.7.0). With an older image every pod fails the
	//     new probe and the control plane goes unroutable.
	//   - clients must move to `<name>-leader` (the headless `<name>` Service keeps
	//     its per-pod DNS identity and now includes Ready standbys, which answer
	//     application requests with 503 not_leader).
	// See docs/HA-LEADER-ROUTING.md for the staged migration and rollback.
	HARoutingLeader HARoutingMode = "LeaderRouting"
)

// PostgresSpec configures the EXTERNAL Postgres store (required when
// Engine=postgres — this operator, like the chart, does not bundle Postgres).
// Mirrors the chart's `postgres.*` values: the DSN never appears inline, it comes
// from a Secret. The engine reads the DSN ONLY from the `--dsn` flag (there is no
// DSN env fallback — core/store/config.go), so the controller wires it as
// `--dsn=$(OLIVARES_DSN)` with the env injected from a secretKeyRef and resolved
// by Kubernetes $(VAR) expansion.
type PostgresSpec struct {
	// DSNSecret names a Secret holding the application-role DSN (a NON-superuser,
	// NOBYPASSRLS libpq/pgx URL — deploy/postgres/01-app-role.sql). Required.
	// +kubebuilder:validation:MinLength=1
	DSNSecret string `json:"dsnSecret"`

	// DSNKey is the key in DSNSecret holding the application DSN.
	// +kubebuilder:default=dsn
	// +optional
	DSNKey string `json:"dsnKey,omitempty"`

	// AdminDSNKey, when set, names the key in DSNSecret holding a dedicated
	// BYPASSRLS (NOSUPERUSER) admin DSN. It serves two consumers: genuinely
	// cross-tenant System reads (the org list, multi-tenant checkpoint coverage),
	// and the backup CronJob's pg_dump initContainer — under FORCE ROW LEVEL
	// SECURITY pg_dump keeps row_security=off and ABORTS as a role that cannot
	// bypass RLS, so an application-role dump produces no backup at all. It is
	// therefore OPT-IN only while spec.backup is unset (empty leaves System reads
	// RLS-scoped, the engine's own logged default) and REQUIRED by validation when
	// backups are enabled on postgres. When set, the key MUST exist in the Secret —
	// the engine eagerly opens and probes the admin pool at boot and fails closed
	// on a bad/absent DSN (core/internal/store/sqlstore openAdminPool), so the
	// controller wires `--admin-dsn=$(OLIVARES_ADMIN_DSN)` only when explicitly
	// requested. (The Helm chart always emits that flag with an `optional` env,
	// which crashloops boot when the key is absent — a footgun this opt-in avoids.)
	// +optional
	AdminDSNKey string `json:"adminDsnKey,omitempty"`
}

// BackupSpec declares a scheduled, ledger-continuity-safe disaster-recovery
// backup of the operand. The owned CronJob runs the REAL `olivares dr backup`
// (cmd/olivares/cmd_dr.go) over the operand's data PVC, sealing the signing keys
// under a key-encryption key (KEK) and writing a verifiable bundle to a
// destination PVC. A same-cluster bundle is NOT disaster recovery — MIRROR the
// destination offsite (3-2-1, docs/DR-RUNBOOK.md).
type BackupSpec struct {
	// Schedule is a standard Kubernetes CronJob cron expression (UTC), e.g.
	// "0 3 * * *" for nightly at 03:00.
	// +kubebuilder:validation:MinLength=9
	Schedule string `json:"schedule"`

	// KEKSecret names a Secret carrying the key-encryption key the bundle seals the
	// signing keys under. REQUIRED: without it a restored ledger cannot be verified
	// (the per-event signing key is sealed with it). Keep it SEPARATE from the
	// bundles. Mounted read-only at 0400.
	// +kubebuilder:validation:MinLength=1
	KEKSecret string `json:"kekSecret"`

	// KEKPassphraseKey is the key in KEKSecret holding a passphrase (the default,
	// Argon2id-derived KEK → `--passphrase-file`). Used unless KEKRawKey is set.
	// +kubebuilder:default=passphrase
	// +optional
	KEKPassphraseKey string `json:"kekPassphraseKey,omitempty"`

	// KEKRawKey, when set, names the key in KEKSecret holding a raw/base64 32-byte
	// KEK (the KMS-unwrapped path) and selects `--kek-key-file` over
	// `--passphrase-file`.
	// +optional
	KEKRawKey string `json:"kekRawKey,omitempty"`

	// Destination configures the PersistentVolumeClaim the bundles are written to.
	// +optional
	Destination BackupDestination `json:"destination,omitempty"`

	// RetentionDays prunes local bundles older than N days after each successful
	// run (`dr backup --retain-days`). The offsite mirror keeps longer.
	// +kubebuilder:default=14
	// +kubebuilder:validation:Minimum=1
	// +optional
	RetentionDays int32 `json:"retentionDays,omitempty"`

	// SuccessfulJobsHistoryLimit caps how many completed backup Job objects are
	// retained (maps to the CronJob's successfulJobsHistoryLimit).
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=1
	// +optional
	SuccessfulJobsHistoryLimit int32 `json:"successfulJobsHistoryLimit,omitempty"`

	// Resources sets the backup container's compute requests/limits. Left empty,
	// the controller applies a conservative default so the Job is never QoS
	// BestEffort.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// PgClientImage is the postgres-client image whose `pg_dump` produces the store
	// snapshot for engine=postgres (the distroless engine image has no pg_dump); it
	// runs as an initContainer. Ignored for sqlite.
	// +kubebuilder:default="postgres:16-alpine"
	// +optional
	PgClientImage string `json:"pgClientImage,omitempty"`
}

// BackupDestination configures where the DR bundle CronJob writes its bundles.
type BackupDestination struct {
	// ExistingClaim names a pre-provisioned PVC to write bundles to. When empty the
	// controller creates one named "<name>-backups" that is intentionally NOT
	// owned by (garbage-collected with) the ControlPlane — DR data must survive a
	// `kubectl delete controlplane`.
	// +optional
	ExistingClaim string `json:"existingClaim,omitempty"`

	// Size of the controller-created destination PVC (ignored when ExistingClaim is
	// set).
	// +kubebuilder:default="16Gi"
	// +optional
	Size string `json:"size,omitempty"`

	// StorageClass of the controller-created destination PVC ("" uses the cluster
	// default; ignored when ExistingClaim is set).
	// +optional
	StorageClass string `json:"storageClass,omitempty"`
}

// PersistenceSpec configures the per-replica data PersistentVolumeClaim
// (volumeClaimTemplate) that holds the data dir — the audit signing key + TLS
// material (+ the store in sqlite mode). Mirrors the chart's core.persistence.*.
// These fields are IMMUTABLE after the StatefulSet is created (a volumeClaimTemplate
// cannot be patched), so set them at install time.
type PersistenceSpec struct {
	// Size of the data PVC.
	// +kubebuilder:default="8Gi"
	// +optional
	Size string `json:"size,omitempty"`

	// StorageClass of the data PVC. "" uses the cluster default; "-" disables
	// dynamic provisioning (an empty storageClassName, for a pre-bound PV) —
	// mirroring the chart. On a cluster with NO default StorageClass, set this or
	// the PVC stays Pending forever.
	// +optional
	StorageClass string `json:"storageClass,omitempty"`

	// AccessModes of the data PVC (default ReadWriteOnce — the single-writer norm;
	// the backup Job co-locates on the same node to share it).
	// +kubebuilder:default={ReadWriteOnce}
	// +optional
	AccessModes []corev1.PersistentVolumeAccessMode `json:"accessModes,omitempty"`
}

// ControlPlaneSpec is the desired state of a control plane instance.
//
// The CEL admission rules below reject — at the apiserver, before the object is
// persisted — the three impossible combinations that would otherwise crashloop or
// silently disable the workload, mirroring the chart's render-time `fail` guards
// (_helpers.tpl "olivares.validate"). The sqlite+replicas>1 case is deliberately
// NOT rejected: it is a safe CLAMP to one effective replica (effectiveReplicas),
// not an impossible spec. ControlPlaneSpec.Validate re-checks the same invariants
// in the controller as defense-in-depth for clusters where CEL is disabled.
//
// +kubebuilder:validation:XValidation:rule="self.engine != 'postgres' || (has(self.postgres) && size(self.postgres.dsnSecret) > 0)",message="engine=postgres requires spec.postgres.dsnSecret: the engine reads the DSN only from --dsn (no env fallback) and this operator does not bundle Postgres"
// +kubebuilder:validation:XValidation:rule="!(self.engine == 'postgres' && self.replicas > 1) || (has(self.auditSigningKeySecret) && size(self.auditSigningKeySecret) > 0)",message="engine=postgres with replicas>1 requires spec.auditSigningKeySecret: in active-passive HA every replica must sign the audit ledger with the SAME shared key or the hash-chain forks when a standby is promoted"
// +kubebuilder:validation:XValidation:rule="!(self.engine == 'postgres' && has(self.backup)) || (has(self.postgres) && has(self.postgres.adminDsnKey) && size(self.postgres.adminDsnKey) > 0)",message="engine=postgres with spec.backup requires spec.postgres.adminDsnKey: pg_dump keeps row_security=off and aborts as the NOBYPASSRLS application role under FORCE ROW LEVEL SECURITY, so without the admin DSN every scheduled dump fails and there is no backup"
type ControlPlaneSpec struct {
	// Image is the fully-qualified olivares image+tag (or @sha256 digest).
	// Changing this field is the UPGRADE path: the controller rolls the
	// StatefulSet to the new image and reports Progressing until the new
	// replicas are Ready.
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// Replicas is the desired number of core replicas. The control plane is a
	// single-WRITER system but supports active-passive HA: with
	// engine=postgres and a shared audit signing key (auditSigningKeySecret), run
	// >1 replica and a Postgres advisory-lock elects one active writer while the
	// rest are hot standbys that take over on failover (ARCHITECTURE.md-ARCHITECTURE
	// §7.1). With engine=sqlite the store and audit key are local to one pod, so
	// >1 replicas would fork the ledger; the operator CLAMPS sqlite to 1 effective
	// replica and reports it via a Degraded condition. Set Engine=postgres (with
	// auditSigningKeySecret) to scale past 1.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=1
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// Engine selects the store engine (sqlite|postgres). Mirrors core.engine.
	// +kubebuilder:default=sqlite
	// +optional
	Engine Engine `json:"engine,omitempty"`

	// HARouting selects the active-passive HA layout (ignored outside HA, i.e.
	// unless engine=postgres with >1 replica). Legacy (the default) keeps the
	// historical leader-only readiness: safe, but the ControlPlane never reports
	// Ready and a rolling update wedges. LeaderRouting switches to the
	// leader-routing layout — pod-health readiness plus a leader-selecting
	// Service — which is what makes an in-place HA upgrade possible. It is an
	// explicit opt-in because it REQUIRES an engine image that serves /pod-readyz
	// and publishes the leader label, and because clients must move to the
	// `<name>-leader` Service. See HARoutingMode and docs/HA-LEADER-ROUTING.md.
	// +kubebuilder:default=Legacy
	// +optional
	HARouting HARoutingMode `json:"haRouting,omitempty"`

	// ProgressDeadlineSeconds bounds how long a rollout may make no observable
	// progress (no new update revision, no additional updated or Ready replica)
	// before the operator reports it as stalled: Progressing=False/
	// ProgressDeadlineExceeded plus Degraded/RolloutStalled. StatefulSets carry no
	// progress deadline of their own, so without this a wedged rollout is
	// indistinguishable from a slow one. Default 600s.
	// +kubebuilder:default=600
	// +kubebuilder:validation:Minimum=30
	// +optional
	ProgressDeadlineSeconds int32 `json:"progressDeadlineSeconds,omitempty"`

	// Postgres configures the external Postgres store. REQUIRED when
	// Engine=postgres (the DSN is wired as --dsn=$(OLIVARES_DSN) from this Secret).
	// Ignored for sqlite.
	// +optional
	Postgres *PostgresSpec `json:"postgres,omitempty"`

	// AuditSigningKeySecret names a Secret with key `audit-signing.key` (base64
	// Ed25519) mounted read-only into EVERY replica and exported as
	// OLIVARES_AUDIT_SIGNING_KEY_FILE so all replicas sign the audit ledger with
	// the SAME key — the hash-chain does not fork when a standby is promoted.
	// REQUIRED for Replicas>1 (active-passive HA). Empty (single-node) lets the
	// engine mint a per-node key in the data dir.
	// +optional
	AuditSigningKeySecret string `json:"auditSigningKeySecret,omitempty"`

	// CatalogSigningKeySecret optionally names a Secret with key
	// `catalog-signing.key` mounted as OLIVARES_CATALOG_SIGNING_KEY_FILE. The
	// catalog (artifact) key is NOT ledger integrity, so sharing it is optional;
	// when empty it defaults to AuditSigningKeySecret (the chart's convention — one
	// Secret may carry both keys, and the engine mints a per-node catalog key if
	// the item is absent).
	// +optional
	CatalogSigningKeySecret string `json:"catalogSigningKeySecret,omitempty"`

	// Resources sets the core container's compute requests/limits. Mirrors the
	// chart's core.resources. Left empty, the controller applies a conservative
	// default (Burstable) so the control plane is never QoS BestEffort — the first
	// thing the kubelet OOM-kills/evicts under pressure.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// Persistence configures the per-replica data PVC (size/storageClass/
	// accessModes). Mirrors the chart's core.persistence.*. IMMUTABLE after create
	// (set it at install time); on a cluster with no default StorageClass it must
	// be set or the PVC stays Pending.
	// +optional
	Persistence *PersistenceSpec `json:"persistence,omitempty"`

	// ConfigRef is the name of a ConfigMap or Secret carrying additional engine
	// configuration (extra env). It is loaded via envFrom. Changing this field is
	// the RECONFIGURE path: a change to the referenced object's name (and, when
	// resolvable, its content hash) triggers a rollout of the StatefulSet. The
	// postgres DSN comes from spec.postgres, NOT here (envFrom env is not read by
	// the engine for the DSN).
	// +optional
	ConfigRef string `json:"configRef,omitempty"`

	// Backup, when set, declares a scheduled backup CronJob (see BackupSpec).
	// +optional
	Backup *BackupSpec `json:"backup,omitempty"`

	// RestoreFrom, when set, names a backup id / source PVC to restore from on
	// the next reconcile. This is a DECLARED seam: the controller records the
	// request and surfaces it on the pod template; a restore is the symmetric
	// `olivares dr restore` runbook operation (docs/DR-RUNBOOK.md), deliberately
	// kept manual rather than auto-driven (a restore overwrites a live ledger).
	// +optional
	RestoreFrom string `json:"restoreFrom,omitempty"`
}

// Validate reports whether the spec is a structurally-impossible combination the
// controller must refuse rather than materialize into a crashlooping workload. It
// is AT LEAST AS STRICT as the CRD's admission CEL rules (and the chart's
// render-time `fail` guards), as in-controller defense-in-depth: a cluster with
// CEL validation disabled still gets a clear Invalid status instead of a
// crashloop. It additionally trims whitespace, so a whitespace-only secret name
// (which the CEL `size() > 0` check would admit) is also caught here. The
// sqlite+replicas>1 case is intentionally NOT an error here — it is a safe clamp
// (effectiveReplicas), not an impossible spec.
func (s *ControlPlaneSpec) Validate() error {
	engine := s.Engine
	if engine == "" {
		engine = EngineSQLite
	}
	var problems []string
	if engine == EnginePostgres && (s.Postgres == nil || strings.TrimSpace(s.Postgres.DSNSecret) == "") {
		problems = append(problems, "engine=postgres requires spec.postgres.dsnSecret (the engine reads the DSN only from --dsn; there is no env fallback)")
	}
	if engine == EnginePostgres && s.Replicas > 1 && strings.TrimSpace(s.AuditSigningKeySecret) == "" {
		problems = append(problems, "engine=postgres with replicas>1 requires spec.auditSigningKeySecret (a shared key, or the audit hash-chain forks when a standby is promoted)")
	}
	if engine == EnginePostgres && s.Backup != nil && (s.Postgres == nil || strings.TrimSpace(s.Postgres.AdminDSNKey) == "") {
		problems = append(problems, "engine=postgres with spec.backup requires spec.postgres.adminDsnKey (pg_dump keeps row_security=off and ABORTS as the NOBYPASSRLS application role under FORCE ROW LEVEL SECURITY, so every scheduled dump fails and there is no backup; separately, a dr backup handed an external snapshot without the admin DSN builds an incomplete tenant inventory)")
	}
	// The two fields are constrained by the CRD schema (an enum and a minimum),
	// but this validator promises to be AT LEAST as strict — a cluster with schema
	// validation disabled must not silently treat an unknown routing value as Legacy
	// (an operator would think the migration was applied) or accept a 5-second
	// progress deadline (which would report every rollout as stalled).
	switch s.HARouting {
	case "", HARoutingLegacy, HARoutingLeader:
	default:
		problems = append(problems, fmt.Sprintf("spec.haRouting %q is not a valid HA layout (use %q or %q)", s.HARouting, HARoutingLegacy, HARoutingLeader))
	}
	if s.ProgressDeadlineSeconds != 0 && s.ProgressDeadlineSeconds < 30 {
		problems = append(problems, fmt.Sprintf("spec.progressDeadlineSeconds %d is below the 30s minimum (a shorter deadline reports healthy rollouts as stalled)", s.ProgressDeadlineSeconds))
	}
	// Reject unparseable PVC sizes here rather than panicking on resource.MustParse
	// deep in the reconciler (which would crash the manager for every ControlPlane).
	if s.Persistence != nil {
		if sz := strings.TrimSpace(s.Persistence.Size); sz != "" {
			if _, err := resource.ParseQuantity(sz); err != nil {
				problems = append(problems, fmt.Sprintf("spec.persistence.size %q is not a valid quantity: %v", sz, err))
			}
		}
	}
	if s.Backup != nil {
		if sz := strings.TrimSpace(s.Backup.Destination.Size); sz != "" {
			if _, err := resource.ParseQuantity(sz); err != nil {
				problems = append(problems, fmt.Sprintf("spec.backup.destination.size %q is not a valid quantity: %v", sz, err))
			}
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New(strings.Join(problems, "; "))
}

// Condition types and phases reported in status.
const (
	// ConditionAvailable answers ONE question: can clients reach the control plane
	// right now? In the leader-routing HA layout that means exactly one Ready pod
	// publishes the leader label (the single endpoint of the `<name>-leader`
	// Service); otherwise it means at least one Ready replica serves. It is
	// deliberately independent of Progressing — a healthy image rollout is normally
	// Progressing=True AND Available=True.
	ConditionAvailable = "Available"
	// ConditionProgressing is True while a rollout (create/scale/upgrade/
	// reconfigure) is ADVANCING toward the desired state. It is False both when the
	// rollout is complete and when it is not advancing at all: a stalled rollout
	// (reason ProgressDeadlineExceeded), a layout migration waiting on an
	// administrator, or converged pods whose leader label is missing/duplicated.
	ConditionProgressing = "Progressing"
	// ConditionDegraded is True when the operator had to clamp or otherwise
	// derate the spec for safety — e.g. an engine=sqlite request for >1 replica,
	// which would fork the audit ledger (each pod mints its own signing key over
	// its own RWO volume) — when the spec failed structural validation (reason
	// SpecInvalid), or when a state needs an operator's attention: the legacy HA
	// readiness layout (HALegacyReadinessBlocked), a pending layout cut-over
	// (HALeaderServiceMigrationRequired), a missing or duplicated leader label
	// (LeaderNotPublished / MultipleLeadersPublished), or a wedged rollout
	// (RolloutStalled). False means the spec is applied as written.
	ConditionDegraded = "Degraded"

	// PhasePending is set when zero replicas are desired (nothing to converge to).
	PhasePending = "Pending"
	// PhaseProgressing is set whenever the observed state has not converged — a
	// rollout in flight, and equally a state that needs attention (see Degraded).
	PhaseProgressing = "Progressing"
	// PhaseReady is set when the rollout is fully realized in OBSERVED state (the
	// StatefulSet has observed the latest generation, revisions are settled, and
	// every desired replica exists, is updated and is Ready) AND client traffic can
	// reach the writer — in the leader-routing HA layout, exactly one Ready pod
	// publishing the leader label. The legacy HA layout cannot reach it by
	// construction: standbys fail the leader-only readiness probe on purpose.
	PhaseReady = "Ready"
	// PhaseInvalid is set when the spec is a structurally-impossible combination
	// (the same combinations the admission CEL rules reject). The controller
	// refuses to materialize a crashlooping workload and surfaces why.
	PhaseInvalid = "Invalid"
)

// ControlPlaneStatus is the observed state of a control plane instance.
type ControlPlaneStatus struct {
	// ObservedGeneration is the .metadata.generation the controller last acted
	// on. When it lags .metadata.generation a reconcile is pending.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions follow the standard metav1.Condition contract (Available,
	// Progressing, Degraded). Patched via meta.SetStatusCondition.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// Phase is a coarse, human-facing summary (Pending|Progressing|Ready|Invalid).
	// +optional
	Phase string `json:"phase,omitempty"`

	// CurrentImage is the image currently rolled out on the StatefulSet. During
	// an upgrade it lags Spec.Image until the rollout completes.
	// +optional
	CurrentImage string `json:"currentImage,omitempty"`

	// ReadyReplicas is the number of Ready replicas observed on the StatefulSet.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// LastBackup is the time the most recent owned backup CronJob last succeeded
	// (mirrored from the CronJob's status), or nil if none has run.
	// +optional
	LastBackup *metav1.Time `json:"lastBackup,omitempty"`

	// LeaderPod is the pod currently publishing the HA leader label — the single
	// endpoint of the `<name>-leader` Service (leader-routing layout only). Empty
	// when none is published, when more than one is (never pick one arbitrarily:
	// the Degraded condition says so), or outside the leader-routing layout.
	// +optional
	LeaderPod string `json:"leaderPod,omitempty"`

	// RolloutRevision, LastProgressTime, LastProgressUpdatedReplicas and
	// LastProgressReadyReplicas are the rollout-progress bookkeeping behind
	// ProgressDeadlineExceeded (spec.progressDeadlineSeconds). They record the last
	// point at which the rollout visibly ADVANCED — a new StatefulSet update
	// revision, one more updated replica, or one more Ready replica — which is the
	// only way to tell a wedged rollout from a slow one (StatefulSet status has no
	// progress deadline of its own).
	// +optional
	RolloutRevision string `json:"rolloutRevision,omitempty"`
	// +optional
	LastProgressTime *metav1.Time `json:"lastProgressTime,omitempty"`
	// +optional
	LastProgressUpdatedReplicas int32 `json:"lastProgressUpdatedReplicas,omitempty"`
	// +optional
	LastProgressReadyReplicas int32 `json:"lastProgressReadyReplicas,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:singular=olivares,shortName=cp;controlplanes,categories=olivares
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.status.currentImage`
// +kubebuilder:printcolumn:name="Engine",type=string,JSONPath=`.spec.engine`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ControlPlane is the Schema for the controlplanes API. It is the declarative
// lifecycle object for an Olivares AI control plane instance: install, upgrade
// (spec.image), reconfigure (spec.configRef), and backup/restore (spec.backup,
// spec.restoreFrom) — Operator Capability Level 3 ("Full Lifecycle").
type ControlPlane struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ControlPlaneSpec   `json:"spec,omitempty"`
	Status ControlPlaneStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ControlPlaneList contains a list of ControlPlane.
type ControlPlaneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ControlPlane `json:"items"`
}

// The ControlPlane types are registered with the scheme by addKnownTypes in
// groupversion_info.go (via runtime.NewSchemeBuilder).
