# QuantStore CLI (`qs`)

QuantStore 命令行客户端，支持 macOS、Linux 和 Windows。
The QuantStore command-line client for macOS, Linux, and Windows.

[下载最新版本 / Latest release](https://github.com/quant-store/quantstore-cli/releases/latest) · [QuantStore](https://quant-store.com)

## 下载 / Downloads

| 平台 / Platform | 下载 / Download |
| --- | --- |
| macOS Apple Silicon (M1/M2/M3/M4, arm64) | [qs-darwin-arm64](https://github.com/quant-store/quantstore-cli/releases/download/v0.1.0/qs-darwin-arm64) |
| macOS Intel (amd64) | [qs-darwin-amd64](https://github.com/quant-store/quantstore-cli/releases/download/v0.1.0/qs-darwin-amd64) |
| Linux x86_64 (amd64) | [qs-linux-amd64](https://github.com/quant-store/quantstore-cli/releases/download/v0.1.0/qs-linux-amd64) |
| Linux arm64 | [qs-linux-arm64](https://github.com/quant-store/quantstore-cli/releases/download/v0.1.0/qs-linux-arm64) |
| Windows x86_64 (amd64) | [qs-windows-amd64.exe](https://github.com/quant-store/quantstore-cli/releases/download/v0.1.0/qs-windows-amd64.exe) |

选择与你的操作系统和处理器匹配的文件。Release 中的 `Source code` 压缩包不是可执行程序。
Choose the file matching your operating system and processor. The automatically generated `Source code` archives are not executable downloads.

## 安装 / Installation

### macOS / Linux

以下以 Apple Silicon Mac 为例；其他平台替换下载文件名。
Example for an Apple Silicon Mac; replace the asset name for another platform.

```sh
curl -fL https://github.com/quant-store/quantstore-cli/releases/download/v0.1.0/qs-darwin-arm64 -o qs
chmod +x qs
./qs version
sudo install -m 755 qs /usr/local/bin/qs
```

### Windows PowerShell

```powershell
Invoke-WebRequest https://github.com/quant-store/quantstore-cli/releases/download/v0.1.0/qs-windows-amd64.exe -OutFile qs.exe
.\qs.exe version
.\qs.exe --help
```

将 `qs.exe` 所在目录加入 PATH 后可直接使用 `qs`。
Add the directory containing `qs.exe` to PATH to run `qs` from any terminal.

## 开始使用 / Getting started

在 QuantStore 创建有相应权限的 API Key，然后在终端执行以下命令并按提示输入。
Create a QuantStore API key with the required permissions, then run this command and enter the key at the prompt.

```sh
qs auth set --stdin
qs auth status
qs account show
qs wallet balance
qs --help
```

API Key 存入系统凭据管理器，不写入配置文件。默认连接 `https://api.quant-store.com`。
The API key is stored in the OS credential manager, not the configuration file. The default API origin is `https://api.quant-store.com`.

无桌面凭据服务的 Linux 或 CI 环境可通过进程环境变量 `QS_API_KEY` 提供 Key；从现有密钥管理工具注入，避免放进命令参数或脚本正文。
Headless Linux and CI can supply a process-local `QS_API_KEY` through their existing secret manager. Keep keys out of command arguments and script bodies.

## 命令 / Commands

```text
qs auth set --stdin | status | delete
qs profile list | use | set
qs account show | update
qs wallet balance | ledger
qs subscriptions list | show
qs orders list | show
qs usage summary | calls
qs product request <product-slug> <operation-id>
qs completion bash|zsh|fish|powershell
qs version
```

`--output json` 可输出 JSON，适合脚本处理。产品请求取决于已发布的产品目录、API Key 权限与订阅权益；当前支持目录允许的 REST GET/HEAD 操作。
Use `--output json` for scripts. Product requests depend on the published catalog, API-key permissions, and subscription entitlements; supported operations are catalog-approved REST GET/HEAD calls.

## 当前发布范围 / Distribution scope

本仓库当前提供可执行程序与安装说明，尚未包含 CLI 源码。平台 API 和各产品服务独立运行。
This repository currently distributes executable builds and installation instructions. CLI source code is not included in this repository. The platform API and product services run separately.

问题反馈请使用 [Issues](https://github.com/quant-store/quantstore-cli/issues)，不要提交 API Key 或其他凭据。
Report problems through [Issues](https://github.com/quant-store/quantstore-cli/issues) without including API keys or other credentials.
