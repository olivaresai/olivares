---
title: "Ihre erste Stunde mit Olivares AI"
description: >-
  Olivares AI installieren, sich mit einem KI-Werkzeug anmelden oder einen
  Anbieter verbinden, eine Unterhaltung in Ihrem Projektordner starten, eine
  Aktion genehmigen, anhalten und fortsetzen.
---

Sie benötigen Docker Compose, einen Projektordner, an dem das Werkzeug arbeiten
soll, und entweder ein Werkzeug-Abonnement, einen API-Schlüssel eines Anbieters
oder ein lokales Modell.

## 1. Installieren und die Konsole öffnen

Folgen Sie der [README-Installation](https://github.com/olivaresai/olivares#install).
Sie wählt das Release-Image ausdrücklich aus, bindet Ihr Host-Projekt unter
`/project` ein und gibt eine Konsolenadresse sowie ein Ersatz-Setup-Token für die
einmalige Einrichtung aus. Verwenden Sie dort die AppArmor-Anleitung, wenn sie
auf Ihrem Docker-Host zutrifft.

Öffnen Sie die ausgegebene Adresse. Die erste Installation verwendet ein
selbstsigniertes Zertifikat. Prüfen Sie, dass Sie Ihren eigenen Server öffnen,
bevor Sie die Warnung des Browsers akzeptieren. Geben Sie das Setup-Token, Ihre
E-Mail-Adresse und ein Passwort ein, um den ersten Administrator anzulegen. Halten
Sie Token und Passwort geheim. Die Konsole meldet Sie an und öffnet den
Einrichtungsassistenten.

Der Container führt Werkzeuge auf dem Server aus. Ein auf Ihrem Laptop angemeldetes
Werkzeug gehört zu einer anderen Installation; seine Anmeldung wird nicht in den
Container kopiert.

## 2. Ein Werkzeug und seine Anmeldung wählen

Verwenden Sie den Assistenten oder öffnen Sie **KI-Werkzeuge** (`/agent-tools`).
Jedes Werkzeug zeigt an, ob es installiert und angemeldet ist. Fehlt es, prüfen
Sie die Installation und genehmigen Sie sie. Verwenden Sie die Anmeldeschaltfläche
des Werkzeugs und schließen Sie dessen eigenen Anmeldeablauf ab. Für Claude Code
oder Codex genehmigen Sie die Anmeldung mit Ihrem Claude- beziehungsweise
ChatGPT-Konto.

Bei einer nativen Installation verwendet die Standardinstanz die Anmeldung des
Benutzers, der die Engine ausführt. Über Olivares angemeldete Konten bleiben
getrennte Instanzen. Die Werkzeuge melden Konto, Tarif, Modelle und
Nutzungszeiträume, die sie bereitstellen. Ein nicht verfügbarer Wert ist kein
Guthaben von null. Schlägt eine Aktualisierung fehl, wird der letzte Stand als
veraltet angezeigt, zusammen mit dem Befehl, der fehlgeschlagen ist.

Um stattdessen einen API-Schlüssel zu verwenden, öffnen Sie **Werkzeuge**, fügen
den Anbieter unter **API-Schlüssel** hinzu und testen die Verbindung. Für ein
lokales Modell fügen Sie einen Ollama-Endpunkt hinzu, den die Engine erreichen
kann; ein API-Schlüssel ist nicht nötig. Siehe
[Anbieter hinzufügen](/de/how-to/add-a-provider/). Für ein Werkzeug, das ein Modell
verlangt, etwa Codex, wählen Sie ein Modell aus dem Ergebnis des Verbindungstests,
sofern der Anbieter kein gespeichertes Standardmodell hat. Claude Code kann sein
natives Standardmodell verwenden.

## 3. Eine Unterhaltung in Ihrem Projekt starten

1. Öffnen Sie **Sitzungen** (`/sessions`) und wählen Sie **Neue Sitzung**.
2. Wählen Sie ein bereites Werkzeug. Das Formular zeigt einen Ordner und füllt ihn
   für Sie aus. Lassen Sie ihn für eine Sitzung in einem eigenen Ordner unverändert
   oder wählen Sie **Ordner ändern** und geben `/project` ein, um den Host-Ordner
   zu verwenden, der mit der README-Installation eingebunden wird.
3. Lassen Sie **First message** leer und drücken Sie **Starten**. Ihre Nachricht
   können Sie nach dem Öffnen in der Unterhaltung eingeben. Profil und Home-Pfade
   füllt die Engine aus; für diese erste Sitzung müssen Sie sie nicht registrieren.

Der Container-Benutzer, UID 65532, benötigt Schreibzugriff auf das Host-Projekt.
Die [Anleitung zum Projektordner](https://github.com/olivaresai/olivares/blob/main/deploy/compose/README.md#work-on-a-host-project-folder)
erklärt die Berechtigungen. Eine Sitzung kann den Inhalt des eingebundenen Ordners
ändern. Verwenden Sie ein Projektverzeichnis, nicht Ihr gesamtes Home-Verzeichnis.

Bitten Sie das Werkzeug, das Projekt zu erklären, und lesen Sie die Antwort in der
Unterhaltung. Kann **Starten** nicht fortfahren, nennt das Formular, was fehlt.
Befolgen Sie diese Anweisung unter **Werkzeuge** und kehren Sie dann zur Sitzung
zurück.

## 4. Eine Aktion genehmigen

Um eine Genehmigung auszuprobieren, wählen Sie beim Starten der Sitzung
**Weitere Optionen → Vor jeder Aktion nachfragen**. Bitten Sie das Werkzeug, einen
harmlosen Befehl im Projekt auszuführen. Wenn es eine Berechtigung anfordert,
öffnen Sie **Regeln**. Die Anfrage, die auf Sie wartet, ist orange markiert und
steht offen neben der Warteschlange, mit dem Befehl, dem Ziel, das sie erreicht,
und der Regel, die nachgefragt hat. Wählen Sie **Einmal genehmigen** (oder drücken
Sie `A`) oder **Ablehnen** (`D`) und fügen Sie eine Notiz hinzu. Kehren Sie zur
Unterhaltung zurück, um das Ergebnis zu lesen. Eine Organisationsrichtlinie kann
außerdem verlangen, dass eine Genehmigung erfolgt, bevor eine Sitzung startet; eine
genehmigende Person muss diese Anfrage zuerst annehmen.

## 5. Anhalten und fortsetzen

Wählen Sie bei der Sitzung **Stopp** und bestätigen Sie, falls Sie danach gefragt
werden. Der Prozess des Werkzeugs endet; die Unterhaltung bleibt erhalten. Wählen
Sie bei genau derselben Sitzung **Fortsetzen** und senden Sie eine weitere
Nachricht. Fortsetzen behält das gewählte Profil, Konto und den Ordner bei und
prüft, ob sie noch verwendet werden können.

Für den laufenden Betrieb, die CLI und optionale Profileinstellungen siehe
[Eine Anbietersitzung betreiben](/de/how-to/operate-provider-sessions/).

## Native Startmeldungen

Andere Installationsmethoden haben eigene Qualifizierungsgrenzen; siehe
[Installation](https://github.com/olivaresai/olivares/blob/main/INSTALL.md#installation-qualification).
Ein nativer Schnellstart gibt die Konsolenadresse und den nächsten Schritt für
seinen Einrichtungsstand aus. Dies sind die Meldungen für eine neue Installation,
eine Rückkehr und eine ausstehende Einrichtung:

```text
Next: Open the console; it guides setup, sign-in and your first session.
Next: Open the console and sign in to continue your work.
Next: Open the console to finish setup with the one-time token issued earlier.
```

Wenn keine installationsweite Inferenz-Credential-Quelle konfiguriert ist, enthält
das Start-Log der Engine:

```text
session runtime: no inference credential source configured; stream-json launches are deny-closed
```

Das schränkt Claude-Profile mit verwalteter Injektion ohne gebundenen Anbieter ein.
Ein gebundener Anbieterschlüssel oder die autorisierte Anmeldung des Werkzeugs ist
eine eigene Credential-Quelle; siehe
[Eine Anbietersitzung betreiben](/de/how-to/operate-provider-sessions/#wie-das-tool-sich-verbindet).
