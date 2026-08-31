// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Plus, ScrollText, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { knowledgeApi, knowledgeKeys } from './api'
import './i18n'
import {
  CLASSIFICATIONS,
  type Classification,
  type IngestInput,
  type IngestResponse,
  type KbDTO,
} from './types'

export interface IngestDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  kb: KbDTO
}

export function IngestDialog({ open, onOpenChange, kb }: IngestDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-2xl overflow-y-auto">
        {open && <IngestForm kb={kb} onClose={() => onOpenChange(false)} />}
      </DialogContent>
    </Dialog>
  )
}

interface DraftDoc {
  _k: string
  source_doc_id: string
  title: string
  body: string
  content_type: string
  classification: Classification
}

let docKeySeq = 0
function newDoc(): DraftDoc {
  docKeySeq += 1
  return {
    _k: `d${docKeySeq}`,
    source_doc_id: '',
    title: '',
    body: '',
    content_type: 'text/plain',
    classification: 'internal',
  }
}

function IngestForm({ kb, onClose }: { kb: KbDTO; onClose: () => void }) {
  const { t } = useTranslation(['knowledge', 'common'])
  const { activeTenant } = useAuth()

  const [mode, setMode] = useState<'connector' | 'inline'>('connector')
  const [source, setSource] = useState('')
  const [docs, setDocs] = useState<DraftDoc[]>(() => [newDoc()])

  const connectorValid = source.trim().length > 0
  const inlineValid = docs.some((d) => d.source_doc_id.trim() && d.body.trim())
  const valid = mode === 'connector' ? connectorValid : inlineValid

  const mutation = usePrivilegedMutation<IngestInput, IngestResponse>({
    mutationFn: (input) => knowledgeApi.ingest(kb.id, input),
    invalidateKeys: () => [
      knowledgeKeys.kb(activeTenant, kb.id),
      knowledgeKeys.kbs(activeTenant),
      knowledgeKeys.documents(activeTenant, kb.id),
    ],
    successMessage: (data) =>
      t('ingest.doneSummary', {
        documents: data.documents,
        chunks: data.chunks,
        redactions: data.redactions_total,
      }),
    onDone: onClose,
  })

  function submit() {
    if (!valid) return
    const input: IngestInput =
      mode === 'connector'
        ? { source: source.trim() }
        : {
            documents: docs
              .filter((d) => d.source_doc_id.trim() && d.body.trim())
              .map((d) => ({
                source_kind: 'inline',
                source_doc_id: d.source_doc_id.trim(),
                body: d.body,
                ...(d.title.trim() ? { title: d.title.trim() } : {}),
                ...(d.content_type.trim()
                  ? { content_type: d.content_type.trim() }
                  : {}),
                classification: d.classification,
              })),
          }
    mutation.mutate(input)
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('ingest.title')}</DialogTitle>
        <DialogDescription>
          {t('ingest.body', { name: kb.name })}
        </DialogDescription>
      </DialogHeader>

      <Tabs
        value={mode}
        onValueChange={(v) => setMode(v as 'connector' | 'inline')}
      >
        <TabsList>
          <TabsTrigger value="connector">
            {t('ingest.modeConnector')}
          </TabsTrigger>
          <TabsTrigger value="inline">{t('ingest.modeInline')}</TabsTrigger>
        </TabsList>
      </Tabs>

      <div className="flex flex-col gap-4">
        {mode === 'connector' ? (
          <Field
            label={t('ingest.source')}
            htmlFor="ingest-source"
            description={t('ingest.sourceHint')}
            required
          >
            <Input
              id="ingest-source"
              value={source}
              onChange={(e) => setSource(e.target.value)}
              mono
            />
          </Field>
        ) : (
          <div className="flex flex-col gap-3">
            <div className="flex items-center justify-between">
              <Label>{t('ingest.modeInline')}</Label>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => setDocs((d) => [...d, newDoc()])}
              >
                <Plus />
                {t('ingest.addDoc')}
              </Button>
            </div>
            {docs.length === 0 ? (
              <p className="rounded-md border border-dashed border-border px-3 py-2 text-xs text-muted-foreground">
                {t('ingest.noDocs')}
              </p>
            ) : (
              docs.map((d, i) => (
                <div
                  key={d._k}
                  className="flex flex-col gap-2 rounded-md border border-border bg-muted/40 p-3"
                >
                  <div className="grid gap-2 sm:grid-cols-2">
                    <Input
                      aria-label={t('ingest.sourceDocId')}
                      placeholder={t('ingest.sourceDocId')}
                      value={d.source_doc_id}
                      onChange={(e) =>
                        setDocs((arr) =>
                          arr.map((x, j) =>
                            j === i
                              ? { ...x, source_doc_id: e.target.value }
                              : x,
                          ),
                        )
                      }
                      mono
                    />
                    <Input
                      aria-label={t('ingest.docTitleField')}
                      placeholder={t('ingest.docTitleField')}
                      value={d.title}
                      onChange={(e) =>
                        setDocs((arr) =>
                          arr.map((x, j) =>
                            j === i ? { ...x, title: e.target.value } : x,
                          ),
                        )
                      }
                    />
                  </div>
                  <Textarea
                    aria-label={t('ingest.body_')}
                    placeholder={t('ingest.bodyHint')}
                    value={d.body}
                    rows={3}
                    onChange={(e) =>
                      setDocs((arr) =>
                        arr.map((x, j) =>
                          j === i ? { ...x, body: e.target.value } : x,
                        ),
                      )
                    }
                  />
                  <div className="flex items-center justify-between gap-2">
                    <Select
                      value={d.classification}
                      onValueChange={(v) =>
                        setDocs((arr) =>
                          arr.map((x, j) =>
                            j === i
                              ? { ...x, classification: v as Classification }
                              : x,
                          ),
                        )
                      }
                    >
                      <SelectTrigger
                        aria-label={t('common.classification')}
                        className="max-w-[12rem]"
                      >
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {CLASSIFICATIONS.map((c) => (
                          <SelectItem key={c} value={c}>
                            {t(`common.classifications.${c}`)}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      aria-label={t('ingest.removeDoc')}
                      onClick={() =>
                        setDocs((arr) => arr.filter((_, j) => j !== i))
                      }
                    >
                      <Trash2 />
                    </Button>
                  </div>
                </div>
              ))
            )}
          </div>
        )}
      </div>

      <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <ScrollText className="size-3.5 shrink-0" aria-hidden />
        {t('common:privileged.auditedNotice')}
      </p>

      <DialogFooter>
        <Button
          variant="secondary"
          onClick={onClose}
          disabled={mutation.isPending}
        >
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant="primary"
          onClick={submit}
          disabled={!valid || mutation.isPending}
        >
          {mutation.isPending && <Spinner size="sm" aria-hidden />}
          {t('ingest.confirm')}
        </Button>
      </DialogFooter>
    </>
  )
}
