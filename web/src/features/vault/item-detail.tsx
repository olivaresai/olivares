// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// One vault item. It opens locked: revealing is one click, one audit entry, and the values
// go back into hiding after a minute. Each value copies to the clipboard and is taken off
// it again; the authenticator code refreshes itself while the dialog is open.
import { useMutation } from '@tanstack/react-query'
import { Copy, Eye, EyeOff, LockKeyhole, Pencil } from 'lucide-react'
import { useEffect, useId, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Spinner } from '@/components/ui/spinner'
import { toast } from '@/components/ui/toaster'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import { vaultApi } from './api'
import { CLIPBOARD_CLEAR_MS, copySecret } from './clipboard'
import { ItemForm } from './item-form'
import { parseItem, safeLink, type VaultItem, type VaultRow } from './model'
import './i18n'

/** Revealed values go back into hiding after this long. */
const REVEAL_MS = 60_000

export interface ItemDetailProps {
  row: VaultRow
  userId: string
  taken: ReadonlySet<string>
  onClose: () => void
}

export function ItemDetail({ row, userId, taken, onClose }: ItemDetailProps) {
  const { t } = useTranslation(['vault', 'common'])
  const boundary = useAuthBoundary()
  const report = useFailedActionReporter('vault')
  const [item, setItem] = useState<VaultItem | null>(null)
  const [editing, setEditing] = useState(false)
  const [legacy, setLegacy] = useState(false)

  const reveal = useMutation({
    gcTime: 0, // the answer is the item: keep it only in this dialog
    mutationFn: async (): Promise<{ item: VaultItem; legacy: boolean }> => {
      const { value } = await vaultApi.reveal(row.name, {
        tenant: boundary.tenant ?? undefined,
      })
      const parsed = parseItem(value)
      // A value that is not an item document (stored before items had a shape) is shown
      // as text and cannot be edited: saving it would rewrite it as a note.
      return {
        item: parsed ?? ({ type: 'note', note: value } as VaultItem),
        legacy: parsed === null,
      }
    },
    onSuccess: (res) => {
      setLegacy(res.legacy)
      setItem(res.item)
    },
    onError: (err) => report(err, () => reveal.mutate()),
  })

  // When the values hide again on their own and focus went with them, it goes to Reveal.
  const revealButton = useRef<HTMLButtonElement>(null)
  const hidAgain = useRef(false)
  const lockedNote = useId()
  const { reset: forgetReveal } = reveal
  useEffect(() => {
    if (!item || editing) return
    const id = setTimeout(() => {
      hidAgain.current = true
      setItem(null)
      forgetReveal() // the mutation holds the item too
    }, REVEAL_MS)
    return () => clearTimeout(id)
  }, [item, editing, forgetReveal])
  useEffect(() => {
    if (item || !hidAgain.current) return
    hidAgain.current = false
    const active = document.activeElement
    if (!active || active === document.body) revealButton.current?.focus()
  }, [item])

  if (editing && item) {
    return (
      <ItemForm
        userId={userId}
        existing={{ row, item }}
        taken={taken}
        onCancel={() => setEditing(false)}
        onSaved={onClose}
      />
    )
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle className="font-mono">{row.label}</DialogTitle>
        <DialogDescription>
          {t(`vault:kind.${row.kind}`)}
          {row.description ? ` · ${row.description}` : ''}
        </DialogDescription>
      </DialogHeader>
      {item ? (
        <ItemFields item={item} name={row.name} />
      ) : (
        <div className="flex flex-col items-start gap-3">
          <p
            id={lockedNote}
            className="flex items-center gap-2 text-body text-muted-foreground"
          >
            <LockKeyhole className="size-4 shrink-0" aria-hidden />
            {t('vault:detail.locked')}
          </p>
          <Button
            ref={revealButton}
            variant="primary"
            aria-describedby={lockedNote}
            onClick={() => reveal.mutate()}
            disabled={reveal.isPending}
          >
            {reveal.isPending ? <Spinner size="sm" aria-hidden /> : <Eye />}
            {t('vault:detail.reveal')}
          </Button>
        </div>
      )}
      <DialogFooter>
        {item && !legacy && (
          <Button variant="secondary" onClick={() => setEditing(true)}>
            <Pencil />
            {t('vault:detail.edit')}
          </Button>
        )}
        <Button variant="secondary" onClick={onClose}>
          {t('common:actions.close')}
        </Button>
      </DialogFooter>
    </>
  )
}

type Translate = (key: string, options?: Record<string, unknown>) => string

async function copyField(value: string, field: string, t: Translate) {
  if (await copySecret(value))
    toast.success(
      t('vault:copied', { field, seconds: CLIPBOARD_CLEAR_MS / 1000 }),
    )
  else toast.error(t('vault:copyBlocked'))
}

function ItemFields({ item, name }: { item: VaultItem; name: string }) {
  const { t } = useTranslation('vault')
  const link = safeLink(item.url)
  // The Reveal button that was focused is gone: put focus on what it revealed.
  const fields = useRef<HTMLDListElement>(null)
  useEffect(() => fields.current?.focus(), [])
  return (
    <dl ref={fields} tabIndex={-1} className="flex flex-col gap-3 outline-none">
      {item.username && (
        <Row label={t('form.username')} value={item.username} />
      )}
      {item.password && (
        <Row label={t('form.password')} value={item.password} secret />
      )}
      {item.url && (
        <Row label={t('form.url')} value={item.url} href={link ?? undefined} />
      )}
      {item.totp && <CodeRow name={name} />}
      {item.type === 'totp' && item.totp && (
        <Row label={t('detail.totpKey')} value={item.totp} secret />
      )}
      {item.note && (
        <Row
          label={t('form.note')}
          value={item.note}
          multiline
          secret={item.type === 'note'}
        />
      )}
    </dl>
  )
}

function Row({
  label,
  value,
  secret = false,
  multiline = false,
  href,
}: {
  label: string
  value: string
  secret?: boolean
  multiline?: boolean
  href?: string
}) {
  const { t } = useTranslation('vault')
  const [shown, setShown] = useState(!secret)
  const text = shown ? (
    value
  ) : (
    <>
      <span aria-hidden>••••••••••</span>
      <span className="sr-only">{t('detail.hidden')}</span>
    </>
  )
  return (
    <div className="flex items-start justify-between gap-3">
      <div className="min-w-0">
        <dt className="text-caption text-muted-foreground">{label}</dt>
        <dd
          className={
            multiline
              ? 'break-words whitespace-pre-wrap text-body text-foreground'
              : 'font-mono text-body break-all text-foreground'
          }
        >
          {href && shown ? (
            <a
              href={href}
              target="_blank"
              rel="noopener noreferrer"
              className="underline"
            >
              {text}
            </a>
          ) : (
            text
          )}
        </dd>
      </div>
      <div className="flex shrink-0 gap-1">
        {secret && (
          <Button
            variant="ghost"
            size="icon"
            aria-label={t(shown ? 'detail.hideField' : 'detail.showField', {
              field: label,
            })}
            onClick={() => setShown((v) => !v)}
          >
            {shown ? <EyeOff /> : <Eye />}
          </Button>
        )}
        <Button
          variant="ghost"
          size="icon"
          aria-label={t('detail.copyField', { field: label })}
          onClick={() => void copyField(value, label, t as Translate)}
        >
          <Copy />
        </Button>
      </div>
    </div>
  )
}

/** The authenticator code: fetched when asked, refreshed when its window ends. */
function CodeRow({ name }: { name: string }) {
  const { t } = useTranslation('vault')
  const boundary = useAuthBoundary()
  const report = useFailedActionReporter('vault')
  const [current, setCurrent] = useState<{
    code: string
    expiresAt: number
  } | null>(null)
  const [now, setNow] = useState(() => Date.now())

  const fetchCode = useMutation({
    gcTime: 0,
    mutationFn: () =>
      vaultApi.code(name, { tenant: boundary.tenant ?? undefined }),
    onSuccess: (c) => {
      const at = Date.now()
      setNow(at)
      // At least a second, so an engine answer of 0 cannot make the refresh spin.
      setCurrent({
        code: c.code,
        expiresAt: at + Math.max(1, c.seconds_left) * 1000,
      })
    },
    onError: (err) => {
      setCurrent(null) // stop refreshing: a refused code is not retried every second
      report(err, () => fetchCode.mutate())
    },
  })

  useEffect(() => {
    if (!current) return
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [current])

  const left = current
    ? Math.max(0, Math.ceil((current.expiresAt - now) / 1000))
    : 0
  const { mutate: refresh, isPending } = fetchCode
  useEffect(() => {
    if (current && left === 0 && !isPending) refresh()
  }, [current, left, isPending, refresh])

  // Show code replaces its own button: focus goes to the code it shows, once (a refresh
  // every period must not take it back).
  const codeRef = useRef<HTMLElement>(null)
  const shownBefore = useRef(false)
  const showing = current !== null
  useEffect(() => {
    if (showing && !shownBefore.current) codeRef.current?.focus()
    shownBefore.current = showing
  }, [showing])

  const label = t('detail.code')
  return (
    <div className="flex items-start justify-between gap-3">
      <div className="min-w-0">
        <dt className="text-caption text-muted-foreground">{label}</dt>
        <dd
          ref={codeRef}
          tabIndex={-1}
          className="font-mono text-heading tracking-widest text-foreground outline-none"
        >
          {current ? (
            <>
              {current.code.replace(/(\d{3})(?=\d)/, '$1 ')}
              <span className="ml-3 font-sans text-caption tracking-normal text-muted-foreground">
                {t('detail.codeLeft', { n: left })}
              </span>
            </>
          ) : (
            <span className="font-sans text-body tracking-normal text-muted-foreground">
              {t('detail.codeHidden')}
            </span>
          )}
        </dd>
      </div>
      <div className="flex shrink-0 gap-1">
        {current ? (
          <Button
            variant="ghost"
            size="icon"
            aria-label={t('detail.copyField', { field: label })}
            onClick={() => void copyField(current.code, label, t as Translate)}
          >
            <Copy />
          </Button>
        ) : (
          <Button
            variant="secondary"
            size="sm"
            disabled={isPending}
            onClick={() => refresh()}
          >
            {isPending && <Spinner size="sm" aria-hidden />}
            {t('detail.showCode')}
          </Button>
        )}
      </div>
    </div>
  )
}
