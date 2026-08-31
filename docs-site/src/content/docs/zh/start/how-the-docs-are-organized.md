---
title: 本文档如何组织
description: >-
  这些文档遵循 Diátaxis —— 四种模式（教程、操作指南、参考、解释），各自回应
  一种不同的需求。下面介绍如何浏览它们。
---

本文档采用 **[Diátaxis](https://diataxis.fr/start-here/)**
框架组织。Diátaxis 观察到技术文档服务于四种不同的
需求，而将它们混在一起会让文档对所有人都更糟。因此侧边栏的
顶部是**四种模式**，而不是产品功能的清单：

| 模式 | 取向 | 回答 | 当你处于…… |
|---|---|---|---|
| **[教程](/zh/tutorials/zero-to-graph/)** | 学习 | "带我从零到达一个可用的结果。" | 新手，想通过动手学习 |
| **[操作指南](/zh/how-to/self-hosting/)** | 任务 | "我该如何完成*这件具体的事*？" | 正在工作，需要一份配方 |
| **[参考](/zh/reference/)** | 信息 | "API、事件、模块、标志究竟是什么？" | 基于它构建，需要精确性 |
| **[解释](/zh/explanation/)** | 理解 | "*为什么*要这样构建？" | 在评估，想了解其中的推理 |

各部分位置的快速地图：

- **教程** —— 学习路径：[从零到一个读/写访问
  图谱](/zh/tutorials/zero-to-graph/)，以及按真实场景的入门 ——
  [单节点](/zh/tutorials/getting-started/single-node/)、
  [Docker Compose](/zh/tutorials/getting-started/docker-compose/)、
  [Kubernetes](/zh/tutorials/getting-started/kubernetes/)、
  [气隙](/zh/tutorials/getting-started/air-gapped/)。
- **操作指南** —— 安装与运维（[自托管](/zh/how-to/self-hosting/)、
  [备份与恢复](/zh/how-to/backup-and-restore/)、
  [监控](/zh/how-to/monitor-with-prometheus/)、
  [故障排查](/zh/how-to/troubleshooting/)）、
  [按连接器的指南](/zh/how-to/connectors/pgaudit/)（pgAudit、CloudTrail、
  eBPF、Claude Code、MCP、身份），以及
  治理配方[手册](/zh/how-to/cookbook/deny-closed-policies/)
  （deny-closed 策略、预算、审批、漂移分诊、kill switch、
  SIEM 推送）。
- **参考** —— [REST API](/reference/api/)（从产品自身的
  OpenAPI 3.1 契约渲染）、[API 稳定性策略](/zh/reference/api-stability/)、
  [事件总线](/zh/reference/events/)（一份 AsyncAPI 3.0
  契约）、[模块目录](/zh/reference/modules/overview/)、
  [CLI](/zh/reference/cli/)和[配置](/zh/reference/configuration/)。
- **解释** —— [架构](/zh/explanation/architecture/overview/)、
  [安全模型](/zh/explanation/security/security-model/)和
  [威胁模型](/zh/explanation/security/threat-model/)、
  [开放核心许可](/zh/explanation/open-core-and-licensing/)。

## 约定

- **搜索**是本地的、客户端的（Pagefind）。它完全在你的浏览器中运行；
  不会向外部搜索服务发送任何内容 —— 这与产品的自托管设计一致，
  在该设计中由你决定什么会跨越你的边界。
- **带版本。** 文档是带版本的：当一个新产品版本发布时，
  上一个版本的文档会被保留。版本选择器位于
  顶栏中。
- **对边界诚实。** 凡是某项能力处于设计阶段、v1 之后，或干脆
  尚未构建，文档都会直白说明。参见
  [诚实与边界](/zh/start/honesty-and-limits/)。教程和操作指南中的命令
  都应**按所写内容原样运行**。
- **语言。** 规范文档为英文；提供西班牙语、简体中文、俄语、日语、德语和法语的
  译文（机器翻译，以英文为权威版本，对于尚未翻译的页面会回退到英文）。
