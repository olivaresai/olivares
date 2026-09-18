---
title: Session-Runtime-API (offizielle CLIs)
description: >-
  Community-HTTP-Oberfläche zum Listen, Anhängen, Speisen und Stoppen eigener
  Claude-Code-, Codex- und Grok-CLI-Prozesse. Berechtigungen, PTY, Resume und Reconnect.
---

Die Control Plane **startet die Anbieter-CLI**. Sie ersetzt Claude Code, Codex
oder Grok Build nicht. Sitzungen und Terminals sind ein Modul dieses Produkts,
nicht das Produkt.

Diese Seite dokumentiert die Community-Operate-Routen unter
`/v1/m/sessions/runs`. Sie stehen bereits im
[beta-OpenAPI-Dokument](/reference/api-beta/). v26.10 ergänzt den
Driver-Vertrag, einen lokalen PTY-Runner und die Journeys J01–J08 als Tests.

## Editionsgrenze

| Edition | Was sie tut | Was sie nicht tut |
|---|---|---|
| **Community (diese Seite)** | Eigenes lokales Kind: Start, stdin/stdout/stderr, Attach mit Cursor, Resume der exakten Konversation, Reconnect eines lebenden Streams, Stop mit beobachtetem Exit-Status. Sitzungszeilen und Evidenz bleiben in Modul II. | Identity-&-Scale-Engine mit mehreren Panes, mTLS-Listener, kommerzielle Input-Sessions, xterm-UI-Chunk |
| **Identity-&-Scale-Overlay** | Kommerzielle session-cockpit-Engine (Listener, Panes, Ledger). Routen unter `/v1/m/session-cockpit/`, wenn das Add-on vorliegt. | Ersetzt `/v1/m/sessions/runs` nicht |

Ein Community-Build beantwortet den Overlay-Namespace durch **Abwesenheit**
(404). Es gibt keinen 501-Stub.

Der Claude-Code-Hook bleibt `olivares claude-hook` (PreToolUse-PEP). Das ist
Beobachtung und Durchsetzung, keine Ersatz-CLI.

## Berechtigungen

Bestehende Durchsetzung auf den Modulrouten:

| Berechtigung | Routen |
|---|---|
| `sessions:run:read` | `GET /runs`, `GET /runs/{ref}`, `GET /runs/{ref}/events`, `GET /runs/{ref}/attach` |
| `sessions:run:write` | `POST /runs`, `POST /runs/{ref}/input`, `POST /runs/{ref}/interrupt`, `POST /runs/{ref}/stop`, `POST /runs/{ref}/resume` |
| `sessions:run:admin` | `POST /runs/{ref}/cleanup`, `DELETE /runs/{ref}` |

Ein Viewer darf listen. Create, Input und Stop brauchen write. Der Authorizer
der Route ist der Durchsetzungspunkt; eine fehlende Berechtigung ist 403.

## Routen, die die Konsole liest

Basis: `/v1/m/sessions`. Authentifizieren. `X-Olivares-Tenant` senden.

| Methode | Pfad | Ergebnis |
|---|---|---|
| `GET` | `/runs` | Seite verwalteter Läufe |
| `GET` | `/runs/{ref}` | Ein Lauf. `state` ist abgeleitet. `exit_code` ist beobachtet. Kein erfundenes Success-Feld |
| `GET` | `/runs/{ref}/events` | Lebenszyklus-Evidenzzeilen |
| `GET` | `/runs/{ref}/attach?from={seq}` | SSE: `output`-Rahmen, `lag` wenn der Ring unter dem Cursor verdrängt hat, `end` oder eine not-live-`notice` |
| `POST` | `/runs/{ref}/input` | stdin. Stream-json nutzt `line`/`message`. Codex/Grok nutzen `text`. 202 `{accepted:true}` |
| `POST` | `/runs/{ref}/stop` | SIGTERM, dann SIGKILL der Prozessgruppe. Beobachteter Exit-Status auf der Zeile |
| `POST` | `/runs/{ref}/resume` | Neue Prozessgeneration. Exakte gespeicherte Konversation |
| `POST` | `/runs/{ref}/interrupt` | Aktiven Turn abbrechen. Der Prozess bleibt |

Reconnect nach einem abgebrochenen Attach ist `GET …/attach?from={last+1}` auf
dem **selben** lebenden Prozess. Nach Prozessverlust sagt Attach, dass die
Sitzung nicht live ist. Resume startet eine neue Generation. Reconnect erfindet
keinen Ersatzprozess (SDD R04).

## Transport (Community)

Jede offizielle CLI startet auf dem Transport, den ihre eigene Operate-Form
braucht; `cliruntime.LaunchTransport` erklärt welchen. Alle drei Formen sind
heute Stdio-Protokolle, also verdrahtet die Composition Root
`sessions.NewProcRunner()`: stdin, stdout und stderr sind Pipes und bleiben
getrennte Ströme.

**Claude Code verweigert ein Terminal auf stdin.** Die `--print`-Form mit
stream-json antwortet `Error: Input must be provided either through stdin or as
a prompt argument when using --print` und endet mit 1 ohne einen Protokoll-
Frame. Ein Terminal braucht eine interaktive Form; `sessions.NewPTYRunner()`
bleibt dafür unter Linux verfügbar. Container- und Sandbox-Isolation bleiben
bei beiden verweigert.

Der Driver-Vertrag liegt in `modules/sessions/cliruntime`. Arten: `claude`,
`codex`, `grok`. Conformance läuft immer gegen ein In-Process-Fake und gegen
einen lokalen PTY-Peer. Wenn `claude` / `codex` / `grok` auf PATH liegen, besitzt
ein getrennter Test das echte Binary, stoppt es und zeichnet den Exit auf. Er
sendet keinen Modell-Turn.

## Verwandt

- [Eine Provider-Session betreiben](/how-to/operate-provider-sessions/)
- [Modul II — Live-Betrieb](/reference/modules/ii-sessions/)
- [Claude Code verbinden](/how-to/connect-claude-code/)
- [Beta-OpenAPI](/reference/api-beta/)
