// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The models of a profile, as the person wants them offered: the tool's own list (read, never
// changed), a switch per model, its place in the list, and the models the person adds that the
// tool does not list. It is the person's preference (model-preferences.ts): a hidden model
// leaves the pickers, while the tool's default and a model typed into a picker still work.
import { ArrowDown, ArrowUp, X } from 'lucide-react'
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import {
  isToolDefault,
  modelLabel,
  useModelChoices,
} from '@/features/agentops/model-choices'
import {
  applyPreferences,
  isModelId,
  moveModel,
  useModelPreferences,
} from '@/features/agentops/model-preferences'
import type { ProviderAccountDTO } from '@/features/agentops/types'
import './i18n'

export function ProfileModels({ account }: { account: ProviderAccountDTO }) {
  const { t } = useTranslation('agentTools')
  const heading = useId()
  const choices = useModelChoices({
    profileRef: account.account_ref,
    driver: account.driver,
    model: '',
    enabled: true,
  })
  const { prefs, save } = useModelPreferences(account.account_ref)
  const [query, setQuery] = useState('')
  const [typed, setTyped] = useState('')
  const [problem, setProblem] = useState(false)
  // Nothing to say until the tool has answered; a tool that cannot list its models still
  // lets the person add the ones they use.
  if (choices.status === 'none' || choices.status === 'loading') return null

  const base = (choices.catalog?.models ?? []).filter((m) => !isToolDefault(m))
  const all = applyPreferences(base, prefs, true)
  const hidden = new Set(prefs.hidden)
  const ownOnly = new Set(
    prefs.custom.filter((id) => !base.some((m) => m.id === id)),
  )
  const needle = query.trim().toLowerCase()
  const shown = needle
    ? all.filter(
        (m) =>
          m.id.toLowerCase().includes(needle) ||
          modelLabel(m).toLowerCase().includes(needle),
      )
    : all
  const anyOn = all.some((m) => !hidden.has(m.id))
  const ids = all.map((m) => m.id)
  const change = (next: Partial<typeof prefs>) => save?.({ ...prefs, ...next })

  const add = () => {
    const id = typed.trim()
    if (!isModelId(id)) {
      setProblem(true)
      return
    }
    setProblem(false)
    if (!ids.includes(id)) change({ custom: [...prefs.custom, id] })
    setTyped('')
  }

  return (
    <section aria-labelledby={heading} className="flex flex-col gap-3">
      <h3 id={heading} className="text-body font-medium text-text">
        {t('models.title')}
      </h3>
      <p className="text-caption text-text-2">{t('models.note')}</p>
      <div className="flex flex-wrap items-center gap-2">
        <Input
          className="min-w-0 flex-1"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          aria-label={t('models.filter')}
          placeholder={t('models.filter')}
          autoComplete="off"
          spellCheck={false}
        />
        <Button
          variant="ghost"
          size="sm"
          disabled={!save || all.length === 0}
          onClick={() => change({ hidden: anyOn ? ids : [] })}
        >
          {t(anyOn ? 'models.disableAll' : 'models.enableAll')}
        </Button>
        <span className="text-caption text-text-2">
          {t('providers.models', { count: all.length })}
        </span>
      </div>
      {all.length === 0 ? (
        <p className="text-caption text-text-2">{t('models.empty')}</p>
      ) : (
        <ul className="flex flex-col divide-y divide-line">
          {shown.map((m) => {
            const name = modelLabel(m)
            return (
              <li key={m.id} className="flex items-center gap-2 py-1.5">
                <span className="flex min-w-0 flex-1 flex-col">
                  <span className="truncate text-body text-text">{name}</span>
                  <span className="truncate font-mono text-caption text-text-2">
                    {m.id}
                  </span>
                </span>
                {m.supports_fast_mode ? (
                  <Badge variant="neutral">{t('models.fast')}</Badge>
                ) : null}
                <Button
                  variant="icon"
                  disabled={!save || ids[0] === m.id}
                  aria-label={t('models.up', { name })}
                  onClick={() => change({ order: moveModel(ids, m.id, -1) })}
                >
                  <ArrowUp aria-hidden className="size-4" />
                </Button>
                <Button
                  variant="icon"
                  disabled={!save || ids.at(-1) === m.id}
                  aria-label={t('models.down', { name })}
                  onClick={() => change({ order: moveModel(ids, m.id, 1) })}
                >
                  <ArrowDown aria-hidden className="size-4" />
                </Button>
                {ownOnly.has(m.id) ? (
                  <Button
                    variant="icon"
                    disabled={!save}
                    aria-label={t('models.removeCustom', { name })}
                    onClick={() =>
                      change({
                        custom: prefs.custom.filter((id) => id !== m.id),
                        hidden: prefs.hidden.filter((id) => id !== m.id),
                        order: prefs.order.filter((id) => id !== m.id),
                      })
                    }
                  >
                    <X aria-hidden className="size-4" />
                  </Button>
                ) : null}
                <Switch
                  checked={!hidden.has(m.id)}
                  disabled={!save}
                  onCheckedChange={(on) =>
                    change({
                      hidden: on
                        ? prefs.hidden.filter((id) => id !== m.id)
                        : [...prefs.hidden, m.id],
                    })
                  }
                  aria-label={t('models.use', { name })}
                />
              </li>
            )
          })}
        </ul>
      )}
      <form
        className="flex flex-col gap-1"
        onSubmit={(event) => {
          event.preventDefault()
          add()
        }}
      >
        <div className="flex items-center gap-2">
          <Input
            className="min-w-0 flex-1"
            value={typed}
            onChange={(event) => {
              setTyped(event.target.value)
              setProblem(false)
            }}
            aria-label={t('models.addCustom')}
            aria-invalid={problem}
            placeholder={t('models.customPlaceholder')}
            autoComplete="off"
            spellCheck={false}
          />
          <Button type="submit" disabled={!save || typed.trim() === ''}>
            {t('models.add')}
          </Button>
        </div>
        {problem ? (
          <p role="alert" className="text-caption text-danger">
            {t('models.customInvalid')}
          </p>
        ) : null}
      </form>
    </section>
  )
}
