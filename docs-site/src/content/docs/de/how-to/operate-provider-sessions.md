---
title: Eine Provider-Session betreiben
description: >-
  Registrieren Sie ein Provider-Profil für ein vorhandenes Claude-, Codex- oder
  Grok-Home auf diesem Knoten, pinnen Sie das offizielle Driver-Binary, starten
  Sie eine governte Session aus Konsole oder CLI und unterbrechen oder beenden
  Sie den Turn, ohne einen Runner zu erfinden.
---

Diese Seite ist der **Operate**-Pfad für offizielle Provider-CLIs. Die Control
Plane startet einen eigenen Kindprozess unter einem **Provider-Profil**. Sie
installiert den Provider nicht, legt sein Home nicht an und startet keine
interaktive Browser-Anmeldung.

Das ist nicht der Connector-/Hook-Pfad. Um Konfigurationsdateien von Grok Build
oder Codex zu inventarisieren oder zu governen, verwenden Sie
[Grok Build integrieren](/how-to/integrations/grok/) oder
[Codex integrieren](/how-to/integrations/codex/). Um Claude Code auf demselben
Host mitzubetreiben, verwenden Sie
[Claude Code mit Olivares betreiben](/how-to/run-claude-code-with-olivares/).

Quelle für das Verhalten in v26.9.0: Abschnitt `[26.9.0]` in `CHANGELOG.md`
(Provider-Profile, Codex-Driver, Grok-Driver), die generierten Referenzen zu
[Konsole](/reference/console/) und [Konfiguration](/reference/configuration/),
`cmd/olivares/sessionruntime.go` und `web/src/features/agentops/types.ts`.

## Voraussetzungen

Erfüllen Sie diese Punkte vor einem Start. Ein fehlender Punkt ist eine
Ablehnung, kein Fallback.

1. Olivares AI ist installiert und der erste Administrator existiert.
   Siehe [Ihre erste Stunde](/how-to/first-hour/) für das Setup-Token und die
   AAL3-Passkey-Schranke. Das Anlegen von Quellen und privilegierte
   Session-Operationen erfordern AAL3 (`core/api/middleware.go` `requireAAL3`).
2. Die offizielle Provider-CLI ist bereits auf **diesem Knoten** installiert.
   Das Profil registriert Homes, die bereits existieren. Der Server löst die
   Pfade auf (absolut, Symlinks aufgelöst, vorhandenes Verzeichnis) und legt
   nichts an, installiert nichts und meldet sich nicht an
   (`web/src/features/agentops/types.ts` `CreateProfileRequest`).
3. Sie besitzen `sessions:profile:read`, um **Provider profiles**
   (`/provider-profiles`) zu öffnen, und `sessions:profile:write`, um zu
   registrieren. Das Binden einer Quelle braucht
   `sessions:profile-binding:write` plus Quellenadministration. Das Starten
   eines Laufs braucht `sessions:run:write`.
   Berechtigungen: [Konsolenreferenz](/reference/console/).
4. Der passende Driver ist **auf diesem Knoten registriert**, indem sein
   offizielles Binary gepinnt wird. Die Bereitschaft gilt pro Driver. Es gibt
   keinen gemeinsamen Schalter (`cmd/olivares/sessionruntime.go`).

| Driver | Diese Umgebungsvariable pinnen | Wenn sie nicht gesetzt ist |
|---|---|---|
| Claude Code | `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN` (Standard `claude`) | der Claude-Pfad verwendet den Standard-Executable-Namen |
| Codex | `OLIVARES_SESSION_RUNTIME_CODEX_BIN` | Codex-Profile bleiben beobachtbar und sind nicht startbar |
| Grok | `OLIVARES_SESSION_RUNTIME_GROK_BIN` | Grok-Profile bleiben beobachtbar und sind nicht startbar |

Der Wert ist das offizielle Binary, das dieser Knoten betreiben darf. Die
Engine löst `codex` oder `grok` nicht über `PATH` auf. Die generierte
Konfigurationstabelle listet auch `OLIVARES_SESSION_RUNTIME_OPENCODE_BIN` mit
derselben Registrierungsregel; diese Seite fügt keine weiteren OpenCode-Aussagen
hinzu.

Claude-Starts brauchen weiterhin eine Inferenz-Credential-Quelle
(`OLIVARES_SESSION_RUNTIME_WIF` oder `OLIVARES_SESSION_RUNTIME_TOKEN_FILE`).
Siehe [Ihre erste Stunde §3](/how-to/first-hour/#3-eine-claude-code-sitzung-über-die-konsole-starten).
Codex und Grok verwenden nur den AUTHORIZED `auth_source` des Profils:
`provider_account_home` oder `managed_injection`, ohne Fallback dazwischen und
ohne Vorgabe (`CHANGELOG.md` `[26.9.0]`; `ProviderProfileDTO.auth_source`).

:::caution[Was diese Seite nicht behauptet]
`CHANGELOG.md` `[26.9.0]` stellt fest, dass das Grok-Driver-Verhalten gegen ein
eigenes Fake-ACP-Kind über das echte HTTP, Runtime, Store und Prozessgruppe
nachgewiesen ist. **Kompatibilität mit einem authentifizierten offiziellen
Grok-Konto ist separate Arbeit und wird hier nicht behauptet.**
:::

## 1. Ein Provider-Profil registrieren

Ein Provider-Profil ist die dauerhafte Identität **einer** konfigurierten
Provider-Instanz in **einer** Ausführungsumgebung: Driver, besitzende Umgebung
und die kanonischen `config_home` / `user_home`, unter denen das Kind läuft. Es
ist Konfigurations- und Speicheridentität, kein authentifiziertes Provider-Konto
(`CHANGELOG.md` `[26.9.0]` B1; Konsolentext `agentops.profiles.subtitle`).

### Konsole

1. Öffnen Sie **Provider profiles** (`/provider-profiles`).
2. Wählen Sie **Register profile**.
3. Setzen Sie den Driver (`claude`, `codex` oder `grok`), das vorhandene
   `config_home` und das vorhandene `user_home`. `environment_ref` darf
   entfallen (dieser Knoten).
4. Speichern. Die Liste zeigt `profile_ref`, Driver, Zustand und ob das Profil
   in dieser Umgebung aktiviert ist. Pfade stehen **nicht** in der gewöhnlichen
   Liste.
5. Um gespeicherte Homes zu sehen, verwenden Sie **Reveal configuration**
   (`sessions:profile:admin`). Diese Lesung erfolgt auf Anforderung und wird
   beim Ausblenden verworfen. Sie trägt weiterhin keinen Credential-Wert.

Umbenennen, Deaktivieren und Aktivieren behalten dieselbe ID und dieselben
Homes. **Retire** ist unumkehrbar, wird durch Tippen bestätigt und gibt das Home
für eine **neue** ID frei.

### Was die Engine ablehnt

- Ein Profil, dessen Driver auf diesem Knoten nicht registriert ist, bleibt
  sichtbar und ist nicht startbar (`operable` ist keine Startgarantie;
  `GET …/launch-readiness` ist das Anforderungsfeld).
- Ein Profil, das zu einer anderen Ausführungsumgebung gehört, wird als fremd
  angezeigt und von diesem Knoten nie gestartet.
- Ein Start, der `HOME`, `CLAUDE_CONFIG_DIR`, `CODEX_HOME` oder `GROK_HOME`
  weiterreicht, wird abgelehnt. Diese Namen gehören zum Profil
  (`agentops.create.profileEnvConflict`).

Es gibt keinen Screenshot dieses Bildschirms im veröffentlichten Capture-Satz.
Behandeln Sie kein Bild des Connectors-Tabs als dieses Formular.

## 2. Eine Quelle binden (optional, für beobachtete Attribution)

Eine Quelle kann einem Profil auf der exakten Roster-Revision gewidmet werden,
die dieser Knoten angewendet hat. Der Schlüssel ist die persistente ID der
Roster-Zeile, niemals ihr editierbarer Name (`CHANGELOG.md` `[26.9.0]` B1;
**Source bindings** `/provider-bindings`).

1. Öffnen Sie **Source bindings** (`/provider-bindings`).
2. Binden Sie die persistente `id` der Quelle und die `applied_revision`, die
   der Reconciler dieses Knotens verdrahtet hat. `GET /v1/console/sources`
   meldet beides.
3. Widerrufen Sie die Bindung, um **neue** Profil-Attribution zu stoppen. Ein
   früheres Envelope behält seine historische Bindung beim Replay.

Ohne Bindung erscheint eine bekannte Registrierung weiterhin als
`source`-Beobachtungszeile. Sie wird nicht in einen verwalteten Lauf
zusammengeführt. Siehe
[Live-Betrieb & Sessions](/reference/modules/ii-sessions/).

## 3. Starten

Der produktive Create-Endpunkt verlangt `provider_profile_ref`. Das Weglassen
behält den älteren Request-Body, den diese API ablehnt
(`CHANGELOG.md` `[26.9.0]` B2; CLI-Flag `--provider-profile`).

Der Start-Dialog bietet die **aktiven** Profile. Kein Profil ist vorausgewählt.
Workspace- und Vorlagenauswahl dürfen geleert werden; das Profil nicht
(`CHANGELOG.md` `[26.9.0]` Fixed).

### Konsole

1. Öffnen Sie **Operate sessions** (`/agentops`) oder **Observe sessions**
   (`/sessions`). Sie teilen einen Bildschirm
   ([Konsolenreferenz](/reference/console/)).
2. Öffnen Sie den Start-Dialog.
3. Wählen Sie **Provider profile** (`agentops.create.profile`). Der Hinweis
   sagt, dass ein Profil erforderlich ist.
4. Optional Workspace, Vorlage, Modell und Effort setzen. Modell und Effort
   bleiben provider-eigene offene Strings auf den offiziellen Agent-Flags für
   Grok (`CHANGELOG.md` `[26.9.0]`).
5. Senden Sie **Request launch**. Nur die Profil-**Referenz** wird gepostet.
   Der Server löst die Homes auf.

### CLI

```sh
olivares agent session create --provider-profile <profile_ref>
```

Fügen Sie `--server`, `--tenant` und `--token` (oder den aktiven
Client-Kontext) hinzu wie in der [CLI-Referenz](/reference/cli/). Isolation ist
in diesem Release `native`; `container` und `sandbox` akzeptiert die API und der
Launcher lehnt sie ab, bis diese Runner ausgeliefert sind (generierte
CLI-Hilfe).

Ergebnis: eine Lauf-Ressource. Die verwaltete Live-Zeile ist eindeutig pro
Beobachtungsscope und externer ID. Lesungen, die eine Zeile benennen, verwenden
`live_ref`, nicht die nackte Provider-Session-ID, die zwei Homes teilen können.

## 4. Unterbrechen oder beenden

| Absicht | Konsole | CLI | Ergebnis |
|---|---|---|---|
| Den aktiven Turn beenden, Prozess und Gespräch behalten | Interrupt-Steuerung auf der Live-Session | `olivares agent session interrupt <run-ref>` | der Turn endet; der Prozess bleibt für den nächsten Turn nutzbar (`CHANGELOG.md` `[26.9.0]`) |
| Den Lauf beenden | Stop-Steuerung | `olivares agent session stop <run-ref>` | die Lauf-Ressource; die Runtime räumt das Kind weiterhin ab |

Arbeitsgebundene Läufe senden ihren exakten Lease-Fence. Veraltete oder unsichere
Ergebnisse bleiben explizit. Fortsetzen läuft nur auf demselben nachgewiesenen
Home weiter.

Ein Grok-Interrupt verwendet ACP `session/cancel`, eine Notification ohne
Bestätigung. Der Interrupt löst ausstehende Freigaben, bricht ab und lässt den
Turn offen, bis das eigene korrelierte Ergebnis des Prompts zurückkehrt
(`CHANGELOG.md` `[26.9.0]`). Behandeln Sie ein stilles Cancel nicht als
bestätigten Provider-Empfang.

## Verwandte Themen

- [Ihre erste Stunde](/how-to/first-hour/) — Setup-Token, AAL3, Claude-Credential-Quelle.
- [Claude Code mit Olivares betreiben](/how-to/run-claude-code-with-olivares/) — Co-Deployment-Topologien.
- [Codex integrieren](/how-to/integrations/codex/) / [Grok Build integrieren](/how-to/integrations/grok/) — Connector und PEP-Hook.
- [Live-Betrieb & Sessions](/reference/modules/ii-sessions/) — `live_ref` und Attribution.
- [Konfiguration](/reference/configuration/) — Driver-Pin-Variablen.
- [CLI-Referenz](/reference/cli/) — `olivares agent session *` (aus dem Binary generiert).
