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
Effectuez une sauvegarde avant chaque mise à niveau, y compris celles qui semblent ordinaires.
L'écran **Backups** de la console (`/backups`) et [Sauvegarder et
restaurer](/fr/how-to/backup-and-restore/) permettent tous deux de le faire. Rien dans cette
page ne dépend de l'existence d'une sauvegarde, mais vous serez heureux de l'avoir le jour où
quelque chose vous surprendra.
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

```sh
olivares upgrade --check --current-version 26.8.0
```

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

**Installer depuis un bundle exige une licence active sur la machine.** Elle est vérifiée hors
ligne avec la clé de licence intégrée à votre binaire : aucun appel n'est effectué, ce qui
fonctionne derrière l'air gap. Si vous n'avez pas encore installé votre licence sur la machine,
la page [Installer une licence et passer à Enterprise](/fr/how-to/install-a-license/) explique
comment procéder.
`--check` n'est pas soumis à cette condition ; vous pouvez donc
vérifier un bundle avant de préparer quoi que ce soit :

```sh
olivares upgrade --bundle ./olivares-release.tar.gz --check   # verify only; no license read
olivares upgrade --bundle ./olivares-release.tar.gz --yes     # install; needs a live license
```

Si votre build ne contient aucune clé de publication intégrée, ou si vous répliquez les
versions sous votre propre clé de signature, indiquez à la commande la clé de vérification :

```sh
olivares upgrade --bundle ./olivares-release.tar.gz --pubkey @/etc/olivares/release.pub
```

Consultez [Installer en air-gap](/fr/how-to/air-gap-install/) pour savoir comment le bundle
est produit et transféré.

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

Le binaire précédent est conservé à côté de celui qui l'a remplacé, et la commande affiche
son chemin lors du remplacement. Revenir en arrière consiste à restaurer ce fichier puis à
redémarrer le service.

Le retour est sûr par conception, non par chance : chaque changement de schéma est d'abord
livré sous forme d'expansion additive, et son contrat destructif seulement dans une version
ultérieure. Le binaire de la version précédente continue donc de fonctionner avec le schéma
mis à niveau. C'est pourquoi revenir en arrière signifie « remettre l'ancien binaire », et non
« inverser la base de données ».

Si vous devez installer une version antérieure plutôt que restaurer la sauvegarde conservée,
la protection anti-retour la bloque jusqu'à votre confirmation explicite :

```sh
olivares upgrade --force-rollback --yes
```

Le contournement est inscrit dans l'audit log. Le seuil de version minimale **ne peut pas**
être contourné ainsi : si un manifeste déclare un minimum supérieur à votre version installée,
passez par une version intermédiaire au lieu d'essayer de sauter cette étape.

## En cas de problème

| Symptôme | Signification | Action |
|---|---|---|
| `--check` affiche `UNKNOWN` | La version installée n'a pas pu être mesurée ; aucun ordre ne peut donc être affirmé | Passez à `--current-version` la version que vous savez installée |
| `min_ver` indique que votre version est trop ancienne | La version refuse de s'installer directement par-dessus la vôtre | Mettez d'abord à niveau vers la version intermédiaire indiquée |
| Le nouveau binaire ne démarre pas | Le sondage après remplacement a échoué | La sauvegarde a déjà été rétablie ; consultez les logs et signalez la version |
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
