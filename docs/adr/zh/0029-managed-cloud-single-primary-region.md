> 机器翻译。英文版本为权威来源。

# ADR-0029: 托管云区域——单一主区域，数据驻留要求由 self-hosting 满足

- **Status:** accepted (managed cloud; this record creates no infrastructure)
- **Date:** 2026-08-02
- **Deciders:** Fran Olivares
- **References:** ADR-0027 (managed-cloud ingress), ADR-0028 (managed-cloud database),
  ADR-0020 (enterprise private-repo distribution), ADR-0024 (DDIL offline semantics and
  signed bundles); the platform decision record for the managed cloud.

## 背景与问题陈述

两个问题必须一起回答，因为若其中一个回答不当，就会迫使另一个也得到糟糕的答案：**managed plane 在哪里
运行**，以及**如何回答询问其数据存放地点的 customer**。

一种诱惑是选择能让第二个问题易于回答的 region——其 jurisdiction 在 compliance section 中显得合适——
并接受它给实际 customer 带来的任何 latency。这种先后顺序是错误的。它还建立在一个值得在此永久写明、
以免任何人再次错误推导的误解之上：**byte 的存储地点并不能决定适用哪一部 data-protection law。**
为某个 jurisdiction 的 data subject 提供服务，就会随之适用该 jurisdiction 的法律，无论 hosting
location 在哪里。

## 决策驱动因素

- 面向产品实际销售对象的 latency。
- Enterprise buyer 要求的 compliance evidence；这在很大程度上是关于**infrastructure provider** 的
  evidence，而不是关于 region 的 evidence。
- 在 customer 提出要求之前，不承担第二个 region 的固定成本，也不引入 cross-region data handling 的
  永久复杂性。
- 对有严格 residency requirement 的 customer，能够给出真实且不回避问题的回答。

## 考虑过的选项

- **A——目标市场中的单一主区域**，将第二个 region 作为由需求触发的 project。
- **B——launch 时使用两个 region**，每个主要市场一个。
- **C——为了 regulatory narrative 而不是 customer latency 选择主区域。**

## 决策结果

选定方案：**A——位于目标市场（美国东部）的单一主区域**。第二个 region 是由付费需求启动的 project，
而不是 launch item。第一个 managed release 有意将 per-tenant region pinning 与 cross-region
replication 排除在 scope 之外。

对于具有**主区域无法满足的 contractual 或 regulatory residency requirement** 的 customer，使用
**self-hosted edition** 提供服务——这是产品的主要形态，运行在 customer 自有的 infrastructure 中，
能够完整而不是部分地回答 residency 问题。这不是 workaround；它是更有力的答案，并且从第一天起即可用。

### 后果

- **好处：** deployment 只有一个 region、一个 database、一个需要分析的 failure domain，latency
  budget 花在 customer 所在之处。
- **好处：** residency 答案真实且立即可用——self-host——而不是 roadmap promise。
- **坏处 / 权衡：** 在第二个 region 存在之前，无法服务既要求 *managed*、**又**要求非美国 residency 的
  customer。这是一个已知且接受的 gap，应在 sales material 中明确说明，而不是加以掩饰。
- **坏处：** 单一 region 就是单一 regional failure domain。multi-AZ（ADR-0028）可覆盖一个
  availability zone 的丢失，**但不能**覆盖整个 region 的丢失。regional outage 的 recovery 方案是从
  backup 恢复到其他地点，recovery time 以小时计；在向任何人引用这一时间之前，必须先对其进行**演练**。
- **中性，也是写下本记录的要点：** 选择美国主区域意味着非美国 data subject 的 personal data 会被
  **传输**，因此需要有效的 transfer mechanism，以及一份将 infrastructure provider 列为
  sub-processor 的 processing agreement。本记录**不会创建其中任何一项**。它所记录的是：
  **region 的选择并不会消除这项义务**——从而避免未来任何 reader 将“我们托管在 region X”误认为一份
  compliance 答案。这是一份 engineering record，不是 legal advice；这些 instrument 本身属于
  compliance track。

## 为何否决了其他选项

- **B（launch 时使用两个 region）**——被否决，因为这等于为一个尚不存在的 customer 永久支付双倍成本。
  第二个 region 会使固定 infrastructure 成本下限翻倍，并增加一类永远不会消失的问题：哪个 region
  拥有某个 tenant、哪些内容会在两者之间跨越，以及如何按 tenant 而不是按 platform 证明 residency
  claim。当一项已签署的 requirement 为此提供资金时，所有这些工作都值得完成。
- **C（为了 regulatory narrative 选择 region）**——被否决，因为它买来一个 paragraph，却让每一个
  request 为此付出代价。它也无法兑现表面上的效果：如上所述，hosting location **并不能决定适用的
  law**，因此 narrative 实际上会比听起来更弱，而 latency cost 则会与听起来完全一样高。
