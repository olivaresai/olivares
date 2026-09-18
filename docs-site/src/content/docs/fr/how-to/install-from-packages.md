---
title: Installer depuis un paquet
description: >-
  Installez Olivares AI depuis le .deb, .rpm ou .apk sur un hôte Linux durci :
  vérifiez la release avant de lui faire confiance, exécutez-la sous l'unité
  systemd empaquetée ou, sur Alpine par défaut, au premier plan, branchez votre
  première source et mettez à jour — en ligne, avec version épinglée, ou
  totalement hors réseau.
draft: false
---

:::note[Noms de paquets publiés]
La release GitHub v26.9.1 publie des artefacts `.deb`, `.rpm` et `.apk` pour `amd64`
et `arm64`, avec `checksums.txt`, `checksums.txt.sig` et `checksums.txt.pem`. Les
commandes ci-dessous utilisent les noms littéraux `amd64` de cette release ;
remplacez `amd64` par `arm64` sur un hôte ARM 64 bits. Installez depuis ces artefacts
de release vérifiés. Les producteurs de métadonnées de dépôt dans un arbre source ne
sont pas des instructions d'installation pour ce guide.

**Qualification DIST-24-05.** Ce que la CI qualifie est l'**installateur shell** vérifié
et son contrat service/doctor, pas `dpkg`, `rpm` ni `apk` : une matrice dispatch/pull
request l'exécute contre la release publiée dans des userlands de conteneur
Debian stable, Ubuntu 24.04 LTS, Fedora, openSUSE Leap et Alpine et sur un runner hébergé
macOS 14, et elle échoue comme non mesurable quand la release publique est injoignable,
au lieu de compter un dry-run comme couverture. Cette matrice ne certifie pas
l'installation native par gestionnaire de paquets. Le cycle de vie natif des paquets
construits depuis cet arbre source — installation, démarrage, redémarrage, mise à niveau
d'un service OpenRC en cours et retrait — a été exercé localement dans un invité Alpine
jetable ; c'est une preuve pour cet arbre, pas une qualification signée, hébergée ni de
préproduction, qui reste en attente.

**Dépôts proposés DIST-24-06 (pas une surface d'installation active).** L'arbre source
contient des producteurs déterministes de dépôts apt, rpm-md et APK, un vérificateur
d'index signés, une qualification de client propre et un flux de publication par étapes
dont le dispatch reste inerte tant qu'un relecteur ne l'approuve pas. **Aucune URL de
dépôt de paquets n'est active**, aucun nom DNS n'est délégué et aucune clé de signature
de dépôt de production n'est provisionnée. Rien dans cette proposition n'est une source
pour le gestionnaire de paquets ; continuez d'utiliser les artefacts de release vérifiés
ci-dessous.
:::

C'est la voie pour un hôte Linux normal où vous voulez le moteur en service, pas dans
un conteneur. Debian/Ubuntu et RHEL/Fedora/SUSE utilisent **systemd** par défaut.
L'init par défaut d'Alpine est **OpenRC**, pas systemd. Pour les conteneurs, voir
[Déployer avec Docker](/fr/how-to/docker-deployment/) ; pour un hôte sans route
sortante,
[Installer dans un environnement isolé](/fr/how-to/air-gap-install/), vers laquelle
cette page renvoie à l'étape de mise à jour.

## 1. Vérifiez la release avant de lui faire confiance

Pour un produit de sécurité, la chaîne de construction fait partie du modèle de
confiance, donc rien ici ne vous demande de prendre le téléchargement pour argent
comptant. Placez le paquet, `checksums.txt` et la signature dans un même répertoire
et exécutez le vérificateur **depuis ce répertoire** :

```bash
# keyless / Sigstore (default; reaches Rekor over the network)
./verify-release.sh
```

Les releases sont signées sans clé et ne publient aucune clé publique cosign : utilisez donc la
commande sans clé pour les paquets téléchargés depuis une release. Elle a besoin du matériel de
racine de confiance Sigstore, que cosign récupère s'il n'est pas déjà en cache. `--offline` retire
seulement la consultation Rekor ; la vérification ne se passe pas pour autant du réseau. `--key`
ne concerne que des fichiers signés avec une clé privée que vous contrôlez, et vous devez obtenir
cette clé publique séparément des fichiers qu'elle sert à vérifier.

Ce que chaque étape vérifie, comment elle se comporte sur une release partielle, et
comment vérifier l'image de conteneur à la place, est dans
[Vérifiez ce que vous avez téléchargé](/fr/how-to/verify-a-release/).

## 2. Installez le paquet

Les trois formats portent le binaire dans `/usr/bin/olivares`, un fichier
d'environnement dans `/etc/olivares/olivares.env` (`config|noreplace`), le
répertoire de données `/var/lib/olivares`, et les textes de licence sous
`/usr/share/doc/olivares/`. `.deb` et `.rpm` livrent l'unité **systemd** durcie.
**Les paquets de cet arbre source** placent dans le `.apk` une unité **OpenRC**
exécutable dans `/etc/init.d/olivares`, avec un tampon `package-init` explicite pour que
les crochets ne devinent pas d'après la présence de `systemctl` sur l'hôte.

Le `.apk` **publié précédemment** livrait cette même unité systemd et **pas** d'unité
OpenRC. Cette archive tar linux publiée porte les textes de licence, le README et
`SECURITY.md` ; elle n'inclut ni `scripts/install-service.sh` ni `packaging/service/`.
Ces fichiers adaptateurs sont dans l'archive signée de l'arbre pour la prochaine release.

```bash
# Debian / Ubuntu
sudo dpkg -i olivares_26.9.1_linux_amd64.deb

# RHEL / Fedora / SUSE
sudo rpm -Uvh olivares_26.9.1_linux_amd64.rpm

# Alpine
sudo apk add --allow-untrusted olivares_26.9.1_linux_amd64.apk
```

L'installation **crée l'utilisateur et le groupe système `olivares`** (avec
`/usr/sbin/nologin` comme shell et `/var/lib/olivares` comme répertoire personnel),
crée `/var/lib/olivares` en mode `0750` appartenant à cet utilisateur, et crée
`/etc/olivares`. Les paquets systemd rechargent systemd quand `systemctl` est présent ;
les paquets OpenRC n'activent ni ne démarrent le service. Il ne démarre **rien** —
voir [ce que le paquet ne fait pas](#8-ce-que-le-paquet-ne-fait-pas).

### Démarrer le moteur

Sur Debian/Ubuntu et RHEL/Fedora/SUSE :

```bash
sudo systemctl enable --now olivares
```

Sur **OpenRC** (`.apk` de cet arbre source) :

```bash
sudo rc-service olivares start
# optionnel ; le paquet ne le fait pas :
sudo rc-update add olivares default
```

Le jeton de premier démarrage est dans `/var/log/olivares.log` (et dans `logread` si
syslogd tourne). Les drapeaux supplémentaires de `OLIVARES_EXTRA_ARGS` sont ajoutés avec
le globbing désactivé et découpés sur les espaces ; les guillemets imbriqués ne sont pas
interprétés, et le fichier d'environnement n'est pas évalué comme du shell.

Sur l'**Alpine publié précédemment**, `systemctl` est absent et ce `.apk` n'a pas d'unité
OpenRC. Démarrez le moteur sous l'utilisateur de service ; le jeton de premier
démarrage s'imprime sur stdout :

```bash
sudo -u olivares olivares serve --data-dir=/var/lib/olivares \
  --listen=:8443 --grpc-listen=:8444 --checkpoint-interval=1h
```

## 3. L'unité systemd durcie

L'unité systemd empaquetée exécute le moteur sous l'utilisateur non privilégié
`olivares` avec un ensemble de capacités vide — elle n'en détient aucune, ni
ambiante ni de bornage — et `NoNewPrivileges=true`, de sorte que rien de ce qu'elle
lance ne peut en gagner. Par-dessus, elle porte `ProtectSystem=strict` (le système de
fichiers est en lecture seule sauf `ReadWritePaths=/var/lib/olivares`),
`ProtectHome`, `PrivateTmp`, `PrivateDevices`, les quatre directives
`ProtectKernel*`/`ProtectClock`, `RestrictNamespaces`, `RestrictSUIDSGID`,
`RestrictRealtime`, `LockPersonality`, `MemoryDenyWriteExecute`,
`SystemCallArchitectures=native`, un filtre d'appels système `@system-service` qui
retire en plus `@privileged` et `@resources`, et `UMask=0027`.

Sur Alpine par défaut ces directives systemd ne s'appliquent pas à un `.apk` **publié
précédemment**, parce que l'unité systemd de cette charge n'est pas en cours d'exécution. Les
paquets `.apk` construits depuis cet arbre source tournent sous OpenRC à la place : ils
utilisent le compte `olivares`, écrivent le jeton de premier démarrage dans
`/var/log/olivares.log` et n'implémentent pas les directives de bac à sable de systemd.

**Les écouteurs acceptent les connexions du réseau par défaut** — `--listen=:8443`
pour HTTP (REST plus la console embarquée) et `--grpc-listen=:8444` pour gRPC, le joker
dual-stack. C'est un serveur, et ce qui le protège, c'est TLS activé, aucun identifiant
par défaut et un jeton à usage unique. Restreignez-les délibérément avec
`OLIVARES_EXTRA_ARGS=--listen=127.0.0.1:8443 --grpc-listen=127.0.0.1:8444` dans
`/etc/olivares/olivares.env` — ces drapeaux sont ajoutés après ceux de l'unité et le
dernier l'emporte — et placez devant votre propre terminaison TLS. Pour le loopback IPv6,
utilisez `--listen=[::1]:8443`.

### Montage scratch exécutable

C'est l'échec à connaître à l'avance sur un hôte systemd, parce que le symptôme ne
nomme pas sa cause.

Les connecteurs de première partie hors processus voyagent **embarqués dans le
binaire**. Au démarrage, le moteur extrait ceux dont il a besoin dans un scratch
privé et les exécute en sous-processus. Quand `TMPDIR` n'est pas défini, le scratch
est créé sous `<data-dir>/tmp` ; seul un répertoire de données non inscriptible fait
retomber le moteur sur le répertoire temporaire système. Un `TMPDIR` explicite gagne
toujours.

Le service systemd empaqueté utilise donc `/var/lib/olivares/tmp`, pas `/tmp`, par
défaut. Vérifiez le montage qui tiendra réellement le scratch exécutable :

```bash
check_scratch_mount() {
  target=${1:-/var/lib/olivares}
  opts=$(findmnt -no OPTIONS --target "$target") || {
    printf '%s\n' "cannot read mount options for $target (missing path or permission)" >&2
    return 1
  }
  [ -n "$opts" ] || {
    printf '%s\n' "mount options for $target are unknown" >&2
    return 1
  }
  case ",$opts," in
    *,noexec,*) printf '%s\n' 'noexec — set TMPDIR' ;;
    *) printf '%s\n' 'exec-capable — nothing to do' ;;
  esac
}
check_scratch_mount /var/lib/olivares
```

S'il indique `noexec`, pointez `TMPDIR` vers un répertoire inscriptible sous
`ProtectSystem=strict` **et** situé sur un montage capable d'exécuter :

```bash
sudo install -d -o olivares -g olivares -m 0750 /run/olivares-exec-tmp
sudo systemctl edit olivares      # creates a drop-in; do not edit the shipped unit
```

```ini
[Service]
Environment=TMPDIR=/run/olivares-exec-tmp
ReadWritePaths=/run/olivares-exec-tmp
```

Puis redémarrez. Si un exec est rejeté avec `EACCES` ou `ENOEXEC`, l'erreur du moteur
nomme le montage d'extraction et les deux contrôles de relocalisation (`TMPDIR` et
data-dir) ; elle ne signale pas un montage `noexec` comme un connecteur manquant.

Utilisez `systemctl edit`, jamais une édition directe de
`/usr/lib/systemd/system/olivares.service` : ce fichier appartient au paquet et une
mise à jour le remplace.

## 4. Premier démarrage : `olivares quickstart`

`quickstart` est `serve` avec des valeurs par défaut conviviales et une bannière
guidée. Il n'invente jamais d'identifiants par défaut ; il vous oriente vers la
console embarquée pour créer le premier administrateur avec un **jeton à usage
unique**.

Si vous servez déjà sous l'unité systemd empaquetée, n'exécutez pas `quickstart` —
**le jeton de configuration de premier démarrage s'imprime dans le journal** :

```bash
journalctl -u olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'
```

Si vous avez démarré `serve` au premier plan (Alpine par défaut), la même bannière est
sur stdout.

Ouvrez la console à `https://127.0.0.1:8443` (un certificat auto-signé est généré au
premier démarrage), présentez ce jeton et créez l'administrateur. Le jeton est à
usage unique.

Sur un poste de travail, pour regarder sans installer de service,
`olivares quickstart` fait la même chose au premier plan avec
`--listen`/`--grpc-listen`/`--data-dir` si vous devez le déplacer des valeurs par
défaut.

## 5. Votre première source : pgAudit

Une source est l'endroit d'où le moteur ingère des observations. Les verbes sont
séparés selon ce que chacun coûte, et il vaut la peine de les utiliser dans cet
ordre : `plan` dit ce qui changerait et n'écrit rien, `validate` dit que la
configuration est cohérente par elle-même **sans toucher le réseau**, `test` ouvre
vraiment la source pour prouver qu'elle répond, et `set` applique. La configuration
porte des **références** de secrets (`store:<name>`), jamais des valeurs.

pgAudit lit le journal d'audit PostgreSQL, donc `log_path` est le seul champ
obligatoire :

```bash
# coherent by itself? (writes nothing, opens no socket)
sudo -u olivares olivares sources validate --name pg-prod --kind pgaudit \
  --tenant <your-tenant-id> \
  --config log_path=/var/log/postgresql/postgresql.log \
  --data-dir /var/lib/olivares

# does it actually answer? (opens the source for real)
sudo -u olivares olivares sources test --name pg-prod --kind pgaudit \
  --tenant <your-tenant-id> \
  --config log_path=/var/log/postgresql/postgresql.log \
  --data-dir /var/lib/olivares

# apply it
sudo -u olivares olivares sources set --name pg-prod --kind pgaudit \
  --tenant <your-tenant-id> \
  --config log_path=/var/log/postgresql/postgresql.log \
  --actor "$(id -un)" --reason "onboard the production audit log" \
  --data-dir /var/lib/olivares
```

Trois choses de la ligne de commande ci-dessus ne sont pas du remplissage :

- **`--tenant` est obligatoire.** Une source doit nommer le tenant métier auquel
  appartiennent ses observations ; sans lui, la commande refuse plutôt que de
  deviner un propriétaire pour vos données d'audit.
- **`--actor` et `--reason` sont exigés par `set`, et seulement par `set`.** Une
  opération privilégiée hors ligne doit enregistrer qui l'a faite et pourquoi.
  `validate` n'a besoin d'aucun des deux, parce qu'il n'écrit rien — l'asymétrie est
  le point.
- **`format` vaut par défaut `csvlog` et `follow` vaut `true`**, donc un déploiement
  pgAudit standard n'a besoin d'aucun des deux.

L'application imprime ce qui a changé, champ par champ, et vous dit comment faire
prendre cela à un moteur **en cours d'exécution** sans redémarrage —
`POST /v1/console/runtime/reload`, ou un `SIGHUP`. Sous l'unité systemd empaquetée,
c'est `sudo systemctl reload olivares`. Si vous avez démarré `serve` au premier plan,
envoyez `SIGHUP` à ce processus.

L'utilisateur de service a besoin d'un accès en lecture à ce fichier journal ; sur
la plupart des distributions, cela signifie ajouter `olivares` au groupe `adm` ou
`postgres` — une concession délibérée de votre part, pas quelque chose que le paquet
fait pour vous.

## 6. Mise à jour

`olivares upgrade` remplace le binaire sur place, et les propriétés de sécurité sont
la raison de le préférer à une réinstallation manuelle du paquet : il **ne remplace
jamais le binaire avant que le candidat téléchargé ait été sondé avec succès par
exec**, il conserve une sauvegarde horodatée, et il **revient à cette sauvegarde si
le sondage après échange échoue**.

```bash
sudo olivares upgrade --check    # what would change, without changing anything
sudo olivares upgrade --yes      # do it
```

Trois drapeaux importent spécifiquement pour une installation empaquetée :

- **`--endpoint`** — prendre les mises à jour d'un dépôt GitHub que vous contrôlez
  plutôt que celui par défaut. C'est la voie de sortie pour un miroir ou une
  bifurcation.
- **`--bundle`** — installer depuis un répertoire de bundle local ou un `.tar.gz`
  **sans aucun réseau**. Construire ce bundle et le déplacer est décrit dans
  [Installer dans un environnement isolé](/fr/how-to/air-gap-install/).
- **`--install-timer`** — émettre un minuteur et un service **systemd optionnels**
  qui vérifient les mises à jour selon un calendrier. Rien ne l'installe pour vous ;
  voir [ce que le paquet ne fait pas](#8-ce-que-le-paquet-ne-fait-pas). C'est un
  générateur systemd.

Notez que la mise en scène et le sondage exec se font **dans le répertoire
d'installation, à côté de la cible — pas dans `/tmp`**, donc le montage `noexec`
discuté [plus haut](#montage-scratch-exécutable) ne casse pas une mise à jour. Un
montage `noexec` sur le répertoire d'**installation** est une autre affaire et rend
la version installée non mesurable ; ce cas, les canaux de release, le déploiement
par étapes et le retour arrière sont dans
[Mettre à jour et revenir en arrière](/fr/how-to/upgrade-and-rollback/).

## 7. Désinstaller ou migrer sans deviner les chemins

Le paquet écrit `/var/lib/olivares/install-manifest.json`. Le désinstalleur valide le
manifeste complet contre l'index de distribution signé avant de toucher au service ou
au système de fichiers ; un chemin inattendu renvoie 2. Inspectez d'abord, puis
choisissez explicitement la conservation :

```bash
sudo olivares uninstall --plan --data-dir /var/lib/olivares
sudo olivares uninstall --preserve --data-dir /var/lib/olivares
sudo olivares uninstall --purge --data-dir /var/lib/olivares --yes
```

Preserve est la politique de retrait du paquet : elle conserve la configuration, les
données, les journaux, les clés et leur identité de service. Sur les paquets systemd, le
crochet de retrait exécute `--preserve`. Sur les paquets `.apk` OpenRC construits depuis
cet arbre source, le crochet arrête le service s'il est actif, retire l'entrée du
runlevel par défaut sans échouer s'il n'a jamais été activé, valide le manifeste complet
avec `--plan` et laisse apk retirer les fichiers appartenant au paquet. Les paquets
Alpine publiés précédemment validaient seulement avec `--plan`, parce que cette charge
n'avait pas d'unité OpenRC à arrêter. Exécutez `--purge` avant de retirer le paquet
seulement lorsque l'effacement est l'intention. Il exige une confirmation et
supprime uniquement les chemins indexés.

Pour un déménagement de patrimoine, créez d'abord un `olivares dr backup`, installez
la destination et exécutez-y `olivares dr restore`. Les bundles actuels utilisent
`hmac-sha256-kek-v1` pour authentifier le manifeste et chaque charge utile sous
votre KEK. Une exportation d'un moteur plus récent est refusée avant les écritures ;
un bundle pré-v26.9 authentifié séparément exige `--allow-legacy-unsigned`
explicitement. Le
[guide de sauvegarde et restauration](/fr/how-to/backup-and-restore/) couvre la
garde du KEK et la preuve de continuité après import.

## 8. Ce que le paquet ne fait pas

Dit clairement, parce qu'un produit de sécurité vague ici ne mérite pas
l'installation :

- **Il n'ajoute pas de dépôt.** Rien n'est écrit dans `/etc/apt/sources.list.d`,
  `/etc/yum.repos.d` ni `/etc/apk/repositories`. Vous avez installé un fichier ;
  seul ce fichier a été installé. Les mises à jour sont à vous de déclencher — par
  un nouveau paquet, ou par `olivares upgrade`.
- **Il ne démarre ni n'active le service.** Les paquets systemd rechargent systemd
  lorsque `systemctl` est présent et impriment `systemctl enable --now`. Les paquets
  OpenRC impriment `rc-service olivares start` et n'exécutent pas `rc-update add`.
  Démarrer reste votre décision. Une mise à niveau d'un service OpenRC déjà en cours
  l'arrête, remplace les fichiers et le redémarre ; un service inactif reste inactif.
- **Vérifier une licence n'appelle jamais personne. Télécharger ce que vous avez
  payé, si.** La validation de licence dans la compilation ouverte est Ed25519 hors
  ligne, il n'y a pas d'interrupteur distant, et aucune clé de licence ne bride ni
  ne dégrade cette compilation — la compilation AGPL est toute la plateforme.
  Il n'y a **pas de télémétrie obligatoire ni d'égresse du plan de contrôle par
  défaut : ce qui traverse votre périmètre est ce que vous configurez pour le
  traverser** — appels à vos API de modèles, les sorties SIEM/webhook que vous
  câblez, un fournisseur d'embeddings externe si vous en provisionnez un, et toute
  source qu'un connecteur interroge (son `addr`, `base_url` ou `endpoint`) à
  l'intervalle que vous fixez.
- **Mais `olivares upgrade` fait un appel réseau, délibérément, quand vous
  l'exécutez** — c'est le but d'une vérification de mise à jour, et `--check` vous
  montre le plan avant que rien ne bouge. La forme honnête de la promesse est :
  *vérifier une licence n'appelle jamais personne ; télécharger ce que vous avez
  payé, si.* Utilisez `--bundle` si vous voulez que le chemin de mise à jour ne
  fasse aucun appel non plus.
- **Il ouvre un port vers le réseau.** L'unité se lie à toutes les interfaces
  jusqu'à ce que vous l'élargissiez vous-même.

## Voir aussi

- [Vérifiez ce que vous avez téléchargé](/fr/how-to/verify-a-release/) — le chemin
  complet de vérification
- [Mettre à jour et revenir en arrière](/fr/how-to/upgrade-and-rollback/) — canaux,
  déploiement par étapes, retour arrière
- [Durcir un déploiement](/fr/how-to/security-hardening/) — au-delà de ce que
  l'unité fait déjà
- [Installer dans un environnement isolé](/fr/how-to/air-gap-install/)
- [Déployer avec Docker](/fr/how-to/docker-deployment/)
- [Connecter une source](/fr/how-to/connect-a-source/)
- [Sauvegarder et restaurer](/fr/how-to/backup-and-restore/)
