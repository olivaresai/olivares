---
title: "Modul II — Live-Betrieb & Sessions"
description: >-
  Das Live-Betriebs-Overlay pro Agent-Session: aktuelle Aktion, Live-Tokens/
  -Kosten, ein abgeleiteter Claude-Code-State und eine abspielbare Timeline,
  gestreamt über Server-sent Events. Was es ableitet, was ehrlich leer bleibt
  und die Grenzen.
---

Modul II ist die **Live-Betriebs**-Sicht des Estate: was jede Agent-Session
gerade tut, ihre Live-Token- und Kostensummen, ein abgeleiteter Claude-Code-State
und eine rekonstruierbare Timeline. Während Modul I (Inventar) das dauerhafte
Estate materialisiert, hält Modul II ein **Live-Betriebs-Overlay** pro Session
über demselben Beobachtungsstrom — und zeigt nur, was dieser Strom ehrlich
trägt.

Olivares <!-- release -->0.9<!-- /release --> **startet** auch offizielle Anbieter-CLIs als eigene Kinder unter einem
[Anbieterprofil](/how-to/operate-provider-sessions/). Dieser verwaltete Pfad
ist dasselbe Modul. Er ersetzt das Overlay nicht und führt zwei Homes, die
dieselbe Anbieter-Session-ID bekanntgeben, nicht zusammen
(`CHANGELOG.md` `[26.9.0]` B1/B2).

## Was es ist

Modul II ist ein busgetriebenes Modul der Core-Schicht, Geschwister des
Inventars. Es pflegt einen Live-Datensatz, der nach der externen Referenz jeder
Session gekeyt ist, aufgebaut aus dem kooperativen Beobachtungsstrom — nie
gepollt, nie erfunden. Pro Session verfolgt es:

- die **aktuelle Aktion** (das zuletzt verwendete Tool) und die Ressource/den
  Modus, die sie berührte;
- die **Live-Token- und Kostensummen**, gelesen aus Kostenproben (das kanonische
  Kosten-Ledger und FinOps sind Modul XI, nicht hier — dies ist nur die
  Live-Zahl);
- einen **abgeleiteten Claude-Code-State** (`cc_state`); und
- eine **Timeline**, an die jedes beobachtete Event in Ingest-Reihenfolge
  angehängt wird.

## Sein Vertrag & seine Entitäten

Das Modul registriert zwei mandantenbezogene Entitäten. `sessions.live` hält den
Live-Datensatz pro Session — aktuelle Aktion/Ressource/Modus, Modellreferenz,
Live-Input/-Output-Tokens, Live-Kosten, Event- und Tool-Call-Zähler sowie
First/Last-Event-Zeitstempel. `sessions.timeline` hält eine abspielbare Zeile pro
Event, geordnet nach Ingest. Es gibt **keine gespeicherte
Lebenszyklus-Spalte**: Der kooperative Strom trägt kein Ende-oder-Fehler-Signal,
sodass das einzige ehrliche Lebendigkeitssignal der abgeleitete `cc_state` ist.

`cc_state` wird **zur Lesezeit** aus der Aktualität der Events abgeleitet —
`active` / `idle` / `ended` — und wechselt in einen Silent-Evasion-State, wenn
der Connector dieses Finding meldet (es wird nie vom Modul selbst geschrieben).
Reads werden unter Modulrouten bedient (Live-Liste, einzelne Session,
Session-Timeline) plus einem Live-SSE-Stream; jeder Read erfordert die
Session-Leseberechtigung, und **das Öffnen des Streams wird automatisch
auditiert**. Der SSE-Kanal ist streng **mandantenisoliert** (ein Client erhält
nur Snapshots für seinen autorisierten Mandanten) und **Best-Effort** (ein
langsamer Client verwirft den Zwischenframe und erhält den nächsten — die
Ingestion blockiert nie).

## Provider profiles and `live_ref`

Ein **Provider-Profil** ist die dauerhafte Identität einer konfigurierten
Provider-Instanz auf einer Ausführungsumgebung: Treiber, Umgebung und das
kanonische `config_home` / `user_home`. Es ist kein authentifiziertes
Provider-Konto. Registrieren, umbenennen, deaktivieren/aktivieren und
zurückziehen leben unter `/v1/m/sessions/provider-profiles`. Pfade erscheinen
nur auf dem Admin-`configuration`-Read. Ein Start nennt `provider_profile_ref`;
der Server löst die Homes auf und speichert vor dem Spawn einen nicht-geheimen
Snapshot auf dem Run (`CHANGELOG.md` `[26.9.0]` B1;
`web/src/features/agentops/types.ts`).

Eine Beobachtung fällt in die Live-Zeile ihres **Kanals**, den der Server aus
der host-gestempelten Quellenregistrierung berechnet (`CHANGELOG.md` `[26.9.0]`
B2):

| Channel | Meaning |
|---|---|
| `legacy` | no registration |
| `observed` | a source dedicated to a profile by a binding approved at host admission for the exact applied revision |
| `source` | a known registration with no verifiable profile |
| `managed` | a run the plane launched; the only row carrying `canonical_sid` and `run_ref` |

Jede Live-Zeile stellt `live_ref` und `attribution` bereit. Zwei Homes, die
dieselbe Provider-Session-ID bekanntgeben, sind zwei Zeilen mit zwei Timelines.
Lesen Sie eine Zeile mit `GET /v1/m/sessions/live/by-id/{live_ref}` (und ihrer
Timeline- / Stream- / Runs-Abfrage). Nackte External-ID-Routen bleiben und sind
**Legacy**: sie antworten nur für die Legacy-Zeile.

Treiber werden beim Start auf jedem Knoten registriert. Beim Sitzungsstart
verwendet die Engine die neueste verifizierte verwaltete Installation, danach
die CLI im `PATH` der Engine. `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN`, `_CODEX_BIN`,
`_GROK_BIN` und `_OPENCODE_BIN` überschreiben diese Auflösung ausdrücklich
(siehe [Konfiguration](/reference/configuration/)). Wird keine Programmdatei
gefunden, wird der Start verweigert. Weitere Bereitschaftsprüfungen, einschließlich
Profil-Authentifizierung und Startrichtlinie, gelten weiterhin. Operator-Schritte:
[Eine Provider-Session betreiben](/how-to/operate-provider-sessions/).

## Lebenszyklus verwalteter Läufe und Neustartgrenzen

Sessions ist ein Kernmodul: Es läuft auch dann weiter, wenn es nicht in der gespeicherten
Modulauswahl steht. Das Ausschalten eines optionalen Moduls setzt keine Sitzungsdaten zurück.

Die Managed-Run-API unter `/v1/m/sessions/runs` und `olivares session` verwenden dieselbe
Runtime. Liste und Metadaten erfordern `sessions:run:read`; Inhalte erfordern
`sessions:output:read`, das Editor und höhere Rollen standardmäßig besitzen. Start, Eingabe
und Resume erfordern `sessions:run:write`, Stopp erfordert `sessions:stop:write`. Viewer
behalten Liste und Metadaten, verlieren aber Inhalte ohne ausdrückliche Freigabe.
Mandanten- und Workspace-Autorität gelten weiterhin für jeden Lauf.

```sh
olivares session ls -o json
olivares session show <session> -o json
olivares session send <session> "continue the task"
olivares session stop <session>
olivares session resume <session>
```

Eine Nachricht kann Bilder enthalten. `olivares session send <session> --attach shot.png "what
is wrong here?"` und das Eingabefeld der Konsole (Einfügen, Ablegen oder die Büroklammer)
speichern jedes PNG, JPEG, WebP oder GIF bei der Sitzung, bis zu vier pro Nachricht und je
3.75 MB, und senden sie dann mit dem Text; eine Nachricht braucht ihren Text, daher wird ein
Bild allein nicht gesendet (die CLI und das Eingabefeld fordern ihn an). Claude Code erhält
einen Bild-Content-Block und Codex seine eigene Bildeingabe; ein Tool ohne Bildeingabe lehnt
die Nachricht namentlich ab, bevor etwas gesendet wird. Die API ist
`POST /v1/m/sessions/runs/{ref}/attachments` (das Bild, base64), `GET .../attachments/{id}` und
`attachments` auf `.../input`. Bilder liegen unter dem Datenverzeichnis, je Mandant und Sitzung,
und werden mit der Sitzung entfernt.

Gestoppte Läufe, ihre Referenzen auf Provider-Profil und Workspace sowie Lebenszyklus-Events
überstehen einen Neustart der Engine. Resume startet einen neuen eigenen Kindprozess für den
bestehenden Lauf. Die Live-Attach-Ausgabe ist ein begrenzter In-Memory-Ring; gespeicherte
Lauf-Metadaten und Lebenszyklus-Events versprechen kein persistiertes Gesprächsprotokoll.
Aufzeichnungsrichtlinie und die eigene Resume-Unterstützung des Tools bleiben getrennte
Anforderungen.

## Session-Ordner auf NFS- oder SMB-Freigaben

Ein Session-Ordner ist ein Pfad auf dem Engine-Host, daher wird eine Netzwerkfreigabe zu einem Ordner, sobald der Host sie eingebunden hat. `olivares netfolder add` erledigt das mit den eigenen Mount-Units von systemd und registriert den Mount-Punkt als Ordner; das Einbinden bleibt Sache von systemd, und die Units sind einfache Dateien, die Sie lesen und bearbeiten können. Führen Sie den Befehl als root auf dem Engine-Host aus:

```sh
printf '%s\n' "$SMB_PASSWORD" | sudo olivares netfolder add files //fileserver/projects --user svc --password-stdin
sudo olivares netfolder add data fileserver:/export --mode write_no_delete
olivares netfolder ls
```

Die Quelle ist `//server/share` für SMB oder `server:/export` für NFS, so wie `mount(8)` sie schreibt. Der Name besteht aus Kleinbuchstaben und Ziffern und benennt den Mount-Punkt `/srv/olivares/mnt/<name>`. `--mode` ist der Zugriffsmodus des Ordners (standardmäßig `rw`). Die Units nennen das Konto `olivares` als Eigentümer jeder Datei, die ein SMB-Ordner anzeigt; daher muss die Engine unter diesem Konto laufen. `ls` zeigt jeden Ordner als `mounted`, `unmounted` (er wird beim ersten Zugriff eingebunden; `ls` löst das nicht aus) oder `not responding`. Ein vorhandener Ordner wird nie überschrieben.

Für das SMB-Beispiel schreibt der Befehl diese Units, gemessen auf Debian 13 (Linux 6.12) gegen einen NFSv4.2-Export und eine Samba-4.22-Freigabe (SMB 3.1.1). Die Dateinamen sind das, was `systemd-escape -p /srv/olivares/mnt/files` ausgibt, mit angehängtem `.mount` und `.automount`.

```ini
# /etc/systemd/system/srv-olivares-mnt-files.mount
[Unit]
Description=Olivares network folder files
After=network-online.target
Wants=network-online.target

[Mount]
What=//fileserver/projects
Where=/srv/olivares/mnt/files
Type=cifs
Options=credentials=/etc/olivares/netfolders/files.cred,uid=olivares,gid=olivares,vers=3.1.1,nodev,nosuid,soft,echo_interval=5
TimeoutSec=30
```

```ini
# /etc/systemd/system/srv-olivares-mnt-files.automount
[Unit]
Description=Olivares network folder files (mounted on first use)

[Automount]
Where=/srv/olivares/mnt/files
TimeoutIdleSec=0

[Install]
WantedBy=remote-fs.target
```

Für NFS schreibt er `Type=nfs4`, `What=fileserver:/export` und `Options=nodev,nosuid,vers=4.2,soft,timeo=50,retrans=2` und keine Zugangsdatei. Auf einem Host, der SELinux mit der Richtlinie der Appliance erzwingt, ergänzt er die Optionen um `context=system_u:object_r:olivares_workspace_t:s0`, das Label, das die Engine und ihre Session-Werkzeuge für einen Ordner bereits verwenden. Um einen Ordner zu entfernen, führen Sie `systemctl disable --now` für seine beiden Units aus, löschen diese und seine Zugangsdatei und melden den Ordner ab.

- **Alle, die den Ordner haben, handeln als ein Konto.** Dateien werden als das Konto gelesen und geschrieben, das der Mount vorlegt: `uid=` und `gid=` bei SMB, die Benutzerzuordnung des Exports bei NFS. Die Freigabe unterscheidet Personen nicht. Olivares entscheidet, wer den Ordner erhält.
- **Der Ordner einer Person liest als diese Person.** `--owner <Benutzer-ID>` registriert einen SMB-Ordner als den eigenen einer Person, wie einen persönlichen Ordner: Sein Inhalt gehört nur ihr, und Administratoren finden den Ordner weiterhin, um ihn zu verwalten. Geben Sie mit `--user` und dem Passwort das eigene SMB-Login dieser Person an, dann wendet der Dateiserver ihre Rechte an: Zwei Personen, die dieselbe Freigabe einbinden, sehen jeweils, was ihr eigenes Konto öffnen darf. Legen Sie pro Person einen Ordner mit eigenem Namen an. NFS unterscheidet Personen nicht und nimmt keinen Besitzer. Wer administriert, gibt das Passwort der Person einmal ein, und es bleibt wie das des gemeinsamen Kontos in der nur für root lesbaren Zugangsdatei; die Sitzung der Person enthält es nie. Ein Kerberos-Ticket pro Sitzung, damit die Person das Passwort niemandem anvertrauen muss, ist nicht gebaut. Geprüft mit zwei Konten einer Samba-AD-Domäne auf einer Freigabe: Der Ordner jeder Person listete ihr eigenes Verzeichnis und das gemeinsame, las ihre eigene Datei, und der Dateiserver verweigerte die Datei der anderen Person.
- **Die Zugangsdaten bleiben in einer Datei.** Der Befehl schreibt `username=` und `password=` in die Datei `credentials=`, im Besitz von root mit Modus 0600, aus dem Passwort auf der Standardeingabe. Das Passwort erscheint in keiner Unit, keiner Mount-Option, keinem Prozessargument und keiner Journal-Zeile: `mount.cifs` erhält nur den Pfad. NFS mit `sec=sys` braucht kein Zugangsdatum.
- **Die Automount-Unit hält den Start unabhängig vom Dateiserver.** Beim Booten wartet nichts auf die Freigabe. Die erste Verwendung bindet sie ein; ist der Server in diesem Moment nicht erreichbar, scheitert die Verwendung, nicht der Start.
- **Verwenden Sie `soft`.** Ein `hard`-NFS-Mount, der Standard, lässt einen Prozess, der einen toten Server berührt, an einer Stelle hängen, an der er sich nicht beenden lässt. Mit den oben genannten Optionen schlägt ein Lesen oder Schreiben auf einer eingebundenen Freigabe nach 20 bis 30 Sekunden mit einem Ein-/Ausgabefehler oder „Host is down“ fehl, und die Freigabe funktioniert wieder, sobald der Server antwortet.
- **Ein Ordner, der nicht antwortet, wird gemeldet.** Das Registrieren, Öffnen, Auflisten und Starten einer Sitzung darin wartet jeweils 10 Sekunden auf die Freigabe und lehnt dann mit „The folder ... is not responding. If it is on a network share, check that its file server is up and reachable.“ und dem Status 424 ab. Die Sitzung startet nicht.
- **`write_no_delete` gilt.** Landlock erzwingt sie in der Dateisystemschicht des Kernels, oberhalb des Netzwerkdateisystems. Bei einem echten Sitzungsstart auf beiden Freigaben hat eine Sitzung Dateien angelegt und geändert und wurde beim Umbenennen, Entfernen (unlink) und rmdir abgewiesen, genau wie auf einer lokalen Platte; `rw` hat sie erlaubt. Ein Host, der sich nicht einschränken kann, verweigert den Start weiterhin.
- **Der Ordner ist die Grenze.** Bei denselben Starts wurde in beiden Modi ein Schreibzugriff auf das übergeordnete Verzeichnis des Ordners, das auf derselben Freigabe liegt, abgewiesen und hinterließ keine Datei. Die Sitzung schreibt nur innerhalb ihres Ordners.

## Was es konsumiert (und was es ableitet)

Modul II konsumiert denselben Minimal-Data-Beobachtungsstrom wie das Inventar —
[`edge.observed`](/de/reference/events/), `cost.sampled` und `finding.reported`. Nur
Edges, deren Herkunft eine **Session** ist, erzeugen Live-Betrieb; an eine
Session gebundene Kostenproben tragen zur Live-Token-/Kostenzahl bei (hier wird
kein `CostRecord` geschrieben); Findings mit Session-Subjekt werden annotiert,
und ein Anti-Evasion-Finding markiert den Evasion-State. Zwei Felder werden
**live abgeleitet** aus denselben Signalen: `agent_ref` aus dem einer Session
zugeordneten Agent und `summary` aus einem Context-Compaction-Finding
(forensisch), dessen Titel per Vertrag summary-safe ist — niemals eine vom LLM
erfundene Zusammenfassung.

:::caution[Ehrliche Grenzen]

- **`goal` bleibt leer — ehrlich.** Der kooperative Strom ist Minimal-Data und
  trägt das Ziel oder die Aufgabenliste einer Session **nicht**; sie werden am
  Connector bereinigt, und es gibt keinen In-Process-Prompt-Text auf der
  Leitung. Der Live-Datensatz modelliert das Feld, damit Vertrag und UI bereit
  sind und ein künftiger Metadatenkanal es befüllen kann, aber das Modul
  **erfindet es nie**.
- **Kein gespeicherter Lebenszyklus.** Der Strom hat kein Ende/Fehler-Signal,
  sodass die Lebendigkeit einer Session der **abgeleitete** `cc_state` nach
  Aktualität ist — kein persistierter Status. Ein `ended`-State bedeutet *keine
  kürzlichen Events*, kein bestätigtes sauberes Herunterfahren.
- **Die Live-Zahl ist nicht das Ledger.** Live-Tokens/-Kosten sind eine
  betriebliche Ablesung aus Kostenproben; der maßgebliche, abstimmbare
  Kostendatensatz ist das FinOps-Ledger von Modul XI. Behandeln Sie die
  Live-Zahl nicht als Abrechnungswahrheit.
- **Minimal-Data ist eine Eigenschaft der Leitung.** Nur Referenzen,
  Klassifizierungen und Lebendigkeits-/Kostenzähler werden getragen und
  persistiert — niemals Payloads, Prompts, Befehle oder PII.
:::

## Verwandt

- [Event-Bus-Referenz](/de/reference/events/) — die Events `edge.observed`,
  `cost.sampled` und `finding.reported`, die dieses Modul konsumiert.
- [Modulkatalog](/de/reference/modules/overview/) — wo Modul II einzuordnen ist und
  die ehrliche Actuate-Aufteilung.
- [Access- & Ressourcen-Map](/de/reference/modules/iii-access-map/) — das
  Geschwister-Core-Modul, das den R/RW-Access-Graphen besitzt.
- [Architekturüberblick](/de/explanation/architecture/overview/) — die Engine und die Schichten.
- [Claude Code anbinden](/de/how-to/connect-claude-code/) — den Live-Stream zu erzeugen beginnen.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — was das Produkt heute tut und was nicht.
