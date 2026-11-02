# Xboard / Xboard-Node / v2node 适配记录

检查日期：2026-11-02。本次在 V2bX 原有多核心结构中实现兼容改进，没有把其他后端整体替换进来。下面是源码审查和本地模拟协议测试结果，不代表已在用户的面板实例部署验证。

## 核对的版本

| 项目 | 分支及提交 | 用途 |
| --- | --- | --- |
| cedar2025/Xboard | master `d826a3b32f73bde24acce2a3ab033c6538ce121d` | 实际服务端路由、鉴权、用户、流量和在线设备语义 |
| cedar2025/Xboard-Node | dev `0a29338e1f102a462363ce3527417029f89bab28` | 流量失败恢复、设备跟踪、v2/机器鉴权、证书管理和推送设计 |
| wyx2685/v2node | main `dde9f237fad986dabe662db18025fb9ae349b5bf` | 新版 Xray/REALITY 依赖及后台任务生命周期设计 |

## 已吸收或修复

1. **失败流量保留与合并重试。** 参考 Xboard-Node 的 `internal/service/service.go` 中上报失败后 `RestoreTraffic` 的设计。V2bX 原逻辑先清零核心计数，请求失败后丢弃整批数据。现在按 UID 保留待确认流量，下一周期合并新增量，收到成功响应后才清除；上报和计数清零串行执行，覆盖任务替换时旧请求尚未结束的情况。实现独立编写，适配现有三个核心接口。
2. **兼容 Xboard v2 与机器令牌的静态节点。** 参考 Xboard-Node 的面板客户端，并按 Xboard `ServerV2.php`、`V2/ServerRoute.php` 核实接口。新增 `ApiProtocol: "xboard-v2"` 与 `MachineID`；v2 请求使用 `/api/v2/server/{config,user,push,alive,alivelist}`，不发送旧 `node_type` 过滤器。`MachineID` 自动选择 v2，使用该机器的令牌和显式 NodeID，服务器仍会验证节点属于该机器。
3. **准确处理用户撤销。** Xboard `getAvailableUsers` 会在权限组为空、用户到期、禁用或流量用尽时返回空数组。现在区分 HTTP 304 的 nil 与成功返回的 `[]`：前者不改变用户，后者删除所有现有用户。首次启动也允许没有可用用户的节点，等待后续轮询。
4. **保留跨节点设备限制。** Xboard 的 `alive` 会替换每个 UID 在该节点的 IP 列表，故保留现有定时刷新以及离开用户的空数组，不照搬“空数据都不发送”。`alivelist` 请求失败不会再被伪装为空快照并清除上次设备数，同时故障不阻止用户撤销。
5. **配置与用户缓存可重试。** 节点配置完成校验后才保存 ETag/正文哈希；用户 JSON/MessagePack 必须是完整响应，缺失/null 用户数组、截断或尾随数据均拒绝。有效响应在核心应用失败时会清除对应缓存，以便下一周期重试同一个版本。用户拉取失败时，也不会丢掉同一轮刚收到的节点配置。
6. **后台任务停止/重启隔离。** 参考 v2node 对任务生命周期和定时器的关注，独立修复当前 V2bX 的 Close 与回调报错双重关闭 channel、旧回调误停新任务的问题。每次运行保留独立停止信号，同一 Task 的跨代回调串行；回调可以自行 Close。复用 timer，并拒绝无效间隔和空回调。没有直接移植超时后另外启动工作 goroutine 的机制：忽略取消的旧回调可能继续修改核心。
7. **安全维护依赖。** 已对齐 v2node 当日 Xray/REALITY 依赖；升级 Go 1.27.3 与其他安全依赖，补上旧 sing-box 接口适配及 QUIC 漏洞回移。详见 `security-mptcp.md` 和发行说明。

## 配置

现有 UniProxy 配置可以继续使用，无需添加字段。较新的 Xboard 可在原节点配置中增加：

```json
{
  "ApiHost": "https://panel.example.com",
  "ApiProtocol": "xboard-v2",
  "ApiKey": "YOUR_SERVER_TOKEN",
  "NodeID": 123,
  "NodeType": "vless"
}
```

机器令牌模式再增加 `"MachineID": 12`，并将 `ApiKey` 改成该机器令牌；NodeID 必须属于该机器。保留原来的核心、监听、证书等配置，仅合并这些 API 字段，不要用上面的片段覆盖整份配置。若节点使用嵌套 `ApiConfig`，把字段放在其中。旧面板未提供 v2 接口时保持默认 UniProxy。

当前核对的 Xboard 中间件读取请求参数中的 `token`，不支持用 Bearer 头直接替代它。GET 因此仍带令牌查询参数，应使用 HTTPS 并在反代日志中隐藏查询参数。未擅自改变服务端鉴权约定，也未声称已经消除 URL 令牌暴露。

## 尚未接入的架构功能

Xboard-Node 的 WebSocket 用户推送、自动发现机器下的全部节点、多实例管理、面板下发 PEM/ACME 设置、自动切换代理核心，以及面板自定义出站和复杂路由，属于独立控制面与核心适配工作，本次没有实现。手工填写 MachineID/NodeID 不等于自动机器管理。本次保留 V2bX 的本地证书、现有路由和轮询机制；新面板有同名字段也不表示 V2bX 已支持这些扩展。不能仅以 Xboard-Node 的更新时间或功能数判断其已处理本次 Linux 漏洞。

## 运行边界

失败流量保存在当前控制器内存中，不是磁盘账本。进程退出、崩溃或重建控制器会丢失未确认数据。网络超时也可能发生在面板已经入账之后；核对的 Xboard `processTraffic` 没有请求幂等键，因此重试不能保证恰好入账一次，极端情况下可能重复。此限制也适用于参考后端的失败恢复设计，需要面板与节点协作才能消除。

控制器关闭时会取消面板 HTTP 请求，并在核心读写回调退出后再删除节点；旧轮询代次不能与新代次并发应用配置。单个 Task 的 Close 仍不等待自身回调，核心操作和 ACME 续期不具备强行取消能力。上报仅接受 `{"data":true}`，不跟随重定向，POST 不进行 Resty 自动重试；待确认批次仍在下个上报周期重试，因此超时重复入账的边界仍然存在。

本地测试覆盖三个鉴权模式的五个端点、空 JSON/MessagePack 与 304 的区别、无效用户响应不推进缓存、无效节点配置不推进缓存、在线查询失败保留快照、空列表撤销和核心故障后的重试、零用户启动、失败流量合并、任务回调停止/重启。已交叉构建 Linux 两种架构；新增三个核心的真实监听、VLESS/原生 Hysteria2 TCP 转发与双向计费测试，面板使用本地 Xboard HTTP 模拟服务。真实面板实例、全部协议配置和 WebSocket 均未验证。Linux 实际运行结果以 `production-audit.md` 的 CI 记录为准。

## 源码资料

- [Xboard 服务端接口](https://github.com/cedar2025/Xboard/blob/d826a3b32f73bde24acce2a3ab033c6538ce121d/app/Http/Controllers/V1/Server/UniProxyController.php)
- [Xboard v2 鉴权](https://github.com/cedar2025/Xboard/blob/d826a3b32f73bde24acce2a3ab033c6538ce121d/app/Http/Middleware/ServerV2.php)
- [Xboard 流量、用户和设备语义](https://github.com/cedar2025/Xboard/blob/d826a3b32f73bde24acce2a3ab033c6538ce121d/app/Services/ServerService.php)
- [Xboard-Node 上报失败恢复设计](https://github.com/cedar2025/Xboard-Node/blob/0a29338e1f102a462363ce3527417029f89bab28/internal/service/service.go)
- [Xboard-Node 面板客户端](https://github.com/cedar2025/Xboard-Node/blob/0a29338e1f102a462363ce3527417029f89bab28/internal/panel/client.go)
- [v2node 后台任务](https://github.com/wyx2685/v2node/blob/dde9f237fad986dabe662db18025fb9ae349b5bf/common/task/task.go)

本次参考上述接口和处理思路，新增应用逻辑独立实现；没有复制 Xboard-Node 的整段实现或第三方资源。安全依赖回移另见 `third_party/README.md`，保留其来源和许可证。
