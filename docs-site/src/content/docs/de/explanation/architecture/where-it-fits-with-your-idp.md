---
title: Wo Olivares AI zu Ihrem IdP passt
description: >-
  Olivares AI ist kein Identity Provider. Es föderiert Agent-Identität aus den
  Registries, die Sie bereits betreiben — Entra Agent ID, AWS AgentCore Identity,
  Google Agent Identity — read-only und nutzt sie zur Zuordnung der Access Map.
  Wie es mit Ihrem IdP, SSO/SCIM, SPIFFE/WIF und den ID-JAG-/XAA-Standards
  zusammenwirkt.
sidebar:
  order: 3
---

Eine häufige erste Frage von Sicherheitsarchitekten lautet: *„Ist das noch ein
weiteres Identitätssystem, das ich betreiben muss?“* Nein. **Olivares AI ist kein
Identity Provider und besitzt keine Identitäten.** Es **konsumiert** die
Identitäten, die Sie bereits ausstellen — für Menschen aus Ihrem IdP über
SSO/SCIM; für Agents aus den Agent-Identity-Registries, die die Hyperscaler
allgemein verfügbar gemacht haben — und nutzt sie, um zuzuordnen, *wer oder was*
hinter jeder Edge in der [Access Map](/de/explanation/) steckt. Diese Notiz erklärt
genau, wo die Naht sitzt.

## Die Schichtung

```
   Your IdP (Entra ID / Okta / Google)         ← humans: SSO + SCIM (unchanged)
   Agent-identity registries                    ← agents: Entra Agent ID,
     (Entra Agent ID / AgentCore / Google)        AgentCore Identity, Google Agent Identity
            │  read-only roster sync
            ▼
   Olivares AI  ── SPIFFE/WIF roster ──► R/RW access map (attributed edges)
            │                            └─ Permitted-vs-Observed drift
            └─ deny-closed gates (approvals, hooks PEP, MCP gating) — never an IdP
```

- **Menschen** authentifizieren sich über **Ihren** IdP. Olivares AI integriert
  sich mit standardmäßigem **SSO und SCIM** für Operator-Konten und Gruppe-zu-Rolle-
  Mapping; es speichert keine Credentials und wird nicht zu einem zweiten
  Verzeichnis.
  → [SSO- & SCIM-Identität](/de/how-to/connectors/sso-scim-identity/)
- **Agents** erhalten ihre Identität aus den Registries, die Sie bereits eingeführt
  haben. Olivares AI föderiert diese Roster **read-only** auf ein internes
  **SPIFFE/WIF**-Roster, sodass jeder beobachtete Zugriff einer gesteuerten,
  benannten Identität zugeordnet werden kann statt einem anonymen Prozess.

## Was die Agent-Identity-Föderation tatsächlich tut

Die Control Plane liefert read-only Roster-Konnektoren für die GA-Agent-Identity-
Registries aus, jeder gegen seine Primärquelle verifiziert und **deny-closed**
(keine Credential → leeres Roster, niemals ein Phantom-Fehler):

- **Microsoft Entra Agent ID** — importiert Agent-Identitäten, Blueprints und
  Owner-/Sponsor-Beziehungen über Microsoft Graph; legt von der Registry behauptete
  Orphans offen. Blueprints, die langlebige Passwort-Credentials tragen, erzeugen
  ein **Long-lived-Credential-Drift**-Finding.
- **AWS AgentCore Identity** — importiert das Agent-Roster; Agents mit einer
  Service-Identität werden auf eine Service-Account-Identitätsart abgebildet.
- **Google Agent Identity** — importiert Reasoning-Engine-Identitäten; die Referenz
  ist eine vollständige **SPIFFE ID**, sodass sie über die External ID mit dem
  SPIFFE-Roster konvergiert.

Diese Mappings speisen die Attributionsachse der
[Access-Map-Zuordnung](/de/reference/glossary/#attribution-konfidenz)
(`firm` / `approximate` / `unknown`) — sie reimplementieren sie nicht. Die
Föderation ist strikt read-only: Olivares AI **mutiert niemals** eine entfernte
Registry. Owner- und Orphan-Signale werden an den Non-Human-Identity-Lebenszyklus
weitergeleitet, sodass ein von der Registry behaupteter Orphan über die bestehende
Governance-Maschinerie auftaucht.

:::note[Experimentell und Design-toward, als solche gekennzeichnet]
Ökosystemübergreifende Deskriptoren (**OASF**) und **AGNTCY Agent Badges** werden
als **experimentell** behandelt, bis sie die Verifiable-Credential-Konformität
erfüllen. Roster, die sich noch in der Preview befinden (z. B. Googles Gemini
Enterprise Agent Platform), sind als **Nähte** verdrahtet, nicht als live
behauptet. Wir markieren, was GA ist, was Preview ist und was Design-toward ist —
wir verwischen sie nicht.
:::

## ID-JAG, XAA und SPIFFE-basierte Client-Authentifizierung

Die Enterprise-Standards für *delegierten, zuordenbaren* Agent-Zugriff
konvergieren, und die Control Plane ist darauf ausgelegt, auf ihnen mitzureiten,
statt ihre eigenen zu erfinden:

- **ID-JAG** (Identity Assertion JWT Authorization Grant) und **XAA** (Cross-App
  Access) sind das aufkommende Muster, mit dem ein IdP **scoped, zuordenbare**
  Autorisierung für einen Agent ausstellt, der anwendungsübergreifend handelt — die
  enterprise-managed Autorisierungserweiterung in der MCP-Autorisierungsarbeit.
  Sobald diese landen, wird der zuordenbare Token zu einem weiteren
  hochauflösenden Signal, das die Access Map an eine gesteuerte Identität binden
  kann.
- **SPIFFE-basierte OAuth-Client-Authentifizierung**
  (`draft-ietf-oauth-spiffe-client-auth`) erlaubt den eigenen OAuth-Flows der Plane,
  sich mit einer **SVID** zu authentifizieren, sobald ein Authorization Server
  Unterstützung veröffentlicht — über das bestehende deny-by-default mTLS. Dies ist
  **Design-toward**, ohne Konformitätsanspruch, bis der Draft und die
  Server-Unterstützung stabilisiert sind.
- **Standardmäßig kurzlebig.** Langlebige statische Credentials, die im Estate
  entdeckt werden, werden als Drift-Klasse markiert, im Einklang mit der **Five
  Eyes**-Leitlinie (2026), dass Agent-Credentials kurzlebig sein sollten.

## Was das für Sie bedeutet

- Sie behalten Ihren IdP, Ihr SSO, Ihr SCIM und die Agent-Identity-Registry, auf
  die Sie sich standardisiert haben. Nichts migriert.
- Olivares AI wird zu dem Ort, an dem **alle** diese Identitäten auf das
  **beobachtete Verhalten** Ihres Estates treffen — die einzige Schicht, die sagen
  kann: „Dieser Agent, aus dieser Registry, im Besitz dieses Menschen, nutzt
  Zugriff, den die Policy nie gewährt hat.“
- Da die Föderation read-only und self-hosted ist, braucht diese Korrelation keinen
  vorgegebenen Datentransfer: Es gibt keine verpflichtende Telemetrie und standardmäßig
  keinen Egress der Control Plane. Ihren Perimeter überschreitet nur, was Sie dafür
  konfigurieren — Aufrufe an Ihre Modell-APIs, die von Ihnen eingerichteten
  SIEM-/Webhook-Ausgaben und ein externer Embedding-Anbieter, falls Sie einen bereitstellen.

## Verwandt

- [Agent / Identität / NHI](/de/reference/glossary/#identity--nhi) — die
  Glossar-Definitionen.
- [vs. AI Control Towers](/de/explanation/positioning/vs-control-towers/) — die
  bidirektionale Integration mit den Admin-Planes der Ökosysteme.
