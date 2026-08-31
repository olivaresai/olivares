// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Side-effect-free route inventories shared by the standalone AT gate, the
// Playwright axe checks, and the FEATURE_VIEWS coverage guard.

export const SESSION_VIEWER_ROUTE = '/session-viewer/sess-a11y'

export const AUTH_ROUTES = [
  '/',
  '/onboarding',
  '/workspace',
  '/inventory',
  '/sessions',
  '/access-map',
  '/audit',
  '/health',
  '/console',
  '/capabilities',
  '/communications/protocol-bindings',
  '/permissions',
  '/identity',
  '/claude-policy',
  '/routine-policies',
  //the AgentCore Cedar export route (registry id agentcoreExport).
  '/agentcore-export',
  '/deploy',
  '/knowledge',
  '/catalog',
  '/killswitch',
  '/work',
  '/agentops',
  '/agent-artifacts',
  '/workspace-templates',
  '/eventing',
  '/automations',
  '/inference-proxy',
  '/alerting',
  '/models',
  '/model-operations',
  '/finops',
  '/adoption',
  '/evals',
  '/security',
  '/recordings',
  SESSION_VIEWER_ROUTE,
  '/compliance',
  '/posture-export',
  '/orchestration',
  '/voice',
  '/sandbox',
  '/red-team',
  '/dashboards',
  '/team-costs',
  '/reporting',
  '/observability',
  '/platforms',
  '/rate-limits',
  '/attestation',
  '/api-playground',
  '/backups',
  '/logs',
  '/residency',
  '/tenants',
  '/settings',
]

// ⛔ `/accept-invite` FALTABA — añadida el 2026-08-18. El motor manda ese enlace por correo
//    (`core/api/handlers_onboarding.go`) y es literalmente la primera pantalla de producto que ve
//    una persona invitada, así que no visitarla dejaba sin medir el arranque de todo cliente nuevo
//    que no sea el que instala. Renderiza sin sesión y sin token: es un estado válido y capturable.
export const PUBLIC_ROUTES = [
  '/login',
  '/setup',
  '/accept-invite',
  '/status-page',
]

export const ROUTES = [
  '/dashboards',
  '/access-map',
  '/inventory',
  '/security',
  '/compliance',
  '/health',
  //the onboarding wizard and the two new GRC surfaces.
  '/onboarding',
  '/reporting',
  '/posture-export',
  //the mutation-dense admin surfaces (tab panels, forms, dialogs):
  // the control console, identity/NHI, and the Claude policy editors. These
  // carry the most interactive controls per page, so the quick axe pass must
  // cover them, not only the read-mostly dashboards above.
  '/console',
  '/identity',
  '/claude-policy',
]
