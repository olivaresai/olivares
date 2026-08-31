---
title: Claude apps gateway + Olivares AI gemeinsam deployen
description: >-
  So betreiben Sie Anthropics selbst gehostetes Claude apps gateway und lassen
  es von Olivares AI als weitere Unternehmensoberfläche governen: Inventar,
  Posture, Audit-Ingestion, OTLP-Korrelation und der
  Gateway-Protokoll-Endpunkt der Phase 1.
sidebar:
  order: 9
---

## Was das Claude apps gateway ist

Anthropics
[Claude apps gateway](https://code.claude.com/docs/en/claude-apps-gateway) ist
ein selbst gehosteter Dienst, der ab v2.1.195 im `claude`-Binary enthalten
ist; starten Sie ihn mit `claude gateway --config gateway.yaml` und verwenden
Sie PostgreSQL als Backend. Er schaltet OIDC-Anmeldung vor Amazon Bedrock,
Claude Platform on AWS, Google Cloud Agent Platform, Microsoft Foundry oder
die Anthropic API. Entwickler verwenden dadurch Sitzungen des
Unternehmens-IdP statt lokaler Anbieterzugangsdaten. Seine `gateway.yaml`
ordnet IdP-Gruppen Modell-Allowlisten und Managed Settings zu, und seine
Spend-Limits-Admin-API kann die Ausgaben pro Benutzer, Gruppe oder Organisation
begrenzen. Er verteilt Telemetrie über OTLP und gibt einzeilige
JSON-Audit-Ereignisse aus. Anthropics
[Ankündigung](https://claude.com/blog/introducing-the-claude-apps-gateway) vom
29. Juni 2026 beschreibt ihn als First-Party-Gateway-Infrastruktur für
Claude Code.

## Betreiben Sie ihn. Olivares governt ihn.

Wenn Sie das Anthropic Gateway bereits betreiben oder dies planen, behalten
Sie es. Die Doktrin lautet **und, nicht oder**: Anthropics Gateway besitzt den
Gateway-Session-, Modellzugriffs- und Upstream-Routing-Pfad von Claude Code;
Olivares AI macht dieses Deployment zu einer governten Oberfläche innerhalb
der umfassenderen Kontrollebene.

Der Connector `claude-apps-gateway` inventarisiert `gateway.yaml`: Issuer,
IdP-Gruppe -> Modell-Allowlisten, Spend-Admin-Posture, OTLP-Ziele und
Upstreams. Er erzeugt Posture-Findings für Konfigurationszustände, die für
einen Governance-Operator relevant sind, und nimmt die JSON-Audit-Ereignisse
des Gateways auf, sodass Ablehnungen, ausgestellte Sitzungen und
Inferenzdatensätze in das manipulationserkennbare Audit-Ledger gelangen. Leiten
Sie den OTLP-Fan-out des Gateways an den Olivares-OTLP-Receiver, kann das
`session.id`-Signal mit den Runtime-Einträgen governter Sitzungen korreliert
werden; Olivares bewahrt weiterhin strukturelle Daten auf, keine
Prompt-Payloads.

## Dokumentierte Grenzen

Die folgenden Scope-Entscheidungen von Anthropic sind aus dessen Dokumentation
mit Stand 2026-07-03 zitiert. Dies sind Scope-Aussagen, keine Defekte;
sie bestimmen, wo die Grenze eines gemeinsamen Deployments verläuft.

| Feature | Status | Hinweise |
|---|---|---|
| SAML, LDAP und andere Nicht-OIDC-Authentifizierung | Nicht unterstützt. | Nur OIDC. Bei Bedarf eine OIDC-Bridge davorschalten |
| Multi-Tenancy (mehrere OIDC-Issuer) | Nicht unterstützt. | Ein Issuer pro Gateway. Separate Instanzen betreiben |
| Admin-UI | Nicht verfügbar. | Die Konfiguration ist die YAML-Datei; Änderungen erfordern ein erneutes Deployment |
| Helm-Chart | Nicht verfügbar. | Das Gateway läuft als standardmäßiges stateless Deployment |
| CI-Pipelines | Es gibt keinen Service-Token-Flow für unbeaufsichtigte Pipelines |  |
| OTLP/gRPC | Nicht unterstützt. | Nur OTLP über HTTP |
| Windows-Server | Nicht unterstützt. | Auf Linux deployen |
| Modellkatalog | Nur Claude-Modelle | Das Gateway übersetzt Claude-IDs je Upstream |

## Was Olivares daneben ergänzt

Olivares beseitigt diese Grenzen des Anthropic Gateways nicht. Es ergänzt
daneben die fehlende Governance-Ebene.

| Grenze des Anthropic Gateways | Daneben verfügbare Olivares-Fähigkeit |
|---|---|
| SAML, LDAP und andere Nicht-OIDC-Authentifizierung | Für die Olivares-Konsole und Governance-Ebene dokumentiert [SSO-/SCIM-Identität](/de/how-to/connectors/sso-scim-identity/) die OIDC-/SAML-Föderation, und [die IdP-Architektur](/de/explanation/architecture/where-it-fits-with-your-idp/) bildet Menschen und Agenten auf SSO-/SCIM- und SPIFFE-/WIF-Roster ab. Das rüstet SAML nicht im Anthropic Gateway nach; lassen Sie das Gateway OIDC-only oder schalten Sie eine OIDC-Bridge davor. |
| Multi-Tenancy (mehrere OIDC-Issuer) | Die [mandantenfähige Kontrollebene](/de/reference/modules/xx-multi-tenancy/) von Olivares beschränkt Entitäten, Findings, Sitzungen und das Audit-Ledger auf den jeweiligen Mandanten und verwendet PostgreSQL RLS für mandantenfähige Deployments. Betreiben Sie pro Issuer eine separate Gateway-Instanz und governen Sie jede als eigene Oberfläche; behandeln Sie ein Anthropic Gateway nicht als Multi-Issuer. |
| Admin-UI | Die Olivares-Webkonsole ist eine Präsentationsschicht über derselben API, die [Modul XIX](/de/reference/modules/xix-api-manage-as-code/) beschreibt, und die Identitätsdokumentation zeigt die Live-UI **Identity & NHI -> SSO & SCIM**. Sie ist eine Admin-Konsole für die Kontrollebene, kein UI-Editor für Anthropics `gateway.yaml`. |
| Helm-Chart | Olivares liefert ein eigenes [Kubernetes-Helm-Deployment](/de/tutorials/getting-started/kubernetes/) und einen separaten Kubernetes-Operator. Damit wird die Olivares-Kontrollebene deployed; es wird nicht behauptet, Anthropics Gateway zu paketieren. |
| CI-Pipelines | Olivares-Automation kann über [Manage-as-Code](/de/how-to/manage-as-code/) opake, widerrufbare und mandantengebundene API-Tokens verwenden. Für governte Runtime- und Deployment-Zugangsdaten stellt der WIF-/SPIFFE-Broker kurzlebige Zugangsdaten aus; das ist vom Anthropic Gateway getrennt, dessen eigene CI-Anleitung weiter eine direkte Anbindung an den Anbieter vorsieht, sofern Sie nicht bewusst den nachstehenden Olivares-Proxy-Endpunkt nutzen. |
| OTLP/gRPC | Der Olivares-Receiver `claude` akzeptiert die üblichen OTLP-Receiver-Pfade von [OpenTelemetry GenAI](/de/how-to/connectors/otel-genai/), einschließlich HTTP und gRPC. Das Anthropic Gateway sendet weiterhin OTLP/HTTP; andere governte Agenten können gRPC direkt verwenden, und die entstehenden Ereignisse können das kryptografische Audit-Ledger und [Compliance-Evidence-Packs](/de/reference/modules/xiii-compliance/) speisen. |
| Windows-Server | Hier wird keine Windows-Server-Fähigkeit behauptet. Betreiben Sie serverseitige Komponenten auf Linux, in Containern oder Kubernetes und governen Sie Entwicklerendpunkte über Telemetrie, Hooks und Connector-Evidence. |
| Modellkatalog | [Modul X](/de/reference/modules/x-models/) governt ein anbieterübergreifendes Modell-/Provider-Estate: Claude, OpenAI, Gemini und lokale Inferenz; der Bedrock-Connector ergänzt Bedrock-Nutzung/-Kosten und Guardrails-Observability. Das Anthropic Gateway bleibt Claude-only, während Olivares das weitere Estate governt, einschließlich der Codex-Posture über [Subscription-Auth-Governance](/de/explanation/positioning/governing-subscription-authed-agents/). |

## Protokoll-Superset, Phase 1

Anthropic veröffentlicht das Gateway-Protokoll und lädt Drittanbieter zu
Implementierungen ein. Der Olivares-Inferenz-Proxy implementiert ein
Phase-1-Superset, das im Engineering-Vertrag für das Apps-Gateway-Protokoll
beschrieben ist: OAuth-Discovery, RFC-8628-Geräteautorisierung, Token-Polling
über die Credential-Seam der Sitzungen nach authentifizierter Genehmigung,
Auslieferung eines einzelnen Managed-Settings-Dokuments mit ETag, die
read-only Form der Spend-Limits-Liste und `GET /protocol`.

Der Deskriptor dokumentiert die Abweichungen selbst: Managed Settings
verwenden den Single-Document-Modus, der Versions-Header lautet
`x-olivares-version`, die Write-/Effective-/Audit-Routen für Spend Limits
geben konforme `501`-Antworten zurück, und Olivares behält seine
umfangreichere Budget-Deny-Zuordnung bei und ergänzt
`x-should-retry: false`. Phase 1 liefert weder Anthropics
OIDC-Callback/Browserseite `/device` noch Merge-Regeln für Managed Settings
pro Gruppe, Write-Pfade für Spend Limits, `count_tokens` oder die
Header-Attribution `x-claude-code-session-id`.

## Topologie wählen

- **Nur Gateway.** Ausreichend für eine OIDC-Organisation mit einem einzelnen
  Issuer, die ausschließlich Claude verwendet, YAML und erneute Deployments
  selbst verwalten kann und mit den eigenen Spend Limits, dem OTLP-Fan-out und
  der JSON-Audit-Ausgabe des Gateways zufrieden ist.
- **Gateway + Olivares.** Das empfohlene gemeinsame Deployment, wenn
  Claude Code in ein reguliertes Estate eingeführt wird: Behalten Sie das
  Anthropic Gateway, fügen Sie den Connector `claude-apps-gateway` hinzu,
  leiten Sie OTLP an Olivares und bewahren Sie die resultierende Sicht auf
  Posture, Runtime und Evidence in der Kontrollebene auf.
- **Olivares-Proxy als Gateway-Protokoll-Endpunkt.** Verwenden Sie diese
  Option, wenn der Olivares-Inferenz-Proxy bewusst die
  Gateway-Protokoll-Oberfläche der Phase 1 bereitstellen soll. Sie ist
  geeignet, wenn das ausgelieferte Subset ausreicht; sie ersetzt weder den
  Browser-OIDC-Flow des Anthropic Gateways vollständig noch dessen
  Write-Pfad-Administration für Spend Limits.
