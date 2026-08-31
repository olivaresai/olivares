---
title: "Posture de TAK Server et ingestion gouvernée de Cursor-on-Target"
description: >-
  Gouvernez un déploiement TAK : lisez hors ligne la posture de TAK Server dans
  CoreConfig.xml (avec une sonde live facultative de la version) et ingérez les
  événements Cursor-on-Target via UDP/TCP comme signaux gouvernés minimisant les
  données — les coordonnées et les détails ne quittent jamais le connecteur, et
  chaque arête est fidèlement qualifiée d'approximative.
sidebar:
  order: 9
---

La source `tak` gouverne un déploiement **TAK** (Team Awareness Kit) comme une surface
supplémentaire. Elle remplit deux fonctions distinctes, que vous pouvez activer
indépendamment :

- **Posture de TAK Server** — représente la configuration d'un serveur (ses entrées,
  leurs protocoles/ports, les paramètres TLS/keystore et le backend de signature des
  certificats) sous forme de findings à données minimales. La source **fondée** est le
  propre fichier `CoreConfig.xml` du serveur, lu **hors ligne** depuis le disque ; seule
  une **sonde de version** live facultative lit des données sur le réseau. Elle ne lit
  **pas** la fédération TAK.
- **Ingestion CoT gouvernée** — reçoit des événements **Cursor-on-Target** sur les
  listeners **UDP** et **TCP** propres au connecteur et transforme chacun d'eux en arête
  d'accès gouvernée.

Le connecteur privilégie la **lecture** : il n'écrit jamais dans TAK Server, ne rejoint
jamais une fédération et ne réémet jamais de payload. Si aucun credential ni aucun
listener ne sont configurés, il ne fait **rien**, en toute transparence : il n'émet
aucune donnée au lieu de fabriquer la posture d'un déploiement qu'il n'a jamais contacté.

## Données émises

| Champ | Valeur |
|---|---|
| Source du signal | `cot` |
| Mode | `write` — un émetteur CoT *contribue* à l'état de connaissance de la situation du flux |
| Origine | le `uid` de l'émetteur, **haché par défaut** (`cot_uid_mode`) |
| Confiance | toujours **`approximate`** — le CoT de base n'est pas authentifié (voir ci-dessous) |
| Findings | annulations drop-track, événements à erreur non bornée et rejets agrégés des listeners (limite de débit / taille excessive / malformé / limite de connexions) |

## 1. Posture : lire d'abord le serveur hors ligne

La source fondée de la posture est le propre fichier de configuration du serveur. Dans
une installation par paquet, il se trouve à l'emplacement `/opt/tak/CoreConfig.xml`.
Pointez le connecteur vers ce fichier : il lit les entrées configurées, les paramètres
TLS/keystore et le backend de signature des certificats **sans utiliser le réseau**.
L'élément `<federation>` n'est volontairement pas modélisé, donc aucune posture de
fédération n'est produite.

La **sonde de version** live est facultative et n'ajoute que la version en cours
d'exécution. TAK Server authentifiant les opérateurs par **mTLS**, la sonde refuse par
défaut : si vous définissez `server_url` avec `posture` activé, mais **omettez** le
certificat client, le connecteur **refuse de démarrer** au lieu d'effectuer une sonde
anonyme et de déclarer une posture qu'il n'a pas authentifiée. `server_url` doit utiliser
`https`.

```jsonc
// OLIVARES_SOURCES_CONFIG — posture only
{
  "sources": [{
    "name": "tak-server",
    "kind": "tak",
    "tenant": "<tenant-id>",
    "config": {
      "core_config_path": "/opt/tak/CoreConfig.xml",
      "server_url": "https://takserver.example.mil:8443",
      "client_cert": "${TAK_CLIENT_CERT_PEM}",
      "client_key":  "${TAK_CLIENT_KEY_PEM}"
    }
  }]
}
```

## 2. Ingestion : recevoir du CoT via UDP et TCP

Activez un listener et le connecteur recevra du CoT : un message par datagramme **UDP**
et un message par connexion **TCP** (« open-squirt-close »). Vous dirigez un flux TAK ou
des clients CoT vers l'adresse d'écoute du connecteur ; celui-ci est le consommateur, il
n'établit pas de connexion au serveur pour extraire les données.

```jsonc
// OLIVARES_SOURCES_CONFIG — ingest
{
  "sources": [{
    "name": "tak-edge",
    "kind": "tak",
    "tenant": "<tenant-id>",
    "config": {
      "cot_udp_listen": "0.0.0.0:6969",
      "cot_multicast_group": "239.2.3.1",
      "cot_tcp_listen": "0.0.0.0:8087",
      "allow_public_bind": true,
      "feed_ref": "tak"
    }
  }]
}
```

### Clés de configuration (issues du descripteur fourni avec le connecteur)

| Clé | Type | Valeur par défaut | Secret | Signification |
|---|---|---|:--:|---|
| `core_config_path` | string | — | non | Chemin vers `CoreConfig.xml` (installation par paquet : `/opt/tak/CoreConfig.xml`) — la source fondée et hors ligne de la posture |
| `server_url` | string | — | non | URL de base de TAK Server (p. ex. `https://takserver.example.mil:8443`). Facultative : active uniquement une sonde live de la version |
| `version_path` | string | `/Marti/api/version` | non | Endpoint de version Marti sur `server_url`. Configurable, car la référence d'API de tak.gov nécessite un compte |
| `client_cert` | string | — | **oui** | Certificat client PEM pour le mTLS de TAK Server, par référence |
| `client_key` | string | — | **oui** | Clé privée PEM du certificat client, par référence |
| `ca_cert` | string | — | non | Bundle d'AC PEM du certificat de TAK Server. Une valeur vide utilise le magasin de confiance de l'hôte |
| `posture` | bool | `true` | non | Émettre des findings de posture de TAK Server |
| `request_timeout` | duration | `15s` | non | Timeout de chaque requête vers l'API de TAK Server |
| `feed_ref` | string | `tak` | non | Référence stable de ce flux CoT — le `source_ref` délimité par une liaison sourcescope (`source_type=data`) |
| `cot_udp_listen` | string | — | non | Adresse d'écoute UDP du CoT (p. ex. `127.0.0.1:6969`). Une valeur vide désactive l'ingestion UDP |
| `cot_tcp_listen` | string | — | non | Adresse d'écoute TCP du CoT open-squirt-close (p. ex. `127.0.0.1:8087`). Une valeur vide désactive l'ingestion TCP |
| `cot_multicast_group` | string | — | non | Groupe multicast facultatif à rejoindre sur le listener UDP (la valeur SA par défaut de TAK est `239.2.3.1`) |
| `cot_max_event_bytes` | int | `65536` | non | Nombre maximal d'octets pour un événement CoT |
| `cot_max_detail_bytes` | int | `32768` | non | Nombre maximal d'octets pour la partie opaque `<detail>` d'un événement CoT |
| `cot_rate_limit_eps` | int | `500` | non | Nombre maximal d'événements CoT acceptés par seconde sur l'ensemble des listeners ; les événements excédentaires sont abandonnés et comptés |
| `cot_max_tcp_conns` | int | `128` | non | Nombre maximal de connexions TCP CoT simultanées |
| `cot_uid_mode` | string | `hash` | non | Manière dont un `uid` quitte le connecteur : `hash` (par défaut, à sens unique) ou `raw`. Un uid identifie un appareil, et un appareil identifie la personne qui le porte |

## Ports (TAK Server Configuration Guide v5.2)

Ces conventions donnent le contexte de l'intégration. Les listeners du connecteur se
lient à tout `host:port` que vous configurez ; les exemples ne réutilisent ces numéros
que pour faciliter la compréhension.

| Port / groupe | Convention |
|---|---|
| **8089** | Entrée de streaming CoT TLS — canal client↔serveur authentifié |
| **6969** + multicast **239.2.3.1** | Groupe multicast de connaissance de la situation (SA) |
| **8087** | Port d'entrée conventionnel ; l'exemple canonique du guide le lie en **UDP**. Le protocole est configurable : 8087 n'est **pas** intrinsèquement TCP |
| **8088** | `stcp` — entrée TCP non chiffrée, **réservée aux tests** |
| **8443** | Interface web d'administration |
| **8446** | Enrôlement des certificats |

## Confidentialité : les coordonnées et les détails ne quittent jamais le connecteur

Le CoT est un protocole de suivi de position — le signal le plus dense en PII que ce
produit ingère —, de sorte que la minimisation des données est appliquée strictement :

- Les valeurs `lat` / `lon` / `hae` du `<point>` ne **quittent jamais le connecteur**.
  Une coordonnée est la position d'une personne ; le produit enregistre la réception
  d'un événement, son émetteur et son type CoT, mais jamais l'endroit où se trouve
  quelqu'un.
- La partie opaque `<detail>` ne quitte jamais le connecteur ; seules sa **taille** et
  son **empreinte SHA-256** sont conservées, afin de corréler les payloads identiques
  sans les stocker.
- Le `uid` de l'émetteur est **haché par défaut** (`cot_uid_mode=hash`, avec séparation
  de domaine et à sens unique). `raw` nécessite un choix explicite de l'opérateur.

## Confiance : un uid CoT n'est pas une identité authentifiée

Le CoT de base ne comporte **aucune authentification** : tout hôte pouvant atteindre un
listener peut déclarer n'importe quel `uid`. Le TLS de TAK Server protège le canal
client↔**serveur** (port 8089) ; il ne dit rien d'un événement que ce connecteur reçoit
sur son propre listener UDP/TCP en clair. Ainsi, **toute** arête provenant d'un listener
CoT de base est classée **`approximate`**, par conception : aucun chemin de code ne
renvoie `attributed`.

:::caution[Un `uid` est une déclaration, pas une preuve]
Interprétez un `uid` CoT comme *« un émetteur déclarant cet identifiant a publié dans le
flux »*, et non comme une identité authentifiée. Il ne deviendrait authentifié que si un
listener terminait mTLS et liait l'uid au certificat du pair.
:::

## Périmètre : gouverner le flux avec une liaison sourcescope

Le flux est une source gouvernée de premier ordre. Une liaison **sourcescope** délimite
qui peut l'utiliser, avec `source_type=data` et `source_ref=<feed_ref>`, sur n'importe
quel axe de sujet — **session / agent / user / user_group / role**. Les effets sont
`allow` (par défaut) ou `forbid`, et **`forbid` est absolu** (`forbid` l'emporte sur
`allow`).

```http
POST /v1/m/sourcescope/bindings
Content-Type: application/json

{
  "source_type": "data",
  "source_ref":  "tak",
  "scope_tree":  "agent",
  "scope_ref":   "agent:recon-planner",
  "effect":      "allow",
  "enabled":     true
}
```

Définissez `"effect": "forbid"` (avec, par exemple, `"scope_tree": "user_group"`) pour
retirer l'accès d'un groupe entier, même lorsqu'une autorisation `allow` existe.

## Licence et provenance clean-room

Le format réseau CoT est une implémentation **clean-room** écrite exclusivement à partir
de la **spécification MITRE publiée** : aucun code source TAK ou ATAK n'a été lu, copié
ou utilisé comme dérivé :

- *The Developer's Guide to Cursor on Target*, Butler, MITRE, août 2005 — DTIC
  **ADA637348**, dossier MITRE **n° 06-0249**.
- `Event-PUBLIC.xsd`, le schéma de l'événement CoT de base (version 2.0) — dossier MITRE
  **n° 11-3895**.
- *TAK Server Configuration Guide* **v5.2** — pour les conventions de ports/protocoles.

ATAK-CIV et TAK Server sont sous **GPLv3** et interdits au connecteur (Apache-2.0), ce
que fait respecter le contrôle de frontière des licences. Tous deux portent la mention
fédérale américaine **« Distribution A »**, qui est une **déclaration de diffusion
gouvernementale, et non une licence logicielle** : les arborescences de code restent
sous GPLv3. Le schéma et le guide publiés par MITRE sont ce qui légitime une
implémentation clean-room.

## Limites déclarées

- **Aucun support mesh/radio** — UDP et TCP uniquement ; pas de liaison série, de mesh
  TAK ou de radio.
- **Aucun plugin ATAK/WinTAK** — le connecteur n'implémente aucun client TAK destiné à
  l'utilisateur final.
- **Aucune fédération TAK** — il ne fait qu'*observer* que la fédération est configurée ;
  il ne se fédère jamais.
- **Aucun Link-16 / MIL-STD** ni protocole tactique soumis à certification, et **aucune
  accréditation Iron Bank / DoD** — il s'agit de parcours client distincts et
  facultatifs.
- **Le sous-schéma CoT `<detail>` n'est pas modélisé** — seul l'événement de base est
  analysé ; le détail consiste en octets opaques, limités en taille et condensés.
- **La perte UDP est impossible à compter** — la backpressure ralentit les listeners ;
  en UDP, le **noyau** abandonne des datagrammes avant que ce processus ne les voie, et
  ces pertes ne peuvent pas être comptées. Seuls les événements effectivement refusés
  par le connecteur sont agrégés dans les findings de rejet.

## Pages connexes

- [Connecter une source](/fr/how-to/connect-a-source/) — le modèle de connecteur et la
  taxonomie fidèle des niveaux.
- [Gouverner et approuver](/fr/how-to/govern-and-approve/) — le modèle d'autorisation
  auquel se raccorde une liaison sourcescope.
- [Connecteurs et niveaux de couverture](/fr/reference/connectors/) — le catalogue
  complet.
