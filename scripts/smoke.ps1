#Requires -Version 5.1
<#
.SYNOPSIS
Starts a built binary on a temporary database and checks the release smoke surface.

.DESCRIPTION
The release binary is started with a generated config, then /api/ready,
/api/health, /api/setup and the embedded SPA are read over real HTTP. The
process is stopped and the temporary directory removed at the end.

.EXAMPLE
powershell -File scripts/smoke.ps1
powershell -File scripts/smoke.ps1 -Binary dist/together-linux-amd64
#>
param(
    [string]$Binary = "dist/together-windows-amd64.exe",
    [int]$Port = 0,
    [int]$TimeoutSeconds = 20
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$exe = if ([IO.Path]::IsPathRooted($Binary)) { $Binary } else { Join-Path $root $Binary }
if (-not (Test-Path -LiteralPath $exe)) {
    throw "binary not found: $exe"
}
if ($Port -eq 0) {
    $Port = Get-Random -Minimum 20000 -Maximum 39000
}

$temp = Join-Path ([IO.Path]::GetTempPath()) ("together-smoke-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $temp | Out-Null
$configPath = Join-Path $temp "config.ini"
$dbPath = Join-Path $temp "smoke.db"
$stdout = Join-Path $temp "server.out.log"
$stderr = Join-Path $temp "server.err.log"
@(
    "addr=127.0.0.1:$Port"
    "db_path=$dbPath"
    "locale=en"
    "secure_cookies=false"
) | Set-Content -LiteralPath $configPath -Encoding ASCII

$previousConfig = $env:CONFIG_PATH
$env:CONFIG_PATH = $configPath
$process = $null
try {
    $process = Start-Process -FilePath $exe -PassThru -WindowStyle Hidden `
        -RedirectStandardOutput $stdout -RedirectStandardError $stderr
    $base = "http://127.0.0.1:$Port"
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $ready = $false
    while ((Get-Date) -lt $deadline) {
        if ($process.HasExited) {
            throw "server exited early with code $($process.ExitCode): $(Get-Content -LiteralPath $stderr -Raw -ErrorAction SilentlyContinue)"
        }
        try {
            $response = Invoke-WebRequest -UseBasicParsing -Uri "$base/api/ready" -TimeoutSec 2
            if ($response.StatusCode -eq 200) {
                $ready = $true
                break
            }
        } catch {
            Start-Sleep -Milliseconds 250
        }
    }
    if (-not $ready) {
        throw "server did not become ready within $TimeoutSeconds seconds: $(Get-Content -LiteralPath $stderr -Raw -ErrorAction SilentlyContinue)"
    }

    $health = Invoke-WebRequest -UseBasicParsing -Uri "$base/api/health" -TimeoutSec 5
    $setup = Invoke-RestMethod -Uri "$base/api/setup" -TimeoutSec 5
    $page = Invoke-WebRequest -UseBasicParsing -Uri "$base/" -TimeoutSec 5

    if ($health.StatusCode -ne 200) { throw "health status $($health.StatusCode)" }
    if ($setup.setup_completed -ne $false) { throw "fresh database reports setup completed" }
    if ($page.Content -notmatch 'id="root"') { throw "embedded SPA shell missing" }
    if (-not (Test-Path -LiteralPath $dbPath)) { throw "database was not created" }

    Write-Host "smoke ok: $exe on port $Port"
    Write-Host "  health: $($health.StatusCode)"
    Write-Host "  setup_completed: $($setup.setup_completed)"
    Write-Host "  SPA shell: present"
    Write-Host "  database: $dbPath"
} finally {
    if ($process -and -not $process.HasExited) {
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
        $process.WaitForExit()
    }
    if ($null -eq $previousConfig) {
        Remove-Item Env:CONFIG_PATH -ErrorAction SilentlyContinue
    } else {
        $env:CONFIG_PATH = $previousConfig
    }
    Remove-Item -LiteralPath $temp -Recurse -Force -ErrorAction SilentlyContinue
}
