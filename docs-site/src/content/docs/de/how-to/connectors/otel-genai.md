---
title: "OpenTelemetry GenAI (jeder instrumentierte Agent)"
description: >-
  Speisen Sie die Access Map und FinOps aus JEDEM OTel-instrumentierten Agenten —
  LangChain, LangGraph, CrewAI, AutoGen, Google ADK und vergleichbare — über das
  herstellerneutrale gen_ai.*-Ingest-Profil: opt-in, fixiert auf semconv v1.41.1,
  und normalisiert die drei GenAI-Dialekte, die in realen Flotten koexistieren.
sidebar:
  order: 4
---

Claude Code ist die kanonische kooperative Quelle, aber es ist nicht der
einzige kooperative Agent, den Sie betreiben. Derselbe Connector, der die
Telemetrie von Claude Code empfängt (`kind: claude`), führt ein
**opt-in, herstellerneutrales OpenTelemetry-GenAI-Profil**: Richten Sie einen
beliebigen OTel-instrumentierten Agenten oder ein Framework auf denselben
OTLP-Receiver, und seine `gen_ai.*`-Telemetrie speist die Access-Map- und
Kosten-Pipeline — LangChain, LangGraph, CrewAI, AutoGen, Google ADK und alles
andere, das die semantischen GenAI-Konventionen über Spans oder Log-Events
emittiert.

## Warum es opt-in ist

Die OpenTelemetry-GenAI-Konventionen haben upstream den **Development-Status**
(vor-stabil), und drei Dialekte koexistieren in den Flotten von 2026
tatsächlich nebeneinander. Daher ist das Profil standardmäßig deaktiviert und
genau so gegated, wie es die OTel-SDKs gaten — über das Opt-in-Token:

```json
{
  "sources": [{
    "name": "agents-otel",
    "kind": "claude",
    "tenant": "<tenant-id>",
    "config": {
      "semconv_opt_in": "gen_ai_latest_experimental"
    }
  }]
}
```

`semconv_opt_in` spiegelt `OTEL_SEMCONV_STABILITY_OPT_IN`: eine
kommagetrennte Liste, die `gen_ai_latest_experimental` enthalten muss. Bei
**deaktiviertem** Profil speist ein `gen_ai.*`-Record weiterhin den
Session-Liveness-Watchdog, wird aber **nicht gemappt** — ehrliche Abwesenheit,
keine stille Ingestion.

## Was der Normalisierer akzeptiert

Das Profil ist auf **semconv v1.41.1** fixiert und normalisiert die drei
GenAI-Dialekte, die in realen Estates koexistieren, wobei jedes normalisierte
Event mit dem semconv-Pin des Dialekts gestempelt wird, damit die Provenienz
erhalten bleibt:

| Dialekt | Form |
|---|---|
| Legacy OpenLLMetry | indizierte `gen_ai.prompt.{i}.*`-Attribute |
| v1.36 und davor | die veralteten Per-Message-Events |
| v1.37+ | die `messages`-Generation |

Über die Message-Formen hinaus mappt es die **`mcp.*`-Konventionen (v1.39)**
und den **`invoke_agent`-Client/Internal-Split sowie `invoke_workflow`
(v1.41)** — sodass Framework-orchestrierte Agenten- und Workflow-Aufrufe als
strukturierte Topologie landen, nicht als Rauschen. Sowohl Span-basierte
Emission (so instrumentieren LangGraph, LangChain, CrewAI, AutoGen und Google
ADK) als auch Log-basierte Emission werden ingestiert.

Kostensamples werden anhand der W3C-Span-ID dedupliziert, sodass ein Agent,
dessen Telemetrie sowohl über den Span- als auch den Log-Pfad eintrifft,
niemals doppelt abgerechnet wird.

## Einen Agenten daran anbinden

Der Receiver ist der eigene OTLP-Endpunkt des Connectors (gRPC
`127.0.0.1:4317`, HTTP `127.0.0.1:4318` standardmäßig). Auf der Agentenseite
gilt die übliche OTel-SDK-Konfiguration — Exporter-Endpunkt auf den
Loopback-Receiver und das GenAI-Opt-in, falls Ihre Instrumentierung darauf
gated:

```bash
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
OTEL_SEMCONV_STABILITY_OPT_IN=gen_ai_latest_experimental
```

:::caution[Dieselbe Loopback-Regel wie bei Claude Code]
Das kooperative Ingest ist **nicht authentifiziert** und bindet standardmäßig
an Loopback. Alles, was den Socket erreichen kann, kann Telemetrie fälschen —
halten Sie es auf Loopback (`allow_public_bind` existiert und ist bewusst als
DANGEROUS gekennzeichnet). Off-Host-Agenten sind Aufgabe des
Kernel-Backstops, nicht eines öffentlichen OTLP-Ports.
:::

## Was Sie in der Konsole sehen

Instrumentierte Sessions erscheinen unter **Sessions** als Live-Aktivität,
dem emittierenden Agenten zugeordnet; ihre Modellaufrufe speisen
**Cost & FinOps**; MCP- und Tool-Spans tragen Kanten zur **Access Map** bei
wie jede kooperative Quelle:

<img class="light:sl-hidden" src="/console/sessions-dark.png" alt="Die Sessions-Ansicht zeigt Live-Aktivität von Agenten-Sessions aus kooperativer Telemetrie." />
<img class="dark:sl-hidden" src="/console/sessions-light.png" alt="Die Sessions-Ansicht zeigt Live-Aktivität von Agenten-Sessions aus kooperativer Telemetrie." />

## Ehrliche Grenzen

- **Vor-stabile Konventionen, fixiertes Ingest.** Das Profil ist auf
  v1.41.1 fixiert; wenn sich upstream weiterbewegt, bewegt sich der Pin durch
  ein bewusstes Update, nicht durch stillen Drift. Instrumentierung, die einen
  vierten Dialekt emittiert, wird nicht erraten.
- **Kooperativ heißt kooperativ.** Ein Agent, der nichts emittiert, ist auf
  diesem Pfad unsichtbar — dafür sind
  [eBPF/Tetragon](/de/how-to/connectors/ebpf-tetragon/) und
  store-native Auditierung da.
- **Framework-Eigenheiten bei der Span-Kind sind real.** Manche Frameworks
  emittieren Spans, deren Kind nicht den v1.41-Client/Internal-Regeln
  entspricht; der Normalisierer mappt, was er beweisen kann, und lässt den Rest
  ungemappt, statt ihn falsch zuzuordnen.

## Verwandt

- [Claude Code verbinden](/de/how-to/connect-claude-code/) — derselbe
  Receiver, Claude-spezifische Oberfläche.
- [Enterprise-OTel für Claude Code](/de/how-to/claude-code-enterprise-otel/) —
  flottenweite Telemetrie-Posture.
- [Events-Referenz](/de/reference/events/) — die normalisierten Observationen,
  die dies erzeugt.
