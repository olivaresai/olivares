// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE START COMPOSER'S PILLS: tool and account, model, effort, folder. Each is a native button
// that opens its list (Enter or Space as any button); the values and what a change does belong
// to the start form (first-hour.tsx StartSessionChoices).
import { ChevronDown } from 'lucide-react'
import { useState, type ComponentProps } from 'react'
import { useTranslation } from 'react-i18next'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { AccountUsage, AtLimit } from '@/features/agentops/account-usage'
import {
  useModelTriggerText,
  type ModelChoices,
} from '@/features/agentops/model-choices'
import { ModelList } from '@/features/agentops/model-effort-picker'
import { ProviderAccent } from '@/features/agentops/provider-accent'
import { ToolMark } from '@/features/agentops/tool-mark'
import { cn } from '@/lib/utils'
import { sessionTools, toolName, type SessionTool } from './api'
import type { SessionProfiles } from './session-profiles'

/** The list values of "the key's or the tool's default", the default login, "a new folder
 * of the session's own" and "a path I type": a model ID, account or workspace ref has no
 * spaces, so none of them is a real one. Radix refuses an empty value. */
export const DEFAULT_MODEL = ' default'
export const DEFAULT_PROFILE = ' default-login'
export const FOLDER_NEW = ' new-folder'
export const FOLDER_TYPED = ' typed-folder'
const DEFAULT_EFFORT = ' default-effort'

/** One line of the composer's height; 44 px under a finger. */
const PILL =
  'inline-flex h-7 min-w-0 max-w-56 shrink items-center gap-1.5 whitespace-nowrap rounded-full border border-border px-2.5 text-caption text-text-2 outline-none hover:bg-hover hover:text-text focus-visible:ring-2 focus-visible:ring-focus disabled:pointer-events-none disabled:opacity-50 pointer-coarse:h-11'

function Pill({
  label,
  children,
  className,
  ...props
}: ComponentProps<'button'> & { label: string }) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      className={cn(PILL, className)}
      {...props}
    >
      {children}
      <ChevronDown aria-hidden className="size-3 shrink-0" />
    </button>
  )
}

/** The tool, and under it the accounts of that tool with their share of the week. */
export function ToolPill({
  tool,
  onTool,
  profiles,
}: {
  tool: SessionTool
  onTool: (tool: SessionTool) => void
  profiles: SessionProfiles | null | undefined
}) {
  const { t } = useTranslation('firstHour')
  const account = profiles?.selected
    ? profiles.choices.find((c) => c.value === profiles.selected)
    : undefined
  const value = account
    ? `${toolName(tool)} · ${account.label}`
    : toolName(tool)
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Pill label={t('composer.toolPill', { value })}>
          <ToolMark driver={tool} />
          <span className="truncate">{value}</span>
          {account?.usage ? (
            <AccountUsage usage={account.usage} className="shrink-0" />
          ) : null}
        </Pill>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="max-w-[calc(100vw-2rem)]">
        <DropdownMenuRadioGroup
          value={tool}
          onValueChange={(v) => {
            const next = sessionTools().find((d) => d === v)
            if (next) onTool(next)
          }}
        >
          {sessionTools().map((d) => (
            <DropdownMenuRadioItem key={d} value={d}>
              <ToolMark driver={d} />
              {toolName(d)}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
        {profiles && profiles.choices.length > 1 ? (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuLabel>{t('start.profile')}</DropdownMenuLabel>
            <DropdownMenuRadioGroup
              value={profiles.selected || DEFAULT_PROFILE}
              onValueChange={(v) =>
                profiles.select(v === DEFAULT_PROFILE ? '' : v)
              }
            >
              {profiles.choices.map((c) => (
                <DropdownMenuRadioItem
                  key={c.value || DEFAULT_PROFILE}
                  value={c.value || DEFAULT_PROFILE}
                  disabled={(!c.ready && c.value !== '') || c.atLimit}
                  className="h-auto min-h-8 py-1"
                >
                  <ProviderAccent accent={c.accent} />
                  <span className="min-w-0 truncate">
                    {c.ready
                      ? c.label
                      : `${c.label} · ${t(c.missing === 'key' ? 'start.profileNeedsKey' : 'start.profileNotSignedIn')}`}
                  </span>
                  {c.usage ? (
                    <AccountUsage usage={c.usage} className="ms-auto ps-3" />
                  ) : null}
                  {c.atLimit ? (
                    <AtLimit usage={c.usage} quiet className="ms-2" />
                  ) : null}
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
          </>
        ) : null}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/** The model: the tool's live list, or for a tool on a key the models its last connection
 * test passed (`tested`), with the key's saved default (`keyDefault`). */
export function ModelPill({
  tool,
  choices,
  model,
  onModel,
  tested,
  keyDefault = '',
  noDefault = false,
}: {
  tool: SessionTool
  choices: ModelChoices
  model: string
  onModel: (model: string) => void
  tested?: string[]
  keyDefault?: string
  noDefault?: boolean
}) {
  const { t } = useTranslation('firstHour')
  const [open, setOpen] = useState(false)
  const listed = useModelTriggerText(tool, choices, model)
  if (tested) {
    const keyText = keyDefault
      ? t('start.providerDefault', { model: keyDefault })
      : t('start.nativeDefault', { tool: toolName(tool) })
    const value = model || (noDefault ? t('start.model') : keyText)
    return (
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Pill
            label={t('composer.modelPill', { value })}
            disabled={tested.length === 0 && noDefault}
          >
            <span className="truncate">{value}</span>
          </Pill>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start">
          <DropdownMenuRadioGroup
            value={model || DEFAULT_MODEL}
            onValueChange={(v) => onModel(v === DEFAULT_MODEL ? '' : v)}
          >
            {noDefault ? null : (
              <DropdownMenuRadioItem value={DEFAULT_MODEL}>
                {keyText}
              </DropdownMenuRadioItem>
            )}
            {tested.map((m) => (
              <DropdownMenuRadioItem key={m} value={m}>
                {m}
              </DropdownMenuRadioItem>
            ))}
          </DropdownMenuRadioGroup>
        </DropdownMenuContent>
      </DropdownMenu>
    )
  }
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Pill label={t('composer.modelPill', { value: listed })}>
          <span className="truncate">{listed}</span>
        </Pill>
      </PopoverTrigger>
      <PopoverContent
        align="start"
        className="w-80 max-w-[calc(100vw-2rem)] p-0"
      >
        <ModelList
          driver={tool}
          choices={choices}
          value={model}
          onPick={(next) => {
            onModel(next)
            setOpen(false)
          }}
        />
      </PopoverContent>
    </Popover>
  )
}

/** The effort levels of the model the session starts on; none while no model is known. */
export function EffortPill({
  efforts,
  value,
  defaultEffort,
  onChange,
}: {
  efforts: string[] | null
  value: string
  defaultEffort?: string
  onChange: (effort: string) => void
}) {
  const { t } = useTranslation('firstHour')
  const { t: agentops } = useTranslation('agentops')
  const name = (e: string) => agentops(`effort.${e}`, { defaultValue: e })
  const modelDefault = defaultEffort
    ? agentops('picker.effortDefaultNamed', { effort: name(defaultEffort) })
    : agentops('create.effortDefault')
  const shown = value ? name(value) : modelDefault
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Pill
          label={t('composer.effortPill', { value: shown })}
          disabled={efforts === null}
          title={
            efforts === null ? agentops('picker.effortNeedsModel') : undefined
          }
        >
          <span className="truncate">{shown}</span>
        </Pill>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start">
        <DropdownMenuRadioGroup
          value={value || DEFAULT_EFFORT}
          onValueChange={(v) => onChange(v === DEFAULT_EFFORT ? '' : v)}
        >
          <DropdownMenuRadioItem value={DEFAULT_EFFORT}>
            {modelDefault}
          </DropdownMenuRadioItem>
          {(efforts ?? []).map((e) => (
            <DropdownMenuRadioItem key={e} value={e}>
              {name(e)}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/** The folder: the recent folders, a new folder of its own, another one typed, and More
 * options (the advanced launch). */
export function FolderPill({
  label,
  value,
  folders,
  onPick,
  onMore,
  onCloseAutoFocus,
}: {
  label: string
  value: string
  folders: { value: string; label: string }[]
  onPick: (value: string) => void
  onMore?: () => void
  onCloseAutoFocus: (event: Event) => void
}) {
  const { t } = useTranslation('firstHour')
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Pill label={t('composer.folderPill', { value: label })}>
          <span className="truncate">{label}</span>
        </Pill>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="start"
        className="max-w-[calc(100vw-2rem)]"
        onCloseAutoFocus={onCloseAutoFocus}
      >
        <DropdownMenuRadioGroup value={value} onValueChange={onPick}>
          {folders.map((f) => (
            <DropdownMenuRadioItem
              key={f.value}
              value={f.value}
              title={f.label}
            >
              <span className="min-w-0 truncate">{f.label}</span>
            </DropdownMenuRadioItem>
          ))}
          <DropdownMenuRadioItem value={FOLDER_NEW}>
            {t('start.folderNew')}
          </DropdownMenuRadioItem>
          <DropdownMenuRadioItem value={FOLDER_TYPED}>
            {t('start.folderTyped')}
          </DropdownMenuRadioItem>
        </DropdownMenuRadioGroup>
        {onMore ? (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem onSelect={onMore}>
              {t('composer.moreOptions')}
            </DropdownMenuItem>
          </>
        ) : null}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
