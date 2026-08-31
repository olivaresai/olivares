> 機械翻訳です。正式な情報源は英語版です。

# ADR-0029: マネージドクラウドの region — primary region は 1 つ、residency への回答は self-hosting

- **Status:** accepted (managed cloud; this record creates no infrastructure)
- **Date:** 2026-08-02
- **Deciders:** Fran Olivares
- **References:** ADR-0027 (managed-cloud ingress), ADR-0028 (managed-cloud database),
  ADR-0020 (enterprise private-repo distribution), ADR-0024 (DDIL offline semantics and
  signed bundles); the platform decision record for the managed cloud.

## 背景と課題

2 つの問いには一緒に答えなければならない。一方への答えが悪ければ、もう一方にも悪い答えを強いるからである。
すなわち、**managed plane をどこで稼働させるのか**、そして **顧客から data の所在を尋ねられたときに何と答える
のか**である。

2 番目の問いへの回答を容易にする region、すなわち compliance section で見栄えのする jurisdiction の region を
選び、それが実際の顧客にもたらす latency を受け入れたくなる。しかし、それは順序が逆である。また、そこには、
誰も再び同じ推論をしないよう durable な場所に一度記録しておくべき誤解がある。**byte の保存場所によって、どの
data-protection law が適用されるかが決まるわけではない。** ある jurisdiction の data subject に service を提供すれば、
hosting location にかかわらず、その jurisdiction の law も適用される。

## 意思決定の要因

- 製品を実際に販売する顧客までの latency。
- enterprise buyer が求める compliance evidence。これは主に **infrastructure provider** に関する evidence であり、
  region に関するものではない。
- 顧客から要求される前に、2 番目の region の固定費と、cross-region data handling の永続的な複雑さを負担しないこと。
- 厳格な residency requirement を持つ顧客に対して、正直で回避的でない回答を用意すること。

## 検討した選択肢

- **A — target market に 1 つの primary region。** 2 番目の region は demand-gated project とする。
- **B — launch 時から 2 つの region。** major market ごとに 1 つずつ置く。
- **C — customer latency ではなく regulatory narrative のために選ぶ primary region。**

## 決定の結果

選択した選択肢は **A — target market（United States East）に置く 1 つの primary region** である。2 番目の
region は、料金を支払う requirement によって開始される project であり、launch item ではない。tenant ごとの region
pinning と cross-region replication は、最初の managed release の scope から意図的に除外する。

primary region では満たせない **契約上または規制上の residency requirement** を持つ顧客には、**self-hosted
edition** を提供する。これは製品の primary shape であり、顧客自身の infrastructure 上で稼働し、residency の問いに
部分的ではなく完全に答える。これは workaround ではなく、より強い回答であり、初日から利用できる。

### 帰結

- **良い点:** deployment は 1 region、1 database となり、検討すべき failure domain も 1 つになる。latency budget は
  顧客のいる場所に使われる。
- **良い点:** residency への回答は、roadmap 上の約束ではなく self-host という、正直で即時のものになる。
- **悪い点 / トレードオフ:** *managed* と US 外の residency の **両方**を求める顧客には、2 番目の region が
  存在するまで service を提供できない。これは既知の、受け入れ済みの gap であり、取り繕わず sales material に
  明記すべきである。
- **悪い点:** 1 つの region は、単一の regional failure domain である。multi-AZ（ADR-0028）が cover するのは
  availability zone の喪失であり、region の喪失では **ない**。regional outage 時の recovery は、backup から別の
  場所へ restore することであり、recovery time は時間単位になる。また、誰かにその時間を提示する前に、必ず
  **rehearse** しなければならない。
- **中立、かつこれを記録する理由:** US の primary region を選ぶと、US 外の data subject の personal data は
  **transfer される**ため、有効な transfer mechanism と、infrastructure provider を sub-processor として明記した
  processing agreement が必要になる。この record は、そのどちらも作成しない。この record が記録するのは、
  **region の選択によってその義務がなくなるわけではない**ということである。これにより、将来の読者が「region X で
  host している」ことを compliance answer と取り違えないようにする。これは engineering record であり、legal advice
  ではない。instrument 自体は compliance track に属する。

## 代替案を却下した理由

- **B（launch 時から 2 つの region）** — まだ存在しない顧客のために、恒久的に 2 倍の料金を支払うことになるため
  却下した。2 番目の region は固定 infrastructure cost を 2 倍にし、決して消えない種類の課題を加える。どの region が
  tenant を所有するのか、両 region 間で何を移動させるのか、そして platform 単位ではなく tenant 単位で residency
  claim をどう証明するのか、という課題である。署名済みの requirement によって費用が賄われるなら、すべて取り組む
  価値がある。
- **C（regulatory narrative のために選ぶ region）** — 1 つの paragraph を得る代わりに、すべての request で
  代償を払うため却下した。さらに、見かけどおりの効果もない。前述のとおり、hosting location は applicable law を
  決定しないため、narrative は聞こえるほど強くない一方、latency cost は聞こえるとおりに大きい。
