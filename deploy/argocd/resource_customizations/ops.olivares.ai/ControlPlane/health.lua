-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
--
-- Argo CD custom resource health check for the Olivares AI control
-- plane CRD (ops.olivares.ai/v1alpha1 ControlPlane, reconciled by the Operator —
--). Install it so Argo CD reports the health of an Olivares control plane it
-- manages, instead of leaving a custom resource permanently "Progressing". The
-- contract (verified
-- against argo-cd.readthedocs.io/operator-manual/health/): the resource object is
-- the global `obj`; return a table `hs` with `hs.status` (one of Healthy /
-- Progressing / Degraded / Suspended) and an optional `hs.message`.
--
-- Delivery: place this tree under the Argo CD `resource_customizations` directory,
-- or inline the script as
-- `resource.customizations.health.ops.olivares.ai_ControlPlane`
-- in the `argocd-cm` ConfigMap. Validate with:
--   argocd admin settings resource-overrides health <control-plane.yaml>
-- and unit-test it with `go test ./util/lua/` against health_test.yaml.

local hs = {}

-- A reconciler that has not yet written status leaves the resource Progressing
-- (honest: unknown is not healthy), never silently Healthy.
if obj.status == nil then
  hs.status = "Progressing"
  hs.message = "Waiting for the Operator to report status"
  return hs
end

-- Conditions are independent: a serving control plane can still be progressing,
-- and a safely clamped control plane can be available but degraded. Collect them
-- before deciding so array order cannot change the reported health.
local available = nil
local progressing = nil
local degraded = nil
if obj.status.conditions ~= nil then
  for _, condition in ipairs(obj.status.conditions) do
    if condition.type == "Available" then
      available = condition
    elseif condition.type == "Progressing" then
      progressing = condition
    elseif condition.type == "Degraded" then
      degraded = condition
    end
  end
end

-- Degraded and Invalid always win, even if the last serving replica is still
-- available. Reporting either as Healthy would hide an operator action item.
if degraded ~= nil and degraded.status == "True" then
  hs.status = "Degraded"
  hs.message = degraded.message or "ControlPlane is degraded"
  return hs
end
if obj.status.phase == "Invalid" then
  hs.status = "Degraded"
  hs.message = "ControlPlane spec is invalid"
  return hs
end

-- An advancing rollout remains Progressing even when clients can reach the old
-- replica. Pending and coarse Progressing phases are likewise not Healthy.
if progressing ~= nil and progressing.status == "True" then
  hs.status = "Progressing"
  hs.message = progressing.message or "ControlPlane is progressing"
  return hs
end
if obj.status.phase == "Pending" or obj.status.phase == "Progressing" then
  hs.status = "Progressing"
  hs.message = "ControlPlane phase: " .. tostring(obj.status.phase)
  return hs
end

-- Ready is healthy only when the independent availability signal agrees. The
-- condition-only branch supports status written during a version transition.
if available ~= nil and available.status == "True" and
    (obj.status.phase == "Ready" or
     (obj.status.phase == nil and progressing ~= nil and progressing.status == "False")) then
  hs.status = "Healthy"
  hs.message = available.message or "ControlPlane is ready"
  return hs
end

hs.status = "Progressing"
hs.message = "Waiting for ControlPlane to become ready"
return hs
