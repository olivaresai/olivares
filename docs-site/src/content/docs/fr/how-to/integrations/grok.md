---
title: Intégrer Grok Build
description: >-
  Placez Grok Build sous le control plane de gouvernance : le connecteur, le hook
  gouverné et ce que la console affiche une fois en service.
---

L'intégration `grok` gouverne **Grok Build, l'agent de terminal**, depuis l'hôte où il s'exécute.
En mode lecture seule, elle lit la configuration TOML, le profil sandbox, les noms des serveurs
MCP, les exigences système et le fichier qui désactive les hooks. Elle peut également recevoir
des traces OTLP. Ce n'est pas le connecteur de l'API xAI : il n'interroge aucun modèle distant et
ne nécessite aucun secret du fournisseur. Le contrôle préventif des outils utilise
`olivares grok-hook` et un PEP local distinct.

## Ajouter Grok Build

### Prérequis

- Olivares AI et Grok Build installés sur le même hôte, ou les chemins de configuration Grok
  montés en lecture seule sur l'hôte du connecteur.
- L'UUID du tenant auquel la posture sera attribuée.
- L'autorisation pour le compte de service Olivares de lire `~/.grok/config.toml`,
  `/etc/grok/requirements.toml`, `~/.grok/disabled-hooks` et, si configuré, le fichier compatible
  `managed-settings.json`.
- Un compte superadmin avec élévation AAL3 si la source est créée depuis la console.

Ne saisissez pas de clé xAI pour cette source. Elle n'a aucun champ secret et n'effectue aucun
appel à l'API d'inférence.

1. Ouvrez la **Control console** (`/console`) et sélectionnez l'onglet **Connectors**.
2. Ajoutez une source de type `grok` avec le nom `grok-demo` — ou un nom d'hôte stable —, le
   tenant, un intervalle par lot et l'état activé. `60` secondes permettent de voir les changements
   de posture pendant un pilote sans transformer les lectures locales de fichiers en boucle continue.
3. Enregistrez la source, sélectionnez **Test** et rechargez le roster. La ligne confirme l'entrée
   du roster ; le premier `Gather` qui suit est ce qui lit les fichiers et émet les constats.

<img class="light:sl-hidden" src="/console/guias-connectors-dark.png" alt="Configurez qui accède et ce qu'il peut administrer : intégrez des utilisateurs, connectez le SSO et façonnez les espaces de travail et les groupes d'agents.">
<img class="dark:sl-hidden" src="/console/guias-connectors-light.png" alt="Configurez qui accède et ce qu'il peut administrer : intégrez des utilisateurs, connectez le SSO et façonnez les espaces de travail et les groupes d'agents.">

## Configurer Grok Build

### 1. Inventaire et exigences de l'hôte

| Réglage de la source | Valeur par défaut | Ce qui est mesuré |
|---|---|---|
| `agent_ref` | `grok-build` | Référence stable incluse dans les constats. |
| `config_path` | `~/.grok/config.toml` | Profil sandbox et noms de serveurs MCP déclarés par l'utilisateur. |
| `requirements_path` | `/etc/grok/requirements.toml` | Couche système qui contraint la configuration effective. |
| `disabled_hooks_path` | `~/.grok/disabled-hooks` | Noms des hooks désactivés par l'utilisateur, un par ligne. |
| `managed_settings_path` | vide | `managed-settings.json` compatible Claude Code et honoré par Grok ; vide signifie « non mesuré ». |
| `otlp_http` | `false` | Récepteur de traces, désactivé jusqu'à ce que l'opérateur réserve un port. |

Sous Linux, l'exigence minimale pour appliquer le sandbox est :

```toml
[sandbox]
profile = "strict"
```

Distribuez-la dans `/etc/grok/requirements.toml` avec une propriété administrative. `strict`
limite les écritures au workspace, à `~/.grok/` et aux répertoires temporaires, et bloque l'accès
réseau selon la garantie Linux documentée. La même valeur dans `~/.grok/config.toml` n'est qu'une
préférence utilisateur : les options de ligne de commande et l'environnement peuvent influer sur
la configuration, tandis que `requirements.toml` est la couche contraignante.

Pour restreindre MCP, ne déclarez dans `requirements.toml` que les tables
`[mcp_servers.<nombre-aprobado>]` que la flotte peut utiliser. Olivares inventorie les noms, et non
les commandes, URL ou identifiants contenus dans ces tables. Un fichier absent, un fichier illisible
et un fichier présent sans `[mcp_servers]` produisent des états différents ; « non mesuré » n'est
jamais affiché comme « aucun ».

Grok peut aussi lire `/etc/claude-code/managed-settings.json` à des fins de compatibilité. Ne
définissez `managed_settings_path` que si Olivares doit mesurer cette surface. Ne réutilisez pas
un hook Claude sans vérification : les payloads Grok utilisent des clés camelCase et des
événements snake_case, et nécessitent `olivares grok-hook`.

### 2. Hook gouverné

Installez `olivares grok-hook` au moyen du mécanisme de découverte natif de la version Grok
déployée : soit un fichier JSON de réglages dont Grok consomme la clé `hooks`, soit un fichier
`*.json` dans un répertoire de hooks tel que `~/.grok/hooks/`. Grok charge ces fichiers par leur
nom. Olivares ne définit pas le wrapper de création complet et cet arbre ne le conserve pas ;
utilisez le schéma de la version installée et définissez la commande exactement sur :

```text
olivares grok-hook
```

Le PEP est monté lorsque `OLIVARES_GROK_HOOK_PEP_CONFIG` pointe vers une configuration valide au
démarrage d'Olivares :

```json
{
  "listen": "127.0.0.1:8449",
  "tenant": "11111111-1111-4111-8111-111111111111"
}
```

Chaque instance gouverne un tenant et exige une identité ferme. Le client lit
`OLIVARES_GROK_HOOK_URL`, `OLIVARES_GROK_HOOK_TOKEN`, `OLIVARES_GROK_HOOK_TENANT`,
`OLIVARES_GROK_HOOK_AGENT`, `OLIVARES_GROK_HOOK_ORG` et `OLIVARES_GROK_HOOK_ACCOUNT`.
Fournissez ces valeurs par le processus et le gestionnaire de secrets ; le token n'appartient pas
au JSON du hook.

Le nom attribué au hook est important. Un utilisateur peut l'ajouter à `~/.grok/disabled-hooks`,
et le dispatcher l'omettra, qu'il provienne ou non d'une couche administrée. Ni
`requirements.toml` ni MDM ne contraignent ce fichier. Le connecteur le lit et émet un constat de
haute sévérité avec les noms désactivés, mais il ne peut pas empêcher la désactivation.

### 3. Traces OTLP facultatives

Lorsque `otlp_http=true`, le récepteur écoute par défaut sur `127.0.0.1:4318` et accepte
`POST /v1/traces`, le chemin mesuré pour Grok Build. Cette entrée non authentifiée doit rester sur
le loopback. Si un autre connecteur utilise déjà `4318`, sélectionnez un port local libre et
appliquez la même valeur à `otlp_http_addr` et au point de terminaison OTLP de l'agent.

La collecte réduit les traces à l'attribution, au nom du span et à `session_id` ; elle ne conserve
aucun contenu. Dans cette version, le poll suivant émet un constat agrégé avec les nombres de spans,
de sessions et d'abandons. Utilisez le hook pour la chronologie et le contrôle par outil.

<img class="light:sl-hidden" src="/console/guias-config-step-up-dark.png" alt="Authentification renforcée requise — AAL3 (matériel, résistant à l’hameçonnage)">
<img class="dark:sl-hidden" src="/console/guias-config-step-up-light.png" alt="Authentification renforcée requise — AAL3 (matériel, résistant à l’hameçonnage)">

## Utilisation de la CLI

Les exemples suivants ont été exécutés avec le binaire du worktree le 30 août 2026. Les messages
généraux de démarrage sont omis.

### Enregistrer la source locale

```sh
olivares sources set \
  --data-dir /var/lib/olivares \
  --name grok-demo \
  --kind grok \
  --tenant 11111111-1111-4111-8111-111111111111 \
  --poll-seconds 60 \
  --actor platform-operator \
  --reason integration-guide-rollout
```

```text
created source "grok-demo" (kind "grok", tenant "11111111-1111-4111-8111-111111111111", enabled true)
  kind: - → grok
  tenant: - → 11111111-1111-4111-8111-111111111111
  poll_seconds: - → 60
  enabled: - → true
→ reload a running engine to apply: POST /v1/console/runtime/reload, or `kill -HUP <pid>` (it also applies at next boot)
```

Avec SQLite, arrêtez le moteur avant une modification hors ligne du roster ou utilisez la console
active. Avec PostgreSQL, la commande peut s'exécuter en parallèle du moteur. `--actor` et
`--reason` attribuent la modification de provenance.

Pour les chemins non standard, ajoutez des valeurs de configuration explicites :

```sh
olivares sources set \
  --data-dir /var/lib/olivares \
  --name grok-demo \
  --config config_path=/srv/grok-home/.grok/config.toml \
  --config requirements_path=/etc/grok/requirements.toml \
  --config disabled_hooks_path=/srv/grok-home/.grok/disabled-hooks \
  --config managed_settings_path=/etc/claude-code/managed-settings.json \
  --actor platform-operator \
  --reason grok-paths-for-service-user
```

### Test de connectivité et lecture effective des fichiers

La mesure reproductible effectuée sur l'hôte des captures le 30 août 2026 a donné ce résultat :

```sh
olivares sources test \
  --data-dir /var/lib/olivares \
  --name grok-demo \
  --timeout 20s
```

```text
configuration: VALID (everything that can be decided without the network)
source "grok-demo" (grok): ANSWERED — the connector opened with this configuration and was closed again
NO SOURCE ROW WAS WRITTEN and nothing was wired into a running engine.
```

Le processus s'est terminé avec le code `0`. Cet hôte avait une session Grok active et un fichier
`~/.grok/config.toml` présent ; `/etc/grok/requirements.toml` et `~/.grok/disabled-hooks` étaient
absents. `sources test` n'en a lu aucun : `Open` se contente de résoudre la configuration et
`test` ferme immédiatement sans appeler `Gather`. Par conséquent, `ANSWERED` ne prouve ni la
session, ni le sandbox, ni les constats. Pour tester la lecture des fichiers, rechargez le moteur
et examinez les constats émis par le poll suivant.

### Vérifier le comportement fail-closed du client de hook

Lorsque le point de terminaison n'est pas configuré :

```sh
printf '%s' '{"hookEventName":"pre_tool_use","sessionId":"s-1","toolName":"Bash"}' | olivares grok-hook
```

Sortie standard :

```json
{"decision":"deny","reason":"no governance endpoint is configured (deny-closed)"}
```

Erreur standard :

```text
no governance endpoint is configured (deny-closed)
```

Le code de sortie est `2`, que Grok interprète comme un veto pour `pre_tool_use`. Pour les autres
événements, un refus est enregistré, mais ne peut pas empêcher l'action ; le client le signale sur
stderr au lieu de prétendre à un enforcement.

## Control console

| Emplacement | Ce qui est affiché | Limite opérationnelle |
|---|---|---|
| **Control console > Connectors** (`/console`) | Roster `grok`, chemins configurés, intervalle, mode et actions Test/Save/Reload. | Le test ouvre et ferme le connecteur ; il ne lit pas les fichiers TOML. |
| **Health > Connectors** (`/health`) | État de la source, message, tendance et dernier poll. | La santé du processus ne prouve pas qu'un fichier absent est gouverné. |
| **Observability > Ingestion** (`/observability`) | Constats émis par `olivares.grok`, premier/dernier enregistrement et, si activée, activité OTLP agrégée. | Compteurs couvrant tout le processus depuis le démarrage ; ils sont remis à zéro et ne sont pas propres au tenant. |
| **Security** (`/security`) | Profil sandbox observé et appliqué, noms MCP, présence/validité des exigences, compatibilité des managed settings et noms des hooks désactivés. | « Illisible » reste inconnu au lieu de devenir absent. |
| **Sessions** (`/sessions`) | Session, action, identité, mode d'autorisation, dernière activité et posture `enforced` ou `observed`. | Nécessite des événements de hook. L'inventaire local ne crée pas de session. |
| **Audit** (`/audit`) | Décisions PEP attribuables et preuves chaînées. | N'existe que pour les appels ayant atteint le PEP ; un hook désactivé laisse une lacune. |

N'attendez ni catalogue de modèles, ni dépenses xAI, ni prompts : cette source n'utilise pas l'API
xAI et le récepteur OTLP élimine le contenu.

<img class="light:sl-hidden" src="/console/observability-counters-dark.png" alt="Santé d’ingestion fondée sur les standards et exploration des traces corrélées au registre. Les chiffres concernent l’ensemble du moteur (globaux au processus), et non chaque locataire ; les standards sont épinglés aux versions et niveaux de maturité déclarés par les organismes en amont.">
<img class="dark:sl-hidden" src="/console/observability-counters-light.png" alt="Santé d’ingestion fondée sur les standards et exploration des traces corrélées au registre. Les chiffres concernent l’ensemble du moteur (globaux au processus), et non chaque locataire ; les standards sont épinglés aux versions et niveaux de maturité déclarés par les organismes en amont.">
<img class="light:sl-hidden" src="/console/security-dark.png" alt="Constats de garde-fous, posture d’application, file des anomalies et investigation d’incidents inviolable. Le plan est détectif par défaut — il enregistre, il ne bloque pas de lui-même tant que l’application n’est pas activée et gouvernée.">
<img class="dark:sl-hidden" src="/console/security-light.png" alt="Constats de garde-fous, posture d’application, file des anomalies et investigation d’incidents inviolable. Le plan est détectif par défaut — il enregistre, il ne bloque pas de lui-même tant que l’application n’est pas activée et gouvernée.">

## Utilisation en production

- **Baselines des postes Linux :** distribuez `requirements.toml` comme fichier appartenant à
  root et interrogez chaque hôte. Une absence devient un constat exploitable, pas un état vert par
  défaut.
- **Contrôle MCP :** comparez les noms déclarés par l'utilisateur à ceux fixés par l'administrateur.
  La variable `GROK_CONFIG` ne peut pas ajouter de tables sensibles comme MCP, l'authentification
  ou l'egress ; cette protection vient de Grok et Olivares la rapporte sans la dupliquer.
- **Canari des hooks :** commencez avec un outil sans danger et confirmez l'événement, la décision
  et l'effet. Surveillez ensuite `disabled-hooks` en continu, car le contrôle peut disparaître par
  son nom.
- **Points de terminaison partagés :** configurez des chemins absolus vers le véritable `HOME` du
  compte qui exécute Grok. Le `~` du service Olivares peut se résoudre vers un autre utilisateur
  et produire une mesure exacte du mauvais profil hôte.
- **Télémétrie minimale :** n'activez OTLP que si le signal agrégé est nécessaire, et réservez un
  socket local dédié. Pour la gouvernance préventive, donnez la priorité à une exécution fiable du
  hook.

## Ce qui est appliqué et ce qui est seulement observé

| Surface | Comportement réel |
|---|---|
| Source `grok` | **Observée, en lecture seule.** Lit les fichiers et émet des constats ; ne modifie pas Grok Build et n'appelle pas xAI. |
| `/etc/grok/requirements.toml` | **Applique dans l'agent** les valeurs contraintes du sandbox et de MCP. Olivares vérifie sa présence et l'effet déclaré. |
| `~/.grok/config.toml` | **Préférence observée.** N'est pas à elle seule une politique administrative. |
| `olivares grok-hook` sur `pre_tool_use` | **Peut empêcher l'outil** lorsque la commande s'exécute et se termine avec `2`. Le client refuse en deny-closed lorsque le PEP manque ou échoue. |
| Autres événements Grok | **Observés.** Le refus reste une preuve, mais l'événement n'a pas de veto équivalent. |
| Timeout, crash ou hook qui ne s'exécute jamais | **L'agent échoue en mode ouvert.** Grok continue ; le comportement fail-closed interne d'`olivares grok-hook` ne s'applique que lorsque le processus est invoqué. |
| `~/.grok/disabled-hooks` | **Peut désactiver même un hook administré.** Olivares le détecte ensuite, mais aucune couche d'exigences ne l'empêche. |
| Récepteur OTLP | **Observe des agrégats.** N'authentifie pas, ne conserve aucun contenu et ne remplace pas la chronologie du hook. |

Un déploiement ne doit pas être déclaré « enforced » simplement parce que le sandbox est fixé.
L'achèvement exige des exigences effectives, un hook réellement exécuté, une surveillance continue
de son absence dans `disabled-hooks`, un événement visible et la démonstration d'un veto
`pre_tool_use`.
