# tim

独立 Linux 终端管理器，代码和依赖位于本目录，不需要启动 Cli Simple Hub。使用预装 mihomo，支持订阅下载、后台启停、TUN、规则/全局模式、端口与 DNS 配置、`setting.yaml` 和 `user.yaml` 和手动 GitHub 内核更新。

## 构建与启动

构建需要 Go 1.26；运行需要 Linux 5.3+（使用 pidfd 防止误停被重用 PID 的进程）。官方更新支持 amd64、arm64。

```bash
cd tui
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o dist/tim-linux-arm64 ./cmd/tim
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o dist/tim-linux-amd64 ./cmd/tim
```

在 Linux 上，将预装内核放到启动工作目录的 `data/mihomo`，直接运行即可：

```bash
mkdir -p data
cp /path/to/mihomo data/mihomo
chmod +x data/mihomo
./tim-linux-arm64
```

`setting.yaml` 固定在**启动工作目录**，不随数据目录移动。数据目录优先级为非空 `DATA` 环境变量 → `setting.yaml` 的 `data-dir`；新配置的默认值为 `data`。相对路径以 `setting.yaml` 所在目录为基准，不以可执行程序目录为基准。Docker 等环境可用 `DATA` 覆盖：

```bash
DATA=/absolute/path/to/data ./tim-linux-arm64
sudo env DATA=/absolute/path/to/data ./tim-linux-arm64
```

`[P 基本配置]` 可编辑 `mihomo-path` 和 `data-dir`，两项都支持绝对路径或相对于启动目录的路径。默认内核路径为 `data/mihomo`；首次生成配置时若传入 `DATA`，会将该目录下的 `mihomo` 写入 `mihomo-path`。此后内核路径只读取 `mihomo-path`，不随 `DATA` 自动改写。GitHub 更新成功后将已验证的新版本路径写入该字段，原预装文件保留。

有 `DATA` 时，页面显示实际使用目录；编辑 `data-dir` 只改变保存值，取消环境变量覆盖后生效。没有 `DATA` 时，确认路径修改会切换当前数据目录；目标目录缺少订阅或 `user.yaml` 时使用当前对应配置，旧目录保留，历史日志和缓存不自动搬移。新配置校验或启动失败会恢复原设置与原运行实例。尚未下载订阅且内核停止时，确认会校验内核版本并保存设置，不启动内核。

不迁移旧配置；启动目录已有的 `setting.yaml` 必须显式包含 `mihomo-path` 和 `data-dir`，缺失或为空会报错。以下两项位于 YAML 顶层，与 `config` 并列：

```yaml
mihomo-path: data/mihomo
data-dir: data
```

进入界面时，如果有已下载订阅就自动启动；只有订阅 URL 时先下载再启动，没有订阅时保持停止。已有运行实例会直接接管，不重复启动。首次在“订阅设置”中填入 URL 并下载后，可按 `s` 启动。首版接受完整 mihomo YAML，保留订阅分流和策略组，不提供纯节点 URI/Base64 转换。在订阅设置内下载，校验成功后立即保存；代理正在运行时立即应用。订阅下载总超时为 2 分钟，可用 Ctrl+C 取消。下载失败不破坏已有缓存。

TUN 管理使用管理员身份，并保持相同启动工作目录；使用 `DATA` 时同时传入相同值：

```bash
sudo ./tim-linux-arm64
```

root 写入的配置可能无法再由普通用户读取；启用 TUN 后建议持续使用相同身份管理。工具不会自动修改文件所有者或在后台提权。

## 操作

主界面底部固定一行快捷键提示，直接按 `s/r/l/m/t` 控制启停、重启、日志、模式与 TUN，无需选中菜单项。其余操作通过菜单或对应字母快捷键执行。输入框和 YAML 编辑器内不会截获这些字母。表单可通过上下箭头或 Tab 切换输入项，支持鼠标点击；开关用 `( ) / (*)` 显示，按空格或 Enter 切换。订阅、基本配置、DNS 和链式前置页面的修改先保留为草稿，返回或 Esc 时有修改才询问“确认 / 取消”；确认会应用并保存，取消或弹窗内 Esc 会放弃草稿并返回主菜单。应用失败保留草稿，可再次返回并取消；切换字段不会保存或应用。编辑 `user.yaml` 停止输入 1 秒后自动校验并应用；Esc 返回前也会校验并保存，错误保留在编辑器中。长操作时 Ctrl+C 请求取消并等待清理/恢复；空闲时 Ctrl+C 或 `q` 退出界面。退出不会停止 mihomo。

- `s` 启动/停止切换，`r` 重启（停止时启动），`f` 重新读取真实状态。
- `c` 设置订阅 URL；返回时选择确认会下载、应用并保存新订阅。下载/刷新按钮仍可直接执行，失败保留原 URL 和订阅缓存。
- `m` 直接切换规则/全局，`t` 直接开启/关闭 TUN，无二次选择菜单。
- `d` DNS 配置：监听地址（默认 `0.0.0.0:1053`）、fake-ip、IPv6 解析、域名嗅探、四类上游及 Fake-IP 过滤列表，离开页面时有修改再确认应用并保存，或取消放弃草稿。
- `a` 链式前置代理：开关与独立节点输入，支持单节点 YAML、分享链接和 Base64；页面只保留返回按钮，返回或 Esc 时有修改则询问确认或取消。
- `p` 基本配置：端口、监听、延迟测试 URL、mihomo 路径和 data 目录；`o` 全屏查看完整操作结果，支持方向键、PageUp/PageDown 和 Home/End，Esc 返回。
- `e` 编辑 YAML；`v` 预览最终配置；底栏 `l` 查看日志，有新版本才在底栏显示 `[U 更新 版本号]` 并允许更新。

第一行仅显示状态、规则/全局、TUN 和当前内核版本；运行绿色、停止红色。底栏快捷键提示统一使用 `[L 日志]`、`[Esc 返回]` 格式并标黄；字母显示为大写，大小写均可触发。启动完成后才经代理访问 GitHub 官方 Release 页面查询新版本，不使用有匿名请求限流的 REST API。第二行右侧显示外网 IP 和国家代码（country.is）；停止时直连查询，启动后通过代理查询，查询失败不会退回直连。网络查询在后台进行。

运行时，第二行的 GLOBAL 当前节点和已启用的链式前置名称后显示 `⚡`。鼠标点击闪电发起延迟测试，测试中显示 `…`，成功后显示数字（单位为毫秒，界面不加单位）；点击数字可重新测试。失败恢复闪电，原因显示在操作日志栏，不弹窗。测速使用当前内核控制接口的 `/proxies/{name}/delay`，内核超时为 5000 毫秒。`p` 基本配置中可填写 HTTP/HTTPS 测试 URL，留空使用 `https://www.gstatic.com/generate_204`；地址保存在 `setting.yaml` 的 `delay-test-url`，确认应用后，下次点击测速即可使用新地址。界面上的数值是连接延迟，不是下载带宽；点击出口测的是该出口的实际路径（含配置的前置），点击前置只测前置节点。

操作结果区域及 `o` 打开的完整结果支持鼠标左键拖选、双击选词，右键复制。内容只读，Esc 返回。不设置应用内复制快捷键；`⌘C` 等系统快捷键由本地终端处理，需要终端原生选区（通常按住 Shift 拖选）。右键复制通过 OSC 52 发送到本机剪贴板，需要 SSH 客户端/终端支持并允许。

日志打开后直接定位末尾，每秒刷新最近 64 KiB；↑/PageUp/Home 暂停自动跟随，End 恢复跟随最新日志，Esc 关闭。

模式和 TUN 开关即时应用；运行中会短暂重启，停止中只保存。代理模式与 TUN 独立：全局模式不会自动开启 TUN。操作失败自动打开可滚动的全屏详情，保留完整内核校验输出；主界面操作结果固定为紧凑的 5 行，随时可按 `o` 展开。操作错误附时间戳追加到当前有效数据目录的 `error.log`；日志写入失败会明确提示。运行检查覆盖进程身份、控制 API、实际 TCP/UDP 监听和 TUN 接口存在性，不代表外网代理节点一定可达。

## 配置语义

`setting.yaml` 是 TUI 管理项的唯一来源：订阅 URL、端口/监听、允许局域网、运行模式、TUN、DNS、域名嗅探、链式前置和路径设置。文件结构为 `subscription_url`、`fake-ip-enabled`、`chain-proxy`（`enabled` 和原始 `input`）、`delay-test-url`、`mihomo-path`、`data-dir` 及 `config`（自动配置模板与管理字段）。`user.yaml` 专用于用户自定义，允许文件不存在；不存在时按无自定义配置处理，编辑器默认空白，空白内容表示无自定义配置。

生成顺序为订阅 → 根据 `setting.yaml` 生成自动配置 → `user.yaml` 最终覆盖。端口和 DNS 页面仅在自定义值与自动配置不同时显示配置来源，可打开详情；主界面操作结果只显示简短状态，如“配置已更新”“内核正在运行”“内核已启动”“内核已重启”。因此自定义相同字段后，界面管理值与最终运行值可以不同。普通 YAML 注释尽量保留；锚点/别名在解析时展开。

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

基本配置面板支持 Mixed、HTTP、SOCKS、代理监听 IP、控制接口、延迟测试 URL、mihomo 路径与 data 目录；DNS 监听地址在 DNS 配置页统一管理，默认 `0.0.0.0:1053`。HTTP/SOCKS 设置为 0 可关闭，代理入口默认仅开 Mixed。`redir-port`、`tproxy-port` 等可通过 YAML 配置，但本工具不负责配置对应转发规则。DNS 监听地址仅在 DNS 开启时生效。

### GLOBAL 记忆

生成的运行配置会管理一个 `GLOBAL` select 组：实际代理节点排在前面，保留订阅节点顺序，再接可选策略组和内置出口。provider-only 配置使用按配置顺序排列的 providers，避免初始退到 DIRECT。用户订阅中的其他策略组保持不变；已有同名 `GLOBAL` 的候选定义由上述规则重建，最终预览中可见。

选择状态只保存在 mihomo 的 `home/cache.db` 中，不另建 TUI 选择缓存。首次或历史名称失效时，内核使用当前第一个实际节点；有效历史选择优先恢复。界面显示当前 GLOBAL 出口。没有实际节点的全局模式拒绝应用。可通过 mihomo 控制 API 改变选择，之后由内核记忆。

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

生成配置时，TUI 为前置分配不冲突的内部名称，覆盖原出口节点的 `dialer-proxy` 及 provider 的 `override.dialer-proxy`。前置自身不被套入链路，也不会被 GLOBAL 或 `include-all`/`include-all-proxies` 自动组收录为普通出口。原订阅、用户文件和路由规则不被改写；关闭后重新生成原有配置。此处链式管理字段优先于 `user.yaml`，其余管理项仍沿用原有覆盖顺序。DNS、DIRECT 和订阅下载路径保持现有配置语义。

顶部第二行在 `GLOBAL` 之后显示 `链式前置: 我的前置`，依据已应用的运行配置，不把未应用的输入显示为已生效。输入无效、没有代理出口，或含前置的配置被内核拒绝而不带前置的原配置校验通过时，本次不启用链式，显示 `链式前置: 未生效（原因）`，有效订阅仍正常应用。订阅自身错误、文件错误和操作取消仍按原流程报告；前置网络不通则交给 mihomo，不自动绕过。

### TUN

自动模板采用 `demo.js` 的 system 栈、自动路由、出口识别、strict-route 和 DNS 劫持参数；TUN 开关只改变管理项 `tun.enable`，关闭不删除配套配置。DNS 页面不会自动开启 TUN。`user.yaml` 中明确的 TUN/DNS 自定义最终优先。

TUN 路由和模式变更可能影响 SSH；本工具不提供开机自启、崩溃自动拉起、局域网网关向导或系统代理设置。后台进程能否跨注销存活还受目标系统的会话清理策略影响。

## 数据与恢复

启动目录固定保存 `setting.yaml` 和配置操作锁 `operation.lock`。数据目录采用私有权限，并包含：

| 路径 | 用途 |
|---|---|
| `mihomo` | 默认 `mihomo-path` 指向的预装内核 |
| `user.yaml` | 最终用户自定义覆盖 |
| `subscription.yaml` | 最近成功应用的订阅 |
| `config.yaml` | 固定运行配置，应用失败时恢复原内容 |
| `state.json` | 已应用状态及运行进程身份；停止后清除进程记录。进程中的 binary 仅用于身份核验和失败回滚，不用于选择内核 |
| `home/` | mihomo 工作目录、原生缓存、provider 文件 |
| `kernels/` | 已验证的更新内核，原预装文件不改动 |
| `mihomo.log` | 内核输出 |
| `error.log` | 完整操作错误 |
| `operation.lock` | 防止不同启动目录同时修改同一个 data 目录 |

不读取或迁移旧版 `active.json`、`process.json` 和 `generations/`，不再从 `state.json.binary` 选择内核。`state.json` 暂时仍保存已应用标记与进程身份；运行路径始终由有效 data 目录生成。正常应用不创建历史配置目录，回滚所需旧内容只在操作期间保存在内存中。

仅管理本目录启动的实例，进程身份不符时不会按 PID 盲目停止。停止使用 SIGTERM，等待 12 秒；超时明确报错，不自动强杀。

配置先由 `mihomo -t` 校验，启动后再检查是否真正监听。失败时停止新实例并尝试恢复旧配置。回滚成功或失败都明确报告。多文件保存发生 I/O 错误时恢复已写入文件；突然掉电/强杀发生在跨文件提交中途的情形尚不提供事务日志恢复保证。

内核更新使用官方 GitHub 稳定 Release 的匹配架构资产，从官方资产页面取得 SHA-256，检查下载大小上限、响应 Content-Length（存在时）和 SHA-256、gzip 解压上限和 `mihomo -v`。缺失摘要拒绝更新。下载/验证阶段保持旧进程，切换时才重启；失败尝试恢复旧内核。保留工作目录及缓存，更新保留当前已应用配置。日志和旧内核暂不自动清理，长期运行需自行管理磁盘空间。

## 本地编译、远端测试

本地先运行配置、导入和界面单测，测试超时为 60 秒：

```bash
cd tui
go test -timeout=60s ./...
go vet ./...
```

依赖 Linux 进程锁、pidfd 或真实内核的测试在不满足条件时跳过；远程 Linux 用于部署和运行验证。如需单独运行 Linux 专属集成测试，可交叉编译测试二进制：

```bash
cd tui
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -c -o dist/core-linux-arm64.test ./internal/core
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -c -o dist/ui-linux-arm64.test ./internal/ui
```

传到 Linux 后执行，每个测试进程均设置 60 秒硬超时：

```bash
MIHOMO_TEST_BINARY=/absolute/path/mihomo timeout 60s ./core-linux-arm64.test -test.v -test.timeout=60s
timeout 60s ./ui-linux-arm64.test -test.v -test.timeout=60s
```

真实内核测试覆盖 GLOBAL 默认与缓存恢复、Linux 生命周期、端口冲突回滚和内核切换。更新流程测试使用本地 HTTP fixture 提供官方二进制，以验证下载校验、切换和缓存保留，不依赖测试过程中 GitHub 网络速度。

TUN 测试只在隔离网络命名空间运行，不直接改动远端 SSH 所在网络：

```bash
sudo unshare --net --mount --fork sh -c '
  mount --make-rprivate / &&
  mount -t sysfs sysfs /sys &&
  ip link set lo up &&
  ip link add test0 type dummy &&
  ip addr add 192.0.2.1/24 dev test0 &&
  ip link set test0 up &&
  ip route add default via 192.0.2.254 dev test0 &&
  MIHOMO_TEST_TUN=1 MIHOMO_TEST_BINARY=/absolute/path/mihomo \
    timeout 60s /absolute/path/core-linux-arm64.test \
    -test.run TestLinuxTunLifecycle -test.v -test.timeout=60s
'
```

此测试验证 rule/global 两种模式中的 TUN 接口、路由、传统 DNS 劫持和关闭清理，不替代真实订阅节点的外网连通性验证。

`scripts/pty_smoke.py` 在真实伪终端中启动/退出 TUI。将程序命名为 `tim-linux-arm64`、内核命名为 `mihomo` 放在同一测试目录，分别在两次 SSH 会话运行 `start` 和 `stop`，验证界面退出、SSH 断开后的持续运行与重新接管：

```bash
python3 pty_smoke.py start --directory /absolute/test-directory
python3 pty_smoke.py stop --directory /absolute/test-directory
```
