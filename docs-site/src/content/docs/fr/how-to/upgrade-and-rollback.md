---
title: Mettre à niveau et revenir en arrière
description: >-
  Comment faire passer un déploiement Olivares AI auto-hébergé à une version plus
  récente : prévisualisez le plan, effectuez le remplacement, vérifiez-le et revenez
  en arrière si nécessaire. Couvre la commande libre-service `olivares upgrade`, les
  bundles air-gap et le remplacement d'image de la plateforme.
---

Une mise à niveau remplace le binaire ; elle ne vous migre pas vers un autre produit. Le
répertoire de données, la clé de signature d'audit et le matériel TLS restent en place, et le
moteur applique lui-même les nouvelles migrations de schéma au démarrage. Cette page guide
l'opérateur de « dois-je installer cette version ? » à « je dois récupérer la précédente ».

:::caution[Sauvegardez d'abord]
Avant chaque mise à niveau, créez une sauvegarde DR avec le `dr backup` de la version installée.
L'écran **Backups** (`/backups`) et [Sauvegarder et restaurer](/fr/how-to/backup-and-restore/)
décrivent la procédure. **Après une avancée du schéma, la sauvegarde antérieure à la mise à
niveau et sa phrase secrète privée ou sa KEK sont nécessaires pour revenir à la version précédente.**
`olivares upgrade` conserve l'exécutable ; il ne sauvegarde pas la base de données.
:::

## Quelle voie de mise à niveau vous correspond

Il existe deux manières de faire avancer le binaire, et elles aboutissent au même résultat.

| Votre installation | Voie |
|---|---|
| Un binaire sur un hôte, systemd ou Docker Compose | `olivares upgrade` — cette page |
| Kubernetes / Helm | Définissez l'image et laissez l'opérateur effectuer le rolling update. N'exécutez pas `olivares upgrade` dans un pod : le déploiement est déclaratif et la prochaine réconciliation annulerait le changement. |

## Avant toute chose : lisez le plan

`--check` télécharge et vérifie le manifeste du canal, le compare à ce qui est installé et
affiche ce qui se produirait. Il ne remplace rien.

```sh
olivares upgrade --check
```

La commande répond avec la version installée, celle disponible et une ligne d'état parmi
`up to date`, `upgrade available`, `DOWNGRADE (blocked unless --force-rollback)` ou
`UNKNOWN`. Lisez la ligne d'état plutôt que de comparer vous-même les deux numéros de version.

**`UNKNOWN` ne signifie pas « probablement bon ».** Cela signifie que la version installée
n'a pas pu être mesurée — répertoire de staging d'une autre architecture, montage `noexec`,
build à partir des sources — et que la protection anti-retour comme le seuil de version
minimale portent *sur* cette version installée ; aucune ne peut donc être évaluée. La commande
refuse de deviner. Déclarez la version que vous savez installée et les protections resteront
actives :

<!-- release -->
```sh
olivares upgrade --check --current-version 0.1
```
<!-- /release -->

## Canaux de publication

<!-- BEGIN GENERATED olivares-upgrade-channels — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

`olivares upgrade` suit un **canal** de publication. Il y en a **3**, déclarés dans
`core/release/manifest.go` par ordre de stabilité croissante :

| Valeur de `--channel` | Déclarée comme |
|---|---|
| `stable` | `release.ChannelStable` |
| `security` | `release.ChannelSecurity` |
| `lts` | `release.ChannelLTS` |

Toute valeur absente de ce tableau est rejetée avant le moindre téléchargement
(`release.ValidChannel`).

<!-- END GENERATED olivares-upgrade-channels -->

`stable` est la ligne de disponibilité générale et la valeur par défaut. `security` ne
contient que des correctifs hors bande ; un déploiement qui la suit reçoit donc les versions
de sécurité sans recevoir les versions fonctionnelles.

:::caution[`lts` est valide, mais rien ne le publie]
Le tableau ci-dessus est généré depuis les constantes de canal déclarées par le code ; il
répertorie donc toutes les valeurs acceptées par `--channel`, dont `lts`. **Aucun manifeste
`lts` n'est produit ou publié** : un déploiement qui le suit demande à l'hôte de mise à jour
un objet qui n'existe pas. Le support de sécurité est limité à la durée du contrat, sans
backports généraux, et aucune ligne n'est figée : les droits durent pendant la période payée,
sans fallback acquis ni droit perpétuel. Choisissez `stable` ou `security`.
:::

Choisissez le canal correspondant à votre mode d'exploitation et conservez-le :

```sh
olivares upgrade --channel security
```

Une version de sécurité est signalée comme telle dans le manifeste et `--check` affiche les
avis qu'elle corrige. Si vous utilisez le canal de sécurité, vous recevez ces correctifs hors
bande par rapport à la ligne de disponibilité générale.

## Effectuer la mise à niveau

```sh
olivares upgrade
```

Voici ce que fait la commande, dans l'ordre, et la raison de chaque étape :

1. **Elle télécharge le manifeste du canal et vérifie sa signature hors ligne** avec la clé de
   publication Ed25519 intégrée au build. L'ancre de confiance est la signature, pas le
   transport. Un build sans clé intégrée exige que vous en fournissiez une avec `--pubkey` ;
   aucune voie non vérifiée n'existe.
2. **Elle refuse de revenir en arrière.** L'installation d'une version antérieure à celle en
   cours est bloquée sauf si vous passez `--force-rollback`, ce qui inscrit une entrée d'audit.
3. **Elle lie l'artefact au SHA-256 signé du manifeste** avant que les octets ne soient exécutés.
4. **Elle sonde le candidat**, puis le remplace atomiquement en conservant une sauvegarde
   horodatée du binaire remplacé. Si le nouveau binaire ne démarre pas, la commande rétablit
   elle-même cette sauvegarde.
5. **Elle ne touche pas au processus en cours.** Le remplacement modifie le fichier sur disque.
   Le nouveau code prend le relais au redémarrage du service.

Ajoutez `--yes` lorsque vous pilotez la commande depuis un script et que personne ne peut
répondre à la demande de confirmation.

Le retour automatique de l’étape 4 teste `version`, pas le démarrage du service ni la
compatibilité de la base. Il ne récupère pas un store migré au redémarrage suivant.

:::note[Il n'y a pas de correctif à chaud]
Un binaire Go ne se corrige pas en place. Ici, « zéro interruption » signifie un drainage et
un relais gracieux, ou un rolling restart — jamais un correctif dans le processus. Ce qui
s'applique à chaud, sans redémarrage, ce sont les données et la configuration : sources,
connectors, secrets, policy et licence.
:::

## Installations air-gap

Un déploiement air-gap ne contacte jamais un hôte de mise à jour. Transférez le bundle par le
moyen auquel vous faites déjà confiance, puis installez-le depuis le fichier local : la
vérification est identique, car ce n'est jamais le réseau qui inspirait confiance.

L’installation hors ligne nécessite Enterprise. Community vérifie un bundle avec `--bundle --check`, sans lire de licence ni l’installer.

```sh
olivares upgrade --bundle ./olivares-release.tar.gz --check
```


## Déploiement progressif et vérifications sans surveillance

Un manifeste peut nommer une cohorte de déploiement progressif afin qu'une version atteigne
d'abord une fraction de l'estate. `--if-eligible` fait agir un nœud seulement s'il appartient
à cette cohorte ; sinon, il ne fait rien :

```sh
olivares upgrade --if-eligible --yes
```

C'est la forme qu'exécute le timer intégré. Pour émettre un timer et un service systemd qui
l'appellent pendant une fenêtre de maintenance :

```sh
olivares upgrade --install-timer --timer-schedule 'Sun *-*-* 03:00:00'
```

La commande affiche les unités par défaut ; `--timer-dir` les écrit à l'emplacement indiqué.
C'est opt-in : rien ne se planifie tout seul.

La console présente la moitié en lecture seule des mêmes informations : **Settings → update
status** appelle `POST /v1/console/update-check`, qui vérifie à la demande le canal configuré.
Un déploiement air-gap ou sans canal configuré répond `501` en indiquant pourquoi, au lieu
d'annoncer qu'aucune mise à jour n'existe.

## Vérifier la mise à niveau

```sh
olivares version
olivares upgrade --check
```

`--check` devrait maintenant indiquer `up to date`. Vérifiez ensuite que le service lui-même
est sain : l'écran **Health** de la console (`/health`) ou l'endpoint de readiness du moteur
décrit dans [Superviser avec Prometheus](/fr/how-to/monitor-with-prometheus/).

## Revenir en arrière

L'exécutable précédent est conservé à côté du nouveau, et la commande affiche son chemin.
Cette copie n'est pas un point de récupération des données.

**Un ancien binaire refuse une version du schéma core supérieure à celle qu'il prend en charge**,
même après des migrations additives. Réinstaller le binaire ou l'image précédente n'annule pas
l'avancée du schéma. Ne modifiez pas l'historique des migrations et ne contournez pas ce refus.

1. Arrêtez tous les moteurs utilisant le store. Conservez les données mises à niveau, la configuration
   du service, le matériel TLS et les clés de scellement externes.
2. Avec le **binaire de la version précédente**, restaurez le bundle DR pris **avant** la mise à
   niveau : [Sauvegarder et restaurer](/fr/how-to/backup-and-restore/). SQLite : répertoire neuf,
   ou `dr restore --in-place` avec `--operator` et `--reason` ; conservez les fichiers préservés
   jusqu'à confirmation de la récupération. PostgreSQL : cible vide avec `olivares db init`, ses
   `--dsn`, `--owner-dsn` et `--admin-dsn`, et un répertoire neuf pour la clé de signature restaurée.
3. Exigez une vérification réussie du ledger et de la clé d'audit. Faites pointer le répertoire de données,
   les volumes et les DSN PostgreSQL du service vers le store restauré et les clés de signature
   correspondantes avant de démarrer la version précédente.
4. Connectez-vous et vérifiez les données récupérées et la santé du service.

La récupération revient au point sauvegardé. Les écritures ultérieures sont absentes du store
restauré ; conservez le store mis à niveau pour les réconcilier. Sans ce bundle et sa phrase
secrète ou sa KEK, remplacer l'exécutable ne permet pas cette récupération.

`--force-rollback` autorise l'installation d'un ancien exécutable et inscrit le contournement
dans l'audit log. Il ne contourne ni la vérification du schéma core ni le seuil de version minimale
et ne restaure aucune donnée. En dessous du seuil, passez par une version intermédiaire.

### Tester la récupération avant la mise à niveau en production

Démarrez la version précédente vérifiée avec un répertoire SQLite temporaire, terminez le setup,
connectez-vous et arrêtez-la. Créez et vérifiez un bundle avec ses `dr backup` et `dr verify`.
Démarrez le candidat sur le même store, connectez-vous et arrêtez-le. Si le schéma a dépassé
le plafond de l'ancienne version, celle-ci doit échouer avec `core schema version newer than this binary supports`.
Restaurez le bundle dans un répertoire neuf avec l'ancien `dr restore`. Exigez un code de sortie
zéro et une vérification réussie du ledger ; démarrez-y l'ancienne version et vérifiez la connexion,
la clé publique d'audit d'origine et les données sauvegardées. Un échec de restauration ou de
connexion signifie que le test de récupération a échoué.

Vérification SQLite mesurée (2026-10-08) : la version officielle 26.10.1<!-- release-fixed --> a créé le schéma core 18,
un candidat plus récent l’a avancé à 27. L’ancien binaire a refusé le store avec le code 1
(`database=27 binary=18`). Ses `dr backup`, `dr verify` et `dr restore` ont terminé avec le code 0.
Après restauration du bundle antérieur dans un répertoire neuf, le compte d’origine et
la clé publique d’audit d’origine ont été retrouvés.

## En cas de problème

| Symptôme | Signification | Action |
|---|---|---|
| `--check` affiche `UNKNOWN` | La version installée n'a pas pu être mesurée ; aucun ordre ne peut donc être affirmé | Passez à `--current-version` la version que vous savez installée |
| `min_ver` indique que votre version est trop ancienne | La version refuse de s'installer directement par-dessus la vôtre | Mettez d'abord à niveau vers la version intermédiaire indiquée |
| L’exécutable installé échoue au sondage `version` après remplacement | La vérification de l’exécutable a échoué | La commande restaure l’exécutable conservé ; consultez les logs |
| Le service échoue au redémarrage ou l’ancien binaire détecte un schéma core trop récent | Cela dépasse le sondage de l’exécutable | Arrêtez le service et restaurez le bundle DR antérieur selon Revenir en arrière |
| `--install-timer` se déclenche mais rien ne se produit | Le nœud ne fait pas partie de la cohorte de déploiement progressif | Comportement attendu avec `--if-eligible` ; la cohorte s'élargit à mesure que le déploiement avance |
| "another olivares upgrade is already installing", exit **5** | Une seule mise à niveau à la fois par binaire. Le verrou est détenu pendant toute la séquence de téléchargement et de remplacement | Attendez celle en cours et relancez. Si rien ne tourne, le noyau a déjà libéré le verrou : relancez maintenant |
| "it CHANGED while this upgrade was downloading" | Quelque chose a remplacé le binaire après la préparation du plan : gestionnaire de paquets, déploiement d'image ou exécution de gestion de configuration | Relancez : les protections sont réévaluées face à ce qui est réellement installé. Si cela persiste, deux systèmes gèrent le même binaire |

**Un seul agent de mise à niveau par binaire.** `olivares upgrade` prend un verrou exclusif sur
la cible pendant toute la séquence préparation-téléchargement-remplacement ; une seconde
exécution se termine donc avec le code `5` au lieu d'installer. Installez **un** timer et
modifiez son `--channel` plutôt que d'exécuter un timer par canal : auparavant, deux
installations terminant dans la même seconde écrasaient mutuellement leur sauvegarde de
retour, puis le retour automatique de la perdante restaurait *l'autre* binaire tout en
annonçant un succès. Juste avant le remplacement, la commande relit également les octets de
la cible et refuse de continuer s'ils diffèrent de ceux sur lesquels le plan a été établi,
car les verdicts anti-retour et de version minimale portent sur un fichier installé précis.

Pour tout autre problème, [Résolution des problèmes](/fr/how-to/troubleshooting/) est la voie
générale, et l'écran **Logs** de la console (`/logs`) diffuse le log du moteur.
