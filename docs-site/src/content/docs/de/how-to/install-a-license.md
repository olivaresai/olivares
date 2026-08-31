---
title: Eine Lizenz installieren und zu Business wechseln
description: >-
  Wohin eine erworbene Lizenz gehört, wie Sie sie ohne Neustart der Engine installieren,
  wie Sie die installierte Lizenz prüfen und wie der Community-→-Business-Austausch
  in-place erfolgt. Die Ed25519-Verifizierung ist offline — kein Netzwerkaufruf
  begründet eine Berechtigung.
---

Sie haben einen Plan gekauft und eine Lizenz erhalten. Diese Seite erklärt, was Sie damit
tun: wo die Datei abgelegt wird, wie Sie sie auf eine laufende Engine anwenden, wie Sie die
installierte Lizenz auslesen und — falls Sie einen Business-Plan gekauft haben — wie Sie
das Community-Binary ohne Neuinstallation gegen das kommerzielle Binary austauschen.

:::note[Eine Lizenz ist eine Attestierung, kein Laufzeitschalter]
**Sie sperrt keine Funktion der Software, die Sie ausführen.** Eine abgelaufene oder fehlende
Lizenz schaltet keine Funktionalität ab, und keine Lizenz begrenzt Benutzerkonten — selbst
gehostete Benutzer sind in jeder Stufe unbegrenzt. Sie ist eine signierte Aussage über Ihre
Berechtigung und kein Schlüssel, der bereits auf Ihrer Festplatte vorhandenen Code freischaltet.

**Was sie hingegen sperrt, ist der ZUGRIFF AUF ARTEFAKTE**, und dieser Unterschied macht das
gesamte Modell aus: Eine aktive Lizenz ist erforderlich, um das Enterprise-Build herunterzuladen
und aus einem lokalen Bundle zu installieren (`olivares upgrade --bundle`); sie wird offline
anhand des in Ihrem Binary eingebetteten Schlüssels geprüft. Deshalb ist die Enterprise-Edition
ein anderes Binary, das Sie mit einem Token abrufen, statt eines Feature-Flags, das im bereits
vorhandenen Binary umgelegt wird — und deshalb wäre die Aussage „sie sperrt nichts“ falsch.
:::

## Was Sie erhalten haben

| Ihr Kauf | Was Sie erhalten | Was Sie damit tun |
|---|---|---|
| Community | nichts zu installieren | läuft bereits — nichts auf dieser Seite ist anwendbar |
| Business / Business Max, selbst gehostet | eine **Lizenzdatei** und ein **Download-Token** | Lizenz installieren, dann zum Enterprise-Binary wechseln |
| Cloud | Zugangsdaten für einen gehosteten Tenant | auf einem eigenen Host ist nichts zu installieren |

Die Lizenz ist ein einzelner signierter Blob. Speichern Sie ihn als Datei —
`customer.license`, oder unter einem beliebigen Namen — und bewahren Sie das Download-Token
aus derselben E-Mail auf: Beide werden in unterschiedlichen Schritten verwendet, und nur die
Lizenz wird installiert.

## 1 · Lizenz installieren

```sh
olivares license install ./customer.license --data-dir /var/lib/olivares
```

Der Befehl **verifiziert den Blob, bevor er etwas schreibt**, und zwar gegen den in Ihrem
Build eingebetteten öffentlichen Ed25519-Schlüssel. So schlägt ein abgeschnittenes Copy-and-
Paste hier fehl und nicht erst beim nächsten Start. Bei Erfolg schreibt er
`<data-dir>/license.key` mit Modus `0600` — die kanonische ruhende Lizenz, die die Engine
standardmäßig liest.

Übergeben Sie `-` statt eines Pfads, um den Blob von der Standardeingabe zu lesen:

```sh
pbpaste | olivares license install - --data-dir /var/lib/olivares
```

Die Installation über eine vorhandene Lizenz **ersetzt** diese atomar und gibt aus, welche
Lizenz ersetzt wurde.

### Auf eine laufende Engine anwenden — ohne Neustart

Eine laufende Engine übernimmt die neue Lizenz in-place. Jede dieser Optionen bewirkt das:

```sh
kill -HUP "$(pidof olivares)"                 # signal the running process
curl -X POST .../v1/console/runtime/reload    # the API half
```

…oder die Reload-Steuerung der Konsole. Ein Neustart funktioniert ebenfalls, ist aber nicht
erforderlich.

### Wo die Engine sucht — in dieser Reihenfolge

Falls Sie die Lizenz bereits auf andere Weise einspeisen, beachten Sie, dass die Datei im
Datenverzeichnis die **niedrigste** von vier Quellen ist. Die Engine löst sie mit der höchsten
Priorität zuerst auf:

1. `--license <path>` (oder `LicenseFile` in der Konfigurationsdatei)
2. `OLIVARES_LICENSE_PATH=<path>`
3. `OLIVARES_LICENSE=<blob>` — die Lizenz direkt in der Umgebung
4. `<data-dir>/license.key` — was `license install` schreibt

`license install` **verweigert** den Vorgang, wenn der Befehl erkennen kann, dass ein Override
Vorrang vor der Datei hat, die er schreiben will: Eine Installation unter einem solchen Override
würde eine Datei hinterlassen, die die Engine nie liest, und Sie sähen Exit 0 ohne Änderung. Der
Befehl nennt den gefundenen Override, und `--force` stellt die Datei trotzdem bereit — der
legitime Fall ist ein Override, den Sie gleich entfernen werden.

:::caution[Was diese Verweigerung erkennen kann — und was nicht]
Der Befehl liest `OLIVARES_LICENSE_PATH` und `OLIVARES_LICENSE` **aus seiner eigenen
Prozessumgebung**. Er kann weder ein `--license`-Flag noch einen `LicenseFile`-Eintrag in der
Konfiguration einer bereits als separater Prozess laufenden Engine erkennen — `install` und
`uninstall` akzeptieren überhaupt kein `--license`-Flag. Daher können beide
Befehle auf einem Host, auf dem der Dienst mit einem expliziten Pfad gestartet wurde, erfolgreich
ausgeführt werden, ohne etwas an dem zu ändern, was die Engine liest.

Führen Sie `olivares license status` nach jedem der beiden Befehle aus. Der Befehl löst die
Lizenz mit derselben Rangfolge wie die Engine auf und zeigt, welche Quelle tatsächlich wirksam
ist — und genau das ist die entscheidende Frage.
:::

## 2 · Installierte Lizenz prüfen

```sh
olivares license status --data-dir /var/lib/olivares
```

`status` arbeitet offline und löst die Lizenz mit derselben Rangfolge wie die Engine auf. Der
Befehl beantwortet damit die entscheidende Frage — *welche Lizenz tatsächlich wirksam ist* —
statt nur „existiert eine Datei?“. Er meldet die aufgelöste Quelle, den Inhaber, den Plan und
das Ablaufdatum.

Führen Sie ihn nach jeder Installation und nach dem Entfernen eines Overrides aus.

## 3 · Community → Business, in-place

Mit installierter Lizenz ist das Enterprise-Binary nur noch einen Download entfernt. Nichts
wird neu installiert, und keine Daten werden verschoben:

```sh
olivares upgrade --enterprise --token <TOKEN>
```

Der Befehl lädt den signierten Enterprise-Build für Ihre Plattform herunter und **verifiziert
die Signatur offline** — ein manipuliertes Artefakt bricht das Upgrade ab, während das
laufende Binary unverändert bleibt. Anschließend tauscht er es atomar aus und behält ein
Backup des vorigen Binary. Verwenden Sie zuerst `--check`, wenn Sie den Plan sehen möchten,
ohne ihn auszuführen:

```sh
olivares upgrade --enterprise --token <TOKEN> --check
```

Starten Sie den Dienst neu und schalten Sie anschließend die Add-ons ein:

```sh
olivares enterprise enable <preset>     # starter | regulated | full
```

Die Aktivierung wird gesteuert und auditiert: Sie sehen zuerst einen Diff, und jedes Add-on,
das ein Secret oder eine Prüfung benötigt, wird bereitgestellt statt nur teilweise aktiviert.
`olivares enterprise status` meldet, was aktiv ist. Diese Befehle gibt es **nur im
Enterprise-Binary** — wenn `olivares enterprise` kein Befehl ist, führen Sie noch den
Community-Build aus, und der obige Austausch hat noch nicht stattgefunden.

:::caution[Vor dem Austausch sichern]
Der Austausch ersetzt ein Binary, nicht Ihre Daten — erstellen Sie trotzdem dasselbe Backup,
das [Upgrade und Rollback](/de/how-to/upgrade-and-rollback/) verlangt. Dort wird auch die
Rückkehr zum vorigen Binary beschrieben.
:::

## Lizenz entfernen

```sh
olivares license uninstall --data-dir /var/lib/olivares --yes
```

Der Befehl löscht `<data-dir>/license.key` und meldet, was entfernt wurde. Wie `install`
**verweigert** er den Vorgang, solange er einen `OLIVARES_LICENSE*`-Override erkennen kann — die
Datei ist nicht wirksam, daher würde ihre Entfernung nichts ändern — und hat dieselbe blinde
Stelle: Ein `--license`-Flag, das einer separat laufenden Engine übergeben wurde, ist für ihn
unsichtbar. Dies ist die Offline-Hälfte des konsoleneigenen `DELETE /v1/console/license`.

Das Entfernen der Lizenz deaktiviert **nichts**, was bereits ausgeführt wurde. Es zieht die
Attestierung zurück; das Enterprise-Binary verhält sich weiter wie das Enterprise-Binary,
bis Sie es zurücktauschen.

## Was auf dieser Seite *nicht* behandelt wird

- **Lizenzen ausstellen** (`license keygen` / `sign`) ist die Anbieterseite desselben Befehls.
  Als Kunde benötigen Sie sie nicht.
- **Was jeder Plan enthält**, steht auf den Preisseiten, nicht hier.
- **Wie das Modell funktioniert** — warum ein Abonnement Zugriff auf Artefakte und keinen
  Schalter darstellt — erklärt [Open Core & Lizenzierung](/de/explanation/open-core-and-licensing/).
