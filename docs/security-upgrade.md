# 安全版本升级

`scripts/update-security.sh` 固定下载 `v0.1.3-security.3`，按 Linux 架构选择
Release 压缩包，分别校验压缩包及离线升级助手的 SHA-256，再调用助手。
入口脚本本身应使用发布后确认的固定 SHA-256 校验；不能直接执行未校验的网络响应。
同一 GitHub Release 提供文件与校验值，可以发现损坏及错包，不能抵御整个发布账号被入侵。

只支持已有、运行中的标准 systemd 安装：`/usr/local/V2bX/V2bX`、`V2bX.service`。
升级需要 root，会重启服务并断开现有连接。保留配置、证书、规则库及上报设置；
旧可执行文件保存在 `/var/lib/v2bx-security-upgrade/backup-*`。
新进程启动失败或十秒检查期间退出时尝试恢复旧文件并启动服务。
启动成功仍须检查面板通信和实际客户端连通性，不能等价于生产全量验收。

对于支持的特殊架构之外的系统，可以手工下载对应压缩包，核实 SHA-256 后运行：

```bash
bash upgrade-security.sh --archive /absolute/path/to/V2bX-platform.zip --sha256 VERIFIED_SHA256
```

应用升级不能修补 Linux 内核。仍须安装厂商对 CVE-2026-98432 的内核更新并启动新内核；
暂时关闭 MPTCP 的方法见 `security-mptcp.md`。

发布工作流先验证源码，所有构建固定到通过验证的同一提交；附件上传到草稿，
重新下载并验证全部 SHA-256 后才公开。已公开版本禁止由该工作流覆盖。
