<#
.SYNOPSIS
    Copies the DPAPI-encrypted passwords out of the SSMS registered-servers file into
    credentials.json, for sqlq to resolve.

.DESCRIPTION
    Run this yourself. Never let an agent run it.

    Nothing is decrypted here: the encrypted blob is copied across verbatim, staying under the
    same DPAPI current-user protection SSMS already gave it. No password value is printed, and
    none is ever held in clear by this script.

    Beside each blob, the server and login it was imported FOR are recorded. sqlq refuses to
    use a credential whose profile has since been pointed somewhere else - that is the check
    that stops one account's password being sent to another account's server, which would
    otherwise happen silently the moment someone edits a registration in SSMS without renaming
    it.

    Like the export, this regenerates the whole file from the SSMS XML every time. There is no
    incremental state to get out of step. Anything added to credentials.json by hand is erased
    on the next run: RegSrvr*.xml is the only source.

.PARAMETER RegisteredServersPath
    An explicit RegSrvr*.xml. Skips discovery.

.PARAMETER DataDirectory
    Where credentials.json is written. Defaults to %LOCALAPPDATA%\db-ai-toolkit.

.EXAMPLE
    .\Import-RegisteredServerCredentials.ps1
#>
[CmdletBinding()]
param(
    [string] $RegisteredServersPath,
    [string] $DataDirectory
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

Import-Module (Join-Path $PSScriptRoot 'RegisteredServers.psm1') -Force

$MANAGED_BY = 'registered-servers'

if ([string]::IsNullOrWhiteSpace($DataDirectory)) { $DataDirectory = Get-ToolkitDataDirectory }
$credentialsPath = Join-Path $DataDirectory 'credentials.json'
$serversPath = Join-Path $DataDirectory 'servers.json'

# Stay on the file the previous run used, so that emptying the SSMS list actually empties this
# one instead of falling back to some older file and keeping credentials alive.
$previous = Read-JsonFile -Path $credentialsPath
$established = $null
if ($null -ne $previous -and
    $previous.PSObject.Properties.Name -contains 'source' -and
    $null -ne $previous.source) {
    $established = $previous.source.path
}

$source = Resolve-RegisteredServersFile -RegisteredServersPath $RegisteredServersPath -EstablishedPath $established
Write-SourceChoice -Source $source

$fingerprint = Get-SourceFingerprint -Path $source.Path
$entries = @(Get-RegisteredServerEntry -Document $source.Document -IncludeEncryptedPassword)

$credentials = [ordered]@{}
$noSecret = @()
foreach ($entry in ($entries | Sort-Object Id)) {
    if (-not $entry.HasStoredPassword) {
        $noSecret += $entry.Id
        continue
    }
    # The blob is copied, not decrypted. This script never learns the password.
    $credentials[$entry.Id] = [ordered]@{
        blob    = $entry.EncryptedPassword
        boundTo = [ordered]@{
            server = $entry.ServerName
            login  = $(if ($entry.Login) { $entry.Login } else { '' })
        }
    }
}

$document = [ordered]@{
    managedBy   = $MANAGED_BY
    generatedAt = (Get-Date).ToString('o')
    source      = $fingerprint
    credentials = $credentials
}

# --- what this run changes, in counts only -----------------------------------------------------

$previousIds = @()
$previousBindings = @{}
if ($null -ne $previous -and
    $previous.PSObject.Properties.Name -contains 'credentials' -and
    $null -ne $previous.credentials) {
    foreach ($property in $previous.credentials.PSObject.Properties) {
        $previousIds += $property.Name
        $binding = $null
        if ($property.Value.PSObject.Properties.Name -contains 'boundTo') { $binding = $property.Value.boundTo }
        $previousBindings[$property.Name] = $binding
    }
}

$currentIds = @($credentials.Keys)
$added = @($currentIds | Where-Object { $_ -notin $previousIds })
$removed = @($previousIds | Where-Object { $_ -notin $currentIds })
$kept = @($currentIds | Where-Object { $_ -in $previousIds })

# A changed binding is the interesting one: same id, different destination. The credential
# itself cannot be compared - DPAPI produces a different ciphertext every time it encrypts, so
# comparing blobs byte for byte says nothing about whether the password changed.
$rebound = @()
foreach ($id in $kept) {
    $before = $previousBindings[$id]
    if ($null -eq $before) { continue }
    $after = $credentials[$id].boundTo
    if ($before.server -ne $after.server -or $before.login -ne $after.login) {
        $rebound += "$id ($($before.server)/$($before.login) -> $($after.server)/$($after.login))"
    }
}

Write-JsonFileAtomic -Path $credentialsPath -Value $document

# --- summary ------------------------------------------------------------------------------------

Write-Host ''
Write-Host "Credentials: $credentialsPath"
Write-Host "  $($added.Count) added, $($removed.Count) removed, $($kept.Count) refreshed"
foreach ($id in $added)   { Write-Host "    added:   $id" }
foreach ($id in $removed) { Write-Host "    removed: $id" }
foreach ($line in $rebound) {
    Write-Host "    re-bound: $line"
    Write-Host "      the previous credential would have been refused; it is replaced."
}
foreach ($id in $noSecret) { Write-Host "    no stored password in SSMS, nothing imported: $id" }

# --- do the two artifacts agree? ----------------------------------------------------------------

$inventory = Read-JsonFile -Path $serversPath
if ($null -eq $inventory) {
    Write-Host ''
    Write-Host "No inventory at $serversPath. Run Export-RegisteredServers.ps1 as well:"
    Write-Host '  without it there are no profiles for sqlq to resolve these credentials for.'
} elseif ($inventory.source.sha256 -ne $fingerprint.sha256) {
    Write-Host ''
    Write-Host 'WARNING: the inventory was generated from a different state of the source file.'
    Write-Host "  inventory   $($inventory.source.sha256)"
    Write-Host "  credentials $($fingerprint.sha256)"
    Write-Host '  Run Export-RegisteredServers.ps1 now, so profiles and credentials match.'
}

Write-Host ''
Write-Host 'No password was decrypted or displayed by this script.'
