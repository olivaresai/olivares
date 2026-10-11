---
title: "Claude Code mit Olivares betreiben (Co-Deployment)"
description: "Co-Deployment der Olivares Control Plane und einer Claude-Code-Laufzeit auf einem Linux-Rechner, secure by default, sodass die Engine Claude-Code-Sessions startet, governt und abbaut — über einen gemeinsamen Workspace, in vier Topologien."
---

Dies ist die **Operate**-Hälfte der Anthropic-first-Geschichte: nicht nur Claude Code
*beobachten* und *governen*, sondern es **dirigieren**. Die Control Plane startet einen
echten `claude`-Prozess, brückt dessen I/O in einen governten Stream, verankert jeden
Lifecycle-Übergang im Audit-Ledger und baut ihn ab — über einen gemeinsamen Workspace, von
der API/dem CLI (und, später, dem Portal), **ohne SSH**. Diese Seite co-deployt beide
Hälften auf einem Linux-Host in vier Topologien, secure by default.

Für den Pfad des *kooperativen Beobachtens* (OTLP-Telemetrie → Access Map) siehe
[Claude Code verbinden](/how-to/connect-claude-code/); für den *Govern*-Pfad (PreToolUse-Hooks
als PEP) siehe das [govern-claude-code-Beispiel](https://github.com/olivaresai/olivares/tree/main/examples/govern-claude-code).
Diese Seite behandelt das **Co-Deployment**: die beiden Laufzeiten gemeinsam zum Laufen
bringen. Claude, Codex oder Grok nach vorhandener Laufzeit unter einem
Anbieterprofil zu starten: [Eine Anbieter-Session betreiben](/how-to/operate-provider-sessions/).

:::note[Wie Governance die Session tatsächlich erreicht]
Eine Session ist governt, weil **die Engine `claude`s stdin/stdout besitzt** — der
`stream-json`-Headless-Transport. Die Engine spawnt `claude` als Kindprozess (der native
procRunner) und brückt jeden NDJSON-Frame. Das funktioniert nur, wenn die Engine und
`claude` einen Ausführungskontext teilen (denselben Host oder denselben Container). Die
empfohlenen Topologien stellen sie genau aus diesem Grund zusammen; die gemischten
Topologien und ihre ehrlichen Einschränkungen stehen weiter unten.
:::

## Zwei Prinzipien, bevor Sie beginnen

1. **Opt-in.** Das Olivares-Image **enthält kein `claude`**. In Docker installiert Olivares
   die Agent-Tools in sein Daten-Volume und meldet sie über das Produkt an, wenn Sie sie
   anfordern; nativ erledigt das der Install-Zusatz unten. Wenn Sie kein governtes Claude
   Code betreiben, wird nichts installiert.
2. **Offizielle Quelle, niemals weiterverteilt.** Anthropics Bedingungen erlauben die
   Weiterverteilung des `claude`-Binaries nicht, daher **installieren wir es aus Anthropics
   offizieller, GPG-signierter Quelle** bei der Installation (nativ die signierten
   apt/dnf/apk-Repositories; im Image Anthropics signiertes Release, gegen denselben
   Schlüssel geprüft), gepinnt und mit deaktiviertem Auto-Updater. Wir liefern kein
   Drittanbieter-Binary aus. Sie können auch Ihr **eigenes** `claude` mitbringen und die
   Engine darauf zeigen lassen.

## Die vier Topologien auf einen Blick

| # | Olivares | Claude Code | Wie die Engine es dirigiert | Status |
|---|----------|-------------|----------------------------|--------|
| 1 | Docker | Docker | **Derselbe Container** (das Standard-Image), procRunner-Kind | **Empfohlen** (gleicher governter Pfad wie 2) |
| 2 | Nativ | Nativ | Derselbe Host (systemd), procRunner-Kind | **Empfohlen**, end-to-end smoke-getestet |
| 3 | Docker | Nativ (Host) | Cross-Namespace — so nicht governbar | Stattdessen co-lokalisieren (siehe unten) |
| 4 | Nativ | Docker (pro Session) | Container pro Session über die Docker-API | Follow-up (dokumentiert) |

Die beiden **co-lokalisierten** Topologien (1, 2) sind der sichere Standard. Topologie 2
(nativ) ist end-to-end durch [`scripts/smoke-agentops.sh`](https://github.com/olivaresai/olivares/blob/main/scripts/smoke-agentops.sh)
getestet; Topologie 1 nutzt **denselben** governten procRunner-Pfad, und die CI führt die
Prüfung der Agent-Laufzeit des Images aus (`scripts/qualify-container-agent-runtime.sh`). Die
Topologien 3 und 4 wollen den Governor und das Governte in *verschiedenen* Containern;
stdio über diese Grenze zu brücken erfordert Docker-API-Zugriff (ein Privileg, das die
Engine bewusst standardmäßig **nicht** nimmt). Ihre ehrlichen Pfade sind in
[Gemischte Topologien](#gemischte-topologien-3-und-4) ausbuchstabiert.

---

## Topologie 1 — beide in Docker (empfohlen)

Der Standard-Engine-Container betreibt auch `claude`: Olivares installiert die Agent-Tools in
sein Daten-Volume und meldet sie über das Produkt an. Non-root,
schreibgeschütztes Root-Dateisystem — die Haltung des Basis-Compose, ohne zweites Image und
ohne Override-Datei.

### Hochfahren

Pinnen Sie die Engine per Digest und verifizieren Sie sie zuerst:

<!-- release -->
```sh
# verify the engine image you run (it is cosign-signed)
cosign verify docker.io/olivaresai/olivares:0.1 \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

export OLIVARES_IMAGE=docker.io/olivaresai/olivares@sha256:<digest>
export OLIVARES_BIND=127.0.0.1
docker compose -f deploy/compose/docker-compose.yml up -d
```
<!-- /release -->

### Claude Code installieren und anmelden

Über **KI-Werkzeuge** in der Konsole oder mit der CLI:

```sh
olivares tool install claude
olivares tool login claude
```

Das Tool liegt im Daten-Volume, und die Anmeldung gehört Ihrer Organisation; Sessions nutzen
nie eine Anmeldung aus dem Home des Server-Kontos.

---

## Topologie 2 — beide nativ (kein Docker)

Engine und `claude` auf dem Host; systemd betreibt die Engine, die `claude` dirigiert. Der
Workspace liegt unter `/var/lib/olivares/workspaces`.

### Herunterladen, prüfen, dann ausführen

Leiten Sie das Installationsskript nie in eine Shell. Prüfen Sie die signierte
Prüfsummenliste des Releases, checken Sie genau den Commit aus, den sie nennt, und führen
Sie das Skript aus diesem Checkout aus. Es liest `install.sh` und seine Paketdateien dann
aus dem Checkout, nie aus einem Branch. Der Block läuft in einer Subshell mit `set -eu`: Er
bricht beim ersten fehlgeschlagenen Schritt ab und startet das Skript nie nach einer
fehlgeschlagenen Prüfung.

<!-- release -->
```sh
(
set -eu
cd "$(mktemp -d)"
ver=0.1
base=https://github.com/olivaresai/olivares/releases/download/$ver
curl -fsSLO $base/checksums.txt
curl -fsSLO $base/checksums.txt.sig
curl -fsSLO $base/checksums.txt.pem
curl -fsSLO $base/release-commit.txt
cosign verify-blob \
  --certificate checksums.txt.pem --signature checksums.txt.sig \
  --certificate-identity "https://github.com/olivaresai/olivares/.github/workflows/release.yml@refs/tags/$ver" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
grep ' release-commit.txt$' checksums.txt | sha256sum --check
git clone https://github.com/olivaresai/olivares.git
cd olivares && git checkout --detach "$(cat ../release-commit.txt)"
OLIVARES_TOPOLOGY=native OLIVARES_VERSION=$ver sh scripts/install-agentops.sh
)
```
<!-- /release -->

Es erkennt die native Topologie automatisch, installiert das **verifizierte** Engine-Binary
(das cosign-gegatete `install.sh`), installiert `claude` aus dem signierten
apt/dnf/apk-Repository (mit Key-Fingerprint-Verifikation — oder
`OLIVARES_CLAUDE_INSTALL=byo` zum Überspringen), erstellt den No-Login-`olivares`-Service-User
und das Workspace-Verzeichnis und legt das gehärtete systemd-Override + Env-Beispiel ab. Es
startet **keine** Governance-Plane automatisch — eine zu betreiben ist Ihre ausdrückliche
Entscheidung.

### Was der Installer verdrahtet (und warum)

- `packaging/systemd/olivares.service.d/agentops.conf` — ein Drop-in, das dem dirigierten
  `claude` ein beschreibbares `HOME` für `~/.claude` gibt (unter `/var/lib/olivares`
  gehalten), sicherstellt, dass das Workspace-Verzeichnis existiert, und die
  Dateisystem-Richtlinie der Basiseinheit wiederholt: `ProtectSystem=full`,
  `ProtectHome=false`, `PrivateTmp=false` und `MemoryDenyWriteExecute=false` (die
  `claude`-Laufzeit JIT-kompiliert und braucht W→X-Speicher). Home-Verzeichnisse und
  `/tmp` bleiben für die Engine sichtbar, damit die gewählten Ordner erreichbar bleiben;
  das `claude` jeder Session und seine stdio-MCP-Server laufen stattdessen unter der
  ordnerbezogenen Landlock-Eingrenzung des Produkts. Jede andere Härtungsdirektive der
  Basiseinheit bleibt in Kraft.
- `/etc/olivares/agentops.env` — die Session-Runtime-Konfiguration (Token-Datei, TTL,
  optionale Gateway-Basis-URL, optionaler BYO-`claude`-Pfad).

Dann, bewusst:

```sh
sudo nano /etc/olivares/agentops.env     # wire the short-lived inference token (refresher)
sudo systemctl enable --now olivares     # listens on every interface by default
```

:::note[Warum es keinen separaten `claude`-Service gibt]
Ein langlaufender `claude`-Daemon würde sein stdin/stdout außer Reichweite der Engine
bringen — und der governte Transport *ist* stdio. Daher startet und besitzt die Engine den
`claude`-Prozess selbst; die „Runtime-Unit" ist der eigene Service der Engine,
konfiguriert für die Operate-Rolle durch das Drop-in.
:::

---

## Die erste governte Session starten

Gleiche Schritte in beiden co-lokalisierten Topologien. Authentifizieren Sie das CLI,
registrieren Sie den gemeinsamen Workspace, starten Sie:

```sh
export OLIVARES_SERVER_URL=https://127.0.0.1:8443
export OLIVARES_TOKEN=<your-api-token>
export OLIVARES_TENANT=<your-tenant-id>

# 1) register the shared workspace (the session's working dir; jailed file API on top)
olivares agent workspace add /var/lib/olivares/workspaces/project-x --name project-x --mode rw

# 2) launch a governed session over the stream-json transport
olivares agent session create --transport stream-json \
  --permission-mode acceptEdits --model opus \
  --workspace <workspace-ref> --isolation native

# 3) attach to its live, bridged I/O (lossless replay from a cursor); send input; stop
olivares agent session attach <run-ref>
olivares agent session input  <run-ref> --line '{"type":"user","message":{"role":"user","content":"…"}}'
olivares agent session stop   <run-ref>
```

Jeder Übergang (`created → launched → … → stopped`) wird **im signierten Audit-Ledger
verankert** (`olivares agent session events <run-ref>`); die Workspace-Datei-API
(`olivares agent workspace files|get|put|…`) ist eingesperrt und auditiert. Der
Reproduzierbarkeitsvertrag für all dies ist [`scripts/smoke-agentops.sh`](https://github.com/olivaresai/olivares/blob/main/scripts/smoke-agentops.sh),
das das native Co-Deployment gegen ein hermetisches Fake-`claude` hochfährt und festsetzt,
dass die Session end-to-end governbar ist.

:::note[Nur `--isolation native` ist in diesem Release funktional]
`--isolation container` und `--isolation sandbox` sind **Vorwärtskompatibilitäts-Nahtwerte,
noch nicht verdrahtet** (der Container-Runner pro Session ist das dokumentierte Follow-up in
[Topologie 4](#topologie-4--olivares-nativ-claude-in-einem-container-pro-session)). Der native
Runner **verweigert** einen Container-/Sandbox-Start (ein klarer Fehler), statt stillschweigend
`claude` ohne die von Ihnen angeforderte Isolation auszuführen. Verwenden Sie `native` — unter
dem Standard-Image / dem systemd-Co-Deployment ist das die eigene gehärtete
Container-/Host-Grenze der Engine.
:::

:::caution[`bypassPermissions` gehört hinter Governance]
`claude` headless mit einem permissiven `--permission-mode` (`bypassPermissions`,
`dontAsk`) auszuführen, ist genau dann, wenn Sie die Governance-Plane wollen. Die
allowlist-basierte Umgebung der Engine leakt nie ein `OLIVARES_*`/`ANTHROPIC_*`-Secret an
den Agenten, und der PreToolUse-PEP / das Budget / der Kill-Switch entscheiden, was
die Session tatsächlich tun darf.
:::

---

## Gemischte Topologien (3 und 4)

Diese teilen den Governor und das Governte über eine Container-Grenze auf. Seien Sie
klarsichtig darüber, was das kostet.

### Topologie 3 — Olivares in Docker, Claude auf dem Host

Es gibt **keinen sauberen governten Pfad**: Eine containerisierte Engine kann das stdio
eines Prozesses in den Namespaces des Hosts nicht besitzen, und der governte Transport ist
stdio. Ein Host-`claude` zu erreichen würde erfordern, den Host-PID-Namespace und Mounts in
den Engine-Container zu teilen — eine große, bewusste De-Isolation, die den Sinn des
Einschließens der Engine zunichtemacht. **Stattdessen co-lokalisieren**: Betreiben Sie
beide im Standard-Image (das *ist* Topologie 1) oder beide nativ (Topologie 2). Dies ist
eine reale Grenze, benannt statt überdeckt.

### Topologie 4 — Olivares nativ, Claude in einem Container pro Session

Dies ist die natürliche Heimat für **Fresh-Container-Isolation pro Session**: Jede Session
bekommt einen brandneuen gehärteten `claude`-Container (Workspace bind-gemountet,
schreibgeschütztes Root, non-root, cap-drop), erstellt und abgebaut von der Engine über die
Docker-API, mit stdio gebrückt via Docker-Attach/-Hijack. Die Datenmodell-Naht
**modelliert** es bereits (`--isolation container` ist ein gültiger Wert, und das
Executor-Mount-Primitiv, das es konsumieren wird, ist bereits ausgeliefert) — aber der Runner dahinter ist
noch nicht verdrahtet, daher verweigert der native Runner diesen Wert heute (siehe den
Hinweis oben).

**Es ist ein dokumentiertes Follow-up, nicht in diesem Release ausgeliefert.**
Geschwister-Container zu steuern bedeutet, der Engine Docker-API-Zugriff zu geben
(idealerweise über einen Least-Privilege-Socket-Proxy) — eine Vertrauensoberfläche, die
dieses Release bewusst zugunsten des socket-freien Standard-Images vermeidet. Diese
Topologie zu wählen heißt, stärkere Governor/Governte-Isolation zu wählen — *zum Preis* des
Docker-API-Grants; sie wird hinter der bestehenden `isolation=container`-Naht eintreffen.
Bis dahin ist der sichere Standard die Co-Lokation.

---

## Sicherheitshaltung (alle Topologien)

- **Veröffentlichte Ports.** Docker Compose veröffentlicht HTTPS und gRPC standardmäßig
  auf allen Host-Schnittstellen (`0.0.0.0`). Mit `OLIVARES_BIND=127.0.0.1`, wie im Befehl
  oben, beschränken Sie sie auf diesen Host. Die Engine lauscht im Container auf
  `0.0.0.0`; die Host-Port-Zuordnung steuert den Zugriff. Auch native/systemd-Listener
  binden standardmäßig alle Schnittstellen; konfigurieren Sie ihre Listen-Adressen.
- **Non-root, Least Privilege.** uid/gid 65532, schreibgeschütztes Root-Dateisystem,
  `cap_drop: ALL`, `no-new-privileges` (Docker) / die Kernel- und Namespace-Direktiven
  `Protect*`/`Restrict*`, wobei Home-Verzeichnisse und `/tmp` sichtbar bleiben, W^X erlaubt
  ist und jede Session per Landlock auf ihren Ordner eingegrenzt wird (systemd).
- **Minimal-Data, allowlist-basierte Env.** Das Kind-`claude` erbt nur eine explizite
  Allowlist (PATH, HOME, Locale…) plus das In-Memory-Inferenz-Token — **keine**
  `OLIVARES_*`-Signing-Keys, **kein** ambientes `ANTHROPIC_*`/`CLAUDE_CODE_*`, das die
  geprägte Credential überschatten könnte.
- **Verifizierte Supply Chain.** Die Engine ist cosign-signiert (verifizieren Sie sie /
  pinnen Sie per Digest); `claude` installiert aus Anthropics signierten Repos mit
  gepinntem Key-Fingerprint. Der Installer **verweigert es, eine unverifizierte Engine zu
  betreiben**, ohne Umgehung der Verifikation.
- **Verankertes Audit.** Jeder Lifecycle-Übergang und jede Workspace-Mutation wird im
  hash-verketteten, signierten Ledger per `PayloadHash` versiegelt — die Bytes von Dateien
  und der Inhalt von Frames werden nie persistiert.

## Siehe auch

- [Claude Code verbinden](/how-to/connect-claude-code/) — der kooperative Beobachtungspfad.
- [Sicherheit & Härtung](/how-to/security-hardening/) — die Baseline-Haltung der Engine.
- [Ein Release verifizieren](/how-to/verify-a-release/) — cosign / SBOM / SLSA-Verifikation.
- [INSTALL.md](https://github.com/olivaresai/olivares/blob/main/INSTALL.md#operate-claude-code-co-deployment) — die Install-Matrix, einschließlich dieses Co-Deployments.
