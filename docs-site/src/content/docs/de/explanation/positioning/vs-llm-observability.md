---
title: Olivares AI vs. LLM-Gateways & Observability (LiteLLM, Langfuse)
description: >-
  Ein ehrlicher Vergleich mit dem beliebten selbst gehosteten LLM-Ops-Stack — einem
  Gateway (LiteLLM) plus einer Observability-Plattform (Langfuse). Was jedes gut kann, wo
  Olivares AI anders ist und warum es „und“ heißt, nicht „oder“.
sidebar:
  order: 3
---

Ein verbreiteter, sinnvoller selbst gehosteter Stack kombiniert ein **LLM-Gateway** (zum
Beispiel **LiteLLM**) mit einer **LLM-Observability-Plattform** (zum Beispiel **Langfuse**).
Wenn Sie einen solchen haben, fragen Sie sich vielleicht zu Recht, ob Sie überhaupt eine
Control Plane brauchen. Diese Seite beantwortet das ehrlich — einschließlich der Fälle,
in denen die Antwort **nein** lautet.

:::tip[Die Kurzfassung]
Bei LiteLLM und Langfuse geht es um **die Modellaufrufe, die Ihre Anwendung tätigt**:
sie routen, tracen, Prompts verwalten, Kosten pro Aufruf erfassen. Bei Olivares AI geht es
um **jeden Agenten in Ihrem Estate und alles, was er liest oder schreibt** — Datenbanken,
Object-Stores, MCP-Server, Tools, Dateien — und ob das dem entspricht, was die Policy
erlaubt. Andere Flughöhe. Sie ergänzen sich; wir **ingestieren dasselbe
OpenTelemetry-GenAI-Signal**, das sie emittieren.
:::

## Was dieser Stack gut kann (dafür nutzen)

- **LiteLLM** — ein vereinheitlichtes, OpenAI-kompatibles Gateway vor vielen Providern:
  Routing, Fallbacks, Retries, virtuelle Keys, Budgets und Rate Limits pro Key sowie
  Kostenrechnung für die Modellaufrufe, die hindurchlaufen.
- **Langfuse** — LLM-Engineering und Observability: Request/Response-**Traces**,
  Prompt-Management und -Versionierung, Evaluationen, Datasets und eine
  entwicklerorientierte UI zum Debuggen von Chains.

Wenn Ihr Problem lautet *„instrumentiere die LLM-Aufrufe meiner App, debugge Prompts und
verwalte den Modellzugriff über einen Endpunkt“*, ist dieser Stack hervorragend und selbst
hostbar. Sie brauchen dafür keine Control Plane, und wir tun nicht so, als wäre es anders.

## Wo Olivares AI strukturell anders ist

| Dimension | LLM-Gateway + Observability | Olivares AI |
|---|---|---|
| **Betrachtungseinheit** | Ein Modellaufruf (Prompt → Completion) | Ein Agent und jede Ressource, die er liest/schreibt — DBs, Object-Stores, MCP, Tools, Dateien |
| **Beobachtungspunkt** | **Im Request-Pfad** (Proxy/SDK); sieht, was die App sendet | **Out of Band, read-first**; beobachtet Telemetrie, natives Audit und einen Kernel-Backstop — nie im Datenpfad |
| **Source of Truth** | Was die App/der Proxy **berichtet** | Selbstberichtete Telemetrie **abgeglichen gegen das eigene Ledger des Systems** — pgAudit (read vs. write), CloudTrail (Object-Zugriff), eBPF-Backstop |
| **Die Schlüsselfrage** | „Was hat dieser Prompt getan, und was hat er gekostet?“ | „Nutzt dieser Agent Zugriff, den **niemand gewährt** hat?“ — [Permitted-vs-Observed-Drift](/de/explanation/#die-access-map-read-first-minimal-data-permitted-vs-observed) |
| **Enforcement** | Gateway kann **Modellaufrufe** gaten (Keys, Budgets) | Deny-closed Gates auf **Aktionen und Ressourcenzugriff**: Freigaben, der [Claude-Code-Hooks-PEP](/de/how-to/connectors/claude-code-hooks-pep/), MCP-Tool-Gating, Kill Switches |
| **Audit-Artefakt** | Traces / Logs zum Debuggen | Append-only, hash-chained, **Ed25519-signiertes** Ledger, **off-box verifizierbar**, exportierbar als **OSCAL**-Evidenzpakete |
| **Deployment-Posture** | Selbst hostbar | Self-hosted **oder Air-Gapped**; Data Plane verlässt nie Ihre Grenze; **AGPL**, source-available |

Der tragende Unterschied ist die **Ground Truth**. Ein Observability-Trace sagt Ihnen,
was die Anwendung zu tun *behauptete*. Er kann Ihnen nicht sagen, dass ein Agent eine
Tabelle erreicht hat, die der Trace nie erwähnte. Olivares AI gleicht das kooperative
Signal gegen die Data Plane ab, sodass „was der Agent berührt hat“ ein bestätigter Fakt ist,
kein Selbstbericht. Siehe [Analyst-Vokabular](/de/explanation/positioning/analyst-vocabulary/),
warum das die erste unserer drei Bahnen ist.

## Es heißt „und“, nicht „oder“ — wir ingestieren Ihre Telemetrie

Olivares AI ist **kein** Ersatz für Ihr Gateway oder Ihr Tracing-Tool und will nicht in
dem Request-Pfad sitzen, den diese belegen. Es **konsumiert dasselbe Signal**: Die Control
Plane ingestiert **OpenTelemetry-GenAI**-Spans gemäß Semantic Convention — dieselbe
GenAI-Telemetrie, die diese Tools emittieren und konsumieren. Eine gesunde Anordnung ist also:

- Behalten Sie **LiteLLM** als Ihr Modell-Gateway und **Langfuse** für entwicklerorientiertes
  Tracing und Prompt-Arbeit.
- Richten Sie den **OTel-GenAI**-Stream als eine bestätigende Quelle auf Olivares AI und
  lassen Sie die Access Map, die Drift-Erkennung und das Ledger die estate-weite
  Governance-Schicht obendrauf erledigen.

→ [OpenTelemetry GenAI ingestieren](/de/how-to/connectors/otel-genai/) ·
[Enterprise-OTel für Claude Code](/de/how-to/claude-code-enterprise-otel/)

## Wann Sie *nicht* zu Olivares AI greifen sollten

Ehrlichkeit schneidet in beide Richtungen. Sie brauchen diese Control Plane wahrscheinlich
**nicht**, wenn:

- Ihr einziges Ziel **Tracing und Debugging von LLM-Aufrufen** in einer oder zwei Apps mit
  einem Prompt-Playground ist — Langfuse allein passt besser.
- Sie nur ein **Multi-Provider-Gateway** mit Budgets und Failover brauchen — das ist
  LiteLLMs Aufgabe, und wir integrieren uns mit diesem Muster, statt es neu zu implementieren.
- Sie **kein zu governendes Estate** haben: ein einziger Service, ein einziges Modell, keine
  Agenten, die Datenbanken/Object-Stores/MCP berühren, und keine Audit- oder regulatorische
  Verpflichtung.

Olivares AI verdient seinen Platz, wenn die Fragen *estate-weit und adversariell* werden:
**welche Agenten existieren, was kann jeder tatsächlich erreichen, wo driftet der Zugriff
von der Policy ab, kann ich es einem Prüfer beweisen, und kann ich eine schlechte Aktion
deny-closed stoppen** — alles, ohne dieses Bild in die Cloud eines anderen zu senden.
