---
title: "模块 XXIII — 模型运营"
description: >-
  对你所拥有的模型（托管、微调或导入）进行治理的 registry，包含签名模型准入
  与本地推理 deployment。它治理自有模型的供应链，但不训练、不提供推理服务，也不
  对模型做 benchmark。
---

模块 XXIII 是模型栈中负责**自有模型**的一侧。模块 X（models & providers）治理
你所消费模型的*参考 catalog*和*路由*；本模块则治理你**拥有并运营**的模型：
一份有版本的 registry、决定哪些版本可被部署的签名模型**准入** gate，以及提供这些
模型的本地**推理 deployment**。它负责追踪和治理；绝不训练模型、运行微调 job 或
自行执行推理。

本模块在控制台中的 surface 是 Intelligence 组的 **Model Operations**，包含自有
模型、dataset、微调 job、准入、deployment 和 AIBOM seal ledger 等 tab。供应商
GPAI posture（按 provider）位于 **Models & providers**，agent 供应链则有独立视图；
两者都是按 provider 或整个 estate 关注，而不是按单个自有模型关注。

## 它是什么

三个相互协作的 surface，全部 deny-closed 且受审计：

- **自有模型与版本。** 你所拥有模型（`hosted`、`fine_tuned`、`imported`）的
  registry；每个模型都有指向一个 artifact 的不可变**版本**。先记录版本，再准入其
  已签名 artifact；版本行本身绝不改变。
- **准入。** 每 tenant 一份**信任策略**和已记录的**判定**历史。策略指定信任锚：
  CA root 和/或 public key，加上可选的 Sigstore identity 与 issuer。签名**方法由
  配置推导**（`sigstore-keyless`、`certificate-pki` 或 `bare-key`）；空策略不准入
  任何内容。准入版本时，会针对策略验证签名 **bundle** 并记录判定。验证失败的判定
  也会如实记录，而不是隐藏。
- **Deployment。** 本地推理 deployment（vLLM、Ollama、llama.cpp 等）。tenant
  **强制要求**签名模型时，创建或更新引用某版本的 deployment 会重新检查准入：
  如果该版本没有已验证判定，或当初准入它的信任 root 已不在策略中，deployment
  会被拒绝。

## Lineage 与证据

- **Dataset。** 最小数据的 lineage component：名称、可选的内容引用与 hash、
  classification 和 governance label，**绝不包含 dataset 内容**。dataset 作用于
  整个 tenant；其可选模型引用是一个 lineage pointer，并以 deny-closed 方式验证。
  `verified` 是运营者对来源的**声明**，绝不是密码学结果；控制台也会如此标示。
- **微调 job。** 记录在外部运行的微调工作，以及每项工作产出的模型**版本**。
  control plane 绝不启动、取消或执行训练，也不存储权重或 dataset 内容；这些是
  inventory record，而不是训练 launcher。
- **AIBOM 与模型卡。** 可以从自有模型**生成**实时 CycloneDX AIBOM（或
  SPDX 3.0.1 serialization）和模型卡（JSON 或 Markdown），全部只读。生成的文档在
  **seal** 之前不是证据：seal 会把 canonical content-hash commitment 锚定到审计
  台账（始终使用 CycloneDX；SPDX 绝不能 seal）。台账只存 hash，因此 seal receipt
  是保存 sealed 文档的唯一机会。跨模型的 **AIBOM seals** tab 是这些 commitment
  持久、仅追加的台账。

## 它强制什么

开启 `require_signed` 时，引用模型版本的 deployment **只有在**该版本具有已验证的
准入判定，且其锚定信任 root 仍在配置中时，才会获准。把某 root 从策略中轮换出去，
会追溯性地拒绝今后为仅由该 root 准入的版本创建或更新 deployment；必须先在当前
信任锚下对这些版本**重新准入**。这就是引擎在每次判定的 `signer_roots` 中记录的
同一 anchor pin，
界面会将其展示出来，让运营者准确看到是哪个 root 为某版本作保。

## 它不是什么

- 它**不运行**训练或微调 job，只为 lineage 记录其状态。
- 它**不提供**推理服务，只治理提供推理服务的 deployment record。
- 它**不根据**已存判定决定“当前可部署”。只有引擎在部署时的重新检查才具有权威性，
  因此控制台绝不会仅凭历史就把某版本标为 trusted 或 deployable。

## Agent 供应链

独立的 **Agent Artifacts** 控制台视图登记整个 tenant estate 的四类 artifact：
Agent Skills、`.mcpb` extension、MCP App `ui://` template 和 `AGENTS.md` instruction
file。registry 存储 identity、provenance、内容 fingerprint 和 posture metadata，
绝不存储 skill body、manifest 或 instruction text。posture grade 是 connector
scanner 或运营者**记录的扫描结果**，不是控制台执行的扫描；缺少 grade 时会中性地
显示为未扫描。

它的 CycloneDX 1.6 agent-supply-chain BOM 与单模型 lineage AIBOM 不同。seal 会把
canonical content-hash commitment 追加到独立的 `models.agent_aibom` 台账，而返回的
receipt 仍是 sealed 文档的唯一副本。coverage 只包括已登记内容：从未登记的 artifact
不会被表示。
