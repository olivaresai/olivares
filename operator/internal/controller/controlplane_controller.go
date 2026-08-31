// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package controller holds the ControlPlane reconciler. It materializes the same
// production Kubernetes shapes the Helm chart renders (deploy/helm/olivares): a
// single-writer (or active-passive HA) StatefulSet with the postgres DSN, shared
// audit signing key and compute Resources wired in; a headless Service; and —
// when spec.Backup is set — a CronJob that runs the REAL `olivares dr backup`
// over the operand's data PVC, all owned by the ControlPlane object.
package controller

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	opsv1alpha1 "github.com/olivaresai/olivares/operator/api/v1alpha1"
)

const (
	// containerName mirrors the Helm chart's core container name.
	containerName = "olivares"
	// dataMountPath / dataVolumeName mirror core.dataDir + the volumeClaimTemplate.
	dataMountPath  = "/var/lib/olivares"
	dataVolumeName = "data"
	tmpVolumeName  = "tmp"
	// httpsPort / grpcPort mirror the chart's containerPorts (8443/8444).
	httpsPort = 8443
	grpcPort  = 8444
	// runAsUser mirrors the chart's hardened non-root uid/gid (65532).
	runAsUser int64 = 65532

	// Shared-signing-key mounts (HA): the audit key is mounted into EVERY
	// replica so the audit hash-chain does not fork at failover; the catalog key is
	// the optional artifact-signing companion. Paths/filenames mirror the chart.
	auditKeyVolumeName   = "audit-key"
	auditKeyMountPath    = "/etc/olivares/audit-key"
	auditKeyFileName     = "audit-signing.key"
	auditKeyEnv          = "OLIVARES_AUDIT_SIGNING_KEY_FILE"
	catalogKeyVolumeName = "catalog-key"
	catalogKeyMountPath  = "/etc/olivares/catalog-key"
	catalogKeyFileName   = "catalog-signing.key"
	catalogKeyEnv        = "OLIVARES_CATALOG_SIGNING_KEY_FILE"
	// 0440 so the engine's fail-closed owner-only secret check accepts the mount (a
	// default 0644 Secret mount is refused); 0400 for the KEK.
	keySecretMode int32 = 0o440
	kekSecretMode int32 = 0o400

	// DSN wiring (postgres): the engine reads the DSN only from --dsn (no env
	// fallback), so the controller injects an env from a secretKeyRef and lets
	// Kubernetes expand $(OLIVARES_DSN) into the arg.
	dsnEnvName      = "OLIVARES_DSN"
	adminDSNEnvName = "OLIVARES_ADMIN_DSN"

	// Backup (DR) job paths, mirroring backup-cronjob.yaml.
	kekVolumeName     = "kek"
	kekMountPath      = "/etc/olivares/dr-kek"
	backupsVolumeName = "backups"
	backupsMountPath  = "/backups"
	workVolumeName    = "work"
	workMountPath     = "/work"
	pgSnapshotPath    = "/work/dump.pgcustom"
	backupContainer   = "dr-backup"
	pgDumpContainer   = "pg-dump"

	// podNameLabel is the StatefulSet-injected pod identity label; the backup Job
	// pins itself to the ordinal-0 pod's node so it can mount that pod's RWO data
	// PVC (RWO permits multiple pods on the SAME node).
	podNameLabel = "statefulset.kubernetes.io/pod-name"
	hostnameKey  = "kubernetes.io/hostname"

	// readyzPath / podReadyzPath are the engine's two readiness surfaces.
	// /readyz answers "route client traffic here" (leader-only: a standby drains
	// itself); /pod-readyz answers "is this pod healthy" with no leadership
	// check, which is what the kubelet must ask in the leader-routing layout so a
	// hot standby can be Ready and a rolling update can progress past it.
	readyzPath    = "/readyz"
	podReadyzPath = "/pod-readyz"
	// haLeaderLabelEnv switches the engine into leader-label publishing (it is the
	// engine's OLIVARES_HA_LEADER_LABEL contract, cmd/olivares/haleaderlabel.go).
	haLeaderLabelEnv = "OLIVARES_HA_LEADER_LABEL"

	// configHashAnnotation drives the RECONFIGURE rollout: it records the
	// ConfigRef name (and, when resolvable, its content hash) on the pod
	// template so a change rolls the StatefulSet.
	configHashAnnotation = "ops.olivares.ai/config-hash"
	// configHashSourceAnnotation records, on the StatefulSet OBJECT (never on the pod
	// template, so writing it rolls nothing), the digest the current template
	// annotation corresponds to. It is what makes the hash-format fix
	// non-disruptive: on the first reconcile after an operator upgrade the live
	// template annotation is ADOPTED as-is and this records the new-format digest, so
	// pods roll only when the referenced configuration ACTUALLY changes — not merely
	// because the operator now computes the digest differently. Without it, upgrading
	// the operator would perturb (and, on the legacy HA readiness layout, wedge) a
	// workload whose owner changed nothing.
	configHashSourceAnnotation = "ops.olivares.ai/config-hash-source"
	// restoreAnnotation records the last RestoreFrom the controller acted on.
	restoreAnnotation = "ops.olivares.ai/restore-from"
)

// ControlPlaneReconciler reconciles a ControlPlane object.
type ControlPlaneReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// APIReader is the UNCACHED reader. The manager caches Secrets with their
	// payload stripped (an operator must not park every credential in the cluster in
	// its heap — cmd/manager cacheOptions), so the one place that genuinely needs the
	// content — folding a referenced Secret into the rollout config-hash — reads
	// through this. nil falls back to the cached client, which is what the
	// fake-client tests use.
	APIReader client.Reader
}

// reader returns the uncached reader when one is wired, else the cached client.
func (r *ControlPlaneReconciler) reader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// +kubebuilder:rbac:groups=ops.olivares.ai,resources=controlplanes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ops.olivares.ai,resources=controlplanes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ops.olivares.ai,resources=controlplanes/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=list;delete
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=core,resources=configmaps;secrets,verbs=get;list;watch
//
// Stage-2 (leader-routing layout). The manager reads pods to resolve the
// leader-route predicate, and creates the operand's own narrowly-scoped credential.
// `patch` on pods is NOT used by this reconciler: Kubernetes' privilege-escalation
// prevention refuses to create a Role granting rights the creator does not itself
// hold, and the operand Role grants `get,patch` on pods. Granting it here is
// strictly safer than the alternative (`escalate`/`bind`, which would let the
// manager mint ANY permission).
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=core,resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete

// Reconcile drives a ControlPlane toward its desired state: it ensures the
// StatefulSet, headless Service and (optionally) backup CronJob exist and match
// spec, then updates status (conditions + observedGeneration). It handles the
// INSTALL, UPGRADE (spec.Image change) and RECONFIGURE (spec.ConfigRef change)
// paths idempotently via controllerutil.CreateOrUpdate. A spec that fails
// structural validation (a combination the admission CEL rules also reject) is
// surfaced as Phase=Invalid and NOT materialized into a crashlooping workload.
func (r *ControlPlaneReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var cp opsv1alpha1.ControlPlane
	if err := r.Get(ctx, req.NamespacedName, &cp); err != nil {
		// Not found => owned objects are GC'd by ownerReferences; nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Defense-in-depth against a cluster with CEL admission disabled: refuse to
	// materialize a structurally-impossible spec (engine=postgres without a DSN, or
	// postgres HA without a shared audit key) and surface why instead of crashlooping.
	if err := cp.Spec.Validate(); err != nil {
		logger.Info("controlplane spec invalid; refusing to materialize a workload", "reason", err.Error())
		if nerr := r.neutralizeBackup(ctx, &cp); nerr != nil {
			return ctrl.Result{}, fmt.Errorf("neutralize backup cronjob of invalid spec: %w", nerr)
		}
		return ctrl.Result{}, r.markInvalid(ctx, &cp, err.Error())
	}

	// Resolve the config hash for the RECONFIGURE path. A change to the ConfigRef
	// name OR its content rolls the pod template. A read failure aborts the reconcile
	// rather than silently hashing a source away (which would roll the pods twice:
	// once when the read fails and once when it recovers).
	cfgHash, err := r.configHash(ctx, &cp)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("resolve config hash: %w", err)
	}

	// Observe the live pods ONCE per reconcile: they feed both the publisher's
	// authorization set (below) and the leader-route predicate in status. A failed
	// observation aborts the reconcile rather than degrading into a guess.
	pods, err := r.observePods(ctx, &cp)
	if err != nil {
		return ctrl.Result{}, err
	}

	// --- Operand RBAC (leader-routing layout only) ---
	// BEFORE the Service and the StatefulSet: the Role's resourceNames must already
	// cover a new ordinal when the StatefulSet scales up, or the new pod's very
	// first label publication is denied (design §B.1). It also spans every pod that
	// still exists, so a scaling-DOWN pod keeps the right to demote its own label
	// while it terminates.
	if err := r.reconcileLeaderRBAC(ctx, &cp, pods.names); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile leader rbac: %w", err)
	}

	// --- Services: the governing headless Service, plus the leader-selecting
	// client Service in the leader-routing layout. It must exist BEFORE the pod
	// template starts making standbys Ready, so client traffic has somewhere
	// correct to go the moment the layout changes. ---
	if err := r.reconcileService(ctx, &cp); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile service: %w", err)
	}

	// --- StatefulSet (mirrors core-statefulset.yaml; image=UPGRADE seam) ---
	sts, err := r.reconcileStatefulSet(ctx, &cp, cfgHash)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile statefulset: %w", err)
	}

	// --- Backup CronJob (real `dr backup`; only when spec.Backup set) ---
	if err := r.reconcileBackup(ctx, &cp); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile backup: %w", err)
	}

	// --- Status ---
	cls, err := r.updateStatus(ctx, &cp, sts, pods)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	logger.V(1).Info("reconciled", "name", cp.Name, "phase", cp.Status.Phase, "readyReplicas", cp.Status.ReadyReplicas)
	// Re-queue while a rollout is unfinished: StatefulSet status changes DO wake the
	// controller, but a rollout that stops changing produces no event at all — which
	// is exactly the wedge the progress deadline must catch. A periodic tick makes
	// stall classification independent of external events (design §C.4).
	//
	// A state that is STATIC by construction (the legacy readiness layout, a
	// migration waiting on a human) has no deadline to evaluate and will not change
	// on its own, so it polls slowly instead: the status writes are idempotent, but
	// re-examining a permanent condition twice a minute forever is pure noise.
	switch {
	case cls.ready:
		return ctrl.Result{}, nil
	case cls.haReadinessBlocked || migratingToLeaderRouting(&cp, sts):
		return ctrl.Result{RequeueAfter: staticStateRequeueInterval}, nil
	default:
		return ctrl.Result{RequeueAfter: progressRequeueInterval}, nil
	}
}

const (
	// progressRequeueInterval is how often an unconverged ControlPlane is re-examined
	// so the progress deadline can fire without a triggering cluster event.
	progressRequeueInterval = 30 * time.Second
	// staticStateRequeueInterval paces the states that only a human (or a spec edit,
	// which produces its own event) can change.
	staticStateRequeueInterval = 5 * time.Minute
)

// observePods reads the live pods of this ControlPlane: how many exist, how many
// are Ready, which of those publish the leader label, and which images they
// actually run. It is OBSERVED state — the input the desired pod template cannot
// provide — and a read failure degrades to "not observed" rather than to a wrong
// verdict (the classifier then falls back to StatefulSet counters).
func (r *ControlPlaneReconciler) observePods(ctx context.Context, cp *opsv1alpha1.ControlPlane) (podObservation, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(cp.Namespace), client.MatchingLabels(labelsFor(cp))); err != nil {
		// Never turn a failed observation into a verdict. The leader-route predicate
		// ("exactly one Ready pod publishes the leader label") is a ROUTING fact that
		// StatefulSet counters cannot stand in for: falling back to ReadyReplicas>0
		// would report a converged, Available control plane whose leader Service has no
		// endpoint at all. Fail the reconcile and retry instead.
		return podObservation{}, fmt.Errorf("list workload pods: %w", err)
	}
	obs := podObservation{observed: true}
	seen := map[string]bool{}
	for i := range pods.Items {
		p := &pods.Items[i]
		// Every pod that still EXISTS — terminating included — keeps its name in the
		// publisher's authorization set, so a pod draining during a scale-down can
		// still demote its own label instead of being 403'd on the way out.
		obs.names = append(obs.names, p.Name)
		// A terminating pod is already out of the Service endpoints; counting it
		// would report a phantom second leader during a rolling update.
		if p.DeletionTimestamp != nil {
			continue
		}
		obs.total++
		ready := podIsReady(p)
		if ready {
			obs.ready++
		}
		if p.Labels[haRoleLabelKey] == haRoleLeader {
			obs.leaders++
			if ready {
				obs.readyLeaders++
				obs.leaderPod = p.Name
			}
		}
		for _, c := range p.Spec.Containers {
			if c.Name == containerName && !seen[c.Image] {
				seen[c.Image] = true
				obs.images = append(obs.images, c.Image)
			}
		}
	}
	if obs.readyLeaders != 1 {
		// Never name a leader when zero or several claim the label: the status field
		// must not invite a client to pick one (the conditions say what is wrong).
		obs.leaderPod = ""
	}
	sort.Strings(obs.images)
	sort.Strings(obs.names)
	return obs, nil
}

// podIsReady reports the pod's Ready condition — the same signal the endpoint
// controllers use to decide whether it receives Service traffic.
func podIsReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// configHash computes a rollout-driving hash for spec.ConfigRef. If the
// referenced ConfigMap/Secret can be read its content is folded in (so an edit
// to the config rolls the pods); otherwise the ref NAME alone is hashed (so at
// least changing the ref rolls). Empty ConfigRef => empty hash (no annotation).
//
// It is DETERMINISTIC and covers BOTH objects (stage-2, design §A.5). Two
// bugs are fixed here, and both were live:
//   - ranging over a map fed the digest in Go's randomized iteration order, so the
//     SAME content could hash differently between reconciles — an annotation that
//     flip-flops rolls the StatefulSet forever;
//   - the Secret was read only when no same-named ConfigMap existed, while the pod
//     loads BOTH via envFrom — so a Secret-only edit rolled nothing.
//
// Fixing the format changes the annotation once for every ControlPlane that has a
// configRef, which rolls its pods one time. That is deliberate: the alternative is
// preserving an order-dependent hash, and this lands together with the layout that
// lets an HA rollout complete.
func (r *ControlPlaneReconciler) configHash(ctx context.Context, cp *opsv1alpha1.ControlPlane) (string, error) {
	if cp.Spec.ConfigRef == "" {
		return "", nil
	}
	h := sha256.New()
	writeHashField(h, "ref", []byte(cp.Spec.ConfigRef))

	// Content fold-in over BOTH sources, each with an explicit kind delimiter and
	// sorted keys. Only a confirmed NotFound means "this source does not exist": any
	// other read failure is returned, because treating a transient apiserver error
	// like an absent object would drop that source from the digest, change the
	// annotation, roll the pods — and roll them back when the read recovers.
	key := types.NamespacedName{Namespace: cp.Namespace, Name: cp.Spec.ConfigRef}
	var cm corev1.ConfigMap
	switch err := r.Get(ctx, key, &cm); {
	case err == nil:
		writeHashField(h, "configmap", nil)
		for _, k := range sortedKeys(cm.Data) {
			writeHashField(h, k, []byte(cm.Data[k]))
		}
	case !apierrors.IsNotFound(err):
		return "", fmt.Errorf("read ConfigMap %s: %w", key, err)
	}
	// Read the Secret through the UNCACHED reader: the manager's cache strips Secret
	// payloads on purpose, so a cached read would hash an empty body and a
	// credential rotation would silently roll nothing.
	var sec corev1.Secret
	switch err := r.reader().Get(ctx, key, &sec); {
	case err == nil:
		writeHashField(h, "secret", nil)
		for _, k := range sortedKeys(sec.Data) {
			writeHashField(h, k, sec.Data[k])
		}
	case !apierrors.IsNotFound(err):
		return "", fmt.Errorf("read Secret %s: %w", key, err)
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16], nil
}

// writeHashField feeds one length-delimited (name, value) pair into the digest, so
// no concatenation of adjacent keys/values can collide with a different split.
func writeHashField(h hash.Hash, name string, value []byte) {
	var lenBuf [8]byte
	binary.BigEndian.PutUint32(lenBuf[0:4], uint32(len(name)))
	binary.BigEndian.PutUint32(lenBuf[4:8], uint32(len(value)))
	_, _ = h.Write(lenBuf[:])
	_, _ = h.Write([]byte(name))
	_, _ = h.Write(value)
}

// sortedKeys returns a map's keys in deterministic order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func labelsFor(cp *opsv1alpha1.ControlPlane) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "olivares",
		"app.kubernetes.io/instance":   cp.Name,
		"app.kubernetes.io/component":  "core",
		"app.kubernetes.io/managed-by": "olivares-operator",
	}
}

// backupLabelsFor narrows labelsFor to the backup workload. The four generic
// labels alone do not identify a backup Job — they are the operand's labels —
// so anything that SELECTS Jobs to act on must use this, and must still verify
// ownership before deleting (labels are not proof; see neutralizeBackup).
func backupLabelsFor(cp *opsv1alpha1.ControlPlane) map[string]string {
	l := labelsFor(cp)
	l["app.kubernetes.io/component"] = "backup"
	return l
}

func (r *ControlPlaneReconciler) reconcileService(ctx context.Context, cp *opsv1alpha1.ControlPlane) error {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: cp.Name, Namespace: cp.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		svc.Labels = labelsFor(cp)
		// Headless: the core is a single stateful writer (StatefulSet), so a
		// headless Service gives it a stable per-pod DNS identity, mirroring the
		// chart's serviceName wiring. It stays the GOVERNING Service in every
		// layout — the leader-routing layout ADDS `<name>-leader` for client
		// traffic rather than repurposing this one (repurposing it would change the
		// StatefulSet's immutable serviceName and force a recreate).
		svc.Spec.ClusterIP = corev1.ClusterIPNone
		svc.Spec.Selector = labelsFor(cp)
		svc.Spec.Ports = []corev1.ServicePort{
			{Name: "https", Port: httpsPort, TargetPort: intstr.FromString("https"), Protocol: corev1.ProtocolTCP},
			{Name: "grpc", Port: grpcPort, TargetPort: intstr.FromString("grpc"), Protocol: corev1.ProtocolTCP},
		}
		return controllerutil.SetControllerReference(cp, svc, r.Scheme)
	})
	if err != nil {
		return err
	}
	return r.reconcileLeaderService(ctx, cp)
}

// leaderServiceName is the CLIENT endpoint in the leader-routing layout: a normal
// ClusterIP Service whose selector adds the leader role label, so the ordinary
// endpoint controllers resolve it to the single Ready pod that currently holds the
// Postgres election lock.
func leaderServiceName(cp *opsv1alpha1.ControlPlane) string { return cp.Name + "-leader" }

// leaderPublisherName is the ServiceAccount/Role/RoleBinding trio that lets the
// engine label its OWN pod (see reconcileLeaderRBAC).
func leaderPublisherName(cp *opsv1alpha1.ControlPlane) string { return cp.Name + "-leader-publisher" }

// haLeaderRouting reports the leader-routing (Patroni-style) HA layout:
// active-passive HA that has EXPLICITLY opted in via spec.haRouting=LeaderRouting.
// It is never inferred: the layout requires an engine image that serves
// /pod-readyz and publishes the role label, which the operator cannot verify from
// an image reference, and it changes where clients must connect.
func haLeaderRouting(cp *opsv1alpha1.ControlPlane) bool {
	return isHA(cp) && cp.Spec.HARouting == opsv1alpha1.HARoutingLeader
}

// leaderCutoverAnnotation is the administrator's acknowledgement that clients have
// been moved to the `<name>-leader` Service, which is the ONLY thing the operator
// cannot verify for itself before it changes where traffic lands.
const leaderCutoverAnnotation = "ops.olivares.ai/ha-leader-cutover"

// leaderCutoverAcknowledged reports whether that acknowledgement is present.
func leaderCutoverAcknowledged(cp *opsv1alpha1.ControlPlane) bool {
	return strings.EqualFold(strings.TrimSpace(cp.Annotations[leaderCutoverAnnotation]), "acknowledged")
}

// migratingToLeaderRouting reports the PREPARE phase of the layout migration: an
// EXISTING HA StatefulSet is still on the legacy leader-only readiness layout, the
// spec now asks for leader routing, and the administrator has not yet acknowledged
// the client cut-over.
//
// Why this phase exists at all (design §B.1 "Existing-StatefulSet migration"):
// flipping the readiness probe in one step is not a zero-downtime migration in
// either ordering. The old leader's engine cannot publish the new label, so the
// leader Service starts EMPTY; meanwhile the first replaced standby becomes
// pod-Ready and joins the legacy governing Service, where clients that have not
// moved reach it and get 503s. So the operator prepares the destination first —
// the leader Service and the publisher RBAC exist, the pod template does not
// change — and reports HALeaderServiceMigrationRequired until the operator
// acknowledges the cut-over. A FRESH install has no live StatefulSet and skips
// this entirely.
func migratingToLeaderRouting(cp *opsv1alpha1.ControlPlane, sts *appsv1.StatefulSet) bool {
	if !haLeaderRouting(cp) || leaderCutoverAcknowledged(cp) {
		return false
	}
	if sts == nil {
		return false // fresh install: create it in the split shape directly
	}
	return livePodTemplateIsLegacy(sts)
}

// livePodTemplateIsLegacy reports whether the LIVE StatefulSet still probes the
// leader-only readiness path (i.e. it has not been converted to the leader-routing
// layout yet).
func livePodTemplateIsLegacy(sts *appsv1.StatefulSet) bool {
	for _, c := range sts.Spec.Template.Spec.Containers {
		if c.Name != containerName || c.ReadinessProbe == nil || c.ReadinessProbe.HTTPGet == nil {
			continue
		}
		return c.ReadinessProbe.HTTPGet.Path != podReadyzPath
	}
	return false
}

// reconcileLeaderService creates (or removes) the leader-selecting client Service.
// In the legacy layout it must NOT exist: /readyz already drains standbys there, so
// a second Service would only add a confusing empty endpoint list.
func (r *ControlPlaneReconciler) reconcileLeaderService(ctx context.Context, cp *opsv1alpha1.ControlPlane) error {
	name := leaderServiceName(cp)
	if !haLeaderRouting(cp) {
		return r.deleteIfExists(ctx, cp, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cp.Namespace}})
	}
	selector := labelsFor(cp)
	selector[haRoleLabelKey] = haRoleLeader
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cp.Namespace}}
	if err := r.assertOwnable(ctx, cp, svc.DeepCopy()); err != nil {
		return err
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		svc.Labels = labelsFor(cp)
		svc.Spec.Type = corev1.ServiceTypeClusterIP
		// Only the POSITIVE leader value is selected: a standby publishes
		// role=standby (not "no label"), so an unlabeled pod — one whose engine
		// predates the layout, or whose publication failed — is never routed to.
		svc.Spec.Selector = selector
		svc.Spec.Ports = []corev1.ServicePort{
			{Name: "https", Port: httpsPort, TargetPort: intstr.FromString("https"), Protocol: corev1.ProtocolTCP},
			{Name: "grpc", Port: grpcPort, TargetPort: intstr.FromString("grpc"), Protocol: corev1.ProtocolTCP},
		}
		return controllerutil.SetControllerReference(cp, svc, r.Scheme)
	})
	return err
}

// reconcileLeaderRBAC provisions the operand's OWN Kubernetes credential for the
// leader-routing layout: a dedicated ServiceAccount plus a namespaced Role that can
// `get,patch` exactly the pods of THIS StatefulSet, and nothing else.
//
// This is a real, deliberate privilege grant — the engine has no Kubernetes API
// access in any other layout — so it is scoped as tightly as RBAC allows:
// resourceNames pins the pod names, the verbs cannot create or delete, and the
// credential is mounted only into HA pods. It is NOT true self-only access:
// Kubernetes RBAC has no variable for "the caller's own pod", and all replicas
// share one ServiceAccount, so a compromised standby could mislabel its peers —
// a bounded denial-of-service inside this StatefulSet. It can never make itself
// the writer: the Postgres advisory lock is the sole write authority and every
// application request re-checks it (design §B.1 "Exact RBAC and blast radius").
//
// resourceNames covers the UNION of desired and currently-observed replicas, and
// is reconciled BEFORE the StatefulSet: on scale-up the new pod can publish from
// its first breath, and on scale-down a terminating pod can still demote itself.
func (r *ControlPlaneReconciler) reconcileLeaderRBAC(ctx context.Context, cp *opsv1alpha1.ControlPlane, livePods []string) error {
	name := leaderPublisherName(cp)
	if !haLeaderRouting(cp) {
		// Revoke on revert: the objects are owned by the ControlPlane (so a delete of
		// the CR collects them), but switching back to Legacy must drop the operand's
		// API credential immediately rather than leaving it dormant.
		if err := r.deleteIfExists(ctx, cp, &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cp.Namespace}}); err != nil {
			return err
		}
		if err := r.deleteIfExists(ctx, cp, &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cp.Namespace}}); err != nil {
			return err
		}
		return r.deleteIfExists(ctx, cp, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cp.Namespace}})
	}

	// Never adopt a same-named credential this ControlPlane does not own: taking over
	// (and rewriting) another workload's ServiceAccount/Role/RoleBinding would be a
	// privilege incident, not a reconcile.
	for _, obj := range []client.Object{
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cp.Namespace}},
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cp.Namespace}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cp.Namespace}},
	} {
		if err := r.assertOwnable(ctx, cp, obj); err != nil {
			return err
		}
	}

	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cp.Namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, sa, func() error {
		sa.Labels = labelsFor(cp)
		return controllerutil.SetControllerReference(cp, sa, r.Scheme)
	}); err != nil {
		return fmt.Errorf("leader publisher serviceaccount: %w", err)
	}

	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cp.Namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, role, func() error {
		role.Labels = labelsFor(cp)
		role.Rules = []rbacv1.PolicyRule{{
			APIGroups:     []string{""},
			Resources:     []string{"pods"},
			ResourceNames: leaderPublisherPodNames(cp, livePods),
			Verbs:         []string{"get", "patch"},
		}}
		return controllerutil.SetControllerReference(cp, role, r.Scheme)
	}); err != nil {
		return fmt.Errorf("leader publisher role: %w", err)
	}

	// RoleBinding.roleRef is IMMUTABLE. If an owned binding drifted to a different
	// role, patching it fails on every reconcile forever, so the repair is to delete
	// and recreate it (the ownership guard above already refused to touch a binding
	// this ControlPlane does not own).
	var liveRB rbacv1.RoleBinding
	switch err := r.Get(ctx, types.NamespacedName{Namespace: cp.Namespace, Name: name}, &liveRB); {
	case err == nil:
		if liveRB.RoleRef.Kind != "Role" || liveRB.RoleRef.Name != name || liveRB.RoleRef.APIGroup != rbacv1.GroupName {
			log.FromContext(ctx).Info("recreating the leader-publisher RoleBinding: roleRef is immutable and drifted",
				"name", name, "roleRef", liveRB.RoleRef)
			if err := client.IgnoreNotFound(r.Delete(ctx, &liveRB)); err != nil {
				return fmt.Errorf("delete drifted leader publisher rolebinding: %w", err)
			}
		}
	case !apierrors.IsNotFound(err):
		return fmt.Errorf("read leader publisher rolebinding: %w", err)
	}

	rb := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cp.Namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, rb, func() error {
		rb.Labels = labelsFor(cp)
		rb.Subjects = []rbacv1.Subject{{Kind: "ServiceAccount", Name: name, Namespace: cp.Namespace}}
		rb.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name}
		return controllerutil.SetControllerReference(cp, rb, r.Scheme)
	}); err != nil {
		return fmt.Errorf("leader publisher rolebinding: %w", err)
	}
	return nil
}

// leaderPublisherPodNames enumerates the pod names the operand may patch: the union
// of the DESIRED ordinals and the pods that actually EXIST right now (terminating
// ones included). Deriving it from a replica counter alone is not safe in either
// direction — a scale-up needs the new ordinal authorized before the pod starts,
// and a scale-down must keep the draining pod authorized until it is really gone,
// which the counter can report too early.
func leaderPublisherPodNames(cp *opsv1alpha1.ControlPlane, livePods []string) []string {
	desired, _ := effectiveReplicas(cp)
	set := map[string]bool{}
	for i := int32(0); i < desired; i++ {
		set[fmt.Sprintf("%s-%d", cp.Name, i)] = true
	}
	for _, n := range livePods {
		set[n] = true
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// deleteIfExists removes an object the current spec no longer wants — but ONLY if
// this ControlPlane actually owns it. A same-named object the operator did not
// create (an administrator's hand-made Service, another workload's RBAC) is left
// strictly alone: reverting one ControlPlane's HA layout must never delete someone
// else's resources, and pre-staging the leader Service by hand is a supported
// migration step. Tolerates a concurrent delete.
func (r *ControlPlaneReconciler) deleteIfExists(ctx context.Context, cp *opsv1alpha1.ControlPlane, obj client.Object) error {
	err := r.Get(ctx, types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()}, obj)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !metav1.IsControlledBy(obj, cp) {
		log.FromContext(ctx).Info("leaving a same-named object that this ControlPlane does not own",
			"kind", fmt.Sprintf("%T", obj), "name", obj.GetName(), "namespace", obj.GetNamespace())
		return nil
	}
	return client.IgnoreNotFound(r.Delete(ctx, obj))
}

// assertOwnable fails the reconcile when a same-named object exists that this
// ControlPlane does NOT control. controllerutil.CreateOrUpdate would otherwise
// ADOPT an unowned object — silently taking over (and rewriting) a Service or
// RoleBinding somebody else provisioned. A collision is an operator-visible error,
// never a silent mutation.
func (r *ControlPlaneReconciler) assertOwnable(ctx context.Context, cp *opsv1alpha1.ControlPlane, obj client.Object) error {
	err := r.Get(ctx, types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()}, obj)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(obj.GetOwnerReferences()) == 0 || !metav1.IsControlledBy(obj, cp) {
		return fmt.Errorf("%T %s/%s already exists and is not owned by ControlPlane %s: refusing to adopt or overwrite it",
			obj, obj.GetNamespace(), obj.GetName(), cp.Name)
	}
	return nil
}

// effectiveReplicas returns the replica count the operator will actually apply
// and whether the requested count was clamped for safety. engine=sqlite is
// single-pod: each pod holds its own RWO data volume and mints its own audit
// signing key, so >1 sqlite replicas would fork the audit ledger. The Helm chart
// rejects this combination outright (_helpers.tpl); the operator clamps to 1 and
// surfaces a Degraded condition, mirroring that safety contract without wedging
// the rollout. Postgres HA (>1) is the supported path (a shared audit key is
// required by Validate / the CRD's CEL rules).
func effectiveReplicas(cp *opsv1alpha1.ControlPlane) (replicas int32, clamped bool) {
	engine := engineOf(cp)
	if engine != opsv1alpha1.EnginePostgres && cp.Spec.Replicas > 1 {
		return 1, true
	}
	return cp.Spec.Replicas, false
}

func engineOf(cp *opsv1alpha1.ControlPlane) opsv1alpha1.Engine {
	if cp.Spec.Engine == "" {
		return opsv1alpha1.EngineSQLite
	}
	return cp.Spec.Engine
}

// isHA reports active-passive HA: postgres with >1 effective replica. HA changes
// the pod-management policy (standbys are intentionally not Ready, so OrderedReady
// would wedge the scale-up).
func isHA(cp *opsv1alpha1.ControlPlane) bool {
	replicas, _ := effectiveReplicas(cp)
	return engineOf(cp) == opsv1alpha1.EnginePostgres && replicas > 1
}

// haPolicyMismatch reports the silent-wedge hazard: the spec asks for HA
// (podManagementPolicy=Parallel) but the LIVE StatefulSet was created OrderedReady,
// an immutable field the controller cannot patch. Reachable by scaling an existing
// single-writer ControlPlane into HA (Replicas 1→N + audit key, or sqlite→postgres).
func haPolicyMismatch(cp *opsv1alpha1.ControlPlane, sts *appsv1.StatefulSet) bool {
	return isHA(cp) &&
		sts.Spec.PodManagementPolicy != "" &&
		sts.Spec.PodManagementPolicy != appsv1.ParallelPodManagement
}

// dataVolumeClaimTemplate builds the per-replica data PVC template from
// spec.persistence (mirroring the chart's core.persistence), defaulting to
// 8Gi/RWO/cluster-default StorageClass. Sizes are pre-validated by
// ControlPlaneSpec.Validate, so resource.MustParse here cannot panic.
func dataVolumeClaimTemplate(cp *opsv1alpha1.ControlPlane) corev1.PersistentVolumeClaim {
	size := "8Gi"
	accessModes := []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	var storageClass string
	if p := cp.Spec.Persistence; p != nil {
		if s := strings.TrimSpace(p.Size); s != "" {
			size = s
		}
		if len(p.AccessModes) > 0 {
			accessModes = p.AccessModes
		}
		storageClass = strings.TrimSpace(p.StorageClass)
	}
	pvc := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: dataVolumeName, Labels: labelsFor(cp)},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: accessModes,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)},
			},
		},
	}
	// Mirror the chart: "-" disables dynamic provisioning (empty storageClassName,
	// for a pre-bound PV); any other non-empty value pins the class; "" leaves the
	// cluster default.
	switch {
	case storageClass == "-":
		empty := ""
		pvc.Spec.StorageClassName = &empty
	case storageClass != "":
		pvc.Spec.StorageClassName = &storageClass
	}
	return pvc
}

// appliedConfigHash decides which value the pod-template config-hash annotation
// carries, and it is what keeps the digest-format fix from rolling workloads
// nobody asked to roll.
//
// The rule: the template annotation changes only when the DIGEST OF THE REFERENCED
// CONFIGURATION changes, never merely because the operator now computes digests
// differently. The StatefulSet object (not its template) records which digest the
// current template value corresponds to, so:
//
//   - fresh StatefulSet: the template gets the new digest;
//   - first reconcile after an operator upgrade (no source annotation yet): the live
//     template value is ADOPTED unchanged — no rollout;
//   - later reconciles: the template moves to the new digest exactly when the
//     recorded source digest differs, i.e. when the config really changed.
//
// It also mutates sts.Annotations, which is safe inside the CreateOrUpdate mutate
// function: object annotations are not part of the pod template, so writing one
// never triggers a rollout.
func appliedConfigHash(sts, live *appsv1.StatefulSet, cfgHash string) string {
	if sts.Annotations == nil {
		sts.Annotations = map[string]string{}
	}
	defer func() { sts.Annotations[configHashSourceAnnotation] = cfgHash }()

	if cfgHash == "" || live == nil {
		// No configRef, or a StatefulSet being created: nothing to preserve.
		return cfgHash
	}
	applied := live.Spec.Template.Annotations[configHashAnnotation]
	if applied == "" {
		return cfgHash
	}
	if prev, ok := live.Annotations[configHashSourceAnnotation]; !ok {
		return applied // adopt: an older operator wrote it; do not roll for the format change
	} else if prev == cfgHash {
		return applied // the referenced configuration has not changed
	}
	return cfgHash
}

func (r *ControlPlaneReconciler) reconcileStatefulSet(ctx context.Context, cp *opsv1alpha1.ControlPlane, cfgHash string) (*appsv1.StatefulSet, error) {
	replicas, _ := effectiveReplicas(cp)
	engine := engineOf(cp)

	// Read the LIVE StatefulSet first: two decisions depend on what is already
	// running rather than on what we are about to write — whether the HA layout
	// migration is still in its prepare phase, and whether the config-hash
	// annotation may be rewritten. Both must be evaluated against the pre-update
	// object, so they are resolved here and passed into the mutate function.
	var live *appsv1.StatefulSet
	var existing appsv1.StatefulSet
	switch err := r.Get(ctx, types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name}, &existing); {
	case err == nil:
		live = &existing
	case !apierrors.IsNotFound(err):
		return nil, err
	}
	split := haLeaderRouting(cp) && !migratingToLeaderRouting(cp, live)

	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: cp.Name, Namespace: cp.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sts, func() error {
		sts.Labels = labelsFor(cp)
		sts.Spec.ServiceName = cp.Name
		sts.Spec.Replicas = &replicas
		sts.Spec.Selector = &metav1.LabelSelector{MatchLabels: labelsFor(cp)}
		sts.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType}

		// podManagementPolicy is IMMUTABLE after creation, so set it only on first
		// create and preserve it on update. HA needs Parallel: the standbys never
		// become Ready (/readyz drains the non-leader), so OrderedReady would wait
		// forever for pod-1 to be Ready before creating pod-2 and wedge the scale-up.
		if sts.Spec.PodManagementPolicy == "" {
			if isHA(cp) {
				sts.Spec.PodManagementPolicy = appsv1.ParallelPodManagement
			} else {
				sts.Spec.PodManagementPolicy = appsv1.OrderedReadyPodManagement
			}
		}

		podAnnotations := map[string]string{}
		if applied := appliedConfigHash(sts, live, cfgHash); applied != "" {
			podAnnotations[configHashAnnotation] = applied
		}
		if cp.Spec.RestoreFrom != "" {
			// Surface the restore request on the template so the seam is visible.
			podAnnotations[restoreAnnotation] = cp.Spec.RestoreFrom
		}

		sts.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labelsFor(cp), Annotations: podAnnotations},
			// The pod template keeps the LEGACY shape while the layout migration is in
			// its prepare phase (see migratingToLeaderRouting): the destination Service
			// and RBAC exist, but nothing that changes where traffic lands moves until
			// the client cut-over is acknowledged.
			Spec: r.podSpec(cp, engine, split),
		}

		// volumeClaimTemplate for the data dir (mirrors core.persistence). Immutable
		// after creation; CreateOrUpdate only sets it on first create. Kept for ALL
		// modes (including HA) so the operand always has a stable data-<name>-0 PVC
		// the backup Job can mount.
		if len(sts.Spec.VolumeClaimTemplates) == 0 {
			sts.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{dataVolumeClaimTemplate(cp)}
		}
		return controllerutil.SetControllerReference(cp, sts, r.Scheme)
	})
	if err != nil {
		return nil, err
	}
	return sts, nil
}

// podSpec builds the hardened pod spec mirroring the Helm core-statefulset:
// non-root uid 65532, readOnlyRootFilesystem, all caps dropped, the listen ports,
// the engine/replica/config wiring, the postgres DSN, the shared signing keys and
// the compute Resources from spec.
func (r *ControlPlaneReconciler) podSpec(cp *opsv1alpha1.ControlPlane, engine opsv1alpha1.Engine, split bool) corev1.PodSpec {
	noPriv := false
	roRoot := true
	nonRoot := true
	uid := runAsUser

	args := []string{
		"serve",
		fmt.Sprintf("--listen=:%d", httpsPort),
		fmt.Sprintf("--grpc-listen=:%d", grpcPort),
		"--data-dir=" + dataMountPath,
	}
	env := []corev1.EnvVar{
		{Name: "OLIVARES_DATA_DIR", Value: dataMountPath},
		{Name: "OLIVARES_ENGINE", Value: string(engine)},
	}

	if engine == opsv1alpha1.EnginePostgres && cp.Spec.Postgres != nil {
		// The engine has NO DSN env fallback, so wire --dsn=$(OLIVARES_DSN) and
		// inject OLIVARES_DSN from the Secret. $(VAR) is expanded by Kubernetes.
		args = append(args, "--engine=postgres", "--dsn=$("+dsnEnvName+")")
		env = append(env, dsnEnvVar(cp))
		if adminDSNWired(cp) {
			args = append(args, "--admin-dsn=$("+adminDSNEnvName+")")
			env = append(env, adminDSNEnvVar(cp))
		}
	} else if engine == opsv1alpha1.EnginePostgres {
		// Validate() guarantees Postgres != nil for postgres; this branch is an
		// unreachable safety net (still pass --engine so the engine fails loudly
		// rather than silently booting sqlite).
		args = append(args, "--engine=postgres")
	}

	volumes := []corev1.Volume{
		{Name: tmpVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	mounts := []corev1.VolumeMount{
		{Name: dataVolumeName, MountPath: dataMountPath},
		{Name: tmpVolumeName, MountPath: "/tmp"},
	}

	// Shared audit signing key (HA): mounted read-only into EVERY replica so the
	// audit hash-chain does not fork at failover (fail-closed in the engine).
	if cp.Spec.AuditSigningKeySecret != "" {
		env = append(env, corev1.EnvVar{Name: auditKeyEnv, Value: auditKeyMountPath + "/" + auditKeyFileName})
		mounts = append(mounts, corev1.VolumeMount{Name: auditKeyVolumeName, MountPath: auditKeyMountPath, ReadOnly: true})
		volumes = append(volumes, secretVolume(auditKeyVolumeName, cp.Spec.AuditSigningKeySecret, keySecretMode, nil, false))
	}
	// Optional catalog (artifact) signing key. Defaults to the audit Secret (the
	// chart's convention); the engine mints a per-node key if the projected item is
	// absent, so the projection is optional and a single-key audit Secret boots.
	if catalogSecret := catalogSecretName(cp); catalogSecret != "" {
		env = append(env, corev1.EnvVar{Name: catalogKeyEnv, Value: catalogKeyMountPath + "/" + catalogKeyFileName})
		mounts = append(mounts, corev1.VolumeMount{Name: catalogKeyVolumeName, MountPath: catalogKeyMountPath, ReadOnly: true})
		volumes = append(volumes, secretVolume(catalogKeyVolumeName, catalogSecret, keySecretMode,
			[]corev1.KeyToPath{{Key: catalogKeyFileName, Path: catalogKeyFileName}}, true))
	}

	c := corev1.Container{
		Name:  containerName,
		Image: cp.Spec.Image,
		Args:  args,
		Ports: []corev1.ContainerPort{
			{Name: "https", ContainerPort: httpsPort, Protocol: corev1.ProtocolTCP},
			{Name: "grpc", ContainerPort: grpcPort, Protocol: corev1.ProtocolTCP},
		},
		Env:       env,
		Resources: coreResources(cp),
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: &noPriv,
			ReadOnlyRootFilesystem:   &roRoot,
			RunAsNonRoot:             &nonRoot,
			RunAsUser:                &uid,
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
				Path: "/livez", Port: intstr.FromString("https"), Scheme: corev1.URISchemeHTTPS,
			}},
			InitialDelaySeconds: 5, PeriodSeconds: 15, TimeoutSeconds: 3, FailureThreshold: 6,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
				// the readiness path is the LAYOUT decision. /readyz is
				// leader-only (a standby fails it on purpose) — correct when the
				// headless Service is the client endpoint, but it makes ReadyReplicas
				// unreachable and wedges a rolling update. The leader-routing layout
				// probes /pod-readyz (pod health, leader-agnostic) and moves client
				// routing to the leader-selecting Service instead.
				Path: readinessPath(split), Port: intstr.FromString("https"), Scheme: corev1.URISchemeHTTPS,
			}},
			InitialDelaySeconds: 5, PeriodSeconds: 10, TimeoutSeconds: 3, FailureThreshold: 3,
		},
		VolumeMounts: mounts,
	}

	// RECONFIGURE: load extra (non-DSN) config via envFrom from the referenced
	// object. The postgres DSN is wired explicitly above, NOT here (envFrom env is
	// not consulted by the engine for the DSN).
	if cp.Spec.ConfigRef != "" {
		c.EnvFrom = []corev1.EnvFromSource{
			{ConfigMapRef: &corev1.ConfigMapEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: cp.Spec.ConfigRef},
				Optional:             ptrBool(true),
			}},
			{SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: cp.Spec.ConfigRef},
				Optional:             ptrBool(true),
			}},
		}
	}

	fsGroup := runAsUser
	spec := corev1.PodSpec{
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot:   &nonRoot,
			RunAsUser:      &uid,
			RunAsGroup:     &uid,
			FSGroup:        &fsGroup,
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		Containers: []corev1.Container{c},
		Volumes:    volumes,
	}
	if split {
		// The leader-routing layout is the ONLY one in which the engine talks to the
		// Kubernetes API: it labels its own pod so the leader Service can select it.
		// That needs an identity (downward API), the narrowly-scoped ServiceAccount
		// reconcileLeaderRBAC provisions, and its projected token mounted.
		spec.ServiceAccountName = leaderPublisherName(cp)
		spec.AutomountServiceAccountToken = ptrBool(true)
		spec.Containers[0].Env = append(spec.Containers[0].Env,
			corev1.EnvVar{Name: haLeaderLabelEnv, Value: "1"},
			corev1.EnvVar{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
			corev1.EnvVar{Name: "POD_NAMESPACE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}},
		)
	}
	return spec
}

// readinessPath is the container readinessProbe path for the ControlPlane's HA
// layout: pod health in the leader-routing layout, leader-only drain otherwise.
func readinessPath(split bool) string {
	if split {
		return podReadyzPath
	}
	return readyzPath
}

// reconcileBackup ensures the backup CronJob exists/matches spec.Backup, or is
// removed when spec.Backup is nil. The CronJob runs the REAL `olivares dr backup`
// (shell-free — the engine image is distroless) over the operand's data PVC,
// sealing the signing keys under the KEK and writing a verifiable bundle to a
// destination PVC. For engine=postgres a pg_dump initContainer produces the store
// snapshot the bundle wraps.
func (r *ControlPlaneReconciler) reconcileBackup(ctx context.Context, cp *opsv1alpha1.ControlPlane) error {
	name := cp.Name + "-backup"

	if cp.Spec.Backup == nil {
		// Garbage-collect a previously-created CronJob if backup was removed. The
		// destination PVC is intentionally NOT deleted (DR data must survive).
		var existing batchv1.CronJob
		err := r.Get(ctx, types.NamespacedName{Namespace: cp.Namespace, Name: name}, &existing)
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		return client.IgnoreNotFound(r.Delete(ctx, &existing))
	}

	b := cp.Spec.Backup

	// Resolve the destination PVC: an operator-provided existingClaim, or one the
	// controller creates (un-owned, so it survives a ControlPlane delete).
	destClaim := strings.TrimSpace(b.Destination.ExistingClaim)
	if destClaim == "" {
		destClaim = cp.Name + "-backups"
		if err := r.ensureBackupPVC(ctx, cp, destClaim); err != nil {
			return fmt.Errorf("ensure backup destination PVC: %w", err)
		}
	}

	history := b.SuccessfulJobsHistoryLimit
	if history < 1 {
		history = 3
	}

	cj := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cp.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cj, func() error {
		cj.Labels = labelsFor(cp)
		cj.Spec.Schedule = b.Schedule
		cj.Spec.SuccessfulJobsHistoryLimit = &history
		cj.Spec.ConcurrencyPolicy = batchv1.ForbidConcurrent
		// EXPLICIT unsuspend, never "leave it at the default": neutralizeBackup
		// suspends this object when the spec goes invalid, and CreateOrUpdate
		// mutates the object it just fetched — so a suspend=true written then
		// would survive here forever and the operand would silently have no
		// scheduled backup after the spec was repaired.
		resume := false
		cj.Spec.Suspend = &resume
		// Jobs templated from this CronJob get the backup labels, so anything
		// selecting them (neutralizeBackup) matches the real objects rather than
		// the operand at large. Pod labels live in JobTemplate.Spec.Template and
		// are NOT these: this is the JobTemplate's own metadata (its embedded
		// ObjectMeta, promoted here).
		cj.Spec.JobTemplate.Labels = backupLabelsFor(cp)
		spec, serr := r.backupJobSpec(cp, b, destClaim)
		if serr != nil {
			return serr
		}
		cj.Spec.JobTemplate.Spec = spec
		return controllerutil.SetControllerReference(cp, cj, r.Scheme)
	})
	return err
}

// ensureBackupPVC creates the destination PVC ONCE if it does not exist. It is
// deliberately NOT owned by the ControlPlane: deleting the ControlPlane must not
// garbage-collect the DR bundles. Once created it is never reconciled (its spec
// is immutable apart from a manual expand).
func (r *ControlPlaneReconciler) ensureBackupPVC(ctx context.Context, cp *opsv1alpha1.ControlPlane, name string) error {
	var existing corev1.PersistentVolumeClaim
	err := r.Get(ctx, types.NamespacedName{Namespace: cp.Namespace, Name: name}, &existing)
	if err == nil {
		return nil // already exists; leave it untouched (DR data)
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	size := strings.TrimSpace(cp.Spec.Backup.Destination.Size)
	if size == "" {
		size = "16Gi"
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cp.Namespace,
			Labels:    labelsFor(cp),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)},
			},
		},
	}
	if sc := strings.TrimSpace(cp.Spec.Backup.Destination.StorageClass); sc != "" {
		pvc.Spec.StorageClassName = &sc
	}
	if err := r.Create(ctx, pvc); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// backupJobSpec builds the Job that runs `olivares dr backup`.
func (r *ControlPlaneReconciler) backupJobSpec(cp *opsv1alpha1.ControlPlane, b *opsv1alpha1.BackupSpec, destClaim string) (batchv1.JobSpec, error) {
	engine := engineOf(cp)
	nonRoot := true
	uid := runAsUser

	mounts := []corev1.VolumeMount{
		// The operand's data dir: signing keys (+ the sqlite store) to back up.
		// RW (not RO): for postgres the engine's manifest-boot may mint the
		// artifact keys; RWO permits this co-located mount alongside pod-0.
		{Name: dataVolumeName, MountPath: dataMountPath},
		{Name: kekVolumeName, MountPath: kekMountPath, ReadOnly: true},
		{Name: backupsVolumeName, MountPath: backupsMountPath},
		{Name: workVolumeName, MountPath: workMountPath},
		{Name: tmpVolumeName, MountPath: "/tmp"},
	}
	volumes := []corev1.Volume{
		{Name: dataVolumeName, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
			ClaimName: dataVolumeName + "-" + cp.Name + "-0",
		}}},
		secretVolume(kekVolumeName, b.KEKSecret, kekSecretMode, nil, false),
		{Name: backupsVolumeName, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
			ClaimName: destClaim,
		}}},
		{Name: workVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: tmpVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}

	env := []corev1.EnvVar{
		{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
		{Name: "HOME", Value: "/tmp"},
		{Name: "OLIVARES_DATA_DIR", Value: dataMountPath},
	}
	if engine == opsv1alpha1.EnginePostgres && cp.Spec.Postgres != nil {
		env = append(env, dsnEnvVar(cp))
		if adminDSNWired(cp) {
			env = append(env, adminDSNEnvVar(cp))
		}
	}
	// In HA the operand data dir holds NO signing key (it lives in the shared
	// Secret), so the backup's manifest signer must resolve the SAME key from the
	// mounted Secret (externalKeyCustodyConfigured keys off this env).
	if cp.Spec.AuditSigningKeySecret != "" {
		env = append(env, corev1.EnvVar{Name: auditKeyEnv, Value: auditKeyMountPath + "/" + auditKeyFileName})
		mounts = append(mounts, corev1.VolumeMount{Name: auditKeyVolumeName, MountPath: auditKeyMountPath, ReadOnly: true})
		volumes = append(volumes, secretVolume(auditKeyVolumeName, cp.Spec.AuditSigningKeySecret, keySecretMode, nil, false))
	}
	// Mirror the core pod's catalog-key wiring: the DR backup boots a full engine to
	// build the manifest, and that boot resolves a catalog signing key. Without this
	// it would MINT a throwaway per-node catalog key into the (RW-mounted) operand
	// PVC and escrow the wrong key. Point it at the same shared/defaulted Secret.
	if catalogSecret := catalogSecretName(cp); catalogSecret != "" {
		env = append(env, corev1.EnvVar{Name: catalogKeyEnv, Value: catalogKeyMountPath + "/" + catalogKeyFileName})
		mounts = append(mounts, corev1.VolumeMount{Name: catalogKeyVolumeName, MountPath: catalogKeyMountPath, ReadOnly: true})
		volumes = append(volumes, secretVolume(catalogKeyVolumeName, catalogSecret, keySecretMode,
			[]corev1.KeyToPath{{Key: catalogKeyFileName, Path: catalogKeyFileName}}, true))
	}

	hardened := &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptrBool(false),
		ReadOnlyRootFilesystem:   ptrBool(true),
		RunAsNonRoot:             &nonRoot,
		RunAsUser:                &uid,
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}

	var initContainers []corev1.Container
	if engine == opsv1alpha1.EnginePostgres {
		// The distroless engine image has no pg_dump; a postgres-client image
		// produces the consistent custom-format dump the engine then bundles via
		// --snapshot-file.
		pgImage := strings.TrimSpace(b.PgClientImage)
		if pgImage == "" {
			pgImage = "postgres:16-alpine"
		}
		pgEnv := []corev1.EnvVar{
			{Name: "HOME", Value: "/tmp"},
			{Name: "PGCONNECT_TIMEOUT", Value: "10"},
		}
		// The dump MUST run on the admin DSN (BYPASSRLS), never the application
		// DSN: every tenant table is under FORCE ROW LEVEL SECURITY, and pg_dump
		// keeps row_security=off by default, so as a role that cannot bypass RLS
		// it ABORTS — every scheduled dump fails and there is no backup at all
		// (DR-RUNBOOK "Postgres (logical) and PITR"). Validate() refuses such a
		// spec before reconcile; this builder holds the invariant INDEPENDENTLY —
		// no app-DSN fallback exists here, so a reordered or bypassed validation
		// fails loudly instead of materializing a dump pod that cannot work.
		if !adminDSNWired(cp) {
			return batchv1.JobSpec{}, fmt.Errorf("postgres backup requires spec.postgres.adminDsnKey (BYPASSRLS admin DSN): pg_dump aborts under FORCE ROW LEVEL SECURITY as the application role")
		}
		pgEnv = append(pgEnv, adminDSNEnvVar(cp))
		initContainers = []corev1.Container{{
			Name:    pgDumpContainer,
			Image:   pgImage,
			Command: []string{"pg_dump"},
			Args: []string{
				"--format=custom", "--no-owner", "--no-privileges",
				"--file=" + pgSnapshotPath, "--dbname=$(" + adminDSNEnvName + ")",
			},
			Env:             pgEnv,
			Resources:       backupResources(b),
			SecurityContext: hardened,
			VolumeMounts: []corev1.VolumeMount{
				{Name: workVolumeName, MountPath: workMountPath},
				{Name: tmpVolumeName, MountPath: "/tmp"},
			},
		}}
	}

	drBackup := corev1.Container{
		Name:            backupContainer,
		Image:           cp.Spec.Image,
		Args:            backupArgs(cp, b, engine),
		Env:             env,
		Resources:       backupResources(b),
		SecurityContext: hardened,
		VolumeMounts:    mounts,
	}

	backoff := int32(2)
	ttl := int32(86400)
	return batchv1.JobSpec{
		BackoffLimit:            &backoff,
		TTLSecondsAfterFinished: &ttl,
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labelsFor(cp)},
			Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
				// Pin to the ordinal-0 pod's node so the (RWO) data-<name>-0 PVC is
				// mountable here (RWO permits multiple pods on the SAME node).
				Affinity: &corev1.Affinity{PodAffinity: &corev1.PodAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
						LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{podNameLabel: cp.Name + "-0"}},
						TopologyKey:   hostnameKey,
					}},
				}},
				SecurityContext: &corev1.PodSecurityContext{
					RunAsNonRoot:   &nonRoot,
					RunAsUser:      &uid,
					RunAsGroup:     &uid,
					FSGroup:        &uid,
					SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
				},
				InitContainers: initContainers,
				Containers:     []corev1.Container{drBackup},
				Volumes:        volumes,
			},
		},
	}, nil
}

// backupArgs builds the `dr backup` argv. The engine image's ENTRYPOINT is the
// olivares binary, so args start at the subcommand. POD_NAME (downward API) gives
// a unique, traceable bundle name per run WITHOUT a shell.
func backupArgs(cp *opsv1alpha1.ControlPlane, b *opsv1alpha1.BackupSpec, engine opsv1alpha1.Engine) []string {
	args := []string{"dr", "backup", "--data-dir=" + dataMountPath, "--engine=" + string(engine)}
	if engine == opsv1alpha1.EnginePostgres {
		args = append(args, "--dsn=$("+dsnEnvName+")")
		if adminDSNWired(cp) {
			args = append(args, "--admin-dsn=$("+adminDSNEnvName+")")
		}
		args = append(args, "--snapshot-file="+pgSnapshotPath)
	}
	args = append(args, "--out="+backupsMountPath+"/olivares-dr-$(POD_NAME).drbundle")
	if rawKey := strings.TrimSpace(b.KEKRawKey); rawKey != "" {
		args = append(args, "--kek-key-file="+kekMountPath+"/"+rawKey)
	} else {
		passKey := strings.TrimSpace(b.KEKPassphraseKey)
		if passKey == "" {
			passKey = "passphrase"
		}
		args = append(args, "--passphrase-file="+kekMountPath+"/"+passKey)
	}
	if b.RetentionDays > 0 {
		args = append(args, "--retain-days="+strconv.Itoa(int(b.RetentionDays)))
	}
	return args
}

// --- shared wiring helpers --------------------------------------------------

func dsnEnvVar(cp *opsv1alpha1.ControlPlane) corev1.EnvVar {
	key := "dsn"
	if k := strings.TrimSpace(cp.Spec.Postgres.DSNKey); k != "" {
		key = k
	}
	return corev1.EnvVar{Name: dsnEnvName, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: cp.Spec.Postgres.DSNSecret},
		Key:                  key,
	}}}
}

func adminDSNEnvVar(cp *opsv1alpha1.ControlPlane) corev1.EnvVar {
	return corev1.EnvVar{Name: adminDSNEnvName, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: cp.Spec.Postgres.DSNSecret},
		Key:                  strings.TrimSpace(cp.Spec.Postgres.AdminDSNKey),
	}}}
}

// adminDSNWired reports whether the cross-tenant admin pool should be wired. It is
// OPT-IN (a non-empty AdminDSNKey): the engine eagerly connects the admin pool and
// fails closed on a bad/absent DSN, so the controller never emits the flag with an
// unresolved env (the chart's footgun).
func adminDSNWired(cp *opsv1alpha1.ControlPlane) bool {
	return engineOf(cp) == opsv1alpha1.EnginePostgres &&
		cp.Spec.Postgres != nil &&
		strings.TrimSpace(cp.Spec.Postgres.AdminDSNKey) != ""
}

// catalogSecretName resolves the catalog signing key Secret: an explicit override,
// else (the chart's convention) the audit Secret. Empty when neither is set.
func catalogSecretName(cp *opsv1alpha1.ControlPlane) string {
	if s := strings.TrimSpace(cp.Spec.CatalogSigningKeySecret); s != "" {
		return s
	}
	return strings.TrimSpace(cp.Spec.AuditSigningKeySecret)
}

// secretVolume builds a Secret-backed volume. items projects a subset (used for
// the catalog key); optional marks the projection optional (so a single-key audit
// Secret mounts cleanly and the engine falls back to a per-node catalog key).
func secretVolume(name, secretName string, mode int32, items []corev1.KeyToPath, optional bool) corev1.Volume {
	src := &corev1.SecretVolumeSource{SecretName: secretName, DefaultMode: ptrInt32(mode)}
	if items != nil {
		src.Items = items
	}
	if optional {
		src.Optional = ptrBool(true)
	}
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{Secret: src}}
}

func resourcesEmpty(r corev1.ResourceRequirements) bool {
	return len(r.Requests) == 0 && len(r.Limits) == 0 && len(r.Claims) == 0
}

// coreResources returns the core container's compute requests/limits: the spec's
// when set, else a conservative Burstable default (mirroring the chart) so the
// control plane is NEVER QoS BestEffort.
func coreResources(cp *opsv1alpha1.ControlPlane) corev1.ResourceRequirements {
	if !resourcesEmpty(cp.Spec.Resources) {
		return cp.Spec.Resources
	}
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1000m"),
			corev1.ResourceMemory: resource.MustParse("1Gi"),
		},
	}
}

func backupResources(b *opsv1alpha1.BackupSpec) corev1.ResourceRequirements {
	if !resourcesEmpty(b.Resources) {
		return b.Resources
	}
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("50m"),
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("512Mi"),
		},
	}
}

// neutralizeBackup suspends the owned backup CronJob and deletes the Jobs it
// started, when the spec has gone structurally invalid. Without this, an operator
// upgrade that tightens validation marks the CR Invalid while the CronJob
// materialized by an OLDER controller keeps its stale template and keeps
// scheduling runs — the concrete case being pre-fix postgres CronJobs that
// carried the application DSN and could only produce failing dumps.
//
// Suspending rather than deleting is reversible: reconcileBackup writes
// suspend=false EXPLICITLY on the valid path, so repairing the spec restores the
// schedule. The bundle PVC is deliberately untouched — neutralizing a schedule
// must never destroy DR history.
//
// Deletion is per-object filtered by OWNERSHIP, not a label-selected collection
// delete. Two reasons, both load-bearing: labels are a selector and never proof
// of ownership (a third-party Job wearing them must survive), and — the case
// this function exists for — a CronJob materialized by an OLDER controller has
// no backup labels on its JobTemplate at all, so its running Jobs carry none
// either and a label-selected delete would silently find nothing on exactly the
// upgrade path being neutralized. The CronJob's UID is what identifies its Jobs.
// This also keeps the RBAC at `jobs: delete` rather than `deletecollection`.
func (r *ControlPlaneReconciler) neutralizeBackup(ctx context.Context, cp *opsv1alpha1.ControlPlane) error {
	var cj batchv1.CronJob
	err := r.Get(ctx, types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name + "-backup"}, &cj)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !metav1.IsControlledBy(&cj, cp) {
		return nil // not ours — never touch an object this controller does not own
	}
	if cj.Spec.Suspend == nil || !*cj.Spec.Suspend {
		suspend := true
		cj.Spec.Suspend = &suspend
		if err := r.Update(ctx, &cj); err != nil {
			return err
		}
	}
	// Suspension stops NEW runs; Jobs already started keep going. Delete the ones
	// this CronJob owns so a dump that can only fail is not retried under an
	// invalid spec (background propagation reaps the pods).
	// Listed through the UNCACHED reader on purpose. A cached List starts an
	// informer for the listed kind, and a controller-runtime cache is not scoped
	// by a per-call InNamespace filter: one legacy invalid CR would install a
	// permanent cluster-wide Jobs ListWatch — sync, watches and memory
	// proportional to every Job in the cluster — during the very upgrade this
	// path exists to make safe. This sweep runs only on the invalid-spec branch,
	// so a live read costs nothing in the normal case.
	var jobs batchv1.JobList
	if err := r.reader().List(ctx, &jobs, client.InNamespace(cp.Namespace)); err != nil {
		return err
	}
	for i := range jobs.Items {
		job := &jobs.Items[i]
		if !metav1.IsControlledBy(job, &cj) {
			continue
		}
		if err := r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// markInvalid records that the spec is structurally impossible (Phase=Invalid)
// without materializing a crashlooping workload.
func (r *ControlPlaneReconciler) markInvalid(ctx context.Context, cp *opsv1alpha1.ControlPlane, msg string) error {
	cp.Status.ObservedGeneration = cp.Generation
	cp.Status.Phase = opsv1alpha1.PhaseInvalid
	meta.SetStatusCondition(&cp.Status.Conditions, metav1.Condition{
		Type: opsv1alpha1.ConditionAvailable, Status: metav1.ConditionFalse,
		Reason: "SpecInvalid", Message: msg, ObservedGeneration: cp.Generation,
	})
	meta.SetStatusCondition(&cp.Status.Conditions, metav1.Condition{
		Type: opsv1alpha1.ConditionProgressing, Status: metav1.ConditionFalse,
		Reason: "SpecInvalid", Message: "spec rejected; not reconciling a workload", ObservedGeneration: cp.Generation,
	})
	meta.SetStatusCondition(&cp.Status.Conditions, metav1.Condition{
		Type: opsv1alpha1.ConditionDegraded, Status: metav1.ConditionTrue,
		Reason: "SpecInvalid", Message: msg, ObservedGeneration: cp.Generation,
	})
	return r.Status().Update(ctx, cp)
}

// updateStatus recomputes status from the observed StatefulSet and the live pods,
// patches it, and returns the classification (the caller uses it to decide whether
// to re-queue). It sets ObservedGeneration, ReadyReplicas, CurrentImage, LeaderPod,
// the rollout-progress bookkeeping, Phase, and the Available/Progressing/Degraded
// conditions.
func (r *ControlPlaneReconciler) updateStatus(ctx context.Context, cp *opsv1alpha1.ControlPlane, sts *appsv1.StatefulSet, pods podObservation) (rolloutClass, error) {
	desired, clamped := effectiveReplicas(cp)

	// Classify the rollout from OBSERVED StatefulSet status (revisions + replica
	// counters) and the live pods (readiness, leader label, images), NOT the desired
	// pod template (design §A.4). classifyRollout reads the PRIOR CurrentImage and
	// progress bookkeeping, so it must run before those are overwritten.
	cls := classifyRollout(cp, sts, pods, metav1.Now())
	ready := sts.Status.ReadyReplicas

	cp.Status.ObservedGeneration = cp.Generation
	cp.Status.ReadyReplicas = ready
	cp.Status.LeaderPod = pods.leaderPod
	cp.Status.RolloutRevision = cls.progress.revision
	cp.Status.LastProgressUpdatedReplicas = cls.progress.updatedReplicas
	cp.Status.LastProgressReadyReplicas = cls.progress.readyReplicas
	progressAt := cls.progress.at
	cp.Status.LastProgressTime = &progressAt
	// CurrentImage is the LAST FULLY CONVERGED image, exactly as the CRD documents:
	// it lags spec.image until every pod is on the update revision, and it is then
	// set to the desired image (which, at full revision convergence, is provably the
	// image the pods were created from).
	//
	// The subtlety is provenance. A pre controller wrote this field from the
	// DESIRED pod template, so a value inherited from it may name an image no pod
	// ever ran. Such a value must not be preserved as if it were observed. This
	// controller records `rolloutRevision` whenever it manages a rollout, so its
	// presence is the marker that the current value came from THIS controller: with
	// it, the prior value is kept while rolling (the documented lag); without it, an
	// unconverged CR reports "" — the honest "not confirmed" — exactly once, until
	// the first convergence establishes a trustworthy value.
	switch {
	case cls.imageRolled:
		cp.Status.CurrentImage = cp.Spec.Image
	case cp.Status.RolloutRevision == "":
		cp.Status.CurrentImage = "" // inherited/unknown provenance: do not vouch for it
	}

	// Mirror the most recent backup time if a CronJob reported one.
	if cp.Spec.Backup != nil {
		var cj batchv1.CronJob
		if err := r.Get(ctx, types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name + "-backup"}, &cj); err == nil {
			cp.Status.LastBackup = cj.Status.LastSuccessfulTime
		}
	}

	switch {
	case desired == 0:
		cp.Status.Phase = opsv1alpha1.PhasePending
		meta.SetStatusCondition(&cp.Status.Conditions, metav1.Condition{
			Type: opsv1alpha1.ConditionAvailable, Status: metav1.ConditionFalse,
			Reason: "ScaledToZero", Message: "spec.replicas is 0",
			ObservedGeneration: cp.Generation,
		})
		meta.SetStatusCondition(&cp.Status.Conditions, metav1.Condition{
			Type: opsv1alpha1.ConditionProgressing, Status: metav1.ConditionFalse,
			Reason: "ScaledToZero", Message: "no replicas desired",
			ObservedGeneration: cp.Generation,
		})
	case cls.ready:
		cp.Status.Phase = opsv1alpha1.PhaseReady
		meta.SetStatusCondition(&cp.Status.Conditions, metav1.Condition{
			Type: opsv1alpha1.ConditionAvailable, Status: metav1.ConditionTrue,
			Reason: "MinimumReplicasAvailable", Message: "control plane ready",
			ObservedGeneration: cp.Generation,
		})
		meta.SetStatusCondition(&cp.Status.Conditions, metav1.Condition{
			Type: opsv1alpha1.ConditionProgressing, Status: metav1.ConditionFalse,
			Reason:             "RolloutComplete",
			Message:            fmt.Sprintf("%d/%d replicas ready at %s", ready, desired, cp.Spec.Image),
			ObservedGeneration: cp.Generation,
		})
	case cls.haReadinessBlocked:
		// LEGACY HA is fully rolled out and the leader is serving, but standbys drain
		// the leader-only /readyz probe, so ReadyReplicas cannot reach desired
		// on this layout. Surface the honest, actionable state instead of an eternal,
		// silent PhaseProgressing: Available (a leader serves), NOT ready; the
		// Degraded condition below carries the fix pointer (spec.haRouting).
		cp.Status.Phase = opsv1alpha1.PhaseProgressing
		meta.SetStatusCondition(&cp.Status.Conditions, metav1.Condition{
			Type: opsv1alpha1.ConditionAvailable, Status: metav1.ConditionTrue,
			Reason:             "LeaderServing",
			Message:            fmt.Sprintf("leader serving; %d/%d replicas Ready (standbys drain /readyz by design)", ready, desired),
			ObservedGeneration: cp.Generation,
		})
		meta.SetStatusCondition(&cp.Status.Conditions, metav1.Condition{
			Type: opsv1alpha1.ConditionProgressing, Status: metav1.ConditionFalse,
			Reason:             reasonHALegacyReadinessBlocked,
			Message:            "rollout complete; PhaseReady blocked by the leader-only readiness layout",
			ObservedGeneration: cp.Generation,
		})
	default:
		// Rolling out: install, scale, an image/config rollout in flight, a stalled
		// rollout, or converged pods whose leader label is missing/duplicated.
		msg := rolloutMessage(cp, cls, pods, ready, desired)
		cp.Status.Phase = opsv1alpha1.PhaseProgressing
		// Progressing=True means "the rollout is advancing". A stalled rollout, or a
		// converged one whose leader publication is wrong, is NOT advancing — saying
		// otherwise would hide a wedge behind a spinner (design §C.3, §C.4).
		progressingStatus := metav1.ConditionTrue
		progressingReason := cls.reason
		switch {
		case cls.stalled:
			progressingStatus = metav1.ConditionFalse
			progressingReason = reasonProgressDeadlineExceeded
		case cls.reason == reasonLeaderNotPublished || cls.reason == reasonMultipleLeadersPublished:
			progressingStatus = metav1.ConditionFalse
		}
		meta.SetStatusCondition(&cp.Status.Conditions, metav1.Condition{
			Type: opsv1alpha1.ConditionProgressing, Status: progressingStatus,
			Reason: progressingReason, Message: msg, ObservedGeneration: cp.Generation,
		})
		// Availability is an INDEPENDENT question: "can clients reach a leader right
		// now?" In the leader-routing layout that is exactly one Ready leader-labeled
		// pod (the leader Service's only endpoint); in the legacy layout any Ready
		// replica is the leader. So an image rollout can be Progressing and Available
		// at the same time.
		availStatus := metav1.ConditionFalse
		if cls.available {
			availStatus = metav1.ConditionTrue
		}
		meta.SetStatusCondition(&cp.Status.Conditions, metav1.Condition{
			Type: opsv1alpha1.ConditionAvailable, Status: availStatus,
			Reason: cls.reason, Message: msg, ObservedGeneration: cp.Generation,
		})
	}

	// Degraded surfaces a derate or a stuck transition so it is never silent:
	//   - SQLiteReplicasClamped: engine=sqlite + Replicas>1 applied as 1 effective
	//     replica (a multi-pod sqlite deployment would fork the audit ledger);
	//   - HARequiresRecreate: the spec asks for active-passive HA (Parallel pod
	//     management) but the live StatefulSet was created OrderedReady, which is
	//     IMMUTABLE — the standbys never become Ready, so an OrderedReady scale-up
	//     would wedge. The controller cannot patch the field, so it flags the need
	//     to recreate rather than letting the rollout hang invisibly.
	degraded := metav1.ConditionFalse
	degradedReason, degradedMsg := "AsConfigured", "spec applied as written"
	switch {
	case clamped:
		degraded = metav1.ConditionTrue
		degradedReason = "SQLiteReplicasClamped"
		degradedMsg = fmt.Sprintf("engine=sqlite forces 1 effective replica (requested %d); set engine=postgres for active-passive HA", cp.Spec.Replicas)
	case haPolicyMismatch(cp, sts):
		degraded = metav1.ConditionTrue
		degradedReason = "HARequiresRecreate"
		degradedMsg = fmt.Sprintf("active-passive HA needs podManagementPolicy=Parallel but the StatefulSet was created %q (immutable); standby pods stay NotReady so an OrderedReady scale-up wedges. Recreate to enable HA: `kubectl delete statefulset %s --cascade=orphan` (pods are preserved), then reconcile", sts.Spec.PodManagementPolicy, cp.Name)
	case migratingToLeaderRouting(cp, sts):
		// The destination exists (leader Service + publisher RBAC) but the pod template
		// is deliberately untouched until the administrator confirms clients moved.
		// Reporting this as Degraded rather than "Progressing" is the honest framing:
		// nothing is converging on its own, a human action is required.
		degraded = metav1.ConditionTrue
		degradedReason = reasonHALeaderServiceMigrationRequired
		degradedMsg = fmt.Sprintf("spec.haRouting=LeaderRouting is requested and Service/%s plus the %s credential are ready, but this StatefulSet still uses the leader-only readiness layout. Changing the pod template now would expose health-Ready standbys through the legacy %s Service before clients moved. Point every application client (ingress/CLI/SDK/collectors) at Service/%s, then acknowledge with `kubectl annotate controlplane %s %s=acknowledged` to start the rollout (docs/HA-LEADER-ROUTING.md).",
			leaderServiceName(cp), leaderPublisherName(cp), cp.Name, leaderServiceName(cp), cp.Name, leaderCutoverAnnotation)
	case cls.haReadinessBlocked:
		degraded = metav1.ConditionTrue
		degradedReason = reasonHALegacyReadinessBlocked
		degradedMsg = fmt.Sprintf("active-passive HA is fully rolled out and the leader is serving, but standby pods intentionally fail the leader-only readiness probe, so ReadyReplicas (%d) cannot reach desired (%d); reaching Ready needs the leader-routing layout — set spec.haRouting=LeaderRouting with an engine image that serves /pod-readyz and move clients to the %s Service (docs/HA-LEADER-ROUTING.md). The control plane is available through its leader.", ready, desired, leaderServiceName(cp))
	case cls.degraded == reasonLeaderNotPublished:
		degraded = metav1.ConditionTrue
		degradedReason = reasonLeaderNotPublished
		degradedMsg = fmt.Sprintf("the rollout converged but no Ready pod publishes %s=%s, so Service/%s has no endpoint and clients cannot reach the writer. Either no node holds the election lock (check the Postgres DSN and the engine logs) or the engine could not label its pod (check that spec.image serves /pod-readyz and that the %s ServiceAccount is mounted).", haRoleLabelKey, haRoleLeader, leaderServiceName(cp), leaderPublisherName(cp))
	case cls.degraded == reasonMultipleLeadersPublished:
		degraded = metav1.ConditionTrue
		degradedReason = reasonMultipleLeadersPublished
		degradedMsg = fmt.Sprintf("%d Ready pods claim %s=%s; Service/%s would fan out across them. The operator never picks one: the Postgres advisory lock still permits a single writer and the engine answers 503 not_leader on the stale pod, which self-heals when its publisher resyncs.", pods.readyLeaders, haRoleLabelKey, haRoleLeader, leaderServiceName(cp))
	case cls.stalled:
		degraded = metav1.ConditionTrue
		degradedReason = reasonRolloutStalled
		degradedMsg = fmt.Sprintf("no rollout progress for more than %s: still %d/%d updated and %d/%d Ready on revision %q. Inspect the pods (image pull, PVC binding, failing probes); this is a wedge, not slow progress.", progressDeadline(cp), sts.Status.UpdatedReplicas, desired, ready, desired, sts.Status.UpdateRevision)
	}
	meta.SetStatusCondition(&cp.Status.Conditions, metav1.Condition{
		Type: opsv1alpha1.ConditionDegraded, Status: degraded,
		Reason: degradedReason, Message: degradedMsg, ObservedGeneration: cp.Generation,
	})

	return cls, r.Status().Update(ctx, cp)
}

// rolloutMessage renders the human-facing Progressing/Available message for an
// unconverged rollout, naming what is actually being waited on.
func rolloutMessage(cp *opsv1alpha1.ControlPlane, cls rolloutClass, pods podObservation, ready, desired int32) string {
	switch cls.reason {
	case reasonUpgrading:
		return fmt.Sprintf("rolling out %s (%d/%d ready)", cp.Spec.Image, ready, desired)
	case reasonReconfiguring:
		return fmt.Sprintf("applying a configuration change (%d/%d ready)", ready, desired)
	case reasonWaitingForPodHealth:
		return fmt.Sprintf("every replica is on the update revision; %d/%d pass the pod-health probe", ready, desired)
	case reasonLeaderNotPublished:
		return fmt.Sprintf("no Ready pod publishes %s=%s; Service/%s has no endpoint", haRoleLabelKey, haRoleLeader, leaderServiceName(cp))
	case reasonMultipleLeadersPublished:
		return fmt.Sprintf("%d Ready pods claim the leader label; refusing to choose between them", pods.readyLeaders)
	default:
		return fmt.Sprintf("%d/%d replicas ready", ready, desired)
	}
}

// SetupWithManager wires the reconciler: it owns the StatefulSet, Service and
// CronJob it creates, so changes to those re-trigger Reconcile.
//
// NOTE on agent-runtime CRDs: we deliberately do NOT add Watches/Owns against
// Agent Sandbox or kagent CRDs here. Those APIs are pre-stable (see
// internal/agentruntime) — watching them would couple this controller's startup
// to schemas that are explicitly "moving fast". Presence detection is an opt-in,
// data-only path, not a controller watch.
func (r *ControlPlaneReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Index ControlPlanes by spec.configRef so a ConfigMap/Secret event maps to the
	// ControlPlanes that REFERENCE it with one indexed lookup instead of listing and
	// filtering every ControlPlane in the cluster on every config event.
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &opsv1alpha1.ControlPlane{}, configRefIndex,
		func(obj client.Object) []string {
			cp, ok := obj.(*opsv1alpha1.ControlPlane)
			if !ok || cp.Spec.ConfigRef == "" {
				return nil
			}
			return []string{cp.Spec.ConfigRef}
		}); err != nil {
		return fmt.Errorf("index %s: %w", configRefIndex, err)
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&opsv1alpha1.ControlPlane{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&batchv1.CronJob{}).
		// The leader-routing layout's operand credential: owned, so a manual edit is
		// reverted on the next reconcile.
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		// Stage-2: pod events drive the leader-route predicate. The engine
		// publishes its role label asynchronously (after it wins the election), and
		// that transition changes NO owned object — without this watch the status
		// would only catch up on the next periodic re-queue.
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.controlPlaneForWorkloadPod)).
		// Stage-2 (design §A.5): a config edit must actually re-enqueue the
		// reconfigure it promises. The manager already holds get/list/watch on both
		// kinds, so this adds a watch, not a privilege.
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.controlPlanesForConfigObject)).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.controlPlanesForConfigObject)).
		Complete(r)
}

// configRefIndex is the field index name for ControlPlane.spec.configRef.
const configRefIndex = "spec.configRef"

// controlPlaneForWorkloadPod maps a workload pod back to its ControlPlane using the
// instance label the operator stamps on every pod it renders. Pods of other
// workloads carry no such label and enqueue nothing.
func (r *ControlPlaneReconciler) controlPlaneForWorkloadPod(_ context.Context, obj client.Object) []reconcile.Request {
	labels := obj.GetLabels()
	if labels["app.kubernetes.io/name"] != "olivares" ||
		labels["app.kubernetes.io/managed-by"] != "olivares-operator" ||
		labels["app.kubernetes.io/component"] != "core" {
		return nil
	}
	instance := labels["app.kubernetes.io/instance"]
	if instance == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: instance}}}
}

// controlPlanesForConfigObject maps a ConfigMap/Secret event to every ControlPlane
// in its namespace that references it via spec.configRef (indexed lookup).
func (r *ControlPlaneReconciler) controlPlanesForConfigObject(ctx context.Context, obj client.Object) []reconcile.Request {
	var list opsv1alpha1.ControlPlaneList
	if err := r.List(ctx, &list,
		client.InNamespace(obj.GetNamespace()),
		client.MatchingFields{configRefIndex: obj.GetName()}); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: list.Items[i].Namespace, Name: list.Items[i].Name,
		}})
	}
	return reqs
}

func ptrBool(b bool) *bool    { return &b }
func ptrInt32(i int32) *int32 { return &i }
