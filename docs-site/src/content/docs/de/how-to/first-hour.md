---
title: "Ihre erste Stunde mit Olivares AI (v26.9.1, wie ausgeliefert)"
description: >-
  Was Sie mit einer sauberen Installation des öffentlichen Binärprogramms
  v26.9.1 in der ersten Stunde tatsächlich tun können: Setup-Token,
  AAL3-Schranke, Passkey-Registrierung, Sitzungsstart über Anbieterprofile,
  Deployment, Wissen und die PEP-Hooks für Codex und Grok.
---

Diese Seite beschreibt **v26.9.1 im ausgelieferten Zustand**. Sie ist weder
ein geplanter Einrichtungsassistent für den ersten Start noch ein Screenshot
eines Mockups. Jeder der folgenden Schritte ist etwas, das das öffentliche
Binärprogramm heute ausführt, einschließlich der Datei oder Umgebungsvariable,
die dies ermöglicht. Wo das Produkt etwas verweigert, sagt die Seite es.

Die nummerierten Fakten wurden am 2026-09-04 mit einer sauberen Installation
des öffentlichen Binärprogramms gemessen. Diese Seite verweist auf diese
Messungen und auf die Stellen im Code, an denen sie ankommen.

Wie Sie das Binärprogramm auf den Host bringen, erklären
[Olivares AI selbst hosten](/how-to/self-hosting/) und
[Ein Release verifizieren](/how-to/verify-a-release/). Diese Seite fordert Sie
nicht dazu auf, `https://olivares.ai/install` in eine Shell zu pipen. Sobald
das Binärprogramm auf dem Host liegt, lautet der empfohlene erste Befehl
`olivares quickstart`.

:::note[Was dies nicht ist]
`--seed-demo` ist keine Produkttour. Gemessen am 2026-09-04 mit einer sauberen
Installation des öffentlichen Binärprogramms: **36 von 54** Konsolenrouten
sind auch nach einem Start mit Seed-Daten noch leer. Diese Zahl ist die Zählung
des Messtags. Die generierte
[Konsolenreferenz](/reference/console/) in diesem Baum listet **75 Routen**.
Diese Seite zählt leere Tabs nach `--seed-demo` auf v26.9.1 nicht neu.
Der Demo-Bestand füllt den Durchlauf durch den Zugriffsgraphen in
[Von null zu einem Lese-/Schreibzugriffsgraphen](/tutorials/zero-to-graph/);
den Rest der Konsole füllt er nicht. Verwenden Sie ihn nicht, um „das Produkt
zu erkunden“.
:::

## 1. Empfohlener Start: `olivares quickstart`

Ein neues Datenverzeichnis enthält **keine Standardanmeldedaten**. Der
empfohlene erste Befehl ist `olivares quickstart`
(`cmd/olivares/cmd_quickstart.go`). Er entspricht `serve` mit sicheren
Standardeinstellungen: TLS ist aktiv, es gibt keine Standardanmeldedaten und ein
einmalig verwendbares Setup-Token. Die voreingestellte Listen-Adresse ist `:8443`
— alle Schnittstellen, denn dies ist ein Server (`cmd/olivares/binddefaults.go`). Gemessen am 2026-09-04 mit einer sauberen
Installation des öffentlichen Binärprogramms und echtem TLS (einschließlich
eines Laufs auf **:8460**); der Text des Panels ist identisch.

Das Willkommenspanel (`announceQuickstart`, `:154-163`) ist nummeriert. Die
Engine gibt für diesen Bind `https://localhost:8443` aus und listet unter dem Token
jede weitere Adresse auf, unter der dieser Host antwortet. **Verwenden Sie keine IP für die
Passkey-Zeremonie.** Der Browser lehnt eine IP als WebAuthn-RP-ID ab
(`SecurityError`). Das Produkt leitet die RP-ID aus dem Hostnamen der Anfrage
ab (`core/api/handlers_webauthn.go:33-50`). Öffnen Sie die Konsole vor der
Passkey-Registrierung unter `https://localhost:PORT` (oder unter einem echten
Hostnamen), nicht unter `127.0.0.1`. `PORT` ist `8443`, sofern Sie nicht
`--listen` übergeben haben.

```text
=== WELCOME TO OLIVARES AI ===
  1. Open:   https://localhost:8443
     (HTTPS with a self-signed certificate on first boot — your browser will
      warn once; that is expected for a local install.)
  2. Complete setup with this one-time token (shown once, single-use):

         olst_…
```

Das Banner gibt für den Standard-Bind `localhost` aus und listet unter dem Token die
weiteren Adressen dieses Hosts; eine Passkey-Zeremonie braucht einen Namen, keine
Adresse. Ersetzen Sie
den Host in der Adressleiste durch `localhost`.

Das Token-Präfix ist `olst_` (`cmd/olivares/e2e_binary_test.go` gleicht mit
`olst_[A-Z0-9]+` ab). Die Konsolenseite ist `/setup`. Die API, an die der
Assistent seine Anfrage sendet, lautet:

```http
POST /v1/setup
Content-Type: application/json

{"token":"olst_…","email":"you@example.com","password":"…"}
```

`olivares serve` gibt ein ähnliches Banner aus (`announceSetup` in
`cmd/olivares/cmd_serve.go`). Verwenden Sie für die erste Stunde `quickstart`:
URL, Warnung zum selbstsignierten Zertifikat und einmaliges Token stehen in
einem einzigen Panel. Melden Sie sich anschließend mit `POST /v1/auth/login`
an. Sie verfügen nun über eine AAL1-Passwortsitzung.

`README.md` und dieses Willkommenspanel nennen Token und URL. Die
Passkey-Registrierung nennen sie **nicht**. Sie folgt als Nächstes in der
Konsole, nachdem Sie eine Schaltfläche betätigt haben.

## 2. Die AAL3-Schranke — die Konsole leitet Sie nach dem Klick weiter, nicht vorher

Nach dem Setup wird **das Erstellen von Quellen, Konnektoren, Workspaces und
Secrets verweigert, bis die Sitzung AAL3 erreicht hat**. Die Schranke ist
`requireAAL3` in `core/api/middleware.go:310`. Ein Principal unterhalb von AAL3
erhält `403
step_up_required`. **21 Aufrufstellen** führen durch diese Schranke.
Zu den Schreibpfaden in diesem Quellbaum gehören:

| Oberfläche | Handler | Datei |
|---|---|---|
| Quellenliste einstellen/löschen/neu laden | `handlePutSource` / `handleDeleteSource` / `handleReloadRuntime` | `core/api/handlers_sources.go` |
| Konnektoren schreiben und testen | `handlers_connectors.go` | `core/api/handlers_connectors.go` |
| Secret einstellen/löschen | `handlePutSecret` / `handleDeleteSecret` | `core/api/handlers_secrets.go` |
| Workspace erstellen/aktualisieren | `handleCreateWorkspace` / `handleUpdateWorkspace` | `core/api/handlers_scoping.go:152` / `:199` |
| Mitglied aufnehmen | `handleOnboardMember` | `core/api/handlers_onboarding.go:59` |

**Mit PIV/CAC erreichen Sie dieses Niveau in einer Standardinstallation nicht.**
Ohne Konfiguration antworten die PIV-Routen mit **501**
`piv_not_configured` (`core/api/handlers_piv.go`, `core/api/errors.go`).

### Was die Konsole tatsächlich tut

Die Konsole leitet Sie **tatsächlich** zur Registrierung weiter. Dies geschieht
**reaktiv**.

1. Sie lösen eine privilegierte Aktion aus. Das Step-up-Panel zeigt
   **Mit Sicherheitsschlüssel authentifizieren**
   (`web/src/features/identity/i18n/en.json` `assurance.authenticate`;
   Schaltfläche in `web/src/features/identity/assurance.tsx:230-239`).
2. Dieser Klick ruft `POST /v1/auth/webauthn/authenticate/options` auf
   (Step-up, keine Registrierung). Ohne Passkey antwortet die Engine mit
   **400** `no_webauthn_credential` (`isNoWebAuthnCredential` in
   `web/src/features/identity/api.ts:260-265`).
3. Das Panel weist Sie dann an, sich zuerst im Tab **Privilegierte Anmeldung**
   zu registrieren (`assurance.tsx:160-168` → `assurance.unenrolled`). Dieser
   Satz ist **kein Link**.

Sie navigieren manuell dorthin:

1. Öffnen Sie die Konsole unter **`https://localhost:PORT`**, nicht unter
   `127.0.0.1` (siehe §1).
2. Öffnen Sie `/identity` (`web/src/features/registry.tsx` —
   `path: '/identity'`).
3. Tab **Privilegierte Anmeldung** (`tabs.login`).
4. **Passkey registrieren** (`passkeys.register`). Ein
   **Plattform-Authentifikator genügt** (die Aufforderung des Browsers oder
   Betriebssystems; kein physischer Schlüssel erforderlich). Der Server
   verlangt eine **Benutzerverifizierung** (`core/auth/webauthn.go:74-85`,
   `UserVerification: VerificationRequired`). Gemessen am 2026-09-04 mit einer
   sauberen Installation des öffentlichen Binärprogramms und einer vollständigen
   WebAuthn-Zeremonie: Registrierung **200**, Authentifizierung `{"aal":3}` und
   anschließend `PUT /v1/console/connectors` **200**.

`POST /v1/auth/webauthn/register/options` wird als **Sitzungs-Principal**
authentifiziert und ruft `requireAAL3` **nicht** auf. Bei Erfolg schreibt der
Endpunkt **200** mit `{publicKey: …}`
(`core/api/handlers_webauthn.go:76-88`). Schließen Sie die Zeremonie mit
`POST /v1/auth/webauthn/register` ab. Versuchen Sie den Step-up danach erneut.

`README.md` und das Willkommenspanel von `olivares quickstart` nennen den Tab
„Privilegierte Anmeldung“ **nicht**. Die Konsole nennt ihn erst **nach** dieser
400-Antwort.

Das Identity-Panel nennt AAL3 (NIST SP 800-63B-4) und PIV/CAC (FIPS 201-3) als
**Zielstandards** und erklärt, dass es **keine Zertifizierung beansprucht**
(`targetStandardsNote`). Auch diese Seite beansprucht keine Zertifizierung.

### Konnektor hinzufügen: die Schranke, keine Felder

**Konnektor hinzufügen** (`web/src/features/console/i18n/en.json`
`connectors.add`) öffnet einen Dialog, in dem `ConnectorForm` von
`<RequireAssurance minAal={AAL.HARDWARE}>` umschlossen ist
(`web/src/features/console/connectors-tab.tsx:348-357`). Unterhalb von AAL3
wird das Formular nicht eingebunden. Gemessen am 2026-09-04 mit einer sauberen
Installation des öffentlichen Binärprogramms: Dieser Dialog enthält **keine
Eingabefelder** (`inputs: []`), sondern nur das Step-up-Panel. Der Katalog der
Typen ist mit AAL1 sichtbar (`ConnectorCatalog` steht außerhalb der Schranke,
dieselbe Datei `:341-346`); einen Typ **hinzufügen** können Sie damit nicht.

### `/workspace` und Protokollbindungen: kein Workspace-Umschalter

`/workspace` (`registry.tsx` `path: '/workspace'`) und
`/communications/protocol-bindings` benötigen einen Workspace. Bei einer
sauberen Installation haben Sie keinen und können bis AAL3 auch keinen
erstellen (`handleCreateWorkspace`). `WorkspaceSwitcher` wird bei höchstens
einem Workspace **nicht gerendert**
(`web/src/components/layout/workspace-switcher.tsx:43-44`:
`if (workspaces.length <= 1) return null`). In der oberen Leiste bleibt
**Organisation wechseln** (`web/src/lib/i18n/locales/en/auth.json`
`tenant.switch`). Es gibt keinen Workspace-Selektor, den Sie anklicken könnten.

## 3. Eine Claude-Code-Sitzung über die Konsole starten

Die Konsole kann einen `claude`-Prozess nur starten, wenn der **Host** über
eine Quelle für Inferenz-Anmeldedaten verfügt. Ohne eine solche Quelle werden
stream-json-Starts nach dem Deny-closed-Prinzip abgelehnt. Gemessen am
2026-09-04 mit einer sauberen Installation des öffentlichen Binärprogramms:
**HTTP 503**.

Setzen Sie **eine** der folgenden Variablen:

- `OLIVARES_SESSION_RUNTIME_WIF` — prozessinterne WIF-Ausstellung (`cmd/olivares/sessionruntime.go`, `cmd/olivares/wifbroker.go`)
- `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` — Pfad zu einer rotierten, kurzlebigen Token-Datei

Der Composition Root protokolliert, welche Quelle verdrahtet wurde, oder:

```text
session runtime: no inference credential source configured; stream-json launches are deny-closed
```

(`cmd/olivares/sessionruntime.go:74-76`). Optionale verwandte Variablen:
`OLIVARES_SESSION_RUNTIME_WIF_RULE`, `OLIVARES_SESSION_RUNTIME_TOKEN_TTL`,
`OLIVARES_SESSION_RUNTIME_BASE_URL`, `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN`
(aufgeführt in `cmd/olivares/config_registry.go` und unter
[Konfiguration](/reference/configuration/)).

Die **Operate**-Varianten der gemeinsamen Bereitstellung (auf demselben Host
wie `claude`) finden Sie unter
[Claude Code mit Olivares ausführen](/how-to/run-claude-code-with-olivares/).
Der OTLP-Beobachtungspfad ist unter
[Claude Code verbinden](/how-to/connect-claude-code/) beschrieben.

### Provider-Schlüssel starten keine Sitzung

**Modelle → Provider-Schlüssel** ist ein Governance-Register für **Referenzen**.
Das Formular **akzeptiert niemals ein Secret**:

> Dieses Formular akzeptiert niemals ein Geheimnis. Olivares speichert nur
> eine Referenz und einen maskierten Hinweis.

(`web/src/features/models/i18n/en.json` `keys.dialog.noSecretNote`). Das
Ausfüllen dieses Tabs erfüllt weder `OLIVARES_SESSION_RUNTIME_WIF` noch
`OLIVARES_SESSION_RUNTIME_TOKEN_FILE`. Es aktiviert keine Sitzungsstarts.

## 4. Der Deployment-Plan bleibt 503, bis Sie einen Executor bereitstellen

Plan/Apply per `POST` im Deployment-Modul gibt **503** zurück, bis der Host
`OLIVARES_DEPLOY_EXECUTOR_CONFIG` auf eine JSON-Datei setzt. Fehlt die Variable,
behält das Modul den unverdrahteten Deny-closed-Executor bei
(`cmd/olivares/deployexec_load.go:16-20`). Eine nicht lesbare Datei lässt den
Start **fehlschlagen**.

Das JSON-Objekt ist `deployExecutorConfig` in
`cmd/olivares/deployexec_load.go:26-42`. Optionale Backend-Blöcke:
`tofu`, `terraform`, `gitops`, `k8s`, `docker`, `nomad`, `crossplane` sowie
`credential`, `blast_radius`, `identity_binding`, `drift`.

Die Umgebungsvariable ist unter
[Konfiguration](/reference/configuration/) dokumentiert
(`docs-site/src/content/docs/reference/configuration.md:152`). Sie ist nicht
als Konsolenklick für die erste Stunde dokumentiert. In der UI gibt es keine
Auswahl, die diese Datei ersetzt.

Der Modulkatalog kennzeichnet die Deployment-Aktuierung als
**on-demand (503)** ([Module](/reference/modules/overview/)). Diese Zeile
beschreibt denselben Sachverhalt.

## 5. Eine öffentliche Wissensbasis abfragen oder eine Agentenidentität verwenden

Der Standard-Embedder ist der ausgangsnetzfreie **LocalHashEmbedder**. Beim
Start wird gewarnt, dass die Suche **lexikal, nicht semantisch** ist, und
`embed_model=local-hash` ausgegeben (`cmd/olivares/claude_inference.go`,
`cmd/olivares/knowledgestatus.go`). Die lexikalische Suche gibt dennoch Chunks
zurück, wenn die Schutzprüfung sie zulässt.

Ohne authentifizierte Agentenidentität gewährt die Retrieval-Schutzprüfung nur
Zugriff auf **öffentliche, uneingeschränkte** Inhalte
(`modules/knowledge/query.go:120-127`). Eine menschliche REST-Anfrage an
`/query` für eine **interne** Wissensbasis wird abgelehnt. Gemessen am
2026-09-04 mit einer sauberen Installation des öffentlichen Binärprogramms:
Eine **öffentliche** Wissensbasis gab **1 Ergebnis** zurück (Score 0.738).
Fragen Sie eine öffentliche Wissensbasis ab oder verwenden Sie eine
Agentenidentität.

Eine abgelehnte Abfrage meldet heute `excluded_chunks: 0`, selbst wenn alles
ausgeschlossen wurde. Dieser Zähler wird nur für die vom Betreiber definierte
Untergrenze `excluded_sources` erhöht (`query.go:256-284`); eine Ablehnung
aufgrund von Freigabestufe oder ACL erhöht ihn niemals.

## 6. Codex- und Grok-Sessions: Anbieterprofile, dann die übrigen CLI-Hooks

v26.9.1 betreibt die offizielle Codex-CLI und die offizielle Grok-CLI als
Session-Treiber, zusätzlich zu Claude Code (`CHANGELOG.md` `[26.9.0]` Added).
Die Konsole verwaltet diese Starts unter **Provider profiles**
(`/provider-profiles`, `sessions:profile:read`) und **Source bindings**
(`/provider-bindings`, `sessions:profile-binding:read`). Beide Routen stehen
in der generierten [Konsolenreferenz](/reference/console/).

Ein Profil ist die dauerhafte Identität einer konfigurierten Anbieterinstanz
auf einer Ausführungsumgebung. Es ist kein authentifiziertes Anbieterkonto
(`web/src/features/agentops/types.ts`). Die Registrierung prüft Homes, die auf
diesem Knoten bereits existieren. Der Server installiert, erstellt oder
meldet sich nicht an.

### Treiber auf dem Host registrieren, bevor ein Start möglich ist

Bereitschaft ist pro Treiber. Es gibt keinen gemeinsamen Schalter
(`cmd/olivares/sessionruntime.go`). Die passende Umgebungsvariable
registriert diesen Treiber auf diesem Knoten:

| Treiber | Umgebungsvariable | Wenn ungesetzt |
|---|---|---|
| Claude Code | `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN` (Standard `claude`) | der Claude-Pfad nutzt den Standardnamen |
| Codex | `OLIVARES_SESSION_RUNTIME_CODEX_BIN` | Codex-Profile bleiben beobachtbar und sind nicht startbar |
| Grok | `OLIVARES_SESSION_RUNTIME_GROK_BIN` | Grok-Profile bleiben beobachtbar und sind nicht startbar |

Werte sind gepinnte offizielle Binärdateien. Die Engine löst `codex` oder
`grok` nicht über `PATH` auf. Quelle: [Konfiguration](/reference/configuration/).

Claude-Starts brauchen weiterhin eine Inferenz-Credential-Quelle wie in §3.
Codex und Grok authentifizieren über die AUTORISIERTE `auth_source` des Profils
(`provider_account_home` oder `managed_injection`, kein Fallback).
`CHANGELOG.md` `[26.9.0]` behauptet **keine** Kompatibilität mit einem
authentifizierten offiziellen Grok-Konto.

Der Startdialog verlangt ein Anbieterprofil. Der produktive Create-Endpunkt
verlangt `provider_profile_ref`. Weglassen behält den älteren Request-Body,
den diese API ablehnt ([CLI](/reference/cli/)
`olivares agent session create --provider-profile`).

Profil registrieren, Quelle binden, starten, unterbrechen und stoppen:
[Eine Anbieter-Session betreiben](/how-to/operate-provider-sessions/).

### Was CLI-only bleibt (Hooks und verwaltete Konfiguration)

Diese Befehle sind kein Konsolen-Session-Start. Sie existieren weiterhin:

| Befehl | Funktion | Quelle |
|---|---|---|
| `olivares codex` | Rendert Codex-`requirements.toml` / `managed_config.toml` aus einer Policy-JSON-Datei. **Schreibt Dateien; kommuniziert nicht mit der Control Plane.** | `cmd/olivares/cmd_codexmanagedconfig.go` |
| `olivares codex-hook` | Deny-closed-PEP-Hook, den Codex aufruft (stdin → Control Plane → stdout in der Form des Ereignisses). | `cmd/olivares/cmd_codexhook.go` |
| `olivares grok-hook` | Deny-closed-PEP-Hook, den Grok Build aufruft. Eine Ablehnung **blockiert** nur bei `pre_tool_use`. | `cmd/olivares/cmd_grokhook.go` |

Bei der Installation des Codex-Hooks (anhand der im Kommentar dieser Datei
dokumentierten Form von Codex' `hooks.json` verifiziert) muss `command` ein
**String** sein: `olivares codex-hook`. Umgebung:
`OLIVARES_CODEX_HOOK_URL`, `OLIVARES_CODEX_HOOK_TOKEN`,
`OLIVARES_CODEX_HOOK_TENANT` (sowie optional Agent/Organisation/Konto).

Umgebung des Grok-Hooks: `OLIVARES_GROK_HOOK_URL`,
`OLIVARES_GROK_HOOK_TOKEN`, `OLIVARES_GROK_HOOK_TENANT`. Grok kann einen Hook
über `~/.grok/disabled-hooks` anhand seines Namens deaktivieren; dies ist keine
Steuerungsmöglichkeit der Konsole.

Suchen Sie nicht nach einer Schaltfläche „Codex verbinden“ oder
„Grok verbinden“. Die Konnektor-Aufnahme ist **Control console → Connectors**
(Typ `codex` oder `grok`). Das ist die Beobachten-/Steuern-Ebene in
[Codex integrieren](/how-to/integrations/codex/) und
[Grok Build integrieren](/how-to/integrations/grok/). Sie registriert keinen
Session-Treiber.

## 7. `--seed-demo` füllt die Konsole nicht

`olivares serve --seed-demo` lädt einen Demo-Bestand, damit das Tutorial zum
Zugriffsgraphen ausgeführt werden kann. Gemessen am 2026-09-04 mit einer
sauberen Installation des öffentlichen Binärprogramms: **36 von 54**
Konsolenansichten sind auch in diesem Bestand noch leer. Diese Zahl ist die
Zählung des Messtags; die generierte Konsolenreferenz listet heute **75 Routen**.
Verwenden Sie `--seed-demo` nur für den Pfad unter
[Von null zu einem Lese-/Schreibzugriffsgraphen](/tutorials/zero-to-graph/).
Behandeln Sie leere Tabs nach `--seed-demo` weder als defekte Installation noch
das Flag als Produkttour.

## Verwandte Themen

- [Ehrlichkeit und Grenzen](/start/honesty-and-limits/) — was die Dokumentation behaupten darf.
- [Olivares AI selbst hosten](/how-to/self-hosting/) — wie das Binärprogramm ausgeführt wird.
- [Eine Anbieter-Session betreiben](/how-to/operate-provider-sessions/) — Profile, Treiber-Pins, Start, Interrupt.
- [Konfiguration](/reference/configuration/) — die oben genannten Umgebungsvariablen.
- [Module](/reference/modules/overview/) — on-demand (503) gegenüber aktiver Aktuierung.
