---
title: "使用 Prometheus 监控（SLO、指标、告警）"
description: >-
  抓取引擎的 /metrics，采用已发布的 SLO 目标，并加载随附的燃尽率告警规则——
  这与产品自身运行手册所依据的 SLI 完全一致，并诚实地给出单写入者的数值。
---

引擎在 HTTP 监听器上暴露三个运维端点，全部对探针友好：

| 端点 | 认证 | 用途 |
|---|---|---|
| `/livez` | 无 | 进程存活探测——**不做依赖检查**，因此存储中断绝不会导致重启循环 |
| `/readyz` | 无 | 就绪探测——存储 ping（以及 HA 主导权）：`200 {"status":"ok","store":"up","leader":true,…}`、`503 {"status":"unavailable","store":"down"}`，或在 HA 备节点上返回 `503 {"status":"standby",…,"leader":false}` |
| `/metrics` | 无 | Prometheus 暴露格式。有意不做认证：它携带的是运维序列，绝不包含租户数据 |

`/readyz` 的可达性**就是**可用性 SLI。

## 真正重要的指标集

所有序列均由引擎注册（已对照当前代码核实）；其中起承重作用的：

| 序列 | 它告诉你什么 |
|---|---|
| `olivares_store_up` | 存储是否响应 ping——每个运行手册首先检查的项 |
| `olivares_http_requests_total{code}` | 请求成功率 SLI（`code!~"5.."`） |
| `olivares_http_request_duration_seconds` | API 延迟（下文给出 p99 目标） |
| `olivares_ingest_duration_seconds` | **背压 SLI**——当某个订阅者饱和时，摄取 p99 会上升 |
| `olivares_ingest_observations_total` / `olivares_ingest_rejected_total` | 摄取吞吐量与拒绝量 |
| `olivares_eventbus_queue_depth` / `_queue_capacity`（按订阅者） | 哪个模块是慢消费者 |
| `olivares_eventbus_publish_blocked_total` | 背压事件（总线会阻塞；它不会丢弃） |
| `olivares_eventbus_bridge_*` | 启用分布式总线时的 NATS 桥接健康状况——`_connected`、`_pending_messages`、`_dropped_total`（跨节点投递为至多一次；丢弃会被计数，绝不静默） |
| `olivares_audit_checkpoint_age_seconds` | 篡改检测证据的新鲜度——当其超过检查点间隔的 2 倍时告警 |
| `olivares_auth_login_attempts_total{outcome}` | 登录成功 / 失败 / 锁定 |
| `olivares_http_ratelimit_decisions_total{decision}` | 限流压力 |
| `olivares_grpc_requests_total` / `olivares_grpc_request_duration_seconds` | collector→core 的摄取平面 |

## SLO 目标（已发布，诚实）

单节点目标——默认拓扑实际所能支撑的——以及 HA 层级：

| SLI | 单节点 | HA 层级（Postgres） |
|---|---|---|
| 可用性（`/readyz`） | **99.5% / 28 天** | 99.9% / 28 天 |
| 请求成功率（非 5xx） | **99.9%** | 99.95% |
| API 延迟 p99 | **< 300 ms** | < 200 ms |
| 摄取延迟 p99 | **< 250 ms** | < 150 ms |
| 摄取成功率 | **99.9%** | 99.95% |

数值中的诚实之处：单节点上的单个写入者无法承诺三个九的可用性，所以文档不这么承诺——
99.5%（每 28 天约 3 小时 39 分的预算）是单节点的真相，而 99.9% 这一层级是靠
[HA 拓扑](/zh/tutorials/getting-started/kubernetes/#3-active-passive-高可用) 挣来的，
而非靠乐观假设。

## 加载随附的告警规则

`deploy/monitoring/olivares-slo.rules.yaml` 随附 14 条可直接用于你的 Prometheus 的告警：
针对请求成功率预算的多窗口燃尽率告警（快速 14.4× 触发 page / 中速 6× 触发 page /
慢速 1× 触发工单）、绝对延迟与可用性触发
（`OlivaresIngestP99High`、`OlivaresApiLatencyP99High`、`OlivaresStoreDown`、
`OlivaresControlPlaneUnscrapeable`）、饱和度
（`OlivaresEventBusSaturated`，队列 >90% 持续 10 分钟）、桥接健康
（`OlivaresEventBusBridgeDropping`、`OlivaresEventBusBridgeDisconnected`），以及
账本新鲜度（`OlivaresAuditCheckpointStale`，age > 2h）。

```yaml
# prometheus.yml
rule_files:
  - olivares-slo.rules.yaml
scrape_configs:
  - job_name: olivares
    scheme: https
    tls_config: { insecure_skip_verify: true }   # or pin the real cert
    static_configs: [{ targets: ["olivares.internal:8443"] }]
```

在 Kubernetes 上，chart 的 `ServiceMonitor` 选项会为 Prometheus operator 接好抓取。
一份用于外部 `/readyz` 探测的 Gatus 状态页配置与这些规则一同随附
（`deploy/monitoring/status-page.gatus.yaml`）。

## 当告警触发时

逐症状的诊断——存储宕机、摄取 p99 偏高、总线饱和、检查点陈旧——见
[故障排查页面](/zh/how-to/troubleshooting/)，它由告警注解所引用的同一批运行手册提炼而成。
