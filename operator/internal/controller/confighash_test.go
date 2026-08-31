// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	opsv1alpha1 "github.com/olivaresai/olivares/operator/api/v1alpha1"
)

// hashOf resolves the config hash and fails the test on a read error (the
// reconciler now treats an operational read failure as fatal rather than silently
// hashing the source away).
func hashOf(t *testing.T, r *ControlPlaneReconciler, cp *opsv1alpha1.ControlPlane) string {
	t.Helper()
	h, err := r.configHash(context.Background(), cp)
	if err != nil {
		t.Fatalf("configHash: %v", err)
	}
	return h
}

func cpWithConfigRef(ref string) *opsv1alpha1.ControlPlane {
	cp := sampleCP()
	cp.Spec.ConfigRef = ref
	return cp
}

func configMap(ns, name string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Data: data}
}

// TestConfigHashIsDeterministic is the design §A.5 regression: the hash fed the
// pod-template annotation by ranging over a map, so Go's randomized iteration order
// could produce a DIFFERENT hash for identical content on the very next reconcile —
// an annotation that flip-flops rolls the StatefulSet forever.
func TestConfigHashIsDeterministic(t *testing.T) {
	cp := cpWithConfigRef("engine-config")
	cm := configMap(cp.Namespace, "engine-config", map[string]string{
		"OLIVARES_LOG_LEVEL": "debug", "OLIVARES_BASE_URL": "https://cp.example",
		"A": "1", "B": "2", "C": "3", "D": "4", "E": "5", "F": "6",
	})
	r, _ := newReconciler(t, cp, cm)

	first := hashOf(t, r, cp)
	if first == "" {
		t.Fatal("config hash is empty for a resolvable ConfigMap")
	}
	for i := range 50 {
		if got := hashOf(t, r, cp); got != first {
			t.Fatalf("config hash changed between reconciles on iteration %d: %q != %q", i, got, first)
		}
	}
}

// TestConfigHashCoversBothObjects: the pod loads the ConfigMap AND the same-named
// Secret via envFrom, so a Secret-only edit MUST roll the pods. The previous hash
// read the Secret only when no ConfigMap existed, so that edit rolled nothing.
func TestConfigHashCoversBothObjects(t *testing.T) {
	cp := cpWithConfigRef("engine-config")
	cm := configMap(cp.Namespace, "engine-config", map[string]string{"OLIVARES_LOG_LEVEL": "info"})
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "engine-config", Namespace: cp.Namespace},
		Data:       map[string][]byte{"OLIVARES_CLAUDE_ADMIN_KEY": []byte("secret-v1")},
	}
	r, c := newReconciler(t, cp, cm, sec)

	withBoth := hashOf(t, r, cp)

	sec.Data["OLIVARES_CLAUDE_ADMIN_KEY"] = []byte("secret-v2")
	if err := c.Update(context.Background(), sec); err != nil {
		t.Fatal(err)
	}
	afterSecretEdit := hashOf(t, r, cp)
	if afterSecretEdit == withBoth {
		t.Error("editing the Secret did not change the config hash; the reconfigure rollout would never fire")
	}

	// And the ConfigMap still counts.
	cm.Data["OLIVARES_LOG_LEVEL"] = "debug"
	if err := c.Update(context.Background(), cm); err != nil {
		t.Fatal(err)
	}
	if hashOf(t, r, cp) == afterSecretEdit {
		t.Error("editing the ConfigMap did not change the config hash")
	}
}

// TestConfigHashFieldsAreUnambiguous: length-delimited fields mean no pair of
// key/value splits can collide (a plain concatenation would hash "ab"+"c" and
// "a"+"bc" identically).
func TestConfigHashFieldsAreUnambiguous(t *testing.T) {
	cp := cpWithConfigRef("cfg")
	rA, _ := newReconciler(t, cp.DeepCopy(), configMap(cp.Namespace, "cfg", map[string]string{"ab": "c"}))
	rB, _ := newReconciler(t, cp.DeepCopy(), configMap(cp.Namespace, "cfg", map[string]string{"a": "bc"}))
	if hashOf(t, rA, cp) == hashOf(t, rB, cp) {
		t.Error("different key/value splits hash identically; the digest is ambiguous")
	}
}

// TestConfigHashUnreadableRefStillRolls: an unreadable (or absent) referenced object
// must still hash the ref NAME, so at least pointing spec.configRef somewhere else
// rolls the pods.
func TestConfigHashUnreadableRefStillRolls(t *testing.T) {
	cp := cpWithConfigRef("absent")
	r, _ := newReconciler(t, cp)
	first := hashOf(t, r, cp)
	if first == "" {
		t.Fatal("an absent configRef must still hash its name")
	}
	cp.Spec.ConfigRef = "another"
	if hashOf(t, r, cp) == first {
		t.Error("changing spec.configRef did not change the hash")
	}
	cp.Spec.ConfigRef = ""
	if got := hashOf(t, r, cp); got != "" {
		t.Errorf("empty configRef hash = %q, want empty (no annotation)", got)
	}
}

// TestControlPlaneForWorkloadPod pins the pod watch mapper: only pods this operator
// renders for a ControlPlane enqueue that ControlPlane. It is what makes the leader
// label — published asynchronously by the engine, changing no owned object — reach
// the status without waiting for the periodic re-queue.
func TestControlPlaneForWorkloadPod(t *testing.T) {
	r, _ := newReconciler(t)
	cp := sampleCP()

	got := r.controlPlaneForWorkloadPod(context.Background(), workloadPod(cp, 0, haRoleLeader, true, cp.Spec.Image))
	want := []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: cp.Namespace, Name: cp.Name}}}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("mapped requests = %v, want %v", got, want)
	}

	foreign := workloadPod(cp, 0, haRoleLeader, true, cp.Spec.Image)
	foreign.Labels["app.kubernetes.io/managed-by"] = "someone-else"
	if got := r.controlPlaneForWorkloadPod(context.Background(), foreign); len(got) != 0 {
		t.Errorf("a foreign pod enqueued %v, want nothing", got)
	}

	unlabeled := workloadPod(cp, 0, haRoleLeader, true, cp.Spec.Image)
	delete(unlabeled.Labels, "app.kubernetes.io/instance")
	if got := r.controlPlaneForWorkloadPod(context.Background(), unlabeled); len(got) != 0 {
		t.Errorf("an instance-less pod enqueued %v, want nothing", got)
	}
}
