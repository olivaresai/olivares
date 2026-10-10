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

Quelle für dieses Verhalten: Abschnitt `[26.9.0]` in `CHANGELOG.md`
(Provider-Profile, Codex-Driver, Grok-Driver), die generierten Referenzen zu
[Konsole](/reference/console/) und [Konfiguration](/reference/configuration/),
`cmd/olivares/sessionruntime.go` und `web/src/features/agentops/types.ts`.

## Voraussetzungen

Erfüllen Sie diese Punkte vor einem Start. Ein fehlender Punkt ist eine
Ablehnung, kein Fallback.

1. Olivares AI ist installiert und der erste Administrator existiert.
   Siehe [Ihre erste Stunde](/de/how-to/first-hour/) für das Setup-Token.
   Die zusätzliche Authentifizierung für administrative Aktionen (`admin_step_up`)
   ist standardmäßig auf `none` gesetzt. Aktiviert ein Administrator `totp` oder
   `passkey`, erfüllen Sie diese Richtlinie vor privilegierten Operationen
   (`core/api/middleware.go` `requireStepUp`).
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
4. Für den Driver muss auf **diesem Knoten** ein Executable verfügbar sein.
   Ein explizit gepinntes Binary hat Vorrang; andernfalls ermittelt die Engine
   das Executable beim Start. Bereitschafts- und Richtlinienprüfungen gelten
   weiterhin pro Driver.

| Driver | Diese Umgebungsvariable pinnen | Wenn sie nicht gesetzt ist |
|---|---|---|
| Claude Code | `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN` | Neueste verifizierte verwaltete Installation, danach `claude` im `PATH` der Engine. |
| Codex | `OLIVARES_SESSION_RUNTIME_CODEX_BIN` | Neueste verifizierte verwaltete Installation, danach `codex` im `PATH` der Engine. |
| Grok | `OLIVARES_SESSION_RUNTIME_GROK_BIN` | Neueste verifizierte verwaltete Installation, danach `grok` im `PATH` der Engine. |

Der Wert pinnt das offizielle Executable, das dieser Knoten betreiben darf.
Ohne Pin ist ein installiertes Tool ohne Neustart der Engine verfügbar.
Fehlen sowohl eine verwaltete Installation als auch ein Executable in `PATH`,
wird der Start verweigert. `OLIVARES_SESSION_RUNTIME_OPENCODE_BIN` folgt
derselben Suchreihenfolge.

Für Claude-Profile mit `managed_injection`, die keinen Anbieter nennen, liefert
`OLIVARES_SESSION_RUNTIME_WIF` oder `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` die
Inferenz-Zugangsdaten des Hosts. Ein Profil mit einem gebundenen Anbieter nutzt
dessen Zugangsdaten; bei einem Fehler wird der Start ohne Fallback verweigert.
Ein Profil mit `provider_account_home` nutzt die autorisierte Anmeldung des Tools
und benötigt keine der beiden Variablen. Siehe
[Anbieter hinzufügen](/de/how-to/add-a-provider/).
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

Der Start-Dialog bietet die **aktiven** Profile. Das einzige aktive Profil ist
vorausgewählt; bei mehreren ist keines vorausgewählt, und **Start** fordert zur
Auswahl auf. Workspace- und Vorlagenauswahl dürfen geleert werden; das Profil
nicht (`CHANGELOG.md` `[26.9.0]` Fixed).

### Konsole

1. Öffnen Sie **Sitzungen** (`/sessions`). `/agentops` öffnet denselben Bildschirm
   ([Konsolenreferenz](/reference/console/)).
2. Öffnen Sie **New session** und dann **Advanced launch options** (unter
   **More options**, wenn ein Werkzeug bereit ist).
3. Prüfen Sie **Provider profile** (`agentops.create.profile`) und schreiben
   Sie bei Bedarf die **First message**.
4. Optional Workspace, Vorlage, Modell und Effort unter **Advanced options**
   setzen. Modell und Effort bleiben provider-eigene offene Strings auf den
   offiziellen Agent-Flags für Grok (`CHANGELOG.md` `[26.9.0]`).
5. Klicken Sie auf **Start**. Solange es nicht starten kann, sagt die Zeile
   darunter, was fehlt. Nur die Profil-**Referenz** wird gepostet. Der Server
   löst die Homes auf.

Die Auswahl **Ordner** legt fest, wo das Tool arbeitet. **Temporärer Ordner für
diese Sitzung** gibt dem Lauf ein eigenes Verzeichnis; ein registrierter Ordner
verwendet diesen Ordner. Dies ist vom Workspace getrennt, der die Sitzung
autorisiert. **Kontext → Details** zeigt gespeicherten Workspace und Ordner;
der gewöhnliche temporäre Ordner ist keine Warnung.

### Ein eigener Git-Worktree (optional)

Standardmäßig arbeitet die Sitzung im ausgewählten Ordner. Ist dies der oberste
Ordner eines Git-Repositorys, können Sie einen **neuen Git-Worktree** anfordern:
Aktivieren Sie **In einem neuen Git-Worktree arbeiten** unter **Ordner**, oder
führen Sie `olivares session start . --worktree` aus. Die Sitzung arbeitet auf
einem neuen Branch (`olivares/` plus acht Zeichen ihrer ID) in ihrem eigenen
Worktree; zwei Sitzungen teilen so keine Dateien. Mergen Sie diesen gewöhnlichen
Repository-Branch wie üblich aus Ihrem eigenen Checkout.

- Worktrees liegen unter `<data directory>/session-worktrees`.
  `OLIVARES_SESSION_WORKTREE_DIR` verschiebt sie und
  `OLIVARES_SESSION_WORKTREE_BRANCH_PREFIX` ändert das Branch-Präfix.
- **Fortsetzen** verwendet denselben Worktree. Wurde nur sein Verzeichnis gelöscht,
  stellt die Engine ihn auf demselben Branch wieder her.
- **Aufräumen**, **Löschen** und `olivares session rm` entfernen Worktree und Branch,
  wenn der Branch in den aktuellen Workspace-Branch gemergt ist, der Worktree auf
  diesem Branch steht und keine uncommitted Dateien hat. Sonst wird die Freigabe
  mit 409 verweigert und nennt den möglichen Verlust: ungemergte Arbeit, detached
  HEAD, ein anderer Branch oder ein unerreichbarer Worktree. Aktivieren Sie
  **Auch Worktree und Branch verwerfen** oder fügen Sie `--discard-worktree` hinzu,
  um dennoch fortzufahren; bei unerreichbarem Worktree wird die Sitzung freigegeben
  und der Worktree bleibt liegen. Von Git ignorierte Dateien wie Build-Ausgaben
  werden mit dem Worktree entfernt.
- Die Option wird vor jeder Erstellung mit 422 verweigert für Ordner außerhalb
  der Repository-Wurzel, Repositorys ohne Commit, schreibgeschützte Workspaces oder
  Ordner, nicht-native Isolation und Git-Konfigurationen mit Filtern
  (`filter.<name>.clean`, `smudge`, `process`) oder Datei-Includes. Sitzungen ohne
  diese Option bleiben unverändert.
- Die Engine führt Git mit deaktivierten Repository-Hooks und Dateisystemmonitor,
  ohne Ihre Git-Konfiguration sowie mit Zeit- und Ausgabelimit aus. Ein dort
  konfigurierter Git-LFS-Filter wird daher nicht angewendet: LFS-Dateien erscheinen
  als Pointer-Dateien. Eine Sitzung kann das gemeinsame Git-Verzeichnis des
  Repositorys beschreiben; alle Sitzungen teilen es. Ein Worktree isoliert Dateien,
  nicht Git-Metadaten.

### Die im Handoff benannte Arbeit öffnen

Ein Handoff kann die Git-Position seiner Arbeit angeben: Sein Inhalt akzeptiert
optional `branch` und `sha` (eine vollständige Commit-ID). Bieten Sie ihn über die
API oder `olivares message handoff offer --context-file` an; dessen JSON kann beide
enthalten. Ein Handoff ohne diese Angaben bleibt unverändert.

Beim Lesen zeigt das Panel Branch und Commit als Text. **In einem neuen Sitzungs-
Worktree öffnen** öffnet den Startdialog mit ausgewähltem Worktree und sichtbarem
Startpunkt. Der Start wartet, bis Sie den Workspace wählen, dessen Repository den
Commit enthält. Das Zurücksetzen der Ordnerauswahl behält die Anforderung bei; deaktivieren
Sie den Worktree ausdrücklich, um stattdessen eine gewöhnliche Sitzung zu starten.
In der Kommandozeile:

```sh
olivares session start . --worktree-from <commit or branch> --name review
```

`--worktree-from` impliziert `--worktree`. Die Sitzung arbeitet auf ihrem eigenen
neuen Branch an diesem Commit; der Branch des Absenders und Ihr Checkout bleiben
unverändert. Fehlt der Commit im Workspace-Repository, wird der Start mit 422
verweigert, bevor etwas angelegt wird: fetchen Sie ihn zuerst dorthin. Auch ein
Commit, den kein Branch, Tag oder Remote-Branch hält, wird verweigert. **Branch-
Änderungen** zeigt anschließend, was der Sitzungsbranch gegenüber dem aktuellen
Workspace-Commit enthält, und öffnet den Text eines Pfads an der Merge-Basis neben
dem Text an der Branch-Spitze. Sichtbar ist nur committed Arbeit innerhalb der
erlaubten Workspace-Unterpfade und DLP-Posture; uncommitted Änderungen stehen in
**Änderungen**.

### CLI

```sh
olivares agent session create --provider-profile <profile_ref>
```

Fügen Sie `--server`, `--tenant` und `--token-file` (oder den aktiven
Client-Kontext) hinzu wie in der [CLI-Referenz](/reference/cli/). Isolation ist
in diesem Release `native`; `container` und `sandbox` werden vor dem Anlegen
eines Runs mit HTTP 422 abgelehnt. Wählen Sie `native` für den integrierten Runner.

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

- [Ihre erste Stunde](/de/how-to/first-hour/) — Setup-Token, zusätzliche administrative Authentifizierung, Claude-Credential-Quelle.
- [Claude Code mit Olivares betreiben](/how-to/run-claude-code-with-olivares/) — Co-Deployment-Topologien.
- [Codex integrieren](/how-to/integrations/codex/) / [Grok Build integrieren](/how-to/integrations/grok/) — Connector und PEP-Hook.
- [Session-Runtime-API](/reference/session-runtime-api/) — Listen, Attach, Input, Stop; Community-PTY und Editionsgrenze.
- [Live-Betrieb & Sessions](/reference/modules/ii-sessions/) — `live_ref` und Attribution.
- [Konfiguration](/reference/configuration/) — Driver-Pin-Variablen.
- [CLI-Referenz](/reference/cli/) — `olivares agent session *` (aus dem Binary generiert).
