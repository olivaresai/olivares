> 機械翻訳です。正式な情報源は英語版です。

# ADR-0027: マネージドクラウドの ingress — collector mTLS には L4 passthrough、control-plane API には L7

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

## 背景と課題

ADR-0012 は ingestion topology を定めた。collector は顧客の infrastructure 上で稼働し、相互 TLS を使って
gRPC 経由で観測結果を **push** し、core **自身が**その mTLS を terminate する。

これによって何が得られるのかは正確に述べる必要がある。この文を曖昧に言い換えたものは誤りであり、信じれば
重要な前提になってしまうからである。collector plane への admission は、**互いに独立した 2 つの factor** に
基づく。

1. **transport gate。** server は、設定済みの collector CA まで chain する client certificate を要求して
   verify する。これは、我々が certificate を発行した key の所持を証明する。その certificate は subject として
   parse されず、principal を指定しない。
2. **bearer principal。** authorization と audit chain（ADR-0009）が対象とする authenticated identity は、
   certificate ではなく request の bearer token から得られる。

どちらも**製品自身の process 内で** enforce される。途中の intermediary は、どちらの factor についても保証しない。
この record が扱うのはこの性質である。「certificate が identity である」のではなく、「どの intermediary も、
どちらの factor についても保証しない」ということである。

マネージドクラウドは、その binary の前段に load balancer を置く最初の deployment である。同じ deployment は、
通常の public HTTPS surface — REST API、console、admin — も公開するが、こちらには正反対の扱い、すなわち managed
public certificate、web application firewall、host/path routing が必要である。単一の ingress では、どちらか一方を
犠牲にせずに両方を提供することはできない。

## 意思決定の要因

- 2 つの admission factor はどちらも、引き続き**製品自体が terminate する TLS session によって** enforce
  されなければならない。どちらか一方でも「intermediary が問題ないと伝えた」へひそかに downgrade する
  マネージドクラウドは、製品の中心的な主張を弱める。
- public HTTP surface は、製品側で再実装することなく、L7 が提供する edge protection を利用できるべきである。
- 長時間存続する collector stream は、ingress の idle behaviour によって切断されてはならない。
- self-hosted deployment に対する regression を起こさない。code path は 1 つであり、2 つではない。

## 検討した選択肢

- **A — すべてに 1 つの L4 load balancer。** 両 plane に TCP passthrough を使い、public API を含むすべての
  TLS session を binary が terminate する。
- **B — split ingress。** collector plane には passthrough の **TCP listener を持つ network (L4) load
  balancer**、control-plane HTTP surface には **application (L7) load balancer** を使う。
- **C — managed mutual TLS を持つ 1 つの L7 load balancer。** application load balancer 自身が client
  certificate を authenticate する（trust store に対する verify mode と revocation list を使用）か、certificate
  chain を HTTP header として target に forward する。

## 決定の結果

選択した選択肢は **B — split ingress** である。

### 帰結

- **良い点:** collector plane は byte-for-byte で self-hosted path と同一である。TCP listener は TLS を
  terminate しないため、binary が handshake を行い、on-premises とまったく同じように certificate requirement
  自体を enforce する。authorizer に cloud 固有の branch はなく、audit chain にも cloud 固有の case はない。
- **良い点:** public surface は、製品側で再実装することなく managed certificate、host/path routing、web
  application firewall を利用できる。この firewall は**別料金**の service であり、L7 load balancer に無料で
  付属する性質ではない。ここでは利用可能なものとして挙げており、含まれるものとしてではない。
- **良い点、その範囲を正確に述べると:** TCP listener の idle timeout は **60 秒から 6000 秒の間で設定可能**
  である（`tcp.idle_timeout.seconds`、default は **350**）。TLS listener の idle timeout は **350 秒に固定され、
  変更できない**。これは byte が流れないことに対する **idle** timeout であり、**stream duration の上限ではない**。
  data または keepalive frame を送信し続ける stream は、350 秒で切断されない。したがって passthrough が「長い
  stream を可能にする」のではなく、idle budget をこちらで設定できるようにする。重要な点を逆から言えば、
  **無通信の stream は、これらの ingress のどれでも切断される**ため、client はそれに耐えなければならない。
- **悪い点、かつ上の項目を警告として述べる理由:** collector client は **gRPC keepalive を一切設定していない**
  （library の default は無効）。さらに send が失敗した後も、dead stream を再構築せず cache に保持する。そのため、
  設定した timeout より長い idle period、leader の交代、または deployment によって collector stream が終了し、
  それを再接続するものは何もない。これは**split が生み出した問題ではなく**、以前から存在する。ただし split は、
  intermediary が idle connection を能動的に閉じる最初の deployment であり、この欠陥によって data が失われ始める
  場所でもある。collector 側の reconnect-with-backoff loop は、この ingress を production-ready と呼ぶための
  **前提条件**である。
- **悪い点 / トレードオフ:** load balancer が 2 つになるため、時間単位の料金が 2 つ、独立した capacity-unit meter
  も 2 つになり、小規模 deployment の固定月額の大半を両者が占める。これは両方の admission factor を process 内に
  維持するために支払う、現実に継続して発生するコストである。
- **悪い点、かつ脚注ではなく build requirement:** **TCP または TLS protocol の IP-type target group では、
  client IP preservation は default で無効になっている**。また、managed container runtime 上の task は IP target
  である。default のままでは、すべての collector connection が load balancer の private address を source として
  binary に到達する。address から導出されるもの — audit record、rate limit、address allow-list — はすべて、初日から
  気付かれないまま誤ったものになる。`preserve_client_ip.enabled` を有効にするか、binary が handshake より前に
  Proxy Protocol v2 を parse するまで、ingress は完成していない。preservation を有効にすると、target の security
  group は load balancer の address ではなく client address を source とする traffic に対応することにもなり、
  network design はこれを考慮しなければならない。
- **中立 / follow-up:** source address を復元する 2 つの mechanism のどちらを選ぶかは implementation phase に
  委ねるが、**default から継承するのではなく、必ず選択して test しなければならない**。記録された source address が
  collector のものと一致することを assert する test が acceptance criterion である。

## 代替案を却下した理由

- **A（1 つの L4 load balancer）** — collector plane ではなく、*public* plane について却下した。より安価で、
  self-hosted topology に最も近いが、control-plane API は managed certificate、WAF、host/path routing を失い、
  edge がすでに提供するものを製品が L7 で再実装することになる。選択肢 A の collector 側は、まさに選択肢 B が
  維持する部分である。
- **C（L7 の managed mutual TLS）** — **trust boundary を移動させる**ため却下した。verify mode では edge が
  certificate check を実行し、application はすでに保証済みの request を受け取る。passthrough mode では
  certificate chain が `X-Amzn-Mtls-Clientcert` header として届く。どちらの場合も、transport gate は製品が
  enforce するものではなくなり、別の何かによる assertion になる。これは、この製品が検証可能にするために存在する、
  まさにその置換であり、その failure mode（target に直接到達できるものは何であれ header を forge できる）は、
  network configuration の誤り 1 つで現実になる。revocation list を備えた managed trust store は真の
  operational advantage だが、collector certificate について製品は現在その機能をまったく持たない。製品は CA を
  load して通常の X.509 validation を行うだけであり、CRL も OCSP も check しない。managed revocation が将来
  first-hand termination より重要になるなら、この record の amendment ではなく、**新しい record** で決定する。
