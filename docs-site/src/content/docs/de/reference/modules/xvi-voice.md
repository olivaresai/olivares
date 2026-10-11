---
title: "Modul XVI — Voice- & Echtzeit-Agenten"
description: >-
  Die Observe-and-Govern-Ebene für konversationelle/Echtzeit-Agenten. Sie regelt,
  wer eine Voice-Session öffnen darf, mit welchem Modell und Provider, unter einer
  default-DENY-Policy — und verfolgt Session-Metadaten unter striktem Verbot
  jeglichen Audio- oder Transkript-Inhalts.
---

Modul XVI regelt **konversationelle und Echtzeit-Agenten**. Es ist eine
**Observe-and-Govern**-Ebene: Es implementiert **kein** Voice-SDK neu (Realtime API,
WebRTC, ASR oder TTS) und öffnet selbst nie einen Media-Stream. Es entscheidet, *wer*
eine Voice-Session öffnen darf, mit *welchem* Modell und Provider, unter *welcher*
Policy, und verfolgt die Metadaten dieser Session — nie ihren Inhalt.

## Was es ist

Das Öffnen einer Voice-Schnittstelle wird als **privilegierte Aktion** behandelt,
nicht als freie Operation. Die Policy ist **default-DENY**: Eine Session ohne
erlaubende Policy wird verweigert. Ein Öffnen ist **zweiphasig** und
**human-in-the-loop-gegated** über das [Approval-Gate](/de/how-to/govern-and-approve/);
es ist an einen `plan_hash` gebunden, sodass eine Genehmigung nicht still auf ein
stärkeres Modell hochgestuft werden kann (Anti-TOCTOU), wird dem **realen Principal**
auditiert (nie `system`) und **append-only** belegt. Das Modul selbst ruft nie einen
Provider auf — die Actuation verlässt es über eine separate Dispatch-Naht.

Die andere Hälfte ist **Beobachtung**: Das Modul verfolgt ausschließlich
Session-Metadaten — abgeleiteter Zustand (live/idle/ended, zur Lesezeit aus der
Aktualität der Aktivität berechnet, ohne gespeicherte Lifecycle-Spalte),
Turn-Zählungen, Dauer, Latenz (ehrlicher Durchschnitt und Maximum aus realen Samples)
und BCP-47-Sprache. Daraus erhebt es Governance-**Findings**: eine Policy-Verletzung,
wenn die Telemetrie einen Agenten/ein Modell/einen Provider nennt, den keine Policy
erlaubt, ein Degraded-Latency-Finding, wenn die Latenz eine Policy-SLA überschreitet,
und ein Ungoverned-Open-Finding, wenn ein Öffnen ohne verdrahtetes Gate versucht wird
— die Lücke wird sichtbar gemacht und das Öffnen wird dennoch verweigert.

## Vertrag & Entitäten

Das Modul deklariert drei Entitäten im gemeinsamen Datenmodell:

| Entität | Veränderbarkeit | Zweck |
|---|---|---|
| **session** | veränderlich (Upsert) | Session-Metadaten; **null Inhalt** |
| **policy** | veränderlich | Governance-Deklaration — wer mit welchem Modell/Provider öffnen darf (default-DENY) |
| **decision** | **append-only** | unveränderliches Ledger der Öffnungs-/Schließungs-Entscheidungen |

Eine Policy matcht auf Agent, erlaubtes Modell und erlaubten Provider (jeweils
spezifisch oder Wildcard), mit optionalen Session-Minuten- und Latenz-SLA-Grenzen.
**Keine matchende Policy bedeutet DENY.** Das decision-Ledger erfasst jedes
`open_request`, `open` und `close` mit seinem Policy-Verdikt, Gate-Status und
Ergebnis-Status. Lesezugriff ist die Viewer-Rolle und höher; das Deklarieren einer
Policy und das Öffnen einer Session sind administrative, mandantengebundene und
auditierte Aktionen. Diese Modulrouten werden in der separaten **Beta**-
[Modulrouten-Referenz](/reference/api-beta/) veröffentlicht, nicht im stabilen Kernvertrag —
ihre feldgenauen Formen leben in den typisierten Interfaces des Produkts. Geldbeträge stehen **nicht** hier; FinOps (Modul XI) besitzt
Kosten.

## Was es konsumiert & produziert

Das Modul besitzt eine deny-closed-Ingestion-Naht — sein eigenes
`voice.telemetry.observed`-Event — über das eine **In-Process**-Sonde
Session-Metadaten einspeisen würde. Die Leitung ist **datenminimal per Konstruktion**:
Der Telemetrie-Parser trägt eine Allow-List und **verwirft das gesamte Event**, wenn er
einen verbotenen Schlüssel sieht, sodass niemals Audio, Transkript-Text, ASR/TTS-Text,
Prompt-/Response-Inhalt oder Sprecher-PII persistiert werden kann. Das einzige
gehaltene Transkript-Signal ist ein Einweg-Hash eines *externen*
Transkript-**Locators** — Beleg, dass ein Transkript existiert, niemals das Transkript.
Governance-Findings werden als [`finding.reported`](/de/reference/events/) mit gehashtem
Detail nach Commit emittiert.

## Actuate-Status

Ein geregeltes Öffnen dispatcht **live**: Sobald der Betreiber einen Voice-
Dispatcher bereitstellt, prägt ein genehmigtes Öffnen ein **serverseitiges ephemeres
Credential** und gibt es mit Verbindungskoordinaten zurück. Die Sitzungskonfiguration
des Betreibers liefert Stimme und Turn-Detection; ein konfiguriertes Modell ersetzt
das angefragte Modell. Ohne konfiguriertes Modell nutzt der Dispatcher das angefragte,
von der Mandanten-Policy erlaubte Modell. Der Master-Key des Providers verlässt nie
den Server. Ohne Bereitstellung ist die Dispatch-Naht **deny-closed**: Ein
genehmigtes Öffnen wird ehrlich als „deklariert, nicht geöffnet“ aufgezeichnet.

## Ein geregeltes Öffnen konfigurieren und testen

Aktivieren Sie das bestehende Modul mit `olivares modules on voice`. Die Engine
speichert die Auswahl und startet einmal neu, falls sich die laufenden Module
ändern; `olivares modules ls` zeigt, ob es läuft. Voice benötigt gemäß Modulspezifikation
FinOps und Governance. Ausschalten behält Policies, Sitzungsmetadaten und Entscheidungs-Ledger.

Der Dispatcher wird auf dem Engine-Host über `OLIVARES_VOICE_DISPATCH_CONFIG`
bereitgestellt, den absoluten Pfad einer betreibereigenen JSON-Datei. Nur das
Engine-Konto darf sie lesen. Provider-Master-Keys gehören in diese Datei, niemals
in CLI-Argumente, Policy-Zeilen oder ein Client-Verbindungsbundle. Für einen
OpenAI-Adapter sieht sie so aus:

```json
{
  "providers": [
    {"ref": "openai", "kind": "openai", "api_key": "<server-held provider key>"}
  ],
  "policies": [
    {
      "agent_ref": "contact-agent",
      "provider_ref": "openai",
      "model": "<your permitted realtime model>",
      "voice": "marin",
      "max_duration_seconds": 60
    }
  ]
}
```

Setzen Sie die Umgebungsvariable für den Engine-Dienst und starten Sie ihn neu.
Eine angegebene unlesbare oder ungültige JSON-Datei verhindert den Start. Ohne
Dispatcher-Konfiguration bleibt das Verhalten „deklariert, nicht geöffnet“.
Die Betreiberdatei wählt Provider-Adapter und Sitzungseinstellungen; die Voice-Policy
des Mandanten autorisiert separat Agent, Modell und Provider der Anfrage. Verwenden
Sie in beiden dieselben Modell- und Provider-Referenzen.

Nach der Anmeldung mit `olivares login` deklarieren Sie diese Policy und fordern
eine Genehmigung an:

```sh
olivares voice policies set --agent-ref contact-agent \
  --allowed-model-ref '<your permitted realtime model>' --allowed-provider-ref openai \
  --max-session-minutes 1 --max-latency-ms 300
olivares voice sessions open --session-ref contact-1 --agent-ref contact-agent \
  --model-ref '<your permitted realtime model>' --provider-ref openai -o json
```

Die erste Anfrage liefert `op_status: requested`, eine `approval_ref` und CLI-Exit
7. Sie öffnet keine Medienverbindung und prägt kein Provider-Credential. Die
erforderlichen unabhängigen Genehmigenden bestätigen die Referenz auf der Governance-
Genehmigungsseite oder mit `olivares governance approvals approve <approval-ref>`.
Bei neuen Anfragen über die standardmäßige lokale Genehmigungsbrücke kann der
Antragsteller seine eigene Anfrage nicht genehmigen, auch nicht mit einem anderen
Credential desselben Kontos. Wiederholen Sie denselben Open mit
`--approval-ref <approval-ref>`. Policy-Verweigerung oder noch ausstehende Genehmigung
liefert 403 und CLI-Exit 3; Adapterfehler liefern 502. Budget- und Estate-Stop-Prüfungen
gelten weiterhin.

Eine erfolgreiche konfigurierte Anfrage liefert `op_status: dispatched`.
`dispatch_ref` ist ein JSON-String mit kurzlebigem `credential`, `connect`-Koordinaten,
`transport`, Modell und Ablaufzeit. Behandeln Sie diese Antwort als Credential:
nicht in Berichte oder Logs kopieren. Bei OpenAI tauscht der Client ein SDP-Angebot
an der zurückgegebenen `connect`-URL mit dem kurzlebigen Credential aus und besitzt
dann die WebRTC-Medienverbindung. Das Prägen eines Credentials allein beweist keine
verbundene Medienverbindung. Das Entscheidungs-Ledger behält einen SHA-256-Fingerprint
eines Credential-haltigen Bundles, nicht das Verbindungs-Credential. Auch ältere
gespeicherte Bundles werden beim Lesen fingerprinted; bestehende Append-only-Zeilen
werden nicht umgeschrieben. Einfache Provider-Handles behalten ihren Wert.

Prüfen Sie die gespeicherten Metadaten und Entscheidungen:

```sh
olivares voice sessions get contact-1 -o json
olivares voice sessions decisions contact-1 -o json
olivares voice policies ls -o json
```

Verwenden Sie über Engine-Neustarts hinweg dasselbe Datenverzeichnis. Die Policy
und Append-only-Entscheidungen bleiben nach Neustart sowie Aus- und Einschalten von
Voice verfügbar; Provider-Credentials müssen separat bereitgestellt bleiben. Die
JSON-Befehle oben verwenden dieselben mandantenbezogenen `/v1/m/voice`-Routen wie die API.

:::caution[Ehrliche Grenzen]
- **Genehmigungsattribution hat einen Geltungsbereich.** Die standardmäßige lokale
  Brücke behält bei neuen, von Menschen gestarteten Opens den authentifizierten
  Antragsteller. Bestehende Genehmigungen behalten ihre gespeicherte Attribution.
  Eine explizit konfigurierte Service-Token-Brücke schreibt Anfragen ihrem Service-
  Credential zu und bietet nicht dieselbe Trennung der initiierenden Person.
- **Beobachtung benötigt einen konfigurierten Produzenten.** Die optionale OpenAI-
  Realtime-SIP-Anrufebene verwendet `OLIVARES_VOICE_CALL_CONFIG` für Webhook-Prüfung,
  Mandanten- und Projektattribution zusammen mit den Dispatcher-Provider-Credentials.
  Ohne diese Konfiguration oder einen In-Process-Telemetrieproduzenten bleibt die
  Observe-Hälfte leer. Ein WebRTC-Credential füllt weder Turn-Zahlen noch Latenz.
  Out-of-Process-Plugins können das Modul-Event nicht über die gRPC-Control-Plane
  veröffentlichen, die keinen Event-RPC bereitstellt.
- **Der Client besitzt die Medienverbindung der Sitzung.** Das Modul implementiert
  weder einen WebRTC-Client noch schließt es dessen Audioverbindung. Der optionale
  SIP-Controller ist ein separater Pfad. Ein lokaler Protokolltest mit synthetischem
  Audio qualifiziert weder Vendor-Sprache, Abrechnung, SIP-Beobachtung noch Medienabbau.
- **Die Konsole hat einen eigenen Umfang.** Die Voice-Ansicht bearbeitet Policies
  und zeigt Sitzungen, Entscheidungen und Metadatenströme. Dispatcher-Bereitstellung
  und Client-Medienverbindung sind davon getrennt; eine API- oder CLI-Journey
  qualifiziert keine Browseraktion.
- **Kein Inhalt, niemals.** Dies ist eine harte Eigenschaft der Leitung, keine
  Einstellung: Das Schema hat keine Inhaltsspalte und der Parser verwirft unbekannte
  Schlüssel. Latenz wird als ehrlicher Durchschnitt/Maximum aus realen Samples gezeigt —
  nie ein fabrizierter p50/p95.
- **Kein „Stall"-Finding.** Das Enden einer Voice-Session ist normale Stille (wie ein
  fertiger Agent). Ohne ehrliche Baseline wäre ein Stall-Finding ein False Positive,
  daher wird es bewusst ausgelassen.
- **Pre-1.0.** Wie ein Großteil der Plattform befindet sich dieses Modul in der Tiefe
  im Design-Stadium — siehe [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/).
:::

## Verwandt

- [Modulkatalog](/de/reference/modules/overview/) — wo Modul XVI sitzt und sein Actuate-Status.
- [Event-Bus-Referenz](/de/reference/events/) — `finding.reported` trägt die Voice-Findings.
- [Modul IV — Orchestrierung](/de/reference/modules/iv-orchestration/) — die verwandte Dispatch-Naht (Live-Fire).
- [Modul X — Modell- & Provider-Routing](/de/reference/modules/x-models/) — welche Modelle eine Policy erlauben darf.
- [Govern and approve](/de/how-to/govern-and-approve/) — das zweiphasige Open-Gate in der Praxis.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — die Observe/Govern/Actuate-Aufteilung.
