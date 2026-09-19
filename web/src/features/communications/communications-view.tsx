// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQueryClient } from '@tanstack/react-query'
import {
  Handshake,
  Inbox,
  KeyRound,
  MailPlus,
  MessagesSquare,
} from 'lucide-react'
import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { WorkspaceRequiredState } from '@/features/shared'
import { ForbiddenState } from '@/components/ui/error-state'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { IntelPage, SectionCard } from '@/features/_intel'
import {
  invalidateCapabilities,
  useCapability,
  useCapabilityPreflight,
} from '@/lib/auth/capabilities'
import { useAuth } from '@/lib/auth/context'
import { useUrlState } from '@/lib/hooks/use-url-state'
import { communicationsKeys } from './api'
import { administrationSurfaceQuestion } from './capabilities'
import { useCommunicationsScope, type CommunicationsScope } from './boundary'
import { ChannelAdminSheet } from './channel-admin-sheet'
import { ChannelAdministration } from './channel-administration'
import { ChannelCatalog } from './channel-catalog'
import { ChannelCreateForm } from './channel-create-form'
import { ChannelSheet } from './channel-sheet'
import { ComposeDialog } from './compose-dialog'
import { DeliverySheet } from './delivery-sheet'
import { HandoffInbox } from './handoff-inbox'
import { HandoffResponseHost } from './handoff-response-host'
import type { HandoffRespondTarget } from './handoff-respond-dialog'
import { HandoffSheet } from './handoff-sheet'
import { InboxTable } from './inbox-table'
import { MessageSheet } from './message-sheet'
import type { Me } from './subject-picker'
import type { Channel, ChannelAccess, ChannelCatalogItem } from './types'
import './i18n'

/** Which door opened this view. The four routes mount ONE room; the entrance only
 * decides the page title and the tab that opens first. Each tab is offered on its
 * own permission, and every act inside checks its own tier where it acts. */
export type CommunicationsEntrance =
  'catalog' | 'inbox' | 'new' | 'administration' | 'handoffs'

type Tab = 'channels' | 'inbox' | 'handoffs' | 'create' | 'administration'

const ID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
/** `handoff` names the carrier Delivery and nothing else: there is no
 *  `GET /handoffs/{id}` to resolve a handoff id with, and neither an ETag nor an
 *  operation key belongs in a URL. `delivery` keeps its own meaning — it opens the
 *  ordinary delivery sheet, a different read under a different projection. */
const URL_KEYS = [
  'channel',
  'delivery',
  'message',
  'admin_channel',
  'handoff',
] as const

/**
 * CommunicationsView — the console room of the K3 communication kernel: catalog
 * and channel card (channel:read), the personal inbox with delivery and message
 * reads (delivery:read, message:read), channel creation with explicit grants
 * (channel:write), direct notices (message-send:write), the explicit Ack and the
 * personal seen cursor (delivery:write), and — I2 — channel administration
 * (channel:admin): the administrable catalog, configuration and grants.
 *
 * The room is remounted on the authority boundary AND on the explicit workspace:
 * nothing read, drafted or half-confirmed under one scope survives into the next,
 * the previous scope's cache partition is cancelled and removed, and a late
 * response of a previous scope has nowhere to land. Without an explicit workspace
 * nothing is requested: the console never selects one on the operator's behalf.
 */
export function CommunicationsView({
  entrance = 'catalog',
}: {
  entrance?: CommunicationsEntrance
}) {
  const scope = useCommunicationsScope()
  return <Inner key={scope.key} entrance={entrance} scope={scope} />
}

function Inner({
  entrance,
  scope,
}: {
  entrance: CommunicationsEntrance
  scope: CommunicationsScope
}) {
  const { t } = useTranslation('communications')
  const { can, principal } = useAuth()
  const queryClient = useQueryClient()
  const preflight = useCapabilityPreflight()

  const canChannelRead = can('sessions:channel:read')
  const canChannelWrite = can('sessions:channel:write')
  const canDeliveryRead = can('sessions:delivery:read')
  const canDeliveryWrite = can('sessions:delivery:write')
  const canMessageRead = can('sessions:message:read')
  const canSend = can('sessions:message-send:write')
  const canHandoffRespond = can('sessions:handoff-response:write')
  const canUserRead = can('user:read')
  const canAgentRead = can('agent:read')

  // Personal handoffs require a tenant member. Use the principal's explicit flag;
  // an unresolved principal must not be classified as a global administrator.
  const globalSuperadminAccount = principal?.superadmin === true

  const me: Me = {
    userId: principal?.kind === 'user' ? principal.user_id : null,
    label: principal?.display_name ?? principal?.actor ?? '',
  }

  // ⛔ THE ADMINISTRATION DOOR NO LONGER ASKS WHOAMI. `sessions:channel:admin` is a
  //    tenant-wide membership fact; this door's authority is a workspace-scoped ADMISSION
  //    the engine decides, and the two disagree in both directions — a scoped authored
  //    grant that the permission set never names, and a reflected permission an authored
  //    policy forbids. So the engine is asked for THIS workspace, and only a current
  //    `reachable` opens the tab. Unknown is not authority: the tab stays closed and the
  //    collection is never fetched, while the installed navigation LINK may remain.
  //
  // Global accounts cannot receive tenant-member capability evidence. Keep the
  // hook unconditional, but leave this surface question unsubmitted for them.
  // A null question returns unknown without a permit. Unresolved principals and
  // all other questions retain their existing behavior.
  const administration = useCapability(
    globalSuperadminAccount
      ? null
      : administrationSurfaceQuestion(scope.workspace),
  )
  const administrationReachable = administration.access === 'reachable'

  const tabPermitted: Record<Tab, boolean> = {
    channels: canChannelRead,
    inbox: canDeliveryRead,
    // The personal handoff page is a `delivery:read` collection, exactly like the
    // ordinary inbox: the engine declares no separate read tier for it. Responding
    // is gated apart, where it acts.
    handoffs: canDeliveryRead,
    create: canChannelWrite,
    administration: administrationReachable,
  }
  const order: Tab[] = [
    'channels',
    'inbox',
    'handoffs',
    'create',
    'administration',
  ]
  const wanted: Tab =
    entrance === 'inbox'
      ? 'inbox'
      : entrance === 'handoffs'
        ? 'handoffs'
        : entrance === 'new'
          ? 'create'
          : entrance === 'administration'
            ? 'administration'
            : 'channels'
  const firstPermitted = order.find((x) => tabPermitted[x]) ?? null
  const [tab, setTab] = useState<Tab>(() =>
    tabPermitted[wanted] ? wanted : (firstPermitted ?? 'channels'),
  )
  // A tab whose permission is gone right now is neither shown nor left selected.
  const effective: Tab | null = tabPermitted[tab] ? tab : firstPermitted

  const [url, patchUrl] = useUrlState(URL_KEYS)
  const urlChannel =
    url.channel && ID.test(url.channel) ? url.channel : undefined
  const urlDelivery =
    url.delivery && ID.test(url.delivery) ? url.delivery : undefined
  const urlMessage =
    url.message && ID.test(url.message) ? url.message : undefined
  const urlAdminChannel =
    url.admin_channel && ID.test(url.admin_channel)
      ? url.admin_channel
      : undefined
  const urlHandoff =
    url.handoff && ID.test(url.handoff) ? url.handoff : undefined

  const [channelSheet, setChannelSheet] = useState<{
    id: string
    access: ChannelAccess | null
  } | null>(() =>
    urlChannel && canChannelRead ? { id: urlChannel, access: null } : null,
  )
  const [compose, setCompose] = useState<Channel | null>(null)
  const [deliverySheet, setDeliverySheet] = useState<string | null>(() =>
    urlDelivery && canDeliveryRead ? urlDelivery : null,
  )
  const [messageSheet, setMessageSheet] = useState<string | null>(() =>
    urlMessage && canMessageRead ? urlMessage : null,
  )
  // ⛔ A VALID DEEP LINK OPENS ON ITS OWN QUESTION, NOT ON THE COLLECTION'S. The sheet asks
  //    the exact `GET /channels/{id}/grants` operation for this one channel and enables
  //    itself on that answer alone, so a principal admitted to one row and not to the list
  //    reaches it — which is the ordinary shape of a scoped grant, not a loophole. Nothing
  //    is gated here on the surface, and nothing is gated on a read tier.
  const [adminSheet, setAdminSheet] = useState<string | null>(
    () => urlAdminChannel ?? null,
  )
  // A direct handoff link is admitted by the same presentation guard as the
  // ordinary delivery sheet: it resolves through the same Delivery-bound route,
  // the engine still decides the read, and a concealed row answers 404.
  const [handoffSheet, setHandoffSheet] = useState<string | null>(() =>
    urlHandoff && canDeliveryRead ? urlHandoff : null,
  )
  /**
   * The selection follows the URL for the life of the mount, not only at the first
   * render: back, forward and any other navigation that changes this key must
   * select the new Delivery and start its own read. A value that fails the
   * canonical-id check or the read guard selects nothing.
   */
  const admittedUrlHandoff = urlHandoff && canDeliveryRead ? urlHandoff : null
  const [seenUrlHandoff, setSeenUrlHandoff] = useState(admittedUrlHandoff)
  if (seenUrlHandoff !== admittedUrlHandoff) {
    setSeenUrlHandoff(admittedUrlHandoff)
    setHandoffSheet(admittedUrlHandoff)
  }
  const [respondTarget, setRespondTarget] =
    useState<HandoffRespondTarget | null>(null)
  /** Bumped when a response resolves or conflicts, so the sheet rereads. */
  const [handoffRefresh, setHandoffRefresh] = useState(0)
  /** The sheet control that opened the response dialog, captured at the click. */
  const responseOpener = useRef<HTMLElement | null>(null)
  const sheetFallbackFocus = useRef<() => HTMLElement | null>(() => null)

  const openChannel = (item: ChannelCatalogItem) => {
    setChannelSheet({ id: item.id, access: item.my_access })
    patchUrl({ channel: item.id })
  }
  const openChannelById = (id: string) => {
    setChannelSheet({ id, access: null })
    patchUrl({ channel: id })
  }
  const closeChannel = () => {
    setChannelSheet(null)
    patchUrl({ channel: undefined })
  }
  const openDelivery = (id: string) => {
    setDeliverySheet(id)
    patchUrl({ delivery: id })
  }
  const closeDelivery = () => {
    setDeliverySheet(null)
    patchUrl({ delivery: undefined })
  }
  const openMessage = (id: string) => {
    if (!canMessageRead) return
    setMessageSheet(id)
    patchUrl({ message: id })
  }
  const closeMessage = () => {
    setMessageSheet(null)
    patchUrl({ message: undefined })
  }
  const openHandoff = (deliveryId: string) => {
    setHandoffSheet(deliveryId)
    patchUrl({ handoff: deliveryId })
  }
  const closeHandoff = () => {
    setHandoffSheet(null)
    patchUrl({ handoff: undefined })
  }
  const openAdmin = (id: string) => {
    setAdminSheet(id)
    patchUrl({ admin_channel: id })
  }
  const closeAdmin = () => {
    setAdminSheet(null)
    patchUrl({ admin_channel: undefined })
  }
  const invalidateWorkspace = () => {
    // A successful administrative act may have changed what this principal may do next,
    // so every observation of this context is asked again. A 200 never MINTS a permit.
    invalidateCapabilities(queryClient, preflight.context)
    if (scope.workspace) {
      void queryClient.invalidateQueries({
        queryKey: communicationsKeys.workspaceScope(
          scope.tenant,
          scope.epoch,
          scope.workspace,
        ),
      })
    }
  }
  const checkCatalog = () => {
    invalidateWorkspace()
    setTab('channels')
  }
  /**
   * A resolved or conflicting response changed the durable row and the WorkItem's
   * ownership. The cached communications collections are invalidated, and the open
   * protected detail is refreshed through its own read owner, which no cache key
   * can reach.
   */
  const handoffResponseResolved = () => {
    invalidateWorkspace()
    setHandoffRefresh((n) => n + 1)
  }

  // ⛔ A ROOM WITH NO TAB IS STILL A ROOM WHEN THE URL NAMES ONE ENTITY. Refusing the whole
  //    page here would make an entity permit unusable for exactly the principal it exists
  //    for: one admitted to a single channel and to no collection in this workspace. The
  //    tabs disappear, the sheet opens on its own answer, and NOTHING fetches a collection.
  const door =
    entrance === 'inbox'
      ? 'inbox'
      : entrance === 'handoffs'
        ? 'handoffs'
        : entrance === 'new'
          ? 'new'
          : entrance === 'administration'
            ? 'administration'
            : 'catalog'
  const Icon =
    door === 'inbox'
      ? Inbox
      : door === 'handoffs'
        ? Handshake
        : door === 'new'
          ? MailPlus
          : door === 'administration'
            ? KeyRound
            : MessagesSquare

  return (
    <IntelPage
      icon={Icon}
      title={t(`doors.${door}.title`)}
      description={t(`doors.${door}.description`)}
    >
      {!scope.workspace ? (
        <WorkspaceRequiredState
          icon={<MessagesSquare />}
          title={t('workspace.requiredTitle')}
          description={t('workspace.requiredBody')}
        />
      ) : effective === null && adminSheet === null ? (
        <ForbiddenState />
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-muted/30 px-3 py-2 text-caption text-muted-foreground">
            <span>{t('workspace.scope')}</span>
            <Badge variant="outline">
              {scope.workspaceName || scope.workspace}
            </Badge>
          </div>
          {effective === null ? null : (
            <Tabs value={effective} onValueChange={(v) => setTab(v as Tab)}>
              <TabsList>
                {canChannelRead ? (
                  <TabsTrigger value="channels">
                    {t('tabs.channels')}
                  </TabsTrigger>
                ) : null}
                {canDeliveryRead ? (
                  <TabsTrigger value="inbox">{t('tabs.inbox')}</TabsTrigger>
                ) : null}
                {canDeliveryRead ? (
                  <TabsTrigger value="handoffs">
                    {t('tabs.handoffs')}
                  </TabsTrigger>
                ) : null}
                {canChannelWrite ? (
                  <TabsTrigger value="create">{t('tabs.create')}</TabsTrigger>
                ) : null}
                {administrationReachable ? (
                  <TabsTrigger value="administration">
                    {t('tabs.administration')}
                  </TabsTrigger>
                ) : null}
              </TabsList>
              {canChannelRead ? (
                <TabsContent value="channels" className="mt-4">
                  <ChannelCatalog
                    scope={scope}
                    canChannelRead={canChannelRead}
                    canChannelWrite={canChannelWrite}
                    onOpenChannel={openChannel}
                    onOpenById={openChannelById}
                    onCreate={() => setTab('create')}
                  />
                </TabsContent>
              ) : null}
              {canDeliveryRead ? (
                <TabsContent value="inbox" className="mt-4">
                  <InboxTable
                    scope={scope}
                    canDeliveryRead={canDeliveryRead}
                    canDeliveryWrite={canDeliveryWrite}
                    me={me}
                    onOpenDelivery={openDelivery}
                  />
                </TabsContent>
              ) : null}
              {canDeliveryRead ? (
                <TabsContent value="handoffs" className="mt-4">
                  <HandoffInbox
                    scope={scope}
                    canDeliveryRead={canDeliveryRead}
                    globalSuperadminAccount={globalSuperadminAccount}
                    onOpenHandoff={openHandoff}
                  />
                </TabsContent>
              ) : null}
              {canChannelWrite ? (
                <TabsContent value="create" className="mt-4">
                  <SectionCard
                    title={t('create.title')}
                    description={t('create.description')}
                  >
                    <ChannelCreateForm
                      scope={scope}
                      canChannelRead={canChannelRead}
                      canChannelWrite={canChannelWrite}
                      canUserRead={canUserRead}
                      canAgentRead={canAgentRead}
                      me={me}
                      onCheckCatalog={canChannelRead ? checkCatalog : undefined}
                    />
                  </SectionCard>
                </TabsContent>
              ) : null}
              {administrationReachable ? (
                <TabsContent value="administration" className="mt-4">
                  <ChannelAdministration
                    scope={scope}
                    onOpenChannel={openAdmin}
                  />
                </TabsContent>
              ) : null}
            </Tabs>
          )}
          {canChannelRead ? (
            <ChannelSheet
              open={channelSheet !== null}
              onOpenChange={(o) => (o ? undefined : closeChannel())}
              channelId={channelSheet?.id ?? null}
              catalogAccess={channelSheet?.access ?? null}
              scope={scope}
              canChannelRead={canChannelRead}
              canSend={canSend}
              onCompose={(channel) => setCompose(channel)}
            />
          ) : null}
          {compose && canSend ? (
            <ComposeDialog
              open
              onOpenChange={(o) => (o ? undefined : setCompose(null))}
              channel={{
                id: compose.id,
                name: compose.name,
                slug: compose.slug,
              }}
              scope={scope}
              canSend={canSend}
              canUserRead={canUserRead}
              canAgentRead={canAgentRead}
              me={me}
            />
          ) : null}
          {canDeliveryRead ? (
            <DeliverySheet
              open={deliverySheet !== null}
              onOpenChange={(o) => (o ? undefined : closeDelivery())}
              deliveryId={deliverySheet}
              scope={scope}
              canDeliveryRead={canDeliveryRead}
              canDeliveryWrite={canDeliveryWrite}
              canMessageRead={canMessageRead}
              onOpenMessage={openMessage}
            />
          ) : null}
          {canMessageRead ? (
            <MessageSheet
              open={messageSheet !== null}
              onOpenChange={(o) => (o ? undefined : closeMessage())}
              messageId={messageSheet}
              scope={scope}
              canMessageRead={canMessageRead}
            />
          ) : null}
          {canDeliveryRead ? (
            <HandoffSheet
              open={handoffSheet !== null}
              onOpenChange={(o) => (o ? undefined : closeHandoff())}
              deliveryId={handoffSheet}
              scope={scope}
              canDeliveryRead={canDeliveryRead}
              canRespond={canHandoffRespond}
              refreshSignal={handoffRefresh}
              registerFallbackFocus={(get) => {
                sheetFallbackFocus.current = get
              }}
              onRespond={(transition, target) => {
                // Captured at the gesture: this control lives inside the sheet,
                // where a focusin observer cannot see it.
                responseOpener.current =
                  document.activeElement instanceof HTMLElement
                    ? document.activeElement
                    : null
                setRespondTarget({ ...target, transition })
              }}
            />
          ) : null}
          {/* Mounted beside the sheet, not inside it: an unresolved response must
              outlive the panel that started it. */}
          <HandoffResponseHost
            scope={scope}
            canRespond={canHandoffRespond}
            canDeliveryRead={canDeliveryRead}
            target={respondTarget}
            getOpener={() => responseOpener.current}
            getFallbackFocus={() => sheetFallbackFocus.current()}
            onTargetConsumed={() => setRespondTarget(null)}
            onResolved={handoffResponseResolved}
            // A request for a fresh read, not a claim that anything settled: the
            // sheet's own read owner performs it under its current scope and
            // admission, and rejects a late result as it always does.
            onRequestFreshDetail={() => setHandoffRefresh((n) => n + 1)}
          />
          <ChannelAdminSheet
            open={adminSheet !== null}
            onOpenChange={(o) => (o ? undefined : closeAdmin())}
            channelId={adminSheet}
            scope={scope}
            canUserRead={canUserRead}
            canAgentRead={canAgentRead}
            me={me}
            onMutated={invalidateWorkspace}
          />
        </>
      )}
    </IntelPage>
  )
}
