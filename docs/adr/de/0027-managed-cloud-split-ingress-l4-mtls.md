> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0027: Managed-Cloud-Ingress — L4-Passthrough für Collector-mTLS, L7 für die Control-Plane-API

- **Status:** accepted (managed cloud; this record creates no infrastructure)
- **Date:** 2026-08-02
- **Deciders:** Fran Olivares
- **References:** ADR-0012 (collectors push to the core over gRPC + mTLS), ADR-0028
  (managed-cloud database), ADR-0029 (managed-cloud regions), ADR-0009 (append-only
  hash-chained audit); the platform decision record for the managed cloud; AWS Elastic
  Load Balancing documentation, consulted 2026-08-02:
  `https://docs.aws.amazon.com/elasticloadbalancing/latest/network/network-load-balancers.html`,
  `https://docs.aws.amazon.com/elasticloadbalancing/latest/network/edit-target-group-attributes.html`,
  `https://docs.aws.amazon.com/elasticloadbalancing/latest/application/configuring-mtls-with-elb.html`.

## Kontext und Problemstellung

ADR-0012 legte die Ingestion-Topologie fest: Collector laufen in der Infrastruktur des
Kunden und **pushen** Beobachtungen über gRPC mit gegenseitigem TLS, und der Core
**terminiert dieses mTLS selbst**.

Es lohnt sich, genau zu benennen, was dies bewirkt, denn die ungenaue Fassung dieses Satzes
ist falsch und würde, wenn man sie glaubte, zu einer tragenden Annahme. Die Zulassung auf der
Collector-Ebene beruht auf **zwei unabhängigen Faktoren**:

1. **Ein Transport-Gate.** Der Server verlangt und verifiziert ein Client-Zertifikat, dessen
   Kette bis zur konfigurierten Collector-CA reicht. Dies beweist den Besitz eines Schlüssels,
   dessen Zertifikat wir ausgestellt haben; das Zertifikat wird nicht ausgewertet, um daraus
   ein Subjekt zu gewinnen, und benennt keinen Principal.
2. **Ein Bearer-Principal.** Die authentifizierte Identität, auf deren Grundlage die
   Autorisierung und die Audit-Kette (ADR-0009) handeln, stammt aus dem Bearer-Token der
   Anfrage, nicht aus dem Zertifikat.

Beide werden **im eigenen Prozess des Produkts** durchgesetzt. Kein Intermediär bürgt für
einen der beiden Faktoren. Um diese Eigenschaft geht es in diesem Eintrag: nicht „das
Zertifikat ist die Identität“, sondern „kein Intermediär bürgt für einen der beiden Faktoren“.

Die Managed Cloud ist das erste Deployment, das einen Load Balancer vor diese Binärdatei
setzt. Dasselbe Deployment stellt auch eine gewöhnliche öffentliche HTTPS-Oberfläche bereit
— REST API, Konsole, Admin —, die genau die gegenteilige Behandlung benötigt: ein
verwaltetes öffentliches Zertifikat, eine Web Application Firewall sowie Host-/Pfad-Routing.
Ein einzelner Ingress kann nicht beides bereitstellen, ohne auf einer Seite etwas aufzugeben.

## Entscheidungstreiber

- Beide Zulassungsfaktoren müssen weiterhin durch **eine TLS-Sitzung durchgesetzt werden, die
  das Produkt selbst terminiert**. Eine Managed Cloud, die einen der beiden stillschweigend
  darauf herabstuft, dass „ein Intermediär uns mitgeteilt hat, er sei in Ordnung“, würde die
  zentrale Aussage des Produkts schwächen.
- Die öffentliche HTTP-Oberfläche sollte die Edge-Schutzmechanismen von L7 nutzen können,
  ohne dass das Produkt sie neu implementieren muss.
- Langlebige Collector-Streams müssen das Idle-Verhalten des Ingresses überstehen.
- Keine Regression gegenüber dem Self-Hosted-Deployment: ein Codepfad, nicht zwei.

## Betrachtete Optionen

- **A — ein L4-Load-Balancer für alles.** TCP-Passthrough für beide Ebenen; die Binärdatei
  terminiert jede TLS-Sitzung, einschließlich der öffentlichen API.
- **B — geteilter Ingress.** Ein **Network Load Balancer (L4) mit einem TCP-Listener** im
  Passthrough für die Collector-Ebene sowie ein **Application Load Balancer (L7)** für die
  HTTP-Oberfläche der Control Plane.
- **C — ein L7-Load-Balancer mit verwaltetem gegenseitigem TLS.** Der Application Load
  Balancer authentifiziert Client-Zertifikate selbst (Verify-Modus gegenüber einem Trust
  Store mit Sperrlisten) oder leitet die Kette in einem HTTP-Header an das Target weiter.

## Entscheidungsergebnis

Gewählte Option: **B — geteilter Ingress**.

### Konsequenzen

- **Gut:** Die Collector-Ebene entspricht bytegenau dem Self-Hosted-Pfad. Ein TCP-Listener
  terminiert TLS nicht; daher führt die Binärdatei den Handshake durch und setzt die
  Zertifikatsanforderung selbst durch, genau wie On-Premises. Es gibt weder einen
  Cloud-spezifischen Zweig im Authorizer noch einen Cloud-spezifischen Fall in der Audit-Kette.
- **Gut:** Die öffentliche Oberfläche kann ein verwaltetes Zertifikat, Host-/Pfad-Routing und
  eine Web Application Firewall nutzen, ohne dass das Produkt etwas davon neu implementieren
  muss. Die Firewall ist ein **separat bepreister** Dienst und keine kostenlose Eigenschaft
  des L7-Load-Balancers; sie wird hier als verfügbar, nicht als enthalten aufgeführt.
- **Gut, mit präzise angegebenem Geltungsbereich:** Das Idle-Timeout des TCP-Listeners ist
  **zwischen 60 und 6000 Sekunden konfigurierbar** (`tcp.idle_timeout.seconds`, Standardwert
  **350**); das eines TLS-Listeners ist **fest auf 350 Sekunden eingestellt und kann nicht
  geändert werden**. Dies ist ein **Inaktivitäts**-Timeout — das Ausbleiben von Bytes — und
  **keine Obergrenze für die Stream-Dauer**: Ein Stream, der weiterhin Daten oder
  Keepalive-Frames sendet, wird nicht nach 350 Sekunden getrennt. Passthrough „ermöglicht
  lange Streams“ also nicht; es überlässt uns die Festlegung des Inaktivitätsbudgets. Anders
  herum formuliert, weil dies der entscheidende Teil ist: **Ein stiller Stream stirbt an
  jedem dieser Ingresses**, und der Client muss dies überstehen.
- **Schlecht und der Grund, weshalb der vorherige Punkt als Warnung formuliert ist:** Der
  Collector-Client konfiguriert **kein gRPC-Keepalive** (die Bibliothek deaktiviert es
  standardmäßig) und behält nach einem fehlgeschlagenen Sendevorgang den toten Stream im Cache,
  statt ihn neu aufzubauen. Eine Inaktivitätsdauer oberhalb des konfigurierten Timeouts, ein
  Führungswechsel oder ein Deployment beendet daher einen Collector-Stream, den nichts wieder
  verbindet. Dies wird **nicht durch den Split verursacht** — es bestand bereits zuvor —,
  doch der Split ist das erste Deployment, in dem ein Intermediär inaktive Verbindungen aktiv
  schließt; hier beginnt die Lücke somit Daten zu kosten. Eine Wiederverbindungsschleife mit
  Backoff auf der Collector-Seite ist eine **Voraussetzung**, um diesen Ingress als
  produktionsbereit zu bezeichnen.
- **Schlecht / Abwägungen:** Zwei Load Balancer bedeuten zwei stündliche Gebühren und zwei
  unabhängige Capacity-Unit-Zähler, die zusammen die feste monatliche Untergrenze eines
  kleinen Deployments dominieren. Dies sind reale, wiederkehrende Kosten dafür, beide
  Zulassungsfaktoren im Prozess zu halten.
- **Schlecht und eine Build-Anforderung statt einer Fußnote:** Bei **Target Groups vom Typ IP
  mit TCP- oder TLS-Protokoll ist die Erhaltung der Client-IP standardmäßig deaktiviert** —
  und Tasks in der verwalteten Container-Laufzeit sind IP-Targets. Beim Standardwert erreicht
  jede Collector-Verbindung die Binärdatei mit der privaten Adresse des Load Balancers als
  Quelladresse. Alles Adressabgeleitete — Audit-Datensätze, Rate Limits,
  Adress-Allow-Lists — wäre vom ersten Tag an unbemerkt falsch. Der Ingress ist erst
  vollständig, wenn entweder `preserve_client_ip.enabled` aktiviert ist oder die Binärdatei
  Proxy Protocol v2 vor dem Handshake parst. Wird die Erhaltung aktiviert, sieht sich die
  Security Group des Targets außerdem den Client-Adressen statt der Adresse des Load
  Balancers gegenüber; dies muss das Netzwerkdesign berücksichtigen.
- **Neutral / Follow-ups:** Welcher der beiden Mechanismen die Quelladresse wiederherstellt,
  bleibt der Implementierungsphase überlassen, aber **die Wahl muss getroffen und getestet
  werden; sie darf nicht von einem Standardwert geerbt werden**. Ein Test, der bestätigt,
  dass die aufgezeichnete Quelladresse der des Collectors entspricht, ist das
  Abnahmekriterium.

## Warum die Alternativen verworfen wurden

- **A (ein L4-Load-Balancer)** — für die *öffentliche* Ebene abgelehnt, nicht für die
  Collector-Ebene. Die Option ist günstiger und kommt der Self-Hosted-Topologie am nächsten,
  aber die Control-Plane-API würde verwaltete Zertifikate, WAF und Host-/Pfad-Routing
  verlieren, und das Produkt würde auf L7 neu implementieren, was der Edge bereits
  bereitstellt. Die Collector-Hälfte von Option A ist genau das, was Option B beibehält.
- **C (verwaltetes gegenseitiges TLS auf L7)** — abgelehnt, weil die Option **die
  Vertrauensgrenze verschiebt**. Im Verify-Modus prüft der Edge das Zertifikat, und die
  Anwendung erhält eine Anfrage, für die bereits gebürgt wurde; im Passthrough-Modus trifft
  die Zertifikatskette in einem `X-Amzn-Mtls-Clientcert`-Header beim Target ein. In beiden
  Fällen ist das Transport-Gate nicht länger etwas, das das Produkt durchgesetzt hat, sondern
  wird zu einer Behauptung eines anderen Systems — genau jener Ersetzung, deren
  Überprüfbarkeit dieses Produkt sicherstellen soll. Ihr Fehlermodus ist nur einen Fehler in
  der Netzwerkkonfiguration entfernt: Alles, was das Target direkt erreichen kann, kann den
  Header fälschen. Der verwaltete Trust Store mit Sperrlisten ist ein echter betrieblicher
  Vorteil, über den das Produkt bei Collector-Zertifikaten derzeit überhaupt nicht verfügt:
  Es lädt eine CA und führt eine gewöhnliche X.509-Validierung ohne CRL- oder OCSP-Prüfung
  durch. Sollte die verwaltete Sperrung jemals schwerer wiegen als die direkte Terminierung,
  wird dies in einem **neuen ADR** festgehalten und nicht durch eine Änderung dieses ADRs.
