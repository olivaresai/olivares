---
title: "Session-Cockpit (Verfügbarkeit)"
description: >-
  Verfügbarkeitsdeskriptor für den API-Namensraum session-cockpit. Community
  registriert diesen Namensraum ohne Handler und ohne interaktives Cockpit.
  Wie Sie die Abwesenheit anhand von Live-Sessions und AgentOps prüfen und
  welche offenen Fähigkeiten Sie nutzen.
---

Die Community-Binärdatei registriert einen Verfügbarkeitsdeskriptor für den
API-Namensraum `session-cockpit`. Dieser Namensraum hat derzeit **keine Handler**
und **kein interaktives Cockpit**. Anfragen unter `/v1/m/session-cockpit`
erhalten **404 durch Abwesenheit**. Der Deskriptor ist keines der 30
Produktmodule im Katalog.

## Aktuelle Verfügbarkeit

| Oberfläche | Community (dieses Artefakt) |
|---|---|
| API-Namensraum | `session-cockpit` (`/v1/m/session-cockpit`) |
| Deskriptor | `olivares.session-cockpit` `0.1.0` — Titel `Session cockpit (availability)` |
| Registrierte Routen / Handler | keine |
| Interaktives Cockpit | keines |
| Deklarierte Berechtigung | `session-cockpit:availability:read` (deklariert, nicht geroutet) |
| Lebenszyklus | leer (`Init` / `Start` / `Stop` tun nichts) |

Ein 404 in diesem Namensraum ist die erwartete Community-Antwort. Das bedeutet
nicht, dass die Control Plane fehlgeschlagen installiert wurde.

## Abwesenheit diagnostizieren

Prüfen Sie, dass mitgelieferte Session-Oberflächen weiterhin funktionieren:

1. Live-Session-Modulrouten unter dem Namensraum `sessions` —
   [Live-Betrieb und Sessions](/de/reference/modules/ii-sessions/).
2. Konsole **Sessions** (`/sessions`), **Claude Code** (`/agentops`) und
   **Work** (`/work`) — [Konsolenreferenz](/de/reference/console/).
3. Offizieller CLI-Lebenszyklus im nächsten Abschnitt.

Wenn diese antworten und `/v1/m/session-cockpit` 404 ist, entspricht der
Deskriptor diesem Artefakt.

## Offene Fähigkeiten

Installation, Start, Beobachtung und Verwaltung der offiziellen CLIs bleiben
im offenen Community-Produkt:

- [Live-Betrieb und Sessions](/de/reference/modules/ii-sessions/) — Live-Agent-
  Sessions, Zeitlinien, Provider-Profile und `live_ref`.
- [Eine Provider-Session betreiben](/de/how-to/operate-provider-sessions/) —
  Claude, Codex oder Grok unter einem gepinnten offiziellen Binary starten.
- [Claude Code mit Olivares ausführen](/de/how-to/run-claude-code-with-olivares/) —
  AgentOps-Co-Deployment offizieller `claude`-Sessions.
- Connector- und PEP-Hook-Anleitungen (beobachten/steuern, kein Session-Start):
  [Claude Code](/de/how-to/integrations/claude-code/),
  [Codex](/de/how-to/integrations/codex/),
  [Grok Build](/de/how-to/integrations/grok/).
- [Aufzeichnung privilegierter Sessions](/de/reference/modules/recording/)
- [Identität, Berechtigungen und Governance](/de/reference/modules/vi-governance/)

## Verwandt

- [Modulkatalog](/de/reference/modules/overview/)
- [Ehrlichkeit und Grenzen](/de/start/honesty-and-limits/)
