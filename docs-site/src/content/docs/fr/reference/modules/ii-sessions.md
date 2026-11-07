---
title: "Module II — opération en direct et sessions"
description: >-
  La surcouche opérationnelle en direct par session d'agent : action en cours,
  tokens/coût en direct, un état Claude Code dérivé et une chronologie rejouable,
  diffusés via server-sent events. Ce qu'il dérive, ce qui reste honnêtement vide,
  et les limites.
---

Le module II est la vue de l'**opération en direct** du parc (estate) : ce que chaque
session d'agent est en train de faire à l'instant présent, ses totaux de tokens et de coût
en direct, un état Claude Code dérivé, et une chronologie reconstructible. Là où le module I
(inventaire) matérialise le parc durable, le module II maintient une **surcouche
opérationnelle en direct** par session au-dessus du même flux d'observations — et ne montre
que ce que ce flux porte honnêtement.

Olivares <!-- release -->0.7<!-- /release --> **lance** aussi les CLI officielles de fournisseur comme enfants
détenus sous un [profil fournisseur](/how-to/operate-provider-sessions/). Ce
chemin géré est le même module. Il ne remplace pas la surcouche et ne fusionne
pas deux homes qui annoncent le même id de session fournisseur
(`CHANGELOG.md` `[26.9.0]` B1/B2).

## Ce qu'il est

Le module II est un module de couche Core piloté par le bus, frère de l'inventaire. Il
maintient un enregistrement en direct indexé par la référence externe de chaque session,
construit à partir du flux d'observations coopératif — jamais interrogé par scrutation,
jamais fabriqué. Par session, il suit :

- l'**action en cours** (le dernier outil utilisé) et la ressource/le mode qu'elle a touchés ;
- les **totaux de tokens et de coût en direct**, lus depuis les échantillons de coût (le
  journal de coût canonique et FinOps sont le module XI, pas ici — ceci n'est que le chiffre
  en direct) ;
- un **état Claude Code dérivé** (`cc_state`) ; et
- une **chronologie** à laquelle chaque événement observé est ajouté dans l'ordre d'ingestion.

## Son contrat et ses entités

Le module enregistre deux entités à portée de tenant. `sessions.live` contient
l'enregistrement en direct par session — action/ressource/mode en cours, référence de
modèle, tokens d'entrée/sortie en direct, coût en direct, compteurs d'événements et d'appels
d'outils, et horodatages de premier/dernier événement. `sessions.timeline` contient une ligne
rejouable par événement, ordonnée par ingestion. Il n'y a **aucune colonne de cycle de vie
stockée** : le flux coopératif ne porte aucun signal de fin-ou-échec, de sorte que le seul
signal de vivacité honnête est le `cc_state` dérivé.

`cc_state` est dérivé **au moment de la lecture** à partir de la récence des événements —
`active` / `idle` / `ended` — et bascule vers un état d'évasion silencieuse lorsque le
connecteur émet ce finding (il n'est jamais écrit par le module lui-même). Les lectures sont
servies sous des routes de module (liste en direct, session unique, chronologie par session)
plus un flux SSE en direct ; chaque lecture exige la permission de lecture de session, et
**l'ouverture du flux est auto-auditée**. Le canal SSE est strictement **isolé par tenant**
(un client ne reçoit que des instantanés pour son tenant autorisé) et **au mieux (best-effort)**
(un client lent abandonne la trame intermédiaire et reçoit la suivante — l'ingestion ne bloque
jamais).

## Provider profiles and `live_ref`

Un **profil fournisseur** est l’identité durable d’une instance fournisseur
configurée sur un environnement d’exécution : pilote, environnement, et les
`config_home` / `user_home` canoniques. Ce n’est pas un compte fournisseur
authentifié. Enregistrer, renommer, désactiver/activer et retirer vivent sous
`/v1/m/sessions/provider-profiles`. Les chemins n’apparaissent que sur la
lecture admin `configuration`. Un lancement nomme `provider_profile_ref` ; le
serveur résout les homes et persiste un instantané non secret sur le run avant
le spawn (`CHANGELOG.md` `[26.9.0]` B1 ; `web/src/features/agentops/types.ts`).

Une observation se plie dans la ligne live de son **canal**, calculé par le
serveur à partir de l’enregistrement de source tamponné par l’hôte
(`CHANGELOG.md` `[26.9.0]` B2) :

| Channel | Meaning |
|---|---|
| `legacy` | no registration |
| `observed` | a source dedicated to a profile by a binding approved at host admission for the exact applied revision |
| `source` | a known registration with no verifiable profile |
| `managed` | a run the plane launched; the only row carrying `canonical_sid` and `run_ref` |

Chaque ligne live expose `live_ref` et `attribution`. Deux homes qui annoncent
le même identifiant de session fournisseur sont deux lignes avec deux
chronologies. Lisez une ligne avec `GET /v1/m/sessions/live/by-id/{live_ref}`
(et sa requête timeline / stream / runs). Les routes d’id externe nues restent
et sont **legacy** : elles répondent seulement pour la ligne legacy.

Les pilotes sont enregistrés sur chaque nœud au démarrage. Au lancement d’une
session, le moteur utilise l’installation gérée vérifiée la plus récente, puis
la CLI dans son `PATH`. `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN`, `_CODEX_BIN`,
`_GROK_BIN` et `_OPENCODE_BIN` remplacent explicitement cette résolution
(voir [Configuration](/reference/configuration/)). Si aucun exécutable n’est trouvé,
le lancement est refusé. Les autres vérifications, dont l’authentification du profil
et la politique de lancement, s’appliquent toujours. Étapes opérateur :
[Exploiter une session de fournisseur](/how-to/operate-provider-sessions/).

Un message peut porter des images. `olivares session send <session> --attach shot.png "what
is wrong here?"` et le composeur de la console (coller, déposer ou le trombone) stockent chaque
image PNG, JPEG, WebP ou GIF avec la session, à raison de quatre au plus par message et de
3,75 Mo chacune, puis les envoient avec le texte ; un message a besoin de son texte, donc une
image seule n'est pas envoyée (la CLI et le composeur le demandent). Claude Code reçoit un
bloc de contenu image et Codex sa propre entrée d'image ; un outil sans entrée d'image refuse
le message en le nommant avant que rien ne soit envoyé. L'API est
`POST /v1/m/sessions/runs/{ref}/attachments` (l'image, en base64),
`GET .../attachments/{id}` et `attachments` sur `.../input`. Les images résident sous le
répertoire de données, par tenant et par session, et sont supprimées avec la session.

## Dossiers de session sur des partages NFS ou SMB

Un dossier de session est un chemin sur l'hôte du moteur : un partage réseau devient donc un dossier une fois que l'hôte l'a monté. `olivares netfolder add` le fait avec les unités de montage systemd elles-mêmes ; le montage reste celui de systemd, et les unités sont des fichiers ordinaires que vous pouvez lire et modifier. Exécutez-le en root sur l'hôte du moteur :

```sh
printf '%s\n' "$SMB_PASSWORD" | sudo olivares netfolder add files //fileserver/projects --user svc --password-stdin
sudo olivares netfolder add data fileserver:/export --mode write_no_delete
olivares netfolder ls
```

La source est `//server/share` pour SMB ou `server:/export` pour NFS, tels que `mount(8)` les écrit. Le nom est en minuscules et chiffres, et désigne le point de montage `/srv/olivares/mnt/<name>`. `--mode` est le mode d'accès du dossier (`rw` par défaut). Les unités désignent le compte `olivares` comme propriétaire de chaque fichier qu'un dossier SMB affiche, donc le moteur doit tourner sous ce compte. `ls` affiche chaque dossier comme `mounted`, `unmounted` (il se monte au premier usage, et `ls` ne le déclenche pas) ou `not responding`. Un dossier existant n'est jamais écrasé.

Pour l'exemple SMB, la commande écrit ces unités, mesurées sur Debian 13 (Linux 6.12) face à un export NFSv4.2 et à un partage Samba 4.22 (SMB 3.1.1). Les noms de fichiers sont ceux qu'affiche `systemd-escape -p /srv/olivares/mnt/files`, auxquels on ajoute `.mount` et `.automount`.

```ini
# /etc/systemd/system/srv-olivares-mnt-files.mount
[Unit]
Description=Olivares network folder files
After=network-online.target
Wants=network-online.target

[Mount]
What=//fileserver/projects
Where=/srv/olivares/mnt/files
Type=cifs
Options=credentials=/etc/olivares/netfolders/files.cred,uid=olivares,gid=olivares,vers=3.1.1,nodev,nosuid,soft,echo_interval=5
TimeoutSec=30
```

```ini
# /etc/systemd/system/srv-olivares-mnt-files.automount
[Unit]
Description=Olivares network folder files (mounted on first use)

[Automount]
Where=/srv/olivares/mnt/files
TimeoutIdleSec=0

[Install]
WantedBy=remote-fs.target
```

Pour NFS, elle écrit `Type=nfs4`, `What=fileserver:/export` et `Options=nodev,nosuid,vers=4.2,soft,timeo=50,retrans=2`, et aucun fichier d'identifiants. Sur un hôte qui applique SELinux avec la politique de l'appliance, elle ajoute `context=system_u:object_r:olivares_workspace_t:s0` aux options, l'étiquette que le moteur et ses outils de session utilisent déjà pour un dossier. Pour retirer un dossier, exécutez `systemctl disable --now` sur ses deux unités, supprimez-les ainsi que son fichier d'identifiants, puis désenregistrez le dossier.

- **Toutes les personnes qui ont le dossier agissent comme un seul compte.** Les fichiers sont lus et écrits sous le compte que le montage présente : `uid=` et `gid=` pour SMB, la correspondance d'utilisateurs de l'export pour NFS. Le partage ne distingue pas les personnes. Olivares décide qui reçoit le dossier.
- **Le dossier propre à une personne lit en tant que cette personne.** `--owner <id utilisateur>` enregistre un dossier SMB comme celui d'une personne, comme un dossier personnel : son contenu n'est qu'à elle, et les administrateurs retrouvent toujours le dossier pour le gérer. Donnez avec `--user` et le mot de passe l'identifiant SMB propre à cette personne et le serveur de fichiers applique ses droits : deux personnes qui montent le même partage voient chacune ce que leur propre compte peut ouvrir. Ajoutez un dossier par personne, chacun avec son nom. NFS ne distingue pas les personnes et n'accepte pas de propriétaire. L'administrateur saisit le mot de passe de la personne une seule fois ; il reste dans le fichier d'identifiants lisible par root seul, comme celui du compte partagé, et la session de la personne ne le détient jamais. Un ticket Kerberos par session, pour que la personne n'ait à confier son mot de passe à personne, n'est pas construit. Vérifié avec deux comptes d'un domaine Samba AD sur un même partage : le dossier de chaque personne listait son propre répertoire et le répertoire commun, lisait son propre fichier, et le serveur de fichiers refusait le fichier de l'autre personne.
- **L'identifiant reste dans un fichier.** La commande écrit `username=` et `password=` dans le fichier `credentials=`, propriété de root avec le mode 0600, à partir du mot de passe lu sur l'entrée standard. Le mot de passe n'apparaît dans aucune unité, option de montage, argument de processus ni ligne du journal : `mount.cifs` ne reçoit que le chemin. NFS avec `sec=sys` n'a besoin d'aucun identifiant.
- **L'unité automount rend le démarrage indépendant du serveur de fichiers.** Rien n'attend le partage au démarrage. Le premier usage le monte, et un serveur indisponible à ce moment fait échouer cet usage, pas le démarrage.
- **Utilisez `soft`.** Un montage NFS `hard`, la valeur par défaut, laisse un processus qui touche un serveur mort bloqué sans pouvoir être arrêté. Avec les options ci-dessus, une lecture ou une écriture sur un partage monté échoue avec une erreur d'entrée/sortie ou « Host is down » après 20 à 30 secondes, et le partage refonctionne dès que le serveur répond.
- **Un dossier qui ne répond pas est signalé.** L'enregistrement du dossier, son ouverture, sa liste et le démarrage d'une session dedans attendent chacun 10 secondes le partage, puis refusent avec "The folder ... is not responding. If it is on a network share, check that its file server is up and reachable." et un statut 424. La session ne démarre pas.
- **`write_no_delete` tient.** Landlock l'applique dans la couche système de fichiers du noyau, au-dessus du système de fichiers réseau. Lors d'un lancement réel de session sur les deux partages, une session a créé et modifié des fichiers, et s'est vu refuser renommage, suppression et rmdir, exactement comme sur un disque local, tandis que `rw` les autorisait. Un hôte qui ne peut pas confiner refuse toujours le lancement.
- **Le dossier est la frontière.** Lors des mêmes lancements, dans les deux modes, une écriture dans le parent du dossier, qui se trouve sur le même partage, a été refusée et n'a laissé aucun fichier. La session n'écrit qu'à l'intérieur de son dossier.

## Ce qu'il consomme (et ce qu'il dérive)

Le module II consomme le même flux d'observations à donnée minimale que l'inventaire —
[`edge.observed`](/fr/reference/events/), `cost.sampled` et `finding.reported`. Seules les arêtes
dont l'origine est une **session** produisent de l'opération en direct ; les échantillons de
coût liés à une session s'ajoutent au chiffre de tokens/coût en direct (aucun `CostRecord`
n'est écrit ici) ; les findings dont le sujet est une session sont annotés, et un finding
anti-évasion marque l'état d'évasion. Deux champs sont **dérivés en direct** à partir de ces
mêmes signaux : `agent_ref` à partir de l'agent attribué à une session, et `summary` à partir
d'un finding de compaction de contexte (forensique) dont le titre est sûr pour le résumé par
contrat — jamais un résumé fabriqué par un LLM.

:::caution[Limites honnêtes]

- **`goal` reste vide — honnêtement.** Le flux coopératif est à donnée minimale et ne porte
  **pas** l'objectif ou la liste de tâches d'une session ; ils sont expurgés au connecteur et
  aucun texte de prompt en cours de processus n'est sur le fil. L'enregistrement en direct
  modélise le champ pour que le contrat et l'UI soient prêts et qu'un futur canal de métadonnées
  puisse le peupler, mais le module **ne l'invente jamais**.
- **Aucun cycle de vie stocké.** Le flux n'a aucun signal de fin/échec, donc la vivacité d'une
  session est le `cc_state` **dérivé** par récence — non un statut persisté. Un état `ended`
  signifie *aucun événement récent*, et non un arrêt propre confirmé.
- **Le chiffre en direct n'est pas le journal.** Les tokens/coût en direct sont une lecture
  opérationnelle issue des échantillons de coût ; l'enregistrement de coût faisant autorité et
  réconciliable est le journal FinOps du module XI. Ne traitez pas le chiffre en direct comme la
  vérité de facturation.
- **La donnée minimale est une propriété du fil.** Seuls des références, des classifications et
  des compteurs de vivacité/coût sont portés et persistés — jamais de charges utiles, de prompts,
  de commandes ou de PII.
:::

## En lien

- [Référence du bus d'événements](/fr/reference/events/) — les événements `edge.observed`,
  `cost.sampled` et `finding.reported` que ce module consomme.
- [Catalogue des modules](/fr/reference/modules/overview/) — où se situe le module II et la
  répartition honnête de l'actionnement.
- [Carte d'accès et de ressources](/fr/reference/modules/iii-access-map/) — le module Core frère
  qui possède le graphe d'accès R/RW.
- [Vue d'ensemble de l'architecture](/fr/explanation/architecture/overview/) — le moteur et les
  couches.
- [Connecter Claude Code](/fr/how-to/connect-claude-code/) — commencer à produire le flux en direct.
- [Honnêteté et limites](/fr/start/honesty-and-limits/) — ce que le produit fait et ne fait pas
  aujourd'hui.
