// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ComponentType, ReactNode } from 'react'
import type { RBACCatalogDTO, CustomRoleDTO, PermGroupDTO } from './console/api'
import type { PdpPublishResult, PdpRollbackResult } from './claude-policy/types'
import type { StepUpAttempt } from '@/stores/step-up'
import { useAuth } from '@/lib/auth/context'

/**
 * A panel that a build-time extension adds inside a public page: a tab or a card. The
 * default console carries none (`PANEL_EXTENSIONS` in ./extensions); a paid build's
 * extensions module supplies them. The panel makes its own reads and RBAC checks;
 * `permission` only decides whether the page offers it, as `FeatureView.permission`
 * does for a page.
 */
export interface PanelExtension {
  readonly id: string
  readonly permission?: string
  readonly Component: ComponentType
}

/** A panel shown as a tab: it names itself, evaluated on render for the current language. */
export interface TabExtension extends PanelExtension {
  readonly label: () => string
}

/**
 * One more way to sign a profile in, offered under "Other ways" in AI tools (Add profile
 * and a profile's Sign in). The default console offers none besides the terminal command.
 */
export interface ProfileSignInExtension {
  readonly id: string
  readonly permission?: string
  readonly Component: ComponentType<{
    /** The tool's driver key. */
    driver: string
    /** The profile's reference; absent for the organization's default login. */
    accountRef?: string
    /** The profile's name; absent for the organization's default login, which has none. */
    accountName?: string
  }>
}

/** The places in public pages that accept extension panels, in page order. */
export type AdditionalStepUpCeremony = (
  attempt: StepUpAttempt,
) => Promise<{ ok: boolean; aal?: number }>
export interface StepUpMethodProps {
  readonly disabled: boolean
  readonly run: (ceremony: AdditionalStepUpCeremony) => Promise<void>
}
export interface StepUpMethodExtension {
  readonly id: string
  readonly permission?: string
  readonly Component: ComponentType<StepUpMethodProps>
}

/**
 * What a session panel is told about the session on screen, and nothing more: the
 * reference to read it by, the folder it works in, the tool and the state. A panel makes
 * its own reads.
 */
export interface SessionPanelSession {
  /** The session's own reference (its run's, else its observed row's). */
  readonly ref: string
  readonly workspaceRef?: string
  /** The tool's driver name ("claude", "codex"). */
  readonly tool?: string
  /** The session's state word key as the engine reports it. */
  readonly state?: string
}

/** The ids of the side pane's own tabs: a registered panel cannot take one. */
export const RESERVED_SESSION_PANEL_IDS = [
  'context',
  'changes',
  'files',
  'preview',
] as const

/**
 * A panel of the session thread's side pane, after Context, Changes, Files and Preview. The public
 * console carries none; a paid build registers its own (`PANEL_EXTENSIONS.sessionPanels`).
 * `labelKey` is the tab's name as an i18n key (`namespace:path`) and `icon` its glyph. The
 * panel is a COMPONENT, mounted and unmounted as its tab is chosen, so it may use hooks;
 * `permission`, when set, must be held for the tab to show.
 */
export interface SessionPanelExtension {
  readonly id: string
  readonly labelKey: string
  readonly icon: ComponentType<{ className?: string }>
  readonly permission?: string
  readonly Component: ComponentType<{ session: SessionPanelSession }>
}

export interface AuthorizationForms {
  readonly GrantForm: ComponentType<{
    catalog?: RBACCatalogDTO
    roles: CustomRoleDTO[]
    workspaces: { slug: string; name: string }[]
    agentGroups: { slug: string; name: string }[]
    onClose: () => void
  }>
  readonly RoleForm: ComponentType<{
    catalog?: RBACCatalogDTO
    groups: PermGroupDTO[]
    existing?: CustomRoleDTO
    onClose: () => void
  }>
  readonly GroupForm: ComponentType<{
    catalog?: RBACCatalogDTO
    existing?: PermGroupDTO
    onClose: () => void
  }>
}
export interface CedarPolicyAuthoring {
  readonly publish: (source: string, note?: string) => Promise<PdpPublishResult>
  readonly rollback: (revision: number) => Promise<PdpRollbackResult>
}

/** The places in public pages that accept extension panels, in page order. */
export interface DashboardCostPanels {
  readonly permitted: boolean
  readonly loading: boolean
  readonly hasActivity: boolean
  readonly headline: ReactNode
  readonly homeTile: ReactNode
  readonly sections: ReactNode
  readonly floor: ReactNode
}

export interface DashboardRedteamPanels {
  readonly permitted: boolean
  readonly query: import('@tanstack/react-query').UseQueryResult<
    import('@/lib/api/types').ListResponse<
      import('@/features/redteam/types').Run
    >
  >
  readonly deriveRisk: (
    findings?: import('@/lib/api/types').ListResponse<
      import('@/features/security/types').Finding
    >,
    drift?: import('@/features/access-map/types').DiffResponse,
  ) => import('@/features/executive/derive').RiskKpi | null
  readonly description: string
  readonly panel: ReactNode
}

export interface PanelExtensions {
  /** The edition supplies operations export; stored choices remain portable. */
  readonly operationsExportAvailable?: true
  /** Optional action beside the local trace detail. */
  readonly traceExportAction?: ComponentType<{ traceId: string }>
  readonly authorizationForms?: AuthorizationForms
  readonly cedarPolicyAuthoring?: CedarPolicyAuthoring
  readonly identityLoginCards?: readonly PanelExtension[]
  readonly identityStepUpMethods?: readonly StepUpMethodExtension[]
  /** Session thread: panels in the side pane, after Context, Changes, Files and Preview. */
  readonly sessionPanels?: readonly SessionPanelExtension[]
  /** Governance: edition-provided tabs after agent risk. */
  readonly governanceTabs?: readonly TabExtension[]
  readonly useDashboardRedteam?: () => DashboardRedteamPanels
  readonly useDashboardCost?: (
    params: { since: string },
    models?: import('@/lib/api/types').ListResponse<
      import('@/features/models/types').GovernedModel
    >,
  ) => DashboardCostPanels
  /** Capabilities: tabs after Tools. */
  readonly capabilitiesTabs: readonly TabExtension[]
  /** Compliance: tabs after the stored-records tabs. */
  readonly complianceView?: ComponentType
  readonly complianceTabs: readonly TabExtension[]
  /** Reporting: cards below the report catalog. */
  readonly reportingCards: readonly PanelExtension[]
  /** Console › License: cards below the license. */
  readonly licenseCards: readonly PanelExtension[]
  /** Console › Scopes: cards below the workspaces. */
  readonly scopesCards: readonly PanelExtension[]
  /**
   * Console › SSO: a tab that replaces the single-provider SSO tab for a superadmin. It
   * may reuse `SSOOverview` and `SSOForm` from console/sso-tab.
   */
  readonly ssoTab?: ComponentType
  /** AI tools › sign in a profile: further ways beside the terminal command. */
  readonly profileSignInWays?: readonly ProfileSignInExtension[]
}

/** The panels of one place that the signed-in user is offered. */
export function useOfferedPanels<P extends { readonly permission?: string }>(
  panels: readonly P[],
): readonly P[] {
  const { can } = useAuth()
  return panels.filter((p) => p.permission === undefined || can(p.permission))
}
