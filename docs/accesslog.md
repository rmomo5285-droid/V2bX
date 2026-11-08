# 访问分析与在线设备心跳

## 数据流

原生 Hysteria2、Xray、sing-box 在各自请求/路由回调中采集目标，经同一个有界队列，
使用 HTTPS 批量发送 JSONEachRow 到分析服务器。分析服务器写入 ClickHouse，
across_log-panel 读取 `access_log`，按 UID、域名和时间查看访问分布及画像。
Xboard 继续使用现有 API 提供用户、节点、流量和在线设备信息，无需修改数据库。
节点不需要安装 Elise-Backend、ClickHouse 或面板，也不直接使用 MySQL。

上报默认关闭。配置中没有固定域名、密码或数据库地址。一个 V2bX 实例中的所有节点
共用发送器，事件带各自的节点 ID 和 Xboard 用户数字 UID。

## 现有安装启用

先安装**包含本功能的版本**；旧版本不会识别 `accesslog` 命令。在节点终端执行：

```bash
/usr/local/V2bX/V2bX accesslog configure --endpoint https://logs.example.com/ingest --username v2bx_writer && systemctl restart V2bX
```

根据提示输入分析服务器的 **writer 密码**，终端不回显。这里不是 SSH 密码、面板登录密码，
也不是 Xboard API Key。目标域名按实际部署替换。命令在主配置旁生成权限 0600 的
`accesslog.json`，不改写现有 `config.json`、节点、证书及核心配置。
主配置不是默认位置时，增加 `--config /实际路径/config.json`。

自动化可将密码从权限 0600 的文件输入，避免出现在命令参数和 shell 历史中：

```bash
/usr/local/V2bX/V2bX accesslog configure --endpoint https://logs.example.com/ingest --username v2bx_writer --password-stdin < /root/writer.password
systemctl restart V2bX
```

也可以复制 `example/accesslog.example.json` 为主配置旁的 `accesslog.json`，将
`Enabled` 改为 true，设置端点、用户名及 `PasswordFile`。相对密码路径以主配置目录为准。
`Password` 和 `PasswordFile` 二选一。主配置内也支持同样字段的 `AccessLog` 对象，
同目录 sidecar 存在时优先。**修改 sidecar 后重启服务**。

## 一次升级并配置上报

发行包附带 `upgrade-accesslog.sh`。该脚本支持标准 systemd 安装的 Linux amd64/arm64，
下载指定发行版 ZIP 及 SHA-256 清单，验证并暂存新程序、先验证上报配置，再停止服务替换。
启动失败会恢复旧程序和旧上报配置。原本停止的服务升级后保持停止。
不要使用仍不包含本功能的旧 Release；开发分支推送不会自动变成正式 Release。

```bash
bash upgrade-accesslog.sh --version vX.Y.Z --endpoint https://logs.example.com/ingest --username v2bx_writer
```

本地构建包也可升级，校验值来自你已核对的构建产物：

```bash
bash upgrade-accesslog.sh --archive /root/V2bX-linux-64.zip --sha256 ACTUAL_SHA256 --endpoint https://logs.example.com/ingest --username v2bx_writer
```

脚本可加 `--password-file /root/writer.password`。备份在
`/var/lib/v2bx-accesslog-upgrade/backup-*`，其中上报配置含密码，目录权限 0700。
该脚本保留旧管理器；以后用旧管理器降级到不含本功能的版本会停止上报。
自定义程序目录、Docker 和其他架构使用原有部署方式安装新程序，再执行配置命令。

## 分析服务器接口

推荐将 HTTPS `/ingest` 固定代理到 ClickHouse 的 INSERT 查询，限制请求体大小
（至少 64 KiB，建议 128 KiB），并使用仅有目标表 INSERT 权限的专用 writer。
认证通过 `X-ClickHouse-User`、`X-ClickHouse-Key` 请求头传递；面板用独立 reader。
不要将 ClickHouse 原生端口暴露给公网。成功应返回空响应体 2xx；错误应返回非 2xx。
代理需等待 INSERT 执行结果，例如设置 `async_insert=1`、`wait_for_async_insert=1`、
`wait_end_of_query=1`。返回 200 但有异常码/非空正文视为结果不确定。

兼容已部署 across_log-panel 的建表示例：

```sql
CREATE TABLE IF NOT EXISTS default.access_log (
  time DateTime('UTC'),
  network LowCardinality(String),
  node_id Int32,
  user_id Int64,
  user_ip String,
  host String,
  dest_ip String,
  dest_port UInt16
) ENGINE = MergeTree
PARTITION BY toDate(time)
ORDER BY (time, host, user_id)
TTL time + INTERVAL 7 DAY DELETE;
```

需要直接对接 ClickHouse HTTPS HTTP 接口时，配置命令增加
`--backend clickhouse_http --database default --table access_log`；Endpoint 指向 HTTP 接口，
程序构造并固定 INSERT 查询。HTTPS 证书必须有效，仅 loopback 地址允许 HTTP 测试。
不接受 URL 内嵌凭据，不跟随重定向，防止 writer 凭据被转发到其他目标。

使用 Cloudflare 橙云且源站 Caddy 自动 HTTPS 时，将 SSL/TLS 模式设为「完全（严格）」。
「灵活」模式可能使 HTTPS 请求被源站 HTTP 入口重定向回自身，状态里出现 308；
此时应修正代理的 TLS 配置，上报器不会追随重定向。

示例行：

```json
{"time":"2026-11-01 00:00:00","network":"tcp","node_id":3,"user_id":42,"user_ip":"198.51.100.1","host":"example.com","dest_ip":"","dest_port":443}
```

域名来自核心已知目标或已有嗅探结果；目标本身为 IP 时 `dest_ip` 填该 IP。
不会为日志额外做 DNS 查询。保持已有嗅探设置即可，未能识别域名时显示 IP。
这记录核心观察到的连接/UDP 会话请求，不代表访问成功；不是 HTTP 页面浏览次数，
不包含完整 URL、HTTPS 内容或请求正文。原生 Hy2 和 sing-box 的 UDP 记录是会话初始目标，
同一会话后续逐包改变的目标未逐包采集。不同 Xboard 的 UID 可能重叠，应分库/分表部署分析。

## 内存、失败与保留

默认队列 256 条、估算负载上限 256 KiB，批次最多 64 条/64 KiB，5 秒刷一次，
单请求超时 10 秒，只有一个发送 worker 和一个在途请求。队列满直接丢弃并计数，
不上报路径中等待网络，不写本地访问文件。队列加批次只是数据缓冲预算，
不等于 Go 进程总 RSS，TLS、运行时和核心本身另占内存。

本版本使用尽力而为发送：故障时会丢记录；不自动重试，避免 MergeTree 在超时/未知
提交结果时产生重复。`failed` 是 HTTP 拒绝，`uncertain` 是网络错误或异常响应，
`dropped` 是队列满/退出期间丢弃。未提供持久化队列和精确一次交付。
关停最多等待约 3 秒，随后取消请求并清理缓冲。统计每 30 秒更新，重启后重新计数。

上述计数都是本进程启动后的累计值，历史 `uncertain` 非零不能证明当前仍在
超时。告警改为只在本次 30 秒统计窗口新增 failed/uncertain/dropped 时记录，
同时提供 `interval_failed`、`interval_uncertain`、`interval_dropped`；恢复发送
后记录一次 info，不再每 30 秒重复历史错误。累计值仍保留在状态文件中。
`last_error_kind` 和 `last_failure` 保留最近错误的分类与时间，区分 timeout、
response_timeout、dns_timeout、HTTP 拒绝及异常正文，不保存原始错误、URL、
请求头、凭据或事件正文。HTTP 200 仅代表最近一次收到的状态码，响应正文读取
超时仍计 uncertain。

## 与计费同时运行的超时压力验证

`vision-capacity.yml` 包含 `timeout-recovery` 场景：保持 150/300 个不同 UID、
每用户 4 条真实 TLS Vision 下载连接，另以 4 个 worker 提供最多 50 次/秒
真实短连接。访问事件从 Xray 路由钩子产生，不直接向队列填造事件。
所有日志参数保持生产默认值。日志接口先正常，再交替卡住响应头、2xx 后的
响应体，最后恢复；流量 push、用户拉取和设备心跳同时运行。

测试要求触发 uncertain 和 dropped，队列不超过 256 条/256 KiB，恢复后
Sent 继续增加，空闲后队列排空；长连接期间 push 年龄不能超过 10 秒，用户
设备数必须匹配。每个容器复用节点三轮，禁止通过重启或显式 GC 清场。
日志超时不等于流量接口超时；生产环境应区分 `Access log delivery status`
与 `Report user traffic failed`、`Get user list failed`，再检查对应接口。

2026-11-08 Windows 的 150 UID 测试（GOMAXPROCS=1，70 秒活跃、70 秒空闲）
通过，600 条长连接外完成 3,456 条短连接，0 个客户端错误；日志排队峰值
256 条/45,312 估算字节，1,676 条 dropped、132 条 uncertain，恢复后 Sent
增加至 2,248。流量报告年龄采样低于 1 秒，在线设备 150，空闲后设备及受管理
连接均为 0。进程 RSS 采样约 365–376 MB，包含客户端、目标及模拟面板；
不是节点单独内存，也不是 Linux 1 GiB cgroup 或真实服务器容量结论。
Linux 对照结果需查看对应修复提交的 Actions 工件，不能用这轮 Windows
结果宣称生产 timeout 已定位或修复。

```bash
/usr/local/V2bX/V2bX accesslog status
journalctl -u V2bX --since '10 minutes ago'
```

检查 `sent`、`last_success` 是否推进，再到分析面板核对该节点/UID；仅队列 accepted
增加不代表已写数据库。状态文件仅有计数，不包含凭据和访问内容。
数据库磁盘随请求量变化，应使用真实日增量规划容量；TTL 后台清理并非到点立即删除。

关闭：

```bash
/usr/local/V2bX/V2bX accesslog disable && systemctl restart V2bX
```

## Xboard #1081 同类漏报修复

在线心跳与流量结算分开，每 60 秒读取活跃连接快照。长连接、静默 QUIC 连接不会因为
一次统计窗口被清空而失踪；同一用户、同一 IP 多连接引用计数，最后连接关闭才删除。
Hy2 每条认证 QUIC 会话有独立内部身份，流量仍归原用户 UUID；NAT 端口变化不影响释放。
已知连接迁移到新 IP 时，在后续请求回调更新来源。

保留 `DeviceOnlineMinTraffic` 的探针过滤；达到用户流量门槛后，该活跃 IP 在连接存续期间
保持资格，静默周期不清空。该门槛按用户流量判断，不能精确区分同用户多个 IP 的流量。
发送离线 UID 的空列表，只有 HTTP 成功才推进基线，失败继续后续心跳。
断线检测仍受核心/QUIC 空闲超时影响。本次不修改原有计费协议或承诺解决所有面板计数问题。
