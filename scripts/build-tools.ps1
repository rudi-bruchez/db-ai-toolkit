#requires -Version 5.1
<#
.SYNOPSIS
    Build the Go tools and inject the binaries into the plugins that use them.

.DESCRIPTION
    Compiles ./tools/cmd/* and copies each resulting binary into the bin/
    directory of the plugin(s) that invoke it. Plugin bin/ folders are on the
    Claude Code PATH when the plugin is enabled, so the skill can call the tool
    as a bare command (e.g. `errorlog-parse`).

    By default builds for the host OS/arch only. Use -All to cross-compile the
    windows/linux/darwin (amd64) matrix into tools/dist/ for a release.

.EXAMPLE
    ./scripts/build-tools.ps1
    Build errorlog-parse for this machine and drop it into the SQL Server plugin.

.EXAMPLE
    ./scripts/build-tools.ps1 -All
    Cross-compile all target platforms into tools/dist/.
#>
[CmdletBinding()]
param(
    [switch]$All
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
$toolsDir = Join-Path $repoRoot 'tools'

# Map each command to the plugin bin/ directories that should receive it.
$targets = @{
    'errorlog-parse' = @(
        (Join-Path $repoRoot 'plugins/sqlserver-toolkit/bin')
    )
}

function Get-BinaryName([string]$cmd, [string]$goos) {
    if ($goos -eq 'windows') { return "$cmd.exe" }
    return $cmd
}

Push-Location $toolsDir
try {
    go vet ./...
    go test ./...

    foreach ($cmd in $targets.Keys) {
        $pkg = "./cmd/$cmd"

        if ($All) {
            $matrix = @(
                @{ os = 'windows'; arch = 'amd64' },
                @{ os = 'linux';   arch = 'amd64' },
                @{ os = 'darwin';  arch = 'amd64' }
            )
            $distRoot = Join-Path $toolsDir 'dist'
            foreach ($t in $matrix) {
                $out = Join-Path $distRoot "$($t.os)-$($t.arch)"
                New-Item -ItemType Directory -Force -Path $out | Out-Null
                $bin = Join-Path $out (Get-BinaryName $cmd $t.os)
                $env:GOOS = $t.os; $env:GOARCH = $t.arch
                Write-Host "Building $cmd for $($t.os)/$($t.arch) -> $bin"
                go build -o $bin $pkg
            }
            Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
        }
        else {
            $goos = (go env GOOS)
            $bin = Get-BinaryName $cmd $goos
            foreach ($dir in $targets[$cmd]) {
                New-Item -ItemType Directory -Force -Path $dir | Out-Null
                $out = Join-Path $dir $bin
                Write-Host "Building $cmd -> $out"
                go build -o $out $pkg
            }
        }
    }
    Write-Host 'Done.' -ForegroundColor Green
}
finally {
    Pop-Location
}
