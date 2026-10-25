---
title: Eine Anbietersitzung betreiben
description: >-
  Ein KI-Werkzeug und sein Konto oder seinen Anbieter auswählen, in einem
  Projektordner arbeiten, Aktionen genehmigen, einen Turn unterbrechen, stoppen
  und fortsetzen, über die Konsole oder die CLI.
---

Schließen Sie zuerst [Ihre erste Stunde](/de/how-to/first-hour/) ab, bevor Sie hier
beginnen. Werkzeuge laufen auf dem Host der Engine, bei einer Compose-Installation
auch im Container. Die Anmeldung und die Ordner Ihres Client-Terminals sind dort
nicht automatisch verfügbar.

## Wie das Tool sich verbindet

**KI-Werkzeuge** (`/agent-tools`) installiert Werkzeuge und führt deren eigene
Anmeldeabläufe aus. Bei einer nativen Engine verwendet die Standardinstanz des
Werkzeugs die Anmeldung des Engine-Benutzers; über Olivares angemeldete Konten
bleiben getrennte Instanzen. Wählen Sie das Konto, das Sie verwenden möchten. Die
Kontoübersicht zeigt E-Mail, Tarif, Nutzungszeiträume und Modelle, die das Werkzeug
meldet. Fehlende Werte bleiben nicht verfügbar, und fehlgeschlagene Aktualisierungen
werden als veraltet markiert.

**API-Schlüssel** auf **Werkzeuge** (`/agent-tools`) enthält API-Schlüssel und
Endpunkte für lokale Modelle. Fügen Sie den Anbieter dort hinzu und testen Sie ihn;
danach verwenden Sie ihn über **Neue Sitzung**. Ein Anbieterschlüssel und ein
Werkzeug-Abonnement sind unterschiedliche Wege, sich zu verbinden. Ollama benötigt
einen erreichbaren Endpunkt und ein Modell, aber keinen API-Schlüssel. Siehe
[Anbieter hinzufügen](/de/how-to/add-a-provider/).

Für Claude-Profile mit `managed_injection`, die keinen Anbieter nennen,
liefern `OLIVARES_SESSION_RUNTIME_WIF` oder `OLIVARES_SESSION_RUNTIME_TOKEN_FILE`
die Inferenz-Zugangsdaten des Hosts. Ein Profil, das an einen Anbieter-Eintrag
gebunden ist, verwendet stattdessen diesen Eintrag; kann dessen Zugangsdaten nicht
geöffnet werden, wird der Start verweigert, ohne auf die Zugangsdaten des Hosts
zurückzugreifen. Ein Profil mit `provider_account_home` verwendet seine autorisierte
Werkzeuganmeldung und benötigt keine der beiden Variablen. Siehe
[wo die Umgebungsvariablen gelten](/de/how-to/add-a-provider/#die-umgebungsvariablen-und-wo-sie-weiterhin-gelten).

## Starten und Nachrichten senden

Öffnen Sie **Sitzungen → Neue Sitzung** und wählen Sie ein bereites Werkzeug. Die
Engine füllt Profil und Ordner aus. Für die README-Compose-Installation wählen Sie
**Ordner ändern** und geben `/project` ein, um an Ihrem eingebundenen Host-Projekt
zu arbeiten. Mit dem Standardordner müssen Sie keinen Pfad eintippen. Lassen Sie
**First message** leer, um zu starten, und schreiben Sie dann in der Unterhaltung,
oder geben Sie die Nachricht ein, bevor Sie **Starten** drücken.

Ein Werkzeug, das einen Anbieterschlüssel verwendet, nutzt sein gespeichertes Modell
oder lässt Sie ein Modell wählen, das der Verbindungstest zurückgegeben hat. Wird ein
Modell benötigt, hat der Test aber noch keines zurückgegeben, testen Sie zuerst die
Verbindung unter **API-Schlüssel** auf **Werkzeuge**. Claude Code kann sein natives
Standardmodell verwenden.

Für die README-Compose-Installation führen Sie die CLI im Engine-Container aus. Dort
findet sie die lokale Engine und deren Zertifikat. Melden Sie sich mit dem
Olivares-Administratorkonto an, das Sie bei der Einrichtung angelegt haben, nicht
mit Ihrem Werkzeug-Abonnement.

Wenn Sie einen Anbieterschlüssel mit einem Werkzeug verwenden, das ein Modell
verlangt, etwa Codex, speichern Sie unter **API-Schlüssel** auf **Werkzeuge** ein
Standardmodell, bevor Sie `session start` unten ausführen. Wenn weder der Anbieter
noch das Profil des Werkzeugs ein konfiguriertes Modell hat, hängen Sie
`--model <tested-model-id>` an den Befehl `session start` an und ersetzen den
Platzhalter durch eine Modell-ID, die der Verbindungstest zurückgegeben hat. Ein in
**Neue Sitzung** gewähltes Modell gilt nur für diesen Start in der Konsole; es legt
kein Modell für einen CLI-Start fest.

Legen Sie die Compose-Datei einmal in Ihrem Host-Terminal fest:

```sh
export COMPOSE_FILE="$HOME/olivares/deploy/compose/docker-compose.yml"
docker compose exec olivares olivares login
docker compose exec olivares olivares tool ls
docker compose exec olivares olivares tool providers
docker compose exec olivares olivares session start /project --name first-hour
docker compose exec olivares olivares session send first-hour "Explain this project."
```

Für eine native Installation führen Sie dieselben Olivares-Befehle direkt auf dem
Engine-Host aus und ersetzen `/project` durch Ihren Projektpfad. Der Client gibt
aus, welches Werkzeug und welche Verbindung er gewählt hat. Bei mehreren Werkzeugen
wählen Sie eines mit `--tool`; `--profile` wählt ein vorhandenes Profil. Wenn Sie
das Client-Terminal schließen, läuft die Sitzung auf der Engine weiter. Um sich
wieder mit ihrer Ausgabe zu verbinden:

```sh
docker compose exec olivares olivares session follow first-hour
```

## Genehmigen, unterbrechen, stoppen und fortsetzen

Unter **Weitere Optionen** veranlasst **Vor jeder Aktion nachfragen**, dass das
Werkzeug vor jeder Aktion fragt. Unter **Regeln** steht die Anfrage, die auf Sie
wartet, offen neben der Warteschlange, mit ihrem Befehl, dem Ziel, das sie erreicht,
und der Regel, die nachgefragt hat. Wählen Sie **Einmal genehmigen** (`A`) oder
**Ablehnen** (`D`) und fügen Sie eine Notiz hinzu. Ein Start, der eine Genehmigung
der Organisation verlangt, wartet ebenfalls dort; senden Sie Ihre Nachricht, sobald
der Start genehmigt ist.

Verwenden Sie **Interrupt**, um den aktuellen Turn zu beenden und die Unterhaltung
offen zu lassen. Verwenden Sie **Stopp**, um den Prozess zu beenden, und bestätigen
Sie gegebenenfalls. **Fortsetzen** startet dieselbe Sitzung mit demselben Profil,
Konto und Ordner neu; Zugangsdaten und Richtlinie werden erneut geprüft. Senden Sie
nach dem Fortsetzen eine neue Nachricht.

```sh
docker compose exec olivares olivares session interrupt first-hour
docker compose exec olivares olivares session stop first-hour
docker compose exec olivares olivares session resume first-hour
docker compose exec olivares olivares session send first-hour "Continue explaining this project."
```

Ein geändertes oder nicht verfügbares Konto führt zu einer Startverweigerung, nicht
zu einer Erlaubnis, auf eine andere Anmeldung zu wechseln. Ein unterbrochener
Grok-Turn verwendet ACP-Abbruch ohne Bestätigung; warten Sie auf das korrelierte
Turn-Ergebnis, bevor Sie ihn als beendet betrachten.

## Optionale Profil- und Git-Einstellungen

Für ein vorhandenes konfiguriertes Werkzeug-Home lässt der Reiter **Anbieterprofile**
unter **Sitzungen** (`/sessions?tab=profiles`) einen Administrator ein Profil
registrieren und die Startbereitschaft prüfen. Profil und Quellenbindungen eines
benannten Kontos stehen außerdem in dessen Detailansicht unter **Werkzeuge**. Die
Homes müssen auf dem Ausführungshost existieren. Verwenden Sie **Weitere Optionen →
Erweiterte Startoptionen** im Dialog **Neue Sitzung**, um dieses Profil auszuwählen.
Binary-Überschreibungen wählen ein ausführbares Programm; andernfalls versucht die
Engine die neueste verifizierte verwaltete Installation und danach das Werkzeug in
ihrem `PATH`. Siehe [Konfiguration](/de/reference/configuration/).

Für das oberste Verzeichnis eines Git-Repositorys mit mindestens einem Commit
erstellt **In einem neuen Git-Worktree arbeiten** einen Branch und ein eigenes
Arbeitsverzeichnis für die Sitzung. Fortsetzen verwendet diese wieder. Eine Übergabe
mit Branch und Commit bietet **In einem neuen Sitzungs-Worktree öffnen** an; wählen
Sie einen registrierten Ordner, dessen Repository diesen Commit enthält. Die Engine
verweigert nicht unterstützte oder unsichere Repository-Einstellungen, bevor sie den
Worktree anlegt.

Ein Ordner kann einen Setup-Befehl für neue Worktrees speichern. Eine Person muss
dessen exakten Text per SHA-256 genehmigen, bevor ein Worktree-Start akzeptiert
wird; die Änderung eines einzigen Zeichens erfordert erneut eine Genehmigung. Noch
führt nichts den Befehl aus. Siehe
[Den Setup-Befehl eines Ordners genehmigen](/de/reference/session-runtime-api/#den-setup-befehl-eines-ordners-genehmigen).

**Aufräumen** verweigert das Verwerfen nicht committeter oder nicht zusammengeführter
Arbeit. **Auch Worktree und Branch verwerfen** erlaubt diesen Verlust ausdrücklich;
von Git ignorierte Dateien werden mit dem Worktree entfernt. Worktrees teilen die
Git-Metadaten des Repositorys. Siehe die
[CLI-Referenz](/de/reference/cli/#command-olivares-session-start) für
Worktree-Optionen und die [Session-Runtime-API](/de/reference/session-runtime-api/)
für Profilberechtigungen, Lebenszyklusaufrufe und Isolationsgrenzen.
