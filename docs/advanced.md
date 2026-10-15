# 配置与运行参考

[返回使用说明](../README.md)

配置合并、断网保护、系统服务及失败处理的详细约定。日常操作请先阅读使用说明。

## 断网保护

需要 sudo/root。优先使用预检通过的 nftables，其次使用同时可用的 iptables、ip6tables 和各自的 restore 工具；没有可用后端时提示安装 nftables。开启后固定所选后端，不在运行中切换。只创建本程序的 `inet tim_network_guard` 表或 `TIM_NETWORK_GUARD` 链，不清空系统规则，也不使用所有 root 进程或所有 established 连接作为放行条件。

保护只约束本机 OUTPUT：普通程序不经受管 TUN 的公网流量被丢弃，已有直连连接也受约束。允许回环、受管 TUN、tim 自身必要联网、mihomo 标记出站和以下私网：`10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16`、`169.254.0.0/16`、`fc00::/7`、`fe80::/10`。普通程序绕过 TUN 的 TCP/UDP 53、853 在私网放行前阻断。保留必要 DHCP、IPv6 邻居发现等控制流量。

这是防止 TUN 中断后回退直连的保护，mihomo 正常的 DIRECT 分流仍可用。tim 在 TUN 不运行时仍可下载订阅、内核及查询 IP；其 TCP/UDP socket 与 DNS socket 单独设置标记，不豁免公共系统解析器。保护开启时，tim 使用 `setting.yaml.config.dns.default-nameserver` 中的非回环 IP 进行必要解析，该列表至少需要一个有效 IP。无法仅凭端口识别局域网 HTTPS 中的 DoH，也不对恶意 root 提供防御。

无内核、订阅或 TUN 时也可先开启，不自动启动 mihomo；显示“保护已开启，TUN 未就绪”。若 mihomo 正在运行，开启会重建配置并重启一次，使已有出站 socket 使用标记。生成配置固定使用 `tim-tun` 接口；拒绝 `tun.auto-redirect: true` 以及冲突的全局、节点或 provider `routing-mark`。停止内核、关闭 TUN、退出 tim、卸载 mihomo 服务都不解除规则。关闭保护需要明确确认；网页认证后需手动重新开启。

不再读写 `/etc/tim/network-guard.json` 或保存开启意图，直接从实际 nftables/iptables 规则识别保护后端。状态以实际规则为准：规则完整为“开”，整套规则不存在为“关”，部分缺失、被修改或无法查询为“异常”。显式开启失败保留已有规则，允许重试开启或明确关闭。

**规则是否跨重启保留，由系统的 nftables/iptables 保存和加载机制决定；tim 不启停防火墙服务，也不自动保存或恢复规则。** 进入 TUI、start/restart、安装服务、更新订阅均不恢复保护。系统未加载规则时显示“关”。需要重新开启时执行 `guard on`，不自动启动 mihomo：

```bash
sudo /opt/tim/tim guard status
sudo /opt/tim/tim guard on
sudo /opt/tim/tim guard off        # 输入 y 确认关闭并恢复直连
sudo /opt/tim/tim guard off --yes  # 明确跳过交互确认
```

`guard status/off` 不依赖 setting.yaml、DATA、订阅或内核文件完整性。进入 tim 只查询防火墙，不创建或修复规则。未放行的公网 SSH 连接也可能被阻断，请在本地控制台操作。部署程序本身不会自动开启保护。

## 配置语义

`setting.yaml` 是 TUI 管理项的唯一来源：订阅 URL、端口/监听、允许局域网、运行模式、TUN、DNS、域名嗅探、链式前置和路径设置。文件结构为 `subscription_url`、`fake-ip-enabled`、`chain-proxy`（`enabled` 和原始 `input`）、`delay-test-url`、`mihomo-path`、`data-dir` 及 `config`（自动配置模板与管理字段）。`user.yaml` 专用于用户自定义，允许文件不存在；不存在时按无自定义配置处理，编辑器默认空白，空白内容表示无自定义配置。

生成顺序为订阅 → 根据 `setting.yaml` 生成自动配置 → `user.yaml` 最终覆盖（合并前剔除 `mode` 和整个 `tun` 段，这两项仅使用界面管理设置；用户原文件保留）。端口和 DNS 页面仅在自定义值与自动配置不同时显示配置来源，可打开详情；主界面操作结果只显示简短状态，如“配置已更新”“内核正在运行”“内核已启动”“内核已重启”。因此自定义相同字段后，界面管理值与最终运行值可以不同。普通 YAML 注释尽量保留；锚点/别名在解析时展开。

`setting.yaml` 每次保存时统一使用两空格缩进，映射和非空列表展开为多行 YAML；保留配置值、标量引号及配置节点注释，便于手工编辑。

不读取旧 `settings.json`，不从 `user.yaml` 提取管理设置，不创建迁移备份；旧数据文件不自动搬移或删除。

DNS 首次默认 fake-ip 开启、IPv6 解析关闭、域名嗅探开启。IPv6 开关仅修改 `dns.ipv6`。上游在界面中使用逗号分隔，保存时转为 YAML 字符串列表；去首尾空白，拒绝空条目，保留 URL 和 `#` 参数。前三类上游不允许为空，`direct-nameserver` 可清空为 `[]`。

```yaml
default-nameserver: ["223.5.5.5", "119.29.29.29"]
nameserver: ["https://1.1.1.1/dns-query"]
proxy-server-nameserver:
  - "https://doh.pub/dns-query#DIRECT"
  - "https://dns.alidns.com/dns-query#DIRECT"
direct-nameserver:
  - "system"
  - "https://dns.alidns.com/dns-query"
  - "https://doh.pub/dns-query"
fake-ip-filter: ["*.lan", "+.local", "+.cn"]
```

Fake-IP 过滤列表每行一项，Ctrl+S 确定、Esc 放弃本次列表修改；确定后返回 DNS 草稿，离开页面时确认应用或取消放弃。允许空列表。关闭 fake-ip 时，保存的真实列表保留，自动配置保持 `enhanced-mode: fake-ip`、`fake-ip-filter-mode: blacklist`，仅将生成的过滤列表改为 `['+.*']`；重新开启恢复真实列表。`user.yaml` 仍可最终覆盖这些字段，界面会提示。

自动生成配置时，TUI 管理的 DNS 段完整替换订阅 DNS 段；不会继承订阅的 `nameserver-policy`、`fallback`、`fallback-filter`、`fake-ip-range` 等额外字段。内置默认项与 `demo.js` 对齐，保留已确认的无 `#main` 上游地址和默认过滤列表；已有管理设置沿用保存值，可通过重置恢复默认。`user.yaml` 仍可最后显式添加或覆盖 DNS 项。

“一键重置 DNS 配置”恢复页面开关、四类上游和过滤列表默认值，先更新草稿，退出时确认应用并保存；DNS 监听地址也恢复为 `demo.js` 的 `0.0.0.0:1053`，保留其他管理项和 `user.yaml` 自定义配置。离开 DNS 页面时有修改会询问是否应用，仅提供确认和取消；确认应用并保存，取消放弃草稿并返回主菜单。旧配置若关闭 DNS，应用 DNS 页面设置后会启用自动配置中的 DNS，页面标题明确提示；用户自定义的 `dns.enable` 仍优先。

DNS 使用 `demo.js` 的缓存、规则遵循和 hosts 模板；嗅探使用 HTTP/TLS/QUIC 协议配置；profile 设置 `store-selected: true`、`store-fake-ip: false`。模板包括 DNS 引导 hosts、Google 域名映射和 bilivideo 屏蔽，用户可在 `user.yaml` 覆盖。

合并规则：

- 映射递归覆盖，包括 DNS；标量和普通列表整体替换。
- `user.yaml.rules` 去空、精确去重后前置于订阅规则；订阅规则顺序保留。
- 空的用户规则列表不清空订阅规则。不注入现有 Hub 的固定规则。
- 用户 `MATCH` 保留位置，并提示它可能遮蔽后续规则。
- `profile.store-selected: false` 明确报错，避免与原生记忆需求冲突。

基本配置面板支持 Mixed、HTTP、SOCKS、允许局域网、栈模式、控制接口、延迟测试 URL、mihomo 路径与 data 目录；新配置默认允许局域网，自动配置的代理监听地址由该开关决定：开启为 `0.0.0.0`，关闭为 `127.0.0.1`，不再提供独立监听地址输入。已有设置保留原开关选择，旧管理文件中的独立 `bind-address` 不再使用；DNS 监听地址在 DNS 配置页统一管理，默认 `0.0.0.0:1053`。HTTP/SOCKS 设置为 0 可关闭，代理入口默认仅开 Mixed。`redir-port`、`tproxy-port` 等可通过 YAML 配置，但本工具不负责配置对应转发规则。DNS 监听地址仅在 DNS 开启时生效。栈模式提供 `mixed`、`system`、`gvisor` 三个选项，保存为管理配置的 `tun.stack`，默认沿用 `system`；返回确认后应用，TUN 开启时使用所选栈。

### GLOBAL 记忆

规则和全局模式均不自动生成或重建 `GLOBAL`，订阅和用户显式定义的策略组保留。没有显式 `GLOBAL` 时，mihomo 自行创建内置组，因此规则模式的控制 API 也可能返回 `GLOBAL`；实际路由模式以 `/configs` 的 `mode` 为准。

选择状态只保存在 mihomo 的 `home/cache.db` 中，不另建 TUI 选择缓存。首次全局模式的候选顺序、默认出口及历史选择失效后的回退均由内核决定，tim 不自动选择节点；原生默认出口可能是 `DIRECT`。界面显示内核返回的当前 GLOBAL 出口，可通过 mihomo 控制 API 改变选择，之后由内核记忆。

### 链式前置代理

在 `a` 页面粘贴一个独立前置节点，开启后应用。链路为 **本机 → 前置 → 原出口 → 目标网站**，规则和 GLOBAL 模式均可使用；TUN 开关与此独立。

输入支持单个节点映射、只有一项的节点列表、只包含一个节点的 `proxies` 包装，以及以下分享链接：`vless://`、`vmess://`、`ss://`、`ssr://`、`trojan://`、`hysteria://`、`hysteria2://`/`hy2://`、`tuic://`、`anytls://`、`socks://`/`socks5://`/`socks5h://`、`http://`/`https://`。也支持整条链接或单节点 YAML 的标准/URL-safe Base64（有无 padding 均可）。VMess 的 Base64 JSON、SOCKS 的 Base64 用户名密码分别按对应格式解析；这些格式不是所有协议通用的标准。URI 转换复用 mihomo v1.19.20 的转换包，协议是否可用最终由实际安装的内核校验；其他协议或特殊参数请使用完整的单节点 YAML。

例如下面是一个不含真实凭据的 SOCKS5 前置：

```yaml
name: 我的前置
type: socks5
server: 192.0.2.10
port: 1080
udp: true
```

每次只接受一个独立节点，输入上限 64 KiB。不导入多节点订阅、策略组或规则，也不允许前置节点再通过 `dialer-proxy` 引用其他节点。开关和原始输入在应用成功后保存于私有权限的 `setting.yaml`，编辑草稿不会影响当前连接。

生成配置时，TUI 为前置分配不冲突的内部名称，覆盖原出口节点的 `dialer-proxy` 及 provider 的 `override.dialer-proxy`。前置自身不被套入链路，并从 `include-all`/`include-all-proxies` 自动组中排除；内置 GLOBAL 的候选由内核决定，可能包含这个前置节点。原订阅、用户文件和路由规则不被改写；关闭后重新生成原有配置。此处链式管理字段优先于 `user.yaml`，其余管理项仍沿用原有覆盖顺序。DNS、DIRECT 和订阅下载路径保持现有配置语义。

顶部第二行在 `GLOBAL` 之后显示 `链式前置: 我的前置`，依据已应用的运行配置，不把未应用的输入显示为已生效。输入无效、没有代理出口，或含前置的配置被内核拒绝而不带前置的原配置校验通过时，本次不启用链式，显示 `链式前置: 未生效（原因）`，有效订阅仍正常应用。订阅自身错误、文件错误和操作取消仍按原流程报告；前置网络不通则交给 mihomo，不自动绕过。

### TUN

自动模板采用 `demo.js` 的 system 栈、自动路由、出口识别、strict-route 和 DNS 劫持参数；TUN 开关只改变管理项 `tun.enable`，关闭不删除配套配置。DNS 页面不会自动开启 TUN。TUN 配置仅使用界面管理设置，`user.yaml` 的整个 `tun` 段不参与覆盖；DNS 自定义仍最终优先。

TUN 路由和模式变更可能影响 SSH。后台进程跨注销存活受会话清理策略影响。本工具不配置局域网网关或系统代理。

## 命令行

可从任意目录执行，配置读取自 tim 实际可执行文件所在目录：

```sh
./tim status
sudo ./tim install
sudo ./tim uninstall
sudo ./tim start
sudo ./tim stop
sudo ./tim restart
sudo ./tim update-sub
sudo ./tim update-sub --hot
```

无参数进入 TUI，进入时识别真实状态，停止中的内核保持停止，退出时保留正在运行的内核。命令行和 TUI 共用内核启停及配置应用逻辑。

`tim start/restart` 和界面 S/R 只使用已有的 `config.yaml`，不下载订阅、不重新合并或写入配置，也不改写服务定义。缺少运行配置时明确提示先下载订阅或应用配置。基本配置、DNS、链式前置、模式/TUN 和 `user.yaml` 的应用负责更新运行配置。

普通命令行订阅更新在运行中重新应用，停止中也生成并保存 `config.yaml`，不启动内核；`--hot` 仅适用于运行中内核，保持 PID，不允许改变控制接口、密钥、主要监听或 TUN 开关。下载、校验、API 应用和落盘失败均按阶段显式报告。下载缓存先独立保存；新配置保存后再执行内核应用，失败不恢复旧配置、旧服务执行路径或旧实例。可另行执行 `tim start` 或按 `s` 启动。

### 系统启动服务与原生监督

自动选择当前运行的服务管理器，优先级为 **systemd → OpenRC**：检测 `/run/systemd/system`，否则检测 `/run/openrc/softlevel`。OpenRC 需要 `rc-service`、`rc-update`、`rc-status`、`supervise-daemon` 和 `/sbin/openrc-run`。选定管理器的操作失败会明确报错，不切换到另一管理器。

服务安装是显式操作，不会因启动内核而自动注册。`tim install` 要求内核已停止，注册后保持停止，不启用开机启动。`tim start/restart` 启动后启用开机启动；`tim stop` 停止服务并禁用开机启动，因此重开界面或重启主机仍保持停止。OpenRC 启动时登记到 `default` runlevel，停止时移除该服务的所有 runlevel 登记。直接执行 `systemctl stop` 或 `rc-service <服务名> stop` 保持管理器原生语义，跨主机重启保持停止应使用 `tim stop`。

已安装时，systemd 直接监督 mihomo，退出后间隔 3 秒持续重试，不设失败次数上限；自动恢复仅使用当前 `config.yaml`，不下载订阅、不重新合并设置。服务不增加启动后或运行期健康检查；进程存在不保证代理可用，错误配置会持续产生失败日志。未安装时仍使用原有直接启停，共用进程发现、PID 身份核验及配置应用；服务操作失败会明确报错，不退回直接启动。

OpenRC 使用原生 `supervise-daemon` 直接监督 mihomo，设置 3 秒重试延迟和不限次数，不引入 cron 或 tim 常驻守护。**已知限制：Alpine 的 OpenRC 0.63.2 在子进程极快退出时存在原生退出检测竞态，可能只启动一次后不再重试。** 实测正常运行中的内核被杀后可以恢复，但不能承诺快速失败时持续重试。当前版本交付启动服务和原生监督能力，不通过额外健康检查绕过这一限制；运行期状态仍需结合内核 PID 和日志判断。原因与证据见 [OpenRC 监督器研究](../.trellis/research/openrc-supervision.md)。

服务名绑定配置目录：systemd 为 `tim-mihomo-<摘要>.service`，文件位于 `/etc/systemd/system/`；OpenRC 为 `tim-mihomo-<摘要>`，脚本位于 `/etc/init.d/`。状态命令显示管理器和实际名称；执行路径使用绝对路径。使用 `DATA` 安装时，后续管理必须使用相同的有效目录，或通过基本配置正常切换路径。内核更新和数据目录切换会同步服务执行路径，维护中的临时停止不取消开机启动。

服务已安装但未运行时，若发现由外部脚本启动的同配置内核，`tim start` 会报错，要求先停止外部实例再启动服务；不会将该实例视为已受系统监督，也不会并行启动另一个内核。

`tim uninstall` 和菜单卸载均先确认。继续后停止内核、禁用开机启动、移除服务；停止失败则不删除服务。取消或 CLI 未收到确认时不执行卸载。配置、内核文件、缓存和日志全部保留。服务安装及管理需要管理员权限。

## 数据与失败处理

可执行文件目录固定保存 `setting.yaml`。不再创建或读取 `operation.lock`；旧文件无需删除，不影响打开界面和执行操作。数据目录采用私有权限，并包含：

| 路径 | 用途 |
|---|---|
| `mihomo` | 默认 `mihomo-path` 指向的预装内核 |
| `user.yaml` | 最终用户自定义覆盖 |
| `subscription.yaml` | 最近下载并解析成功的订阅（独立于内核应用结果） |
| `config.yaml` | 配置应用或订阅下载时生成；启动只读取，后续失败保留已保存内容 |
| `home/` | mihomo 工作目录、原生缓存、provider 文件 |
| `kernels/` | 已验证的更新内核，原预装文件不改动 |
| `mihomo.log` | 内核输出 |
| `error.log` | 完整操作错误 |

不再创建或读取 `state.json`，打开当前数据目录时清理这个废弃文件。待启动的配置由持久运行配置 `config.yaml` 决定，实际运行状态由控制器与进程检查判断，内核路径来自 `setting.yaml`；进程状态从 `/proc` 中精确匹配实际配置和工作目录并核验，不依赖旧 PID 文件。旧版 `active.json`、`process.json` 和 `generations/` 不读取或迁移。配置应用不保存回滚快照。

只管理配置路径和工作目录匹配的实例；存在多个匹配进程时拒绝操作。命令行参数在 `--` 处停止解析，监督器后面携带的内核参数不被当成监督器自己的身份。直接运行模式停止时以 pidfd 和启动时间防止 PID 重用，使用 SIGTERM 并等待 12 秒，超时不强杀。systemd 模式等待 12 秒后强制结束服务 cgroup；OpenRC 使用 `TERM/12/KILL/3` 原生停止策略。强停可能丢失未落盘缓存。

已有内核时，配置应用先由 `mihomo -t` 校验，再停止原实例、保存新配置、同步服务并按原运行状态启动。直接运行模式启动后还检查是否真正监听，系统服务模式不增加启动就绪钩子。校验失败不提交；提交后的失败明确报告，不恢复旧配置或旧实例。单个文件仍原子替换，多文件按顺序保存，发生 I/O 错误时保留已完成的写入，修正错误后重新应用。热更新也先保存新配置再调用 API，失败不发送恢复请求；磁盘配置可能尚未在运行内核生效。

内核更新使用官方 GitHub 稳定 Release 的匹配架构资产，从官方资产页面取得 SHA-256，检查下载大小上限、响应 Content-Length（存在时）和 SHA-256、gzip 解压上限和 `mihomo -v`。缺失摘要拒绝更新。下载/验证阶段保持旧进程，切换时才重启；保存新内核路径后的启动失败保留新路径并明确报错，不恢复旧内核。保留工作目录及缓存，更新保留当前已应用配置。日志和旧内核暂不自动清理，长期运行需自行管理磁盘空间。

