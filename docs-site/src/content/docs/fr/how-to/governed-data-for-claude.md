---
title: "Données gouvernées pour Claude"
description: "Exposez vos contenus Drive ou S3 à Claude Code via une base de connaissances sémantique et un endpoint de récupération MCP, gouvernés par l'identité, le niveau d'habilitation, les ACL et le périmètre de la source."
sidebar:
  order: 7
---

Cette procédure permet à Claude Code d'interroger **vos** contenus Google Drive ou S3
sans transformer Olivares en passerelle d'IA. Le plan de contrôle importe les contenus
dans une base de connaissances gouvernée, enregistre la provenance de chaque document
et n'expose via MCP que les outils de récupération :

| Comportement par défaut | Signification |
|---|---|
| Base de connaissances sémantique | `embed_policy=model_backed` ; avant l'ingestion, `/status` doit afficher `retrieval_semantic=true`. |
| Repli explicite | Si aucun modèle d'embedding sémantique n'est configuré, la création ou l'ingestion de la base de connaissances est refusée au lieu de prétendre que les vecteurs de hachage locaux sont sémantiques. |
| Garde tenant compte des ACL | L'agent demandeur doit être associé à une identité liée possédant un `attr_clearance` suffisant et des ACL de groupe correspondantes. |
| Périmètre de la source | Liez la base de connaissances à l'agent Claude Code ; pour les sujets hors périmètre, la vérification échoue en mode fermé. |
| Mode live fidèle | La réponse d'un connecteur live contient `source_mode=live` ; les exports statiques restent marqués `source_mode=export` et ne sont jamais présentés comme live. |

## 1. Stocker le credential de la source

Conservez le credential de la source live dans le magasin de secrets du runtime. La
configuration de la source y fera référence sous la forme `store:<name>`, jamais en
ligne.

```sh
olivares secrets put \
  --data-dir /var/lib/olivares \
  --name s3/prod-runbooks-read \
  --value-file /run/secrets/s3-prod-runbooks-read
```

Pour Google Drive, stockez les éléments OAuth bearer/refresh qu'utilise votre
déploiement pour accéder à Drive en lecture seule, sous un autre nom de secret.

## 2. Générer la configuration du RAG gouverné

Pour S3 :

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

Pour Google Drive, utilisez `--source gdrive --drive-id <shared-drive-id>` et une
référence de credential Drive.

La commande crée :

| Fichier | Rôle |
|---|---|
| `sources.json` | Enregistre la source de contenu sous `documents[]` avec `mode=live`. |
| `agent-gateway.json` | Active le serveur de ressources MCP avec `retrieval.enabled=true`. |
| `bootstrap-after-login.sh` | Crée la base de connaissances sémantique, ingère la source live, lie l'agent et ajoute la liaison au périmètre de la source. |

Si la commande avertit que `retrieval_semantic=false`, configurez d'abord
`OLIVARES_EMBEDDINGS_*`. Une base de connaissances adossée à un modèle refuse
intentionnellement l'ingestion si seul le repli par hachage local est disponible.

## 3. Démarrer avec la configuration générée

```sh
OLIVARES_SOURCES_CONFIG=/var/lib/olivares/quickstart/governed-rag/sources.json \
OLIVARES_AGENT_GATEWAY_CONFIG=/var/lib/olivares/quickstart/governed-rag/agent-gateway.json \
olivares quickstart --data-dir /var/lib/olivares
```

S'il s'agit d'une nouvelle installation, terminez la configuration initiale dans la
console. Exécutez ensuite le script de bootstrap avec un token d'administration :

```sh
OLIVARES_TOKEN=<admin-token> \
OLIVARES_TENANT=ten_... \
/var/lib/olivares/quickstart/governed-rag/bootstrap-after-login.sh
```

## 4. Prérequis d'identité

La garde de récupération lit les attributs d'identité dans le graphe roster/SCIM.
L'identité liée doit exister avant que Claude Code puisse récupérer du contenu
restreint :

| Attribut d'identité | Exemple |
|---|---|
| Sujet du token de l'agent / `agent_ref` | `claude-code-governed` |
| Identité NHI liée | `agent:claude-code-governed` |
| Métadonnée de niveau d'habilitation | `attr_clearance=confidential` ou supérieur |
| Appartenance à un groupe | `group:engineering` correspondant à l'ACL du document |

Si l'agent n'a pas d'identité, pas de niveau d'habilitation ou pas de groupe
correspondant, les fragments restreints ne sont pas renvoyés. Si l'agent n'est pas lié
à la base de connaissances par le périmètre de la source, l'appel de récupération MCP
échoue en mode fermé.

## 5. Connecter Claude Code à MCP

Configurez Claude Code avec l'URL de la ressource protégée affichée par le quickstart,
généralement :

```text
http://127.0.0.1:8446/mcp
```

Le token d'accès présenté à ce serveur de ressources MCP doit comporter :

| Claim/contrôle | Valeur requise |
|---|---|
| `iss` | L'émetteur configuré par `--mcp-issuer`. |
| `sub` | L'identifiant externe de l'agent, par exemple `claude-code-governed`. |
| Scope | `knowledge:retrieval:read`. |
| Audience/ressource | L'URL de la ressource MCP configurée dans `agent-gateway.json`. |

## 6. Vérifier

Exécutez la démo E2E de référence :

```sh
task demo:governed-rag
```

Elle vérifie le statut sémantique, la provenance de la source live, une récupération
autorisée et conforme au périmètre, la non-récupération avec un faible niveau
d'habilitation, un refus hors périmètre et la présence de `source_mode=live` dans le
résultat MCP.

Pour un déploiement existant, vérifiez aussi un document réel :

```sh
curl -sk "$OLIVARES_BASE_URL/v1/m/knowledge/kbs/$KB_ID/documents" \
  -H "Authorization: Bearer $OLIVARES_TOKEN" \
  -H "X-Olivares-Tenant: $OLIVARES_TENANT"
```

Chaque document ingéré depuis une source live doit afficher `source_mode: "live"`.
S'il affiche `export`, la base de connaissances a été alimentée depuis un fichier
d'export et doit être présentée comme telle aux opérateurs.
