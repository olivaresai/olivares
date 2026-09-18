---
title: 从软件包安装
description: >-
  在加固的 Linux 主机上从 .deb、.rpm 或 .apk 安装 Olivares AI：先验证发行版再信任它，在随包
  systemd 单元下运行，或在默认 Alpine 上以前台方式运行，接入第一个源，并在线、固定版本或完全
  隔离升级。
draft: false
---

:::note[已发布的软件包名称]
GitHub 的 v26.9.1 发行发布 `amd64` 与 `arm64` 的 `.deb`、`.rpm` 和 `.apk`，并附带
`checksums.txt`、`checksums.txt.sig` 和 `checksums.txt.pem`。下面的命令使用该发行的字面
`amd64` 名称；在 64 位 ARM 主机上将 `amd64` 换成 `arm64`。请从这些已验证的发行产物安装。
源码树中的仓库元数据生成器不是本指南的安装说明。

**DIST-24-05 资格认定。** CI 认定的是经验证的 **shell 安装器** 及其服务/doctor 契约，而不是
`dpkg`、`rpm` 或 `apk`：一个 dispatch/pull request 矩阵在 Debian stable、Ubuntu 24.04 LTS、
Fedora、openSUSE Leap 和 Alpine 的容器 userland 以及托管的 macOS 14 runner 上，针对已发布的
`v26.9.1` 发行运行它；当公开发行不可达时，它以「不可测量」失败，而不是把 dry-run 算作覆盖。
该矩阵不认证原生包管理器安装。本源码树构建的软件包的原生生命周期 — 安装、启动、重启、升级
正在运行的 OpenRC 服务以及移除 — 已在一次性的 Alpine 客户机中本地演练；这是对本源码树的证据，
不是已签名、已托管或 preproduction 的资格认定，那些仍待完成。

**DIST-24-06 拟议仓库（不是可用的安装面）。** 源码树包含确定性的 apt、rpm-md 和 APK 仓库
生成器、签名索引校验器、干净客户端资格认定，以及一个分阶段发布工作流，其 dispatch 在审阅者
批准之前保持不激活。**没有任何软件包仓库 URL 处于可用状态**，没有委派的 DNS 名称，也没有配置
生产环境的仓库签名密钥。此提案中没有任何东西是包管理器源；请继续使用下面经验证的发行产物。
:::

这是普通 Linux 主机上把引擎作为服务（而不是容器）运行的路径。Debian/Ubuntu 与
RHEL/Fedora/SUSE 默认使用 **systemd**。Alpine 的默认 init 是 **OpenRC**，不是 systemd。
容器见 [使用 Docker 部署](/zh/how-to/docker-deployment/)；完全没有出站路由的主机见
[在隔离环境中安装](/zh/how-to/air-gap-install/)，本页在升级步骤会链回该文。

## 1. 信任之前先验证发行

对安全产品而言，构建流水线是信任模型的一部分，因此这里不会要求你凭空相信下载。把软件包、
`checksums.txt` 和签名放在同一目录，并 **从该目录** 运行验证器：

```bash
# keyless / Sigstore (default; reaches Rekor over the network)
./verify-release.sh
```

发布采用无密钥签名，不发布 cosign 公钥，因此对从发布下载的软件包请使用无密钥命令。它需要
Sigstore 信任根材料，尚未缓存时由 cosign 获取。`--offline` 只去掉 Rekor 查询，并不会让验证
无需网络。`--key` 只适用于用你控制的私钥签名的文件，并且必须与待验证文件分开获取该公钥。

各步骤检查什么、在部分发行上如何表现、以及如何改为验证容器镜像，见
[验证你下载的内容](/zh/how-to/verify-a-release/)。

## 2. 安装软件包

三种格式携带二进制 `/usr/bin/olivares`、环境文件 `/etc/olivares/olivares.env`
（`config|noreplace`）、数据目录 `/var/lib/olivares` 以及许可证文本。
`.deb`/`.rpm` 附带加固的 **systemd** 单元。**本源码树构建的软件包** 在 `.apk` 中放入
可执行的 **OpenRC** 单元 `/etc/init.d/olivares`，并带有显式的 `package-init` 标记，使钩子不
根据主机上是否存在 `systemctl` 来猜测。

此前发布的 `.apk` 仍附带同一个 systemd 单元，**不** 附带 OpenRC 单元。那个已发布的
linux tarball 携带许可证文本、README 和 `SECURITY.md`；它不含 `scripts/install-service.sh` 或
`packaging/service/`。这些适配器文件位于源码树内为下一发行准备的签名归档中。

```bash
# Debian / Ubuntu
sudo dpkg -i olivares_26.9.1_linux_amd64.deb

# RHEL / Fedora / SUSE
sudo rpm -Uvh olivares_26.9.1_linux_amd64.rpm

# Alpine
sudo apk add --allow-untrusted olivares_26.9.1_linux_amd64.apk
```

安装会 **创建系统用户和组 `olivares`**（shell 为 `/usr/sbin/nologin`，家目录为
`/var/lib/olivares`），创建属主为该用户、模式为 `0750` 的 `/var/lib/olivares`，并创建
`/etc/olivares`。systemd 软件包在存在 `systemctl` 时重载 systemd；OpenRC 软件包不会启用或
启动服务。它 **不会启动任何东西** — 见
[软件包不会做的事](#8-软件包不会做的事)。

### 启动引擎

在 Debian/Ubuntu 与 RHEL/Fedora/SUSE 上：

```bash
sudo systemctl enable --now olivares
```

在 **OpenRC**（本源码树的 `.apk`）上：

```bash
sudo rc-service olivares start
# 可选；软件包不会这样做：
sudo rc-update add olivares default
```

首次启动令牌在 `/var/log/olivares.log` 中（若 syslogd 在运行，也在 `logread` 中）。
`OLIVARES_EXTRA_ARGS` 中的额外参数在禁用 globbing 的情况下按空格拆分后追加；不解释嵌套引号，
env 文件也不会作为 shell 执行。

在 **此前发布的 Alpine** 上没有 `systemctl`，该 `.apk` 也没有 OpenRC 单元。
以服务用户启动引擎；首次启动令牌打印到 stdout：

```bash
sudo -u olivares olivares serve --data-dir=/var/lib/olivares \
  --listen=:8443 --grpc-listen=:8444 --checkpoint-interval=1h
```

## 3. 加固的 systemd 单元

随包 systemd 单元以非特权用户 `olivares` 运行引擎，capability bounding 集为空 — 既没有
ambient 也没有 bounding capability — 并设置 `NoNewPrivileges=true`，因此它启动的任何进程都
无法获得权限。此外还带有 `ProtectSystem=strict`（文件系统只读，例外是
`ReadWritePaths=/var/lib/olivares`）、`ProtectHome`、`PrivateTmp`、`PrivateDevices`、四条
`ProtectKernel*`/`ProtectClock` 指令、`RestrictNamespaces`、`RestrictSUIDSGID`、
`RestrictRealtime`、`LockPersonality`、`MemoryDenyWriteExecute`、
`SystemCallArchitectures=native`、额外去掉 `@privileged` 和 `@resources` 的
`@system-service` 系统调用过滤器，以及 `UMask=0027`。

在默认 Alpine 上，这些 systemd 指令不适用于 **此前发布的** `.apk`，因为该载荷的 systemd
单元并未运行。本源码树构建的 `.apk` 软件包改为在 OpenRC 下运行：使用 `olivares` 账户，把首次
启动令牌写入 `/var/log/olivares.log`，并且不实现 systemd 沙箱指令。

**监听器默认接受来自网络的连接** — HTTP（REST 加上嵌入式控制台）为 `--listen=:8443`，gRPC 为
`--grpc-listen=:8444`，即双栈通配符。这是一台服务器，保护它的是默认启用的 TLS、没有任何默认凭据，
以及单次使用的设置令牌。要加以限制，请在 `/etc/olivares/olivares.env` 中有意设置
`OLIVARES_EXTRA_ARGS=--listen=127.0.0.1:8443 --grpc-listen=127.0.0.1:8444`（这些标志附加在单元
自身的标志之后，后者获胜），并在前面放置你自己的 TLS 终结。IPv6 回环使用
`--listen=[::1]:8443`。

### 可执行的临时目录挂载

这是 systemd 主机上值得提前知道的失败，因为症状并不点明原因。

进程外的第一方连接器 **嵌入在二进制中**。启动时引擎把需要的连接器提取到私有临时目录并以
子进程执行。未设置 `TMPDIR` 时，临时目录创建在 `<data-dir>/tmp` 下；只有数据目录不可写时，
引擎才会回退到系统临时目录。显式 `TMPDIR` 始终优先。

因此随包 systemd 服务默认使用 `/var/lib/olivares/tmp`，而不是 `/tmp`。检查实际会承载可执行
临时目录的挂载：

```bash
check_scratch_mount() {
  target=${1:-/var/lib/olivares}
  opts=$(findmnt -no OPTIONS --target "$target") || {
    printf '%s\n' "cannot read mount options for $target (missing path or permission)" >&2
    return 1
  }
  [ -n "$opts" ] || {
    printf '%s\n' "mount options for $target are unknown" >&2
    return 1
  }
  case ",$opts," in
    *,noexec,*) printf '%s\n' 'noexec — set TMPDIR' ;;
    *) printf '%s\n' 'exec-capable — nothing to do' ;;
  esac
}
check_scratch_mount /var/lib/olivares
```

若显示 `noexec`，把 `TMPDIR` 指向在 `ProtectSystem=strict` 下可写 **且** 位于可执行挂载上的
目录：

```bash
sudo install -d -o olivares -g olivares -m 0750 /run/olivares-exec-tmp
sudo systemctl edit olivares      # creates a drop-in; do not edit the shipped unit
```

```ini
[Service]
Environment=TMPDIR=/run/olivares-exec-tmp
ReadWritePaths=/run/olivares-exec-tmp
```

然后重启。若 exec 以 `EACCES` 或 `ENOEXEC` 被拒绝，引擎错误会点名提取挂载以及两个重定位控制
（`TMPDIR` 和 data-dir）；它不会把 noexec 挂载报告为缺失的连接器。

使用 `systemctl edit`，绝不要直接编辑 `/usr/lib/systemd/system/olivares.service`：该文件属于
软件包，升级会替换它。

## 4. 首次启动：`olivares quickstart`

`quickstart` 是带友好默认值和引导横幅的 `serve`。它从不编造默认凭据；它把你指向嵌入式控制台，
用 **一次性令牌** 创建第一位管理员。

若已在随包 systemd 单元下提供服务，就不要运行 `quickstart` —
**首次启动设置令牌会打印到日志**：

```bash
journalctl -u olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'
```

若你在前台启动了 `serve`（默认 Alpine），同一横幅会出现在 stdout。

在 `https://127.0.0.1:8443` 打开控制台（首次启动会生成自签名证书），出示该令牌并创建管理员。
令牌一次性有效。

在工作站上若想不安装服务就先看看，`olivares quickstart` 在前台做同样的事；若需离开默认值，
使用 `--listen`/`--grpc-listen`/`--data-dir`。

## 5. 第一个源：pgAudit

源是引擎摄取观测的地方。动词按各自成本拆分，值得按该顺序使用：`plan` 说明会改变什么且不写入
任何内容，`validate` 说明配置自身一致 **且不触网**，`test` 真正打开源以证明它会应答，`set`
再应用。配置携带秘密的 **引用**（`store:<name>`），从不携带值。

pgAudit 读取 PostgreSQL 审计日志，因此唯一必填字段是 `log_path`：

```bash
# coherent by itself? (writes nothing, opens no socket)
sudo -u olivares olivares sources validate --name pg-prod --kind pgaudit \
  --tenant <your-tenant-id> \
  --config log_path=/var/log/postgresql/postgresql.log \
  --data-dir /var/lib/olivares

# does it actually answer? (opens the source for real)
sudo -u olivares olivares sources test --name pg-prod --kind pgaudit \
  --tenant <your-tenant-id> \
  --config log_path=/var/log/postgresql/postgresql.log \
  --data-dir /var/lib/olivares

# apply it
sudo -u olivares olivares sources set --name pg-prod --kind pgaudit \
  --tenant <your-tenant-id> \
  --config log_path=/var/log/postgresql/postgresql.log \
  --actor "$(id -un)" --reason "onboard the production audit log" \
  --data-dir /var/lib/olivares
```

上面命令行中有三点不是填充：

- **`--tenant` 是必填的。** 源必须点名其观测所属的业务租户；没有它，命令会拒绝，而不是为你的
  审计数据猜测所有者。
- **`--actor` 和 `--reason` 只由 `set` 要求。** 特权离线操作必须记录是谁做的、为什么做。
  `validate` 两者都不需要，因为它不写入任何内容 — 这种不对称正是重点。
- **`format` 默认为 `csvlog`，`follow` 默认为 `true`**，因此标准 pgAudit 部署两者都不需要。

应用会逐字段打印变更，并告诉你如何让 **正在运行** 的引擎在不重启的情况下接收 —
`POST /v1/console/runtime/reload`，或 `SIGHUP`。在随包 systemd 单元下，这是
`sudo systemctl reload olivares`。若你在前台启动了 `serve`，向该进程发送 `SIGHUP`。

服务用户需要读取该日志文件；在多数发行版上这意味着把 `olivares` 加入 `adm` 或 `postgres`
组 — 这是你有意授予的权限，不是软件包替你做的事。

## 6. 升级

`olivares upgrade` 原地替换二进制，其安全属性是优先于手工重装软件包的原因：它 **在已下载候选
成功通过 exec 探测之前绝不替换二进制**，保留带时间戳的备份，并且 **若交换后探测失败则回退到
该备份**。

```bash
sudo olivares upgrade --check    # what would change, without changing anything
sudo olivares upgrade --yes      # do it
```

对软件包安装特别重要的三个标志：

- **`--endpoint`** — 从你控制的 GitHub 仓库而不是默认位置获取更新。这是镜像或分叉的出路。
- **`--bundle`** — 从本地捆绑目录或 `.tar.gz` **完全不联网** 安装。构建并转移该捆绑见
  [在隔离环境中安装](/zh/how-to/air-gap-install/)。
- **`--install-timer`** — 发出按计划检查更新的 **可选 systemd** 定时器与服务。没有任何东西
  替你安装它；见 [软件包不会做的事](#8-软件包不会做的事)。它是 systemd 生成器。

注意：暂存与 exec 探测发生在 **安装目录中、目标旁边 — 而不是 `/tmp`**，因此
[上文](#可执行的临时目录挂载) 讨论的 `noexec` 挂载不会破坏升级。**安装** 目录上的 `noexec`
挂载是另一回事，会使已安装版本无法测量；该情况、发行通道、分阶段推出与回滚见
[升级与回滚](/zh/how-to/upgrade-and-rollback/)。

## 7. 卸载或迁移，不要猜测路径

软件包写入 `/var/lib/olivares/install-manifest.json`。卸载程序在触碰服务或文件系统之前，会按
已签名的发行索引验证完整清单；意外路径返回 2。先检查，再明确选择保留策略：

```bash
sudo olivares uninstall --plan --data-dir /var/lib/olivares
sudo olivares uninstall --preserve --data-dir /var/lib/olivares
sudo olivares uninstall --purge --data-dir /var/lib/olivares --yes
```

Preserve 是软件包移除策略：它保留配置、数据、日志、密钥及其服务身份。在 systemd 主机上，软件包
钩子运行 `--preserve`。在本源码树构建的 OpenRC `.apk` 软件包上，钩子会在服务活动时停止它，
移除默认 runlevel 条目（若从未启用也不会失败），用 `--plan` 验证完整清单，并让 apk 删除软件包
所有的文件。此前发布的 Alpine 软件包只用 `--plan` 验证，因为该载荷没有可停止的 OpenRC
单元。仅在意图是擦除时，才在移除软件包之前运行 `--purge`。
它需要确认，并且只删除已编入索引的路径。

若要迁移整套资产，先创建 `olivares dr backup`，安装目标后再在那里运行
`olivares dr restore`。当前捆绑使用 `hmac-sha256-kek-v1` 在你的 KEK 下认证清单和每份载荷。
较新引擎的导出会在写入前被拒绝；单独认证的 pre-v26.9 捆绑需要显式
`--allow-legacy-unsigned`。[备份与恢复指南](/zh/how-to/backup-and-restore/) 涵盖 KEK 保管与
导入后的连续性证明。

## 8. 软件包不会做的事

说清楚，因为安全产品在这里含糊其辞就不配安装：

- **它不添加仓库。** 不会写入 `/etc/apt/sources.list.d`、`/etc/yum.repos.d` 或
  `/etc/apk/repositories`。你安装的是一个文件；只有该文件被安装。升级由你触发 — 用新软件包，
  或用 `olivares upgrade`。
- **它不启动或启用服务。** systemd 软件包在存在 `systemctl` 时重载 systemd，并打印
  `systemctl enable --now`。OpenRC 软件包打印 `rc-service olivares start`，且不执行
  `rc-update add`。是否启动仍由你决定。升级一个正在运行的 OpenRC 服务会停止它、替换文件并再次
  启动；未运行的服务保持未运行。
- **验证许可证从不打电话。下载你付过款的内容会。** 开放构建中的许可证验证是离线 Ed25519，没有
  远程终止开关，也没有许可证密钥限制或降级该构建 — AGPL 构建就是整个平台。
  **默认没有强制遥测，也没有控制平面出站：越过边界的是你配置为越过的内容** — 对你的模型 API
  的调用、你接线的 SIEM/webhook 输出、若你配置了外部嵌入提供方，以及连接器轮询的任何源
  （其 `addr`、`base_url` 或 `endpoint`），按你设置的间隔。
- **但 `olivares upgrade` 在你运行它时会故意发起网络调用** — 这正是更新检查的意义，`--check`
  在任何东西移动之前向你展示计划。该承诺的诚实表述是：*验证许可证从不打电话；下载你付过款的
  内容会。* 若希望更新路径也不调用，使用 `--bundle`。
- **它会向网络打开端口。** 单元绑定所有网络接口，直到你自己加以限制。

## 另见

- [验证你下载的内容](/zh/how-to/verify-a-release/) — 完整验证路径
- [升级与回滚](/zh/how-to/upgrade-and-rollback/) — 通道、分阶段推出、回滚
- [加固部署](/zh/how-to/security-hardening/) — 超出单元已经做的
- [在隔离环境中安装](/zh/how-to/air-gap-install/)
- [使用 Docker 部署](/zh/how-to/docker-deployment/)
- [连接源](/zh/how-to/connect-a-source/)
- [备份与恢复](/zh/how-to/backup-and-restore/)
