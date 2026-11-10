# QuantStore CLI (`qs`)

Standalone Go command-line client for QuantStore's programmatic account API.
It is an independent module and does not import web-core or subproduct
`internal` packages.

## Download and install / 下载与安装

Full CLI source, tests, build files, and the required catalog/hash libraries are included under the [MIT License](LICENSE).
本仓库包含完整 CLI 源码、测试、构建文件及必要的目录与哈希库，采用 MIT 许可证。

[Latest release / 最新版本](https://github.com/quant-store/quantstore-cli/releases/latest)

| Platform / 平台 | Executable / 程序 |
| --- | --- |
| macOS Apple Silicon | [qs-darwin-arm64](https://github.com/quant-store/quantstore-cli/releases/download/v0.1.1/qs-darwin-arm64) |
| macOS Intel | [qs-darwin-amd64](https://github.com/quant-store/quantstore-cli/releases/download/v0.1.1/qs-darwin-amd64) |
| Linux x86_64 | [qs-linux-amd64](https://github.com/quant-store/quantstore-cli/releases/download/v0.1.1/qs-linux-amd64) |
| Linux arm64 | [qs-linux-arm64](https://github.com/quant-store/quantstore-cli/releases/download/v0.1.1/qs-linux-arm64) |
| Windows x86_64 | [qs-windows-amd64.exe](https://github.com/quant-store/quantstore-cli/releases/download/v0.1.1/qs-windows-amd64.exe) |

macOS / Linux: download the matching file, rename it to `qs`, then run:
下载对应文件并重命名为 `qs`，执行：

```sh
chmod +x qs
./qs version
sudo install -m 755 qs /usr/local/bin/qs
```

Windows: rename the downloaded file to `qs.exe` and run `.\qs.exe version` in PowerShell.
Windows 下载后重命名为 `qs.exe`，在 PowerShell 运行 `.\qs.exe version`。

## Build from source / 源码构建

Download this repository's source archive, extract it, and run these commands in the extracted directory. Go 1.23 or newer is required.
下载本仓库源码压缩包并解压，在源码目录执行以下命令，需要 Go 1.23 或更新版本。

```sh
go test ./...
go vet ./...
go build -buildvcs=false -trimpath -o qs ./cmd/qs
./qs version
```

Release version metadata can be set with `make build VERSION=0.1.1` on macOS/Linux, or `.\build.ps1 -Version 0.1.1` on Windows.
macOS/Linux 使用 `make build VERSION=0.1.1`，Windows 使用 `.\build.ps1 -Version 0.1.1` 可写入版本信息。

## Build and test

Requirements: Go 1.23 or newer.

Platform-neutral development commands:

```bash
go test ./...
go build -buildvcs=false -trimpath -o qs ./cmd/qs
```

Release-shaped local builds use the platform helper:

```bash
# macOS / Linux
cd quantstore-cli
make test
make build VERSION=0.1.1 BUILD_DATE=2026-07-12T00:00:00Z
./bin/qs version
```

```powershell
# Windows PowerShell
Set-Location quantstore-cli
go test ./...
.\build.ps1 -Version 0.1.1 -BuildDate 2026-07-12T00:00:00Z
.\bin\qs.exe version
```

## Configure

The default profile targets `https://api.quant-store.com`. Local profile files
contain only endpoint/output preferences and an OS-keychain reference; API key
plaintext is never written to the config file.

```bash
# Pipe from a source that does not put the key in shell arguments.
printf '%s\n' "$QS_API_KEY" | env -u QS_API_KEY qs auth set --stdin

qs auth status
qs profile set local --base-url http://localhost:8080
qs profile use local
```

For CI, set `QS_API_KEY` for that process. It takes precedence over the OS
keychain and is never persisted by the CLI.

The native credential backends are macOS Keychain, Linux Secret Service over a
session D-Bus, and Windows Credential Manager. Headless Linux sessions commonly
lack Secret Service; in that case keychain operations fail closed and CI should
provide only the process-local `QS_API_KEY`. There is no plaintext file
fallback. Keychain calls have a 15-second command bound; an interrupted
write/delete reports that its final backend result may be unknown.

## Current commands

```text
qs auth set --stdin | status | delete
qs profile list | use | set
qs account show | update
qs wallet balance | ledger
qs subscriptions list | show
qs orders list | show
qs usage summary | calls
qs completion bash|zsh|fish|powershell
qs version
```

Use `--output table|json|jsonl|yaml|raw`; `auto` selects table on a terminal and
JSON when piped. Usage responses are explicitly reported as best-effort and
partial platform responses produce a warning on stderr without corrupting
stdout.

## Product catalog and release status

`qs product request <slug> <operation-id>` is implemented for bodyless REST
`GET`/`HEAD` operations. Use repeatable `--path name=value` and
`--query name=value` flags; callers never supply a raw URL, method, header, or
catalog digest.

The CLI reads two small objects directly from the dedicated Cloudflare R2
custom domain; web-core does not proxy them:

```text
/catalogs/v1/products/<slug>.json
/catalogs/v1/sha256/<64-lowercase-hex>.json
```

The first is a strict five-field current reference:

```json
{
  "schema_version": 1,
  "product_slug": "coinglass",
  "catalog_id": "product/coinglass/proxy",
  "catalog_digest": "sha256:<64-lowercase-hex>",
  "https_origin": "https://coinglass.proxy.quant-store.com"
}
```

The second is the canonical catalog bytes whose SHA-256 equals the reference.
The CLI strict-parses it, rechecks product/catalog identity and digest, and
atomically caches it with mode `0600`. R2 downloads carry no IAM credential.
Only after all catalog checks pass does the CLI send the profile's Bearer key
to the exact `*.proxy.quant-store.com` or `*.gateway.quant-store.com` origin.

Existing operations whose missing classification safely normalizes to
`driver_required` continue through the compiled Coinglass, Twitter Monitor, or
Smart Money Monitor Pro driver. A newly added endpoint can avoid a full CLI download only
when its catalog entry explicitly declares `cli_execution: "declarative"` and
`required_cli_capabilities: ["rest.read.v1"]`, and is REST GET/HEAD, bodyless,
side-effect-free, idempotent, and fully described by scalar path/query schemas.
Everything else—including POST/mutations, MCP, WSS, files, custom headers,
special normalization, or a new capability—still requires executable code.

There is no background updater or GitHub Actions implementation in this
repository. Publishing the R2 objects is an external/manual release operation.
Account, wallet, subscriptions, orders, and usage commands do not initialize or
depend on the product catalog resolver.

## Security behavior

- The API key is accepted only through stdin/no-echo terminal input, the OS
  keychain, or the process-local `QS_API_KEY` environment variable.
- Requests use only `Authorization: Bearer ...`; the key is never placed in a
  URL, query parameter, config file, or command argument.
- HTTP redirects are never followed, including same-origin redirects.
- Non-local endpoints require HTTPS. Plain HTTP is accepted only for loopback
  development origins.
- stdout is reserved for business output; warnings and request IDs are emitted
  on stderr.

