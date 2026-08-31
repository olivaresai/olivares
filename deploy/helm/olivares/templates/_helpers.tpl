{{/*
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
*/}}

{{/* Base name, overridable. */}}
{{- define "olivares.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Fully qualified app name. */}}
{{- define "olivares.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "olivares.core.fullname" -}}
{{- printf "%s-core" (include "olivares.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "olivares.collector.fullname" -}}
{{- printf "%s-collector" (include "olivares.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "olivares.postgres.fullname" -}}
{{- printf "%s-postgres" (include "olivares.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Chart label (name-version). */}}
{{- define "olivares.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Common labels merged onto every object. */}}
{{- define "olivares.labels" -}}
helm.sh/chart: {{ include "olivares.chart" . }}
{{ include "olivares.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: olivares
{{- with .Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{- define "olivares.selectorLabels" -}}
app.kubernetes.io/name: {{ include "olivares.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Image reference. Pin by DIGEST when image.digest is set (the supply-chain /
air-gap posture, SCP-06), otherwise fall back to the tag (defaulting to the
chart appVersion). Never produces a bare ":latest".
*/}}
{{- define "olivares.image" -}}
{{- $repo := .Values.image.repository -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" $repo .Values.image.digest -}}
{{- else -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end -}}
{{- end -}}

{{/* Generic image ref for a {repository,tag,digest} block (postgres etc). */}}
{{- define "olivares.imageRef" -}}
{{- if .digest -}}
{{- printf "%s@%s" .repository .digest -}}
{{- else -}}
{{- printf "%s:%s" .repository .tag -}}
{{- end -}}
{{- end -}}

{{- define "olivares.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "olivares.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* The in-cluster gRPC address of the core ingest endpoint (for collectors). */}}
{{- define "olivares.coreAddr" -}}
{{- if .Values.collectors.coreAddr -}}
{{- .Values.collectors.coreAddr -}}
{{- else -}}
{{- printf "%s:%d" (include "olivares.core.fullname" .) (int .Values.service.grpcPort) -}}
{{- end -}}
{{- end -}}

{{/*
Validation guardrails — fail the render early with a clear message rather than
producing a subtly-broken release. (docs/SECURITY-HARDENING.md: fail closed, never silently.)
*/}}
{{- define "olivares.validate" -}}
{{- $ha := and (eq .Values.core.engine "postgres") (gt (int .Values.core.replicaCount) 1) -}}
{{- if and .Values.backup.enabled $ha (not .Values.core.auditSigningKeySecret) -}}
{{- fail "backup.enabled in HA requires core.auditSigningKeySecret: the backup pod has no core data PVC, so the externally custodied audit key must be mounted from the shared Secret." -}}
{{- end -}}
{{- if gt (int .Values.core.replicaCount) 1 -}}
{{- if not (eq .Values.core.engine "postgres") -}}
{{- fail "core.replicaCount > 1 requires core.engine=postgres: the sqlite store is single-node (the file is local to one pod). For HA, use Postgres as the shared store." -}}
{{- end -}}
{{- if not .Values.core.auditSigningKeySecret -}}
{{- fail "core.replicaCount > 1 requires core.auditSigningKeySecret: in active-passive HA every replica must sign the audit ledger with the SAME key (mounted from a shared Secret) so the hash-chain does not fork when a standby takes over. Provision a Secret with key `audit-signing.key` (base64 Ed25519) and set core.auditSigningKeySecret." -}}
{{- end -}}
{{- end -}}
{{- if eq .Values.core.engine "postgres" -}}
{{- if not .Values.postgres.dsnSecret -}}
{{- fail "core.engine=postgres requires postgres.dsnSecret (a Secret holding the olivares_app DSN). This chart does not bundle Postgres — bring your own." -}}
{{- end -}}
{{- if and .Values.backup.enabled (not .Values.postgres.adminDsnKey) -}}
{{- fail "backup.enabled with core.engine=postgres requires postgres.adminDsnKey: pg_dump must use a dedicated NOSUPERUSER BYPASSRLS role with SELECT on all tables. Without it pg_dump keeps row_security=off and ABORTS as the app role under FORCE RLS, so every scheduled dump fails and there is no backup. Separately, building a manifest from an external snapshot without the admin DSN enumerates tenants RLS-scoped and can omit some." -}}
{{- end -}}
{{- end -}}
{{- if and .Values.collectors.enabled (not .Values.collectors.ingestTokenSecret) -}}
{{- fail "collectors.enabled=true requires collectors.ingestTokenSecret (a Secret holding an ingest:write bearer token)." -}}
{{- end -}}
{{- if and (not (eq .Values.core.engine "sqlite")) (not (eq .Values.core.engine "postgres")) -}}
{{- fail "core.engine must be \"sqlite\" or \"postgres\"." -}}
{{- end -}}
{{- end -}}
