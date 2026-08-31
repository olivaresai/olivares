---
title: "TAK-Server-Posture & governte Cursor-on-Target-Ingestion"
description: >-
  Governen Sie ein TAK-Deployment: Lesen Sie die TAK-Server-Posture offline aus
  CoreConfig.xml (mit optionalem Live-Versions-Probe) und nehmen Sie
  Cursor-on-Target-Ereignisse über UDP/TCP als governtes Minimal-Data-Signal auf
  — Koordinaten und Details verlassen den Connector nie, jede Kante bleibt
  ehrlich approximate.
sidebar:
  order: 9
---

Die Source `tak` governt ein **TAK**-Deployment (Team Awareness Kit) als eine
weitere Oberfläche. Sie erfüllt zwei voneinander unabhängige Aufgaben, von
denen Sie jede einzeln aktivieren können:

- **TAK-Server-Posture** — meldet die Konfiguration eines Servers (seine
  Inputs und deren Protokolle/Ports, TLS-/Keystore-Einstellungen und das
  Backend für Zertifikatssignaturen) als Minimal-Data-Findings. Die
  **fundierte** Source ist die servereigene `CoreConfig.xml`, die **offline**
  von der Festplatte gelesen wird; ein optionaler Live-**Versions-Probe** ist
  das Einzige, was über das Netzwerk gelesen wird. Die TAK-Föderation wird
  **nicht** gelesen.
- **Governte CoT-Ingestion** — empfängt **Cursor-on-Target**-Ereignisse an
  den eigenen **UDP**- und **TCP**-Listenern des Connectors und wandelt jedes
  in eine governte Zugriffskante um.

Der Connector ist **read-first**: Er schreibt niemals auf einen TAK-Server,
tritt nie einer Föderation bei und sendet nie ein Payload erneut aus. Wenn
weder Zugangsdaten noch ein Listener konfiguriert sind, ist er ein ehrlicher
**No-op** — er gibt nichts aus, statt eine Posture für ein nie kontaktiertes
Deployment zu erfinden.

## Was er ausgibt

| Feld | Wert |
|---|---|
| Signalquelle | `cot` |
| Modus | `write` — ein CoT-Emitter *trägt* Situational-Awareness-Zustand zum Feed bei |
| Ursprung | die `uid` des Emitters, **standardmäßig gehasht** (`cot_uid_mode`) |
| Confidence | immer **`approximate`** — Base-CoT ist nicht authentifiziert (siehe unten) |
| Findings | Drop-Track-Abbrüche, Unbounded-Error-Ereignisse und aggregierte Listener-Ablehnungen (Rate-Limit / Oversize / Malformed / Conn-Limit) |

## 1. Posture: Server zuerst offline lesen

Die fundierte Posture-Source ist die servereigene Konfigurationsdatei. Bei
einer Paketinstallation liegt sie unter `/opt/tak/CoreConfig.xml`. Verweisen
Sie den Connector darauf, liest er die konfigurierten Inputs,
TLS-/Keystore-Einstellungen und das Backend für Zertifikatssignaturen,
**ohne das Netzwerk zu berühren**. Das Element `<federation>` wird bewusst
nicht modelliert, sodass keine Föderations-Posture erzeugt wird.

Der Live-**Versions-Probe** ist optional und ergänzt nur die laufende Version.
Da TAK Server Operatoren per **mTLS** authentifiziert, arbeitet der Probe
deny-closed: Wenn Sie bei aktivierter `posture` einen `server_url` setzen, das
Client-Zertifikat aber **weglassen**, **verweigert der Connector den Start**,
statt anonym abzufragen und eine nicht authentifizierte Posture zu melden.
`server_url` muss `https` verwenden.

```jsonc
// OLIVARES_SOURCES_CONFIG — posture only
{
  "sources": [{
    "name": "tak-server",
    "kind": "tak",
    "tenant": "<tenant-id>",
    "config": {
      "core_config_path": "/opt/tak/CoreConfig.xml",
      "server_url": "https://takserver.example.mil:8443",
      "client_cert": "${TAK_CLIENT_CERT_PEM}",
      "client_key":  "${TAK_CLIENT_KEY_PEM}"
    }
  }]
}
```

## 2. Ingestion: CoT über UDP und TCP empfangen

Aktivieren Sie einen Listener, und der Connector empfängt CoT — eine Nachricht
pro **UDP**-Datagramm, eine Nachricht pro **TCP**-Verbindung
(„open-squirt-close“). Sie verweisen einen TAK-Feed oder CoT-Clients auf die
Listen-Adresse des Connectors; der Connector ist der Consumer und baut keine
Verbindung zum Server auf, um Daten abzurufen.

```jsonc
// OLIVARES_SOURCES_CONFIG — ingest
{
  "sources": [{
    "name": "tak-edge",
    "kind": "tak",
    "tenant": "<tenant-id>",
    "config": {
      "cot_udp_listen": "0.0.0.0:6969",
      "cot_multicast_group": "239.2.3.1",
      "cot_tcp_listen": "0.0.0.0:8087",
      "allow_public_bind": true,
      "feed_ref": "tak"
    }
  }]
}
```

### Konfigurationsschlüssel (aus dem mitgelieferten Connector-Deskriptor)

| Schlüssel | Typ | Standard | Secret | Bedeutung |
|---|---|---|:--:|---|
| `core_config_path` | string | — | nein | Pfad zu `CoreConfig.xml` (Paketinstallationen: `/opt/tak/CoreConfig.xml`) — die fundierte Offline-Posture-Source |
| `server_url` | string | — | nein | Basis-URL des TAK Servers (z. B. `https://takserver.example.mil:8443`). Optional: aktiviert nur einen Live-Versions-Probe |
| `version_path` | string | `/Marti/api/version` | nein | Marti-Versionsendpunkt unter `server_url`. Konfigurierbar, weil die API-Referenz von tak.gov einen Account erfordert |
| `client_cert` | string | — | **ja** | PEM-Client-Zertifikat für TAK-Server-mTLS, als Referenz |
| `client_key` | string | — | **ja** | Privater PEM-Schlüssel für das Client-Zertifikat, als Referenz |
| `ca_cert` | string | — | nein | PEM-CA-Bundle für das TAK-Server-Zertifikat. Leer verwendet den Trust Store des Hosts |
| `posture` | bool | `true` | nein | TAK-Server-Posture-Findings ausgeben |
| `request_timeout` | duration | `15s` | nein | Timeout pro Request an die TAK-Server-API |
| `feed_ref` | string | `tak` | nein | Stabile Referenz für diesen CoT-Feed — die `source_ref`, auf die eine Sourcescope-Bindung (`source_type=data`) den Scope beschränkt |
| `cot_udp_listen` | string | — | nein | UDP-Listen-Adresse für CoT (z. B. `127.0.0.1:6969`). Leer deaktiviert die UDP-Ingestion |
| `cot_tcp_listen` | string | — | nein | TCP-Listen-Adresse für CoT open-squirt-close (z. B. `127.0.0.1:8087`). Leer deaktiviert die TCP-Ingestion |
| `cot_multicast_group` | string | — | nein | Optionale Multicast-Gruppe, der der UDP-Listener beitritt (TAKs SA-Standard ist `239.2.3.1`) |
| `cot_max_event_bytes` | int | `65536` | nein | Maximale Bytezahl eines CoT-Ereignisses |
| `cot_max_detail_bytes` | int | `32768` | nein | Maximale Bytezahl des undurchsichtigen `<detail>`-Bereichs eines CoT-Ereignisses |
| `cot_rate_limit_eps` | int | `500` | nein | Höchstens akzeptierte CoT-Ereignisse pro Sekunde über alle Listener; Überschuss wird verworfen und gezählt |
| `cot_max_tcp_conns` | int | `128` | nein | Höchstens gleichzeitig bestehende TCP-CoT-Verbindungen |
| `cot_uid_mode` | string | `hash` | nein | Wie eine `uid` den Connector verlässt: `hash` (Standard, Einweg) oder `raw`. Eine uid identifiziert ein Gerät, und ein Gerät identifiziert seinen Träger |

## Ports (TAK Server Configuration Guide v5.2)

Als Kontext für das System, das Sie integrieren: Die eigenen Listener des
Connectors binden an den von Ihnen konfigurierten `host:port`; die Beispiele
verwenden diese Nummern nur, weil sie vertraut sind.

| Port / Gruppe | Konvention |
|---|---|
| **8089** | TLS-CoT-Streaming-Input — der authentifizierte Client↔Server-Kanal |
| **6969** + Multicast **239.2.3.1** | Multicast-Gruppe für Situational Awareness (SA) |
| **8087** | Konventioneller Input-Port; das kanonische Beispiel des Leitfadens bindet ihn als **UDP**. Das Protokoll ist konfigurierbar — 8087 ist **nicht** grundsätzlich TCP |
| **8088** | `stcp` — unverschlüsselter TCP-Input, **nur für Tests** |
| **8443** | Administrative Web-UI |
| **8446** | Zertifikatsregistrierung |

## Datenschutz: Koordinaten und Details verlassen den Connector nie

CoT ist ein Positionsmeldeprotokoll — das PII-reichste Signal, das dieses
Produkt aufnimmt — daher wird Minimal Data strikt erzwungen:

- `lat` / `lon` / `hae` des `<point>` **verlassen den Connector niemals.**
  Eine Koordinate ist der Standort einer Person; das Produkt zeichnet auf,
  dass ein Ereignis empfangen wurde, von welchem Emitter und von welchem
  CoT-Typ — niemals, wo sich jemand befindet.
- Der undurchsichtige `<detail>`-Bereich verlässt den Connector nie; nur seine
  **Größe** und ein **SHA-256-Digest** werden aufbewahrt, sodass identische
  Payloads korreliert werden können, ohne das Payload zu speichern.
- Die `uid` des Emitters wird **standardmäßig gehasht**
  (`cot_uid_mode=hash`, Domain-separiert und Einweg). `raw` ist ein
  ausdrückliches Opt-in des Operators.

## Confidence: Eine CoT-uid ist keine authentifizierte Identität

Base-CoT enthält **keine Authentifizierung** — jeder Host, der einen Listener
erreichen kann, kann eine beliebige `uid` behaupten. TLS von TAK Server
schützt den Client↔**Server**-Kanal (Port 8089); es sagt nichts über ein
Ereignis aus, das dieser Connector an seinem eigenen unverschlüsselten
UDP-/TCP-Listener empfängt. Deshalb wird **jede** Kante eines
Base-CoT-Listeners konstruktionsbedingt mit **`approximate`** bewertet — es
gibt keinen Codepfad, der `attributed` zurückgibt.

:::caution[Eine `uid` ist eine Behauptung, kein Beweis]
Lesen Sie eine CoT-`uid` als *„Ein Emitter, der diese ID beansprucht, hat in
den Feed veröffentlicht“*, nicht als authentifizierte Identität. Sie wäre nur
authentifiziert, wenn ein Listener mTLS terminieren und die uid an das
Peer-Zertifikat binden würde.
:::

## Scoping: Feed mit einer Sourcescope-Bindung governen

Der Feed ist eine governte First-Class-Source. Eine **sourcescope**-Bindung
beschränkt, wer ihn mit `source_type=data` und `source_ref=<feed_ref>` nutzen
darf, entlang jeder Subject-Achse — **session / agent / user / user_group /
role**. Die Effekte sind `allow` (Standard) oder `forbid`, und **`forbid` ist
absolut** (`forbid` überschreibt `allow`).

```http
POST /v1/m/sourcescope/bindings
Content-Type: application/json

{
  "source_type": "data",
  "source_ref":  "tak",
  "scope_tree":  "agent",
  "scope_ref":   "agent:recon-planner",
  "effect":      "allow",
  "enabled":     true
}
```

Setzen Sie `"effect": "forbid"` (etwa zusammen mit
`"scope_tree": "user_group"`), um den Zugriff einer ganzen Gruppe zu
entziehen, selbst wenn ein `allow` vorhanden ist.

## Lizenz und Clean-Room-Herkunft

Das CoT-Wire-Format ist eine **Clean-Room**-Implementierung, die
ausschließlich anhand der **öffentlich freigegebenen MITRE-Spezifikation**
geschrieben wurde — es wurde kein TAK- oder ATAK-Quellcode gelesen, kopiert
oder abgeleitet:

- *The Developer's Guide to Cursor on Target*, Butler, MITRE, Aug. 2005 — DTIC
  **ADA637348**, MITRE **Case #06-0249**.
- `Event-PUBLIC.xsd`, das CoT-Base-Event-Schema (Version 2.0) — MITRE
  **Case #11-3895**.
- *TAK Server Configuration Guide* **v5.2** — für die
  Port-/Protokollkonventionen.

ATAK-CIV und TAK Server sind **GPLv3** und für den Connector (Apache-2.0)
tabu; die Lizenzgrenzenprüfung erzwingt dies. Beide tragen eine
US-Bundeskennzeichnung **„Distribution A“**. Das ist eine
**Freigabeerklärung einer Behörde, keine Softwarelizenz** — die Code-Trees sind
GPLv3. Das öffentlich freigegebene Schema und der Leitfaden von MITRE
legitimieren eine Clean-Room-Implementierung.

## Ehrliche Grenzen

- **Keine Mesh-/Funk-Bearer** — nur UDP und TCP; kein Serial, TAK Mesh oder
  Funk.
- **Keine ATAK-/WinTAK-Plugins** — der Connector implementiert keinen
  TAK-Client für Endbenutzer.
- **Keine TAK-Föderation** — er *beobachtet* lediglich, dass Föderation
  konfiguriert ist; er föderiert nie.
- **Kein Link-16 / MIL-STD** oder zertifizierungsgebundenes taktisches
  Protokoll und **keine Iron-Bank-/DoD-Akkreditierung** — getrennte, optionale
  Kundenpfade.
- **Das CoT-`<detail>`-Subschema wird nicht modelliert** — nur das
  Base-Ereignis wird geparst; Detail bleibt undurchsichtig, größenbegrenzt und
  als Digest gespeicherte Bytes.
- **UDP-Verlust ist nicht zählbar** — Backpressure verlangsamt die Listener;
  bei UDP verwirft der **Kernel** Datagramme, bevor dieser Prozess sie sieht,
  und diese Verluste können nicht gezählt werden. Nur Ereignisse, die der
  Connector tatsächlich abgelehnt hat, werden zu Ablehnungs-Findings
  aggregiert.

## Verwandte Themen

- [Eine Source anbinden](/de/how-to/connect-a-source/) — das Connector-Modell und
  die Taxonomie der ehrlichen Tiers.
- [Governen und genehmigen](/de/how-to/govern-and-approve/) — das
  Autorisierungsmodell, in das eine Sourcescope-Bindung eingebunden wird.
- [Connectoren & Coverage-Tiers](/de/reference/connectors/) — der vollständige
  Katalog.
