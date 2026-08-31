// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	opsv1alpha1 "github.com/olivaresai/olivares/operator/api/v1alpha1"
)

// newScheme builds a scheme with the built-in types this controller touches plus
// the ControlPlane CRD. No envtest, no apiserver — pure in-memory.
func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		corev1.AddToScheme,
		appsv1.AddToScheme,
		batchv1.AddToScheme,
		rbacv1.AddToScheme,
		opsv1alpha1.AddToScheme,
	} {
		if err := add(s); err != nil {
			t.Fatalf("scheme add: %v", err)
		}
	}
	return s
}

// newReconciler wires a fake client (with status subresource enabled for the
// ControlPlane) and the reconciler under test.
func newReconciler(t *testing.T, objs ...client.Object) (*ControlPlaneReconciler, client.Client) {
	t.Helper()
	s := newScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&opsv1alpha1.ControlPlane{}).
		WithObjects(objs...).
		Build()
	return &ControlPlaneReconciler{Client: c, Scheme: s}, c
}

func sampleCP() *opsv1alpha1.ControlPlane {
	return &opsv1alpha1.ControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: opsv1alpha1.ControlPlaneSpec{
			Image:    "ghcr.io/olivaresai/olivares:0.1.0",
			Replicas: 1,
			Engine:   opsv1alpha1.EngineSQLite,
		},
	}
}

func reconcileOnce(t *testing.T, r *ControlPlaneReconciler, nn types.NamespacedName) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: nn}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

// --- small assertion helpers -------------------------------------------------

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func envByName(env []corev1.EnvVar, name string) (corev1.EnvVar, bool) {
	for _, e := range env {
		if e.Name == name {
			return e, true
		}
	}
	return corev1.EnvVar{}, false
}

func volByName(vols []corev1.Volume, name string) (corev1.Volume, bool) {
	for _, v := range vols {
		if v.Name == name {
			return v, true
		}
	}
	return corev1.Volume{}, false
}

func mountByName(ms []corev1.VolumeMount, name string) (corev1.VolumeMount, bool) {
	for _, m := range ms {
		if m.Name == name {
			return m, true
		}
	}
	return corev1.VolumeMount{}, false
}

func containerByName(cs []corev1.Container, name string) (corev1.Container, bool) {
	for _, c := range cs {
		if c.Name == name {
			return c, true
		}
	}
	return corev1.Container{}, false
}

func getSTS(t *testing.T, c client.Client, nn types.NamespacedName) appsv1.StatefulSet {
	t.Helper()
	var sts appsv1.StatefulSet
	if err := c.Get(context.Background(), nn, &sts); err != nil {
		t.Fatalf("statefulset not created: %v", err)
	}
	return sts
}

// TestReconcile_CreatesStatefulSet asserts the INSTALL path: a StatefulSet and a
// headless Service are created from spec, hardened, with compute Resources set
// (NEVER QoS BestEffort) and OrderedReady pod management for the single writer.
func TestReconcile_CreatesStatefulSet(t *testing.T) {
	cp := sampleCP()
	r, c := newReconciler(t, cp)
	nn := types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name}

	reconcileOnce(t, r, nn)

	sts := getSTS(t, c, nn)
	if got, want := *sts.Spec.Replicas, int32(1); got != want {
		t.Errorf("replicas = %d, want %d", got, want)
	}
	if sts.Spec.PodManagementPolicy != appsv1.OrderedReadyPodManagement {
		t.Errorf("podManagementPolicy = %q, want OrderedReady for single writer", sts.Spec.PodManagementPolicy)
	}
	if len(sts.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("want 1 container, got %d", len(sts.Spec.Template.Spec.Containers))
	}
	cont := sts.Spec.Template.Spec.Containers[0]
	if got, want := cont.Image, cp.Spec.Image; got != want {
		t.Errorf("image = %q, want %q", got, want)
	}
	// Hardening mirrored from the Helm chart: non-root uid 65532, ro rootfs.
	if sc := cont.SecurityContext; sc == nil || sc.RunAsUser == nil || *sc.RunAsUser != 65532 {
		t.Errorf("container runAsUser not 65532: %+v", sc)
	}
	if sc := cont.SecurityContext; sc == nil || sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Errorf("readOnlyRootFilesystem not set")
	}
	// Resources set (gap #4): defaulted to Burstable, never BestEffort.
	if cont.Resources.Requests.Cpu().IsZero() || cont.Resources.Requests.Memory().IsZero() {
		t.Errorf("core container has no resource requests (QoS BestEffort): %+v", cont.Resources)
	}
	if cont.Resources.Limits.Memory().IsZero() {
		t.Errorf("core container has no memory limit: %+v", cont.Resources)
	}
	// Engine env wired through; sqlite passes NO --dsn/--engine flag.
	if e, ok := envByName(cont.Env, "OLIVARES_ENGINE"); !ok || e.Value != string(opsv1alpha1.EngineSQLite) {
		t.Errorf("OLIVARES_ENGINE=sqlite env not found: %+v", cont.Env)
	}
	if hasArg(cont.Args, "--engine=postgres") {
		t.Errorf("sqlite must not pass --engine=postgres: %v", cont.Args)
	}
	if !hasArg(cont.Args, "--listen=:8443") || !hasArg(cont.Args, "--grpc-listen=:8444") {
		t.Errorf("container listener args = %v, want dual-stack :8443/:8444", cont.Args)
	}
	// Owner reference set.
	if len(sts.OwnerReferences) != 1 || sts.OwnerReferences[0].Kind != "ControlPlane" {
		t.Errorf("statefulset not owned by ControlPlane: %+v", sts.OwnerReferences)
	}

	// Headless service created.
	var svc corev1.Service
	if err := c.Get(context.Background(), nn, &svc); err != nil {
		t.Fatalf("service not created: %v", err)
	}
	if svc.Spec.ClusterIP != corev1.ClusterIPNone {
		t.Errorf("service not headless: clusterIP=%q", svc.Spec.ClusterIP)
	}
}

// TestReconcile_CoreResourcesHonored asserts an explicit spec.resources block is
// passed through verbatim (not overridden by the default).
func TestReconcile_CoreResourcesHonored(t *testing.T) {
	cp := sampleCP()
	cp.Spec.Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")},
	}
	r, c := newReconciler(t, cp)
	nn := types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name}
	reconcileOnce(t, r, nn)

	cont := getSTS(t, c, nn).Spec.Template.Spec.Containers[0]
	if got := cont.Resources.Requests.Memory().String(); got != "512Mi" {
		t.Errorf("memory request = %q, want 512Mi (spec honored)", got)
	}
	if got := cont.Resources.Limits.Memory().String(); got != "2Gi" {
		t.Errorf("memory limit = %q, want 2Gi (spec honored)", got)
	}
}

// TestReconcile_SQLiteReplicasClamped asserts the safety guard: engine=sqlite
// with spec.Replicas>1 would fork the audit ledger, so the operator clamps the
// StatefulSet to 1 replica and raises a Degraded condition (NOT an Invalid spec —
// the clamp is deliberate). engine=postgres is left to its HA path.
func TestReconcile_SQLiteReplicasClamped(t *testing.T) {
	cp := sampleCP()
	cp.Spec.Engine = opsv1alpha1.EngineSQLite
	cp.Spec.Replicas = 3
	r, c := newReconciler(t, cp)
	nn := types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name}

	reconcileOnce(t, r, nn)

	sts := getSTS(t, c, nn)
	if got, want := *sts.Spec.Replicas, int32(1); got != want {
		t.Fatalf("sqlite replicas not clamped: got %d, want %d (a multi-pod sqlite deployment forks the ledger)", got, want)
	}
	// Clamped sqlite stays OrderedReady (single writer), not Parallel.
	if sts.Spec.PodManagementPolicy != appsv1.OrderedReadyPodManagement {
		t.Errorf("clamped sqlite podManagementPolicy = %q, want OrderedReady", sts.Spec.PodManagementPolicy)
	}

	var live opsv1alpha1.ControlPlane
	if err := c.Get(context.Background(), nn, &live); err != nil {
		t.Fatalf("get cp: %v", err)
	}
	if cnd := conditionStatus(live.Status.Conditions, opsv1alpha1.ConditionDegraded); cnd != metav1.ConditionTrue {
		t.Errorf("Degraded condition = %q, want True (clamp must be visible, not silent)", cnd)
	}
}

// postgresHACP is a VALID active-passive HA spec: postgres + a DSN Secret + a
// shared audit signing key + 3 replicas.
func postgresHACP() *opsv1alpha1.ControlPlane {
	cp := sampleCP()
	cp.Spec.Engine = opsv1alpha1.EnginePostgres
	cp.Spec.Replicas = 3
	cp.Spec.Postgres = &opsv1alpha1.PostgresSpec{DSNSecret: "pg-dsn", DSNKey: "dsn"}
	cp.Spec.AuditSigningKeySecret = "olivares-audit-key"
	return cp
}

// TestReconcile_PostgresHA asserts the supported HA path: postgres + shared audit
// key + 3 replicas passes through unclamped, with Parallel pod management, the DSN
// wired to the engine, and the shared audit key mounted into every replica.
func TestReconcile_PostgresHA(t *testing.T) {
	cp := postgresHACP()
	r, c := newReconciler(t, cp)
	nn := types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name}

	reconcileOnce(t, r, nn)

	sts := getSTS(t, c, nn)
	if got, want := *sts.Spec.Replicas, int32(3); got != want {
		t.Errorf("postgres replicas = %d, want %d (HA must not be clamped)", got, want)
	}
	// HA MUST be Parallel: standbys are not Ready, so OrderedReady wedges scale-up.
	if sts.Spec.PodManagementPolicy != appsv1.ParallelPodManagement {
		t.Errorf("HA podManagementPolicy = %q, want Parallel", sts.Spec.PodManagementPolicy)
	}

	cont := sts.Spec.Template.Spec.Containers[0]
	// DSN wired to the engine (gap #1): --engine + --dsn=$(OLIVARES_DSN) arg, env
	// from the Secret. The engine has no DSN env fallback, so the arg is required.
	if !hasArg(cont.Args, "--engine=postgres") || !hasArg(cont.Args, "--dsn=$(OLIVARES_DSN)") {
		t.Errorf("postgres DSN args not wired: %v", cont.Args)
	}
	dsnEnv, ok := envByName(cont.Env, "OLIVARES_DSN")
	if !ok || dsnEnv.ValueFrom == nil || dsnEnv.ValueFrom.SecretKeyRef == nil {
		t.Fatalf("OLIVARES_DSN env not sourced from a Secret: %+v", cont.Env)
	}
	if got := dsnEnv.ValueFrom.SecretKeyRef.Name; got != "pg-dsn" {
		t.Errorf("OLIVARES_DSN secret = %q, want pg-dsn", got)
	}
	if got := dsnEnv.ValueFrom.SecretKeyRef.Key; got != "dsn" {
		t.Errorf("OLIVARES_DSN key = %q, want dsn", got)
	}
	// No admin pool unless opted-in.
	if hasArg(cont.Args, "--admin-dsn=$(OLIVARES_ADMIN_DSN)") {
		t.Errorf("admin-dsn must be opt-in: %v", cont.Args)
	}

	// Shared audit key mounted into the replica (gap #3).
	akEnv, ok := envByName(cont.Env, "OLIVARES_AUDIT_SIGNING_KEY_FILE")
	if !ok || akEnv.Value != "/etc/olivares/audit-key/audit-signing.key" {
		t.Errorf("audit key env not wired: %+v", cont.Env)
	}
	akMount, ok := mountByName(cont.VolumeMounts, "audit-key")
	if !ok || !akMount.ReadOnly || akMount.MountPath != "/etc/olivares/audit-key" {
		t.Errorf("audit-key mount missing/incorrect: %+v", cont.VolumeMounts)
	}
	akVol, ok := volByName(sts.Spec.Template.Spec.Volumes, "audit-key")
	if !ok || akVol.Secret == nil || akVol.Secret.SecretName != "olivares-audit-key" {
		t.Fatalf("audit-key volume not from the Secret: %+v", sts.Spec.Template.Spec.Volumes)
	}
	if akVol.Secret.DefaultMode == nil || *akVol.Secret.DefaultMode != 0o440 {
		t.Errorf("audit-key defaultMode = %v, want 0440 (owner-only fail-closed check)", akVol.Secret.DefaultMode)
	}
	// Catalog key defaults to the audit Secret, projected optionally.
	ckVol, ok := volByName(sts.Spec.Template.Spec.Volumes, "catalog-key")
	if !ok || ckVol.Secret == nil || ckVol.Secret.SecretName != "olivares-audit-key" {
		t.Errorf("catalog-key volume should default to the audit Secret: %+v", sts.Spec.Template.Spec.Volumes)
	}
	if ok && (ckVol.Secret.Optional == nil || !*ckVol.Secret.Optional) {
		t.Errorf("catalog-key projection must be optional (single-key audit Secret boots)")
	}

	var live opsv1alpha1.ControlPlane
	if err := c.Get(context.Background(), nn, &live); err != nil {
		t.Fatalf("get cp: %v", err)
	}
	if cnd := conditionStatus(live.Status.Conditions, opsv1alpha1.ConditionDegraded); cnd != metav1.ConditionFalse {
		t.Errorf("Degraded condition = %q, want False for postgres HA", cnd)
	}
}

// TestReconcile_PostgresAdminDSNOptIn asserts the admin pool is wired only when
// AdminDSNKey is set (and never with an unresolved env — the chart's footgun).
func TestReconcile_PostgresAdminDSNOptIn(t *testing.T) {
	cp := sampleCP()
	cp.Spec.Engine = opsv1alpha1.EnginePostgres
	cp.Spec.Replicas = 1
	cp.Spec.Postgres = &opsv1alpha1.PostgresSpec{DSNSecret: "pg-dsn", AdminDSNKey: "admin-dsn"}
	r, c := newReconciler(t, cp)
	nn := types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name}
	reconcileOnce(t, r, nn)

	cont := getSTS(t, c, nn).Spec.Template.Spec.Containers[0]
	if !hasArg(cont.Args, "--admin-dsn=$(OLIVARES_ADMIN_DSN)") {
		t.Errorf("admin-dsn not wired when opted in: %v", cont.Args)
	}
	adminEnv, ok := envByName(cont.Env, "OLIVARES_ADMIN_DSN")
	if !ok || adminEnv.ValueFrom == nil || adminEnv.ValueFrom.SecretKeyRef == nil ||
		adminEnv.ValueFrom.SecretKeyRef.Key != "admin-dsn" {
		t.Errorf("OLIVARES_ADMIN_DSN env not sourced from the admin-dsn key: %+v", cont.Env)
	}
}

// TestReconcile_InvalidSpecs asserts admission-equivalent rejection in the
// controller: a structurally-impossible spec is marked Invalid and NO StatefulSet
// is created (no crashloop).
func TestReconcile_InvalidSpecs(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*opsv1alpha1.ControlPlane)
	}{
		{"postgres without DSN", func(cp *opsv1alpha1.ControlPlane) {
			cp.Spec.Engine = opsv1alpha1.EnginePostgres
		}},
		{"postgres HA without audit key", func(cp *opsv1alpha1.ControlPlane) {
			cp.Spec.Engine = opsv1alpha1.EnginePostgres
			cp.Spec.Replicas = 3
			cp.Spec.Postgres = &opsv1alpha1.PostgresSpec{DSNSecret: "pg-dsn"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cp := sampleCP()
			tc.mut(cp)
			r, c := newReconciler(t, cp)
			nn := types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name}
			reconcileOnce(t, r, nn)

			var sts appsv1.StatefulSet
			if err := c.Get(context.Background(), nn, &sts); err == nil {
				t.Fatalf("a StatefulSet was created for an invalid spec (should crashloop-proof refuse)")
			}
			var live opsv1alpha1.ControlPlane
			if err := c.Get(context.Background(), nn, &live); err != nil {
				t.Fatalf("get cp: %v", err)
			}
			if live.Status.Phase != opsv1alpha1.PhaseInvalid {
				t.Errorf("phase = %q, want Invalid", live.Status.Phase)
			}
			if cnd := conditionStatus(live.Status.Conditions, opsv1alpha1.ConditionAvailable); cnd != metav1.ConditionFalse {
				t.Errorf("Available = %q, want False for an invalid spec", cnd)
			}
		})
	}
}

// TestReconcile_UpgradeUpdatesImage asserts the UPGRADE path: changing spec.Image
// and reconciling updates the StatefulSet's container image.
func TestReconcile_UpgradeUpdatesImage(t *testing.T) {
	cp := sampleCP()
	r, c := newReconciler(t, cp)
	nn := types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name}

	reconcileOnce(t, r, nn)

	var live opsv1alpha1.ControlPlane
	if err := c.Get(context.Background(), nn, &live); err != nil {
		t.Fatalf("get cp: %v", err)
	}
	newImage := "ghcr.io/olivaresai/olivares:0.2.0"
	live.Spec.Image = newImage
	live.Generation = 2
	if err := c.Update(context.Background(), &live); err != nil {
		t.Fatalf("update cp image: %v", err)
	}

	reconcileOnce(t, r, nn)

	sts := getSTS(t, c, nn)
	if got := sts.Spec.Template.Spec.Containers[0].Image; got != newImage {
		t.Errorf("after upgrade image = %q, want %q", got, newImage)
	}
}

// TestReconcile_StatusObservedGeneration asserts status is written.
func TestReconcile_StatusObservedGeneration(t *testing.T) {
	cp := sampleCP()
	cp.Generation = 3
	r, c := newReconciler(t, cp)
	nn := types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name}

	reconcileOnce(t, r, nn)

	var live opsv1alpha1.ControlPlane
	if err := c.Get(context.Background(), nn, &live); err != nil {
		t.Fatalf("get cp: %v", err)
	}
	if live.Status.ObservedGeneration != 3 {
		t.Errorf("observedGeneration = %d, want 3", live.Status.ObservedGeneration)
	}
	// Fresh install: the fake client runs no StatefulSet controller, so there is no
	// rollout status yet. CurrentImage is the OBSERVED rolled image — empty until the
	// first rollout completes. It must NOT echo the desired spec.Image (the pre
	// bug reported the desired pod template as the actual rolled image).
	if live.Status.CurrentImage != "" {
		t.Errorf("currentImage = %q, want empty on a fresh (un-rolled) install", live.Status.CurrentImage)
	}
	if live.Status.Phase != opsv1alpha1.PhaseProgressing {
		t.Errorf("phase = %q, want %q", live.Status.Phase, opsv1alpha1.PhaseProgressing)
	}
	if cnd := conditionStatus(live.Status.Conditions, opsv1alpha1.ConditionProgressing); cnd != metav1.ConditionTrue {
		t.Errorf("Progressing condition = %q, want True", cnd)
	}
}

// TestReconcile_BackupCronJobSQLite asserts the REAL backup seam (not a fake): the
// CronJob runs `olivares dr backup` over the operand's data PVC (NOT an emptyDir
// placeholder), with the KEK, a unique --out and retention wired in, plus an
// owner ref and a created destination PVC.
func TestReconcile_BackupCronJobSQLite(t *testing.T) {
	cp := sampleCP()
	cp.Spec.Backup = &opsv1alpha1.BackupSpec{
		Schedule:                   "0 3 * * *",
		KEKSecret:                  "dr-kek",
		KEKPassphraseKey:           "passphrase",
		RetentionDays:              14,
		SuccessfulJobsHistoryLimit: 5,
	}
	r, c := newReconciler(t, cp)
	nn := types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name}

	reconcileOnce(t, r, nn)

	var cj batchv1.CronJob
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name + "-backup"}, &cj); err != nil {
		t.Fatalf("backup cronjob not created: %v", err)
	}
	if cj.Spec.Schedule != "0 3 * * *" {
		t.Errorf("cronjob schedule = %q, want '0 3 * * *'", cj.Spec.Schedule)
	}
	if cj.Spec.SuccessfulJobsHistoryLimit == nil || *cj.Spec.SuccessfulJobsHistoryLimit != 5 {
		t.Errorf("successfulJobsHistoryLimit = %v, want 5", cj.Spec.SuccessfulJobsHistoryLimit)
	}
	if len(cj.OwnerReferences) != 1 || cj.OwnerReferences[0].Kind != "ControlPlane" {
		t.Errorf("cronjob not owned by ControlPlane: %+v", cj.OwnerReferences)
	}

	pod := cj.Spec.JobTemplate.Spec.Template.Spec
	back, ok := containerByName(pod.Containers, "dr-backup")
	if !ok {
		t.Fatalf("dr-backup container missing: %+v", pod.Containers)
	}
	// Real `dr backup` argv — NOT a fake `backup --destination`.
	for _, want := range []string{"dr", "backup", "--data-dir=/var/lib/olivares", "--engine=sqlite",
		"--out=/backups/olivares-dr-$(POD_NAME).drbundle", "--passphrase-file=/etc/olivares/dr-kek/passphrase", "--retain-days=14"} {
		if !hasArg(back.Args, want) {
			t.Errorf("dr-backup args missing %q: %v", want, back.Args)
		}
	}
	if hasArg(back.Args, "--destination=$(DESTINATION)") {
		t.Errorf("the fake --destination seam must be gone: %v", back.Args)
	}
	// sqlite: no pg-dump initContainer, no --snapshot-file.
	if _, ok := containerByName(pod.InitContainers, "pg-dump"); ok {
		t.Errorf("sqlite backup must not have a pg-dump initContainer")
	}

	// The data volume is the operand's PVC (data-<name>-0), NOT an emptyDir.
	dataVol, ok := volByName(pod.Volumes, "data")
	if !ok || dataVol.PersistentVolumeClaim == nil {
		t.Fatalf("backup data volume must be a PVC, got %+v", dataVol)
	}
	if got, want := dataVol.PersistentVolumeClaim.ClaimName, "data-test-0"; got != want {
		t.Errorf("backup data PVC = %q, want %q (the operand's volumeClaimTemplate PVC)", got, want)
	}
	if dataVol.EmptyDir != nil {
		t.Errorf("backup data volume must not be a placeholder emptyDir")
	}
	// KEK mounted read-only at 0400.
	kekVol, ok := volByName(pod.Volumes, "kek")
	if !ok || kekVol.Secret == nil || kekVol.Secret.SecretName != "dr-kek" {
		t.Fatalf("kek volume not from the Secret: %+v", pod.Volumes)
	}
	if kekVol.Secret.DefaultMode == nil || *kekVol.Secret.DefaultMode != 0o400 {
		t.Errorf("kek defaultMode = %v, want 0400", kekVol.Secret.DefaultMode)
	}
	// POD_NAME downward-API env present (drives the unique --out).
	if e, ok := envByName(back.Env, "POD_NAME"); !ok || e.ValueFrom == nil || e.ValueFrom.FieldRef == nil {
		t.Errorf("POD_NAME downward-API env missing: %+v", back.Env)
	}
	// Backup pinned to pod-0's node so the RWO data PVC is mountable.
	if pod.Affinity == nil || pod.Affinity.PodAffinity == nil ||
		len(pod.Affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution) == 0 {
		t.Fatalf("backup must pin to the operand pod-0 node (podAffinity)")
	}
	sel := pod.Affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0].LabelSelector
	if sel == nil || sel.MatchLabels["statefulset.kubernetes.io/pod-name"] != "test-0" {
		t.Errorf("podAffinity must target pod-0: %+v", sel)
	}
	// Backup container has resources (never BestEffort).
	if back.Resources.Requests.Memory().IsZero() {
		t.Errorf("backup container has no resource requests")
	}

	// The destination PVC was created (un-owned, survives delete).
	var pvc corev1.PersistentVolumeClaim
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name + "-backups"}, &pvc); err != nil {
		t.Fatalf("destination PVC not created: %v", err)
	}
	if len(pvc.OwnerReferences) != 0 {
		t.Errorf("destination PVC must NOT be owned by the ControlPlane (DR data survives delete): %+v", pvc.OwnerReferences)
	}
}

// TestReconcile_BackupCronJobPostgres asserts the postgres backup wiring: a
// pg_dump initContainer feeds --snapshot-file, and the dump runs on the ADMIN
// (BYPASSRLS) DSN — pg_dump keeps row_security=off and ABORTS as the NOBYPASSRLS
// application role under FORCE ROW LEVEL SECURITY, so wiring the app DSN here
// meant every scheduled dump failed and no backup existed (the release-gate
// finding). The dr-backup container needs BOTH DSNs: app to boot the store,
// admin for the cross-tenant manifest inventory (RLS-scoped enumeration would
// silently miss tenants).
func TestReconcile_BackupCronJobPostgres(t *testing.T) {
	cp := postgresHACP()
	cp.Spec.Postgres.AdminDSNKey = "admin-dsn"
	cp.Spec.Backup = &opsv1alpha1.BackupSpec{
		Schedule:      "0 */6 * * *",
		KEKSecret:     "dr-kek",
		PgClientImage: "postgres:16-alpine",
		RetentionDays: 7,
	}
	r, c := newReconciler(t, cp)
	nn := types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name}
	reconcileOnce(t, r, nn)

	var cj batchv1.CronJob
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name + "-backup"}, &cj); err != nil {
		t.Fatalf("backup cronjob not created: %v", err)
	}
	pod := cj.Spec.JobTemplate.Spec.Template.Spec

	pg, ok := containerByName(pod.InitContainers, "pg-dump")
	if !ok {
		t.Fatalf("postgres backup must have a pg-dump initContainer: %+v", pod.InitContainers)
	}
	if pg.Image != "postgres:16-alpine" {
		t.Errorf("pg-dump image = %q, want postgres:16-alpine", pg.Image)
	}
	if len(pg.Command) == 0 || pg.Command[0] != "pg_dump" {
		t.Errorf("pg-dump command = %v, want [pg_dump]", pg.Command)
	}
	if !hasArg(pg.Args, "--file=/work/dump.pgcustom") || !hasArg(pg.Args, "--dbname=$(OLIVARES_ADMIN_DSN)") {
		t.Errorf("pg-dump args missing dump/admin-dsn: %v", pg.Args)
	}
	if _, ok := envByName(pg.Env, "OLIVARES_ADMIN_DSN"); !ok {
		t.Errorf("pg-dump missing OLIVARES_ADMIN_DSN env: %+v", pg.Env)
	}
	// Minimality: the dump container carries ONLY the DSN it uses — the app DSN
	// has no business in a BYPASSRLS dump pod.
	if _, ok := envByName(pg.Env, "OLIVARES_DSN"); ok {
		t.Errorf("pg-dump must not carry the application DSN alongside the admin DSN: %+v", pg.Env)
	}

	back, _ := containerByName(pod.Containers, "dr-backup")
	for _, want := range []string{"--engine=postgres", "--dsn=$(OLIVARES_DSN)", "--admin-dsn=$(OLIVARES_ADMIN_DSN)", "--snapshot-file=/work/dump.pgcustom"} {
		if !hasArg(back.Args, want) {
			t.Errorf("postgres dr-backup args missing %q: %v", want, back.Args)
		}
	}
	if _, ok := envByName(back.Env, "OLIVARES_ADMIN_DSN"); !ok {
		t.Errorf("dr-backup missing OLIVARES_ADMIN_DSN env (the manifest's cross-tenant inventory would be RLS-scoped and silently incomplete): %+v", back.Env)
	}
	// HA backup mounts the shared audit key so the manifest signer uses it.
	if _, ok := mountByName(back.VolumeMounts, "audit-key"); !ok {
		t.Errorf("HA backup must mount the shared audit key: %+v", back.VolumeMounts)
	}
	if e, ok := envByName(back.Env, "OLIVARES_AUDIT_SIGNING_KEY_FILE"); !ok || e.Value == "" {
		t.Errorf("HA backup missing OLIVARES_AUDIT_SIGNING_KEY_FILE: %+v", back.Env)
	}
}

// TestReconcile_InvalidSpecNeutralizesExistingBackup pins the upgrade seam: a
// postgres CR whose backup CronJob was materialized by an OLDER controller (its
// template still carries the application DSN) goes structurally invalid under the
// tightened validation. Marking it Invalid is not enough — the stale CronJob
// would keep scheduling dumps that can only fail — so the reconciler must
// suspend the owned CronJob and delete its started Jobs, while leaving the
// bundle PVC alone (DR history must survive neutralization).
func TestReconcile_InvalidSpecNeutralizesExistingBackup(t *testing.T) {
	cp := postgresHACP()
	// Invalid under the new rule: backup enabled, no adminDsnKey.
	cp.Spec.Backup = &opsv1alpha1.BackupSpec{Schedule: "0 3 * * *", KEKSecret: "dr-kek"}

	// The world an older controller left behind, modeled HONESTLY: its CronJob
	// has NO labels on the JobTemplate (that field is new here), so the Job it
	// templated carries none either. Giving the fixture the new labels would have
	// hidden the real defect — a label-selected delete finds nothing on exactly
	// the upgrade path this code exists for. Ownership is what identifies it.
	scheme := newScheme(t)
	oldCJ := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{
		Name: cp.Name + "-backup", Namespace: cp.Namespace, Labels: labelsFor(cp),
	}}
	if err := controllerutil.SetControllerReference(cp, oldCJ, scheme); err != nil {
		t.Fatalf("own cronjob: %v", err)
	}
	startedJob := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name: cp.Name + "-backup-123", Namespace: cp.Namespace, // no labels: pre-fix template
	}}
	if err := controllerutil.SetControllerReference(oldCJ, startedJob, scheme); err != nil {
		t.Fatalf("own job: %v", err)
	}
	// A third-party Job wearing the backup labels must SURVIVE: labels select,
	// ownership decides.
	foreignJob := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name: "someone-elses-job", Namespace: cp.Namespace, Labels: backupLabelsFor(cp),
	}}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
		Name: cp.Name + "-backups", Namespace: cp.Namespace,
	}}

	r, c := newReconciler(t, cp, oldCJ, startedJob, foreignJob, pvc)
	nn := types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name}
	reconcileOnce(t, r, nn)

	var got opsv1alpha1.ControlPlane
	if err := c.Get(context.Background(), nn, &got); err != nil {
		t.Fatalf("get cp: %v", err)
	}
	if got.Status.Phase != opsv1alpha1.PhaseInvalid {
		t.Errorf("phase = %q, want Invalid", got.Status.Phase)
	}
	var cj batchv1.CronJob
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name + "-backup"}, &cj); err != nil {
		t.Fatalf("stale cronjob must SURVIVE (suspended), not be deleted: %v", err)
	}
	if cj.Spec.Suspend == nil || !*cj.Spec.Suspend {
		t.Errorf("stale backup cronjob must be suspended under an invalid spec; suspend = %v", cj.Spec.Suspend)
	}
	var job batchv1.Job
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name + "-backup-123"}, &job); err == nil {
		t.Errorf("started backup job must be deleted under an invalid spec")
	}
	var foreign batchv1.Job
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: cp.Namespace, Name: "someone-elses-job"}, &foreign); err != nil {
		t.Errorf("a Job this CronJob does not own must SURVIVE (labels select, ownership decides): %v", err)
	}
	var gotPVC corev1.PersistentVolumeClaim
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name + "-backups"}, &gotPVC); err != nil {
		t.Errorf("bundle PVC must survive neutralization: %v", err)
	}

	// --- Phase two: repair the spec; the schedule must come BACK. ---
	if err := c.Get(context.Background(), nn, &got); err != nil {
		t.Fatalf("re-get cp: %v", err)
	}
	got.Spec.Postgres.AdminDSNKey = "admin-dsn"
	if err := c.Update(context.Background(), &got); err != nil {
		t.Fatalf("repair spec: %v", err)
	}
	reconcileOnce(t, r, nn)

	if err := c.Get(context.Background(), types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name + "-backup"}, &cj); err != nil {
		t.Fatalf("get cronjob after repair: %v", err)
	}
	if cj.Spec.Suspend == nil || *cj.Spec.Suspend {
		t.Errorf("repairing the spec must UNSUSPEND the backup cronjob (suspend = %v); relying on the field default leaves the operand with no scheduled backup forever", cj.Spec.Suspend)
	}
	pg, ok := containerByName(cj.Spec.JobTemplate.Spec.Template.Spec.InitContainers, "pg-dump")
	if !ok {
		t.Fatalf("repaired cronjob must template a pg-dump initContainer: %+v", cj.Spec.JobTemplate.Spec.Template.Spec.InitContainers)
	}
	if !hasArg(pg.Args, "--dbname=$(OLIVARES_ADMIN_DSN)") {
		t.Errorf("the repaired template must carry the admin DSN, not the stale one: %v", pg.Args)
	}
}

// TestReconcile_BackupExistingClaim asserts an operator-provided destination PVC
// is used verbatim and no PVC is created.
func TestReconcile_BackupExistingClaim(t *testing.T) {
	cp := sampleCP()
	cp.Spec.Backup = &opsv1alpha1.BackupSpec{
		Schedule:    "0 3 * * *",
		KEKSecret:   "dr-kek",
		Destination: opsv1alpha1.BackupDestination{ExistingClaim: "my-backups"},
	}
	r, c := newReconciler(t, cp)
	nn := types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name}
	reconcileOnce(t, r, nn)

	// No operator-created PVC.
	var pvc corev1.PersistentVolumeClaim
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name + "-backups"}, &pvc); err == nil {
		t.Errorf("operator must not create a PVC when existingClaim is set")
	}
	var cj batchv1.CronJob
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name + "-backup"}, &cj); err != nil {
		t.Fatalf("cronjob not created: %v", err)
	}
	bv, ok := volByName(cj.Spec.JobTemplate.Spec.Template.Spec.Volumes, "backups")
	if !ok || bv.PersistentVolumeClaim == nil || bv.PersistentVolumeClaim.ClaimName != "my-backups" {
		t.Errorf("backups volume must use the existingClaim: %+v", bv)
	}
}

// TestReconcile_BackupRemovedGCsCronJob asserts removing spec.backup deletes the
// owned CronJob.
func TestReconcile_BackupRemovedGCsCronJob(t *testing.T) {
	cp := sampleCP()
	cp.Spec.Backup = &opsv1alpha1.BackupSpec{Schedule: "0 3 * * *", KEKSecret: "dr-kek"}
	r, c := newReconciler(t, cp)
	nn := types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name}
	reconcileOnce(t, r, nn)

	var live opsv1alpha1.ControlPlane
	if err := c.Get(context.Background(), nn, &live); err != nil {
		t.Fatalf("get cp: %v", err)
	}
	live.Spec.Backup = nil
	if err := c.Update(context.Background(), &live); err != nil {
		t.Fatalf("update: %v", err)
	}
	reconcileOnce(t, r, nn)

	var cj batchv1.CronJob
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name + "-backup"}, &cj); err == nil {
		t.Errorf("backup CronJob should be GC'd when spec.backup is removed")
	}
}

// TestReconcile_HATransitionRequiresRecreate covers the silent-wedge hazard: an
// existing single-writer ControlPlane scaled into HA keeps its immutable
// OrderedReady podManagementPolicy (standbys never Ready → the scale-up would
// wedge). The controller cannot patch the field, so it must surface
// Degraded/HARequiresRecreate instead of letting the rollout hang invisibly.
func TestReconcile_HATransitionRequiresRecreate(t *testing.T) {
	cp := sampleCP()
	cp.Spec.Engine = opsv1alpha1.EnginePostgres
	cp.Spec.Replicas = 1
	cp.Spec.Postgres = &opsv1alpha1.PostgresSpec{DSNSecret: "pg-dsn"}
	r, c := newReconciler(t, cp)
	nn := types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name}
	reconcileOnce(t, r, nn)

	if got := getSTS(t, c, nn).Spec.PodManagementPolicy; got != appsv1.OrderedReadyPodManagement {
		t.Fatalf("precondition: single-replica postgres should be OrderedReady, got %q", got)
	}

	// Scale into HA (the documented path): 3 replicas + a shared audit key. Valid.
	var live opsv1alpha1.ControlPlane
	if err := c.Get(context.Background(), nn, &live); err != nil {
		t.Fatalf("get: %v", err)
	}
	live.Spec.Replicas = 3
	live.Spec.AuditSigningKeySecret = "audit"
	live.Generation = 2
	if err := c.Update(context.Background(), &live); err != nil {
		t.Fatalf("update: %v", err)
	}
	reconcileOnce(t, r, nn)

	sts := getSTS(t, c, nn)
	if *sts.Spec.Replicas != 3 {
		t.Errorf("replicas = %d, want 3 (bumped)", *sts.Spec.Replicas)
	}
	if sts.Spec.PodManagementPolicy != appsv1.OrderedReadyPodManagement {
		t.Errorf("podManagementPolicy = %q; it is immutable and cannot become Parallel in place", sts.Spec.PodManagementPolicy)
	}
	if err := c.Get(context.Background(), nn, &live); err != nil {
		t.Fatalf("get: %v", err)
	}
	if conditionStatus(live.Status.Conditions, opsv1alpha1.ConditionDegraded) != metav1.ConditionTrue {
		t.Fatalf("HA-on-OrderedReady must surface Degraded=True, not wedge silently")
	}
	if r := conditionReason(live.Status.Conditions, opsv1alpha1.ConditionDegraded); r != "HARequiresRecreate" {
		t.Errorf("Degraded reason = %q, want HARequiresRecreate", r)
	}
}

// TestReconcile_PersistenceHonored asserts spec.persistence flows into the data
// volumeClaimTemplate (size/storageClass/accessModes), with the chart's "-"
// storageClass sentinel disabling dynamic provisioning.
func TestReconcile_PersistenceHonored(t *testing.T) {
	cp := sampleCP()
	cp.Spec.Persistence = &opsv1alpha1.PersistenceSpec{
		Size:         "50Gi",
		StorageClass: "fast-ssd",
		AccessModes:  []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod},
	}
	r, c := newReconciler(t, cp)
	nn := types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name}
	reconcileOnce(t, r, nn)

	vct := getSTS(t, c, nn).Spec.VolumeClaimTemplates
	if len(vct) != 1 {
		t.Fatalf("want 1 volumeClaimTemplate, got %d", len(vct))
	}
	if got := vct[0].Spec.Resources.Requests.Storage().String(); got != "50Gi" {
		t.Errorf("data PVC size = %q, want 50Gi", got)
	}
	if sc := vct[0].Spec.StorageClassName; sc == nil || *sc != "fast-ssd" {
		t.Errorf("data PVC storageClass = %v, want fast-ssd", sc)
	}
	if len(vct[0].Spec.AccessModes) != 1 || vct[0].Spec.AccessModes[0] != corev1.ReadWriteOncePod {
		t.Errorf("data PVC accessModes = %v, want [ReadWriteOncePod]", vct[0].Spec.AccessModes)
	}

	// "-" sentinel → empty storageClassName (pre-bound PV, no dynamic provisioning).
	cp2 := sampleCP()
	cp2.Name = "dash"
	cp2.Spec.Persistence = &opsv1alpha1.PersistenceSpec{StorageClass: "-"}
	r2, c2 := newReconciler(t, cp2)
	nn2 := types.NamespacedName{Namespace: cp2.Namespace, Name: cp2.Name}
	reconcileOnce(t, r2, nn2)
	sc := getSTS(t, c2, nn2).Spec.VolumeClaimTemplates[0].Spec.StorageClassName
	if sc == nil || *sc != "" {
		t.Errorf("storageClass '-' should yield empty storageClassName, got %v", sc)
	}
}

// TestReconcile_BackupMountsCatalogKey asserts the backup job mounts the catalog
// key too (so its manifest boot uses the shared key instead of minting a throwaway
// into the operand PVC).
func TestReconcile_BackupMountsCatalogKey(t *testing.T) {
	cp := postgresHACP()
	cp.Spec.Postgres.AdminDSNKey = "admin-dsn" // postgres backups require the BYPASSRLS admin DSN
	cp.Spec.Backup = &opsv1alpha1.BackupSpec{Schedule: "0 3 * * *", KEKSecret: "dr-kek"}
	r, c := newReconciler(t, cp)
	nn := types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name}
	reconcileOnce(t, r, nn)

	var cj batchv1.CronJob
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name + "-backup"}, &cj); err != nil {
		t.Fatalf("cronjob: %v", err)
	}
	pod := cj.Spec.JobTemplate.Spec.Template.Spec
	back, _ := containerByName(pod.Containers, "dr-backup")
	if _, ok := mountByName(back.VolumeMounts, "catalog-key"); !ok {
		t.Errorf("backup must mount catalog-key: %+v", back.VolumeMounts)
	}
	if e, ok := envByName(back.Env, "OLIVARES_CATALOG_SIGNING_KEY_FILE"); !ok || e.Value == "" {
		t.Errorf("backup missing OLIVARES_CATALOG_SIGNING_KEY_FILE: %+v", back.Env)
	}
	cv, ok := volByName(pod.Volumes, "catalog-key")
	if !ok || cv.Secret == nil || cv.Secret.Optional == nil || !*cv.Secret.Optional {
		t.Errorf("catalog-key volume must be an optional Secret projection: %+v", cv)
	}
}

func conditionStatus(conds []metav1.Condition, condType string) metav1.ConditionStatus {
	for _, c := range conds {
		if c.Type == condType {
			return c.Status
		}
	}
	return metav1.ConditionUnknown
}

func conditionReason(conds []metav1.Condition, condType string) string {
	for _, c := range conds {
		if c.Type == condType {
			return c.Reason
		}
	}
	return ""
}
