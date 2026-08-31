---
title: Intégrer Claude Code
description: >-
  Placez Claude Code sous le control plane de gouvernance : le connecteur, les
  managed settings, le hook PEP gouverné et ce que la console affiche une fois en service.
---

Cette intégration place Claude Code sous le control plane de gouvernance sans faire d'Olivares AI
un proxy obligatoire. Le connecteur `claude` reçoit la télémétrie OTLP et les événements de hook,
corrèle les sessions et enregistre les accès R/RW, les coûts et les constats. Lorsqu'un contrôle
préventif est nécessaire, le hook administré `olivares claude-hook` interroge le PEP Olivares local
avant chaque utilisation d'outil. Ces deux plans sont indépendants : recevoir de la télémétrie ne
signifie pas que la politique est appliquée.

## Ajouter Claude Code

### Prérequis

- Un binaire Olivares AI incluant le connecteur first-party `claude`.
- L'UUID du tenant enterprise auquel les observations seront attribuées.
- Claude Code installé sur les postes à gouverner. Le récepteur local ne nécessite pas de clé API
  Anthropic.
- Une connectivité locale de Claude Code vers le récepteur Olivares. Les valeurs par défaut sont
  `127.0.0.1:4317` pour OTLP/gRPC et `127.0.0.1:4318` pour OTLP/HTTP et les hooks coopératifs.
- Un chemin temporaire exécutable pour le service Olivares. `claude` s'exécute comme plugin isolé ;
  sur les systèmes où `/tmp` est monté avec `noexec`, définissez `TMPDIR` dans l'unité du service
  sur un répertoire dédié appartenant au compte de service Olivares.

N'exposez pas les récepteurs OTLP ni le point de terminaison coopératif au-delà du loopback. Ils
n'authentifient pas l'émetteur : tout hôte capable de les atteindre pourrait fabriquer de la
télémétrie. Le PEP gouverné est une surface distincte : il utilise son propre socket local,
authentifie chaque requête et enregistre chaque décision.

1. Ouvrez la **Control console** (`/console`) et sélectionnez l'onglet **Connectors**. Le roster des
   connecteurs est global : un compte superadmin est requis, et l'enregistrement, le test et le
   rechargement nécessitent une élévation AAL3.
2. Ajoutez une source de type `claude`, avec un nom opérationnel stable tel que
   `claude-code-prod`, le tenant approprié, le mode `live`, l'intervalle `0` et l'état activé. Un
   intervalle nul est correct : ce connecteur maintient des récepteurs au lieu d'effectuer des
   interrogations par lot.
3. Enregistrez la source et sélectionnez **Reload**. La ligne confirme son nom, son type, son mode
   et son état. L'action de test de la console n'est pas disponible pour `claude`, car il s'agit
   d'un connecteur hors processus ; la validation se fait à l'enregistrement, et le test complet
   d'ouverture utilise `olivares sources test`, qui lance le plugin.

<img class="light:sl-hidden" src="/console/guias-connectors-dark.png" alt="Configurez qui accède et ce qu'il peut administrer : intégrez des utilisateurs, connectez le SSO et façonnez les espaces de travail et les groupes d'agents.">
<img class="dark:sl-hidden" src="/console/guias-connectors-light.png" alt="Configurez qui accède et ce qu'il peut administrer : intégrez des utilisateurs, connectez le SSO et façonnez les espaces de travail et les groupes d'agents.">

## Configurer Claude Code

Distribuez ensemble deux configurations : la source d'observation et la politique administrée
de l'agent.

### 1. Récepteur et minimisation des données

La configuration initiale sûre correspond aux valeurs par défaut :

| Réglage de la source | Valeur initiale | Effet |
|---|---:|---|
| `enable_grpc` | `true` | Sert OTLP/gRPC sur `grpc_addr` (`127.0.0.1:4317`). |
| `enable_http` | `true` | Sert OTLP/HTTP et le hook coopératif sur `http_addr` (`127.0.0.1:4318`). |
| `hook_path` | `/hooks` | Chemin du hook coopératif dans le récepteur HTTP. |
| `content_capture` | vide | Conserve la structure, mais pas les prompts, les corps d'outils ni les corps d'API. Le raisonnement étendu est toujours expurgé. |
| `enforcement` | vide | Observe les hooks ; cette source ne renvoie pas de décisions préventives. |
| `allow_public_bind` | `false` | Refuse une écoute hors du loopback. |

Si un hôte exécute plusieurs récepteurs OTLP, affectez à chacun une adresse de loopback différente
et utilisez la même valeur dans la configuration de l'agent. Claude, Codex et Grok utilisent
`4318` par défaut dans certains modes et ne peuvent pas réserver le même socket simultanément.

### 2. Managed settings et PEP gouverné

Générez le fichier système de Claude Code avec le binaire Olivares :

```sh
olivares agent managed-settings \
  --otel-endpoint http://127.0.0.1:4317 \
  --out /etc/claude-code/managed-settings.json
```

Le générateur installe `allowManagedHooksOnly: true`, un hook `PreToolUse` qui exécute
`olivares claude-hook`, ainsi que le hook d'expurgation `PostToolUse`. Il active aussi OTLP avec
le protocole `grpc` ; le point de terminaison ci-dessus utilise donc le récepteur `4317`, et non
le récepteur HTTP `4318`. Le fichier appartient à la couche système administrée, pas au `HOME`
de la session.

Le serveur PEP est activé lorsqu'Olivares démarre avec un fichier indiqué par
`OLIVARES_HOOK_PEP_CONFIG`. Voici un exemple de politique valide pour un tenant :

```json
{
  "listen": "127.0.0.1:8447",
  "tenants": [
    {
      "tenant": "11111111-1111-4111-8111-111111111111",
      "require_firm_identity": true,
      "enforcement": "enforce",
      "policy": {
        "version": "claude-prod-v1",
        "default": "allow",
        "rules": [
          {
            "tool": "Bash",
            "decision": "ask",
            "reason": "Shell commands require human confirmation"
          }
        ]
      }
    }
  ]
}
```

Les sessions lancées par Olivares reçoivent des valeurs éphémères pour
`OLIVARES_HOOK_PEP_URL`, `OLIVARES_HOOK_PEP_TOKEN`, `OLIVARES_HOOK_PEP_TENANT` et l'attribution
de l'agent. Pour une session lancée indépendamment, l'opérateur doit fournir ces valeurs par le
canal des secrets ; ne les écrivez pas dans `managed-settings.json`. Si le point de terminaison
est absent ou indisponible, `olivares claude-hook` renvoie `deny`.

Pour un déploiement initial non bloquant, utilisez le mode `observe` avec une valeur RFC3339
future dans `observe_until`. Cette tolérance est temporaire : un horodatage absent, invalide ou
expiré se résout en `enforce`. Les invariants de plateforme — notamment l'identité, le tenant, le
kill switch, le pare-feu et les erreurs fail-closed — restent appliqués pendant l'observation des
règles métier.

<img class="light:sl-hidden" src="/console/guias-config-step-up-dark.png" alt="Authentification renforcée requise — AAL3 (matériel, résistant à l’hameçonnage)">
<img class="dark:sl-hidden" src="/console/guias-config-step-up-light.png" alt="Authentification renforcée requise — AAL3 (matériel, résistant à l’hameçonnage)">

## Utilisation de la CLI

Les extraits de sortie suivants ont été mesurés avec le binaire construit depuis ce worktree le
30 août 2026. Les messages généraux de démarrage du moteur sont omis.

### Enregistrer la source

Avec SQLite, arrêtez le moteur avant de modifier le roster depuis la CLI, car il utilise un profil
à écrivain unique. Avec PostgreSQL, l'opération peut s'exécuter en parallèle du moteur. Utilisez
la console pour les modifications à chaud de SQLite.

```sh
olivares sources set \
  --data-dir /var/lib/olivares \
  --name claude-code-prod \
  --kind claude \
  --tenant 11111111-1111-4111-8111-111111111111 \
  --poll-seconds 0 \
  --config mode=live \
  --actor platform-operator \
  --reason integration-guide-rollout
```

```text
created source "claude-code-prod" (kind "claude", tenant "11111111-1111-4111-8111-111111111111", enabled true)
  kind: - → claude
  tenant: - → 11111111-1111-4111-8111-111111111111
  poll_seconds: - → 0
  enabled: - → true
  config.mode: - → live
→ reload a running engine to apply: POST /v1/console/runtime/reload, or `kill -HUP <pid>` (it also applies at next boot)
```

`--actor` et `--reason` sont obligatoires, car cette modification change la provenance des données
et est enregistrée dans le ledger d'audit.

### Valider et ouvrir le connecteur

```sh
olivares sources validate \
  --data-dir /var/lib/olivares \
  --name claude-code-prod

olivares sources test \
  --data-dir /var/lib/olivares \
  --name claude-code-prod \
  --timeout 20s
```

```text
source "claude-code-prod"
configuration: VALID (everything that can be decided without the network)
  ? not checked here: the "claude" connector runs out-of-process, so its connector identity is only known once the binary is launched (`olivares sources test` launches it)
configuration: VALID (everything that can be decided without the network)
  ? not checked here: the "claude" connector runs out-of-process, so its connector identity is only known once the binary is launched (`olivares sources test` launches it)
source "claude-code-prod" (claude): ANSWERED — the connector opened with this configuration and was closed again
NO SOURCE ROW WAS WRITTEN and nothing was wired into a running engine.
```

`validate` n'ouvre aucun socket. `test` appelle `Open` et `Close`, mais n'appelle pas `Gather`,
ne raccorde pas la source au moteur et ne prouve pas que Claude Code envoie de la télémétrie. Si
le plugin échoue avec `permission denied` malgré son bit exécutable, vérifiez si le `TMPDIR` du
processus se trouve sur un volume `noexec`.

### Confirmer le comportement fail-closed du hook

Lorsque le point de terminaison est volontairement laissé sans configuration, le client renvoie
un refus dans le format attendu par Claude Code :

```sh
printf '%s\n' '{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/repo/README.md"}}' | olivares claude-hook
```

```json
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"governed PEP endpoint not configured (deny-closed)"}}
```

Cette sonde vérifie le client local, et non une décision de politique distante. En production,
testez également une règle autorisée, une règle refusée et une requête `ask` avec une identité
ferme avant d'élargir le déploiement.

## Control console

L'ajout d'une source ne crée pas de données historiques. Après rechargement du roster et réception
du premier événement, les opérateurs peuvent utiliser les vues suivantes :

| Emplacement | Ce qui est affiché | Comment interpréter l'état |
|---|---|---|
| **Control console > Connectors** (`/console`) | Nom, type `claude`, mode, configuration non secrète, état du roster et actions d'enregistrement/rechargement. | « Enregistré » prouve la persistance. Il ne prouve pas qu'un événement est arrivé. |
| **Health > Connectors** (`/health`) | Santé du connecteur, message opérationnel, tendance et dernier poll ou dernière activité connus. | Un récepteur ouvert peut être sain alors que l'agent reste silencieux. |
| **Observability > Ingestion** (`/observability`) | Enregistrements par source, types `edge`, `cost` et `finding`, signal et premier/dernier événement. | Ces compteurs couvrent tout le processus depuis le démarrage ; ils sont remis à zéro au redémarrage et ne sont pas propres au tenant. |
| **Sessions** (`/sessions`) | Session, état, action, modèle, tokens, coût, dernière activité et posture `enforced` ou `observed`. | La posture résume les preuves d'événements ; elle n'est pas déduite de l'enregistrement du connecteur. |
| **Access map** (`/access-map`) | Arêtes R/RW attribuées à partir des outils, de MCP et des ressources observés. | Une arête observée décrit une activité ; elle n'équivaut pas à une autorisation préalable. |
| **Cost & FinOps** (`/finops`) | Échantillons de coûts et de tokens dérivés de la télémétrie reçue. | La couverture se limite à ce que la flotte exporte ; les appels qui n'ont jamais émis d'OTLP ne peuvent pas être reconstruits. |
| **Security** (`/security`) | Lacunes de télémétrie, posture sandbox/MCP et autres constats émis. | L'absence de constat ne rend pas conforme une surface non observée. |
| **Claude Policy** (`/claude-policy`) | Création, distribution, versions et état de check-in des surfaces Claude Code administrées. | La distribution et la vérification de dérive sont des faits distincts affichés séparément. |

<img class="light:sl-hidden" src="/console/sessions-dark.png" alt="Opération des agents en direct — ce que chaque session fait à l’instant même, ses tokens, son coût et sa cadence, mis à jour via un flux en direct.">
<img class="dark:sl-hidden" src="/console/sessions-light.png" alt="Opération des agents en direct — ce que chaque session fait à l’instant même, ses tokens, son coût et sa cadence, mis à jour via un flux en direct.">
<img class="light:sl-hidden" src="/console/security-dark.png" alt="Constats de garde-fous, posture d’application, file des anomalies et investigation d’incidents inviolable. Le plan est détectif par défaut — il enregistre, il ne bloque pas de lui-même tant que l’application n’est pas activée et gouvernée.">
<img class="dark:sl-hidden" src="/console/security-light.png" alt="Constats de garde-fous, posture d’application, file des anomalies et investigation d’incidents inviolable. Le plan est détectif par défaut — il enregistre, il ne bloque pas de lui-même tant que l’application n’est pas activée et gouvernée.">

## Utilisation en production

- **Déploiement progressif :** commencez avec du contenu structurel et des règles en mode observé,
  avec une date d'expiration. Examinez les faux positifs, puis passez chaque tenant à `enforce`.
- **Administration de flotte :** distribuez `/etc/claude-code/managed-settings.json` par RPM,
  image immuable, Ansible, Salt ou un gestionnaire de configuration enterprise équivalent.
  Contrôlez le fichier actif avec une seconde source `managed-settings` pour détecter son absence
  ou sa dérive.
- **Séparation des responsabilités :** l'équipe plateforme maintient les récepteurs et leur
  disponibilité ; l'équipe sécurité versionne les règles ; les responsables des tenants examinent
  les requêtes `ask` et les constats. Chaque modification privilégiée reste attribuable.
- **Minimisation des données :** laissez `content_capture` vide, sauf besoin forensique approuvé
  avec résidence et rétention définies. Les données structurelles suffisent généralement aux
  analyses d'adoption et de coûts.
- **Hôtes durcis :** gardez les récepteurs sur le loopback, fournissez au plugin un répertoire
  temporaire exécutable minimal et placez la politique en lecture seule. Ne relâchez pas `noexec`
  globalement pour faire démarrer le connecteur.

## Ce qui est appliqué et ce qui est seulement observé

| Surface | Comportement réel |
|---|---|
| Télémétrie OTLP et hook coopératif du connecteur `claude` | **Observés.** L'émetteur coopère ; le récepteur loopback n'authentifie pas, et un processus local peut omettre ou fabriquer un signal. |
| Réglage `enforcement` vide sur la source | **Observé.** C'est la valeur par défaut et elle ne bloque aucun outil. |
| `olivares claude-hook` + PEP + managed settings | **Applique** `allow`, `ask` ou `deny` aux événements sur lesquels Claude Code peut opposer un veto, et enregistre la décision. Les pannes du point de terminaison refusent en deny-closed. |
| `allowManagedHooksOnly` dans la couche administrée | **Durcit l'installation** contre les hooks utilisateur ou projet susceptibles de concurrencer le PEP. |
| `PostToolUse` | **Observe et expurge après l'action.** Il ne peut pas annuler les effets déjà produits par l'outil. |
| Actions hors du processus et du hook Claude Code | **Non couvertes par ce raccordement.** Utilisez les contrôles du système d'exploitation, l'audit natif et les politiques réseau comme protections complémentaires. |

La vérification opérationnelle exige quatre contrôles distincts : un roster persisté, un connecteur
ouvert, un événement visible dans **Ingestion** et un outil réellement bloqué par le PEP. Aucun de
ces contrôles ne remplace les trois autres.
