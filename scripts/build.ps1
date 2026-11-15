<#
.SYNOPSIS
Builds the frontend and cross-compiles the v1 binary for the declared targets.

.DESCRIPTION
The frontend is compiled into internal/web/dist and embedded by the Go build.
CGO is disabled because the SQLite driver is pure Go, so the result is a single
static binary per target.

.EXAMPLE
powershell -File scripts/build.ps1
powershell -File scripts/build.ps1 -Targets linux/amd64
#>
param(
    [string[]]$Targets = @("windows/amd64", "windows/arm64", "linux/amd64", "linux/arm64")
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
    Write-Host "Building frontend"
    npm run build
    if ($LASTEXITCODE -ne 0) { throw "frontend build failed" }

    New-Item -ItemType Directory -Force -Path (Join-Path $root "dist") | Out-Null
    foreach ($target in $Targets) {
        $parts = $target.Split("/")
        if ($parts.Count -ne 2) { throw "target must look like os/arch: $target" }
        $os, $arch = $parts
        $extension = if ($os -eq "windows") { ".exe" } else { "" }
        $output = Join-Path $root "dist/together-$os-$arch$extension"
        Write-Host "Building $target"
        $env:GOOS = $os
        $env:GOARCH = $arch
        $env:CGO_ENABLED = "0"
        go build -trimpath -buildvcs=false -ldflags "-s -w" -o $output ./cmd/server
        if ($LASTEXITCODE -ne 0) { throw "go build failed for $target" }
    }
} finally {
    Remove-Item Env:GOOS -ErrorAction SilentlyContinue
    Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
    Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue
    Pop-Location
}

Get-ChildItem (Join-Path $root "dist") | Select-Object Name, Length | Format-Table -AutoSize
