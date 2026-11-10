[CmdletBinding()]
param(
    [string]$Version = "dev",
    [string]$BuildDate = "unknown"
)

$ErrorActionPreference = "Stop"
$previousCGO = $env:CGO_ENABLED
$ldflags = "-s -w -X github.com/quant-store/quantstore-cli/core/internal/version.Version=$Version -X github.com/quant-store/quantstore-cli/core/internal/version.BuildDate=$BuildDate"

try {
    $env:CGO_ENABLED = "0"
    New-Item -ItemType Directory -Force -Path "bin" | Out-Null
    & go build -buildvcs=false -trimpath -ldflags $ldflags -o "bin/qs.exe" "./cmd/qs"
    if ($LASTEXITCODE -ne 0) {
        exit $LASTEXITCODE
    }
}
finally {
    if ($null -eq $previousCGO) {
        Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue
    }
    else {
        $env:CGO_ENABLED = $previousCGO
    }
}
