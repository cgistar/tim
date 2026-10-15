# 开发、测试与发布

[返回使用说明](../README.md)

以下命令均在仓库根目录执行，构建需要 Go 1.26。

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o dist/tim-linux-arm64 ./cmd/tim
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o dist/tim-linux-amd64 ./cmd/tim
```

## GitHub Actions 编译与发布

[Release 工作流](../.github/workflows/release.yml) 在推送 `vX.Y.Z` 版本标签时自动执行，也支持在 GitHub 的 **Actions → Release → Run workflow** 中填写已有标签。手动运行会检出所填标签的代码；工作流需已存在于默认分支。标签应指向包含工作流和待发布代码的提交。

```bash
git tag -a v1.0.0 -m "Release v1.0.0"
git push origin v1.0.0
```

工作流先在 Linux 上执行 `go test -timeout=60s ./...` 和 `go vet ./...`，通过后使用 `go.mod` 声明的 Go 版本、`CGO_ENABLED=0` 并行交叉编译 Linux amd64、arm64，再统一创建 GitHub Release。任何测试、编译或打包失败都会阻止发布。当前 tim 的运行检查仅支持这两个 Linux 架构，不发布 macOS、Windows 或其他架构产物。

每次 Release 包含：

- `tim-v1.0.0-linux-amd64.tar.gz`
- `tim-v1.0.0-linux-arm64.tar.gz`
- `SHA256SUMS`：两个压缩包的 SHA-256 校验值。

压缩包内为同名目录，包含可执行文件 `tim` 和 `README.md`，不包含 mihomo、配置或运行数据。下载对应压缩包及 `SHA256SUMS` 后，可验证并解压：

```bash
sha256sum --check --ignore-missing SHA256SUMS
tar -xzf tim-v1.0.0-linux-amd64.tar.gz
cd tim-v1.0.0-linux-amd64
./tim
```

`v1.0.0-rc.1` 等带预发布标识的标签会创建 GitHub 预发布版本；正式标签创建正式版本，发布说明由 GitHub 自动生成。发布使用仓库自带的 `GITHUB_TOKEN`，仅发布任务申请 `contents: write`，无需额外配置 PAT。已存在的 Release 不会被覆盖；发布步骤失败后若留下草稿，需先检查并删除该草稿，再重新运行失败任务。

## 本地编译、远端测试

本地先运行配置、导入和界面单测，测试超时为 60 秒：

```bash
cd /path/to/tim
go test -timeout=60s ./...
go vet ./...
```

依赖 Linux 进程锁、pidfd 或真实内核的测试在不满足条件时跳过；远程 Linux 用于部署和运行验证。如需单独运行 Linux 专属集成测试，可交叉编译测试二进制：

```bash
cd /path/to/tim
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -c -o dist/core-linux-arm64.test ./internal/core
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -c -o dist/ui-linux-arm64.test ./internal/ui
```

传到 Linux 后执行，每个测试进程均设置 60 秒硬超时：

```bash
MIHOMO_TEST_BINARY=/absolute/path/mihomo timeout 60s ./core-linux-arm64.test -test.v -test.timeout=60s
timeout 60s ./ui-linux-arm64.test -test.v -test.timeout=60s
```

在 systemd Linux 上可单独运行隔离服务测试（需要 root，测试创建自己的临时配置和服务，结束后卸载）：

```bash
MIHOMO_TEST_SYSTEMD=1 MIHOMO_TEST_BINARY=/absolute/path/mihomo \
  timeout 60s ./core-linux-arm64.test -test.run '^TestLinuxSystemd(ManagedLifecycle|KernelUpdateKeepsNativeSelection)$' -test.v -test.timeout=60s
MIHOMO_TEST_SYSTEMD=1 \
  timeout 60s ./core-linux-arm64.test -test.run '^TestLinuxSystemdContinuousRetryAndForceStop$' -test.v -test.timeout=60s
```

在 OpenRC Linux 上验证启动服务、更新后恢复及强停（同样需要 root 和隔离目录）：

```bash
MIHOMO_TEST_OPENRC=1 MIHOMO_TEST_BINARY=/absolute/path/mihomo \
  timeout 60s ./core-linux-arm64.test -test.run '^TestLinuxOpenRC(ManagedLifecycle|StartRejectsExternalProcess|KernelUpdateKeepsNativeSelection)$' -test.v -test.timeout=60s
MIHOMO_TEST_OPENRC=1 \
  timeout 60s ./core-linux-arm64.test -test.run '^TestLinuxOpenRCForceStop$' -test.v -test.timeout=60s
```

快速退出诊断测试 `TestLinuxOpenRCContinuousRetryAndForceStop` 保留持续重试断言；OpenRC 0.63.2 上已知失败，见上述原生监督限制。

真实内核测试覆盖 GLOBAL 默认与缓存恢复、Linux 生命周期、启动不改写配置、停止态订阅生成配置、应用失败保留新配置和内核切换。更新流程测试使用本地 HTTP fixture 提供官方二进制，以验证下载校验、切换和缓存保留，不依赖测试过程中 GitHub 网络速度。

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

断网保护的真实发包测试同样只在隔离 namespace 执行，需要 nftables、完整 iptables/ip6tables、`unshare`、`nsenter` 和 `ip`。挂载命名空间里的 sysfs 必须重新挂载，避免将宿主机接口视为测试接口：

```bash
sudo unshare --mount --net sh -c '
  mount --make-rprivate / &&
  mount -t sysfs sysfs /sys &&
  TIM_GUARD_TEST=1 MIHOMO_TEST_BINARY=/absolute/path/mihomo \
    /absolute/path/core-linux-arm64.test \
      -test.run TestLinuxGuardPackets -test.v -test.timeout=60s
'
```

测试创建独立 veth 与服务端命名空间，验证双栈 TCP/UDP、已有连接、53/853、私网、标记出站、真实 mihomo DIRECT 出站和 TUN、停止后的保护保留、规则缺失后的恢复及其他程序规则保留；不会加载规则到宿主机网络。

`scripts/pty_smoke.py` 在真实伪终端中启动/退出 TUI。将程序命名为 `tim-linux-arm64`、内核命名为 `mihomo` 放在同一测试目录，分别在两次 SSH 会话运行 `start` 和 `stop`，验证界面退出、SSH 断开后的持续运行与重新接管：

```bash
python3 pty_smoke.py start --directory /absolute/test-directory
python3 pty_smoke.py stop --directory /absolute/test-directory
```

在 systemd 测试机加 `--service` 分别运行 `start` 和 `stop`，覆盖菜单安装、动态标签、退出后保活、CLI 取消/EOF 不卸载、菜单取消及确认卸载。脚本使用独立配置与服务，`stop` 成功后移除该测试服务。
