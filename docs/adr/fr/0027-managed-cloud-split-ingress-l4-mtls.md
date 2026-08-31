> Traduction automatique. La version anglaise fait foi.

# ADR-0027: Entrée du cloud géré — passthrough L4 pour le mTLS des collecteurs, L7 pour l'API du plan de contrôle

- **Status:** accepted (managed cloud; this record creates no infrastructure)
- **Date:** 2026-08-02
- **Deciders:** Fran Olivares
- **References:** ADR-0012 (collectors push to the core over gRPC + mTLS), ADR-0028
  (managed-cloud database), ADR-0029 (managed-cloud regions), ADR-0009 (append-only
  hash-chained audit); the platform decision record for the managed cloud; AWS Elastic
  Load Balancing documentation, consulted 2026-08-02:
  `https://docs.aws.amazon.com/elasticloadbalancing/latest/network/network-load-balancers.html`,
  `https://docs.aws.amazon.com/elasticloadbalancing/latest/network/edit-target-group-attributes.html`,
  `https://docs.aws.amazon.com/elasticloadbalancing/latest/application/configuring-mtls-with-elb.html`.

## Contexte et énoncé du problème

L'ADR-0012 a fixé la topologie d'ingestion : les collecteurs s'exécutent sur
l'infrastructure du client et **poussent** des observations via gRPC avec TLS mutuel, et le
cœur **termine lui-même ce mTLS**.

Il convient de préciser exactement ce que cela apporte, car la version approximative de
cette phrase est fausse et aurait des conséquences déterminantes si elle était tenue pour
vraie. L'admission sur le plan des collecteurs repose sur **deux facteurs indépendants** :

1. **Une barrière de transport.** Le serveur exige et vérifie un certificat client dont la
   chaîne remonte à la CA des collecteurs configurée. Cela prouve la possession d'une clé
   dont nous avons émis le certificat ; celui-ci n'est pas analysé pour en extraire un sujet
   et ne désigne aucun principal.
2. **Un principal bearer.** L'identité authentifiée sur laquelle agissent l'autorisation et
   la chaîne d'audit (ADR-0009) provient du bearer token de la requête, et non du certificat.

Les deux sont appliqués **dans le propre processus du produit**. Aucun intermédiaire ne se
porte garant de l'un ou l'autre facteur. Telle est la propriété visée par ce document : non
pas « le certificat est l'identité », mais « aucun intermédiaire ne se porte garant de l'un
ou l'autre facteur ».

Le cloud géré est le premier déploiement qui place un équilibreur de charge devant ce
binaire. Le même déploiement expose également une surface HTTPS publique ordinaire — API
REST, console, administration — qui requiert le traitement opposé : un certificat public
géré, un pare-feu applicatif web et un routage par hôte/chemin. Une seule entrée ne peut
servir les deux sans sacrifier quelque chose d'un côté.

## Facteurs de décision

- Les deux facteurs d'admission doivent continuer d'être appliqués par **une session TLS que
  le produit termine lui-même**. Un cloud géré qui dégraderait discrètement l'un ou l'autre
  en « un intermédiaire nous a dit qu'il était valide » affaiblirait la promesse centrale du
  produit.
- La surface HTTP publique doit pouvoir utiliser les protections périphériques offertes par
  L7, sans que le produit ait à les réimplémenter.
- Les flux de collecteurs de longue durée ne doivent pas être interrompus par la gestion de
  l'inactivité de l'entrée.
- Aucune régression par rapport au déploiement auto-hébergé : un seul chemin de code, pas
  deux.

## Options envisagées

- **A — un seul équilibreur de charge L4 pour tout.** Passthrough TCP pour les deux plans ;
  le binaire termine chaque session TLS, y compris celle de l'API publique.
- **B — entrée séparée.** Un **équilibreur de charge réseau (L4) doté d'un listener TCP**
  pour le plan des collecteurs en passthrough, ainsi qu'un **équilibreur de charge
  applicatif (L7)** pour la surface HTTP du plan de contrôle.
- **C — un seul équilibreur de charge L7 avec TLS mutuel géré.** L'équilibreur de charge
  applicatif authentifie lui-même les certificats clients (mode verify face à un trust
  store, avec des listes de révocation) ou transmet la chaîne à la cible dans un en-tête
  HTTP.

## Résultat de la décision

Option retenue : **B — entrée séparée**.

### Conséquences

- **Avantage :** le plan des collecteurs suit, octet pour octet, le chemin auto-hébergé. Un
  listener TCP ne termine pas TLS ; le binaire réalise donc la négociation et impose lui-même
  l'exigence du certificat, exactement comme sur site. Il n'y a aucune branche propre au cloud
  dans l'outil d'autorisation ni aucun cas propre au cloud dans la chaîne d'audit.
- **Avantage :** la surface publique peut utiliser un certificat géré, le routage par
  hôte/chemin et un pare-feu applicatif web sans que le produit ait à réimplémenter l'un de ces
  éléments. Le pare-feu est un service **facturé séparément**, et non une propriété gratuite
  de l'équilibreur de charge L7 ; il est indiqué ici comme disponible, et non comme inclus.
- **Avantage, avec une portée énoncée précisément :** le délai d'inactivité du listener TCP
  est **configurable entre 60 et 6000 secondes** (`tcp.idle_timeout.seconds`, valeur par
  défaut **350**) ; celui d'un listener TLS est **fixé à 350 secondes et ne peut pas être
  modifié**. Il s'agit d'un délai d'**inactivité** — une absence d'octets —, **et non d'un
  plafond de durée du flux** : un flux qui continue d'envoyer des données ou des frames de
  keepalive n'est pas coupé à 350 secondes. Le passthrough ne « rend donc pas possibles les
  flux longs » ; il nous permet de fixer le budget d'inactivité. Dit dans l'autre sens, car
  c'est ce qui importe : **un flux silencieux meurt sur chacune de ces entrées**, et le client
  doit y survivre.
- **Inconvénient, et raison pour laquelle le point précédent est formulé comme un
  avertissement :** le client du collecteur ne configure **aucun keepalive gRPC** (il est
  désactivé par défaut dans la bibliothèque) et, après un envoi échoué, conserve le flux mort
  en cache au lieu de le reconstruire. Une période d'inactivité supérieure au délai configuré,
  un changement de leader ou un déploiement met donc fin à un flux de collecteur que rien ne
  reconnecte. Ce problème n'est **pas créé par la séparation** — il préexistait —, mais la
  séparation constitue le premier déploiement où un intermédiaire fermera activement les
  connexions inactives ; c'est donc là que cette lacune commence à coûter des données. Une
  boucle de reconnexion avec backoff côté collecteur est une **condition préalable** pour
  qualifier cette entrée de prête pour la production.
- **Inconvénient / compromis :** deux équilibreurs de charge entraînent deux coûts horaires
  et deux compteurs d'unités de capacité indépendants qui, ensemble, dominent le plancher
  mensuel fixe d'un petit déploiement. Il s'agit d'un coût réel et récurrent, payé pour
  conserver les deux facteurs d'admission dans le processus.
- **Inconvénient, et exigence de build plutôt que note de bas de page :** pour les **groupes
  cibles de type IP utilisant le protocole TCP ou TLS, la préservation de l'IP client est
  désactivée par défaut** — et les tâches du runtime de conteneurs géré sont des cibles IP.
  Avec la valeur par défaut, chaque connexion de collecteur atteint le binaire avec l'adresse
  privée de l'équilibreur de charge comme adresse source. Tout ce qui est dérivé de l'adresse
  — enregistrements d'audit, limites de débit, listes d'adresses autorisées — serait
  silencieusement erroné dès le premier jour. L'entrée n'est complète que si
  `preserve_client_ip.enabled` est activé ou si le binaire analyse Proxy Protocol v2 avant
  la négociation. L'activation de la préservation signifie également que
  le groupe de sécurité de la cible est exposé aux adresses des clients plutôt qu'à celle de
  l'équilibreur de charge, ce que la conception du réseau doit prendre en compte.
- **Neutre / suivis :** le choix du mécanisme qui rétablit l'adresse source est laissé à la
  phase d'implémentation, mais **ce choix doit être fait et testé, et non hérité d'une valeur
  par défaut**. Le critère d'acceptation est un test affirmant que l'adresse source enregistrée
  est celle du collecteur.

## Pourquoi les alternatives ont été rejetées

- **A (un seul équilibreur de charge L4)** — rejetée pour le plan *public*, pas pour le plan
  des collecteurs. Elle est moins chère et se rapproche le plus de la topologie auto-hébergée,
  mais l'API du plan de contrôle perdrait les certificats gérés, le WAF et le routage par
  hôte/chemin, et le produit finirait par réimplémenter en L7 ce que la périphérie fournit
  déjà. La moitié de l'option A consacrée aux collecteurs est exactement ce que l'option B
  conserve.
- **C (TLS mutuel géré en L7)** — rejetée parce qu'elle **déplace la frontière de
  confiance**. En mode verify, la périphérie vérifie le certificat et l'application reçoit une
  requête dont l'authenticité a déjà été garantie ; en mode passthrough, la chaîne de
  certificats arrive dans un en-tête `X-Amzn-Mtls-Clientcert`. Dans les deux cas, la barrière
  de transport cesse d'être appliquée par le produit et devient une assertion émise par un
  autre composant — précisément la substitution que ce produit existe pour rendre vérifiable,
  et dont le mode de défaillance (tout ce qui peut atteindre directement la cible peut
  falsifier l'en-tête) n'est distant que d'une erreur de configuration réseau. Le trust store
  géré avec listes de révocation constitue un véritable avantage opérationnel, dont le produit
  ne dispose actuellement pas du tout pour les certificats des collecteurs : il charge une CA
  et effectue une validation X.509 ordinaire, sans vérification CRL ni OCSP. Si la révocation
  gérée l'emporte un jour sur la terminaison directe, cela fera l'objet d'un **nouvel ADR**, et
  non d'une modification de celui-ci.
