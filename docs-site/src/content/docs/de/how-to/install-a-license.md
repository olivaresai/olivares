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

:::note[Die Lizenz, nicht das Binary, schaltet die kommerziellen Funktionen ein]
**Das Business-Build führt eine kommerzielle Funktion nur aus, solange eine gültige Lizenz
installiert ist, deren Berechtigungen sie umfassen.** Es prüft die Lizenz offline, auf dem
Rechner, bei jeder Nutzung. Ohne Lizenz oder mit einer abgelaufenen oder widerrufenen Lizenz
antwortet neue Arbeit dieser Funktion mit `addon_requires_license`; Lesen, Prüfen und Exportieren
Ihrer Daten funktionieren weiter, und keine Lizenz begrenzt Benutzerkonten — selbst gehostete
Benutzer sind in jeder Stufe unbegrenzt. Das Community-Binary liest nie eine Lizenz, um etwas
ein- oder auszuschalten.

**Eine aktive Lizenz gewährt Zugang zu kommerziellen Artefakten.** Die Offline-Installation (`olivares upgrade --bundle`) erfordert das Enterprise-Binary. Enterprise installiert ein signiertes Community-Bundle ohne Lizenz; ein kommerzielles Bundle erfordert eine aktive, offline geprüfte Lizenz. Community und Business unterstützen `--bundle --check`, ohne eine Lizenz zu lesen oder das Bundle zu installieren.
:::

## Was Sie erhalten haben

| Ihr Kauf | Was Sie erhalten | Was Sie damit tun |
|---|---|---|
| Community | nichts zu installieren | läuft bereits — nichts auf dieser Seite ist anwendbar |
| Business / Enterprise, selbst gehostet | eine **Lizenzdatei** und ein **Download-Token** | Lizenz installieren, dann zum kommerziellen Binary wechseln |
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

Wenn Sie eine gültige Business- oder Enterprise-Lizenz auf einem Community-Build installieren,
läuft dieser Build weiter. Das Session-Cockpit und die übrigen kommerziellen Seiten erscheinen
erst, nachdem Sie diese Installation auf Business umgestellt haben. Die Ansicht
**Edition & license** der Konsole nennt die installierte Lizenz getrennt vom Build. **Expires**
ist die bezahlte Laufzeit; **Lease until**, sofern angezeigt, ist die davon getrennte
vorläufige Instanz-Lease. Läuft die Installation in Docker? Gehen Sie unten zu
[Docker und Compose](#docker-und-compose).

:::note[Business und der kommerzielle Artefaktkanal]
Das Flag `--enterprise` bezeichnet den kommerziellen Artefaktkanal, nicht die Edition.
Die Editionen sind Community, Business und Enterprise. Business enthält Regulated Operations,
AI Runtime Security, Compliance Packs und Identity & Scale in einem Abonnement.
Jede Familie kann aktiviert oder deaktiviert werden; keine wird separat verkauft.
:::

Mit installierter Lizenz ist das kommerzielle Binary nur noch einen Download entfernt. Nichts
wird neu installiert, und keine Daten werden verschoben:

```sh
export OLIVARES_UPGRADE_TOKEN=<TOKEN>   # not needed once this installation is connected
olivares upgrade --edition business
```

`olivares license install` und `olivares license status` geben den genauen Befehl für die Art aus, wie diese Installation installiert wurde, und die Ansicht **Edition & license** der Konsole zeigt denselben Schritt als einen Button, **Zu Business wechseln**: Die Engine lädt den Build herunter, prüft ihn, tauscht ihn an Ort und Stelle aus und startet damit neu. Das Download-Token steht auf der Seite **Downloads** des lizenzierten Download-Portals ([https://licenses.olivares.ai/portal/downloads](https://licenses.olivares.ai/portal/downloads)), neben diesen Befehlen, und in Ihrer Lizenz-E-Mail; übergeben Sie es wie oben über den Namen, damit es nicht in der Prozessliste erscheint. Eine verbundene Installation (`olivares license connect start`) braucht kein Token. `--enterprise` ist die ältere Schreibweise derselben Route. Der Button der Konsole braucht eines von beiden, das Token in der Umgebung der Engine selbst oder diese Verbindung; ohne beides zeigt die Ansicht stattdessen den Befehl.

:::caution[Paketinstallationen]
Bei einer deb-, rpm-, apk- oder AUR-Installation gehört `/usr/bin/olivares` dem Paketmanager, daher wird
dieser Befehl abgelehnt: Er endet mit Exit-Code 2, bevor etwas heruntergeladen wird. Nutzen Sie das
Business-Paket, sobald der berechtigte Paketkanal es bereitstellt. Bis dahin ersetzen Sie die Datei
selbst mit `--target /usr/bin/olivares --data-dir /var/lib/olivares`; die Paketdatenbank passt dann nicht mehr zu ihr. `--check`
zeigt weiterhin den Plan.
:::

Der Befehl lädt den signierten kommerziellen Build für Ihre Plattform herunter und **verifiziert
die Signatur offline** — ein manipuliertes Artefakt bricht das Upgrade ab, während das
laufende Binary unverändert bleibt. Anschließend tauscht er es atomar aus und behält ein
Backup des vorigen Binary. Business hat dieselbe Versionsnummer wie Community; der Befehl funktioniert
also auch, wenn Sie diese Version bereits ausführen. Verwenden Sie zuerst `--check`, wenn Sie den Plan
sehen möchten, ohne ihn auszuführen; er nennt den Wechsel `edition: community -> enterprise`:

```sh
olivares upgrade --edition business --check
```

Starten Sie den Dienst neu und schalten Sie anschließend die Module ein:

```sh
olivares enterprise enable <preset>     # starter | regulated | full
```

Der Befehl und die Preset-Namen sind das, was das kommerzielle Binary akzeptiert. Die Presets sind keine Editionen und entsprechen nicht eins zu eins den vier Business-Funktionsfamilien.
Die Aktivierung wird gesteuert und auditiert: Sie sehen zuerst einen Diff, und jedes Modul,
das ein Secret oder eine Prüfung benötigt, wird bereitgestellt statt nur teilweise aktiviert.
`olivares enterprise status` meldet, was aktiv ist. Diese Befehle gibt es **nur im
kommerziellen Binary** — wenn `olivares enterprise` kein Befehl ist, führen Sie noch den
Community-Build aus, und der obige Austausch hat noch nicht stattgefunden.

:::caution[Vor dem Austausch sichern]
Der Austausch ersetzt ein Binary, nicht Ihre Daten — erstellen Sie trotzdem dasselbe Backup,
das [Upgrade und Rollback](/de/how-to/upgrade-and-rollback/) verlangt. Dort wird auch die
Rückkehr zum vorigen Binary beschrieben.
:::

### Docker und Compose

Das öffentliche Image `docker.io/olivaresai/olivares` ist Community. In einem Container
installiert `olivares upgrade` das Release in das Datenvolume statt in das Image, sodass das
Neuerstellen des Containers es erneut startet. Das wendet das Business-Release an, wenn es
neuer ist als das Image oder dieselbe Version hat (`--check` nennt den Wechsel `edition: community -> enterprise`);
das Image selbst zu bauen, wie unten beschrieben, ist die Alternative. Ist die Lizenz installiert
(Abschnitt 2), führen Sie das Upgrade im laufenden Container aus und starten ihn neu. Das Token
steht in Ihrer Fulfilment-E-Mail; übergeben Sie es per Namen, damit es nicht in der
Prozessliste erscheint:

```sh
export OLIVARES_UPGRADE_TOKEN=<TOKEN>
docker compose exec -e OLIVARES_UPGRADE_TOKEN olivares olivares upgrade --edition business --yes
docker compose restart olivares
```

Das Release liegt in `ota/` im Datenverzeichnis (`/var/lib/olivares/ota` im ausgelieferten
Image), zusammen mit dem signierten Manifest und dem Artefakt, aus dem es verifiziert wurde.
Das Image-Binary startet es, wenn es neuer ist als das Image oder dieselbe Version anders
gebaut wurde (ein Community-Image der Version, die Sie als Business angewendet haben). Dem
Volume wird nichts für sich allein vertraut: Jeder Start prüft die Signatur des Manifests
gegen den im Image eingebetteten OTA-Schlüssel und das Artefakt gegen den Digest, den das
Manifest signiert. Der Nachweis wird im temporären Verzeichnis des Containers gehalten (die
ausgelieferte Compose-Datei mountet dort ein `tmpfs`), nicht im Volume; ein Neuerstellen leert
ihn. Ist das Image neuer als das angewendete Release, läuft das Image und das Startlog sagt es;
führen Sie das Upgrade dann erneut aus. Evidenz, die sich nicht verifizieren lässt, wird mit
einer Meldung ignoriert, und das Image läuft. Das Volume hält keine Sicherung des vorigen
Binary: Um zum Binary des Images zurückzukehren, entfernen Sie `ota/`. Das erfordert ein Volume,
das Ausführen erlaubt (ein benanntes Volume tut das; bei einem `noexec`-Mount stoppt
`olivares serve` und nennt den Grund, statt stillschweigend das Binary des Images auszuführen).

Sie können das Image auch selbst bauen. Laden Sie das Business-Binary **derselben Version und Linux-Architektur** über das
[Portal für lizenzierte Downloads](https://licenses.olivares.ai/portal) herunter und verifizieren
Sie Signatur und Prüfsumme anhand der Anleitung des Downloads. Anschließend können Sie dieses
verifizierte Binary auf Ihrem eigenen Host als zusätzliche Schicht auf das passende
Community-Runtime-Image legen. Verwenden Sie in einem Verzeichnis, das das verifizierte Binary
mit dem Namen `olivares` enthält, dieses `Dockerfile`:

```dockerfile
ARG COMMUNITY_IMAGE
FROM ${COMMUNITY_IMAGE}
COPY --chmod=0755 olivares /usr/local/bin/olivares
```

Bauen Sie mit dem Digest des Community-Images dieser Version, verifiziert wie in
[Ein Release verifizieren](/de/how-to/verify-a-release/). Das Ergebnis behält den Non-Root-Benutzer,
den Entrypoint und die Ports dieses Images, daher braucht das `Dockerfile` keine `USER`-Zeile:

```sh
docker build --build-arg COMMUNITY_IMAGE=docker.io/olivaresai/olivares@sha256:<digest> \
  --tag olivares-business-local:X.Y .
```

Halten Sie dieses Image privat und bewahren Sie die mit dem Download gelieferte kommerzielle
Lizenz und die Hinweise (notices) auf. Legen Sie weder Ihre Lizenzdatei noch Ihr Download-Token in den
Build-Kontext. Setzen Sie das `image` Ihres Compose-Dienstes auf `olivares-business-local:X.Y` und
erstellen Sie diesen Dienst dann mit `docker compose up -d --no-deps --force-recreate olivares`
neu. Erstellen Sie bei eigenständigem Docker den Container mit diesem Image und demselben Volume
und denselben Argumenten neu. Behalten Sie das Datenvolume: Verwenden Sie nicht `down --volumes`.

Prüfen Sie nach dem Neustart **Edition & license**: Die Edition sollte **Business** anzeigen, und
die installierte Lizenz bleibt im Datenvolume. Enterprise-Verträge verwenden ebenfalls diesen
kommerziellen Build; ein vertragsspezifisches Artefakt folgt den Anweisungen seines Downloads.

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
Attestierung zurück; das kommerzielle Binary verhält sich weiter wie das kommerzielle Binary,
bis Sie es zurücktauschen.

## Was auf dieser Seite *nicht* behandelt wird

- **Lizenzen ausstellen** (`license keygen` / `sign`) ist die Anbieterseite desselben Befehls.
  Als Kunde benötigen Sie sie nicht.
- **Was jeder Plan enthält**, steht auf den Preisseiten, nicht hier.
- **Wie das Modell funktioniert** — warum ein Abonnement Zugriff auf Artefakte und keinen
  Schalter darstellt — erklärt [Open Core & Lizenzierung](/de/explanation/open-core-and-licensing/).
- **Die Probe von Kauf → Erstattung** (Sandbox-Fixtures, keine echte Karte) ist
  [Den kommerziellen Zyklus proben](/how-to/rehearse-commercial-cycle/).
