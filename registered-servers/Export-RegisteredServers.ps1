<#
.SYNOPSIS
    Inventories the SQL Server instances registered in SSMS, and generates sqlq profiles for
    the ones that have been approved.

.DESCRIPTION
    Reads the SSMS registered-servers XML and writes two things:

      %LOCALAPPDATA%\db-ai-toolkit\servers.json   every registered server. No secrets.
      ~\.config\db-ai-toolkit\mssql-profiles.json a sqlq profile per APPROVED server.

    Inventorying is not approving. Every registered server is inventoried, but a profile is
    generated only for ids listed in approved.json, which this script never rewrites. Writing
    a connection profile by hand used to be the moment someone could ask whether querying that
    instance was a good idea; generating them all at once removes that moment, so the approval
    file puts it back.

    This script touches no secret. It never reads the encrypted password out of the XML, so it
    cannot leak one into servers.json - that is a property of the code, not a rule to remember.

    The whole result is computed in memory first. A single collision with a hand-written
    profile fails the entire export and nothing is written: a servers.json listing 27 servers
    beside a profile file that received 25 is a state nobody diagnoses.

.PARAMETER RegisteredServersPath
    An explicit RegSrvr*.xml (or an SSMS .regsrvr export). Skips discovery.

.PARAMETER DataDirectory
    Where servers.json and approved.json live. Defaults to %LOCALAPPDATA%\db-ai-toolkit.

.PARAMETER ProfilePath
    The sqlq profile file. Defaults to ~\.config\db-ai-toolkit\mssql-profiles.json.

.EXAMPLE
    .\Export-RegisteredServers.ps1
#>
[CmdletBinding()]
param(
    [string] $RegisteredServersPath,
    [string] $DataDirectory,
    [string] $ProfilePath
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

Import-Module (Join-Path $PSScriptRoot 'RegisteredServers.psm1') -Force

$MANAGED_BY = 'registered-servers'

function Get-ProfileDefaultPath {
    $home_ = [Environment]::GetFolderPath([Environment+SpecialFolder]::UserProfile)
    return (Join-Path $home_ '.config\db-ai-toolkit\mssql-profiles.json')
}

function Test-ProdGroup {
    # The group names on this estate are AU_.PRD, FR_.PRD, FR_.STA... so the test runs on the
    # slugified segments: 'au-prd' splits to 'au' and 'prd'.
    param([AllowEmptyCollection()][string[]] $GroupSegments)
    foreach ($segment in $GroupSegments) {
        $slug = ConvertTo-RegisteredServerSlug -Text $segment -Subject 'group name'
        foreach ($token in ($slug -split '-')) {
            if ($token -in @('prd', 'prod', 'production')) { return $true }
        }
    }
    return $false
}

function New-SqlqProfile {
    <#
    .SYNOPSIS
        Builds one sqlq profile entry, or explains why the entry cannot produce one.
    .DESCRIPTION
        The inventory vocabulary and the sqlq vocabulary do not coincide: the inventory says
        'windows', sqlq says 'integrated'. Anything outside the table below is inventoried but
        produces no profile, loudly - a profile that fails to connect six weeks later, for
        reasons nobody can reconstruct, is worse than a refusal today.
    #>
    param(
        [Parameter(Mandatory)] $Entry,
        [Parameter(Mandatory)] $Fingerprint
    )

    $built = [ordered]@{ server = $Entry.ServerName }
    if (-not [string]::IsNullOrWhiteSpace($Entry.Database)) { $built.database = $Entry.Database }

    $note = $null
    switch ($Entry.AuthType) {
        'sql' {
            if (-not $Entry.HasStoredPassword) {
                return [pscustomobject]@{
                    Profile = $null
                    Skipped = 'SQL authentication with no stored password in SSMS; nothing to import.'
                }
            }
            $built.auth = 'sql'
            $built.user = $Entry.Login
            $built.passwordDpapi = $Entry.Id
        }
        'windows' {
            # No krb5 fields: the SSMS XML carries no realm, keytab or credential cache. The
            # profile works on Windows and fails honestly anywhere else.
            $built.auth = 'integrated'
        }
        'entra' {
            $built.auth = 'entra'
            # Writing the default down rather than inheriting it: ActiveDirectoryDefault is a
            # reasonable choice, but it is a choice, and it should be visible in the file.
            $built.fedauth = 'ActiveDirectoryDefault'
            $note = 'fedauth not deducible from SSMS; wrote ActiveDirectoryDefault'
            if ($Entry.Login) { $built.user = $Entry.Login }
            if ($Entry.HasStoredPassword) { $built.passwordDpapi = $Entry.Id }
        }
        default {
            return [pscustomobject]@{
                Profile = $null
                Skipped = "authentication mode '$($Entry.AuthType)' has no sqlq equivalent."
            }
        }
    }

    # Always readonly at generation. Moving a profile to readwrite is a human decision taken
    # one entry at a time, not something an inventory script decides for 27 servers at once.
    $built.mode = 'readonly'
    $built.encrypt = $Entry.Encrypt
    $built.trustServerCertificate = [bool]$Entry.TrustServerCertificate
    if (Test-ProdGroup -GroupSegments $Entry.GroupSegments) { $built.environment = 'prod' }
    $built.managedBy = $MANAGED_BY
    $built.source = $Fingerprint

    return [pscustomobject]@{ Profile = $built; Skipped = $null; Note = $note }
}

function Get-ApprovedId {
    <#
    .SYNOPSIS
        Reads approved.json. Never writes to it once it exists.
    #>
    param([Parameter(Mandatory)][string] $Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        Write-JsonFileAtomic -Path $Path -Value ([ordered]@{ approved = @() })
        Write-Host ''
        Write-Host "Created an empty approval file: $Path"
        Write-Host '  No profile is generated until you list ids in it. Take them from'
        Write-Host '  servers.json, one at a time, and read the README before you do.'
        return @()
    }

    $content = Read-JsonFile -Path $Path
    if ($null -eq $content) { return @() }
    # A hand-edited file: accept both a bare array and { "approved": [...] }.
    if ($content -is [array]) { return @($content | ForEach-Object { [string]$_ }) }
    if ($content.PSObject.Properties.Name -contains 'approved' -and $null -ne $content.approved) {
        return @($content.approved | ForEach-Object { [string]$_ })
    }
    throw ("'$Path' must hold either a JSON array of ids or an object with an " +
           '"approved" array. Nothing was written.')
}

function Compare-GeneratedProfile {
    <#
    .SYNOPSIS
        Lists the fields that differ between the profile being written and the one it replaces.
    .DESCRIPTION
        Full regeneration silently undoes hand edits made to a generated profile - a database
        pinned to one catalog, a lowered timeout, encrypt raised to strict. The reversion is in
        the safe direction for mode (readwrite back to readonly) and in the dangerous direction
        for everything else, so it gets said out loud. This cannot distinguish "the human edited
        it" from "SSMS changed"; it reports what changed and lets the human recognise their own
        edit.
    #>
    param([Parameter(Mandatory)] $Old, [Parameter(Mandatory)] $New)

    $changed = @()
    $names = @($Old.PSObject.Properties.Name) + @($New.Keys)
    foreach ($field in ($names | Sort-Object -Unique)) {
        if ($field -eq 'source') { continue }  # provenance, expected to change every run
        $oldValue = $null
        if ($Old.PSObject.Properties.Name -contains $field) { $oldValue = $Old.$field }
        $newValue = $null
        if ($New.Contains($field)) { $newValue = $New[$field] }
        if ([string]$oldValue -ne [string]$newValue) { $changed += $field }
    }
    return $changed
}

# ---------------------------------------------------------------------------------------------

if ([string]::IsNullOrWhiteSpace($DataDirectory)) { $DataDirectory = Get-ToolkitDataDirectory }
if ([string]::IsNullOrWhiteSpace($ProfilePath)) { $ProfilePath = Get-ProfileDefaultPath }

$serversPath = Join-Path $DataDirectory 'servers.json'
$approvedPath = Join-Path $DataDirectory 'approved.json'

# Staying on the file the previous run used is what makes deletion work: if the last
# registration is removed in SSMS the file goes empty, and cold discovery would reject it.
$established = $null
$previousInventory = Read-JsonFile -Path $serversPath
if ($null -ne $previousInventory -and
    $previousInventory.PSObject.Properties.Name -contains 'source' -and
    $null -ne $previousInventory.source) {
    $established = $previousInventory.source.path
}

$source = Resolve-RegisteredServersFile -RegisteredServersPath $RegisteredServersPath -EstablishedPath $established
Write-SourceChoice -Source $source

$fingerprint = Get-SourceFingerprint -Path $source.Path
$entries = @(Get-RegisteredServerEntry -Document $source.Document)

# --- the inventory: every registered server, no secret -----------------------------------------

$inventory = [ordered]@{
    managedBy   = $MANAGED_BY
    generatedAt = (Get-Date).ToString('o')
    source      = $fingerprint
    servers     = @(
        foreach ($entry in ($entries | Sort-Object Id)) {
            [ordered]@{
                id                     = $entry.Id
                name                   = $entry.Name
                group                  = $entry.Group
                serverName             = $entry.ServerName
                description            = $entry.Description
                authType               = $entry.AuthType
                login                  = $entry.Login
                database               = $entry.Database
                encrypt                = $entry.Encrypt
                trustServerCertificate = [bool]$entry.TrustServerCertificate
                hasStoredPassword      = [bool]$entry.HasStoredPassword
            }
        }
    )
}

# --- the profiles: approved ids only -----------------------------------------------------------

$approvedIds = @(Get-ApprovedId -Path $approvedPath)
$approvedSet = [System.Collections.Generic.HashSet[string]]::new(
    [string[]]$approvedIds, [System.StringComparer]::OrdinalIgnoreCase)

$knownIds = [System.Collections.Generic.HashSet[string]]::new(
    [string[]]@($entries.Id), [System.StringComparer]::OrdinalIgnoreCase)
$unknownApproved = @($approvedIds | Where-Object { -not $knownIds.Contains($_) })

$generated = [ordered]@{}
$skipped = @()
$notes = @()
foreach ($entry in ($entries | Sort-Object Id)) {
    if (-not $approvedSet.Contains($entry.Id)) { continue }
    $built = New-SqlqProfile -Entry $entry -Fingerprint $fingerprint
    if ($null -eq $built.Profile) {
        $skipped += "$($entry.Id): $($built.Skipped)"
        continue
    }
    if ($built.Note) { $notes += "$($entry.Id): $($built.Note)" }
    # The prod marker reads the group path only, which is what the spec fixes it to. A server
    # registered at the root has no group, so a production instance sitting there gets no
    # marker however it is named. Say so rather than quietly widen the rule to hostnames,
    # where 'prd' inside a longer word would start marking things at random.
    if (-not $built.Profile.Contains('environment') -and
        (($entry.Name -match '(?i)pr[o]?d') -or ($entry.ServerName -match '(?i)pr[o]?d'))) {
        $notes += ("$($entry.Id): name looks like production but its group does not say so, " +
                   'so it carries no environment marker')
    }
    $generated[$entry.Id] = $built.Profile
}

# --- merge into a file that is not ours --------------------------------------------------------

$existingProfiles = Read-JsonFile -Path $ProfilePath
$manual = [ordered]@{}
$previousGenerated = @{}
if ($null -ne $existingProfiles) {
    foreach ($property in $existingProfiles.PSObject.Properties) {
        $value = $property.Value
        $isOurs = ($null -ne $value) -and
                  ($value.PSObject.Properties.Name -contains 'managedBy') -and
                  ($value.managedBy -eq $MANAGED_BY)
        if ($isOurs) { $previousGenerated[$property.Name] = $value }
        else { $manual[$property.Name] = $value }
    }
}

$collisions = @($generated.Keys | Where-Object { $manual.Contains($_) })
if ($collisions.Count -gt 0) {
    $lines = @('A generated profile name is already taken by a hand-written profile.',
               "Nothing was written - neither $serversPath nor $ProfilePath.")
    foreach ($name in $collisions) { $lines += "  $name" }
    $lines += 'Rename the server in SSMS, or rename the profile in your profile file, then run this again.'
    throw ($lines -join [Environment]::NewLine)
}

$discarded = @()
foreach ($id in $generated.Keys) {
    if (-not $previousGenerated.ContainsKey($id)) { continue }
    # @() because PowerShell unrolls a one-element array on return, and a single
    # discarded field is exactly the common case.
    $changed = @(Compare-GeneratedProfile -Old $previousGenerated[$id] -New $generated[$id])
    if ($changed.Count -gt 0) { $discarded += "$id ($($changed -join ', '))" }
}
$removed = @($previousGenerated.Keys | Where-Object { -not $generated.Contains($_) })

$merged = [ordered]@{}
foreach ($name in ($manual.Keys | Sort-Object)) { $merged[$name] = $manual[$name] }
foreach ($name in ($generated.Keys | Sort-Object)) { $merged[$name] = $generated[$name] }

# --- publish ------------------------------------------------------------------------------------

Write-JsonFileAtomic -Path $serversPath -Value $inventory
Write-JsonFileAtomic -Path $ProfilePath -Value $merged

# --- summary --------------------------------------------------------------------------------------

$notApproved = $entries.Count - @($entries | Where-Object { $approvedSet.Contains($_.Id) }).Count

Write-Host ''
Write-Host "Inventory: $serversPath"
Write-Host "  $($entries.Count) inventoried, $($generated.Count) approved, $notApproved not approved"
foreach ($group in ($entries | Group-Object AuthType | Sort-Object Name)) {
    Write-Host "  authType $($group.Name): $($group.Count)"
}

Write-Host ''
Write-Host "Profiles: $ProfilePath"
Write-Host "  $($manual.Count) left untouched (not managed here), $($generated.Count) generated, $($removed.Count) removed"

if ($generated.Count -gt 0) {
    Write-Host ''
    Write-Host '  Generated profiles, and the login each one will authenticate as:'
    foreach ($id in ($generated.Keys | Sort-Object)) {
        $entry = $generated[$id]
        $login = $(if ($entry.Contains('user')) { $entry['user'] } else { '(integrated)' })
        $env_ = $(if ($entry.Contains('environment')) { " [$($entry['environment'])]" } else { '' })
        Write-Host "    $id -> $($entry['server']) as $login$env_"
    }
    Write-Host ''
    Write-Host "  $($generated.Count) profile(s) were generated without any check of what those"
    Write-Host '  logins are allowed to do. This tool creates no login and restricts no'
    Write-Host '  permission. Queries will be attributed to the named account above, not to'
    Write-Host '  the agent. See README.md.'
}

foreach ($line in $removed)         { Write-Host "  removed: $line" }
foreach ($line in $discarded)       { Write-Host "  manual edit discarded: $line" }
foreach ($line in $notes)           { Write-Host "  note: $line" }
foreach ($line in $skipped)         { Write-Host "  approved but no profile: $line" }
foreach ($line in $unknownApproved) { Write-Host "  approved id not in the inventory: $line" }

if ($generated.Count -gt 0) {
    Write-Host ''
    Write-Host 'Now run Import-RegisteredServerCredentials.ps1 yourself, so the passwords match.'
}
