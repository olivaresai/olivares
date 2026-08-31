---
title: Wo Olivares vs. Ihr AI-Gateway & Guardrails passt
description: >-
  Sie betreiben bereits ein AI-Gateway (LiteLLM, Portkey, Cloudflare) oder
  Hyperscaler-Guardrails (Bedrock, Azure). Gut — behalten Sie sie. Olivares AI ist
  kein Gateway und konkurriert nicht bei Routing oder Caching. Es ist die
  Governance- und Evidenz-Plane, die daneben sitzt und die Lücke schließt, die
  jene offen lassen.
sidebar:
  order: 7
---

Wenn Sie bereits in ein **AI-Gateway** oder in die **Guardrails** eines Hyperscalers
investiert haben, ist das ehrliche Erste, was zu sagen ist: **behalten Sie sie, und
Olivares AI versucht nicht, sie zu ersetzen.** Die Aufgabe eines Gateways ist der
Modellaufruf — ihn routen, cachen, balancieren, budgetieren. Die Aufgabe von Guardrails
ist Content-Sicherheit bei diesem Aufruf. Beide sind real, beide sind gut in dem, was sie
tun, und keines ist das, was Olivares ist.

:::tip[Die Kurzfassung]
**Olivares AI ist kein AI-Gateway.** Es routet, cacht, load-balanciert nicht und sitzt
nicht auf dem Hot Path Ihres Modell-Traffics, und das wird es nie. Es sitzt **neben und
hinter** Ihrem Gateway als die *Governance- und Evidenz-Plane*: In-Process-Durchsetzung
innerhalb der Agenten-Laufzeit, ein manipulationserkennbares Evidenz-Ledger,
Non-Human-Identity-Lifecycle und Human-in-the-Loop / Break-Glass / Kill-Switch
über **Live-Sessions**. Ihr Gateway regelt den *Request*; Olivares regelt den
*Agenten und alles, was er berührt*, und beweist es einem Prüfer.
:::

## Was ein Gateway und Guardrails gut können (dafür nutzen)

Dies sind Commodity-, gut verstandene Fähigkeiten, und die Anbieter beschreiben sie
unverblümt:

- **AI-Gateways** sind Request-Path-Manager für Modellaufrufe. LiteLLM ist ein
  *"OpenAI Proxy Server (LLM Gateway) to call 100+ LLMs in a unified interface &
  track spend, set budgets per virtual key/user"*
  ([LiteLLM](https://docs.litellm.ai/docs/simple_proxy)); Cloudflare AI Gateway
  lässt Sie *"Connect to any model, dynamically route requests, and manage usage,
  billing, and logs from one unified gateway"*
  ([Cloudflare](https://www.cloudflare.com/products/ai-gateway/)); Portkey
  *"records real-time API requests, including cost"*
  ([Portkey](https://portkey.ai/features/ai-gateway)). Routing, Fallbacks,
  Caching, virtuelle Keys, Budgets pro Key, Request-Logging — das ist ihre Bahn.
- **Hyperscaler-Guardrails** sind Content-Sicherheitsfilter. Bedrock Guardrails
  *"provides configurable safeguards to help you build safe generative AI
  applications"*, die *"detect and filter undesirable content and protect
  sensitive information that might be present in user inputs or model responses"* —
  Content-Filter, gesperrte Themen, Wortfilter, PII-Maskierung, Contextual-Grounding-
  und Automated-Reasoning-Checks
  ([AWS](https://docs.aws.amazon.com/bedrock/latest/userguide/guardrails.html)).

Wenn Ihr Problem lautet *„gib meinen Apps einen Endpunkt zu vielen Modellen, mit Budgets,
Caching und Content-Filtering“*, löst dieser Stack es, und Sie brauchen dafür keine
Control Plane. Wir integrieren uns mit diesem Muster; wir implementieren es nicht neu.

## Die Governance-Lücke, die sie offen lassen

Ein Gateway sieht einen **Request**. Guardrails sehen **Content**. Keines sieht den
**Agenten** — seine Identität über die Zeit, was er über Ihre Data Plane erreicht hat, wer
eine riskante Aktion freigegeben hat und ob sich irgendetwas davon später beweisen lässt.
Das ist die Lücke, die Olivares füllt.

| Lücke, die Gateway / Guardrails lassen | Warum es zählt | Was Olivares AI bietet |
|---|---|---|
| **Durchsetzung an der Agenten-Laufzeit** | Ein Gateway setzt an der *Request-Grenze* durch; es kann einen lokalen Claude-Code-Tool-Call, der es nie durchläuft, nicht stoppen | Ein deny-closed [In-Process-PEP](/de/how-to/connectors/claude-code-hooks-pep/) am Agenten: Firm-Identity-Gate, Policy-Disposition, Live-Policy-Overlay, alles bevor das Tool läuft |
| **Manipulationserkennbare Evidenz** | Gateway und Guardrails emittieren *Logs* — veränderliche Request-Datensätze; ein Prüfer will unveränderlichen Beweis | Append-only, hash-chained, [Ed25519-signiertes Ledger](/de/reference/glossary/#audit-ledger), off-box verifizierbar, exportierbar als OSCAL-Evidenz |
| **Non-Human-Identity-Lifecycle** | Der „virtuelle Key“ eines Gateways ist ein Budget-Topf, keine Identität, die provisioniert, attribuiert, rotiert und offgeboardet wird | [NHI-Lifecycle](/de/reference/glossary/#identity--nhi): Staleness → Block, Offboarding-Kaskade, Dual-Control bei Rotation, an die Access Map gebunden |
| **Live-Session-Intervention** | Logs und Budgets sind nachträglich; keines dieser betrachteten Tools stoppt eine Session mitten im Flug | [HITL-Freigaben](/de/reference/glossary/#approval-hitl), [Break-Glass](/de/reference/glossary/#break-glass) und ein [Kill Switch](/de/reference/glossary/#kill-switch), der jede geregelte Aktuierung verweigert, bis ein Dual-Control-Re-Enable erfolgt |
| **Ground Truth über das Estate** | Ein Gateway sieht nur die Aufrufe, die es durchlaufen; Agenten berühren auch DBs, Object Stores, MCP, Dateien direkt | Die read-first [R/RW Access Map](/de/explanation/#die-access-map-read-first-minimal-data-permitted-vs-observed) und der Permitted-vs-Observed-Drift, abgeglichen gegen nativen Audit |
| **Souveränität** | SaaS-Gateways und Cloud-Guardrails verarbeiten diesen Traffic in ihrer Cloud | Self-hosted / air-gapped; die Data Plane verlässt Ihre Grenze nie |

Keines davon sind Routing-Features. Das ist der Punkt: Die Lücke ist nicht *besseres
Routing*, sie ist **Governance, die der Request-Pfad nie zu liefern entworfen wurde.**

## Speziell zu Guardrails: Content-Sicherheit ist ein Hook, kein Wettbewerber

Bedrock Guardrails können auf zwei Arten angewandt werden — inline während eines
Bedrock-Inferenzaufrufs, oder *"directly through the `ApplyGuardrail` API without invoking
the foundation models"*, was *"with any foundation model whether hosted on
Amazon Bedrock or self-hosted models"* funktioniert
([AWS](https://aws.amazon.com/bedrock/guardrails/)). Das ist wirklich nützlich, und
Olivares behandelt Content-Sicherheit als einen **Detektor, den Sie einstecken**, niemals
als eine Mauer, die wir Sie *anstelle von* Guardrails zu wählen bitten. Zwei ehrliche,
eigenständige Fakten:

- Der Inline-Inferenz-Proxy stellt eine **Content-Inspection-Naht** bereit — einen
  pluggable Punkt, an dem ein Content-/DLP-Detektor ein Verdikt zurückgibt, auf das der
  deny-closed Decider reagiert. Content-Sicherheit gehört *dorthin*, in die Pipeline,
  statt als konkurrierender Filter neu implementiert zu werden.
- Olivares liest die **eigenen Entscheidungen** Ihrer Guardrails read-first. Der
  AWS-Connector ingestiert Bedrock-Guardrail-Entscheidungen aus deren CloudWatch-/S3-Logs
  als Posture und Evidenz; er ruft die kostenpflichtige `ApplyGuardrail`-Laufzeit
  absichtlich **nicht** selbst auf. Ihre Content-Verdikte werden Teil des
  manipulationserkennbaren Datensatzes.

So komponiert sich Content-Sicherheit mit dem, was Sie bereits betreiben. Was Guardrails
*nicht* dokumentieren — und wo die Governance-Lücke offen bleibt — ist der Rest des Lebens
des Agenten: Die Bedrock-Seiten dokumentieren keine Agenten-Identität, kein
Session-Management, keine menschlichen Freigaben und keine Kosten-Governance (auf jenen
Seiten nicht dokumentiert, geprüft 2026-06-21).
Olivares ist genau dieses Komplement: Es trägt die Identität, die Session-Kontrollen,
die Freigaben und die Evidenz; der Content-Filter bleibt, wo er bereits lebt.

## Wie sie sich komponieren

Eine gesunde Anordnung hält jedes Tool in seiner Bahn:

- **Behalten Sie Ihr Gateway** (LiteLLM / Portkey / Kong / Cloudflare) als die
  Modellaufruf-Plane — Routing, Caching, virtuelle Keys, Budgets auf dem Request.
- **Behalten Sie Ihre Guardrails** (Bedrock / Azure Content Safety) als Ihren
  Content-Sicherheits-Detektor — der Olivares-PEP führt einen pluggable Detektor an seiner
  Content-Inspection-Naht aus und liest die eigenen Entscheidungen Ihrer Guardrails
  read-first als Evidenz; er ruft `ApplyGuardrail` nicht selbst auf.
- **Fügen Sie Olivares daneben hinzu** als die Governance- und Evidenz-Plane: den
  In-Process-PEP auf den Agenten, die Ihr Gateway nie erreichen, die Access Map über das
  gesamte Estate, das manipulationserkennbare Ledger und die Live-HITL/Break-Glass/Kill-
  Kontrollen.

Der eine Ort, an dem Olivares Inferenz berührt, ist eng und explizit — ein
**API-Key-only**-Gateway-Pfad für rohe SDK-/`curl`-Aufrufer, beschrieben in
[Abonnement-authentifizierte Agenten regeln](/de/explanation/positioning/governing-subscription-authed-agents/).
Er existiert, um Traffic zu regeln, den Ihre anderen Tools nicht erreichen können, niemals
um mit ihnen beim Routing zu konkurrieren, und er trägt **niemals** ein
Abonnement-Credential.

## Wann Ihr Gateway genügt

Ehrlichkeit schneidet in beide Richtungen. Wenn Ihre Agenten Modelle ausschließlich
**durch** Ihr Gateway aufrufen, Ihre Content-Sicherheitsbedürfnisse von Guardrails erfüllt
werden, Sie **keine self-hosted oder laptop-residenten Agenten** haben, die Datenbanken /
Object Stores / MCP direkt erreichen, und Sie **keine Souveränitäts- oder
Anforderung an manipulationserkennbare Evidenz** haben — dann sind Ihr Gateway plus seine
Logs und Guardrails vielleicht alles, was Sie brauchen, und Sie sollten keine Control
Plane um ihrer selbst willen hinzufügen.

Olivares verdient seinen Platz, wenn die Fragen *estate-weit und adversariell* werden:
welche Agenten existieren und was jeder tatsächlich erreicht hat, kann ich eine schlechte
Aktion deny-closed **am Agenten** stoppen, wer hat die riskante freigegeben, und kann ich
einem Prüfer **unveränderlichen Beweis** überreichen — alles, ohne dieses Bild in die
Cloud eines anderen zu senden.
Für die tiefere Behandlung zweier benachbarter Vergleiche siehe
[vs. AI-Control-Towers](/de/explanation/positioning/vs-control-towers/) und
[vs. LLM-Gateways & Observability](/de/explanation/positioning/vs-llm-observability/).
