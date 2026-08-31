---
title: "Recipe: human-in-the-loop approvals"
description: "Gate destructive actions behind governed approvals: open a request
  bound to the exact plan, let authorized humans decide with separation-of-duty
  and expiry enforced server-side, and get the decision recorded in the ledger."
sidebar:
  order: 3
slug: 2026-06/how-to/cookbook/hitl-approvals
---

**Goal:** "a deployment apply (or an orchestration fire, or a voice session
open) does not happen until a human who is *not* the requester approves it —
and the decision is a recorded fact."

The approval engine is live in the default binary; the
[governance model](/2026-06/how-to/govern-and-approve/#the-human-in-the-loop-posture)
explains the posture. This recipe is the operational wiring.

## 1. Wire the approval gate

Module actions that would mutate infrastructure pass through the
human-in-the-loop bridge. It is enabled by configuration — without it those
actions stay deny-closed:

```bash
OLIVARES_APPROVAL_BRIDGE_CONFIG=/etc/olivares/approval-bridge.json
```

Run the component that *opens* approvals as its **own service account that is
never in the approver pool**. Separation of duty is enforced engine-side (the
opener cannot decide its own request, and a system token cannot approve at
all) — if the opener's account is also an approver, you have built a liveness
deadlock, not a control.

## 2. Open a request

```bash
curl -ks -X POST "$BASE/v1/m/governance/approvals" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{
    "subject_kind": "deployment",
    "subject_ref": "deploy:payments-api",
    "action": "deploy.apply",
    "reason": "rollout v2.4.1",
    "expires_in_seconds": 3600
  }'
```

The request opens **deny-closed and time-boxed**, bound to the exact plan it
covers. If an enabled approval *policy* matches `(action, subject_kind)`, the
policy's `required_approvals` is authoritative — a requester cannot lower the
bar from the request side.

## 3. Decide

```bash
# The queue (filter by status / action):
curl -ks "$BASE/v1/m/governance/approvals?status=pending" \
  -H "Authorization: Bearer $APPROVER_TOKEN" -H "X-Olivares-Tenant: $TENANT"

# The decision (approval-admin permission):
curl -ks -X POST "$BASE/v1/m/governance/approvals/$ID/decisions" \
  -H "Authorization: Bearer $APPROVER_TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"decision":"approve","note":"reviewed the plan hash"}'
```

What the engine enforces server-side — none of this is client convention:

* **Separation of duty:** the decider is keyed on the stable user id; the
  requester cannot decide, and the same human cannot decide twice (a unique
  index, not a UI rule).
* **Expiry:** an expired request can never receive a binding decision, even
  before the sweeper materializes the state.
* **Risk-tier floor:** actions pre-classified CRITICAL (the kill-switch
  family, credential finalization and peers) require **at least two distinct
  human approvers with strong (AAL3) authentication per decision** — and the
  floor is structural: an approval policy that tries to downgrade the tier is
  re-floored at the decision point.

## 4. The record

Every decision is appended to the audit ledger with the real actor in the
same transaction — `GET /v1/m/governance/approvals/{id}/decisions` is the
immutable trail, and the [pull export](/2026-06/how-to/forward-audit-to-splunk/)
carries it to your SIEM. You cannot make a governed change the ledger
silently forgets.

## Notes

* `escalate_in_seconds` notifies the SoD team if a request sits undecided —
  use it for production-critical actions.
* Cancel (`POST …/{id}/cancel`) is for the requester or an admin on a
  pending request; it is recorded too.
* What is still maturing is the richer review **console**; the engine-side
  guarantees above are live ([honest scope](/2026-06/how-to/govern-and-approve/)).
