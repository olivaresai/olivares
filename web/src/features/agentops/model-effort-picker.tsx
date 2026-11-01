// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// MODEL AND EFFORT, ONE CONTROL FOR EVERY PLACE A SESSION IS CHOSEN.
//
// The New session form, the launch dialog and the session's message bar show the same
// two choices from the same source: the live catalog of the session's profile, read from
// the installed tool or the bound provider (`model-catalog.ts`). No model is named here.
// When the tool cannot list its models, the list is only "the tool's default" and a
// typed model, said in one sentence; the launch validates what is typed.
//
// Launch speed choices use the same catalog in the full launch dialog; this shared
// picker owns model and effort.
import { Check, ChevronsUpDown, Star } from 'lucide-react'
import { useId, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Command,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from '@/components/ui/command'
import { Field } from '@/components/ui/field'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { cn } from '@/lib/utils'
import {
  effortAfterModel,
  modelLabel,
  useModelChoices,
  useModelTriggerText,
  type CatalogStatus,
  type ModelChoices,
} from './model-choices'
import { groupModels } from './model-catalog'
import { useModelFavorites } from './model-favorites'
import { toolName } from './engine-tools'
import type { ProfileModelDTO } from './types'
import './i18n'

/** cmdk trims item values, so a sentinel cannot be told by its whitespace; these are
 * names no tool gives a model. */
const DEFAULT_ITEM = '__tool_default__'
const TYPED_ITEM = '__typed__:'
/** Radix Select refuses an empty item value. */
const EFFORT_DEFAULT = '__model_default__'

function statusSentence(status: CatalogStatus): string {
  if (status === 'loading') return 'picker.loading'
  if (status === 'failed') return 'picker.failed'
  if (status === 'unsupported') return 'picker.unsupported'
  // No list to read: no profile yet, or no right to read one.
  if (status === 'none') return 'picker.unavailable'
  return ''
}

/** The models that match what was typed, by id or display name. The list is filtered
 * here, not by cmdk, so a row and its star leave together. */
function matching(
  models: readonly ProfileModelDTO[],
  query: string,
): ProfileModelDTO[] {
  const q = query.trim().toLowerCase()
  if (!q) return [...models]
  return models.filter(
    (m) =>
      m.id.toLowerCase().includes(q) ||
      (m.display_name ?? '').toLowerCase().includes(q),
  )
}

/** The model list: a search over the tool's models, the person's favorites first and the
 * retired ones last, plus "the tool's default" and any typed model. */
export function ModelList({
  driver,
  choices,
  value,
  onPick,
}: {
  driver: string
  choices: ModelChoices
  value: string
  onPick: (model: string) => void
}) {
  const { t } = useTranslation('agentops')
  const { favorites, toggle } = useModelFavorites(driver)
  const [query, setQuery] = useState('')
  const models = choices.catalog?.models ?? []
  const groups = groupModels(matching(models, query), favorites)
  const defaultModel =
    models.find((m) => m.id === choices.catalog?.default_model) ??
    models.find((m) => m.is_default)
  const typed = query.trim()
  const offerTyped =
    typed !== '' &&
    choices.catalog?.free_text !== false &&
    !models.some((m) => m.id === typed)
  const said = statusSentence(choices.status)
  const q = query.trim().toLowerCase()
  const showDefault = q === '' || 'default'.includes(q)
  const nothing =
    !showDefault &&
    !offerTyped &&
    groups.favorites.length + groups.models.length + groups.legacy.length === 0

  const row = (m: ProfileModelDTO): ReactNode => {
    const starred = favorites.has(m.id)
    return (
      <div key={m.id} className="flex items-center gap-1">
        <CommandItem
          value={m.id}
          onSelect={() => onPick(m.id)}
          className="min-w-0 flex-1"
        >
          <span className="min-w-0 flex-1 truncate">{modelLabel(m)}</span>
          {m.is_default ? (
            <span className="shrink-0 text-caption text-muted-foreground">
              {t('picker.default')}
            </span>
          ) : null}
          {m.is_new ? (
            <span className="shrink-0 rounded-sm bg-accent-soft px-1 text-caption text-accent-soft-foreground">
              {t('picker.new')}
            </span>
          ) : null}
          <Check
            aria-hidden="true"
            className={cn(
              'size-4',
              m.id === value ? 'opacity-100' : 'opacity-0',
            )}
          />
        </CommandItem>
        {toggle ? (
          <button
            type="button"
            aria-pressed={starred}
            aria-label={t(starred ? 'picker.unstar' : 'picker.star', {
              model: modelLabel(m),
            })}
            onClick={() => toggle(m.id)}
            className="grid size-8 shrink-0 place-items-center rounded-md text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
          >
            <Star
              aria-hidden="true"
              className={cn(
                'size-4',
                starred && 'fill-current text-accent-text',
              )}
            />
          </button>
        ) : null}
      </div>
    )
  }

  return (
    <Command label={t('picker.search')} shouldFilter={false}>
      <CommandInput
        value={query}
        onValueChange={setQuery}
        placeholder={t('picker.search')}
      />
      <CommandList>
        {nothing ? (
          <p className="py-6 text-center text-body text-muted-foreground">
            {t('picker.none')}
          </p>
        ) : null}
        {showDefault ? (
          <CommandGroup>
            <CommandItem value={DEFAULT_ITEM} onSelect={() => onPick('')}>
              <span className="min-w-0 flex-1 truncate">
                {defaultModel
                  ? t('picker.defaultNamed', {
                      model: modelLabel(defaultModel),
                    })
                  : t('picker.defaultUnnamed', { tool: toolName(driver) })}
              </span>
              <Check
                aria-hidden="true"
                className={cn(
                  'size-4',
                  value === '' ? 'opacity-100' : 'opacity-0',
                )}
              />
            </CommandItem>
          </CommandGroup>
        ) : null}
        {groups.favorites.length > 0 ? (
          <CommandGroup heading={t('picker.favorites')}>
            {groups.favorites.map(row)}
          </CommandGroup>
        ) : null}
        {groups.models.length > 0 ? (
          <CommandGroup heading={t('picker.models')}>
            {groups.models.map(row)}
          </CommandGroup>
        ) : null}
        {groups.legacy.length > 0 ? (
          <CommandGroup heading={t('picker.legacy')}>
            {groups.legacy.map(row)}
          </CommandGroup>
        ) : null}
        {offerTyped ? (
          <CommandGroup>
            <CommandItem
              value={`${TYPED_ITEM}${typed}`}
              onSelect={() => onPick(typed)}
            >
              {t('picker.use', { model: typed })}
            </CommandItem>
          </CommandGroup>
        ) : null}
      </CommandList>
      {said ? (
        <p
          role="status"
          className="border-t border-border px-3 py-2 text-caption text-muted-foreground"
        >
          {t(said)}
        </p>
      ) : null}
    </Command>
  )
}

/** The effort levels of the model a session starts on. With no model known the control
 * is off: an effort is a level of a model, and the caller says so beside it. */
export function EffortSelect({
  efforts,
  value,
  onChange,
  defaultEffort,
  label,
}: {
  efforts: string[] | null
  value: string
  onChange: (effort: string) => void
  /** The effort the tool says the model starts on, when it says. */
  defaultEffort?: string
  label: string
}) {
  const { t } = useTranslation('agentops')
  const name = (e: string) => t(`effort.${e}`, { defaultValue: e })
  return (
    <Select
      value={value || EFFORT_DEFAULT}
      onValueChange={(v) => {
        // Radix bubbles '' through its hidden native select when the value moves to a
        // level whose option is not mounted yet: that is not a choice.
        if (v === '') return
        onChange(v === EFFORT_DEFAULT ? '' : v)
      }}
      disabled={efforts === null}
    >
      <SelectTrigger aria-label={label}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={EFFORT_DEFAULT}>
          {defaultEffort
            ? t('picker.effortDefaultNamed', { effort: name(defaultEffort) })
            : t('create.effortDefault')}
        </SelectItem>
        {(efforts ?? []).map((e) => (
          <SelectItem key={e} value={e}>
            {name(e)}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

/**
 * Model and Effort as two fields, for a form: no "advanced" step in front of either.
 * A change of model forgets an effort the new model does not take.
 */
export function ModelEffortFields({
  profileRef,
  driver,
  model,
  effort,
  onChange,
  enabled,
  fallbackModel = '',
  withModel = true,
}: {
  profileRef: string
  driver: string
  model: string
  effort: string
  onChange: (next: { model: string; effort: string }) => void
  enabled: boolean
  fallbackModel?: string
  /** False when another control already chooses the model (a key's tested models):
   * only the effort of the model the session starts on is shown. */
  withModel?: boolean
}) {
  const { t } = useTranslation('agentops')
  const choices = useModelChoices({
    profileRef,
    driver,
    model,
    enabled,
    fallbackModel,
  })
  const listId = useId()
  const [open, setOpen] = useState(false)
  const trigger = useModelTriggerText(driver, choices, model)
  return (
    <div
      className={cn(
        'grid grid-cols-1 gap-3',
        withModel && 'min-[480px]:grid-cols-2',
      )}
    >
      {withModel ? (
        <Field label={t('create.model')}>
          <Popover open={open} onOpenChange={setOpen}>
            <PopoverTrigger asChild>
              <button
                type="button"
                aria-haspopup="listbox"
                aria-expanded={open}
                aria-controls={open ? listId : undefined}
                aria-label={t('create.model')}
                title={trigger}
                className="flex h-8 w-full min-w-0 items-center justify-between gap-2 rounded-md border border-ctl-border bg-surface px-2.5 text-left text-body text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1 focus-visible:ring-offset-background [&_svg]:size-4 [&_svg]:shrink-0"
              >
                <span className="min-w-0 truncate">{trigger}</span>
                <ChevronsUpDown
                  aria-hidden="true"
                  className="text-muted-foreground"
                />
              </button>
            </PopoverTrigger>
            <PopoverContent
              id={listId}
              align="start"
              sideOffset={4}
              className="w-80 max-w-[calc(100vw-2rem)] p-0"
            >
              <ModelList
                driver={driver}
                choices={choices}
                value={model}
                onPick={(next) => {
                  onChange({
                    model: next,
                    effort: effortAfterModel(
                      driver,
                      choices.catalog,
                      next,
                      fallbackModel,
                      effort,
                    ),
                  })
                  setOpen(false)
                }}
              />
            </PopoverContent>
          </Popover>
        </Field>
      ) : null}
      <Field
        label={t('create.effort')}
        description={
          choices.efforts === null ? t('picker.effortNeedsModel') : undefined
        }
      >
        <EffortSelect
          label={t('create.effort')}
          efforts={choices.efforts}
          value={effort}
          onChange={(next) => onChange({ model, effort: next })}
          defaultEffort={choices.start?.default_effort}
        />
      </Field>
    </div>
  )
}
