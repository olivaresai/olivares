---
title: "Le modèle de sécurité"
description: "La posture sécurisée par conception derrière Olivares AI — pourquoi la lecture d'abord, les données minimales, le refus par défaut et un audit à altération détectable sont les décisions de sécurité porteuses, et non l'énumération des menaces."
---

Olivares AI est un produit de sécurité qui s'exécute **au sein de la propre
infrastructure du client** et construit une carte de ce que chaque agent IA peut
atteindre. Cela le rend à la fois hautement sensible et hautement précieux pour un
attaquant : un défaut dans ce produit est une compromission du parc du client. La barre
est donc la plus élevée, et la posture est conçue pour **passer un pentest et un audit
d'entreprise dès le départ** plutôt que d'être durcie ultérieurement.

Cette page explique la **posture** — les décisions de sécurité ancrées dans la
conception et pourquoi elles sont ce qu'elles sont. Elle ne restitue délibérément
**pas** le modèle de menace formel : l'analyse STRIDE par composant et le flux de
données par frontière de confiance vivent sur la page du
[modèle de menace](/fr/explanation/security/threat-model/). Lisez cette page-là pour *ce
qui pourrait mal tourner et où* ; lisez celle-ci pour *pourquoi l'architecture est
façonnée pour rendre ces choses difficiles*.

:::note[Posture, pas une carte de reconnaissance]
Cette documentation décrit la posture de sécurité, non la surface d'attaque. Elle
n'énumère pas les chaînes de permissions internes, les emplacements des fichiers de
secrets, ni la disposition des ports d'un déploiement. Ces éléments appartiennent au
matériel de durcissement pour opérateurs, pas à la documentation publique.
:::

## Lecture d'abord : faible risque asymétrique

Le cœur **observe** ; il ne s'interpose pas. La carte d'accès est reconstruite à partir
des signaux que le parc émet déjà — OpenTelemetry, audit de base de données, journaux
d'audit cloud, et (comme backstop non coopératif) eBPF — et le collecteur n'est
**jamais dans le chemin de données de l'agent**.

C'est une décision de sécurité avant d'être une décision produit. Un point d'application
en ligne placé devant chaque action d'agent est un point de défaillance unique : s'il
cale ou plante, il peut entraîner la production avec lui, et il devient une cible de
grande valeur précisément *parce qu'il* est dans le chemin. Un observateur en lecture
d'abord porte le profil de risque opposé, **asymétrique**. Si le collecteur échoue, il
cesse de *voir* — il n'arrête pas l'agent, et il ne casse pas la production. La
défaillance dans le pire des cas d'un observateur est un trou dans la visibilité, non
une panne.

La même propriété neutralise l'évasion évidente. Le collecteur s'exécute comme un
service distinct, privilégié, **hors du contrôle de l'agent**, de sorte qu'un agent qui
désactive sa propre télémétrie ne réduit pas le collecteur au silence — et le backstop
eBPF enregistre toujours l'action au niveau du noyau. Un agent connu qui devient
soudainement silencieux est lui-même traité comme un signal, non ignoré.

## Données minimales : ce qui n'est pas stocké ne peut pas fuiter

Le graphe stocke des **relations**, non des contenus. Une arête enregistre qu'un agent a
touché une ressource, dans quel mode (lecture / écriture / lecture-écriture), depuis
quelle source de signal, avec quelle confiance, et quand. Il ne stocke **pas** le SQL
qu'il a exécuté, le corps de la requête, le secret, ni les données personnelles qu'ils
contiennent. Là où une valeur n'est nécessaire que pour dédupliquer, le produit
conserve un hachage à sens unique, jamais la valeur elle-même.

Le principe directeur est sans détour : **ce qui n'est pas stocké ne peut pas fuiter.**
L'actif le plus sensible du système — la carte d'accès — est aussi celui délibérément
construit à partir des données les moins sensibles.

Les champs les plus susceptibles de transporter des secrets ou des données personnelles
(une entrée d'outil, une commande complète) sont **expurgés avant d'être persistés**.
L'expurgation n'est pas laissée au bon comportement du gestionnaire : le moteur l'impose
sur le chemin d'écriture, remplaçant une valeur marquée sensible par un hachage avant
qu'elle ne soit jamais écrite, comme backstop même si un gestionnaire l'oublie. Le
collecteur lit des **identités** — un rôle de base de données, un nom d'application, un
principal IAM — non des valeurs de credentials ni des charges utiles. Ce n'est pas un
renifleur de données.

:::note[La couverture est par paliers, et le produit le dit]
La fidélité lecture/écriture dépend de ce que le stockage sous-jacent expose. Elle est
élevée sur les stockages dotés d'un audit natif (SQL, stockage objet, entrepôts), avec
pertes sur certains stockages document/vecteur, et **impossible à reconstruire
passivement** sur d'autres. Là où lecture versus écriture ne peut être déterminée,
l'arête est marquée `unknown`, et l'attribution se réduit à `approximate` lorsqu'un
compte de service partagé masque l'identité par agent. Le produit montre cela
honnêtement plutôt que de fabriquer une certitude — voir
[honnêteté & limites](/fr/start/honesty-and-limits/).
:::

## Tokens opaques et révocables plutôt que JWT

L'authentification utilise des **tokens bearer opaques**, non des JWT. Le token est une
poignée aléatoire ; toute l'autorité vit côté serveur, liée à un enregistrement que le
moteur contrôle. C'est un choix de posture. Un JWT autonome est un porteur de
revendications permanent, vérifiable hors ligne, qui est délicat à révoquer avant
expiration ; un token opaque est **révocable immédiatement** en invalidant son
enregistrement côté serveur, ne porte aucune revendication intégrée susceptible de
fuiter ou d'être mal jugée digne de confiance, et garde le lien au tenant sous le
contrôle du moteur plutôt que dans une signature détenue par le client. Les tokens de
session et d'API sont des types distincts, et le tenant est résolu à partir du propre
lien du token — une requête dont l'en-tête de tenant contredit son token est
**rejetée**, non réconciliée.

## Aucun credential par défaut, token de configuration à usage unique

L'échec le plus courant d'un produit auto-hébergé est un **credential par défaut**.
Olivares AI n'en livre **aucun**. Au premier démarrage, le moteur imprime un **token de
configuration à usage unique** sur la sortie standard ; l'administrateur l'utilise pour
créer le premier utilisateur, puis il est consommé. Il n'y a aucun compte intégré, aucun
mot de passe partagé, et rien à oublier de changer. (Une amorce de démonstration existe
uniquement pour l'évaluation ; elle porte un mot de passe public et **refuse de se lier
à autre chose que loopback** afin qu'elle ne puisse jamais devenir un point d'ancrage en
production.)

## Autorisation en refus par défaut, une couture ABAC qui ne fait que restreindre

L'autorisation est en **refus par défaut**. Le contrôle d'accès basé sur les rôles
n'accorde rien qu'on ne lui dise explicitement d'accorder. Au-dessus du RBAC se trouve
une couture de politique basée sur les attributs — l'opérateur peut exécuter un moteur de
politique pur-Go embarqué, un service de politique externe via HTTP, ou aucun, le tout
derrière une seule interface — et l'invariant critique est que **la couche ABAC ne peut
que restreindre l'accès, jamais l'élargir.** Une politique peut retirer une permission ;
elle ne peut jamais accorder une permission que le RBAC n'autorisait pas déjà. Cet ordre
signifie qu'une politique mal configurée ou trop permissive ne peut pas devenir un
chemin d'élévation de privilèges : le pire qu'une mauvaise politique puisse faire est
d'exclure des personnes, non de les laisser entrer.

## Consulter le graphe est une action privilégiée, à portée de tenant, auditée

Parce que la carte d'accès est un puissant outil de reconnaissance, la conception traite
**la lire comme une action privilégiée**, non comme une capacité par défaut. Elle est
accordée à partir d'un rôle de niveau éditeur et au-dessus, et n'est **jamais**
disponible pour le rôle de visualiseur le plus bas. Chaque lecture est **limitée au
tenant** — un client ne peut jamais voir le parc d'un autre — et **chaque lecture est
enregistrée dans le journal d'audit** : qui a regardé la carte d'accès de quel agent, et
quand. La défense est ici en couches à dessein : privilège, isolation des tenants et
auto-audit ensemble, de sorte que même un accès légitime à la vue la plus sensible
laisse une trace responsabilisante.

C'est aussi là que se trace la ligne d'usage responsable du produit. Olivares AI est
cadré **défensivement** — il aide les défenseurs à voir et gouverner leur propre parc.
Ce n'est pas un framework de commande et contrôle et il ne scanne pas les credentials
d'autrui. Cette ligne est gardée explicite dans le
[modèle de menace](/fr/explanation/security/threat-model/).

## Audit en ajout seul, à chaînage de hachage, signé — avec l'export externe comme contrôle réel

Le journal d'audit est en **ajout seul** et à **chaînage de hachage** : chaque
enregistrement porte le hachage du précédent, de sorte que toute altération silencieuse
brise la chaîne et est détectable. Au-dessus de la chaîne, le moteur produit des points
de contrôle **signés Ed25519**, afin que la fin ne puisse être réécrite sans la clé de
signature.

Le produit est honnête sur la limite d'un journal embarqué : un attaquant avec un
contrôle total du répertoire de données et de la clé embarquée pourrait en principe
re-signer une chaîne falsifiée. La signature par événement défend contre la compromission
**limitée à la base de données** — injection, sauvegarde ou réplique volée, contournement
de la sécurité au niveau ligne — et contre la suppression de points de contrôle ; elle ne
défend pas, à elle seule, contre la compromission totale de l'hôte.

Le **véritable contrôle anti-altération est donc externe**. Le journal est exporté vers
un système **WORM/SIEM** que le client contrôle, dans des formats standards (`cef`,
`leef`, `syslog`, `otlp`, `otlp_envelope`, `otlp_log_record`, `ocsf`),
portant le numéro de séquence, le hachage précédent,
le hachage et la signature, et **jamais de données personnelles**. Une fois qu'une copie
vit dans un stockage immuable hors du produit, un attaquant qui compromet l'hôte
Olivares ne peut pas revenir en arrière et réécrire ce que le SIEM détient déjà. Cette
copie externe immuable — non la chaîne embarquée seule — est ce qu'un auditeur
d'entreprise demande, et c'est ce que la télémétrie native ne donne pas.

:::note[Deux chemins hors machine : le pull et un push réel]
Le journal vérifiable atteint un SIEM par deux voies. L'export **pull**
(`GET /v1/audit/export`) est toujours disponible et constitue l'artefact qu'un
exploitant archive. Un **push** est réel dès qu'il est configuré : un abonnement
d'eventing `audit.recorded` démarre une pompe de journal par locataire qui livre chaque
enregistrement scellé **au moins une fois** via le transport durable, protégé contre le
SSRF, avec reprise et file de rebut (`modules/siemforward/forwarder.go`, câblé dans
`cmd/olivares/boot.go`). `NopForwarder` s'applique lorsqu'aucun forwarding n'est
configuré — ce n'est pas la seule implémentation existante. Le
[guide Splunk](/fr/how-to/forward-audit-to-splunk/) documente les deux chemins ; la
vérification de signature se fait hors machine, contre la clé publique.
:::

## TLS activé par défaut, aucun repli en clair, mTLS pour les collecteurs distants

Le transport est **chiffré par défaut et échoue fermé**. TLS est activé, et il n'y a
**aucun repli silencieux en clair** — une connexion qui ne peut être sécurisée est
refusée, non rétrogradée. Un mode en clair existe strictement pour le développement en
localhost et doit être demandé explicitement ; il n'est jamais le défaut ni le chemin de
production.

Dans la topologie distribuée, les collecteurs distants **poussent** vers le cœur central
(il n'y a aucun listener entrant sur l'hôte de production, ce qui maintient à zéro la
surface de ports ouverts du collecteur), et ce canal peut exiger un **TLS mutuel** avec
un certificat client vérifié. Le chiffrement au repos est fourni par le déploiement —
chiffrement de disque complet, de système de fichiers ou au niveau base de données —
plutôt que par un pragma au niveau produit, avec des permissions de fichiers strictes
sur le répertoire de données.

## La licence est purement attestation — le cœur ouvert n'est jamais conditionné

La licence commerciale est vérifiée **hors ligne** avec une signature Ed25519, et dans le
**cœur ouvert (AGPL)** c'est une **attestation, non un verrou de fonctionnalité** : rien
dans le produit ouvert ne se désactive jamais sur une vérification de licence. Les add-ons
commerciaux sont licenciés pour un terme payé — un droit qui prend fin avec le terme —
mais toute conséquence en est une décision locale et hors ligne de la build commerciale ;
il n'y a pas de kill switch distant, et vérifier la licence ne nous contacte jamais.
Télécharger ce que vous avez payé, si : l'abonnement est le justificatif d'accès avec lequel
les add-ons commerciaux, leurs mises à jour et leurs correctifs sont récupérés — le modèle
SUSE/Novell, décrit dans [auto-hébergement](/fr/how-to/self-hosting/).
Cela importe particulièrement pour le cas
air-gapped : le produit doit continuer
à faire son travail de sécurité — observer, enregistrer, auditer — quel que soit l'état
de la licence, car un contrôle de sécurité qui se dégrade silencieusement sur un problème
de licence est lui-même une vulnérabilité. La révocation est gérée via l'expiration de
l'abonnement, non en paralysant le moteur en cours d'exécution.

<a id="auto-hébergé--les-données-restent-à-lintérieur-du-périmètre-du-client"></a>

## Auto-hébergé : le client décide de ce qui franchit son périmètre

La propriété structurelle la plus forte de la conception est qu'il n'y a **pas de
télémétrie obligatoire et, par défaut, aucune sortie du plan de contrôle**. Ne franchit
le périmètre du client que ce que celui-ci configure à cette fin : les appels à ses API
de modèles, les sorties SIEM/webhook qu'il raccorde et, s'il en provisionne un, un
fournisseur externe d'embeddings. Olivares AI s'exécute sur les propres
hôtes du client ; le plan de données (les collecteurs) s'exécute **toujours** sur
l'infrastructure du client ; et il n'y a **aucun retour de télémétrie** — rien n'est envoyé à
Olivares AI comme effet de bord de l'exécution. L'éditeur n'est contacté que lorsque le client
lui demande quelque chose — `olivares upgrade`, ou un téléchargement par abonnement des add-ons
commerciaux et de leurs mises à jour — et il ne voit pas la carte d'accès du client.

C'est une réponse directe et défendable aux exigences de **RGPD et de résidence des
données** : chaque franchissement est provisionné par le client, qui détermine et produit
donc les preuves de résidence au lieu d'obtenir une garantie du fournisseur. Et cela fait
de la topologie **air-gapped** un déploiement de premier rang —
tout en local, **zéro sortie réseau**, licence hors ligne — plutôt qu'une réflexion après
coup, pour les parcs qui doivent fonctionner sans aucun réseau sortant. Voir les guides
[auto-hébergement](/fr/how-to/self-hosting/) et
[installation air-gap](/fr/how-to/air-gap-install/).

:::tip[Conçu pour l'audit, certifié plus tard]
L'architecture est construite pour **se mapper sur** les contrôles que recherchent SOC 2,
ISO 27001 et l'EU AI Act — journalisation d'audit, contrôle d'accès, intégrité,
chiffrement, gestion des changements — afin qu'elle passe la revue le moment venu. La
certification formelle est une étape ultérieure et distincte ; la conception la rend
possible, elle ne la revendique pas. La page
[honnêteté & limites](/fr/start/honesty-and-limits/) est le contrat liant sur ce qui est
construit aujourd'hui versus conçu.
:::

## Pourquoi ces décisions tiennent ensemble

Aucun de ces choix ne tient seul. La lecture d'abord garde le produit hors du rayon
d'explosion des systèmes mêmes qu'il surveille. Les données minimales réduisent ce qu'une
compromission du produit pourrait même exposer. Les tokens opaques, l'absence de
credentials par défaut, le RBAC en refus par défaut et une couture ABAC qui ne fait que
restreindre signifient que l'autorité est petite, révocable, et impossible à élargir par
accident. Le journal à chaînage de hachage, signé, exporté en externe rend la propre
honnêteté du produit **vérifiable** plutôt que simplement promise. Et l'auto-hébergement
signifie qu'il n'y a pas de télémétrie obligatoire et, par défaut, aucune sortie du plan de
contrôle. Ne franchit le périmètre du client que ce que celui-ci configure à cette fin :
ses API de modèles, les sorties SIEM/webhook qu'il raccorde et, s'il en provisionne un,
un fournisseur externe d'embeddings. La posture est l'argument de
sécurité ; le [modèle de menace](/fr/explanation/security/threat-model/) est là où chacun de
ces points est confronté à une menace concrète.
