---
title: Upgrade und Rollback
description: >-
  So bringen Sie eine selbst gehostete Olivares-AI-Bereitstellung auf ein neueres
  Release: Plan vorab prüfen, Austausch durchführen, verifizieren und bei Bedarf
  zurückgehen. Behandelt den Self-Service-Befehl `olivares upgrade`, Air-Gap-Bundles
  und den Austausch des Plattform-Images.
---

Ein Upgrade ersetzt das Binary; es migriert Sie nicht auf ein anderes Produkt. Das
Data-Directory, der Audit-Signierschlüssel und das TLS-Material bleiben an ihrem Platz,
und die Engine wendet neue Schema-Migrationen beim Boot selbst an. Diese Seite führt
Betreiber vom „Soll ich dieses Release einspielen?“ bis zum „Ich brauche die vorige
Version zurück“.

:::caution[Zuerst sichern]
Erstellen Sie vor jedem Upgrade ein Backup, auch wenn es routinemäßig aussieht. Sowohl
die Konsole unter **Backups** (`/backups`) als auch
[Sichern und wiederherstellen](/de/how-to/backup-and-restore/) erledigen das. Nichts auf
dieser Seite setzt ein Backup voraus — und spätestens bei der einen unerwarteten
Überraschung werden Sie eines haben wollen.
:::

## Welcher Upgrade-Pfad ist Ihrer?

Es gibt zwei Wege, das Binary vorwärtszubringen; beide führen zum selben Ziel.

| Ihre Installation | Pfad |
|---|---|
| Binary auf einem Host, systemd, Docker Compose | `olivares upgrade` — diese Seite |
| Kubernetes / Helm | Setzen Sie das Image und lassen Sie den Operator den Rollout durchführen. Führen Sie `olivares upgrade` nicht in einem Pod aus: Die Bereitstellung ist deklarativ, und der nächste Reconcile würde die Änderung rückgängig machen. |

## Vor allem anderen: den Plan lesen

`--check` lädt das Channel-Manifest herunter, verifiziert es, vergleicht es mit der
installierten Version und gibt aus, was geschehen würde. Es wird nichts ausgetauscht.

```sh
olivares upgrade --check
```

Die Ausgabe nennt die installierte und die verfügbare Version sowie eine Statuszeile:
`up to date`, `upgrade available`,
`DOWNGRADE (blocked unless --force-rollback)` oder `UNKNOWN`. Lesen Sie die
Statuszeile, statt die beiden Versionsnummern selbst zu vergleichen.

**`UNKNOWN` bedeutet nicht „wahrscheinlich in Ordnung“.** Die installierte Version
konnte nicht ermittelt werden — etwa bei einem Staging-Verzeichnis für eine andere
Architektur, einem `noexec`-Mount oder einem Build aus dem Quellcode. Sowohl der
Anti-Rollback-Schutz als auch das Mindestversions-Gate treffen Aussagen *über* die
installierte Version und können daher nicht ausgewertet werden. Der Befehl verweigert
den Vorgang, statt zu raten. Geben Sie die bekannte Version an; dann bleiben die
Schutzmechanismen aktiv:

```sh
olivares upgrade --check --current-version 26.8.0
```

## Release-Channels

<!-- BEGIN GENERATED olivares-upgrade-channels — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

`olivares upgrade` folgt einem Release-**Channel**. Es gibt **3**; sie sind in
`core/release/manifest.go` nach steigender Stabilität deklariert:

| Wert für `--channel` | Deklariert als |
|---|---|
| `stable` | `release.ChannelStable` |
| `security` | `release.ChannelSecurity` |
| `lts` | `release.ChannelLTS` |

Ein Wert außerhalb dieser Tabelle wird abgelehnt, bevor etwas heruntergeladen wird
(`release.ValidChannel`).

<!-- END GENERATED olivares-upgrade-channels -->

`stable` ist die allgemein verfügbare Linie und der Standard. `security` enthält
ausschließlich außerplanmäßige Sicherheitskorrekturen. Eine Bereitstellung auf diesem
Channel erhält daher Sicherheits-, aber keine Feature-Releases.

:::caution[`lts` wird validiert, aber nicht veröffentlicht]
Die Tabelle oben wird aus den im Code deklarierten Channel-Konstanten erzeugt und listet
daher jeden von `--channel` akzeptierten Wert — einschließlich `lts`. **Es wird kein
`lts`-Manifest erzeugt oder veröffentlicht**. Eine Bereitstellung auf diesem Channel
fragt den Update-Host daher nach einem nicht vorhandenen Objekt. Security-Support gilt
nur für die Vertragslaufzeit und umfasst keine allgemeinen Backports; es gibt keine
eingefrorene Linie. Ansprüche gelten für die bezahlte Laufzeit, ohne erworbenen Fallback
und ohne unbefristetes Recht. Wählen Sie `stable` oder `security`.
:::

Wählen Sie den Channel, der zu Ihrem Betrieb passt, und bleiben Sie dabei:

```sh
olivares upgrade --channel security
```

Ein Security-Release ist im Manifest entsprechend markiert, und `--check` gibt die
behobenen Advisories aus. Auf dem Security-Channel erhalten Sie diese außerhalb der
GA-Linie.

## Upgrade durchführen

```sh
olivares upgrade
```

Der Befehl führt der Reihe nach folgende Schritte aus:

1. Er **lädt das Channel-Manifest herunter und verifiziert dessen Signatur offline**
   gegen den im Build eingebetteten Ed25519-Release-Schlüssel. Der Trust Anchor ist die
   Signatur, nicht der Transport. Bei einem Build ohne eingebetteten Schlüssel müssen
   Sie mit `--pubkey` einen bereitstellen; es gibt keinen unverifizierten Pfad.
2. Er **verweigert den Rückwärtsgang**. Eine ältere als die laufende Version wird nur
   mit `--force-rollback` installiert; der Override erzeugt einen Audit-Eintrag.
3. Er **bindet das Artefakt an den signierten SHA-256 des Manifests**, bevor die Bytes
   ausgeführt werden.
4. Er **prüft den Kandidaten** und tauscht ihn anschließend atomar aus. Dabei bleibt
   ein mit Zeitstempel versehenes Backup des ersetzten Binary erhalten. Läuft das neu
   installierte Binary nicht, stellt der Befehl dieses Backup selbst wieder her.
5. Er **lässt den laufenden Prozess unangetastet**. Der Austausch ändert die Datei auf
   der Platte. Der neue Code übernimmt nach dem Neustart des Dienstes.

Fügen Sie `--yes` hinzu, wenn ein Skript den Vorgang steuert und niemand die
Bestätigungsfrage beantworten kann.

:::note[Kein Hot-Patching]
Ein Go-Binary wird nicht im laufenden Prozess gepatcht. „Zero Downtime“ bedeutet hier
Graceful Drain und Übergabe oder einen Rolling Restart — niemals einen In-Process-Patch.
Ohne Neustart werden hingegen Daten und Konfiguration live wirksam: Sources,
Connectors, Secrets, Policy und Lizenz.
:::

## Air-Gap-Installationen

Eine Air-Gap-Bereitstellung erreicht niemals einen Update-Host. Übertragen Sie das
Bundle mit einem bereits vertrauenswürdigen Verfahren und installieren Sie es aus der
lokalen Datei. Die Verifikation ist identisch, denn dem Netzwerk wurde nie vertraut.

**Die Installation aus einem Bundle benötigt eine aktive Lizenz auf dem System.**
Sie wird offline gegen den in Ihrem Binary eingebetteten Lizenzschlüssel geprüft. Es
erfolgt kein Aufruf, daher funktioniert das hinter dem Air Gap. Falls Sie Ihre Lizenz noch
nicht auf dem System installiert haben, finden Sie die Schritte auf der Seite
[Eine Lizenz installieren und zu Enterprise wechseln](/de/how-to/install-a-license/).
`--check` ist nicht
gegatet; Sie können ein Bundle vor jedem Staging verifizieren:

```sh
olivares upgrade --bundle ./olivares-release.tar.gz --check   # verify only; no license read
olivares upgrade --bundle ./olivares-release.tar.gz --yes     # install; needs a live license
```

Enthält Ihr Build keinen eingebetteten Release-Schlüssel oder spiegeln Sie Releases
unter Ihrem eigenen Signierschlüssel, verweisen Sie auf den Schlüssel, gegen den
verifiziert werden soll:

```sh
olivares upgrade --bundle ./olivares-release.tar.gz --pubkey @/etc/olivares/release.pub
```

[Air-Gap-Installation](/de/how-to/air-gap-install/) beschreibt, wie das Bundle erzeugt
und übertragen wird.

## Gestaffelter Rollout und unbeaufsichtigte Prüfungen

Ein Manifest kann eine Kohorte für den gestaffelten Rollout benennen, sodass ein
Release zunächst nur einen Teil der Estate erreicht. Mit `--if-eligible` handelt ein
Node nur dann, wenn er dieser Kohorte angehört; andernfalls geschieht nichts:

```sh
olivares upgrade --if-eligible --yes
```

In dieser Form läuft der eingebaute Timer. Um einen systemd-Timer samt Dienst
auszugeben, der den Befehl innerhalb eines Wartungsfensters aufruft:

```sh
olivares upgrade --install-timer --timer-schedule 'Sun *-*-* 03:00:00'
```

Standardmäßig gibt der Befehl die Units aus; `--timer-dir` schreibt sie in das von
Ihnen angegebene Verzeichnis. Das ist opt-in — nichts plant sich selbst ein.

Die Konsole bietet die schreibgeschützte Hälfte derselben Information:
**Settings → update status** ruft `POST /v1/console/update-check` auf und führt bei
Bedarf eine Prüfung gegen den konfigurierten Channel aus. Eine Air-Gap-Bereitstellung
oder eine ohne konfigurierten Channel antwortet mit `501` und nennt den Grund, statt
zu melden, es gebe kein Update.

## Upgrade verifizieren

```sh
olivares version
olivares upgrade --check
```

`--check` sollte jetzt `up to date` melden. Prüfen Sie anschließend den Dienst
selbst: in der Konsole unter **Health** (`/health`) oder über den Readiness-Endpunkt
der Engine aus [Mit Prometheus überwachen](/de/how-to/monitor-with-prometheus/).

## Rollback

Das vorige Binary bleibt neben seinem Ersatz erhalten, und der Befehl gibt beim
Austausch den Pfad aus. Für den Rollback stellen Sie diese Datei wieder her und starten
den Dienst neu.

Rollback ist konstruktionsbedingt sicher: Jede Schemaänderung wird zuerst als additive
Expand-Phase ausgeliefert, ihr destruktiver Contract erst in einem späteren Release.
Das Binary des vorigen Releases funktioniert daher weiterhin mit dem aktualisierten
Schema. Deshalb bedeutet Rollback „altes Binary zurücklegen“ und nicht „Datenbank
zurückmigrieren“.

Müssen Sie statt des aufbewahrten Backups ein älteres Release installieren, blockiert
der Anti-Rollback-Schutz den Vorgang bis zu Ihrem ausdrücklichen Override:

```sh
olivares upgrade --force-rollback --yes
```

Der Override wird im Audit-Log aufgezeichnet. Das Mindestversions-Gate lässt sich
dadurch **nicht** umgehen: Deklariert ein Manifest eine Untergrenze oberhalb Ihrer
installierten Version, führen Sie das Upgrade über das genannte Zwischenrelease aus,
statt die Stufe zu überspringen.

## Wenn etwas schiefgeht

| Symptom | Bedeutung | Maßnahme |
|---|---|---|
| `--check` gibt `UNKNOWN` aus | Die installierte Version konnte nicht ermittelt werden, daher ist keine Aussage über die Reihenfolge möglich | Übergeben Sie mit `--current-version` die bekanntermaßen installierte Version |
| `min_ver` meldet eine zu alte Version | Das Release verweigert die direkte Installation über Ihrer Version | Aktualisieren Sie zuerst auf das genannte Zwischenrelease |
| Das neue Binary startet nicht | Die Prüfung nach dem Austausch ist fehlgeschlagen | Das Backup wurde bereits automatisch wiederhergestellt; prüfen Sie die Logs und melden Sie das Release |
| `--install-timer` löst aus, aber nichts geschieht | Der Node gehört nicht zur Kohorte des gestaffelten Rollouts | Mit `--if-eligible` ist das erwartet; die Kohorte wird im Verlauf des Rollouts erweitert |
| "another olivares upgrade is already installing", exit **5** | Je Binary kann nur ein Upgrade laufen. Die Sperre gilt für die gesamte Download-und-Austausch-Sequenz | Warten Sie auf den laufenden Vorgang und starten Sie erneut. Läuft nichts mehr, hat der Kernel die Sperre bereits freigegeben; starten Sie jetzt erneut |
| "it CHANGED while this upgrade was downloading" | Nach der Planung hat etwas anderes das Binary ersetzt — Paketmanager, Image-Rollout oder Konfigurationsverwaltung | Starten Sie erneut: Die Schutzmechanismen werden gegen die tatsächlich installierte Datei neu ausgewertet. Wiederholt sich das, verwalten zwei Systeme dasselbe Binary |

**Ein Upgrade-Agent pro Binary.** `olivares upgrade` hält während der gesamten
Prepare-Download-Swap-Sequenz eine exklusive Sperre auf dem Ziel. Ein zweiter Lauf
beendet sich daher mit `5`, statt zu installieren. Installieren Sie **einen** Timer
und ändern Sie darin `--channel`, statt je Channel einen Timer zu betreiben: Zwei
Installationen, die in derselben Sekunde endeten, konnten früher das Rollback-Backup
der jeweils anderen überschreiben; der automatische Rollback des Verlierers stellte
dann das *andere* Binary wieder her und meldete Erfolg. Unmittelbar vor dem Austausch
liest der Befehl außerdem die Bytes des Ziels erneut und verweigert den Vorgang, wenn
sie nicht der eingeplanten Datei entsprechen. Anti-Rollback- und
Mindestversionsurteile beziehen sich auf eine konkrete installierte Datei.

Für alles Weitere ist [Fehlerbehebung](/de/how-to/troubleshooting/) der allgemeine
Einstieg; die Konsole zeigt unter **Logs** (`/logs`) den eigenen Log-Stream der Engine.
