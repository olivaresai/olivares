---
title: Einen Anbieter hinzufügen und Claude Code, Codex oder Grok starten
description: >-
  Registrieren Sie einen API-Schlüssel bei der Steuerungsebene, testen Sie die
  Verbindung ohne etwas auszugeben, binden Sie ihn an ein Anbieterprofil und
  starten Sie die erste Sitzung — über die Konsole und über die CLI.
---

Diese Seite ist die erste Stunde der **Anbieter**-Ebene: wohin Ihr API-Schlüssel
gehört, woran Sie erkennen, dass er funktioniert, und wie eine Sitzung damit startet.

In 26.10 ist eine Umgebungsvariable auf dem Server weiterhin eine Antwort auf die erste Frage,
und diese Variablen funktionieren weiterhin. Sie sind nicht mehr der einzige Weg, und sie
sind nicht mehr der Weg, mit dem eine neue Operatorin anfängt.

## Was die drei Wörter bedeuten

Je ein Satz, denn das Produkt hat sie vermischt, und die Ablehnungen, die es gibt,
benennen sie getrennt.

| Wort | Was es ist |
|---|---|
| **Anbieter** | Ein API-Schlüssel oder ein lokaler Modellendpunkt, eine optionale `base_url` und die Art (`anthropic`, `openai`, `xai`, `gemini`, `openai_compatible`, `ollama`). |
| **Anbieterprofil** | Eine Identität auf dieser Maschine: welche offizielle CLI läuft, und unter welchem Konfigurations- und Benutzer-Home. |
| **Sitzung** | Ein gestarteter Kindprozess, unter einem Profil, mit den Zugangsdaten eines Anbieters. |

## 1. Anbieter hinzufügen

### Über die Konsole

1. Öffnen Sie **Anbieter** (KI → Umgebungen → Anbieter).
2. Wählen Sie **Anbieter hinzufügen**.
3. Wählen Sie den Anbieter, geben Sie ihm einen Namen, den Sie in einer Auswahl
   wiedererkennen, und fügen Sie den Schlüssel ein. Lassen Sie den Endpunkt leer,
   sofern Sie nicht auf Ihr eigenes Gateway zeigen; ein `openai_compatible`-Anbieter
   verlangt einen, weil es keinen offiziellen Endpunkt gibt, den man annehmen könnte.
4. Bestätigen Sie.

Die Engine versiegelt den Schlüssel im Ruhezustand und gibt einen Hinweis aus vier
Zeichen zurück. **Der Schlüssel wird nie wieder zurückgegeben**, auch nicht direkt
nach dem Schreiben. Wenn Sie ihn verlieren, wechseln Sie ihn: es gibt keinen Lesepfad,
der ihn wiederherstellt.

### Über die CLI

```sh
# Der Schlüssel wird von stdin gelesen. Er ist nie ein Flag-Wert: ein Flag legt die
# Zugangsdaten in der Shell-Historie und in der Prozesstabelle ab.
olivares provider add --kind anthropic --name "Anthropic (prod)" < key.txt

# Oder aus einer Umgebungsvariablen Ihrer eigenen Shell:
ANTHROPIC_KEY=sk-ant-... olivares provider add \
  --kind openai --name "Codex" --key-env ANTHROPIC_KEY
```

## 2. Verbindung testen

```sh
olivares provider test prv_01J8ABCDEF
```

Der Test fragt den Anbieter, welche Modelle er anbietet. **Er sendet keine Completion
und gibt nichts aus.**

Er antwortet eines von drei Dingen, und das sind drei verschiedene Fragen:

| Ergebnis | Was es bedeutet | Was zu tun ist |
|---|---|---|
| `ok` | Der Anbieter hat geantwortet und die Zugangsdaten akzeptiert. | Nichts. Binden Sie sie. |
| `refused` | Der Anbieter hat geantwortet und die Zugangsdaten abgelehnt. | Wechseln Sie den Schlüssel. |
| `unreachable` | Es kam keine Antwort. | Prüfen Sie Endpunkt, Netz und jeden Proxy. **Das sagt nichts über den Schlüssel** — erzeugen Sie ihn nicht neu. |

Ein Anbieter, den Sie nicht getestet haben, wird als **nicht getestet** angezeigt, nie
als funktionierend. Zugangsdaten zu registrieren ist eine Absicht; ein Test ist eine
Tatsache.

## 3. Profil registrieren und Anbieter binden

Explizit angegebene Home-Verzeichnisse müssen auf der Maschine, die die
Steuerungsebene ausführt, bereits existieren. Der Server validiert sie dort und
legt fehlende explizit angegebene Homes nicht an: ein leeres Ersatz-Home gäbe
einer Sitzung eine Anbieteridentität, die niemand konfiguriert hat.

```sh
olivares agent profile create \
  --driver claude \
  --config-home /home/ops/.claude \
  --user-home /home/ops \
  --name "Claude (Arbeit)" \
  --auth-source managed_injection \
  --provider prv_01J8ABCDEF
```

`--auth-source` entscheidet, woher die Anbieteridentität des Kindes kommt, und die
beiden Werte sind keine Rückfallkette:

- `provider_account_home` — die bereits in den Homes des Profils gespeicherte
  Anmeldung. Olivares injiziert nichts und liest diese Datei nie.
- `managed_injection` — Zugangsdaten, die die Engine liefert. Mit einem gebundenen
  Anbieter sind es dessen.

Die Abkürzung verbindet Erkennung, Registrierung und Anbieterbindung. Wenn weder
`--config-home` noch `--user-home` angegeben wird, verwaltet die Engine die
Home-Verzeichnisse. Ohne `--provider` wählt sie das Profil, das eine neue Sitzung
des Treibers verwenden würde; mit `--provider` erstellt sie ein Profil mit eigenen
Home-Verzeichnissen:

```sh
olivares agent deploy claude --provider prv_01J8ABCDEF
```

Installieren Sie das Werkzeug unter **KI-Werkzeuge**, bevor Sie diese Abkürzung
verwenden. Sie meldet Sie nicht bei Ihrem Anbieterkonto an.

Zum späteren Binden (oder Neubinden):

```sh
olivares provider bind prv_01J8ABCDEF --profile ppf_01J8ZZZZZZ
```

Die Engine lehnt Zugangsdaten ab, die der Treiber des Profils nicht lesen kann. Ein
OpenAI-Schlüssel auf einem Claude-Profil ist eine Ablehnung, die beide benennt — beim
Binden und noch einmal beim Start — und keine Sitzung, die mitten im Handshake
scheitert.

| Anbieterart | Treiber, die sie lesen | Mit eigener `base_url` |
|---|---|---|
| `anthropic` | `claude`, `opencode` | `claude` |
| `openai` | `codex`, `opencode` | `codex` |
| `xai` | `grok`, `opencode` | `grok` |
| `gemini` | `gemini-cli` |  |
| `openai_compatible` | `codex` | `codex` |
| `ollama` | `codex`, `opencode` | `codex`, `opencode` |

Für `anthropic`, `openai` und `xai` akzeptiert OpenCode nur den eigenen Endpunkt des Anbieters: Lassen Sie `base_url` leer. Verwenden Sie `codex` für einen `openai_compatible`-Anbieter und `codex` oder `opencode` für `ollama`.

## 4. Die erste Sitzung starten

```sh
olivares agent workspace add /srv/projects/acme --name acme --mode ro --dlp deny
olivares agent session create \
  --name acme-1 \
  --workspace ws-123 \
  --provider-profile ppf_01J8ZZZZZZ
olivares agent session attach run-123
```

`--provider-profile` wählt ein Profil ausdrücklich aus. Wenn profilbasierte Starts
aktiviert sind, ermittelt die Engine beim Weglassen ein Profil für das
Standardwerkzeug Claude Code: Sie verwendet ein passendes vorhandenes Profil oder
erstellt eines für die eigene Anmeldung des Werkzeugs, andernfalls für einen
kompatiblen Anbieterdatensatz. Dafür ist `sessions:profile:write` erforderlich; ohne
diese Berechtigung wählen Sie ein Profil ausdrücklich aus. Ablehnungen bei der
Profilermittlung und beim Start gelten weiterhin.

In der Konsole ist derselbe Weg **Einstieg → Agenten und erste Sitzung** oder
**Sitzungen → Neue Sitzung**.

## Wechsel und Widerruf

```sh
olivares provider rotate prv_01J8ABCDEF < neuer-schluessel.txt   # versiegelt neu
olivares provider rm prv_01J8ABCDEF --yes                        # unumkehrbar
```

Der Wechsel ersetzt den Wert unter derselben Referenz, also funktionieren alle
gebundenen Profile weiter und der **nächste** Start verwendet den neuen Schlüssel. Eine
bereits laufende Sitzung behält die Zugangsdaten, mit denen sie gestartet ist. Der
vorherige Verbindungstest wird gelöscht: ein Urteil über Zugangsdaten, die es nicht
mehr gibt, belegt nichts über die, die sie ersetzen.

Der Widerruf zerstört den versiegelten Wert und verweigert jeden künftigen Start
**namentlich**. Der Datensatz und die Bindungen bleiben absichtlich erhalten: ein
Profil, das stillschweigend nichts mehr benennt, läse sich als ein Profil, das niemand
konfiguriert hat. Ein Widerruf hier widerruft den Schlüssel nicht beim Anbieter; das
tun Sie in dessen eigener Konsole.

## Was die Engine ablehnt, und warum

| Sie sehen | Es bedeutet |
|---|---|
| `provider credentials cannot be stored on this deployment` | Es ist kein versiegelter Tresor verdrahtet. Die Engine verweigert das Speichern eines Schlüssels, statt einen zu speichern, den sie nicht schützen kann. |
| `the provider connection test is not available` | Es ist keine Sonde verdrahtet. **Das Starten ist nicht betroffen.** |
| `a … credential is not readable by driver …` | Art und Treiber passen nicht zusammen. Siehe die Tabelle oben. |
| `the provider this profile is bound to is revoked` | Binden Sie einen aktiven Anbieter. |
| `this launch has two endpoints` | Das Inferenz-Gateway der Installation und die eigene `base_url` des Anbieters gelten beide. Entfernen Sie eines; die Engine wählt nicht. |
| `the registered provider credential could not be opened` | Der Start wird verweigert. **Er fällt nicht auf Zugangsdaten des Hosts zurück** — das würde Ihre Sitzung auf einem Konto ausführen, das Sie nicht gewählt haben. |

## Die Umgebungsvariablen, und wo sie weiterhin gelten

Für Claude-Profile mit `managed_injection`, die keinen Anbieter nennen, liefert
`OLIVARES_SESSION_RUNTIME_WIF` oder `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` die
Inferenz-Zugangsdaten des Hosts. Ein Profil mit einem gebundenen Anbieter nutzt
dessen Zugangsdaten; bei einem Fehler wird der Start ohne Fallback verweigert.
Ein Profil mit `provider_account_home` nutzt die autorisierte Anmeldung des Tools
und benötigt keine der beiden Variablen.

## Verwandt

- [Eine Anbieter-Session betreiben](/de/how-to/operate-provider-sessions/)
- [Ihre erste Stunde](/de/how-to/first-hour/)
- [CLI-Referenz](/de/reference/cli/)
