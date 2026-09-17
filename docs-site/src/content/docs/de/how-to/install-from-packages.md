---
title: Aus einem Paket installieren
description: >-
  Installieren Sie Olivares AI aus der .deb-, .rpm- oder .apk-Datei auf einem
  gehärteten Linux-Host: prüfen Sie die Release, bevor Sie ihr vertrauen, betreiben
  Sie sie unter der mitgelieferten systemd-Unit oder auf Standard-Alpine im Vordergrund,
  schließen Sie Ihre erste Quelle an und aktualisieren Sie — online, gepinnt oder
  vollständig air-gapped.
draft: false
---

:::note[Veröffentlichte Paketnamen]
Die GitHub-Release v26.9.0 veröffentlicht `.deb`-, `.rpm`- und `.apk`-Artefakte für
`amd64` und `arm64`, zusammen mit `checksums.txt`, `checksums.txt.sig` und
`checksums.txt.pem`. Die Befehle unten verwenden die wörtlichen `amd64`-Namen dieser
Release; ersetzen Sie `amd64` auf einem 64-Bit-ARM-Host durch `arm64`. Installieren
Sie aus diesen verifizierten Release-Artefakten. Repository-Metadatenproduzenten in
einem Quellbaum sind keine Installationsanleitung für diese Seite.

**Qualifikation DIST-24-05.** Was die CI qualifiziert, ist der verifizierte
**Shell-Installer** und sein Service/Doctor-Vertrag, nicht `dpkg`, `rpm` oder `apk`: eine
Dispatch/Pull-Request-Matrix führt ihn gegen die veröffentlichte Release in
Container-Userlands von Debian stable, Ubuntu 24.04 LTS, Fedora, openSUSE Leap und Alpine
sowie auf einem gehosteten macOS-14-Runner aus, und sie schlägt als nicht messbar fehl,
wenn die öffentliche Release nicht erreichbar ist, statt einen Dry-Run als Abdeckung zu
zählen. Diese Matrix zertifiziert keine native Installation per Paketmanager. Der native
Paketlebenszyklus der aus diesem Quellbaum gebauten Pakete — Installation, Start,
Neustart, Upgrade eines laufenden OpenRC-Dienstes und Entfernen — wurde lokal in einem
verwerfbaren Alpine-Gast ausgeübt; das ist ein Nachweis für diesen Baum, keine signierte,
gehostete oder Preproduction-Qualifikation, die weiterhin aussteht.

**Vorgeschlagene Repositories DIST-24-06 (keine aktive Installationsoberfläche).** Der
Quellbaum enthält deterministische apt-, rpm-md- und APK-Repository-Produzenten, einen
Prüfer für signierte Indizes, eine Clean-Client-Qualifikation und einen gestuften
Veröffentlichungs-Workflow, dessen Dispatch inert bleibt, bis ein Reviewer ihn freigibt.
**Keine Paket-Repository-URL ist aktiv**, kein DNS-Name ist delegiert und kein
Produktions-Signaturschlüssel für Repositories ist bereitgestellt. Nichts in diesem
Vorschlag ist eine Paketmanager-Quelle; verwenden Sie weiterhin die verifizierten
Release-Artefakte unten.
:::

Das ist der Weg für einen normalen Linux-Host, auf dem die Engine als Dienst laufen
soll, nicht in einem Container. Debian/Ubuntu und RHEL/Fedora/SUSE verwenden
standardmäßig **systemd**. Alpines Standard-Init ist **OpenRC**, nicht systemd. Für
Container siehe [Mit Docker bereitstellen](/de/how-to/docker-deployment/); für einen
Host ohne Ausgangsroute
[In einer air-gapped Umgebung installieren](/de/how-to/air-gap-install/), auf die
diese Seite beim Aktualisierungsschritt zurückverweist.

## 1. Prüfen Sie die Release, bevor Sie ihr vertrauen

Bei einem Sicherheitsprodukt ist die Build-Pipeline Teil des Vertrauensmodells, daher
fordert hier nichts Sie auf, den Download auf Treu und Glauben zu nehmen. Legen Sie
das Paket, `checksums.txt` und die Signatur in ein Verzeichnis und führen Sie den
Prüfer **aus diesem Verzeichnis** aus:

```bash
# keyless / Sigstore (default; reaches Rekor over the network)
./verify-release.sh
```

Releases werden schlüssellos signiert und veröffentlichen keinen öffentlichen cosign-Schlüssel;
verwenden Sie für Pakete aus einem Release daher den schlüssellosen Befehl. Er benötigt
Sigstore-Trusted-Root-Material, das cosign abruft, sofern es nicht bereits zwischengespeichert
ist. `--offline` entfernt nur die Rekor-Abfrage; die Prüfung wird dadurch nicht netzwerkfrei.
`--key` gilt nur für Dateien, die mit einem privaten Schlüssel unter Ihrer Kontrolle signiert
wurden, und den öffentlichen Schlüssel müssen Sie getrennt von den geprüften Dateien beziehen.

Was jeder Schritt prüft, wie er sich bei einer Teil-Release verhält und wie Sie das
Container-Image stattdessen prüfen, steht in
[Prüfen Sie, was Sie heruntergeladen haben](/de/how-to/verify-a-release/).

## 2. Installieren Sie das Paket

Die drei Paketformate tragen die Binärdatei unter `/usr/bin/olivares`, eine
kommentierte Umgebungsdatei unter `/etc/olivares/olivares.env` (`config|noreplace`),
das Datenverzeichnis `/var/lib/olivares` und die Lizenztexte unter
`/usr/share/doc/olivares/`. `.deb` und `.rpm` liefern die gehärtete **systemd**-Unit.
**Pakete aus diesem Quellbaum** legen in der `.apk` eine ausführbare **OpenRC**-Unit
unter `/etc/init.d/olivares` ab, mit einem expliziten `package-init`-Stempel, damit die
Hooks nicht aus dem Vorhandensein von `systemctl` auf dem Host raten.

Die **zuvor veröffentlichten** `.apk` lieferten dieselbe systemd-Unit und **keine**
OpenRC-Unit. Jenes veröffentlichte Linux-Tarball trägt die Lizenztexte, das README
und `SECURITY.md`; es enthält weder `scripts/install-service.sh` noch
`packaging/service/`. Diese Adapterdateien liegen im signierten Archiv des Baums für die
nächste Release.

```bash
# Debian / Ubuntu
sudo dpkg -i olivares_26.9.0_linux_amd64.deb

# RHEL / Fedora / SUSE
sudo rpm -Uvh olivares_26.9.0_linux_amd64.rpm

# Alpine
sudo apk add --allow-untrusted olivares_26.9.0_linux_amd64.apk
```

Die Installation **erzeugt den Systembenutzer und die Gruppe `olivares`** (mit
`/usr/sbin/nologin` als Shell und `/var/lib/olivares` als Home), erzeugt
`/var/lib/olivares` mit Modus `0750` im Besitz dieses Benutzers und erzeugt
`/etc/olivares`. systemd-Pakete laden systemd neu, wenn `systemctl` vorhanden ist;
OpenRC-Pakete aktivieren oder starten den Dienst nicht. Es wird
**nichts** gestartet — siehe
[was das Paket nicht tut](#8-was-das-paket-nicht-tut).

### Engine starten

Auf Debian/Ubuntu und RHEL/Fedora/SUSE:

```bash
sudo systemctl enable --now olivares
```

Auf **OpenRC** (`.apk` aus diesem Quellbaum):

```bash
sudo rc-service olivares start
# optional; das Paket tut das nicht:
sudo rc-update add olivares default
```

Das Erststart-Token steht in `/var/log/olivares.log` (und in `logread`, wenn syslogd
läuft). Zusätzliche Flags in `OLIVARES_EXTRA_ARGS` werden mit deaktiviertem Globbing
angehängt und an Leerzeichen getrennt; verschachtelte Anführungszeichen werden nicht
interpretiert, und die Env-Datei wird nicht als Shell ausgewertet.

Auf dem **zuvor veröffentlichten Alpine**-Paket fehlt `systemctl`, und jenes `.apk` hat keine
OpenRC-Unit. Starten Sie die Engine als Dienstbenutzer; das Erststart-Token wird auf
stdout gedruckt:

```bash
sudo -u olivares olivares serve --data-dir=/var/lib/olivares \
  --listen=127.0.0.1:8443 --grpc-listen=127.0.0.1:8444 --checkpoint-interval=1h
```

## 3. Die gehärtete systemd-Unit

Die mitgelieferte systemd-Unit führt die Engine als unprivilegierten Benutzer
`olivares` mit leerem Capability-Bounding-Set aus — sie hält keinerlei Capabilities,
weder ambient noch bounding — und `NoNewPrivileges=true`, sodass nichts, was sie
startet, welche gewinnen kann. Darüber hinaus trägt sie `ProtectSystem=strict` (das
Dateisystem ist schreibgeschützt außer `ReadWritePaths=/var/lib/olivares`),
`ProtectHome`, `PrivateTmp`, `PrivateDevices`, die vier
`ProtectKernel*`/`ProtectClock`-Direktiven, `RestrictNamespaces`,
`RestrictSUIDSGID`, `RestrictRealtime`, `LockPersonality`,
`MemoryDenyWriteExecute`, `SystemCallArchitectures=native`, einen
`@system-service`-Syscall-Filter, der zusätzlich `@privileged` und `@resources`
entfernt, und `UMask=0027`.

Auf Standard-Alpine greifen diese systemd-Direktiven bei einer **zuvor veröffentlichten**
`.apk` nicht, weil die systemd-Unit dieser Nutzlast nicht läuft. Aus diesem
Quellbaum gebaute `.apk`-Pakete laufen stattdessen unter OpenRC: sie verwenden das Konto
`olivares`, schreiben das Erststart-Token nach `/var/log/olivares.log` und setzen keine
systemd-Sandbox-Direktiven um.

**Die Listener sind standardmäßig nur Loopback** — `--listen=127.0.0.1:8443` für HTTP
(REST plus eingebettete Konsole) und `--grpc-listen=127.0.0.1:8444` für gRPC. Erweitern
Sie sie bewusst über `OLIVARES_EXTRA_ARGS` in `/etc/olivares/olivares.env` und stellen
Sie Ihre eigene TLS-Terminierung davor. Für IPv6-Loopback verwenden Sie
`--listen=[::1]:8443`.

### Ausführbares Scratch-Dateisystem

Das ist der Fehler, den man auf einem systemd-Host im Voraus kennen sollte, weil das
Symptom die Ursache nicht nennt.

Out-of-process-First-Party-Connectoren werden **in der Binärdatei eingebettet**
mitgeliefert. Beim Boot extrahiert die Engine die benötigten in privates Scratch und
führt sie als Unterprozesse aus. Wenn `TMPDIR` nicht gesetzt ist, wird Scratch unter
`<data-dir>/tmp` angelegt; nur ein nicht beschreibbares Datenverzeichnis lässt die
Engine auf das System-Temp-Verzeichnis zurückfallen. Ein explizites `TMPDIR` gewinnt
immer.

Der mitgelieferte systemd-Dienst verwendet daher standardmäßig
`/var/lib/olivares/tmp`, nicht `/tmp`. Prüfen Sie den Mount, der das ausführbare
Scratch tatsächlich hält:

```bash
check_scratch_mount() {
  target=${1:-/var/lib/olivares}
  opts=$(findmnt -no OPTIONS --target "$target") || {
    printf '%s\n' "cannot read mount options for $target (missing path or permission)" >&2
    return 1
  }
  [ -n "$opts" ] || {
    printf '%s\n' "mount options for $target are unknown" >&2
    return 1
  }
  case ",$opts," in
    *,noexec,*) printf '%s\n' 'noexec — set TMPDIR' ;;
    *) printf '%s\n' 'exec-capable — nothing to do' ;;
  esac
}
check_scratch_mount /var/lib/olivares
```

Wenn dort `noexec` steht, zeigen Sie `TMPDIR` auf ein Verzeichnis, das unter
`ProtectSystem=strict` beschreibbar **und** auf einem ausführbaren Mount liegt:

```bash
sudo install -d -o olivares -g olivares -m 0750 /run/olivares-exec-tmp
sudo systemctl edit olivares      # creates a drop-in; do not edit the shipped unit
```

```ini
[Service]
Environment=TMPDIR=/run/olivares-exec-tmp
ReadWritePaths=/run/olivares-exec-tmp
```

Dann neu starten. Wenn ein Exec mit `EACCES` oder `ENOEXEC` abgelehnt wird, nennt der
Fehler der Engine den Extraktions-Mount und die zwei Umplatzierungssteuerungen
(`TMPDIR` und data-dir); er meldet einen noexec-Mount nicht als fehlenden Connector.

Verwenden Sie `systemctl edit`, niemals eine direkte Bearbeitung von
`/usr/lib/systemd/system/olivares.service`: diese Datei gehört zum Paket und ein
Upgrade ersetzt sie.

## 4. Erster Start: `olivares quickstart`

`quickstart` ist `serve` mit freundlichen Vorgaben und einem geführten Banner. Es
erfindet niemals Standard-Anmeldedaten; es verweist Sie auf die eingebettete Konsole,
um den ersten Administrator mit einem **Einmal-Token** anzulegen.

Wenn Sie bereits unter der mitgelieferten systemd-Unit dienen, führen Sie
`quickstart` nicht aus — **das Erststart-Setup-Token wird ins Journal gedruckt**:

```bash
journalctl -u olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'
```

Wenn Sie `serve` im Vordergrund gestartet haben (Standard-Alpine), steht dasselbe Banner
auf stdout.

Öffnen Sie die Konsole unter `https://127.0.0.1:8443` (beim ersten Start wird ein
selbstsigniertes Zertifikat erzeugt), legen Sie das Token vor und erstellen Sie den
Administrator. Das Token ist einmalig.

Auf einer Workstation, um ohne Dienstinstallation umzusehen, macht
`olivares quickstart` dasselbe im Vordergrund mit `--listen`/`--grpc-listen`/`--data-dir`,
falls Sie die Vorgaben verschieben müssen.

## 5. Ihre erste Quelle: pgAudit

Eine Quelle ist der Ort, aus dem die Engine Beobachtungen aufnimmt. Die Verben sind
nach dem aufgeteilt, was jedes kostet, und es lohnt sich, sie in dieser Reihenfolge zu
nutzen: `plan` sagt, was sich ändern würde, und schreibt nichts, `validate` sagt, dass
die Konfiguration für sich kohärent ist, **ohne das Netz zu berühren**, `test` öffnet
die Quelle wirklich, um zu beweisen, dass sie antwortet, und `set` wendet an.
Konfiguration trägt Geheimnis-**Referenzen** (`store:<name>`), niemals Werte.

pgAudit liest das PostgreSQL-Audit-Protokoll, daher ist `log_path` das einzige
pflichtige Feld:

```bash
# coherent by itself? (writes nothing, opens no socket)
sudo -u olivares olivares sources validate --name pg-prod --kind pgaudit \
  --tenant <your-tenant-id> \
  --config log_path=/var/log/postgresql/postgresql.log \
  --data-dir /var/lib/olivares

# does it actually answer? (opens the source for real)
sudo -u olivares olivares sources test --name pg-prod --kind pgaudit \
  --tenant <your-tenant-id> \
  --config log_path=/var/log/postgresql/postgresql.log \
  --data-dir /var/lib/olivares

# apply it
sudo -u olivares olivares sources set --name pg-prod --kind pgaudit \
  --tenant <your-tenant-id> \
  --config log_path=/var/log/postgresql/postgresql.log \
  --actor "$(id -un)" --reason "onboard the production audit log" \
  --data-dir /var/lib/olivares
```

Drei Dinge an der obigen Kommandozeile sind kein Füllmaterial:

- **`--tenant` ist Pflicht.** Eine Quelle muss den Geschäftstenant nennen, dem ihre
  Beobachtungen gehören; ohne ihn weigert sich der Befehl, statt einen Eigentümer für
  Ihre Audit-Daten zu raten.
- **`--actor` und `--reason` verlangt `set`, und nur `set`.** Eine privilegierte
  Offline-Operation muss festhalten, wer sie ausgeführt hat und warum. `validate`
  braucht keines von beiden, weil es nichts schreibt — die Asymmetrie ist der Punkt.
- **`format` ist standardmäßig `csvlog` und `follow` ist `true`**, daher braucht ein
  übliches pgAudit-Deployment keines von beiden.

Das Anwenden druckt, was sich Feld für Feld geändert hat, und sagt Ihnen, wie eine
**laufende** Engine das ohne Neustart aufnimmt — `POST /v1/console/runtime/reload`
oder ein `SIGHUP`. Unter der mitgelieferten systemd-Unit ist das
`sudo systemctl reload olivares`. Wenn Sie `serve` im Vordergrund gestartet haben,
senden Sie `SIGHUP` an diesen Prozess.

Der Dienstbenutzer braucht Lesezugriff auf diese Protokolldatei; auf den meisten
Distributionen bedeutet das, `olivares` zur Gruppe `adm` oder `postgres` hinzuzufügen —
eine bewusste Freigabe von Ihnen, nichts, was das Paket für Sie tut.

## 6. Aktualisieren

`olivares upgrade` ersetzt die Binärdatei an Ort und Stelle, und die
Sicherheitseigenschaften sind der Grund, das dem manuellen Neuinstallieren des Pakets
vorzuziehen: es **ersetzt die Binärdatei erst, wenn der heruntergeladene Kandidat
erfolgreich per Exec geprüft wurde**, behält eine zeitgestempelte Sicherung und
**kehrt zu dieser Sicherung zurück, wenn die Prüfung nach dem Tausch fehlschlägt**.

```bash
sudo olivares upgrade --check    # what would change, without changing anything
sudo olivares upgrade --yes      # do it
```

Drei Flags sind für eine paketierte Installation besonders relevant:

- **`--endpoint`** — Updates aus einem GitHub-Repository, das Sie kontrollieren, statt
  der Vorgabe. Das ist der Ausweg für einen Spiegel oder einen Fork.
- **`--bundle`** — Installation aus einem lokalen Bundle-Verzeichnis oder `.tar.gz`
  **ohne jedes Netz**. Dieses Bundle zu bauen und zu transportieren beschreibt
  [In einer air-gapped Umgebung installieren](/de/how-to/air-gap-install/).
- **`--install-timer`** — gibt einen **optionalen systemd**-Timer und -Dienst aus, der
  nach Plan nach Updates sucht. Nichts installiert das für Sie; siehe
  [was das Paket nicht tut](#8-was-das-paket-nicht-tut). Es ist ein systemd-Generator.

Beachten Sie, dass Staging und Exec-Prüfung **im Installationsverzeichnis, neben dem
Ziel — nicht in `/tmp`** stattfinden, sodass der [oben](#ausführbares-scratch-dateisystem)
besprochene `noexec`-Mount ein Upgrade nicht bricht. Ein `noexec`-Mount auf dem
**Installationsverzeichnis** ist eine andere Sache und macht die installierte Version
unmessbar; dieser Fall, die Release-Kanäle, gestuftes Rollout und Zurückrollen stehen
in [Aktualisieren und zurückrollen](/de/how-to/upgrade-and-rollback/).

## 7. Deinstallieren oder migrieren, ohne Pfade zu raten

Das Paket schreibt `/var/lib/olivares/install-manifest.json`. Der Deinstaller prüft das
vollständige Manifest gegen den signierten Distributionsindex, bevor er Dienst oder
Dateisystem anfasst; ein unerwarteter Pfad liefert 2. Zuerst inspizieren, dann die
Aufbewahrung ausdrücklich wählen:

```bash
sudo olivares uninstall --plan --data-dir /var/lib/olivares
sudo olivares uninstall --preserve --data-dir /var/lib/olivares
sudo olivares uninstall --purge --data-dir /var/lib/olivares --yes
```

Preserve ist die Paketentfernungsrichtlinie: sie behält Konfiguration, Daten, Protokolle,
Schlüssel und ihre Dienstidentität. Bei systemd-Paketen führt der Entfernungs-Hook
`--preserve` aus. Bei aus diesem Quellbaum gebauten OpenRC-`.apk`-Paketen stoppt der Hook
den Dienst, falls er aktiv ist, entfernt den Eintrag im Standard-Runlevel, ohne
fehlzuschlagen, wenn er nie aktiviert war, prüft das vollständige Manifest mit `--plan`
und lässt apk die paketeigenen Dateien entfernen. Zuvor veröffentlichte Alpine-Pakete
prüften nur mit `--plan`, weil diese Nutzlast keine OpenRC-Unit zu stoppen hatte.
Führen Sie `--purge` vor dem Entfernen des Pakets nur aus, wenn Löschen die Absicht
ist. Es verlangt Bestätigung und löscht nur indizierte Pfade.

Für einen Standortumzug erstellen Sie zuerst ein `olivares dr backup`, installieren das
Ziel und führen dort `olivares dr restore` aus. Aktuelle Bundles verwenden
`hmac-sha256-kek-v1`, um das Manifest und jede Nutzlast unter Ihrem KEK zu
authentifizieren. Ein Export einer neueren Engine wird vor Schreibvorgängen
abgelehnt; ein getrennt authentifiziertes pre-v26.9-Bundle braucht ausdrücklich
`--allow-legacy-unsigned`. Der
[Leitfaden zu Sicherung und Wiederherstellung](/de/how-to/backup-and-restore/)
behandelt KEK-Verwahrung und den Kontinuitätsnachweis nach dem Import.

## 8. Was das Paket nicht tut

Klar gesagt, weil ein Sicherheitsprodukt, das hier vage ist, die Installation nicht
verdient:

- **Es fügt kein Repository hinzu.** Nichts wird nach `/etc/apt/sources.list.d`,
  `/etc/yum.repos.d` oder `/etc/apk/repositories` geschrieben. Sie haben eine Datei
  installiert; nur diese Datei wurde installiert. Updates lösen Sie aus — durch ein
  neues Paket oder durch `olivares upgrade`.
- **Es startet oder aktiviert den Dienst nicht.** systemd-Pakete laden systemd neu,
  wenn `systemctl` vorhanden ist, und drucken `systemctl enable --now`. OpenRC-Pakete
  drucken `rc-service olivares start` und führen kein `rc-update add` aus. Starten
  bleibt Ihre Entscheidung. Ein Upgrade eines bereits laufenden OpenRC-Dienstes stoppt
  ihn, ersetzt die Dateien und startet ihn erneut; ein inaktiver Dienst bleibt inaktiv.
- **Eine Lizenz zu prüfen, ruft niemanden an. Herunterzuladen, wofür Sie bezahlt
  haben, schon.** Die Lizenzprüfung im offenen Build ist Offline-Ed25519, es gibt
  keinen entfernten Kill-Switch, und kein Lizenzschlüssel sperrt oder degradiert
  diesen Build — der AGPL-Build ist die gesamte Plattform.
  Es gibt **keine verpflichtende Telemetrie und kein Control-Plane-Egress als
  Vorgabe: was Ihre Perimetergrenze überquert, ist das, was Sie zum Überqueren
  konfigurieren** — Aufrufe Ihrer Modell-APIs, die SIEM-/Webhook-Ausgänge, die Sie
  verdrahten, ein externer Embedding-Anbieter, wenn Sie einen provisionieren, und
  jede Quelle, die ein Connector abfragt (sein `addr`, `base_url` oder `endpoint`) im
  Intervall, das Sie setzen.
- **Aber `olivares upgrade` macht bewusst einen Netzaufruf, wenn Sie es ausführen** —
  das ist der Sinn einer Update-Prüfung, und `--check` zeigt Ihnen den Plan, bevor
  sich etwas bewegt. Die ehrliche Form des Versprechens lautet: *eine Lizenz zu
  prüfen, ruft niemanden an; herunterzuladen, wofür Sie bezahlt haben, schon.*
  Verwenden Sie `--bundle`, wenn der Update-Pfad ebenfalls keinen Aufruf machen soll.
- **Es öffnet keinen Port ins Netz.** Die Unit bindet nur Loopback, bis Sie sie selbst
  erweitern.

## Siehe auch

- [Prüfen Sie, was Sie heruntergeladen haben](/de/how-to/verify-a-release/) — der
  vollständige Prüfpfad
- [Aktualisieren und zurückrollen](/de/how-to/upgrade-and-rollback/) — Kanäle,
  gestuftes Rollout, Rollback
- [Eine Bereitstellung härten](/de/how-to/security-hardening/) — über das hinaus, was
  die Unit bereits tut
- [In einer air-gapped Umgebung installieren](/de/how-to/air-gap-install/)
- [Mit Docker bereitstellen](/de/how-to/docker-deployment/)
- [Eine Quelle anschließen](/de/how-to/connect-a-source/)
- [Sichern und wiederherstellen](/de/how-to/backup-and-restore/)
