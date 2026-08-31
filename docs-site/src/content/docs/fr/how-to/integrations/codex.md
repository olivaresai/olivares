---
title: Intégrer Codex
description: >-
  Placez Codex sous le control plane de gouvernance : le connecteur, la managed
  config, le hook gouverné et ce que la console affiche une fois en service.
---

Olivares AI intègre Codex au moyen de trois plans complémentaires. En mode lecture seule, la
source `codex` lit Analytics, Compliance, Audit Logs et les coûts facturés à l'aide d'identifiants
d'automatisation enterprise. Le connecteur `codex-managed-config` inventorie et vérifie la politique
système déployée. Enfin, `olivares codex-hook` achemine les sessions et les décisions d'outils vers
le PEP local. Une session authentifiée avec un abonnement ChatGPT personnel ne donne pas, à elle
seule, accès aux API enterprise.

## Ajouter Codex

### Prérequis

- Un tenant enterprise Olivares AI et un compte superadmin avec élévation AAL3 pour les opérations
  sur le roster.
- Pour l'ingestion enterprise, une clé API de plateforme ou un access token de workspace avec les
  scopes de lecture requis, ainsi que le `workspace_id`. Se connecter à la CLI Codex via ChatGPT
  ne fournit pas d'identifiant au connecteur.
- Un accès administrateur à la couche système de l'hôte pour distribuer
  `/etc/codex/requirements.toml`, `/etc/codex/managed_config.toml` et le hook de confiance.
- Un socket loopback dédié au PEP Codex. Sa valeur par défaut est `127.0.0.1:8448` ; ne le partagez
  pas avec Claude ou Grok, car chaque agent attend un format de réponse différent.

1. Ouvrez la **Control console** (`/console`) et sélectionnez **Connectors**.
2. Ajoutez une source de type `codex` avec un nom stable, le tenant et un intervalle par lot.
   `300` secondes constituent un point de départ raisonnable pour un pilote ; adaptez la fréquence
   au budget de l'API et à l'objectif de fraîcheur.
3. Pour une source enterprise, saisissez l'identifiant dans le champ secret `api_key`, sélectionnez
   `auth_mode` (`api_key` ou `access_token`) et fournissez le `workspace_id`. La console scelle la
   valeur et ne la renvoie jamais. Enregistrez, testez et rechargez la source.

Vous pouvez aussi ajouter `codex` sans identifiant pour inventorier le catalogue local. Ce mode
n'interroge ni Analytics, ni Compliance, ni Audit Logs, ni Costs, et `Gather` n'émet aucune
observation distante.

<img class="light:sl-hidden" src="/console/guias-connectors-dark.png" alt="Configurez qui accède et ce qu'il peut administrer : intégrez des utilisateurs, connectez le SSO et façonnez les espaces de travail et les groupes d'agents.">
<img class="dark:sl-hidden" src="/console/guias-connectors-light.png" alt="Configurez qui accède et ce qu'il peut administrer : intégrez des utilisateurs, connectez le SSO et façonnez les espaces de travail et les groupes d'agents.">

## Configurer Codex

### 1. Source enterprise en lecture seule

Les réglages suivants définissent la couverture :

| Réglage | Valeur par défaut | Objet |
|---|---:|---|
| `api_key` | vide | Référence à un identifiant d'automatisation. Une valeur vide active uniquement le catalogue hors ligne. |
| `auth_mode` | `api_key` | Identifie l'élément comme `api_key` ou `access_token` ; tous deux sont envoyés comme Bearer tokens. |
| `workspace_id` | vide | Requis pour Analytics et Compliance limités au workspace. |
| `analytics` | `true` | Utilisation et adoption de Codex ; produit des échantillons structurés et des constats. |
| `compliance` | `true` | Logs Codex Compliance comme preuves d'activité. |
| `audit` | `true` | Audit Logs de l'organisation comme preuves. |
| `costs` | `false` | Coût facturé quotidien. Activez-le avec `project_id` pour ne pas attribuer à Codex des dépenses sans rapport. |
| `attribute_email` | `false` | Conserve `user_id` comme acteur stable et évite d'utiliser l'e-mail comme PII d'attribution. |
| `compliance_prompt_scan` | `false` | Si activé, recherche transitoirement des motifs de risque et ne conserve que des constats structurés. |
| `otlp_http` | `false` | Récepteur de logs expérimental, désactivé car il ouvre un port. Il compte et draine actuellement les événements, mais ne les convertit pas en sessions. |

Gardez `otlp_http` désactivé pour l'intégration initiale. Le hook gouverné fournit le plan complet
des sessions ; activer OTLP dans cette version ne remplace pas cette installation.

Depuis la CLI, stockez l'identifiant hors de l'historique du shell et référencez-le par son nom :

```sh
olivares secrets put \
  --data-dir /var/lib/olivares \
  --name codex/enterprise \
  --value-file /run/secrets/codex-enterprise-token \
  --actor platform-operator \
  --reason codex-enterprise-onboarding

olivares sources set \
  --data-dir /var/lib/olivares \
  --name codex-enterprise \
  --kind codex \
  --tenant 11111111-1111-4111-8111-111111111111 \
  --poll-seconds 300 \
  --config api_key=store:codex/enterprise \
  --config auth_mode=access_token \
  --config workspace_id=ws_eng \
  --actor platform-operator \
  --reason codex-enterprise-onboarding
```

Si vous activez `costs=true`, ajoutez aussi `project_id=<project-id>`. Sans cette restriction,
l'API Costs couvre toute l'organisation et peut mélanger des dépenses sans rapport avec Codex.

### 2. Exigences système et valeurs administrées

Olivares maintient deux couches séparées :

- `requirements.toml` contient les restrictions que les utilisateurs ne peuvent pas élargir :
  politiques d'approbation, modes sandbox, recherche web, contrôle distant, confiance accordée
  aux hooks, lectures interdites et serveurs MCP autorisés.
- `managed_config.toml` contient les valeurs initiales administrées. Ce sont des valeurs par
  défaut ; toute restriction qui doit être immuable appartient à `requirements.toml`.

Le document de politique suivant est valide et refuse par défaut l'accès réseau, la recherche web,
le contrôle distant et MCP, tout en limitant les écritures au workspace :

```json
{
  "requirements": {
    "allowed_approval_policies": ["untrusted", "on-request"],
    "allowed_sandbox_modes": ["read-only", "workspace-write"],
    "allowed_web_search_modes": [],
    "allow_remote_control": false,
    "allow_managed_hooks_only": true,
    "deny_read": ["~/.ssh"],
    "allowed_mcp_servers": []
  },
  "managed_config": {
    "approval_policy": "on-request",
    "sandbox_mode": "workspace-write",
    "web_search": "disabled",
    "network_access": false
  }
}
```

Validez la politique avant distribution, puis générez les deux artefacts avec la même commande :

```sh
olivares codex managed-config \
  --policy /etc/olivares/codex-policy.json \
  --validate

olivares codex managed-config \
  --policy /etc/olivares/codex-policy.json \
  --requirements-out /etc/codex/requirements.toml \
  --managed-config-out /etc/codex/managed_config.toml
```

Le rendu échoue avant toute écriture si la politique contient un enum inconnu, un serveur MCP
sans identité ou du TOML invalide. Pour contrôler ensuite l'état actif et la dérive, enregistrez
une source supplémentaire de type `codex-managed-config` ; elle lit les deux fichiers système
sans les modifier.

### 3. Hook de session et PEP

Codex lit le hook mesuré depuis `$CODEX_HOME/hooks.json`. `command` doit être une chaîne, et non
un tableau : un tableau peut être parsé alors même que le hook ne s'exécute jamais. La table
inline `[hooks]` de `config.toml` n'était pas non plus lue par la version mesurée.

```json
{
  "description": "olivares governed hooks",
  "hooks": {
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "olivares codex-hook"}]}
    ],
    "PreToolUse": [
      {"matcher": "*", "hooks": [{"type": "command", "command": "olivares codex-hook"}]}
    ],
    "PostToolUse": [
      {"matcher": "*", "hooks": [{"type": "command", "command": "olivares codex-hook"}]}
    ],
    "SessionEnd": [
      {"hooks": [{"type": "command", "command": "olivares codex-hook"}]}
    ]
  }
}
```

Le serveur est monté au démarrage d'Olivares lorsque `OLIVARES_CODEX_HOOK_PEP_CONFIG` pointe vers
du JSON valide :

```json
{
  "listen": "127.0.0.1:8448",
  "tenant": "11111111-1111-4111-8111-111111111111"
}
```

Chaque instance gouverne un tenant et la décision provient du PDP déjà configuré dans Olivares.
Le client utilise `OLIVARES_CODEX_HOOK_URL`, `OLIVARES_CODEX_HOOK_TOKEN`,
`OLIVARES_CODEX_HOOK_TENANT`, `OLIVARES_CODEX_HOOK_AGENT`, `OLIVARES_CODEX_HOOK_ORG` et
`OLIVARES_CODEX_HOOK_ACCOUNT`. Fournissez ces valeurs par le processus et le gestionnaire de
secrets ; ne les incorporez pas dans `hooks.json`.

`allow_managed_hooks_only=true` est requis avant de présenter le hook comme un contrôle de flotte.
Sans application de la confiance, Codex peut omettre un hook sans produire d'événement ni
d'avertissement ; une installation silencieuse n'est pas une preuve d'enforcement.

<img class="light:sl-hidden" src="/console/guias-config-step-up-dark.png" alt="Authentification renforcée requise — AAL3 (matériel, résistant à l’hameçonnage)">
<img class="dark:sl-hidden" src="/console/guias-config-step-up-light.png" alt="Authentification renforcée requise — AAL3 (matériel, résistant à l’hameçonnage)">

## Utilisation de la CLI

Les exemples de sortie ont été mesurés le 30 août 2026. Les logs généraux de démarrage sont omis
afin de ne conserver que les réponses aux commandes.

### Enregistrement hors ligne reproductible

```sh
olivares sources set \
  --data-dir /var/lib/olivares \
  --name codex-demo \
  --kind codex \
  --tenant 11111111-1111-4111-8111-111111111111 \
  --poll-seconds 300 \
  --actor platform-operator \
  --reason integration-guide-rollout
```

```text
created source "codex-demo" (kind "codex", tenant "11111111-1111-4111-8111-111111111111", enabled true)
  kind: - → codex
  tenant: - → 11111111-1111-4111-8111-111111111111
  poll_seconds: - → 300
  enabled: - → true
→ reload a running engine to apply: POST /v1/console/runtime/reload, or `kill -HUP <pid>` (it also applies at next boot)
```

Avec SQLite, effectuez les modifications du roster hors ligne lorsque le moteur est arrêté ; avec
PostgreSQL, elles peuvent s'exécuter en parallèle. La console est la voie recommandée pour les
modifications à chaud de SQLite.

### Test de connectivité et ses limites

La mesure reproductible effectuée sur l'hôte des captures le 30 août 2026 a donné ce résultat :

```sh
olivares sources test \
  --data-dir /var/lib/olivares \
  --name codex-demo \
  --timeout 20s
```

```text
configuration: VALID (everything that can be decided without the network)
source "codex-demo" (codex): ANSWERED — the connector opened with this configuration and was closed again
NO SOURCE ROW WAS WRITTEN and nothing was wired into a running engine.
```

Le processus s'est terminé avec le code `0`. L'hôte disposait d'une session de CLI Codex
authentifiée via ChatGPT, mais `codex-demo` n'avait pas d'`api_key` : ce résultat prouve seulement
le catalogue hors ligne et l'acceptation de la configuration par `Open`. Il ne prouve pas
l'authentification OpenAI, n'appelle pas `Gather` et ne lit aucune ligne Analytics ou Compliance.
Même avec un identifiant, `sources test` n'envoie aucune requête en amont, car `Open` se contente
de construire les clients. Le premier test de données est un vrai poll du moteur, suivi
d'observations visibles.

### Valider la politique administrée

```sh
olivares codex managed-config \
  --policy /etc/olivares/codex-policy.json \
  --validate
```

```text
ok: policy renders to valid Codex managed-config TOML
```

### Tester le refus local du hook

Lorsque le point de terminaison est volontairement absent :

```sh
printf '%s\n' '{"hook_event_name":"PreToolUse","session_id":"019fc4c3-40c5-7371-9c92-7b269d23897b","tool_name":"Bash","tool_input":{"command":"echo hi"},"tool_use_id":"exec-1"}' | olivares codex-hook
```

```json
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"governed endpoint not configured (deny-closed)"}}
```

Le processus se termine avec le code `0`, car le refus est porté par le JSON interprété par
Codex. Cette sonde vérifie le client fail-closed ; l'acceptation d'un événement `PreToolUse` par
Codex doit aussi être testée sur un hôte où le hook est marqué comme étant de confiance.

## Control console

| Emplacement | Ce qui est affiché | Condition d'affichage |
|---|---|---|
| **Control console > Connectors** (`/console`) | Source, mode, fréquence, configuration non secrète et actions Test/Save/Reload. | La source persistée apparaît immédiatement ; ses données, non. |
| **Health > Connectors** (`/health`) | État du connecteur, message, tendance et dernière activité. | Après rechargement du roster. |
| **Observability > Ingestion** (`/observability`) | Compteurs pour `olivares.codex`, types d'observation et première/dernière réception. | Après que `Gather` a émis des données. Ces compteurs couvrent tout le processus, démarrent au boot et sont remis à zéro au redémarrage. |
| **Cost & FinOps** (`/finops`) | Utilisation Analytics estimée et, si activé, coût facturé quotidien. | Un identifiant valide, `workspace_id` et des API autorisées ; `costs` exige un opt-in explicite. |
| **Security** (`/security`) | Constats d'adoption, surfaces enterprise indisponibles et analyse structurée opt-in des données Compliance. | Après collecte ; les réponses 403/404 des surfaces enterprise deviennent des preuves de posture, pas un succès. |
| **Sessions** (`/sessions`) | Sessions et chronologie avec action, modèle, identité, coût et posture. | Provient du hook gouverné. La source batch seule ne crée pas de session active. |
| **Audit** (`/audit`) | Preuves d'activité importées et décisions PEP ancrées dans le ledger. | Après réception de logs ou de décisions attribuables. |

Ne considérez pas le catalogue hors ligne comme la preuve que le panneau des modèles contient un
inventaire distant. Le connecteur fournit un catalogue au runtime, mais aucun consommateur de
module dans cet arbre ne le publie sur cet écran.

<img class="light:sl-hidden" src="/console/health-dark.png" alt="Disponibilité, fiabilité et dépendances de votre parc — dérivées de l’activité observée et du balayage d’obsolescence, jamais par sondage de l’infrastructure.">
<img class="dark:sl-hidden" src="/console/health-light.png" alt="Disponibilité, fiabilité et dépendances de votre parc — dérivées de l’activité observée et du balayage d’obsolescence, jamais par sondage de l’infrastructure.">
<img class="light:sl-hidden" src="/console/finops-dark.png" alt="Coût en tokens sur l’ensemble du parc — tendances, refacturation, rapprochement, budgets et prévision. Les chiffres sont exactement ceux que rapporte le registre FinOps.">
<img class="dark:sl-hidden" src="/console/finops-light.png" alt="Coût en tokens sur l’ensemble du parc — tendances, refacturation, rapprochement, budgets et prévision. Les chiffres sont exactement ceux que rapporte le registre FinOps.">
<img class="light:sl-hidden" src="/console/security-dark.png" alt="Constats de garde-fous, posture d’application, file des anomalies et investigation d’incidents inviolable. Le plan est détectif par défaut — il enregistre, il ne bloque pas de lui-même tant que l’application n’est pas activée et gouvernée.">
<img class="dark:sl-hidden" src="/console/security-light.png" alt="Constats de garde-fous, posture d’application, file des anomalies et investigation d’incidents inviolable. Le plan est détectif par défaut — il enregistre, il ne bloque pas de lui-même tant que l’application n’est pas activée et gouvernée.">

## Utilisation en production

- **Pilote sans identifiant :** validez le packaging et le roster avec `codex-demo`, mais
  étiquetez-le comme catalogue hors ligne. Ne l'utilisez pas comme indicateur de connectivité
  enterprise.
- **Ingestion de gouvernance :** utilisez une identité d'automatisation en lecture seule et le jeu
  minimal d'API. Gardez `attribute_email=false`, sauf besoin approuvé de refacturation.
- **Contrôle des points de terminaison :** générez les fichiers TOML à partir d'une politique
  versionnée, distribuez-les avec le système de configuration de flotte et interrogez leur état
  avec `codex-managed-config` pour distinguer intention, déploiement et dérive.
- **Contrôle des sessions :** installez d'abord les hooks sur un groupe canari. Confirmez que
  `PreToolUse` bloque une action sans danger avant d'élargir le cercle. Un hook qui n'a produit
  aucun événement ne doit pas être compté comme gouverné.
- **FinOps exact :** n'activez `costs` que si `project_id` limite les données aux dépenses Codex.
  Utilisez Analytics pour l'adoption et l'API Costs pour le montant facturé ; ne les additionnez
  pas comme s'il s'agissait de deux factures.

## Ce qui est appliqué et ce qui est seulement observé

| Surface | Comportement réel |
|---|---|
| Source `codex` et API enterprise | **Observées, en lecture seule.** Ne modifient pas la configuration OpenAI et n'interceptent pas l'inférence. |
| Mode sans `api_key` | **Catalogue hors ligne.** Ne prouve ni l'abonnement ChatGPT, ni l'API distante, ni le workspace. |
| `requirements.toml` | **Applique des restrictions système** que les utilisateurs ne peuvent pas élargir, notamment la confiance exclusive accordée aux hooks administrés. |
| `managed_config.toml` | **Définit des valeurs administrées par défaut.** Ne remplace pas une restriction de `requirements.toml`. |
| `codex-managed-config` | **Observe et compare la dérive.** Ne corrige jamais les fichiers de l'hôte. |
| `olivares codex-hook` sur `PreToolUse` ou `PermissionRequest` | **Peut empêcher l'action.** Codex n'accepte pas `permissionDecision=allow` ; Olivares représente allow comme une non-intervention et transforme une requête `ask` en refus. |
| `PostToolUse` et événements de cycle de vie | **Preuves aux capacités inégales.** Un blocage ultérieur ne peut pas annuler un outil déjà exécuté, et `SessionEnd` n'a aucune sortie de veto. |
| Récepteur OTLP Codex | **Réception partielle dans cette version.** Compte et draine les événements, mais ne les transforme pas encore en sessions ni en constats. |

L'achèvement est cumulatif : la source doit être rechargée, le premier `Gather` doit renvoyer des
données enterprise, la politique système doit être vérifiée, le hook de confiance doit être
observé et `PreToolUse` doit avoir fait l'objet d'un veto effectif. `ANSWERED` ne couvre que la
première partie de `Open`.
