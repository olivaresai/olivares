> 机器翻译。英文版本为权威来源。

# ADR-0027: 托管云入口——collector mTLS 使用 L4 直通，control-plane API 使用 L7

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

## 背景与问题陈述

ADR-0012 固定了 ingestion topology：collector 运行在客户基础设施上，通过带 mutual TLS 的 gRPC
**推送** observation，并由 core **自行终止该 mTLS**。

有必要精确说明这带来了什么，因为这句话的宽泛版本是错误的；一旦相信它，它就会成为关键前提。collector
plane 的 admission 依赖**两个彼此独立的 factor**：

1. **transport gate。** server 要求并验证一份 client certificate，其证书链可追溯到已配置的 collector
   CA。这证明 client 持有一把 key，且我们为该 key 签发了 certificate；该 certificate 不会被解析成
   subject，也不为任何 principal 命名。
2. **bearer principal。** authorization 与 audit chain（ADR-0009）据以行动的 authenticated identity
   来自 request 的 bearer token，而不是 certificate。

两者都在**产品自身的 process 内** enforce。中间不存在任何组件为任一 factor 背书。这正是本记录所关心的
属性：不是“certificate 就是 identity”，而是“任何 intermediary 都不为任一 factor 背书”。

托管云是第一个在该 binary 前放置负载均衡器的 deployment。同一 deployment 还会暴露常规的公共 HTTPS
surface——REST API、console、admin——而它需要相反的处理：托管的公共证书、Web Application Firewall、
host/path routing。单一 ingress 若不在其中一侧有所牺牲，就无法同时服务这两个 surface。

## 决策驱动因素

- 两个 admission factor 都必须继续由**产品本身终止的 TLS session** enforce。托管云若悄然将其中
  任一个降级为“intermediary 告诉我们它没有问题”，就会削弱本产品的核心主张。
- 公共 HTTP surface 应能使用 L7 提供的 edge protection，而无需产品重新实现这些保护。
- 长生命周期的 collector stream 必须能经受 ingress 的 idle behaviour。
- self-hosted deployment 不得发生回归：使用同一条 code path，而不是两条。

## 考虑过的选项

- **A——所有流量共用一个 L4 负载均衡器。** 两个 plane 都使用 TCP passthrough；由 binary 终止每一条
  TLS session，包括公共 API 的 session。
- **B——拆分 ingress。** collector plane 使用带 TCP listener、执行 passthrough 的 **network (L4)
  load balancer**，control-plane HTTP surface 则使用 **application (L7) load balancer**。
- **C——一个带托管 mutual TLS 的 L7 负载均衡器。** application load balancer 自行认证 client
  certificate（使用 trust store 和 revocation list 的 verify mode），或将证书链作为 HTTP header
  转发给 target。

## 决策结果

选定方案：**B——拆分 ingress**。

### 后果

- **好处：** collector plane 与 self-hosted path 逐字节完全相同。TCP listener 不终止 TLS，因此由
  binary 执行 handshake，并像 on-premises 环境中一样自行 enforce certificate requirement。
  authorizer 中没有 cloud-specific branch，audit chain 中也没有 cloud-specific case。
- **好处：** 公共 surface 可以使用托管证书、host/path routing 与 web application firewall，而产品无需
  重新实现其中任何一项。该 firewall 是**单独计费**的 service，并非 L7 load balancer 免费附带的属性；
  此处只是将其列为可用，而不是列为已包含。
- **好处，并准确说明其适用范围：** TCP listener 的 idle timeout **可在 60 到 6000 秒之间配置**
  （`tcp.idle_timeout.seconds`，默认值为 **350**）；TLS listener 的 idle timeout **固定为 350 秒，
  无法修改**。这是针对无 byte 传输的 **idle** timeout，**不是 stream duration 的上限**：持续发送 data
  或 keepalive frame 的 stream 不会在 350 秒时被切断。因此，passthrough 并不是“让长 stream 成为可能”，
  而是让我们可以设定 idle budget。反过来说才是重要之处：**无流量的 stream 会在这些 ingress 中的任意一个
  上死亡**，client 必须能够承受这一点。
- **坏处，也是上一点以警告方式表述的原因：** collector client **没有配置任何 gRPC keepalive**
  （library 默认将其关闭），而且 send 失败后会继续 cache 已死亡的 stream，而不是重建它。因此，超过已配置
  timeout 的 idle period、leader 变更或 deployment 都会终止 collector stream，且没有任何机制将其重连。
  这**不是由 split 造成的**，而是早已存在的问题；但 split 是第一个会有 intermediary 主动关闭 idle
  connection 的 deployment，因此也正是这个缺口开始造成 data 损失之处。collector 端的
  reconnect-with-backoff loop 是将该 ingress 称为 production-ready 的**前提条件**。
- **坏处 / 权衡：** 两个负载均衡器意味着两笔按小时计费和两套独立的 capacity-unit meter，两者合计会
  主导小规模 deployment 的固定月度成本下限。这是为把两个 admission factor 都保留在 process 内而支付的
  一项真实且持续的成本。
- **坏处，而且是构建要求而非脚注：** 对于使用 TCP 或 TLS protocol 的 **IP-type target group，
  client IP preservation 默认处于禁用状态**——而托管 container runtime 上的 task 正是 IP target。
  如果保留默认值，每条 collector connection 到达 binary 时，其 source 都是负载均衡器的 private
  address。任何从 address 派生的内容——audit record、rate limit、address allow-list——从第一天起就会
  在无声中出错。只有启用 `preserve_client_ip.enabled`，或由 binary 在 handshake 前解析
  Proxy Protocol v2，ingress 才算完整。启用 preservation 还意味着 target 的 security group 面对的是 client
  address，而不是负载均衡器的 address；network design 必须对此作出安排。
- **中性 / 后续：** implementation phase 决定使用上述哪一种机制来恢复 source address，但**必须作出
  选择并进行测试，不得沿用默认值**。验收标准是一项断言：记录的 source address 等于 collector 的
  source address。

## 为何否决了其他选项

- **A（一个 L4 负载均衡器）**——针对*公共* plane 被否决，而不是针对 collector plane。它更便宜，
  也最接近 self-hosted topology，但 control-plane API 将失去托管证书、WAF 与 host/path routing，
  产品最终还要在 L7 重新实现 edge 已经提供的功能。选项 B 保留的正是选项 A 的 collector 部分。
- **C（L7 的托管 mutual TLS）**——被否决，因为它**移动了 trust boundary**。在 verify mode 中，
  edge 执行 certificate check，而 application 收到的是一份已经由 edge 担保的 request；在 passthrough
  mode 中，certificate chain 作为 `X-Amzn-Mtls-Clientcert` header 到达。在两种情况下，transport gate
  都不再由产品 enforce，而变成其他组件作出的 assertion——这正是本产品要使其可验证的那种精确替换；并且
  只要 network configuration 出现一次错误，任何能直接访问 target 的组件就都能伪造该 header。带
  revocation list 的托管 trust store 确实具有 operational advantage，但产品目前对 collector
  certificate 完全不具备这种能力：它只加载 CA 并执行普通的 X.509 validation，不检查 CRL 或 OCSP。如果
  managed revocation 有朝一日比 first-hand termination 更重要，那将需要一份**新的记录**，而不是对本
  记录的修订。
