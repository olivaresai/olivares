---
title: "Das Sicherheitsmodell"
description: "Die secure-by-design Posture hinter Olivares AI — warum read-first, minimal-data, deny-by-default und ein manipulationserkennbares Audit die tragenden Sicherheitsentscheidungen sind, nicht die Bedrohungsaufzählung."
---

Olivares AI ist ein Sicherheitsprodukt, das **innerhalb der eigenen Infrastruktur des
Kunden** läuft und eine Karte dessen erstellt, was jeder AI-Agent erreichen kann. Das macht
es sowohl höchst sensibel als auch höchst wertvoll für einen Angreifer: Ein Defekt in diesem
Produkt ist ein Breach des Estate des Kunden. Die Messlatte ist daher die höchste, und die
Posture ist darauf ausgelegt, **von Anfang an einen Enterprise-Pentest und ein Audit zu
bestehen**, statt nachträglich gehärtet zu werden.

Diese Seite erklärt die **Posture** — die in das Design eingebauten Sicherheitsentscheidungen
und warum sie so sind, wie sie sind. Sie wiederholt bewusst **nicht** das formale
Bedrohungsmodell: Die STRIDE-Analyse je Komponente und der Trust-Boundary-Datenfluss leben
auf der Seite [Bedrohungsmodell](/de/explanation/security/threat-model/). Lesen Sie jene Seite
für *was schiefgehen könnte und wo*; lesen Sie diese für *warum die Architektur so geformt
ist, dass diese Dinge schwer werden*.

:::note[Posture, keine Recon-Karte]
Diese Dokumentation beschreibt die Sicherheits-Posture, nicht die Angriffsfläche. Sie zählt
keine internen Berechtigungs-Strings, keine Geheimnis-Dateipfade und kein Port-Layout eines
Deployments auf. Diese gehören in Härtungsmaterial für Betreiber, nicht in öffentliche Docs.
:::

## Read-first: niedriges asymmetrisches Risiko

Der Core **beobachtet**; er schaltet sich nicht dazwischen. Die Access Map wird aus Signalen
rekonstruiert, die das Estate ohnehin emittiert — OpenTelemetry, Datenbank-Audit,
Cloud-Audit-Trails und (als nicht-kooperativer Backstop) eBPF — und der Collector ist **nie
im Datenpfad des Agenten**.

Das ist eine Sicherheitsentscheidung, bevor es eine Produktentscheidung ist. Ein Inline-Enforcer,
der vor jeder Agentenaktion sitzt, ist ein Single Point of Failure: Wenn er hängt oder
abstürzt, kann er die Produktion mit sich reißen, und er wird zu einem hochwertigen Ziel —
gerade *weil* er im Pfad ist. Ein Read-first-Beobachter trägt das gegenteilige,
**asymmetrische** Risikoprofil. Fällt der Collector aus, hört er auf zu *sehen* — er stoppt
nicht den Agenten und bricht nicht die Produktion. Der Worst-Case-Ausfall eines Beobachters
ist eine Lücke in der Sichtbarkeit, kein Ausfall.

Dieselbe Eigenschaft neutralisiert die naheliegende Umgehung. Der Collector läuft als
separater, privilegierter Dienst **außerhalb der Kontrolle des Agenten**, sodass ein Agent,
der seine eigene Telemetrie deaktiviert, den Collector nicht zum Schweigen bringt — und der
eBPF-Backstop zeichnet die Aktion weiterhin auf Kernel-Ebene auf. Ein bekannter Agent, der
plötzlich verstummt, wird selbst als Signal behandelt, nicht ignoriert.

## Minimale Daten: Was nicht gespeichert wird, kann nicht leaken

Der Graph speichert **Relationen**, nicht Inhalte. Eine Kante hält fest, dass ein Agent eine
Ressource berührt hat, in welchem Modus (read / write / readwrite), aus welcher Signalquelle,
mit welcher Confidence und wann. Er speichert **nicht** das ausgeführte SQL, den Request-Body,
das Geheimnis oder die PII darin. Wo ein Wert nur zur Deduplizierung benötigt wird, behält das
Produkt einen Einweg-Hash, nie den Wert selbst.

Das leitende Prinzip ist unverblümt: **Was nicht gespeichert wird, kann nicht leaken.** Das
einzelne sensibelste Asset im System — die Access Map — ist zugleich dasjenige, das bewusst
aus den am wenigsten sensiblen Daten gebaut ist.

Die Felder, die am ehesten Geheimnisse oder PII tragen (ein Tool-Input, ein vollständiger
Befehl), werden **vor der Persistierung maskiert**. Die Maskierung wird nicht dem
guten Verhalten des Handlers überlassen: Die Engine erzwingt sie auf dem Schreibpfad und
ersetzt einen als sensibel markierten Wert durch einen Hash, bevor er überhaupt geschrieben
wird — als Backstop, selbst wenn ein Handler es vergisst. Der Collector liest **Identitäten** —
eine Datenbankrolle, einen Anwendungsnamen, einen IAM-Principal — keine Credential-Werte oder
Payloads. Er ist kein Daten-Sniffer.

:::note[Abdeckung ist gestuft, und das Produkt sagt es]
Die Read/Write-Genauigkeit hängt davon ab, was der zugrunde liegende Store offenlegt. Sie ist
hoch bei Stores mit nativem Audit (SQL, Object Storage, Warehouses), verlustbehaftet bei einigen
Dokument-/Vektor-Stores und **passiv nicht rekonstruierbar** bei anderen. Wo read versus write
nicht bestimmt werden kann, wird die Kante als `unknown` markiert, und die Attribution kollabiert
zu `approximate`, wenn ein geteiltes Service-Konto die Identität je Agent verbirgt. Das Produkt
zeigt dies ehrlich an, statt Gewissheit zu fabrizieren — siehe
[Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/).
:::

## Opake, widerrufbare Tokens statt JWT

Die Authentifizierung verwendet **opake Bearer-Tokens**, keine JWTs. Das Token ist ein
zufälliges Handle; alle Autorität lebt serverseitig, gebunden an einen Datensatz, den die Engine
kontrolliert. Das ist eine Posture-Wahl. Ein in sich geschlossenes JWT ist ein dauerhafter,
offline verifizierbarer Träger von Claims, der vor Ablauf umständlich zu widerrufen ist; ein
opakes Token ist **sofort widerrufbar**, indem sein serverseitiger Datensatz invalidiert wird,
trägt keine eingebetteten Claims, die leaken oder fehlvertraut werden könnten, und hält die
Tenant-Bindung unter der Kontrolle der Engine statt in einer Signatur, die der Client hält.
Session- und API-Tokens sind unterschiedliche Arten, und der Tenant wird aus der eigenen Bindung
des Tokens aufgelöst — eine Anfrage, deren Tenant-Header ihrem Token widerspricht, wird
**abgelehnt**, nicht abgeglichen.

## Keine Standard-Credentials, einmaliger Setup-Token

Das häufigste Versagen eines selbst gehosteten Produkts ist ein **Standard-Credential**.
Olivares AI liefert **keines** aus. Beim ersten Boot gibt die Engine einen **einmaligen,
einmal nutzbaren Setup-Token** auf Standard-Output aus; der Administrator nutzt ihn, um den
ersten Benutzer anzulegen, und danach ist er verbraucht. Es gibt kein eingebautes Konto, kein
geteiltes Passwort und nichts, das man zu ändern vergessen könnte. (Ein Demo-Seed existiert nur
zur Evaluierung; er trägt ein öffentliches Passwort und **weigert sich, an etwas anderes als
Loopback zu binden**, sodass er nie zu einem Produktions-Standbein werden kann.)

## Deny-by-default Autorisierung, ein ABAC-Seam, das nur einschränkt

Die Autorisierung ist **deny-by-default**. Rollenbasierte Zugriffskontrolle gewährt nichts, was
ihr nicht ausdrücklich aufgetragen wurde zu gewähren. Über RBAC sitzt ein attributbasiertes
Policy-Seam — der Betreiber kann eine eingebettete reine Go-Policy-Engine, einen externen
Policy-Dienst über HTTP oder keines von beidem betreiben, alles hinter einer Schnittstelle —
und die kritische Invariante ist, dass die **ABAC-Schicht den Zugriff nur verengen, nie
erweitern kann.** Eine Policy kann eine Berechtigung entziehen; sie kann nie eine Berechtigung
gewähren, die RBAC nicht bereits erlaubt hat. Diese Reihenfolge bedeutet, dass eine fehlkonfigurierte
oder zu freizügige Policy kein Privilege-Escalation-Pfad werden kann: Das Schlimmste, was eine
schlechte Policy tun kann, ist Leute auszusperren, nicht sie hineinzulassen.

## Den Graphen anzusehen ist eine privilegierte, tenant-gescopte, auditierte Aktion

Weil die Access Map ein mächtiges Reconnaissance-Werkzeug ist, behandelt das Design **das
Lesen als privilegierte Aktion**, nicht als Standardfähigkeit. Sie wird ab einer Editor-Rolle
aufwärts gewährt und ist der niedrigsten Viewer-Rolle **nie** verfügbar. Jeder Lesevorgang ist
**auf den Tenant gescopt** — ein Kunde kann nie das Estate eines anderen sehen — und **jeder
Lesevorgang wird im Audit-Ledger festgehalten**: wer wessen Agenten-Access-Map ansah und wann.
Verteidigung ist hier bewusst geschichtet: Privileg, Tenant-Isolation und Self-Audit zusammen,
sodass selbst legitimer Zugriff auf die sensibelste Ansicht eine nachvollziehbare Spur hinterlässt.

Hier wird auch die Responsible-Use-Linie des Produkts gezogen. Olivares AI ist **defensiv**
gerahmt — es hilft Verteidigern, ihr eigenes Estate zu sehen und zu governen. Es ist kein
Command-and-Control-Framework und scannt nicht die Credentials anderer Leute. Diese Linie wird
im [Bedrohungsmodell](/de/explanation/security/threat-model/) explizit gehalten.

## Append-only, hash-chained, signiertes Audit — mit externem Export als der echten Kontrolle

Das Audit-Ledger ist **append-only** und **hash-chained**: Jeder Datensatz trägt den Hash des
vorherigen, sodass jede stille Änderung die Kette bricht und erkennbar ist. Über der Kette
erzeugt die Engine **Ed25519-signierte** Checkpoints, sodass das Ende nicht ohne den
Signierschlüssel umgeschrieben werden kann.

Das Produkt ist ehrlich über die Grenze eines On-Box-Ledgers: Ein Angreifer mit voller Kontrolle
über das Datenverzeichnis und den On-Box-Schlüssel könnte im Prinzip eine gefälschte Kette
neu signieren. Die Signatur je Ereignis verteidigt gegen den **rein datenbankbezogenen**
Kompromiss — Injection, ein gestohlenes Backup oder Replica, eine Umgehung der Row-Level
Security — und gegen das Löschen von Checkpoints; sie verteidigt für sich allein nicht gegen
eine vollständige Host-Kompromittierung.

Die **echte Anti-Tamper-Kontrolle ist also extern**. Das Ledger wird in ein **WORM/SIEM**-System
exportiert, das der Kunde kontrolliert, in Standardformaten (`cef`, `leef`, `syslog`, `otlp`,
`otlp_envelope`, `otlp_log_record`, `ocsf`), mit Sequenznummer, vorherigem
Hash, Hash und Signatur, und **nie PII**. Sobald eine
Kopie in unveränderlichem Speicher außerhalb des Produkts liegt, kann ein Angreifer, der den
Olivares-Host kompromittiert, nicht zurückgreifen und das umschreiben, was das SIEM bereits hält.
Diese unveränderliche externe Kopie — nicht die On-Box-Kette allein — ist es, was ein
Enterprise-Auditor verlangt, und es ist das, was native Telemetrie nicht liefert.

:::note[Zwei Wege off-box: Pull und ein echter Push]
Das verifizierbare Ledger erreicht ein SIEM auf zwei Wegen. Der **Pull**-Export
(`GET /v1/audit/export`) ist immer verfügbar und ist das Artefakt, das ein Betreiber
archiviert. Ein **Push** ist real, sobald er konfiguriert ist: ein
`audit.recorded`-Eventing-Abonnement startet eine Ledger-Pumpe pro Tenant, die jeden
versiegelten Datensatz **mindestens einmal** über den dauerhaften, SSRF-geschützten
Transport mit Retry/Dead-Letter zustellt (`modules/siemforward/forwarder.go`,
verdrahtet in `cmd/olivares/boot.go`). `NopForwarder` gilt, wenn kein Forwarding
konfiguriert ist — er ist nicht die einzige existierende Implementierung. Das
[Splunk-How-to](/de/how-to/forward-audit-to-splunk/) dokumentiert beide Pfade; die
Signaturverifikation erfolgt off-box, gegen den öffentlichen Schlüssel.
:::

## TLS standardmäßig an, kein Klartext-Fallback, mTLS für Remote-Collectoren

Der Transport ist **standardmäßig verschlüsselt und fällt geschlossen aus**. TLS ist an, und
es gibt **keinen stillen Fallback auf Klartext** — eine Verbindung, die nicht abgesichert werden
kann, wird abgelehnt, nicht herabgestuft. Ein Klartext-Modus existiert strikt für
Localhost-Entwicklung und muss explizit angefordert werden; er ist nie der Standard und nie der
Produktionspfad.

In der verteilten Topologie **pushen** Remote-Collectoren zum zentralen Core (es gibt keinen
eingehenden Listener auf dem Produktions-Host, was die offene Port-Fläche des Collectors bei
null hält), und dieser Kanal kann **gegenseitiges TLS** mit einem verifizierten Client-Zertifikat
verlangen. Verschlüsselung im Ruhezustand wird vom Deployment bereitgestellt — Full-Disk-,
Dateisystem- oder Datenbank-Verschlüsselung — statt durch ein Pragma auf Produktebene, mit
strengen Dateiberechtigungen auf dem Datenverzeichnis.

## Die Lizenz ist nur Attestierung — der offene Kern wird nie gegatet

Die kommerzielle Lizenz wird **offline** mit einer Ed25519-Signatur verifiziert, und im
**offenen (AGPL-)Kern** ist sie eine **Attestierung, kein Feature-Gate**: Nichts im offenen
Produkt schaltet sich je aufgrund einer Lizenzprüfung ab. Kommerzielle Add-ons werden für
eine bezahlte Laufzeit lizenziert — ein Recht, das mit der Laufzeit endet — doch jede
Konsequenz daraus ist eine lokale, offline getroffene Entscheidung im kommerziellen Build;
es gibt keinen Remote-Kill-Switch, und die Prüfung der Lizenz nimmt keinen Kontakt zu uns
auf. Der Bezug dessen, wofür bezahlt wurde, sehr wohl: Das Abonnement ist der
Zugangsnachweis, mit dem die kommerziellen Add-ons, ihre Updates und ihre Patches bezogen
werden — das SUSE/Novell-Modell, beschrieben unter
[Self-Hosting](/de/how-to/self-hosting/). Das zählt besonders für
den Air-Gapped-Fall: Das Produkt muss seine Sicherheitsaufgabe weiter erfüllen — beobachten,
aufzeichnen, auditieren — unabhängig vom Lizenzstatus, denn eine Sicherheitskontrolle, die bei
einem Lizenzproblem stillschweigend degradiert, ist selbst eine Schwachstelle. Der Widerruf wird
über den Abo-Ablauf gehandhabt, nicht durch Lahmlegen der laufenden Engine.

<a id="self-hosted-daten-bleiben-innerhalb-des-kundenperimeters"></a>

## Self-hosted: Der Kunde bestimmt, was seinen Perimeter überschreitet

Die stärkste strukturelle Eigenschaft des Designs ist: Es gibt **keine verpflichtende
Telemetrie und standardmäßig keinen Egress der Control Plane**. Den Kundenperimeter
überschreitet nur, was der Kunde dafür konfiguriert — Aufrufe an seine Modell-APIs, die von ihm
eingerichteten SIEM-/Webhook-Ausgaben und ein externer Embedding-Anbieter, falls er einen
bereitstellt. Olivares AI läuft auf den eigenen Hosts des Kunden; die Data
Plane (die Collectoren) läuft **immer** auf Kundeninfrastruktur; und es gibt **kein
Telemetry-Home** — im laufenden Betrieb wird nichts als Nebeneffekt an Olivares AI gesendet.
Der Anbieter wird nur erreicht, wenn der Kunde etwas von ihm anfordert — `olivares upgrade`
oder ein Abo-Download kommerzieller Add-ons und ihrer Updates — und er sieht die Access Map des
Kunden nicht.

Das ist eine direkte, vertretbare Antwort auf **DSGVO- und Datenresidenz**-Anforderungen: Die
einzelnen Datenübertragungen werden vom Kunden bereitgestellt; damit bestimmt und belegt er die
Residenz selbst, statt sie vom Anbieter zugesichert zu bekommen. Und es macht
die **Air-Gapped**-Topologie zu einem erstklassigen Deployment — alles lokal, **null Egress**,
Offline-Lizenz — statt zu einem Nachgedanken, für Estates, die ganz ohne ausgehendes Netz laufen
müssen. Siehe die Leitfäden [Self-Hosting](/de/how-to/self-hosting/) und
[Air-Gap-Installation](/de/how-to/air-gap-install/).

:::tip[Für Audit designen, später zertifizieren]
Die Architektur ist darauf gebaut, **auf** die Kontrollen **abzubilden**, nach denen SOC 2,
ISO 27001 und der EU AI Act suchen — Audit-Logging, Zugriffskontrolle, Integrität,
Verschlüsselung, Change Management — sodass sie die Prüfung besteht, wenn die Zeit kommt. Die
formale Zertifizierung ist ein späterer, separater Schritt; das Design ermöglicht sie, es
behauptet sie nicht. Die Seite [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) ist der
verbindliche Vertrag darüber, was heute gebaut ist versus designt.
:::

## Warum diese Entscheidungen zusammenhalten

Keine dieser Entscheidungen steht für sich allein. Read-first hält das Produkt aus dem
Wirkungsradius genau der Systeme heraus, die es überwacht. Minimal-data verkleinert, was ein
Breach des Produkts überhaupt offenlegen könnte. Opake Tokens, keine Standard-Credentials,
deny-by-default RBAC und ein nur einschränkendes ABAC-Seam bedeuten, dass die Autorität klein,
widerrufbar und unmöglich versehentlich zu erweitern ist. Das hash-chained, signierte, extern
exportierte Ledger macht die Ehrlichkeit des Produkts **verifizierbar** statt nur versprochen.
Und Self-Hosting bedeutet: keine verpflichtende Telemetrie und standardmäßig kein Egress der
Control Plane. Den Kundenperimeter überschreitet nur, was der Kunde dafür konfiguriert — seine
Modell-APIs, die von ihm eingerichteten SIEM-/Webhook-Ausgaben und ein externer
Embedding-Anbieter, falls er einen bereitstellt. Die Posture ist das
Sicherheitsargument; das [Bedrohungsmodell](/de/explanation/security/threat-model/) ist der Ort,
an dem jedes davon gegen eine konkrete Bedrohung geprüft wird.
