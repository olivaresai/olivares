---
title: Où Olivares s'inscrit face à votre passerelle IA & Guardrails
description: >-
  Vous exploitez déjà une passerelle IA (LiteLLM, Portkey, Cloudflare) ou des
  Guardrails de hyperscaler (Bedrock, Azure). Bien — gardez-les. Olivares AI n'est
  pas une passerelle et ne concurrence pas sur le routage ou le cache. C'est le
  plan de gouvernance et de preuve qui se place à côté d'eux et comble la lacune
  qu'ils laissent ouverte.
sidebar:
  order: 7
---

Si vous avez déjà investi dans une **passerelle IA** ou dans les **Guardrails**
d'un hyperscaler, la première chose honnête à dire est : **gardez-les, et Olivares
AI ne cherche pas à les remplacer.** Le travail d'une passerelle, c'est l'appel de
modèle — le router, le mettre en cache, l'équilibrer, le budgétiser. Le travail des
Guardrails, c'est la sûreté du contenu sur cet appel. Les deux sont réels, les deux
sont bons à ce qu'ils font, et ni l'un ni l'autre n'est ce qu'est Olivares.

:::tip[La version courte]
**Olivares AI n'est pas une passerelle IA.** Il ne route pas, ne met pas en cache,
n'équilibre pas la charge et ne se place pas sur le chemin critique de votre trafic
de modèles, et il ne le fera jamais. Il se place **à côté et derrière** votre
passerelle en tant que *plan de gouvernance et de preuve* : application in-process
au sein du runtime de l'agent, un registre de preuves à altération détectable, cycle de vie
d'identité non humaine, et human-in-the-loop / break-glass / kill-switch sur les
**sessions en direct**. Votre passerelle gouverne la *requête* ; Olivares gouverne
l'*agent et tout ce qu'il touche*, et le prouve à un auditeur.
:::

## Ce qu'une passerelle et les Guardrails font bien (utilisez-les pour cela)

Ce sont des capacités banalisées et bien comprises, et les fournisseurs les
décrivent clairement :

- **Les passerelles IA** sont des gestionnaires de chemin de requête pour les
  appels de modèle. LiteLLM est un *"OpenAI Proxy Server (LLM Gateway) to call 100+
  LLMs in a unified interface & track spend, set budgets per virtual key/user"*
  ([LiteLLM](https://docs.litellm.ai/docs/simple_proxy)) ; Cloudflare AI Gateway
  vous permet de *"Connect to any model, dynamically route requests, and manage
  usage, billing, and logs from one unified gateway"*
  ([Cloudflare](https://www.cloudflare.com/products/ai-gateway/)) ; Portkey
  *"records real-time API requests, including cost"*
  ([Portkey](https://portkey.ai/features/ai-gateway)). Routage, replis, cache, clés
  virtuelles, budgets par clé, journalisation des requêtes — c'est leur couloir.
- **Les Guardrails de hyperscaler** sont des filtres de sûreté du contenu. Bedrock
  Guardrails *"provides configurable safeguards to help you build safe generative
  AI applications"* qui *"detect and filter undesirable content and protect
  sensitive information that might be present in user inputs or model responses"* —
  filtres de contenu, sujets interdits, filtres de mots, caviardage de PII,
  contrôles de contextual-grounding et d'automated-reasoning
  ([AWS](https://docs.aws.amazon.com/bedrock/latest/userguide/guardrails.html)).

Si votre problème est *« donner à mes applications un endpoint unique vers de
nombreux modèles, avec budgets, cache et filtrage de contenu »*, cette stack le
résout, et vous n'avez pas besoin d'un control plane pour le faire. Nous nous
intégrons à ce schéma ; nous ne le réimplémentons pas.

## La lacune de gouvernance qu'ils laissent ouverte

Une passerelle voit une **requête**. Les Guardrails voient du **contenu**. Ni l'un
ni l'autre ne voit l'**agent** — son identité dans le temps, ce qu'il a atteint à
travers votre plan de données, qui a approuvé une action risquée, et si quoi que ce
soit de tout cela peut être prouvé plus tard. C'est la lacune qu'Olivares comble.

| Lacune laissée par la passerelle / les Guardrails | Pourquoi cela compte | Ce qu'Olivares AI fournit |
|---|---|---|
| **Application au runtime de l'agent** | Une passerelle applique à la *frontière de la requête* ; elle ne peut pas arrêter un appel d'outil Claude Code local qui ne la traverse jamais | Un [PEP in-process](/fr/how-to/connectors/claude-code-hooks-pep/) deny-closed au niveau de l'agent : gate d'identité ferme, disposition de politique, surcouche de politique en direct, le tout avant l'exécution de l'outil |
| **Preuve à altération détectable** | La passerelle et les Guardrails émettent des *logs* — des enregistrements de requête mutables ; un auditeur veut une preuve immuable | Registre en ajout seul, à chaînage de hachage, [signé Ed25519](/fr/reference/glossary/#audit-ledger), vérifiable hors machine, exportable en preuves OSCAL |
| **Cycle de vie d'identité non humaine** | La « clé virtuelle » d'une passerelle est un seau de budget, pas une identité qui est provisionnée, attribuée, tournée et offboardée | [Cycle de vie NHI](/fr/reference/glossary/#identity--nhi) : péremption → blocage, cascade d'offboarding, double contrôle sur la rotation, liée à la carte d'accès |
| **Intervention sur session en direct** | Les logs et budgets sont a posteriori ; aucun de ces outils étudiés n'arrête une session en plein vol | [Approbations HITL](/fr/reference/glossary/#approbation-hitl), [break-glass](/fr/reference/glossary/#break-glass), et un [kill switch](/fr/reference/glossary/#kill-switch) qui refuse toute actuation gouvernée jusqu'à une réactivation à double contrôle |
| **Vérité terrain à travers le parc** | Une passerelle ne voit que les appels qui la traversent ; les agents touchent aussi des BDD, stockages objet, MCP, fichiers directement | La [carte d'accès R/RW](/fr/explanation/#laccess-map--read-first-minimal-data-permitted-vs-observed) read-first et la dérive Permis-vs-Observé, corroborées face à l'audit natif |
| **Souveraineté** | Les passerelles SaaS et les Guardrails cloud traitent ce trafic dans leur cloud | Auto-hébergé / air-gapped ; le plan de données ne quitte jamais votre périmètre |

Aucune de ces choses n'est une fonctionnalité de routage. C'est tout l'enjeu : la
lacune n'est pas un *meilleur routage*, c'est **la gouvernance que le chemin de la
requête n'a jamais été conçu pour fournir.**

## Sur les Guardrails en particulier : la sûreté du contenu est un hook, pas un concurrent

Les Bedrock Guardrails peuvent être appliqués de deux façons — inline pendant un
appel d'inférence Bedrock, ou *"directly through the `ApplyGuardrail` API without
invoking the foundation models"*, ce qui fonctionne *"with any foundation model
whether hosted on Amazon Bedrock or self-hosted models"*
([AWS](https://aws.amazon.com/bedrock/guardrails/)). C'est véritablement utile, et
Olivares traite la sûreté du contenu comme un **détecteur que vous branchez**,
jamais un mur que nous vous demanderions de choisir *à la place* des Guardrails.
Deux faits honnêtes et distincts :

- Le proxy d'inférence inline expose une **couture d'inspection de contenu** — un
  point enfichable où un détecteur de contenu / DLP renvoie un verdict sur lequel
  le décideur deny-closed agit. La sûreté du contenu a sa place *là*, dans le
  pipeline, plutôt que d'être réimplémentée comme un filtre concurrent.
- Olivares lit les **propres décisions** de vos Guardrails, en read-first. Le
  connecteur AWS ingère les décisions des guardrails Bedrock depuis leurs logs
  CloudWatch / S3 comme posture et preuve ; il **n'appelle** délibérément **pas**
  le runtime payant `ApplyGuardrail` lui-même. Vos verdicts de contenu deviennent
  partie intégrante de l'enregistrement à altération détectable.

Ainsi la sûreté du contenu se compose avec ce que vous exploitez déjà. Ce que les
Guardrails **ne** documentent **pas** — et là où la lacune de gouvernance reste
ouverte — c'est le reste de la vie de l'agent : les pages Bedrock ne documentent
aucune identité d'agent, aucune gestion de session, aucune approbation humaine, et
aucune gouvernance de coût (non documenté sur ces pages, vérifié le 2026-06-21).
Olivares est exactement ce complément : il porte l'identité, les contrôles de
session, les approbations et la preuve ; le filtre de contenu reste là où il vit
déjà.

## Comment ils se composent

Un arrangement sain garde chaque outil dans son couloir :

- **Gardez votre passerelle** (LiteLLM / Portkey / Kong / Cloudflare) comme plan
  d'appel de modèle — routage, cache, clés virtuelles, budgets sur la requête.
- **Gardez vos Guardrails** (Bedrock / Azure Content Safety) comme votre détecteur
  de sûreté du contenu — le PEP Olivares exécute un détecteur enfichable à sa
  couture d'inspection de contenu et lit les propres décisions de vos Guardrails en
  read-first comme preuve ; il n'invoque pas `ApplyGuardrail` lui-même.
- **Ajoutez Olivares à côté d'eux** comme plan de gouvernance et de preuve : le PEP
  in-process sur les agents qui n'atteignent jamais votre passerelle, la carte
  d'accès à travers tout le parc, le registre à altération détectable, et les contrôles en
  direct HITL/break-glass/kill.

Le seul endroit où Olivares touche bel et bien l'inférence est étroit et explicite
— une voie de passerelle **par clé d'API seulement** pour les appelants SDK/`curl`
bruts, décrite dans
[Gouverner les agents authentifiés par abonnement](/fr/explanation/positioning/governing-subscription-authed-agents/).
Elle existe pour gouverner le trafic que vos autres outils ne peuvent atteindre,
jamais pour les concurrencer sur le routage, et elle ne porte **jamais** un
identifiant d'abonnement.

## Quand votre passerelle suffit

L'honnêteté tranche dans les deux sens. Si vos agents n'appellent jamais les
modèles que **par** votre passerelle, que vos besoins de sûreté du contenu sont
satisfaits par les Guardrails, que vous n'avez **aucun agent auto-hébergé ou
résident sur portable** atteignant des bases de données / stockages objet / MCP
directement, et que vous n'avez **aucune exigence de souveraineté ou de preuve
à altération détectable** — alors votre passerelle plus ses logs et les Guardrails peuvent
être tout ce dont vous avez besoin, et vous ne devriez pas ajouter un control plane
pour le plaisir.

Olivares gagne sa place quand les questions deviennent *à l'échelle du parc et
adversariales* : quels agents existent et ce que chacun a réellement atteint,
puis-je arrêter une mauvaise action en deny-closed **au niveau de l'agent**, qui a
approuvé celle qui était risquée, et puis-je remettre à un auditeur une **preuve
immuable** — le tout sans envoyer cette image au cloud de quelqu'un d'autre. Pour
le traitement plus approfondi de deux comparaisons adjacentes, voir
[face aux tours de contrôle IA](/fr/explanation/positioning/vs-control-towers/) et
[face aux passerelles LLM & à l'observabilité](/fr/explanation/positioning/vs-llm-observability/).
