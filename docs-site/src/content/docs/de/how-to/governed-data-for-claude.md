---
title: "Governte Daten für Claude"
description: "Stellen Sie Claude Code Ihre Drive- oder S3-Inhalte über eine semantische KB und einen MCP-Retrieval-Endpunkt bereit, governt durch Identität, Clearance, ACL und Source-Scope."
sidebar:
  order: 7
---

Auf diesem Weg kann Claude Code Fragen zu **Ihren** Google-Drive- oder
S3-Inhalten stellen, ohne Olivares in ein AI-Gateway zu verwandeln. Die
Kontrollebene zieht die Inhalte in eine governte Knowledge Base, zeichnet die
Herkunft pro Dokument auf und stellt über MCP nur die Retrieval-Tools bereit:

| Standard | Bedeutung |
|---|---|
| Semantische KB | `embed_policy=model_backed`; vor der Ingestion muss `/status` den Wert `retrieval_semantic=true` anzeigen. |
| Sichtbarer Fallback | Ist kein semantischer Embedder konfiguriert, verweigert die KB das Erstellen/die Ingestion, statt Local-Hash-Vektoren fälschlich als semantisch darzustellen. |
| ACL-bewusster Guard | Der anfragende Agent muss einer gebundenen Identität mit ausreichender `attr_clearance` und passenden Gruppen-ACLs zugeordnet werden können. |
| Source-Scope | Binden Sie die KB an den Claude-Code-Agenten; Subjects außerhalb des Scopes werden fail-closed abgewiesen. |
| Ehrlicher Live-Modus | Eine Antwort eines Live-Connectors trägt `source_mode=live`; statische Exporte bleiben `source_mode=export` und werden niemals als live dargestellt. |

## 1. Zugangsdaten der Quelle speichern

Bewahren Sie die Zugangsdaten der Live-Quelle im Runtime-Secret-Store auf. Die
Source-Konfiguration referenziert sie als `store:<name>`, niemals inline.

```sh
olivares secrets put \
  --data-dir /var/lib/olivares \
  --name s3/prod-runbooks-read \
  --value-file /run/secrets/s3-prod-runbooks-read
```

Speichern Sie für Google Drive das OAuth-Bearer-/Refresh-Material, das Ihr
Deployment für den read-only Drive-Zugriff verwendet, unter einem anderen
Secret-Namen.

## 2. Governte RAG-Konfiguration generieren

Für S3:

```sh
olivares quickstart governed-rag \
  --data-dir /var/lib/olivares \
  --tenant-id ten_... \
  --source s3 \
  --source-name prod-runbooks-live \
  --bucket prod-runbooks \
  --prefix claude/ \
  --credential-ref store:s3/prod-runbooks-read \
  --mcp-issuer https://idp.example.com/ \
  --mcp-jwks-url https://idp.example.com/.well-known/jwks.json
```

Verwenden Sie für Google Drive
`--source gdrive --drive-id <shared-drive-id>` und eine Referenz auf die
Drive-Zugangsdaten.

Der Befehl schreibt:

| Datei | Zweck |
|---|---|
| `sources.json` | Registriert die Inhaltsquelle unter `documents[]` mit `mode=live`. |
| `agent-gateway.json` | Aktiviert den MCP-Ressourcenserver mit `retrieval.enabled=true`. |
| `bootstrap-after-login.sh` | Erstellt die semantische KB, nimmt die Live-Quelle auf, bindet den Agenten und fügt die Source-Scope-Bindung hinzu. |

Warnt der Befehl vor `retrieval_semantic=false`, konfigurieren Sie zuerst
`OLIVARES_EMBEDDINGS_*`. Eine modellgestützte KB verweigert die Ingestion mit
nur dem Local-Hash-Fallback absichtlich.

## 3. Mit der generierten Konfiguration starten

```sh
OLIVARES_SOURCES_CONFIG=/var/lib/olivares/quickstart/governed-rag/sources.json \
OLIVARES_AGENT_GATEWAY_CONFIG=/var/lib/olivares/quickstart/governed-rag/agent-gateway.json \
olivares quickstart --data-dir /var/lib/olivares
```

Schließen Sie bei einer frischen Installation die erstmalige Einrichtung in
der Konsole ab. Führen Sie dann das Bootstrap-Skript mit einem Admin-Token aus:

```sh
OLIVARES_TOKEN=<admin-token> \
OLIVARES_TENANT=ten_... \
/var/lib/olivares/quickstart/governed-rag/bootstrap-after-login.sh
```

## 4. Identitätsvoraussetzung

Der Retrieval-Guard liest Identitätsfakten aus dem Roster-/SCIM-Graphen. Die
gebundene Identität muss vorhanden sein, bevor Claude Code eingeschränkte
Inhalte abrufen kann:

| Identitätsfakt | Beispiel |
|---|---|
| Subject des Agent-Tokens / `agent_ref` | `claude-code-governed` |
| Gebundene NHI-Identität | `agent:claude-code-governed` |
| Clearance-Metadaten | `attr_clearance=confidential` oder höher |
| Gruppenmitgliedschaft | `group:engineering`, passend zur Dokument-ACL |

Hat der Agent keine Identität, keine Clearance oder keine passende Gruppe,
werden eingeschränkte Chunks nicht zurückgegeben. Ist der Agent nicht per
Source-Scope an die KB gebunden, schlägt der MCP-Retrieval-Aufruf fail-closed
fehl.

## 5. Claude Code auf MCP verweisen

Konfigurieren Sie Claude Code mit der vom Quickstart ausgegebenen URL der
geschützten Ressource, üblicherweise:

```text
http://127.0.0.1:8446/mcp
```

Das Zugriffstoken für diesen MCP-Ressourcenserver muss Folgendes enthalten:

| Claim/Control | Erforderlicher Wert |
|---|---|
| `iss` | Der mit `--mcp-issuer` konfigurierte Aussteller. |
| `sub` | Die externe ID des Agenten, beispielsweise `claude-code-governed`. |
| Scope | `knowledge:retrieval:read`. |
| Audience/Ressource | Die in `agent-gateway.json` konfigurierte MCP-Ressourcen-URL. |

## 6. Verifizieren

Führen Sie die Referenz-E2E-Demo aus:

```sh
task demo:governed-rag
```

Sie prüft den semantischen Status, die Herkunft aus der Live-Quelle, einen
erlaubten Abruf innerhalb des Scopes, einen nicht erfolgten Abruf bei geringer
Clearance, eine Ablehnung außerhalb des Scopes sowie `source_mode=live` im
MCP-Ergebnis.

Prüfen Sie bei bestehenden Deployments außerdem ein echtes Dokument:

```sh
curl -sk "$OLIVARES_BASE_URL/v1/m/knowledge/kbs/$KB_ID/documents" \
  -H "Authorization: Bearer $OLIVARES_TOKEN" \
  -H "X-Olivares-Tenant: $OLIVARES_TENANT"
```

Jedes live aufgenommene Dokument sollte `source_mode: "live"` anzeigen. Steht
dort `export`, wurde die KB aus einer Exportdatei aufgenommen und muss den
Operatoren entsprechend beschrieben werden.
