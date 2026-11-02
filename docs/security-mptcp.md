# Linux TCP / MPTCP security mitigation

Reviewed 2026-11-02 for CVE-2026-98432.

This release requires Go 1.27.3 and sets `godebug multipathtcp=0` in the
main module. Docker and CI use the same Go version. This disables **default**
MPTCP sockets and mitigates the publicly demonstrated exploit path. It does
not fix the underlying Linux IPv4 TCP use-after-free vulnerability.

Go 1.24 and newer default to MPTCP listeners on Linux. An affected host with
a reachable MPTCP listener can expose the demonstrated unauthenticated remote
kernel-code-execution path, before proxy protocol authentication. Application
UID and TLS do not fix a kernel networking bug. This is not the unrelated
Copy Fail local privilege escalation vulnerability.

## Configuration exceptions

An environment setting such as `GODEBUG=multipathtcp=1` or `=2` overrides
the build default. Explicit `net.ListenConfig.SetMultipathTCP(true)` calls
override both defaults. The pinned Xray fork supports this through `TcpMptcp`
and sing-box through `TCPMultiPath` (`tcp_multi_path` in sing-box JSON).
Avoid enabling these options until the host kernel is fixed. The same warning
applies to custom inbounds, certificate/masquerade HTTP listeners and any
other Go services sharing the host.

## Existing servers

1. Install the distribution/vendor kernel update specifically fixing this CVE,
   then boot the fixed kernel. Check the vendor's full package version: distro
   backports cannot be reliably assessed with a generic `uname -r` comparison.
   Updating the Go installation alone does not change an existing Go executable.
2. Until then, disable MPTCP in the host's relevant network namespaces:
   `sysctl -w net.mptcp.enabled=0`. Persist that setting using the distribution's
   sysctl configuration mechanism. This affects all services using that network
   namespace, not only V2bX.
3. Restart/recreate services with existing MPTCP sockets; changing defaults
   does not close sockets already open. Plan for interrupted proxy connections.
   Containers share the host kernel and may have separate network namespaces.
4. Rebuild and install V2bX with Go 1.27.3. For existing binaries, service-level
   `GODEBUG=multipathtcp=0` is an additional default mitigation. Preserve other
   GODEBUG options when updating an existing service environment.

Neither a zero sysctl value nor a successful Go vulnerability scan proves the
kernel is fixed. The Go Security Team warns that the underlying flaw is not
exclusive to MPTCP and that other exploit paths may exist. One remaining exposed
MPTCP listener can leave the host vulnerable.

## Build and compatibility

Use Go 1.27.3 with `GOEXPERIMENT=jsonv2`. The old sing-box fork requires the
`http2legacy` build tag with Go 1.27. CI, Docker and release builds include it.
Run tests with `go test -tags=http2legacy ./...` and build all cores with:

```
go build -tags "http2legacy sing xray hysteria2 with_quic with_grpc with_utls with_wireguard with_acme with_gvisor" .
```

The updated Xray fork removes `srtp`, `tls`, `utp`, `wechat-video` and
`wireguard` transport headers, Shadowsocks `none`/`plain`, and the old
`DisableIVCheck` switch. Check custom configs and client profiles before
upgrading. This does not remove TLS/REALITY security transports. The updated
Hysteria2 uses a multi-packet UDP sniff hook and checks its outbound ACL when
a session changes target. The V2bX wrapper also rechecks the current panel
rules and underlying ACL on each forwarded packet.

The sing-box QUIC fork has a separate source backport for CVE-2026-40898:
decoded HTTP/3 trailers now have the same field-section size budget as headers.
The backport includes bounds and validation tests. A vulnerability scanner
matching only the original upstream module name can miss this fork entirely.

The remaining Hysteria `GO-2026-5288` finding has no fixed version in the
current Go advisory database. V2bX now rejects more than 64 KiB of held UDP
payload before invoking any third-party request hook. This bounds parser input
in the configured Hysteria path; it is defense in depth and is not represented
as proof that the upstream advisory itself has been fixed.

The preserved sing-box fork is included under `third_party/sing-box` with a
narrow REALITY compatibility patch. Its source, provenance and compatibility
pins are documented in `third_party/README.md`. This is not a full migration
to current official sing-box or v2node.

An offline, binary-only systemd upgrade helper is provided in
`scripts/upgrade-security.sh`. It requires the local archive and its SHA-256,
backs up the executable, and rolls back when the service fails its initial
10-second check. It is for an existing standard installation; it does not
install a kernel update or prove client/panel connectivity.

## References

- [Go Security Team announcement](https://www.openwall.com/lists/oss-security/2026/10/30/1)
- [AWS advisory and mitigation limits](https://aws.amazon.com/security/security-bulletins/2026-160-aws/)
- [Ubuntu kernel package tracker](https://ubuntu.com/security/CVE-2026-98432)
- [v2node update](https://github.com/wyx2685/v2node/commit/5935d2eac6ea93fc2cfa04663d3db6a1f4866cc9)
- [Go security release history](https://go.dev/doc/devel/release)
- [QUIC trailer expansion advisory](https://github.com/quic-go/quic-go/security/advisories/GHSA-vvgj-x9jq-8cj9)
- [QUIC upstream fix](https://github.com/quic-go/quic-go/commit/c56e8c79d1627cc1ed6005b421b4b0adadd83665)
