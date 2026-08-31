// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	opsv1alpha1 "github.com/olivaresai/olivares/operator/api/v1alpha1"
)

// haSpecCP is a valid 3-replica active-passive HA ControlPlane the reconciler can
// materialize (postgres DSN + shared audit key), in the requested HA layout.
func haSpecCP(routing opsv1alpha1.HARoutingMode) *opsv1alpha1.ControlPlane {
	cp := sampleCP()
	cp.Spec.Engine = opsv1alpha1.EnginePostgres
	cp.Spec.Replicas = 3
	cp.Spec.Postgres = &opsv1alpha1.PostgresSpec{DSNSecret: "pg-dsn"}
	cp.Spec.AuditSigningKeySecret = "audit-key"
	cp.Spec.HARouting = routing
	return cp
}

func nnOf(cp *opsv1alpha1.ControlPlane) types.NamespacedName {
	return types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name}
}

// TestLeaderRouting_ObjectShape is the object-shape contract of the leader-routing
// layout (design §B.1): pod-health readiness, a leader-selecting client Service,
// and the operand's OWN narrowly-scoped credential — the pod may `get,patch` only
// the pods of its own StatefulSet, nothing else.
func TestLeaderRouting_ObjectShape(t *testing.T) {
	cp := haSpecCP(opsv1alpha1.HARoutingLeader)
	r, c := newReconciler(t, cp)
	reconcileOnce(t, r, nnOf(cp))

	// --- readinessProbe: pod health, so a hot standby is Ready and the rolling
	// update can progress past it (the wedge fix). ---
	sts := getSTS(t, c, nnOf(cp))
	core, ok := containerByName(sts.Spec.Template.Spec.Containers, containerName)
	if !ok {
		t.Fatal("core container missing")
	}
	if got := core.ReadinessProbe.HTTPGet.Path; got != podReadyzPath {
		t.Errorf("HA readinessProbe = %q, want %q", got, podReadyzPath)
	}
	if got := core.LivenessProbe.HTTPGet.Path; got != "/livez" {
		t.Errorf("livenessProbe = %q, want /livez (unchanged)", got)
	}

	// --- the engine needs an identity + the publish switch to label its own pod ---
	for _, want := range []string{haLeaderLabelEnv, "POD_NAME", "POD_NAMESPACE"} {
		if _, ok := envByName(core.Env, want); !ok {
			t.Errorf("env %s missing; the engine cannot publish its role label", want)
		}
	}
	podName, _ := envByName(core.Env, "POD_NAME")
	if podName.ValueFrom == nil || podName.ValueFrom.FieldRef == nil || podName.ValueFrom.FieldRef.FieldPath != "metadata.name" {
		t.Errorf("POD_NAME = %+v, want a downward-API fieldRef to metadata.name", podName)
	}
	if sts.Spec.Template.Spec.ServiceAccountName != leaderPublisherName(cp) {
		t.Errorf("serviceAccountName = %q, want %q", sts.Spec.Template.Spec.ServiceAccountName, leaderPublisherName(cp))
	}
	if am := sts.Spec.Template.Spec.AutomountServiceAccountToken; am == nil || !*am {
		t.Error("the projected ServiceAccount token must be mounted in the leader-routing layout")
	}

	// --- Services: governing headless (all pods) + leader-selecting client ---
	var governing corev1.Service
	if err := c.Get(context.Background(), nnOf(cp), &governing); err != nil {
		t.Fatalf("governing Service: %v", err)
	}
	if governing.Spec.ClusterIP != corev1.ClusterIPNone {
		t.Errorf("governing Service ClusterIP = %q, want headless", governing.Spec.ClusterIP)
	}
	if _, has := governing.Spec.Selector[haRoleLabelKey]; has {
		t.Error("the governing Service must select ALL workload pods (stable per-pod DNS), never only the leader")
	}

	var leader corev1.Service
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: cp.Namespace, Name: leaderServiceName(cp)}, &leader); err != nil {
		t.Fatalf("leader Service: %v", err)
	}
	if leader.Spec.ClusterIP == corev1.ClusterIPNone {
		t.Error("the leader Service must be a normal ClusterIP Service (the client endpoint)")
	}
	if leader.Spec.Selector[haRoleLabelKey] != haRoleLeader {
		t.Errorf("leader Service selector = %v, want %s=%s", leader.Spec.Selector, haRoleLabelKey, haRoleLeader)
	}
	for k, v := range labelsFor(cp) {
		if leader.Spec.Selector[k] != v {
			t.Errorf("leader Service selector missing workload label %s=%s", k, v)
		}
	}
	if len(leader.Spec.Ports) != 2 {
		t.Errorf("leader Service ports = %d, want https + grpc", len(leader.Spec.Ports))
	}

	// --- operand RBAC: get,patch on exactly this StatefulSet's pods ---
	var role rbacv1.Role
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: cp.Namespace, Name: leaderPublisherName(cp)}, &role); err != nil {
		t.Fatalf("publisher Role: %v", err)
	}
	if len(role.Rules) != 1 {
		t.Fatalf("publisher Role rules = %d, want exactly one", len(role.Rules))
	}
	rule := role.Rules[0]
	if got, want := rule.Verbs, []string{"get", "patch"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("publisher verbs = %v, want %v (never create/delete/list)", got, want)
	}
	if len(rule.Resources) != 1 || rule.Resources[0] != "pods" {
		t.Errorf("publisher resources = %v, want [pods]", rule.Resources)
	}
	wantNames := []string{"test-0", "test-1", "test-2"}
	if len(rule.ResourceNames) != len(wantNames) {
		t.Fatalf("publisher resourceNames = %v, want %v (pinned to this StatefulSet's pods)", rule.ResourceNames, wantNames)
	}
	for i, n := range wantNames {
		if rule.ResourceNames[i] != n {
			t.Errorf("publisher resourceNames[%d] = %q, want %q", i, rule.ResourceNames[i], n)
		}
	}

	var sa corev1.ServiceAccount
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: cp.Namespace, Name: leaderPublisherName(cp)}, &sa); err != nil {
		t.Fatalf("publisher ServiceAccount: %v", err)
	}
	var rb rbacv1.RoleBinding
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: cp.Namespace, Name: leaderPublisherName(cp)}, &rb); err != nil {
		t.Fatalf("publisher RoleBinding: %v", err)
	}
	if len(rb.Subjects) != 1 || rb.Subjects[0].Name != leaderPublisherName(cp) || rb.Subjects[0].Kind != "ServiceAccount" {
		t.Errorf("RoleBinding subjects = %+v, want the publisher ServiceAccount", rb.Subjects)
	}
	if rb.RoleRef.Kind != "Role" || rb.RoleRef.Name != leaderPublisherName(cp) {
		t.Errorf("RoleBinding roleRef = %+v, want the namespaced publisher Role", rb.RoleRef)
	}
}

// TestLegacyLayout_GrantsNoKubernetesPrivilege pins the OPERAND's blast radius:
// without the explicit opt-in the engine keeps ZERO Kubernetes API access, keeps
// the leader-only readiness probe, and no second Service appears — so a workload
// that never enables the layout is not touched.
//
// It is NOT a claim about the operator itself: installing this version widens the
// MANAGER's ClusterRole (serviceaccounts/roles/rolebindings, pods get,list,watch,
// patch) and starts Pod/ConfigMap/Secret informers regardless of any opt-in,
// because it must be able to provision the per-instance credential the moment
// someone does opt in. That deployment-level change is documented in
// docs/HA-LEADER-ROUTING.md §2 rather than hidden behind this test's name.
func TestLegacyLayout_GrantsNoKubernetesPrivilege(t *testing.T) {
	for _, tc := range []struct {
		name string
		cp   *opsv1alpha1.ControlPlane
	}{
		{"legacy HA", haSpecCP(opsv1alpha1.HARoutingLegacy)},
		{"HA with an unset routing mode", func() *opsv1alpha1.ControlPlane {
			cp := haSpecCP("")
			return cp
		}()},
		{"single-node sqlite", sampleCP()},
		{"single-replica postgres asking for leader routing", func() *opsv1alpha1.ControlPlane {
			cp := haSpecCP(opsv1alpha1.HARoutingLeader)
			cp.Spec.Replicas = 1 // not HA: the layout is meaningless and must not be applied
			return cp
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, c := newReconciler(t, tc.cp)
			reconcileOnce(t, r, nnOf(tc.cp))

			sts := getSTS(t, c, nnOf(tc.cp))
			core, _ := containerByName(sts.Spec.Template.Spec.Containers, containerName)
			if got := core.ReadinessProbe.HTTPGet.Path; got != readyzPath {
				t.Errorf("readinessProbe = %q, want %q (leader-only drain preserved)", got, readyzPath)
			}
			if _, ok := envByName(core.Env, haLeaderLabelEnv); ok {
				t.Error("label publishing must not be enabled without spec.haRouting=LeaderRouting")
			}
			if sts.Spec.Template.Spec.ServiceAccountName != "" {
				t.Errorf("serviceAccountName = %q, want none (no Kubernetes API access)", sts.Spec.Template.Spec.ServiceAccountName)
			}
			assertAbsent(t, c, &corev1.Service{}, tc.cp.Namespace, leaderServiceName(tc.cp))
			assertAbsent(t, c, &corev1.ServiceAccount{}, tc.cp.Namespace, leaderPublisherName(tc.cp))
			assertAbsent(t, c, &rbacv1.Role{}, tc.cp.Namespace, leaderPublisherName(tc.cp))
			assertAbsent(t, c, &rbacv1.RoleBinding{}, tc.cp.Namespace, leaderPublisherName(tc.cp))
		})
	}
}

// TestLeaderRouting_ExistingHAMigratesInPhases is the staged migration of a LIVE
// legacy HA deployment (design §B.1 "Existing-StatefulSet migration"). Flipping the
// readiness probe in one step is not zero-downtime in either ordering: the old
// leader cannot publish the new label (so the leader Service starts empty), while
// the first replaced standby becomes pod-Ready and joins the legacy Service, where
// clients that have not moved reach it and get 503s. So the operator PREPARES the
// destination and refuses to touch the pod template until the cut-over is
// acknowledged.
func TestLeaderRouting_ExistingHAMigratesInPhases(t *testing.T) {
	cp := haSpecCP(opsv1alpha1.HARoutingLegacy)
	r, c := newReconciler(t, cp)
	reconcileOnce(t, r, nnOf(cp)) // a live, legacy-layout StatefulSet now exists

	// --- the operator asks for the new layout ---
	var live opsv1alpha1.ControlPlane
	if err := c.Get(context.Background(), nnOf(cp), &live); err != nil {
		t.Fatal(err)
	}
	live.Spec.HARouting = opsv1alpha1.HARoutingLeader
	if err := c.Update(context.Background(), &live); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, nnOf(cp))

	// PREPARE: destination exists…
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: cp.Namespace, Name: leaderServiceName(cp)}, &corev1.Service{}); err != nil {
		t.Fatalf("the leader Service must be prepared before the cut-over: %v", err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: cp.Namespace, Name: leaderPublisherName(cp)}, &rbacv1.Role{}); err != nil {
		t.Fatalf("the publisher Role must be prepared before the cut-over: %v", err)
	}
	// …and the pod template is UNTOUCHED, so no traffic moves and nothing rolls.
	sts := getSTS(t, c, nnOf(cp))
	core, _ := containerByName(sts.Spec.Template.Spec.Containers, containerName)
	if got := core.ReadinessProbe.HTTPGet.Path; got != readyzPath {
		t.Errorf("readinessProbe during the prepare phase = %q, want %q (the template must not move yet)", got, readyzPath)
	}
	if sts.Spec.Template.Spec.ServiceAccountName != "" {
		t.Errorf("serviceAccountName during the prepare phase = %q, want none", sts.Spec.Template.Spec.ServiceAccountName)
	}
	if err := c.Get(context.Background(), nnOf(cp), &live); err != nil {
		t.Fatal(err)
	}
	deg := conditionByType(live.Status.Conditions, opsv1alpha1.ConditionDegraded)
	if deg == nil || deg.Status != metav1.ConditionTrue || deg.Reason != reasonHALeaderServiceMigrationRequired {
		t.Fatalf("Degraded = %+v, want True/%s telling the operator what to do", deg, reasonHALeaderServiceMigrationRequired)
	}

	// --- COMMIT: the administrator confirms clients moved to the leader Service ---
	if live.Annotations == nil {
		live.Annotations = map[string]string{}
	}
	live.Annotations[leaderCutoverAnnotation] = "acknowledged"
	if err := c.Update(context.Background(), &live); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, nnOf(cp))

	sts = getSTS(t, c, nnOf(cp))
	core, _ = containerByName(sts.Spec.Template.Spec.Containers, containerName)
	if got := core.ReadinessProbe.HTTPGet.Path; got != podReadyzPath {
		t.Errorf("readinessProbe after the acknowledged cut-over = %q, want %q", got, podReadyzPath)
	}
	if sts.Spec.Template.Spec.ServiceAccountName != leaderPublisherName(cp) {
		t.Errorf("serviceAccountName after the cut-over = %q, want the publisher", sts.Spec.Template.Spec.ServiceAccountName)
	}
}

// TestLeaderRouting_FreshInstallSkipsMigration: the staged migration exists to
// protect a RUNNING deployment. A fresh install has no clients and no live
// StatefulSet, so it is created in the split shape directly, with no acknowledgement
// and no Degraded condition.
func TestLeaderRouting_FreshInstallSkipsMigration(t *testing.T) {
	cp := haSpecCP(opsv1alpha1.HARoutingLeader)
	r, c := newReconciler(t, cp)
	reconcileOnce(t, r, nnOf(cp))

	sts := getSTS(t, c, nnOf(cp))
	core, _ := containerByName(sts.Spec.Template.Spec.Containers, containerName)
	if got := core.ReadinessProbe.HTTPGet.Path; got != podReadyzPath {
		t.Errorf("fresh install readinessProbe = %q, want %q", got, podReadyzPath)
	}
	var live opsv1alpha1.ControlPlane
	if err := c.Get(context.Background(), nnOf(cp), &live); err != nil {
		t.Fatal(err)
	}
	if deg := conditionByType(live.Status.Conditions, opsv1alpha1.ConditionDegraded); deg != nil && deg.Reason == reasonHALeaderServiceMigrationRequired {
		t.Error("a fresh install must not require a client cut-over acknowledgement")
	}
}

// TestReconcile_NeverTouchesForeignObjects: reverting (or never enabling) the layout
// must not delete a same-named object this ControlPlane does not own, and the
// opt-in path must not silently ADOPT one. Both directions are destructive to
// someone else's workload.
func TestReconcile_NeverTouchesForeignObjects(t *testing.T) {
	cp := haSpecCP(opsv1alpha1.HARoutingLegacy)
	foreign := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: leaderServiceName(cp), Namespace: cp.Namespace,
			Labels: map[string]string{"app": "someone-else"},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "http", Port: 80}}},
	}
	r, c := newReconciler(t, cp, foreign)
	reconcileOnce(t, r, nnOf(cp))

	var still corev1.Service
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: cp.Namespace, Name: leaderServiceName(cp)}, &still); err != nil {
		t.Fatalf("the operator deleted a Service it does not own: %v", err)
	}
	if still.Labels["app"] != "someone-else" {
		t.Errorf("the foreign Service was mutated: %+v", still.Labels)
	}

	// Enabling the layout must FAIL LOUDLY rather than take the object over.
	var live opsv1alpha1.ControlPlane
	if err := c.Get(context.Background(), nnOf(cp), &live); err != nil {
		t.Fatal(err)
	}
	live.Spec.HARouting = opsv1alpha1.HARoutingLeader
	if err := c.Update(context.Background(), &live); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: nnOf(cp)}); err == nil {
		t.Fatal("reconcile silently adopted a foreign Service; it must report the collision instead")
	}
}

// TestConfigHashFormatChangeDoesNotRollExistingPods is the blast-radius contract of
// the digest fix: upgrading the operator must not perturb a workload whose owner
// changed nothing — least of all a legacy HA one, whose rolling update cannot
// finish. The pod template moves only when the referenced CONFIGURATION changes.
func TestConfigHashFormatChangeDoesNotRollExistingPods(t *testing.T) {
	cp := haSpecCP(opsv1alpha1.HARoutingLegacy)
	cp.Spec.ConfigRef = "engine-config"
	cm := configMap(cp.Namespace, "engine-config", map[string]string{"OLIVARES_LOG_LEVEL": "info", "B": "2"})
	r, c := newReconciler(t, cp, cm)
	reconcileOnce(t, r, nnOf(cp))

	// Simulate a StatefulSet written by an OLDER operator: an annotation in the old
	// format and no record of which digest produced it.
	sts := getSTS(t, c, nnOf(cp))
	sts.Spec.Template.Annotations[configHashAnnotation] = "0ldf0rmatd1gest"
	delete(sts.Annotations, configHashSourceAnnotation)
	if err := c.Update(context.Background(), &sts); err != nil {
		t.Fatal(err)
	}

	reconcileOnce(t, r, nnOf(cp))
	sts = getSTS(t, c, nnOf(cp))
	if got := sts.Spec.Template.Annotations[configHashAnnotation]; got != "0ldf0rmatd1gest" {
		t.Fatalf("the operator upgrade rewrote the pod template annotation (%q); that rolls pods nobody asked to roll", got)
	}
	if sts.Annotations[configHashSourceAnnotation] == "" {
		t.Error("the adopted digest was not recorded, so the next reconcile cannot tell a real config change from the format change")
	}

	// A REAL configuration change still rolls.
	cm.Data["OLIVARES_LOG_LEVEL"] = "debug"
	if err := c.Update(context.Background(), cm); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, nnOf(cp))
	sts = getSTS(t, c, nnOf(cp))
	if got := sts.Spec.Template.Annotations[configHashAnnotation]; got == "0ldf0rmatd1gest" {
		t.Error("editing the referenced ConfigMap did not roll the pod template")
	}
}

// TestLeaderRouting_RevertRevokesCredential proves the layout is reversible AND
// that reverting actually REVOKES the operand's Kubernetes credential rather than
// leaving it dormant on the cluster.
func TestLeaderRouting_RevertRevokesCredential(t *testing.T) {
	cp := haSpecCP(opsv1alpha1.HARoutingLeader)
	r, c := newReconciler(t, cp)
	reconcileOnce(t, r, nnOf(cp))

	var live opsv1alpha1.ControlPlane
	if err := c.Get(context.Background(), nnOf(cp), &live); err != nil {
		t.Fatal(err)
	}
	live.Spec.HARouting = opsv1alpha1.HARoutingLegacy
	if err := c.Update(context.Background(), &live); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, nnOf(cp))

	assertAbsent(t, c, &corev1.Service{}, cp.Namespace, leaderServiceName(cp))
	assertAbsent(t, c, &rbacv1.RoleBinding{}, cp.Namespace, leaderPublisherName(cp))
	assertAbsent(t, c, &rbacv1.Role{}, cp.Namespace, leaderPublisherName(cp))
	assertAbsent(t, c, &corev1.ServiceAccount{}, cp.Namespace, leaderPublisherName(cp))

	sts := getSTS(t, c, nnOf(cp))
	core, _ := containerByName(sts.Spec.Template.Spec.Containers, containerName)
	if got := core.ReadinessProbe.HTTPGet.Path; got != readyzPath {
		t.Errorf("readinessProbe after revert = %q, want %q", got, readyzPath)
	}
}

// TestLeaderRouting_ScaleDownKeepsTerminatingPodAuthorized covers the RBAC ordering
// contract: resourceNames spans the union of desired and observed replicas, so a
// pod that is scaling away can still demote its own label instead of being 403'd.
func TestLeaderRouting_ScaleDownKeepsTerminatingPodAuthorized(t *testing.T) {
	cp := haSpecCP(opsv1alpha1.HARoutingLeader)
	r, c := newReconciler(t, cp)
	reconcileOnce(t, r, nnOf(cp))

	var live opsv1alpha1.ControlPlane
	if err := c.Get(context.Background(), nnOf(cp), &live); err != nil {
		t.Fatal(err)
	}
	live.Spec.Replicas = 2
	if err := c.Update(context.Background(), &live); err != nil {
		t.Fatal(err)
	}
	// The three pods still EXIST — pod-2 is draining. Authorization must follow the
	// pods that are really there, not a replica counter that can drop first.
	for i, role := range []string{haRoleLeader, "standby", "standby"} {
		if err := c.Create(context.Background(), workloadPod(cp, i, role, true, cp.Spec.Image)); err != nil {
			t.Fatal(err)
		}
	}
	reconcileOnce(t, r, nnOf(cp))

	var role rbacv1.Role
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: cp.Namespace, Name: leaderPublisherName(cp)}, &role); err != nil {
		t.Fatal(err)
	}
	names := role.Rules[0].ResourceNames
	if len(names) != 3 || names[2] != "test-2" {
		t.Errorf("resourceNames = %v, want the draining pod-2 still authorized to demote its own label", names)
	}
}

// TestPodObservationFailureIsNotAVerdict: the leader-route predicate is a ROUTING
// fact — "exactly one Ready pod publishes the leader label" — and StatefulSet
// counters cannot stand in for it. If the pod list fails, falling back to
// ReadyReplicas>0 would report a converged, Available control plane whose leader
// Service has no endpoint at all. The reconcile must fail and retry instead.
func TestPodObservationFailureIsNotAVerdict(t *testing.T) {
	cp := haSpecCP(opsv1alpha1.HARoutingLeader)
	s := newScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&opsv1alpha1.ControlPlane{}).
		WithObjects(cp).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*corev1.PodList); ok {
					return errors.New("apiserver unavailable")
				}
				return cl.List(ctx, list, opts...)
			},
		}).
		Build()
	r := &ControlPlaneReconciler{Client: c, Scheme: s}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: nnOf(cp)}); err == nil {
		t.Fatal("reconcile succeeded with an unreadable pod list; it must not infer routability from counters")
	}
}

// TestLeaderRouting_StatusReachesReady is the end-to-end status contract this whole
// unit exists for: a healthy 3-replica HA control plane in the leader-routing
// layout reports PhaseReady — which is impossible in the legacy layout — and names
// the pod the leader Service resolves to.
func TestLeaderRouting_StatusReachesReady(t *testing.T) {
	cp := haSpecCP(opsv1alpha1.HARoutingLeader)
	r, c := newReconciler(t, cp)
	reconcileOnce(t, r, nnOf(cp))

	// Simulate the converged cluster: the StatefulSet controller reports a settled
	// rollout, three Ready pods exist, and exactly one publishes the leader label.
	sts := getSTS(t, c, nnOf(cp))
	sts.Status = statefulSetConverged(3, 3)
	if err := c.Status().Update(context.Background(), &sts); err != nil {
		t.Fatal(err)
	}
	for i, role := range []string{haRoleLeader, "standby", "standby"} {
		if err := c.Create(context.Background(), workloadPod(cp, i, role, true, cp.Spec.Image)); err != nil {
			t.Fatal(err)
		}
	}
	reconcileOnce(t, r, nnOf(cp))

	var live opsv1alpha1.ControlPlane
	if err := c.Get(context.Background(), nnOf(cp), &live); err != nil {
		t.Fatal(err)
	}
	if live.Status.Phase != opsv1alpha1.PhaseReady {
		t.Fatalf("phase = %q, want %q (a healthy HA control plane must be able to reach Ready)", live.Status.Phase, opsv1alpha1.PhaseReady)
	}
	if live.Status.CurrentImage != cp.Spec.Image {
		t.Errorf("currentImage = %q, want the rolled image %q", live.Status.CurrentImage, cp.Spec.Image)
	}
	if live.Status.LeaderPod != "test-0" {
		t.Errorf("leaderPod = %q, want test-0", live.Status.LeaderPod)
	}
	if c := conditionByType(live.Status.Conditions, opsv1alpha1.ConditionDegraded); c == nil || c.Status != metav1.ConditionFalse {
		t.Errorf("Degraded = %+v, want False on a healthy leader-routing control plane", c)
	}
	if c := conditionByType(live.Status.Conditions, opsv1alpha1.ConditionAvailable); c == nil || c.Status != metav1.ConditionTrue {
		t.Errorf("Available = %+v, want True", c)
	}
}

// TestReconcile_SettledStatusIsIdempotent guards a feedback loop that only shows up
// in a real cluster: the reconciler watches its own ControlPlane, so every status
// write wakes it again. If a settled ControlPlane rewrote any status field per
// reconcile — the rollout-progress timestamp is the tempting one — the controller
// would spin forever on a healthy object. Two consecutive reconciles of a converged
// control plane must produce byte-identical status.
func TestReconcile_SettledStatusIsIdempotent(t *testing.T) {
	cp := haSpecCP(opsv1alpha1.HARoutingLeader)
	r, c := newReconciler(t, cp)
	reconcileOnce(t, r, nnOf(cp))

	sts := getSTS(t, c, nnOf(cp))
	sts.Status = statefulSetConverged(3, 3)
	if err := c.Status().Update(context.Background(), &sts); err != nil {
		t.Fatal(err)
	}
	for i, role := range []string{haRoleLeader, "standby", "standby"} {
		if err := c.Create(context.Background(), workloadPod(cp, i, role, true, cp.Spec.Image)); err != nil {
			t.Fatal(err)
		}
	}
	reconcileOnce(t, r, nnOf(cp))

	var first opsv1alpha1.ControlPlane
	if err := c.Get(context.Background(), nnOf(cp), &first); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, nnOf(cp))
	var second opsv1alpha1.ControlPlane
	if err := c.Get(context.Background(), nnOf(cp), &second); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(first.Status, second.Status) {
		t.Errorf("status changed between two reconciles of a settled ControlPlane:\n first: %+v\nsecond: %+v", first.Status, second.Status)
	}
}

// TestLeaderRouting_StatusLeaderNotPublished proves the operator does not report a
// converged-but-unroutable control plane as Ready: with no leader label there is no
// Service endpoint, so clients cannot reach the writer at all.
func TestLeaderRouting_StatusLeaderNotPublished(t *testing.T) {
	cp := haSpecCP(opsv1alpha1.HARoutingLeader)
	r, c := newReconciler(t, cp)
	reconcileOnce(t, r, nnOf(cp))

	sts := getSTS(t, c, nnOf(cp))
	sts.Status = statefulSetConverged(3, 3)
	if err := c.Status().Update(context.Background(), &sts); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if err := c.Create(context.Background(), workloadPod(cp, i, "standby", true, cp.Spec.Image)); err != nil {
			t.Fatal(err)
		}
	}
	reconcileOnce(t, r, nnOf(cp))

	var live opsv1alpha1.ControlPlane
	if err := c.Get(context.Background(), nnOf(cp), &live); err != nil {
		t.Fatal(err)
	}
	if live.Status.Phase == opsv1alpha1.PhaseReady {
		t.Fatal("phase = Ready with no published leader; the leader Service has no endpoint")
	}
	if live.Status.LeaderPod != "" {
		t.Errorf("leaderPod = %q, want empty", live.Status.LeaderPod)
	}
	deg := conditionByType(live.Status.Conditions, opsv1alpha1.ConditionDegraded)
	if deg == nil || deg.Status != metav1.ConditionTrue || deg.Reason != reasonLeaderNotPublished {
		t.Errorf("Degraded = %+v, want True/%s", deg, reasonLeaderNotPublished)
	}
	avail := conditionByType(live.Status.Conditions, opsv1alpha1.ConditionAvailable)
	if avail == nil || avail.Status != metav1.ConditionFalse {
		t.Errorf("Available = %+v, want False (no reachable writer)", avail)
	}
}

// --- helpers ----------------------------------------------------------------

// statefulSetConverged is the status a real StatefulSet controller reports for a
// finished rollout: the latest generation observed, one settled revision, and every
// counter at the desired count.
func statefulSetConverged(desired, ready int32) appsv1.StatefulSetStatus {
	return appsv1.StatefulSetStatus{
		ObservedGeneration: 1,
		Replicas:           desired,
		CurrentReplicas:    desired,
		UpdatedReplicas:    desired,
		ReadyReplicas:      ready,
		CurrentRevision:    "rev-1",
		UpdateRevision:     "rev-1",
	}
}

// workloadPod builds a pod as the StatefulSet would render it: the workload labels
// the operator stamps, the engine-published role label, a Ready condition, and the
// image it actually runs.
func workloadPod(cp *opsv1alpha1.ControlPlane, ordinal int, role string, ready bool, image string) *corev1.Pod {
	labels := labelsFor(cp)
	labels[haRoleLabelKey] = role
	cond := corev1.ConditionFalse
	if ready {
		cond = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", cp.Name, ordinal),
			Namespace: cp.Namespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: containerName, Image: image}}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: cond}},
		},
	}
}

func assertAbsent(t *testing.T, c client.Client, obj client.Object, ns, name string) {
	t.Helper()
	err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, obj)
	if err == nil {
		t.Errorf("%T %s/%s exists, want absent", obj, ns, name)
		return
	}
	if !apierrors.IsNotFound(err) {
		t.Fatalf("get %T %s/%s: %v", obj, ns, name, err)
	}
}

func conditionByType(conds []metav1.Condition, t string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == t {
			return &conds[i]
		}
	}
	return nil
}
