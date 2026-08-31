// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package controller

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	opsv1alpha1 "github.com/olivaresai/olivares/operator/api/v1alpha1"
)

// haCP is a valid active-passive HA ControlPlane (postgres, 3 replicas, shared key)
// in the LEGACY layout (leader-only readiness).
func haCP(image string) *opsv1alpha1.ControlPlane {
	return &opsv1alpha1.ControlPlane{
		ObjectMeta: metav1.ObjectMeta{Name: "cp", Namespace: "default"},
		Spec: opsv1alpha1.ControlPlaneSpec{
			Image: image, Replicas: 3, Engine: opsv1alpha1.EnginePostgres,
			AuditSigningKeySecret: "audit",
			Postgres:              &opsv1alpha1.PostgresSpec{DSNSecret: "pg-dsn"},
		},
	}
}

// splitCP is the same HA ControlPlane in the LEADER-ROUTING layout (stage-2).
func splitCP(image string) *opsv1alpha1.ControlPlane {
	cp := haCP(image)
	cp.Spec.HARouting = opsv1alpha1.HARoutingLeader
	return cp
}

// singleCP is a single-node (sqlite) ControlPlane.
func singleCP(image string) *opsv1alpha1.ControlPlane {
	return &opsv1alpha1.ControlPlane{
		ObjectMeta: metav1.ObjectMeta{Name: "cp", Namespace: "default"},
		Spec:       opsv1alpha1.ControlPlaneSpec{Image: image, Replicas: 1, Engine: opsv1alpha1.EngineSQLite},
	}
}

func stsWith(gen int64, s appsv1.StatefulSetStatus) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Generation: gen}, Status: s}
}

// obs builds a pod observation: n pods, r Ready, l Ready leader-labeled, running
// the given images.
func obs(total, ready, readyLeaders int32, images ...string) podObservation {
	o := podObservation{observed: true, total: total, ready: ready, readyLeaders: readyLeaders, leaders: readyLeaders, images: images}
	if readyLeaders == 1 {
		o.leaderPod = "cp-0"
	}
	return o
}

var testNow = metav1.NewTime(time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC))

// TestClassifyRollout is the design §D.1 truth table. Convergence is derived from
// OBSERVED StatefulSet status and the live pods — never the desired pod template —
// and the leader-routing layout is what makes PhaseReady reachable for HA.
func TestClassifyRollout(t *testing.T) {
	tests := []struct {
		name            string
		cp              *opsv1alpha1.ControlPlane
		sts             *appsv1.StatefulSet
		pods            podObservation
		wantImageRolled bool
		wantReady       bool
		wantAvailable   bool
		wantHABlocked   bool
		wantReason      string
		wantDegraded    string
	}{
		{
			name:       "1. fresh 3-replica install: nothing rolled yet",
			cp:         splitCP("img:v2"),
			sts:        stsWith(1, appsv1.StatefulSetStatus{ObservedGeneration: 0}),
			pods:       obs(0, 0, 0),
			wantReason: reasonInstalling,
		},
		{
			name: "2. install observed but only two pods exist: scaling",
			cp:   splitCP("img:v2"),
			sts: stsWith(1, appsv1.StatefulSetStatus{
				ObservedGeneration: 1, Replicas: 2, CurrentReplicas: 2, UpdatedReplicas: 2, ReadyReplicas: 2,
				CurrentRevision: "r1", UpdateRevision: "r1",
			}),
			pods:          obs(2, 2, 1, "img:v2"),
			wantAvailable: true,
			wantReason:    reasonScaling,
		},
		{
			name: "3. image upgrade in flight: Upgrading, available through the leader",
			cp: func() *opsv1alpha1.ControlPlane {
				cp := splitCP("img:v2")
				cp.Status.CurrentImage = "img:v1"
				return cp
			}(),
			sts: stsWith(2, appsv1.StatefulSetStatus{
				ObservedGeneration: 2, Replicas: 3, CurrentReplicas: 1, UpdatedReplicas: 2, ReadyReplicas: 3,
				CurrentRevision: "r1", UpdateRevision: "r2",
			}),
			pods:          obs(3, 3, 1, "img:v1", "img:v2"),
			wantAvailable: true,
			wantReason:    reasonUpgrading,
		},
		{
			name: "4. image upgrade complete with one Ready leader: READY",
			cp:   splitCP("img:v2"),
			sts: stsWith(2, appsv1.StatefulSetStatus{
				ObservedGeneration: 2, Replicas: 3, CurrentReplicas: 3, UpdatedReplicas: 3, ReadyReplicas: 3,
				CurrentRevision: "r2", UpdateRevision: "r2",
			}),
			pods:            obs(3, 3, 1, "img:v2"),
			wantImageRolled: true, wantReady: true, wantAvailable: true,
			wantReason: reasonRolloutComplete,
		},
		{
			name: "5. premature-completion guard: healthy counters but generation not observed",
			cp:   singleCP("img:v2"),
			sts: stsWith(2, appsv1.StatefulSetStatus{
				ObservedGeneration: 1, Replicas: 1, CurrentReplicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1,
				CurrentRevision: "r1", UpdateRevision: "r1",
			}),
			pods:          obs(1, 1, 0, "img:v1"),
			wantAvailable: true,
			wantReason:    reasonInstalling,
		},
		{
			name: "6. config-only rollout in flight: Reconfiguring, not Upgrading",
			cp: func() *opsv1alpha1.ControlPlane {
				cp := splitCP("img:v2")
				cp.Status.CurrentImage = "img:v2"
				return cp
			}(),
			sts: stsWith(3, appsv1.StatefulSetStatus{
				ObservedGeneration: 3, Replicas: 3, CurrentReplicas: 1, UpdatedReplicas: 2, ReadyReplicas: 3,
				CurrentRevision: "r2", UpdateRevision: "r3",
			}),
			pods:          obs(3, 3, 1, "img:v2"),
			wantAvailable: true,
			wantReason:    reasonReconfiguring,
		},
		{
			name: "7. config-only rollout complete: Ready, same image",
			cp:   splitCP("img:v2"),
			sts: stsWith(3, appsv1.StatefulSetStatus{
				ObservedGeneration: 3, Replicas: 3, CurrentReplicas: 3, UpdatedReplicas: 3, ReadyReplicas: 3,
				CurrentRevision: "r3", UpdateRevision: "r3",
			}),
			pods:            obs(3, 3, 1, "img:v2"),
			wantImageRolled: true, wantReady: true, wantAvailable: true,
			wantReason: reasonRolloutComplete,
		},
		{
			name: "8. post-split HA steady state: two healthy standbys + one Ready leader is READY",
			cp:   splitCP("img:v1"),
			sts: stsWith(1, appsv1.StatefulSetStatus{
				ObservedGeneration: 1, Replicas: 3, CurrentReplicas: 3, UpdatedReplicas: 3, ReadyReplicas: 3,
				CurrentRevision: "r1", UpdateRevision: "r1",
			}),
			pods:            obs(3, 3, 1, "img:v1"),
			wantImageRolled: true, wantReady: true, wantAvailable: true,
			wantReason: reasonRolloutComplete,
		},
		{
			name: "9. legacy leader-only readiness: HALegacyReadinessBlocked, never a silent Progressing",
			cp:   haCP("img:v1"),
			sts: stsWith(1, appsv1.StatefulSetStatus{
				ObservedGeneration: 1, Replicas: 3, CurrentReplicas: 3, UpdatedReplicas: 3, ReadyReplicas: 1,
				CurrentRevision: "r1", UpdateRevision: "r1",
			}),
			pods:            obs(3, 1, 0, "img:v1"),
			wantImageRolled: true, wantHABlocked: true, wantAvailable: true,
			wantReason: reasonHALegacyReadinessBlocked, wantDegraded: reasonHALegacyReadinessBlocked,
		},
		{
			name: "10. no published leader: converged pods but no endpoint",
			cp:   splitCP("img:v1"),
			sts: stsWith(1, appsv1.StatefulSetStatus{
				ObservedGeneration: 1, Replicas: 3, CurrentReplicas: 3, UpdatedReplicas: 3, ReadyReplicas: 3,
				CurrentRevision: "r1", UpdateRevision: "r1",
			}),
			pods:            obs(3, 3, 0, "img:v1"),
			wantImageRolled: true,
			wantReason:      reasonLeaderNotPublished, wantDegraded: reasonLeaderNotPublished,
		},
		{
			name: "11. two published leaders: never pick one",
			cp:   splitCP("img:v1"),
			sts: stsWith(1, appsv1.StatefulSetStatus{
				ObservedGeneration: 1, Replicas: 3, CurrentReplicas: 3, UpdatedReplicas: 3, ReadyReplicas: 3,
				CurrentRevision: "r1", UpdateRevision: "r1",
			}),
			pods:            obs(3, 3, 2, "img:v1"),
			wantImageRolled: true,
			wantReason:      reasonMultipleLeadersPublished, wantDegraded: reasonMultipleLeadersPublished,
		},
		{
			name: "12. every pod on the update revision but not yet healthy: WaitingForPodHealth",
			cp:   splitCP("img:v2"),
			sts: stsWith(2, appsv1.StatefulSetStatus{
				ObservedGeneration: 2, Replicas: 3, CurrentReplicas: 3, UpdatedReplicas: 3, ReadyReplicas: 2,
				CurrentRevision: "r2", UpdateRevision: "r2",
			}),
			pods:            obs(3, 2, 1, "img:v2"),
			wantImageRolled: true, wantAvailable: true,
			wantReason: reasonWaitingForPodHealth,
		},
		{
			name: "13. sqlite clamped to one effective replica still reaches Ready",
			cp: func() *opsv1alpha1.ControlPlane {
				cp := singleCP("img:v1")
				cp.Spec.Replicas = 3 // requested 3, sqlite clamps effective to 1
				return cp
			}(),
			sts: stsWith(1, appsv1.StatefulSetStatus{
				ObservedGeneration: 1, Replicas: 1, CurrentReplicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1,
				CurrentRevision: "r1", UpdateRevision: "r1",
			}),
			pods:            obs(1, 1, 0, "img:v1"),
			wantImageRolled: true, wantReady: true, wantAvailable: true,
			wantReason: reasonRolloutComplete,
		},
		{
			name: "14. leader-routing layout with zero Ready pods: not available, not blocked",
			cp:   splitCP("img:v1"),
			sts: stsWith(1, appsv1.StatefulSetStatus{
				ObservedGeneration: 1, Replicas: 3, CurrentReplicas: 3, UpdatedReplicas: 3, ReadyReplicas: 0,
				CurrentRevision: "r1", UpdateRevision: "r1",
			}),
			pods:            obs(3, 0, 0, "img:v1"),
			wantImageRolled: true,
			wantReason:      reasonWaitingForPodHealth,
		},
		{
			name: "15. pod list unavailable: fall back to counters, invent no leader verdict",
			cp:   splitCP("img:v1"),
			sts: stsWith(1, appsv1.StatefulSetStatus{
				ObservedGeneration: 1, Replicas: 3, CurrentReplicas: 3, UpdatedReplicas: 3, ReadyReplicas: 3,
				CurrentRevision: "r1", UpdateRevision: "r1",
			}),
			pods:            podObservation{},
			wantImageRolled: true, wantReady: true, wantAvailable: true,
			wantReason: reasonRolloutComplete,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyRollout(tc.cp, tc.sts, tc.pods, testNow)
			if got.imageRolled != tc.wantImageRolled {
				t.Errorf("imageRolled = %v, want %v", got.imageRolled, tc.wantImageRolled)
			}
			if got.ready != tc.wantReady {
				t.Errorf("ready = %v, want %v", got.ready, tc.wantReady)
			}
			if got.available != tc.wantAvailable {
				t.Errorf("available = %v, want %v", got.available, tc.wantAvailable)
			}
			if got.haReadinessBlocked != tc.wantHABlocked {
				t.Errorf("haReadinessBlocked = %v, want %v", got.haReadinessBlocked, tc.wantHABlocked)
			}
			if tc.wantReason != "" && got.reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", got.reason, tc.wantReason)
			}
			if got.degraded != tc.wantDegraded {
				t.Errorf("degraded = %q, want %q", got.degraded, tc.wantDegraded)
			}
			if got.stalled {
				t.Errorf("stalled = true on a fresh progress observation, want false")
			}
		})
	}
}

// TestClassifyProgressStall covers design §C.4: a rollout that stops advancing must
// be distinguishable from one that is merely slow. StatefulSet status carries no
// progress deadline, so the operator keeps the bookkeeping itself.
func TestClassifyProgressStall(t *testing.T) {
	start := testNow
	sts := stsWith(2, appsv1.StatefulSetStatus{
		ObservedGeneration: 2, Replicas: 3, CurrentReplicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1,
		CurrentRevision: "r1", UpdateRevision: "r2",
	})

	// First observation: the clock starts now, nothing is stalled yet.
	cp := splitCP("img:v2")
	cp.Status.CurrentImage = "img:v1"
	first := classifyRollout(cp, sts, obs(3, 1, 1, "img:v1", "img:v2"), start)
	if first.stalled {
		t.Fatal("a first observation can never be stalled")
	}
	if !first.progress.at.Equal(&start) {
		t.Fatalf("progress timestamp = %v, want the observation time", first.progress.at)
	}

	// Persist the bookkeeping and observe again, unchanged, INSIDE the deadline.
	cp.Status.RolloutRevision = first.progress.revision
	cp.Status.LastProgressUpdatedReplicas = first.progress.updatedReplicas
	cp.Status.LastProgressReadyReplicas = first.progress.readyReplicas
	at := first.progress.at
	cp.Status.LastProgressTime = &at

	within := metav1.NewTime(start.Add(5 * time.Minute))
	if got := classifyRollout(cp, sts, obs(3, 1, 1, "img:v1", "img:v2"), within); got.stalled {
		t.Error("stalled inside the progress deadline; slow is not wedged")
	}

	// Past the deadline with an unchanged tuple: STALLED.
	past := metav1.NewTime(start.Add(11 * time.Minute))
	stalled := classifyRollout(cp, sts, obs(3, 1, 1, "img:v1", "img:v2"), past)
	if !stalled.stalled {
		t.Fatalf("no stall after %s with zero progress (deadline %s)", past.Sub(start.Time), progressDeadline(cp))
	}
	if stalled.degraded != reasonRolloutStalled {
		t.Errorf("degraded = %q, want %q", stalled.degraded, reasonRolloutStalled)
	}
	if !stalled.progress.at.Equal(&start) {
		t.Errorf("a stalled rollout must KEEP the original progress timestamp, got %v", stalled.progress.at)
	}

	// One more updated replica past the deadline = progress: the clock resets.
	advanced := stsWith(2, appsv1.StatefulSetStatus{
		ObservedGeneration: 2, Replicas: 3, CurrentReplicas: 1, UpdatedReplicas: 2, ReadyReplicas: 1,
		CurrentRevision: "r1", UpdateRevision: "r2",
	})
	moved := classifyRollout(cp, advanced, obs(3, 1, 1, "img:v1", "img:v2"), past)
	if moved.stalled {
		t.Error("an advancing rollout must not be reported as stalled")
	}
	if !moved.progress.at.Equal(&past) {
		t.Errorf("progress timestamp = %v, want it reset to the advancing observation", moved.progress.at)
	}
}

// TestClassifyProgressDoesNotFalseStall covers the ways an advance-only clock
// would cry wolf. A stall verdict that fires on healthy transitions trains an
// operator to ignore it, which is worse than not having one.
func TestClassifyProgressDoesNotFalseStall(t *testing.T) {
	settled := func() *opsv1alpha1.ControlPlane {
		cp := splitCP("img:v1")
		cp.Generation = 2
		cp.Status.ObservedGeneration = 2
		cp.Status.RolloutRevision = "r1"
		cp.Status.LastProgressUpdatedReplicas = 3
		cp.Status.LastProgressReadyReplicas = 3
		long := metav1.NewTime(testNow.Add(-2 * time.Hour)) // older than any deadline
		cp.Status.LastProgressTime = &long
		return cp
	}
	converged := appsv1.StatefulSetStatus{
		ObservedGeneration: 1, Replicas: 3, CurrentReplicas: 3, UpdatedReplicas: 3, ReadyReplicas: 3,
		CurrentRevision: "r1", UpdateRevision: "r1",
	}

	t.Run("a new spec applied to a long-settled control plane", func(t *testing.T) {
		cp := settled()
		cp.Generation = 3 // the operator just edited spec.image; status has not caught up
		cp.Spec.Image = "img:v2"
		// The StatefulSet controller has not observed the new generation yet, so its
		// status still shows the OLD, settled numbers.
		sts := stsWith(2, converged)
		if got := classifyRollout(cp, sts, obs(3, 3, 1, "img:v1"), testNow); got.stalled {
			t.Error("a brand-new rollout was reported as already stalled")
		}
	})

	t.Run("a readiness regression on a healthy deployment", func(t *testing.T) {
		cp := settled()
		degradedStatus := converged
		degradedStatus.ReadyReplicas = 2 // one pod just went unhealthy
		if got := classifyRollout(cp, stsWith(1, degradedStatus), obs(3, 2, 1, "img:v1"), testNow); got.stalled {
			t.Error("a fresh health incident was reported as stalled instead of getting a recovery window")
		}
	})

	t.Run("a scale-down", func(t *testing.T) {
		cp := settled()
		cp.Spec.Replicas = 2
		scaled := appsv1.StatefulSetStatus{
			ObservedGeneration: 1, Replicas: 2, CurrentReplicas: 2, UpdatedReplicas: 2, ReadyReplicas: 2,
			CurrentRevision: "r1", UpdateRevision: "r1",
		}
		if got := classifyRollout(cp, stsWith(1, scaled), obs(2, 2, 1, "img:v1"), testNow); got.stalled {
			t.Error("shrinking toward the desired state was reported as stalled")
		}
	})

	t.Run("scaled to zero", func(t *testing.T) {
		cp := settled()
		cp.Spec.Replicas = 0
		empty := appsv1.StatefulSetStatus{ObservedGeneration: 1, CurrentRevision: "r1", UpdateRevision: "r1"}
		got := classifyRollout(cp, stsWith(1, empty), obs(0, 0, 0), testNow)
		if got.stalled {
			t.Error("a deliberately scaled-to-zero ControlPlane cannot be stalled: there is nothing to converge to")
		}
	})

	t.Run("the legacy readiness layout", func(t *testing.T) {
		cp := haCP("img:v1") // legacy: Ready can never reach desired, by construction
		cp.Generation, cp.Status.ObservedGeneration = 1, 1
		cp.Status.RolloutRevision = "r1"
		cp.Status.LastProgressUpdatedReplicas = 3
		cp.Status.LastProgressReadyReplicas = 1
		long := metav1.NewTime(testNow.Add(-2 * time.Hour))
		cp.Status.LastProgressTime = &long
		blocked := converged
		blocked.ReadyReplicas = 1
		got := classifyRollout(cp, stsWith(1, blocked), obs(3, 1, 0, "img:v1"), testNow)
		if got.stalled || got.degraded != reasonHALegacyReadinessBlocked {
			t.Errorf("legacy HA = stalled:%v degraded:%q; want the specific layout reason, not a generic wedge", got.stalled, got.degraded)
		}
	})
}

// TestCurrentImageLagsInsteadOfDisappearing: the CRD promises currentImage lags
// spec.image during an upgrade. Blanking it would lose the last known-good image
// exactly when an operator needs it — but a value inherited from the pre
// controller was copied from the DESIRED template and may name an image no pod ever
// ran, so it is only preserved once THIS controller has managed a rollout.
func TestCurrentImageLagsInsteadOfDisappearing(t *testing.T) {
	inFlight := stsWith(2, appsv1.StatefulSetStatus{
		ObservedGeneration: 2, Replicas: 3, CurrentReplicas: 1, UpdatedReplicas: 2, ReadyReplicas: 3,
		CurrentRevision: "r1", UpdateRevision: "r2",
	})

	trusted := splitCP("img:v2")
	trusted.Status.CurrentImage = "img:v1"
	trusted.Status.RolloutRevision = "r1" // provenance: this controller recorded it
	got := classifyRollout(trusted, inFlight, obs(3, 3, 1, "img:v1", "img:v2"), testNow)
	if got.imageRolled {
		t.Fatal("an in-flight rollout must not be reported as fully rolled")
	}
	if got.reason != reasonUpgrading {
		t.Errorf("reason = %q, want %q for an image rollout", got.reason, reasonUpgrading)
	}

	// The same rollout in its final moments: every pod already LISTS the new image
	// while the revisions settle. It is still an upgrade, not a reconfigure.
	late := classifyRollout(trusted, inFlight, obs(3, 3, 1, "img:v2"), testNow)
	if late.reason != reasonUpgrading {
		t.Errorf("late-rollout reason = %q, want %q (pod images alone must not flip the verdict)", late.reason, reasonUpgrading)
	}
}

// TestProgressDeadlineDefault pins the default and the spec override.
func TestProgressDeadlineDefault(t *testing.T) {
	cp := splitCP("img:v1")
	if got, want := progressDeadline(cp), 600*time.Second; got != want {
		t.Errorf("default progress deadline = %s, want %s", got, want)
	}
	cp.Spec.ProgressDeadlineSeconds = 90
	if got, want := progressDeadline(cp), 90*time.Second; got != want {
		t.Errorf("progress deadline = %s, want %s", got, want)
	}
}
