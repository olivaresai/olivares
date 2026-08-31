---
title: Ehrlichkeit & Grenzen
description: >-
  Was Olivares AI heute leistet, was sich im Design-Stadium oder nach v1 befindet
  und was das Produkt bewusst nicht tut. Keine erfundenen Fähigkeiten.
---

Eine Control Plane für KI ist ein Sicherheitsprodukt. Wenn sie überzeichnet, was
sie abdeckt, vermittelt sie ein falsches Sicherheitsgefühl — was schlimmer ist als
gar kein Werkzeug. Deshalb ist diese Seite der explizite Vertrag darüber, **was
heute läuft, was geplant ist und was bewusst außerhalb des Geltungsbereichs liegt.**
Die restliche Dokumentation hält sich daran: Befehle in Tutorials und How-to-Guides
sind so gemeint, dass sie wie geschrieben ausgeführt werden, und wo das Produkt
etwas noch nicht abdeckt, sagt die Seite das, statt es zu suggerieren.

## Was heute läuft

- **Das einzelne Binary baut, bootet und erreicht einen befüllten
  Zugriffsgraphen.** Das `olivares`-Binary kompiliert zu einem einzigen statischen
  Artefakt mit eingebetteter Web-UI. Es mit dem Demo-Estate zu booten
  (`serve --seed-demo`) und den Pfad *discover → R/RW-Graph →
  Permitted-vs-Observed-Drift → Inventar* zu durchlaufen, wird von der Testsuite
  **end-to-end** geprüft. Das [Tutorial](/de/tutorials/zero-to-graph/) reproduziert
  genau diesen Pfad.
- **Die Ersteinrichtung ist credential-frei.** Eine frische Installation hat
  **keine Standard-Credentials**; die Engine gibt beim ersten Boot ein einmaliges,
  nur einmal verwendbares Setup-Token aus.
- **Die REST-API und das Audit-Ledger sind real.** Die [API-Referenz](/reference/api/)
  wird aus dem eigenen OpenAPI-3.1-Vertrag des Produkts gerendert. Das Audit-Ledger
  ist append-only und hash-chained mit Ed25519-signierten Checkpoints und kann in
  mehreren SIEM-Formaten exportiert werden.
- **Releases sind signiert und offline verifizierbar.** Signatur, SLSA-Provenienz,
  SBOM und OpenVEX lassen sich allesamt [ohne Netzwerkzugang verifizieren](/de/how-to/verify-a-release/),
  und das Produkt liefert ein [Air-Gap-Bundle](/de/how-to/air-gap-install/). **Es gibt
  noch kein getaggtes Release**, dies beschreibt also, was ein Release enthalten wird,
  kein Artefakt, das Sie heute herunterladen und verifizieren können — derselbe
  Vorbehalt, den `SECURITY.md` nennt.

## Open Core — was offen ist vs. Enterprise

Das Produkt ist **Open Core**: Das Standard-Binary (AGPL) ist die gesamte
Governance-Plattform, und eine kleine, **additive** kommerzielle Linie
(`enterprise/`, ausschließlich mit `-tags enterprise` gebaut, nie im öffentlichen
Binary) enthält die reservierten Funktionen. Zwei Grenzen sind für den täglichen
Gebrauch relevant, und das offene Build beantwortet sie ehrlich, statt sie
vorzutäuschen:

- **SSO ist offen für einen einzelnen IdP.** Single-IdP-Login — **OIDC**
  (Authorization Code + PKCE) und **SAML 2.0** (signierte Responses, Anti-Replay) —
  läuft im Standard-Binary **ohne** `-tags enterprise`. **Mehr als einen aktiven
  IdP** zu betreiben (pro Tenant / nach Domain), die **SSO-Durchsetzung**
  (SSO-Pflicht / Passwort-Login blockieren) und **managed SCIM** sind die
  reservierte Enterprise-Linie; das Aktivieren eines zweiten aktiven IdP gibt
  `multi_idp_requires_enterprise` zurück — eine explizite Produktgrenze, niemals ein
  vorgetäuschtes 501.
- **Es gibt kein Benutzerlimit — Konten sind in jeder Edition unbegrenzt.** Community,
  Business, die Add-ons und Enterprise self-hosted lassen alle unbegrenzt viele
  Benutzerkonten zu, unabhängig vom Lizenzzustand: gültig, abgelaufen oder gar keine.
  Das Limit von drei aktiven Konten, das vor dem 2026-07-27 galt, wurde vollständig
  entfernt (die Seat-Naht bleibt als Kompatibilitäts-No-op im Code und lehnt nichts
  ab), und ein Lizenzablauf begrenzt, deaktiviert oder löscht nie ein Konto. Das
  kommerzielle Modell ist eine laufzeitbasierte Berechtigung für die Add-ons, niemals
  eine Abrechnung pro Sitzplatz.
- **Der Rest der Plattform ist offen.** Die gesamte Governance-Schleife — Inventar,
  die R/RW-Access-Map, RBAC-/ABAC-/Cedar-Policy, das versiegelte Audit-Ledger,
  FinOps, Compliance, SIEM-Egress, MCP, HA/verteilt — läuft im offenen Binary ohne
  Lizenzprüfung. Die additiven `enterprise/`-Add-ons (Multi-IdP-Federation, Content
  Firewall/DLP, Hook-Hardening, der kompilierte Threat-Intel-Katalog, Server-Tool-Egress, der
  CyberArk-Conjur-Connector und der Incident-Close-Loop)
  sind neuer Code, der nie im offenen Produkt enthalten war, keine daraus entfernten
  Funktionen. Die Lizenzvalidierung im offenen Binary ist **attestierungs-only** —
  sie aktiviert, deaktiviert oder blockiert nie etwas (siehe
  [Open Core & Lizenzierung](/de/explanation/open-core-and-licensing/)).

## Was sich im Design-Stadium oder vor 1.0 befindet

Olivares AI ist **pre-1.0**. Die Produkt-Designdokumente sind explizit, dass große
Teile der Plattform sich stellenweise im Design-Stadium befinden, selbst wo die
Engine bereits läuft. Behandeln Sie die Tiefe auf Modulebene als **Work in
Progress**, sofern eine Seite nichts anderes angibt.

- **Die Abdeckung der R/RW-Map ist by design gestaffelt.** Die Genauigkeit hängt
  davon ab, was die Quelle nachweisen kann. Sie ist **sauber** bei Stores mit
  nativem Audit (SQL via pgAudit, Object Storage via CloudTrail, Warehouses/Lakes),
  **verlustbehaftet** bei einigen Stores (Document/Vector) und **passiv nicht
  rekonstruierbar** bei anderen (z. B. Redis, SQLite, D1) — wo sich Lesen vs.
  Schreiben nicht bestimmen lässt, wird die Kante als `unknown` markiert. Die
  Zuordnung ist **firm**, wenn eine Quelle eine Identität pro Agent trägt, und fällt
  auf **`approximate`** zurück, wenn ein gemeinsam genutztes Service-Konto sie
  verbirgt. Das Produkt zeigt dies ehrlich; es erfindet keine Gewissheit.
- **Die kanonischen R/RW-Quellen sind im Standard-`serve` verdrahtet.** Die
  Composition-Root registriert die Observer auf Host-Ebene — `pgaudit`,
  `s3cloudtrail`, `ebpf`, `runtime` und die `mcp`-Introspektionsquelle — neben den
  Warehouse-/Lake-Observern (snowflake/databricks/bigquery/mssql/oracle/mongo/redshift/gcs/
  azure-blob/iceberg/openlineage/delta-sharing), alle konfigurierbar über
  `OLIVARES_SOURCES_CONFIG` (der
  [Quickstart](/de/start/quickstart/) verdrahtet eine echte `pgaudit`-Quelle gegen das
  Standard-Binary, und der Smoke-Test überprüft das). Die
  **Dokumentenquellen** der Wissensbasis (gdrive/confluence/notion/sharepoint/s3content)
  sind bewusst *keine* Laufzeitquellen — sie werden bei Bedarf durch
  Knowledge-Ingest-Anfragen geladen. Die
  [Connectors-Referenz](/de/reference/connectors/) kennzeichnet jede Art.
- **Der Standard ist ein einzelnes Binary; der verteilte Event-Bus existiert und
  ist über seine Semantik ehrlich.** Der Standard läuft als ein Binary mit einem
  **In-Process**-Event-Bus. Der **Datenpfad vom Remote-Collector zum Core ist gebaut
  und ausgeliefert**: Edge-Collectors führen Source-Connectors lokal aus und pushen
  Beobachtungen über verified-client-cert-mTLS an einen zentralen Core, ohne
  eingehenden Listener (der `collector`-Modus). Der **verteilte Event-Bus** wurde mit
  der Scale-out-Arbeit ausgeliefert: ein Hybrid, der das In-Process-Fan-out
  für die lokale Zustellung beibehält (blockierender Backpressure, kein lokaler
  Verlust) und Events über **NATS** über Knoten hinweg überbrückt, aktiviert durch
  `OLIVARES_BUS_CONFIG` (eine fehlkonfigurierte Bus-Konfiguration **lässt den Boot
  fehlschlagen**, statt den Bus still zu partitionieren). Die knotenübergreifende
  Zustellung ist ehrlich als **at-most-once** dokumentiert — Bridge-Trennungen und
  -Verluste werden in dedizierten Metriken gezählt, niemals still
  ([Monitoring](/de/how-to/monitor-with-prometheus/)).
- **Governte *Aktuierung* hat drei ehrliche Zustände: live, on-demand und Seam.**
  Das Produkt beobachtet und governt heute breit. Eine kleine Menge an Aktuierungen
  ist **live im Standard-Binary** ohne Bereitstellung: FinOps-Budgetdurchsetzung (ein
  durchsetzendes Budget an seinem Limit verweigert die Ausgabe), der
  Benachrichtigungs-Dispatch-Transport (er routet, sobald ein Ziel konfiguriert ist),
  die Security-Detective-Findings/Guardrails und der In-Process-Synthetic-Sandbox-Runner
  (per Konstruktion isoliert). Mehrere weitere sind **on-demand verdrahtet** — das
  Backend ist gebaut und verdrahtet, bleibt aber **deny-closed oder degradiert, bis
  ein Operator es bereitstellt** via Env-Konfiguration: Modul VII (Deploy)
  `apply`/`retire` (ein `503`, bis ein Executor bereitgestellt ist), Modul-IV-Orchestrierung
  *fire* und Modul-XVI-Voice-Dispatch (beide deny-closed, bis ein Dispatcher
  konfiguriert ist), die OS-isolierte Sandbox-/Red-Team-Laufzeit (synthetisch /
  DEGRADED, bis bereitgestellt), modellgestütztes **semantisches** Retrieval (lexikalisch
  und standardmäßig nur öffentlich) und Modell-*Ausführung* in Modul X (`503`, bis ein
  Inferenz-Credential bereitgestellt ist). Was ein **deklarierter, deny-closed Seam**
  ganz ohne Backend bleibt, ist die ruhende Voice-Telemetrie-Sonde (der verteilte
  Event-Bus hat diese Liste verlassen, als die NATS-Bridge ausgeliefert wurde — siehe
  oben). Der [Modulkatalog](/de/reference/modules/overview/) kennzeichnet den
  Govern/Observe- und Actuate-Status jedes Moduls; nichts behauptet zu handeln, wo es
  das nicht tut. (Dies korrigiert eine frühere Lesart, die Voice, die
  Sandbox-/Red-Team-Laufzeit und semantisches Retrieval als "live" auflistete — sie
  sind on-demand: verifiziert gegen einen Standard-Boot `serve --seed-demo`,
  2026-06-08.)
- **Air-Gap gilt für die Control Plane, nicht für Claude-Inferenz.** Die Control
  Plane läuft vollständig self-hosted und kann air-gapped werden (SQLite Single-Node,
  signiertes Offline-Release, Air-Gap-Bundle). **Claude selbst ist nicht
  self-hostbar** — Anthropic veröffentlicht keine Gewichte —, sodass jede
  Claude-*Inferenz* die API von Anthropic erreicht, direkt oder über
  Bedrock/Vertex/Foundry. "Air-gapped" bedeutet hier die *Governance- und
  Beobachtungs*-Plane und dass ihre Daten innerhalb Ihres Perimeters bleiben; es
  bedeutet **nicht**, dass Claude offline läuft. Modelle, die Sie wirklich selbst
  hosten (z. B. via vLLM/Ollama unter Modul XXIII), können air-gapped laufen;
  vermittelte Frontier-Modelle können das nicht.
- **Modul-Routen sind ein separater, Beta-Vertrag.** Die Modul-Endpunkte (zum
  Beispiel der Access-Map-Graph und der Drift) sind nicht Teil des stabilen
  53-Pfad-Core-Vertrags; sie werden als separates **Beta**-Dokument veröffentlicht —
  die [Modul-Routen-Referenz](/reference/api-beta/) (ausgeliefert unter
  `/openapi.beta.json`). Beta bedeutet, dass sich die Formen mit Vorankündigung
  ändern können, und der Detailgrad auf Feldebene lebt weiterhin in den typisierten
  Interfaces des Produkts. Die [Core-API-Referenz](/reference/api/) dokumentiert die
  stabile Oberfläche; sie ist nicht die gesamte Produktoberfläche.

## Was das Produkt bewusst **nicht** tut

- **Keine offensiven Funktionen.** Olivares AI ist **kein**
  Command-and-Control-Framework und scannt **nicht** die Credentials anderer Leute.
  Die Access Map ist ein mächtiges Aufklärungswerkzeug *für Verteidiger, um ihr
  eigenes Estate zu governen* — sie anzusehen ist eine privilegierte,
  tenant-scoped, vollständig auditierte Aktion. Diese defensive Linie ist
  beabsichtigt und wird explizit gehalten (siehe
  [Threat Model](/de/explanation/security/threat-model/)).
- **Kein nativer Splunk-S2S-Forwarder.** Das Weiterleiten an Splunk ist eine
  dokumentierte *Posture* — richten Sie einen Universal Forwarder auf eine Datei,
  die die Control Plane anhängt, oder pushen Sie über Splunk HEC — **kein** nativer
  Splunk-zu-Splunk-Emitter. Das [Splunk-How-to](/de/how-to/forward-audit-to-splunk/)
  ist explizit darüber, welcher Stream welcher ist.
- **Keine ausgehenden Webhooks im REST-Vertrag.** Das OpenAPI-Dokument definiert
  keine `webhooks`. Signierte ausgehende Zustellung existiert als interner
  Benachrichtigungs-*Destination-Connector*, und der
  SCIM-Security-Event-Token-Endpunkt ist ein *eingehender* Empfänger — keiner von
  beiden ist ein OpenAPI-Webhook. Siehe [API-Referenz](/reference/api/).
- **Modell-Fine-Tuning (Modul XXIII) ist post-v1.** Sein Fehlen ist eine
  Entscheidung, keine Lücke.

## Wo die Docs eine Lücke upstream vermerken

Einige Dinge, die diese Dokumentation aufzeigt, sind **Lücken im Produkt**, die an
die Teams gemeldet wurden, die den jeweiligen Vertrag besitzen, statt sie hier zu
übertünchen:

- Die committete OpenAPI-Datei, die die Site rendert, wird nun **aus dem eigenen
  Generator der Engine regeneriert — und in der CI byte-für-byte dagegen geprüft**,
  sodass sie ihr nicht mehr hinterherhinkt (die frühere Endpunkt-Lücke wurde
  abgeglichen). Die frühere Unter-Dokumentation der Formatliste von
  `/v1/audit/export` wurde ebenfalls upstream behoben: die Zusammenfassung und die
  Bad-Request-Meldung werden beide aus der Format-Registry der Engine gebaut
  (`audit.FormatList()`) und können daher nicht erneut auseinanderlaufen — dieser
  Abschnitt hält den Eintrag fest, weil frühere Editionen dieser Docs die Lücke
  meldeten, und weil dieselbe Fäule bis 2026-07-25 auch `leef` und `ocsf` aus der
  Hilfe und der Completion der CLI verborgen hatte.
- Der **Push**-Pfad des Audit-Ledgers wurde mit der SIEM-/ITSM-Interop-Arbeit
  ausgeliefert: ein `audit.recorded`-Eventing-Abonnement schaltet eine
  Ledger-Pumpe pro Tenant ein, die versiegelte Datensätze **at-least-once** an einen
  konfigurierten Sink weiterleitet (Splunk HEC, Sentinel, Datadog, New Relic oder
  ein HMAC-signierter Webhook). Der **Pull**-Export bleibt die richtige Form für
  WORM-Archivierung und Offline-Re-Verifizierung. Siehe
  [Push an Ihr SIEM](/de/how-to/cookbook/push-to-siem/) und das
  [Splunk-How-to](/de/how-to/forward-audit-to-splunk/). Was weiterhin **nicht**
  existiert, ist ein nativer Splunk-**S2S-Protokoll**-Emitter (siehe unten).

Wenn Sie einen Befehl finden, der sich nicht wie dokumentiert verhält, ist das ein
Bug in den Docs oder im Produkt — bitte melden Sie ihn.
