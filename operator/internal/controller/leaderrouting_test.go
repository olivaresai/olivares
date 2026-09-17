// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

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
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

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
//
// "Healthy" now includes the leader ANSWERING that it will take client traffic.
// That is stated here, not simulated: the observation is injected.
func TestLeaderRouting_StatusReachesReady(t *testing.T) {
	cp := haSpecCP(opsv1alpha1.HARoutingLeader)
	r, c := newReconcilerWithProbe(t, &fakeRouteProbe{result: RouteReady}, cp)
	// The converged cluster: the StatefulSet controller reports a settled rollout,
	// three Ready pods exist, and exactly one publishes the leader label.
	convergeLeaderRouting(t, r, c, cp)
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
	r, c := newReconcilerWithProbe(t, &fakeRouteProbe{result: RouteReady}, cp)
	convergeLeaderRouting(t, r, c, cp)
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

// --- Traffic readiness (D2) --------------------------------------------------

// stsUID / leaderPodUID are the identities a real apiserver mints. The fake client
// does not, and the ownership chain this observation depends on is checked by UID,
// so the fixtures carry them explicitly.
const (
	stsUID       types.UID = "sts-uid"
	leaderPodUID types.UID = "pod-0-uid"
)

// fakeRouteProbe is the injected observation. It records every call so a test can
// assert that NO request was made, records the deadline it was handed so a test can
// assert the observation's budget reached it, and can mutate the cluster mid-call to
// model the pod being replaced between the operator's two identity reads.
type fakeRouteProbe struct {
	result RouteReadiness
	calls  []string
	during func()
	// deadlines/unbounded record what the caller's context promised. A prober that
	// is handed an unbounded context has been given no budget at all, whatever the
	// bound it applies internally.
	deadlines []time.Time
	remaining []time.Duration
	unbounded int
}

func (f *fakeRouteProbe) ProbeRouteReadiness(ctx context.Context, namespace, pod string) RouteReadiness {
	f.calls = append(f.calls, namespace+"/"+pod)
	if d, ok := ctx.Deadline(); ok {
		f.deadlines = append(f.deadlines, d)
		f.remaining = append(f.remaining, time.Until(d))
	} else {
		f.unbounded++
	}
	if f.during != nil {
		f.during()
	}
	return f.result
}

// budgetReader wraps the uncached reader to observe the context of every per-pod
// identity read — the two calls the HTTP-only timeout never bounded — and can hold
// one of them to see whether the deadline is real.
type budgetReader struct {
	client.Reader
	deadlines []time.Time
	remaining []time.Duration
	unbounded int
	// hold, when set, blocks the Nth pod read (1-based) until its context is done.
	hold int
	pods int
}

func (b *budgetReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*corev1.Pod); ok {
		b.pods++
		if d, bounded := ctx.Deadline(); bounded {
			b.deadlines = append(b.deadlines, d)
			b.remaining = append(b.remaining, time.Until(d))
		} else {
			b.unbounded++
		}
		if b.hold == b.pods {
			<-ctx.Done()
			return ctx.Err()
		}
	}
	return b.Reader.Get(ctx, key, obj, opts...)
}

// observationFixture is the converged leader-routing cluster, ready for a direct
// call to observeRouteReadiness with the uncached reader instrumented.
type observationFixture struct {
	r     *ControlPlaneReconciler
	cp    opsv1alpha1.ControlPlane
	sts   appsv1.StatefulSet
	pods  podObservation
	probe *fakeRouteProbe
	read  *budgetReader
}

func newObservationFixture(t *testing.T, result RouteReadiness) *observationFixture {
	t.Helper()
	cp := haSpecCP(opsv1alpha1.HARoutingLeader)
	probe := &fakeRouteProbe{result: result}
	r, c := newReconcilerWithProbe(t, probe, cp)
	convergeLeaderRouting(t, r, c, cp)
	f := &observationFixture{r: r, cp: liveCP(t, c, cp), sts: getSTS(t, c, nnOf(cp)), probe: probe}
	pods, err := r.observePods(context.Background(), &f.cp)
	if err != nil {
		t.Fatalf("observe pods: %v", err)
	}
	f.pods = pods
	f.read = &budgetReader{Reader: r.reader()}
	r.APIReader = f.read
	return f
}

func (f *observationFixture) observe(ctx context.Context) RouteReadiness {
	return f.r.observeRouteReadiness(ctx, &f.cp, &f.sts, f.pods)
}

// TestRouteObservation_IsOneBudgetAcrossEveryCall is the correction Root returned
// this work for. The three calls of an observation — read the pod, ask it, read it
// again — all go to the same API server, and any of them can hang. A budget that
// covered only the HTTP leg bounded nothing: the two uncached reads ran under the
// reconcile's own context, which has no deadline at all.
//
// One nested context, taken where the observation is owned, and every call inside
// it carries the SAME deadline.
func TestRouteObservation_IsOneBudgetAcrossEveryCall(t *testing.T) {
	f := newObservationFixture(t, RouteReady)
	if got := f.observe(context.Background()); got != RouteReady {
		t.Fatalf("positive control = %s, want %s", got, RouteReady)
	}
	if f.read.unbounded != 0 || f.probe.unbounded != 0 {
		t.Fatalf("%d ownership reads and %d probes ran with no deadline at all",
			f.read.unbounded, f.probe.unbounded)
	}
	if len(f.read.deadlines) != 2 || len(f.probe.deadlines) != 1 {
		t.Fatalf("deadlines seen: %d ownership reads, %d probes; want 2 and 1",
			len(f.read.deadlines), len(f.probe.deadlines))
	}
	for i, d := range append(append([]time.Time{}, f.read.deadlines...), f.probe.deadlines...) {
		if !d.Equal(f.read.deadlines[0]) {
			t.Errorf("call %d carries deadline %v, want the one shared budget %v", i, d, f.read.deadlines[0])
		}
	}
	// Measured AT the call, so this is exact rather than slack-adjusted: whatever
	// remained when each call started can never exceed the whole budget.
	for i, left := range append(append([]time.Duration{}, f.read.remaining...), f.probe.remaining...) {
		if left > routeObservationBudget {
			t.Errorf("call %d began with %s left, more than the whole %s budget", i, left, routeObservationBudget)
		}
		if left <= 0 {
			t.Errorf("call %d began with %s left: the budget was already spent", i, left)
		}
	}
	if routeObservationBudget > 3*time.Second {
		t.Errorf("routeObservationBudget = %s, want at most 3s (the engine bounds /readyz at 2s)", routeObservationBudget)
	}
}

// TestRouteObservation_EarlierParentDeadlineWins: the budget is a CEILING, never a
// grant. A caller that already promised something tighter keeps its promise.
func TestRouteObservation_EarlierParentDeadlineWins(t *testing.T) {
	f := newObservationFixture(t, RouteReady)
	parent, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	parentDeadline, _ := parent.Deadline()

	if got := f.observe(parent); got != RouteReady {
		t.Fatalf("observation = %s, want %s inside the parent's window", got, RouteReady)
	}
	for i, d := range append(append([]time.Time{}, f.read.deadlines...), f.probe.deadlines...) {
		if !d.Equal(parentDeadline) {
			t.Errorf("call %d deadline = %v, want the parent's earlier %v (the nested budget must not extend it)",
				i, d, parentDeadline)
		}
	}
}

// TestRouteObservation_SlowIdentityReadHonorsTheBudget: the deadline is not
// decoration. A stalled ownership read — before or after the call — ends the
// observation at the budget and reports "unverified", instead of holding the
// reconcile open on an API server that is not answering.
//
// The parent deadline is deliberately short so this costs milliseconds: what is
// being proved is that the readers run UNDER the window and that its expiry ends
// them, and the window's own size is pinned in the budget test above.
func TestRouteObservation_SlowIdentityReadHonorsTheBudget(t *testing.T) {
	for _, tc := range []struct {
		name      string
		hold      int
		wantCalls int
	}{
		{"the read BEFORE the call stalls", 1, 0},
		{"the read AFTER the call stalls", 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newObservationFixture(t, RouteReady)
			f.read.hold = tc.hold
			parent, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()

			start := time.Now()
			if got := f.observe(parent); got != RouteUnknown {
				t.Errorf("observation = %s, want %s when an ownership read never answers", got, RouteUnknown)
			}
			if elapsed := time.Since(start); elapsed > time.Second {
				t.Errorf("a stalled ownership read held the reconcile for %s", elapsed)
			}
			if len(f.probe.calls) != tc.wantCalls {
				t.Errorf("probe calls = %v, want %d", f.probe.calls, tc.wantCalls)
			}
		})
	}
}

// TestRouteObservation_ClosedWindowIsNotAVerdict: an answer observed inside a
// window that has since closed is not evidence, and this refuses ALL of it.
//
// Ready is the obvious one. The verified setup block matters just as much, because
// that verdict is what takes a converged rollout OFF the progress deadline —
// accepting a stale one would silence stall detection on an observation nobody can
// stand behind.
func TestRouteObservation_ClosedWindowIsNotAVerdict(t *testing.T) {
	for _, result := range []RouteReadiness{RouteReady, RouteSetupBlocked} {
		t.Run(result.String(), func(t *testing.T) {
			f := newObservationFixture(t, result)
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			// Cancelled between the answer and the final identity read. The fake
			// client still serves that read, exactly as a cached or racing apiserver
			// might, so nothing else stops this from being accepted.
			f.probe.during = cancel

			if got := f.observe(parent); got != RouteUnknown {
				t.Errorf("observation = %s, want %s: the window closed before the verdict", got, RouteUnknown)
			}
		})
	}
}

// TestLeaderRouting_StaleStaticBlockDoesNotStopTheClock is the same refusal seen
// where it costs something: at the status seam. A setup block observed in a window
// that closed must not reach status, and must not put the ControlPlane on the
// five-minute static cadence — that cadence and the progress-deadline exclusion are
// reserved for a verdict the operator can stand behind.
func TestLeaderRouting_StaleStaticBlockDoesNotStopTheClock(t *testing.T) {
	cp := haSpecCP(opsv1alpha1.HARoutingLeader)
	probe := &fakeRouteProbe{result: RouteSetupBlocked}
	r, c := newReconcilerWithProbe(t, probe, cp)
	convergeLeaderRouting(t, r, c, cp)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	probe.during = cancel

	res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: nnOf(cp)})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	live := liveCP(t, c, cp)
	deg := conditionByType(live.Status.Conditions, opsv1alpha1.ConditionDegraded)
	if deg == nil || deg.Reason != reasonRouteProbeUnknown {
		t.Errorf("Degraded = %+v, want %s: a setup block from a closed window is not verified", deg, reasonRouteProbeUnknown)
	}
	if res.RequeueAfter != progressRequeueInterval {
		t.Errorf("requeueAfter = %s, want %s: only a VERIFIED static block polls slowly",
			res.RequeueAfter, staticStateRequeueInterval)
	}
}

// convergeLeaderRouting drives a leader-routing ControlPlane to the exact state the
// route observation is defined on: a settled 3-replica rollout, a StatefulSet with
// the UID and controller owner a cluster would give it, and three Ready pods OWNED
// by that StatefulSet, of which pod-0 publishes the leader label.
func convergeLeaderRouting(t *testing.T, r *ControlPlaneReconciler, c client.Client, cp *opsv1alpha1.ControlPlane) {
	t.Helper()
	reconcileOnce(t, r, nnOf(cp))

	sts := getSTS(t, c, nnOf(cp))
	sts.UID = stsUID
	if err := c.Update(context.Background(), &sts); err != nil {
		t.Fatalf("stamp the StatefulSet UID: %v", err)
	}
	sts.Status = statefulSetConverged(3, 3)
	if err := c.Status().Update(context.Background(), &sts); err != nil {
		t.Fatalf("statefulset status: %v", err)
	}
	for i, role := range []string{haRoleLeader, "standby", "standby"} {
		pod := workloadPod(cp, i, role, true, cp.Spec.Image)
		pod.UID = types.UID(fmt.Sprintf("%s-uid", pod.Name))
		ownPodBy(pod, &sts)
		if err := c.Create(context.Background(), pod); err != nil {
			t.Fatalf("create pod: %v", err)
		}
	}
}

// ownPodBy stamps the controller owner reference the StatefulSet controller sets on
// the pods it creates.
func ownPodBy(pod *corev1.Pod, sts *appsv1.StatefulSet) {
	pod.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: "apps/v1", Kind: "StatefulSet",
		Name: sts.Name, UID: sts.UID,
		Controller: ptrBool(true), BlockOwnerDeletion: ptrBool(true),
	}}
}

func liveCP(t *testing.T, c client.Client, cp *opsv1alpha1.ControlPlane) opsv1alpha1.ControlPlane {
	t.Helper()
	var live opsv1alpha1.ControlPlane
	if err := c.Get(context.Background(), nnOf(cp), &live); err != nil {
		t.Fatalf("get controlplane: %v", err)
	}
	return live
}

// TestLeaderRouting_SetupBlockedLeaderIsNotReady is the defect, at the seam that
// decides it. A first-boot PostgreSQL install without the cross-tenant
// administrative pool converges completely — three Ready pods, one of them
// publishing the leader label — and its leader answers 503 on /readyz: the setup
// ceremony cannot complete, so no client traffic can be served yet. Reporting that
// as PhaseReady is a control plane telling its operator the install finished.
//
// The second half of the assertion is what makes the fix bounded: the bootstrap
// route must NOT be withdrawn. The leader Service's selector is how an
// administrator reaches POST /v1/setup to fix precisely this, so a "fix" that
// emptied the endpoint would turn an honest status into an unrecoverable install.
func TestLeaderRouting_SetupBlockedLeaderIsNotReady(t *testing.T) {
	cp := haSpecCP(opsv1alpha1.HARoutingLeader)
	probe := &fakeRouteProbe{result: RouteSetupBlocked}
	r, c := newReconcilerWithProbe(t, probe, cp)
	convergeLeaderRouting(t, r, c, cp)
	reconcileOnce(t, r, nnOf(cp))

	live := liveCP(t, c, cp)
	if live.Status.Phase == opsv1alpha1.PhaseReady {
		t.Errorf("phase = %q: the leader refuses client traffic on %s, so the rollout is not Ready", live.Status.Phase, readyzPath)
	}
	if live.Status.Phase != opsv1alpha1.PhaseProgressing {
		t.Errorf("phase = %q, want %q", live.Status.Phase, opsv1alpha1.PhaseProgressing)
	}
	prog := conditionByType(live.Status.Conditions, opsv1alpha1.ConditionProgressing)
	if prog == nil || prog.Status != metav1.ConditionFalse || prog.Reason != reasonSetupBlocked {
		t.Errorf("Progressing = %+v, want False/%s (converged, and not advancing on its own)", prog, reasonSetupBlocked)
	}
	deg := conditionByType(live.Status.Conditions, opsv1alpha1.ConditionDegraded)
	if deg == nil || deg.Status != metav1.ConditionTrue || deg.Reason != reasonSetupBlocked {
		t.Errorf("Degraded = %+v, want True/%s", deg, reasonSetupBlocked)
	}

	// Exactly one request, at the pod the label already elected. The operator never
	// picks a pod of its own.
	if want := []string{cp.Namespace + "/test-0"}; !reflect.DeepEqual(probe.calls, want) {
		t.Errorf("probe calls = %v, want %v", probe.calls, want)
	}

	// The bootstrap route is untouched: the leader Service still selects the label.
	var svc corev1.Service
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: cp.Namespace, Name: leaderServiceName(cp)}, &svc); err != nil {
		t.Fatalf("leader Service: %v", err)
	}
	wantSelector := labelsFor(cp)
	wantSelector[haRoleLabelKey] = haRoleLeader
	if !reflect.DeepEqual(svc.Spec.Selector, wantSelector) {
		t.Errorf("leader Service selector = %v, want %v: the bootstrap route is how POST /v1/setup is reached", svc.Spec.Selector, wantSelector)
	}
	// Available answers reachability, not completion: the endpoint exists.
	if avail := conditionByType(live.Status.Conditions, opsv1alpha1.ConditionAvailable); avail == nil || avail.Status != metav1.ConditionTrue {
		t.Errorf("Available = %+v, want True: the leader Service still has its endpoint", avail)
	}

	// Nothing the engine said travels into status. The reasons are fixed names and
	// the messages are the operator's own.
	for _, cond := range live.Status.Conditions {
		for _, leak := range []string{"setup_blocked", "cross_tenant_admin_pool_not_configured", "store", "leader\":"} {
			if strings.Contains(cond.Message, leak) {
				t.Errorf("condition %s message carries response text %q: %s", cond.Type, leak, cond.Message)
			}
		}
	}
}

// TestLeaderRouting_RouteObservationDecidesReadiness walks the vocabulary at the
// seam that writes status. Each observation has ONE consequence, and none of them
// is "believe the label": the rollout is converged and identical in every case, so
// the only thing deciding Ready here is what the leader answered.
func TestLeaderRouting_RouteObservationDecidesReadiness(t *testing.T) {
	tests := []struct {
		name       string
		probe      *fakeRouteProbe
		wantPhase  string
		wantReason string
		wantCalls  int
	}{
		{
			name: "the leader will take traffic", probe: &fakeRouteProbe{result: RouteReady},
			wantPhase: opsv1alpha1.PhaseReady, wantReason: reasonRolloutComplete, wantCalls: 1,
		},
		{
			name: "first setup cannot complete", probe: &fakeRouteProbe{result: RouteSetupBlocked},
			wantPhase: opsv1alpha1.PhaseProgressing, wantReason: reasonSetupBlocked, wantCalls: 1,
		},
		{
			name: "the labeled pod refuses traffic", probe: &fakeRouteProbe{result: RouteNotReady},
			wantPhase: opsv1alpha1.PhaseProgressing, wantReason: reasonRouteNotReady, wantCalls: 1,
		},
		{
			name: "the manager may not observe", probe: &fakeRouteProbe{result: RouteForbidden},
			wantPhase: opsv1alpha1.PhaseProgressing, wantReason: reasonRouteProbeForbidden, wantCalls: 1,
		},
		{
			name: "the observation failed", probe: &fakeRouteProbe{result: RouteUnknown},
			wantPhase: opsv1alpha1.PhaseProgressing, wantReason: reasonRouteProbeUnknown, wantCalls: 1,
		},
		{
			// The capability is absent (an operator built without it, a wiring
			// regression). Falling back to the label here is exactly the defect, so
			// it refuses instead — and says that it refused.
			name: "no prober is wired at all", probe: nil,
			wantPhase: opsv1alpha1.PhaseProgressing, wantReason: reasonRouteProbeUnknown, wantCalls: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cp := haSpecCP(opsv1alpha1.HARoutingLeader)
			var probe RouteReadinessProber
			if tc.probe != nil {
				probe = tc.probe
			}
			r, c := newReconcilerWithProbe(t, probe, cp)
			convergeLeaderRouting(t, r, c, cp)
			reconcileOnce(t, r, nnOf(cp))

			live := liveCP(t, c, cp)
			if live.Status.Phase != tc.wantPhase {
				t.Errorf("phase = %q, want %q", live.Status.Phase, tc.wantPhase)
			}
			prog := conditionByType(live.Status.Conditions, opsv1alpha1.ConditionProgressing)
			if prog == nil || prog.Reason != tc.wantReason || prog.Status != metav1.ConditionFalse {
				t.Errorf("Progressing = %+v, want False/%s", prog, tc.wantReason)
			}
			deg := conditionByType(live.Status.Conditions, opsv1alpha1.ConditionDegraded)
			wantDegraded := tc.wantReason != reasonRolloutComplete
			if deg == nil || (deg.Status == metav1.ConditionTrue) != wantDegraded {
				t.Errorf("Degraded = %+v, want degraded=%v", deg, wantDegraded)
			}
			if wantDegraded && deg.Reason != tc.wantReason {
				t.Errorf("Degraded reason = %q, want %q", deg.Reason, tc.wantReason)
			}
			if tc.probe != nil && len(tc.probe.calls) != tc.wantCalls {
				t.Errorf("probe calls = %v, want %d", tc.probe.calls, tc.wantCalls)
			}
			// LeaderPod keeps naming the endpoint in every case: status must still
			// say WHICH pod the Service resolves to, including when that pod is the
			// one refusing traffic.
			if live.Status.LeaderPod != "test-0" {
				t.Errorf("leaderPod = %q, want test-0", live.Status.LeaderPod)
			}
		})
	}
}

// TestLeaderRouting_NoLeaderCandidateIsNeverProbed: zero and several claimants keep
// their existing reasons, and neither produces a request. Asking one of two
// claimants would be the arbitration this operator refuses to perform; asking with
// zero has nothing to address.
func TestLeaderRouting_NoLeaderCandidateIsNeverProbed(t *testing.T) {
	tests := []struct {
		name       string
		roles      []string
		wantReason string
	}{
		{"no pod publishes the label", []string{"standby", "standby", "standby"}, reasonLeaderNotPublished},
		{"two pods claim it", []string{haRoleLeader, haRoleLeader, "standby"}, reasonMultipleLeadersPublished},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cp := haSpecCP(opsv1alpha1.HARoutingLeader)
			probe := &fakeRouteProbe{result: RouteReady}
			r, c := newReconcilerWithProbe(t, probe, cp)
			reconcileOnce(t, r, nnOf(cp))
			sts := getSTS(t, c, nnOf(cp))
			sts.UID = stsUID
			if err := c.Update(context.Background(), &sts); err != nil {
				t.Fatal(err)
			}
			sts.Status = statefulSetConverged(3, 3)
			if err := c.Status().Update(context.Background(), &sts); err != nil {
				t.Fatal(err)
			}
			for i, role := range tc.roles {
				pod := workloadPod(cp, i, role, true, cp.Spec.Image)
				pod.UID = types.UID(fmt.Sprintf("%s-uid", pod.Name))
				ownPodBy(pod, &sts)
				if err := c.Create(context.Background(), pod); err != nil {
					t.Fatal(err)
				}
			}
			reconcileOnce(t, r, nnOf(cp))

			live := liveCP(t, c, cp)
			if live.Status.Phase == opsv1alpha1.PhaseReady {
				t.Errorf("phase = Ready with %d leader claims", len(tc.roles))
			}
			if deg := conditionByType(live.Status.Conditions, opsv1alpha1.ConditionDegraded); deg == nil || deg.Reason != tc.wantReason {
				t.Errorf("Degraded = %+v, want %s (the existing reason, not a route one)", deg, tc.wantReason)
			}
			if len(probe.calls) != 0 {
				t.Errorf("probe calls = %v, want none", probe.calls)
			}
		})
	}
}

// TestLeaderRouting_ProbeRequiresAVerifiedOwnedPod: the observation is bracketed by
// an uncached identity check, because a proxy addresses a live NAME and StatefulSet
// ordinal names are reused. Every way the target can fail to be OUR published
// leader — before or during the call — yields "unverified", never a verdict about
// an engine and never a Ready.
func TestLeaderRouting_ProbeRequiresAVerifiedOwnedPod(t *testing.T) {
	// setup returns the cluster in the converged state, then each case breaks one
	// link of the chain.
	tests := []struct {
		name string
		// breakBefore runs before the reconcile that probes.
		breakBefore func(t *testing.T, c client.Client, cp *opsv1alpha1.ControlPlane)
		// breakDuring runs between the operator's two identity reads.
		breakDuring func(t *testing.T, c client.Client, cp *opsv1alpha1.ControlPlane)
		wantCalls   int
	}{
		{
			name: "the labeled pod is not owned by this StatefulSet",
			breakBefore: func(t *testing.T, c client.Client, cp *opsv1alpha1.ControlPlane) {
				// A pod wearing the workload labels and the leader label, owned by
				// something else entirely. The label is a claim; ownership is a fact.
				pod := getPod(t, c, cp, "test-0")
				pod.OwnerReferences = []metav1.OwnerReference{{
					APIVersion: "apps/v1", Kind: "StatefulSet",
					Name: "impostor", UID: "impostor-uid", Controller: ptrBool(true),
				}}
				updatePod(t, c, pod)
			},
			wantCalls: 0,
		},
		{
			name: "the pod has no stable identity yet",
			breakBefore: func(t *testing.T, c client.Client, cp *opsv1alpha1.ControlPlane) {
				pod := getPod(t, c, cp, "test-0")
				pod.UID = ""
				updatePod(t, c, pod)
			},
			wantCalls: 0,
		},
		{
			name: "the pod is replaced by a new one with the same name mid-call",
			breakDuring: func(t *testing.T, c client.Client, cp *opsv1alpha1.ControlPlane) {
				pod := getPod(t, c, cp, "test-0")
				pod.UID = "a-different-pod-uid"
				updatePod(t, c, pod)
			},
			wantCalls: 1,
		},
		{
			name: "the pod stops being Ready mid-call",
			breakDuring: func(t *testing.T, c client.Client, cp *opsv1alpha1.ControlPlane) {
				pod := getPod(t, c, cp, "test-0")
				pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}
				// Readiness lives on the status subresource, here as on a real
				// apiserver: a plain Update would silently drop it and the case
				// would pass for the wrong reason.
				if err := c.Status().Update(context.Background(), pod); err != nil {
					t.Fatalf("update pod status: %v", err)
				}
			},
			wantCalls: 1,
		},
		{
			name: "the pod begins terminating mid-call",
			breakDuring: func(t *testing.T, c client.Client, cp *opsv1alpha1.ControlPlane) {
				// A finalizer keeps the object present with a deletionTimestamp,
				// which is exactly the state a draining pod is in: still listed,
				// already out of the Service endpoints.
				pod := getPod(t, c, cp, "test-0")
				pod.Finalizers = []string{"olivares.test/hold"}
				updatePod(t, c, pod)
				if err := c.Delete(context.Background(), pod); err != nil {
					t.Fatalf("delete pod: %v", err)
				}
			},
			wantCalls: 1,
		},
		{
			name: "the pod withdraws the leader label mid-call",
			breakDuring: func(t *testing.T, c client.Client, cp *opsv1alpha1.ControlPlane) {
				pod := getPod(t, c, cp, "test-0")
				pod.Labels[haRoleLabelKey] = "standby"
				updatePod(t, c, pod)
			},
			wantCalls: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cp := haSpecCP(opsv1alpha1.HARoutingLeader)
			probe := &fakeRouteProbe{result: RouteReady}
			r, c := newReconcilerWithProbe(t, probe, cp)
			convergeLeaderRouting(t, r, c, cp)
			if tc.breakBefore != nil {
				tc.breakBefore(t, c, cp)
			}
			if tc.breakDuring != nil {
				probe.during = func() { tc.breakDuring(t, c, cp) }
			}
			reconcileOnce(t, r, nnOf(cp))

			live := liveCP(t, c, cp)
			if live.Status.Phase == opsv1alpha1.PhaseReady {
				t.Errorf("phase = Ready on an answer from a pod the operator could not verify")
			}
			if len(probe.calls) != tc.wantCalls {
				t.Errorf("probe calls = %v, want %d", probe.calls, tc.wantCalls)
			}
		})
	}
}

func getPod(t *testing.T, c client.Client, cp *opsv1alpha1.ControlPlane, name string) *corev1.Pod {
	t.Helper()
	var pod corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: cp.Namespace, Name: name}, &pod); err != nil {
		t.Fatalf("get pod %s: %v", name, err)
	}
	return &pod
}

func updatePod(t *testing.T, c client.Client, pod *corev1.Pod) {
	t.Helper()
	if err := c.Update(context.Background(), pod); err != nil {
		t.Fatalf("update pod %s: %v", pod.Name, err)
	}
}

// TestObserveRouteReadiness_RefusesWhatItCannotVerify exercises the observation
// gate directly, for the links a Reconcile cannot break from outside: a
// StatefulSet that is not this ControlPlane's (the reconcile refuses that
// collision before status is ever written, so the guard is unreachable there but
// must still hold), a scaled-to-zero spec, and an unobserved pod list. Each is a
// reason NOT to ask, and none of them is allowed to produce an answer.
func TestObserveRouteReadiness_RefusesWhatItCannotVerify(t *testing.T) {
	base := func() (*opsv1alpha1.ControlPlane, *appsv1.StatefulSet, podObservation) {
		cp := haSpecCP(opsv1alpha1.HARoutingLeader)
		sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{
			Name: cp.Name, Namespace: cp.Namespace, UID: stsUID,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: opsv1alpha1.GroupVersion.String(), Kind: "ControlPlane",
				Name: cp.Name, UID: cp.UID, Controller: ptrBool(true),
			}},
		}}
		pods := obs(3, 3, 1, cp.Spec.Image)
		pods.leaderPod = "test-0"
		return cp, sts, pods
	}
	tests := []struct {
		name    string
		mutate  func(cp *opsv1alpha1.ControlPlane, sts *appsv1.StatefulSet, pods *podObservation)
		wantAsk bool
	}{
		{name: "a verified chain is asked", wantAsk: true},
		{
			name: "the StatefulSet answers to another ControlPlane",
			mutate: func(_ *opsv1alpha1.ControlPlane, sts *appsv1.StatefulSet, _ *podObservation) {
				sts.OwnerReferences[0].UID = "another-controlplane-uid"
			},
		},
		{
			name: "the StatefulSet has no controller at all",
			mutate: func(_ *opsv1alpha1.ControlPlane, sts *appsv1.StatefulSet, _ *podObservation) {
				sts.OwnerReferences = nil
			},
		},
		{
			name: "the StatefulSet has no identity yet",
			mutate: func(_ *opsv1alpha1.ControlPlane, sts *appsv1.StatefulSet, _ *podObservation) {
				sts.UID = ""
			},
		},
		{
			name: "the ControlPlane has no identity yet",
			mutate: func(cp *opsv1alpha1.ControlPlane, sts *appsv1.StatefulSet, _ *podObservation) {
				cp.UID, sts.OwnerReferences[0].UID = "", ""
			},
		},
		{
			name: "nothing is desired",
			mutate: func(cp *opsv1alpha1.ControlPlane, _ *appsv1.StatefulSet, _ *podObservation) {
				cp.Spec.Replicas = 0
			},
		},
		{
			name: "the pod list was never observed",
			mutate: func(_ *opsv1alpha1.ControlPlane, _ *appsv1.StatefulSet, pods *podObservation) {
				*pods = podObservation{}
			},
		},
		{
			// observePods clears leaderPod unless exactly one claimant exists, so
			// this combination should be unreachable. It is asserted anyway: the
			// rule is "never ask when the operator would be choosing", and a rule
			// that only holds because of a distant invariant is one refactor away
			// from being an arbitration between two claimants.
			name: "several pods claim the label, even with one named",
			mutate: func(_ *opsv1alpha1.ControlPlane, _ *appsv1.StatefulSet, pods *podObservation) {
				pods.readyLeaders, pods.leaders = 2, 2
			},
		},
		{
			name: "no pod claims the label, even with one named",
			mutate: func(_ *opsv1alpha1.ControlPlane, _ *appsv1.StatefulSet, pods *podObservation) {
				pods.readyLeaders, pods.leaders = 0, 0
			},
		},
		{
			name: "this is not the leader-routing layout",
			mutate: func(cp *opsv1alpha1.ControlPlane, _ *appsv1.StatefulSet, _ *podObservation) {
				cp.Spec.HARouting = opsv1alpha1.HARoutingLegacy
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cp, sts, pods := base()
			if tc.mutate != nil {
				tc.mutate(cp, sts, &pods)
			}
			probe := &fakeRouteProbe{result: RouteReady}
			r, c := newReconcilerWithProbe(t, probe, cp)
			pod := workloadPod(cp, 0, haRoleLeader, true, cp.Spec.Image)
			pod.UID = leaderPodUID
			ownPodBy(pod, &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: cp.Name, UID: stsUID}})
			if err := c.Create(context.Background(), pod); err != nil {
				t.Fatal(err)
			}

			got := r.observeRouteReadiness(context.Background(), cp, sts, pods)
			if tc.wantAsk {
				if got != RouteReady || len(probe.calls) != 1 {
					t.Errorf("observation = %s after %v calls, want %s after one", got, probe.calls, RouteReady)
				}
				return
			}
			if got != RouteUnknown {
				t.Errorf("observation = %s, want %s", got, RouteUnknown)
			}
			if len(probe.calls) != 0 {
				t.Errorf("probe calls = %v, want none", probe.calls)
			}
		})
	}
}

// TestLeaderRouting_UnreadablePodIsNotAVerdict: the identity read is UNCACHED and
// can fail on its own. A failed read is not a failing engine and not a Ready — it
// is the absence of an observation, and it says so.
func TestLeaderRouting_UnreadablePodIsNotAVerdict(t *testing.T) {
	cp := haSpecCP(opsv1alpha1.HARoutingLeader)
	probe := &fakeRouteProbe{result: RouteReady}
	r, c := newReconcilerWithProbe(t, probe, cp)
	convergeLeaderRouting(t, r, c, cp)

	// Fail ONLY the per-pod identity read: the pod LIST that feeds the label count
	// still succeeds, so this isolates the uncached re-read from everything else.
	s := newScheme(t)
	broken := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&opsv1alpha1.ControlPlane{}).
		WithObjects(collectObjects(t, c, cp)...).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.Pod); ok {
					return errors.New("apiserver unavailable")
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).
		Build()
	r2 := &ControlPlaneReconciler{Client: broken, Scheme: s, RouteProbe: probe}
	reconcileOnce(t, r2, nnOf(cp))

	var live opsv1alpha1.ControlPlane
	if err := broken.Get(context.Background(), nnOf(cp), &live); err != nil {
		t.Fatal(err)
	}
	if live.Status.Phase == opsv1alpha1.PhaseReady {
		t.Errorf("phase = Ready while the operator could not read the pod it was about to ask")
	}
	if deg := conditionByType(live.Status.Conditions, opsv1alpha1.ConditionDegraded); deg == nil || deg.Reason != reasonRouteProbeUnknown {
		t.Errorf("Degraded = %+v, want %s", deg, reasonRouteProbeUnknown)
	}
	if len(probe.calls) != 0 {
		t.Errorf("probe calls = %v, want none: the target was never verified", probe.calls)
	}
}

// collectObjects snapshots the converged cluster so a second client can be built
// over the same objects with a different failure injected.
func collectObjects(t *testing.T, c client.Client, cp *opsv1alpha1.ControlPlane) []client.Object {
	t.Helper()
	var out []client.Object
	var live opsv1alpha1.ControlPlane
	if err := c.Get(context.Background(), nnOf(cp), &live); err != nil {
		t.Fatal(err)
	}
	out = append(out, &live)
	sts := getSTS(t, c, nnOf(cp))
	out = append(out, &sts)
	var pods corev1.PodList
	if err := c.List(context.Background(), &pods, client.InNamespace(cp.Namespace)); err != nil {
		t.Fatal(err)
	}
	for i := range pods.Items {
		out = append(out, &pods.Items[i])
	}
	var svcs corev1.ServiceList
	if err := c.List(context.Background(), &svcs, client.InNamespace(cp.Namespace)); err != nil {
		t.Fatal(err)
	}
	for i := range svcs.Items {
		out = append(out, &svcs.Items[i])
	}
	return out
}

// TestInvalidSpecIsNeverProbed: a structurally impossible spec is refused before
// any workload exists, so there is nothing to ask and nothing to ask it about. The
// probe must not run on the invalid-spec path at all.
func TestInvalidSpecIsNeverProbed(t *testing.T) {
	cp := haSpecCP(opsv1alpha1.HARoutingLeader)
	cp.Spec.Postgres = nil // postgres without a DSN: the CEL rules reject this too
	probe := &fakeRouteProbe{result: RouteReady}
	r, c := newReconcilerWithProbe(t, probe, cp)
	reconcileOnce(t, r, nnOf(cp))

	live := liveCP(t, c, cp)
	if live.Status.Phase != opsv1alpha1.PhaseInvalid {
		t.Fatalf("phase = %q, want %q", live.Status.Phase, opsv1alpha1.PhaseInvalid)
	}
	if len(probe.calls) != 0 {
		t.Errorf("probe calls = %v, want none on an invalid spec", probe.calls)
	}
}

// TestLeaderRouting_RequeueKeepsObserving pins the cadence, which is part of the
// contract and not an implementation detail: an HTTP fact changes with no
// Kubernetes event, so a leader-routing ControlPlane keeps asking even once Ready.
// A verified, human-blocked state polls slowly instead — it is static by
// construction, and re-examining it twice a minute forever is noise.
func TestLeaderRouting_RequeueKeepsObserving(t *testing.T) {
	tests := []struct {
		name   string
		result RouteReadiness
		want   time.Duration
	}{
		{"Ready still refreshes", RouteReady, progressRequeueInterval},
		{"a transient refusal keeps the rollout cadence", RouteNotReady, progressRequeueInterval},
		{"an unverified observation keeps the rollout cadence", RouteUnknown, progressRequeueInterval},
		{"a verified setup block polls slowly", RouteSetupBlocked, staticStateRequeueInterval},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cp := haSpecCP(opsv1alpha1.HARoutingLeader)
			r, c := newReconcilerWithProbe(t, &fakeRouteProbe{result: tc.result}, cp)
			convergeLeaderRouting(t, r, c, cp)
			res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: nnOf(cp)})
			if err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if res.RequeueAfter != tc.want {
				t.Errorf("requeueAfter = %s, want %s", res.RequeueAfter, tc.want)
			}
		})
	}
}

// TestLeaderRouting_SingleWriterLayoutIsUntouched: outside leader routing the
// kubelet probes /readyz itself, so the predicate is unchanged and no request is
// made. This is the compatibility half of the change — a sqlite control plane must
// still reach Ready with no prober wired at all.
func TestLeaderRouting_SingleWriterLayoutIsUntouched(t *testing.T) {
	cp := sampleCP() // sqlite, one replica, no HA routing
	probe := &fakeRouteProbe{result: RouteSetupBlocked}
	r, c := newReconcilerWithProbe(t, probe, cp)
	reconcileOnce(t, r, nnOf(cp))
	sts := getSTS(t, c, nnOf(cp))
	sts.Status = statefulSetConverged(1, 1)
	if err := c.Status().Update(context.Background(), &sts); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(context.Background(), workloadPod(cp, 0, "", true, cp.Spec.Image)); err != nil {
		t.Fatal(err)
	}
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: nnOf(cp)})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	live := liveCP(t, c, cp)
	if live.Status.Phase != opsv1alpha1.PhaseReady {
		t.Errorf("phase = %q, want %q: the single-writer predicate is unchanged", live.Status.Phase, opsv1alpha1.PhaseReady)
	}
	if len(probe.calls) != 0 {
		t.Errorf("probe calls = %v, want none outside the leader-routing layout", probe.calls)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("requeueAfter = %s, want none: nothing here changes without an event", res.RequeueAfter)
	}
}

// TestLeaderRouting_LegacyLayoutKeepsItsOwnBlock: legacy HA reports the layout
// reason it always did, and is never probed — its standbys drain /readyz by design,
// so the question does not apply.
func TestLeaderRouting_LegacyLayoutKeepsItsOwnBlock(t *testing.T) {
	cp := haSpecCP(opsv1alpha1.HARoutingLegacy)
	probe := &fakeRouteProbe{result: RouteSetupBlocked}
	r, c := newReconcilerWithProbe(t, probe, cp)
	reconcileOnce(t, r, nnOf(cp))
	sts := getSTS(t, c, nnOf(cp))
	sts.Status = statefulSetConverged(3, 1)
	if err := c.Status().Update(context.Background(), &sts); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, nnOf(cp))

	live := liveCP(t, c, cp)
	if deg := conditionByType(live.Status.Conditions, opsv1alpha1.ConditionDegraded); deg == nil || deg.Reason != reasonHALegacyReadinessBlocked {
		t.Errorf("Degraded = %+v, want %s", deg, reasonHALegacyReadinessBlocked)
	}
	if len(probe.calls) != 0 {
		t.Errorf("probe calls = %v, want none in the legacy layout", probe.calls)
	}
}

// TestLeaderRouting_RepeatedObservationsDoNotChurn: the controller watches its own
// ControlPlane, so every status write wakes it again. A route observation repeated
// every 30 seconds must therefore be byte-identical when nothing changed —
// including LastTransitionTime, which is what an alert's "for 5m" clause reads.
func TestLeaderRouting_RepeatedObservationsDoNotChurn(t *testing.T) {
	for _, result := range []RouteReadiness{RouteReady, RouteSetupBlocked, RouteNotReady, RouteUnknown} {
		t.Run(result.String(), func(t *testing.T) {
			cp := haSpecCP(opsv1alpha1.HARoutingLeader)
			r, c := newReconcilerWithProbe(t, &fakeRouteProbe{result: result}, cp)
			convergeLeaderRouting(t, r, c, cp)
			reconcileOnce(t, r, nnOf(cp))
			first := liveCP(t, c, cp)
			reconcileOnce(t, r, nnOf(cp))
			reconcileOnce(t, r, nnOf(cp))
			third := liveCP(t, c, cp)
			if !reflect.DeepEqual(first.Status, third.Status) {
				t.Errorf("status churned across repeated identical observations:\n first: %+v\n third: %+v", first.Status, third.Status)
			}
		})
	}
}

// TestLeaderRouting_TransitionsMoveTheConditionOnce: when the answer really does
// change, the conditions must follow it — and then settle. This is the other half
// of "do not churn": a status that never moves is as wrong as one that always does.
func TestLeaderRouting_TransitionsMoveTheConditionOnce(t *testing.T) {
	cp := haSpecCP(opsv1alpha1.HARoutingLeader)
	probe := &fakeRouteProbe{result: RouteSetupBlocked}
	r, c := newReconcilerWithProbe(t, probe, cp)
	convergeLeaderRouting(t, r, c, cp)
	reconcileOnce(t, r, nnOf(cp))
	blocked := liveCP(t, c, cp)
	if blocked.Status.Phase != opsv1alpha1.PhaseProgressing {
		t.Fatalf("phase = %q, want %q", blocked.Status.Phase, opsv1alpha1.PhaseProgressing)
	}

	// The administrator configures the administrative pool; the engine starts
	// answering. Nothing in Kubernetes changed — only the HTTP fact.
	probe.result = RouteReady
	reconcileOnce(t, r, nnOf(cp))
	ready := liveCP(t, c, cp)
	if ready.Status.Phase != opsv1alpha1.PhaseReady {
		t.Fatalf("phase = %q, want %q once the leader answers", ready.Status.Phase, opsv1alpha1.PhaseReady)
	}
	before := conditionByType(blocked.Status.Conditions, opsv1alpha1.ConditionDegraded)
	after := conditionByType(ready.Status.Conditions, opsv1alpha1.ConditionDegraded)
	if before == nil || before.Status != metav1.ConditionTrue || before.Reason != reasonSetupBlocked {
		t.Fatalf("Degraded before = %+v, want True/%s", before, reasonSetupBlocked)
	}
	if after == nil || after.Status != metav1.ConditionFalse {
		t.Errorf("Degraded after = %+v, want False once the leader answers", after)
	}
	// And back: a 200 is never remembered. A leader that stops answering is not
	// Ready on the strength of the previous reconcile's success.
	probe.result = RouteNotReady
	reconcileOnce(t, r, nnOf(cp))
	if live := liveCP(t, c, cp); live.Status.Phase != opsv1alpha1.PhaseProgressing {
		t.Errorf("phase = %q after the leader stopped answering; a past success is not readiness", live.Status.Phase)
	}
}

// TestLeaderRouting_StatusAndLogsCarryNoPayload: the observation's only output is a
// fixed name. Neither the status an operator reads nor the log line the manager
// writes may carry a response body, a header or a credential — that is how a
// transient payload becomes a permanent record.
func TestLeaderRouting_StatusAndLogsCarryNoPayload(t *testing.T) {
	var captured bytes.Buffer
	cp := haSpecCP(opsv1alpha1.HARoutingLeader)
	r, c := newReconcilerWithProbe(t, &fakeRouteProbe{result: RouteSetupBlocked}, cp)
	convergeLeaderRouting(t, r, c, cp)
	ctx := ctrllog.IntoContext(context.Background(), zap.New(zap.WriteTo(&captured), zap.UseDevMode(true)))
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: nnOf(cp)}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	live := liveCP(t, c, cp)
	var status strings.Builder
	for _, cond := range live.Status.Conditions {
		status.WriteString(cond.Reason + " " + cond.Message + "\n")
	}
	for _, leak := range []string{"setup_blocked", "cross_tenant_admin_pool_not_configured", "Bearer", "\"store\""} {
		if strings.Contains(status.String(), leak) {
			t.Errorf("status carries %q:\n%s", leak, status.String())
		}
		if strings.Contains(captured.String(), leak) {
			t.Errorf("the log carries %q:\n%s", leak, captured.String())
		}
	}
	// The fixed name IS reported, in both places: withholding the diagnosis would
	// be the opposite failure.
	if !strings.Contains(status.String(), reasonSetupBlocked) {
		t.Errorf("status never names the reason:\n%s", status.String())
	}
	if !strings.Contains(captured.String(), reasonSetupBlocked) {
		t.Errorf("the reconcile log never records the observation:\n%s", captured.String())
	}
}
