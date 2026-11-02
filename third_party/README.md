# Pinned sing-box integration

`sing-box/` is the unmodified source of the previously pinned module
`github.com/wyx2685/sing-box_mod` at
`v1.13.0-alpha.5.0.20251202212447-8d054dcd8bfe`, except:

- `common/tls/reality_server.go`: convert the existing X25519 private-key bytes
  to `*ecdh.PrivateKey`, matching the current REALITY fork used by Xray.

The source and its GPL license are included so builds do not modify the Go
module cache or depend on an unpublished remote fork. The rest of the fork's
panel integration is retained. The main module pins `sing` to
`v0.8.0-beta.10` and `sing-quic` to `v0.6.5` for this fork's API compatibility.
`http2legacy` is required with Go 1.27 because the fork accesses private HTTP/2
connection-pool internals. That tag selects x/net's maintained legacy transport
implementation at the updated x/net version; it does not downgrade x/net.

This is a narrow integration patch, not a claim that the old sing-box fork is
fully audited or equivalent to current official sing-box. Future upgrades
should remove its unsafe HTTP/2 dependency and update the panel hooks together.

## QUIC security backport

`quic-go/` is pinned from `github.com/sagernet/quic-go` at
`v0.59.0-sing-box-mod.5`, with its BSD license retained. In `http3/headers.go`,
the trailer parser and validators are backported from upstream commit
`c56e8c79d1627cc1ed6005b421b4b0adadd83665` (as present in
`v0.61.0-sing-box-mod.12`). This fixes CVE-2026-40898 / GO-2026-5676 by enforcing
the decoded trailer field-section budget, including the 32-byte per-field
overhead, and validates trailer names/values. Ordinary header limits were
already present. The backport keeps the congestion-control API required by
sing-quic 0.6.5.

`http3/security_trailers_test.go` checks decoded expansion, incremental early
termination, invalid fields and valid trailers. Run from the main module:

```
go test -tags=http2legacy github.com/sagernet/quic-go/http3
```

Go vulnerability tools do not automatically map all upstream advisories to
renamed forks or local replacements. This backport was checked in source and
tested separately; a clean scanner result alone would miss it.
