// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Everything the engine knows about how a tool is installed, in one sheet behind the
// tool's menu: the versions on this server, the reviewed install, Detect, the engine
// user's own login and the log of the last install. None of it is a list row.
import { useMutation } from '@tanstack/react-query'
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import {
  agentToolsApi,
  type ProviderSnapshot,
  type ToolDetection,
  type ToolJob,
} from './api'
import { ProviderDetails } from './providers-section'

const message = (err: unknown) =>
  err instanceof Error ? err.message : undefined

/** A tool Olivares installs without a release called "latest". */
const DEFAULT_VERSION: Record<string, string> = { grok: 'stable' }
export const defaultVersion = (driver: string) =>
  DEFAULT_VERSION[driver] ?? 'latest'

export function InstallDetails({
  driver,
  name,
  open,
  onOpenChange,
  installs,
  verification,
  disabledReason,
  onPreview,
  installedOutside,
  logins,
  job,
}: {
  driver: string
  name: string
  open: boolean
  onOpenChange: (open: boolean) => void
  installs: { version: string; state: string; reason?: string }[]
  verification?: string
  disabledReason?: string
  onPreview: (version: string) => void
  /** The tool is on this server and Olivares did not install it. */
  installedOutside: boolean
  /** The logins of this tool as the tool reports them: the engine user's own, and the one
   * this organization made through Olivares. */
  logins: ProviderSnapshot[]
  /** The last install, when it was this tool's. */
  job?: ToolJob
}) {
  const { t } = useTranslation('agentTools')
  const [version, setVersion] = useState(defaultVersion(driver))
  const reviewReasonId = useId()
  const reviewReason =
    disabledReason || (!version.trim() ? t('versionRequired') : undefined)
  const disabled = !!disabledReason
  const detect = useMutation({ mutationFn: () => agentToolsApi.detect(driver) })
  const probe = usePrivilegedMutation<string, ToolDetection>({
    mutationFn: async (path, authority) => {
      const result = await agentToolsApi.detect(
        driver,
        undefined,
        path,
        authority,
      )
      if (result.probe_error) {
        const failed = result.candidates.find((row) => row.probe_error)
        const selected = result.candidates.find((row) => row.path === path)
        throw new Error(
          failed?.probe_error ?? selected?.probe_skipped ?? result.probe_error,
        )
      }
      return result
    },
    stepUpAction: 'console',
    successMessage: t('probeComplete'),
  })
  const detection = probe.data ?? detect.data
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full max-w-lg overflow-y-auto">
        <SheetHeader>
          <SheetTitle>{t('details.title', { name })}</SheetTitle>
          <SheetDescription>{t('details.description')}</SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-4">
          <div className="flex flex-wrap items-baseline gap-2">
            {installs.length === 0 ? (
              <span className="text-caption text-text-2">
                {installedOutside ? t('installedOutside') : t('notInstalled')}
              </span>
            ) : (
              installs.map((row) => (
                <Badge
                  key={row.version}
                  variant={row.state === 'installed' ? 'neutral' : 'outline'}
                >
                  {row.version} · {t(`installedState.${row.state}`)}
                </Badge>
              ))
            )}
          </div>
          {installs.map(
            (row) =>
              row.reason && (
                <p key={row.version} className="text-caption text-text-2">
                  {row.reason}
                </p>
              ),
          )}
          {verification && (
            <p className="text-caption text-text-2">
              {t(`verification.${verification}`)}
            </p>
          )}
          <form
            onSubmit={(e) => {
              e.preventDefault()
              if (!version.trim()) return
              // The review opens on the page, behind this sheet: close it.
              onOpenChange(false)
              onPreview(version.trim())
            }}
            className="flex flex-col gap-2 sm:flex-row sm:items-end"
          >
            <div className="flex min-w-0 flex-col gap-1 sm:w-52">
              <label htmlFor={`version-${driver}`} className="text-body">
                {t('version', { name })}
              </label>
              <Input
                id={`version-${driver}`}
                value={version}
                onChange={(e) => setVersion(e.target.value)}
                disabled={disabled}
                maxLength={128}
              />
            </div>
            <Button
              type="submit"
              disabled={!!reviewReason}
              aria-describedby={reviewReason ? reviewReasonId : undefined}
              aria-label={t('reviewName', { name })}
            >
              {t('review')}
            </Button>
            <Button
              type="button"
              variant="secondary"
              onClick={() => {
                probe.reset()
                detect.mutate()
              }}
              disabled={detect.isPending || probe.isPending}
            >
              {t('detect', { name })}
            </Button>
          </form>
          {reviewReason && (
            <p
              id={reviewReasonId}
              role="status"
              className="text-caption text-text-2"
            >
              {reviewReason}
            </p>
          )}
          {detect.isPending && (
            <p role="status" className="text-body">
              {t('detecting')}
            </p>
          )}
          {detect.isError && (
            <p role="alert" className="text-body text-danger">
              {message(detect.error)}
            </p>
          )}
          {probe.isError && (
            <p role="alert" className="text-body text-danger">
              {message(probe.error)}
            </p>
          )}
          {detection && (
            <div className="text-body">
              {detection.probe_error && (
                <p role="alert" className="text-danger">
                  {detection.probe_error}
                </p>
              )}
              {detection.candidates.length === 0
                ? !detection.probe_error && <p>{t('notDetected')}</p>
                : detection.candidates.map((row) => (
                    <div
                      key={row.path}
                      className="flex flex-col gap-2 border-t border-line py-2"
                    >
                      <p className="break-all font-mono text-mono-s">
                        {row.path} · {row.version ?? t('versionUnknown')}
                      </p>
                      {row.probe_error && <p role="alert">{row.probe_error}</p>}
                      {row.probe_skipped && (
                        <p className="text-caption text-text-2">
                          {row.probe_skipped}
                        </p>
                      )}
                      {!row.version &&
                        row.executable &&
                        row.match === 'unregistered-observed' && (
                          <>
                            <p className="text-caption text-text-2">
                              {t('probeHint')}
                            </p>
                            <Button
                              variant="secondary"
                              aria-label={t('probeName', { path: row.path })}
                              disabled={disabled || probe.isPending}
                              onClick={() => probe.mutate(row.path)}
                            >
                              {t('probe')}
                            </Button>
                          </>
                        )}
                    </div>
                  ))}
            </div>
          )}
          {logins.length > 0 ? (
            <section
              aria-label={t('details.serverLogin')}
              className="flex flex-col gap-2 border-t border-line pt-4"
            >
              <h3 className="text-body font-medium text-text">
                {t('details.serverLogin')}
              </h3>
              <p className="text-caption text-text-2">
                {t('details.serverLoginHint')}
              </p>
              {logins.map((snap) => (
                <ProviderDetails key={snap.instance} snap={snap} />
              ))}
            </section>
          ) : null}
          {job ? (
            <section
              aria-label={t('job')}
              className="flex flex-col gap-2 border-t border-line pt-4"
            >
              <h3 className="text-body font-medium text-text">{t('job')}</h3>
              <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-all rounded bg-muted p-3 font-mono text-mono-s">
                {job.progress}
              </pre>
            </section>
          ) : null}
        </div>
      </SheetContent>
    </Sheet>
  )
}
