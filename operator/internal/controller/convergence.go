// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package controller

import (
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	opsv1alpha1 "github.com/olivaresai/olivares/operator/api/v1alpha1"
)

// Progressing / Degraded reasons. They are part of the operator's observable
// contract (an operator greps them, an alert routes on them), so they live as
// constants next to the classifier that emits them.
const (
	// reasonInstalling: no completed revision yet (or the StatefulSet controller has
	// not observed the latest generation).
	reasonInstalling = "Installing"
	// reasonScaling: the replica count has not reached desired while revisions are
	// otherwise settled.
	reasonScaling = "Scaling"
	// reasonUpgrading: an IMAGE rollout is in flight (pods still run a different
	// image than spec.image).
	reasonUpgrading = "Upgrading"
	// reasonReconfiguring: a config-only rollout is in flight — revisions differ but
	// every observed pod already runs spec.image (the config-hash annotation moved).
	reasonReconfiguring = "Reconfiguring"
	// reasonWaitingForPodHealth: every pod is on the update revision but fewer than
	// desired pass the pod-health probe.
	reasonWaitingForPodHealth = "WaitingForPodHealth"
	// reasonRolloutComplete: converged and routable.
	reasonRolloutComplete = "RolloutComplete"
	// reasonLeaderNotPublished: the rollout converged but NO Ready pod carries the
	// leader label, so the leader Service has no endpoint. The engine publishes that
	// label after it wins the Postgres election; an empty leader Service means the
	// engine could not publish (RBAC/apiserver) or no node holds the lock.
	reasonLeaderNotPublished = "LeaderNotPublished"
	// reasonMultipleLeadersPublished: more than one Ready pod claims the leader
	// label. The operator NEVER picks one — the Postgres lock is the authority and
	// the engine's request gate refuses application traffic on a non-leader, so this
	// is a visible, self-healing anomaly, not a routing decision to make here.
	reasonMultipleLeadersPublished = "MultipleLeadersPublished"
	// reasonProgressDeadlineExceeded / reasonRolloutStalled: the rollout has not
	// advanced within spec.progressDeadlineSeconds (StatefulSet status carries no
	// progress deadline of its own, so the operator keeps the bookkeeping).
	reasonProgressDeadlineExceeded = "ProgressDeadlineExceeded"
	reasonRolloutStalled           = "RolloutStalled"
	// reasonHALegacyReadinessBlocked is the honest state for LEGACY active-passive
	// HA (spec.haRouting=Legacy): the StatefulSet is fully rolled out and its leader
	// is serving, but standby pods drain the leader-only /readyz probe by design
	//, so ReadyReplicas can never reach desired and PhaseReady is
	// unreachable. The fix is spec.haRouting=LeaderRouting (the leader-routing
	// layout: pod-health probe + leader-selecting Service), which this operator
	// materializes — see an internal design note (not shipped)
	reasonHALegacyReadinessBlocked = "HALegacyReadinessBlocked"
	// reasonHALeaderServiceMigrationRequired is the PREPARE phase of the layout
	// migration: leader routing is requested on an EXISTING legacy HA StatefulSet, so
	// the operator has created the leader Service and the publisher credential but
	// will not touch the pod template until the administrator acknowledges that
	// clients moved to that Service. Flipping readiness first would expose
	// health-Ready standbys through the legacy client Service (design §B.1).
	reasonHALeaderServiceMigrationRequired = "HALeaderServiceMigrationRequired"
)

// HA leader-routing labels. haRoleLabelKey/haRoleLeader are a CONTRACT shared with
// the engine, which self-publishes the label (cmd/olivares/haleaderlabel.go): the
// leader Service selects on it, so the two constants must never drift.
const (
	haRoleLabelKey = "ops.olivares.ai/role"
	haRoleLeader   = "leader"
)

// defaultProgressDeadlineSeconds bounds a rollout that never advances. It is
// deliberately generous: pulling a large image onto a cold node, waiting for a PVC
// to bind, or a slow store recovery are all legitimate slow progress.
const defaultProgressDeadlineSeconds int32 = 600

// podObservation is what the operator SEES of the workload's pods: how many exist,
// how many are Ready, how many Ready pods publish the leader label, and which
// images they actually run. Everything here is observed state — the counterpart of
// the StatefulSet status counters — never the desired pod template.
type podObservation struct {
	// total is the number of pods matching the workload selector (any phase).
	total int32
	// ready is the number of pods whose Ready condition is True.
	ready int32
	// readyLeaders is the number of READY pods labeled ops.olivares.ai/role=leader
	// — exactly the set the leader Service resolves to endpoints.
	readyLeaders int32
	// leaders counts leader-labeled pods in ANY state, so a crashed-but-labeled pod
	// is still visible to diagnostics (it serves no traffic: it is not Ready).
	leaders int32
	// leaderPod names the single Ready leader-labeled pod (empty unless exactly one
	// exists — the operator never picks between two claimants).
	leaderPod string
	// images holds the distinct core-container images observed across the pods.
	images []string
	// names lists every pod of this workload that still exists (terminating ones
	// included) — the authorization set the leader-publisher Role is pinned to.
	names []string
	// observed is false when the pod list could not be read; the classifier then
	// falls back to StatefulSet counters alone rather than inventing a verdict.
	observed bool
}

// progressPoint is the rollout-progress bookkeeping persisted in status. A rollout
// is stalled when this tuple has not advanced within the progress deadline.
type progressPoint struct {
	revision        string
	updatedReplicas int32
	readyReplicas   int32
	at              metav1.Time
}

// rolloutClass is the OBSERVED-state classification of a StatefulSet rollout.
// Every field derives from sts.Status and the live pods (what the cluster reports),
// never sts.Spec.Template (what the operator DESIRES) — that distinction is the
// core correction of: the pre-stage-1 code read the desired template image and
// declared completion before any pod ran it (design §A.4).
type rolloutClass struct {
	// imageRolled is true when every pod has been (re)created on the update
	// revision: the StatefulSet observed the latest generation, current == update
	// revision, and Replicas/CurrentReplicas/UpdatedReplicas all equal desired. It
	// does NOT require readiness, so it stays meaningful for the legacy HA layout
	// (standbys are on the new revision but never Ready) and is the correct signal
	// for status.currentImage.
	imageRolled bool
	// converged adds pod health: every desired replica is also Ready.
	converged bool
	// ready is the PhaseReady predicate: converged AND client traffic can reach the
	// single active writer. Unreachable in the legacy HA layout by construction.
	ready bool
	// available answers "can clients reach a leader right now?" — independent of
	// whether the whole desired revision exists (design §C.3).
	available bool
	// leaderServing is true when at least one replica is Ready (the coarse
	// pre-split signal, kept for the legacy layout's diagnostics).
	leaderServing bool
	// haReadinessBlocked marks LEGACY HA that is fully rolled with a serving leader
	// but fewer Ready replicas than desired — the honest interim state.
	haReadinessBlocked bool
	// reason is the Progressing reason (or RolloutComplete when ready).
	reason string
	// degraded is the Degraded reason this classification implies ("" = none). The
	// controller may override it with a higher-priority derate (an immutable
	// mismatch, a clamped spec).
	degraded string
	// stalled is true when the rollout has not advanced within the progress
	// deadline — "wedged", as distinct from "slow" (design §C.4).
	stalled bool
	// progress is the bookkeeping snapshot to persist in status.
	progress progressPoint
}

// classifyRollout derives the rollout state from OBSERVED StatefulSet status and
// the live pods. It reads cp.Status (prior CurrentImage + progress bookkeeping), so
// callers must invoke it BEFORE overwriting status.
func classifyRollout(cp *opsv1alpha1.ControlPlane, sts *appsv1.StatefulSet, pods podObservation, now metav1.Time) rolloutClass {
	desired, _ := effectiveReplicas(cp)
	st := sts.Status

	stsObserved := st.ObservedGeneration >= sts.Generation
	revisionSettled := st.CurrentRevision != "" &&
		st.UpdateRevision != "" &&
		st.CurrentRevision == st.UpdateRevision
	// Every pod present and on the update revision (image/config fully rolled),
	// independent of readiness.
	fullyUpdated := desired > 0 &&
		stsObserved &&
		revisionSettled &&
		st.Replicas == desired &&
		st.CurrentReplicas == desired &&
		st.UpdatedReplicas == desired

	ha := isHA(cp)
	split := haLeaderRouting(cp)
	allReady := st.ReadyReplicas == desired

	c := rolloutClass{
		leaderServing: st.ReadyReplicas > 0,
		imageRolled:   fullyUpdated,
		converged:     fullyUpdated && allReady,
	}

	// Routability. In the leader-routing layout the leader Service resolves to READY
	// pods carrying the leader label, so "exactly one" is the routable state: zero
	// means no endpoint at all, two means a stale label the engine's request gate is
	// currently 503-ing. In the legacy layout /readyz itself drains standbys, so any
	// Ready replica IS the leader.
	routable := st.ReadyReplicas > 0
	if split {
		routable = pods.readyLeaders == 1
		if !pods.observed {
			// Pod list unavailable: do not invent a routing verdict. Fall back to the
			// coarse signal and let the reason/degraded logic stay silent about labels.
			routable = st.ReadyReplicas > 0
		}
	}
	c.available = routable

	switch {
	case split:
		c.ready = c.converged && routable
	case ha:
		// Legacy HA can never be Ready: standbys drain /readyz by design, and even if
		// every replica somehow reported Ready, the leader-only routing that keeps
		// standbys out of the client Service would be gone. Report the honest blocked
		// state instead (and point at spec.haRouting=LeaderRouting).
		c.ready = false
		c.haReadinessBlocked = fullyUpdated && st.ReadyReplicas > 0
	default:
		c.ready = c.converged
	}

	// Progress bookkeeping / stall detection (design §C.4). The clock runs only
	// where "stalled" is a meaningful verdict: not when the rollout is already
	// converged, not when zero replicas are desired (there is nothing to converge
	// to), and not while the state is a KNOWN static block that already has its own
	// actionable reason — the legacy readiness layout, or a layout migration waiting
	// on a human. Reporting those as RolloutStalled would bury the real instruction
	// under a generic one.
	runClock := !c.ready && desired > 0 && !c.haReadinessBlocked
	c.progress, c.stalled = classifyProgress(cp, sts, now, runClock)

	switch {
	case c.ready:
		c.reason = reasonRolloutComplete
	case c.haReadinessBlocked:
		c.reason = reasonHALegacyReadinessBlocked
		c.degraded = reasonHALegacyReadinessBlocked
	case split && pods.observed && pods.readyLeaders > 1:
		// Never choose one arbitrarily: report it. The Postgres lock still permits
		// exactly one writer and the engine's request gate 503s the impostor.
		c.reason = reasonMultipleLeadersPublished
		c.degraded = reasonMultipleLeadersPublished
	case split && c.converged && pods.observed && pods.readyLeaders == 0:
		c.reason = reasonLeaderNotPublished
		c.degraded = reasonLeaderNotPublished
	case !stsObserved || st.CurrentRevision == "":
		c.reason = reasonInstalling
	case st.Replicas != desired:
		c.reason = reasonScaling
	case !revisionSettled || !fullyUpdated:
		if rollingImage(cp, pods) {
			c.reason = reasonUpgrading
		} else {
			c.reason = reasonReconfiguring
		}
	default:
		// Fully updated, revisions settled, but not every pod passes the health probe.
		c.reason = reasonWaitingForPodHealth
	}
	if c.stalled && c.degraded == "" {
		c.degraded = reasonRolloutStalled
	}
	return c
}

// rollingImage reports whether the in-flight rollout is an IMAGE change (as opposed
// to a config-only one). It prefers OBSERVED pod images — a pod still running the
// previous image is proof — and falls back to the last converged image in status
// when the pod list is unavailable.
func rollingImage(cp *opsv1alpha1.ControlPlane, pods podObservation) bool {
	// The last converged image is the primary signal: while it differs from the
	// desired one, the rollout in flight IS an upgrade — even in the final moments
	// when every pod already lists the new image but the StatefulSet revisions have
	// not settled (a pod-image-only rule would flip the reason to Reconfiguring
	// right at the end and lie about what happened).
	if cp.Status.CurrentImage != "" && cp.Status.CurrentImage != cp.Spec.Image {
		return true
	}
	// Fall back to observed pod images for the case the status cannot answer: a CR
	// with no recorded converged image yet.
	for _, img := range pods.images {
		if img != cp.Spec.Image {
			return true
		}
	}
	return false
}

// classifyProgress advances (or preserves) the rollout-progress bookkeeping and
// reports whether the rollout is stalled. StatefulSet status has no progress
// deadline of its own, so the operator records the last point at which the rollout
// visibly advanced — a new update revision, more updated replicas, or more healthy
// replicas — and calls it stalled when nothing has advanced within the deadline.
// A converged rollout always resets the clock.
func classifyProgress(cp *opsv1alpha1.ControlPlane, sts *appsv1.StatefulSet, now metav1.Time, runClock bool) (progressPoint, bool) {
	st := sts.Status
	cur := progressPoint{
		revision:        st.UpdateRevision,
		updatedReplicas: st.UpdatedReplicas,
		readyReplicas:   st.ReadyReplicas,
		at:              now,
	}
	prior := progressPoint{
		revision:        cp.Status.RolloutRevision,
		updatedReplicas: cp.Status.LastProgressUpdatedReplicas,
		readyReplicas:   cp.Status.LastProgressReadyReplicas,
	}
	if t := cp.Status.LastProgressTime; t != nil {
		prior.at = *t
	}
	// The clock restarts whenever anything OBSERVABLE about the rollout changed —
	// not only on "advance". Advance-only resets produce false wedges that would
	// train an operator to ignore the signal: a spec change applied to a
	// long-settled ControlPlane (whose StatefulSet status lags by one reconcile), a
	// readiness regression on a healthy deployment, and a scale-DOWN all move the
	// counters without increasing them. What a real wedge looks like is the
	// opposite: nothing changes at all, for a long time, while unconverged.
	//
	// Rewriting the timestamp when NOTHING changed would be its own bug: a status
	// write bumps resourceVersion, which wakes this controller again — a feedback
	// loop on a settled object. Hence "changed", not "every reconcile".
	newSpec := cp.Status.ObservedGeneration != cp.Generation
	stsCatchingUp := st.ObservedGeneration < sts.Generation
	changed := prior.at.IsZero() ||
		prior.revision != cur.revision ||
		prior.updatedReplicas != cur.updatedReplicas ||
		prior.readyReplicas != cur.readyReplicas
	if newSpec || stsCatchingUp || changed {
		return cur, false
	}
	// Nothing observable changed: keep the ORIGINAL timestamp. While the clock
	// runs, that is what makes the deadline measurable; while it does not (a
	// settled or HA-readiness-blocked object), it is what keeps status
	// idempotent — stamping `now` here was the per-reconcile rewrite the
	// comment above forbids, and TestReconcile_SettledStatusIsIdempotent
	// caught it whenever two reconciles straddled a second boundary. runClock
	// only decides whether the kept timestamp is compared against the
	// deadline, never whether it is preserved.
	kept := progressPoint{
		revision:        cur.revision,
		updatedReplicas: cur.updatedReplicas,
		readyReplicas:   cur.readyReplicas,
		at:              prior.at,
	}
	if !runClock {
		return kept, false
	}
	return kept, now.Sub(prior.at.Time) > progressDeadline(cp)
}

// progressDeadline resolves spec.progressDeadlineSeconds (default 600s).
func progressDeadline(cp *opsv1alpha1.ControlPlane) time.Duration {
	secs := cp.Spec.ProgressDeadlineSeconds
	if secs <= 0 {
		secs = defaultProgressDeadlineSeconds
	}
	return time.Duration(secs) * time.Second
}
