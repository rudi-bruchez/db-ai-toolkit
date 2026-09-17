<#
.SYNOPSIS
    Shared building blocks for the registered-servers export and import scripts.

.DESCRIPTION
    Both Export-RegisteredServers.ps1 and Import-RegisteredServerCredentials.ps1 derive the
    same ids from the same SSMS file. They must agree exactly: the export writes a profile
    keyed by id, the import writes the credential keyed by id, and sqlq joins them. Two copies
    of a slug algorithm drift, and the drift is silent - a password filed under one id and
    looked up under another. So the algorithm, the id construction and the collision check
    live here, once, and both scripts call them.
#>

Set-StrictMode -Version Latest

# The SSMS registered-servers tree always starts with these two path elements. Everything
# after them is user-created group names.
$script:StoreRoot = '/RegisteredServersStore'
$script:EngineGroup = 'DatabaseEngineServerGroup'
$script:GroupSeparator = '/ServerGroup/'

# Connection-string keyword synonyms. DbConnectionStringBuilder applies ADO.NET quoting rules
# but does not fold synonyms, so "uid" and "user id" arrive as two different keys.
$script:Synonyms = @{
    user        = @('user id', 'uid', 'user')
    password    = @('password', 'pwd')
    database    = @('initial catalog', 'database')
    server      = @('data source', 'server', 'addr', 'address', 'network address')
    integrated  = @('integrated security', 'trusted_connection')
    authn       = @('authentication')
    encrypt     = @('encrypt')
    trustCert   = @('trust server certificate', 'trustservercertificate')
}

function ConvertTo-RegisteredServerSlug {
    <#
    .SYNOPSIS
        Turns a display name or group name into an id segment.
    .DESCRIPTION
        The steps are fixed and their order matters. ToLowerInvariant, never ToLower: under a
        Turkish culture ToLower maps 'I' to a dotless 'i', so the same server would get two
        different ids on two machines. An empty result is an error rather than a fallback,
        because a silent fallback is how two entries end up sharing one id.
    #>
    [CmdletBinding()]
    [OutputType([string])]
    param(
        [Parameter(Mandatory)][AllowEmptyString()][string] $Text,
        [string] $Subject = 'name'
    )

    $slug = $Text.Normalize([System.Text.NormalizationForm]::FormKC).ToLowerInvariant()
    $slug = [regex]::Replace($slug, '[^a-z0-9]+', '-').Trim('-')

    if ([string]::IsNullOrEmpty($slug)) {
        throw ("Cannot build an id: the $Subject '$Text' has no letter or digit left after " +
               'normalisation. Rename it in SSMS.')
    }
    return $slug
}

function Get-ToolkitDataDirectory {
    <#
    .SYNOPSIS
        Where this tool keeps its generated files.
    .DESCRIPTION
        %LOCALAPPDATA%, deliberately: %APPDATA% roams, and a roaming profile would put the
        encrypted credentials on a network share.
    #>
    [OutputType([string])]
    param()
    if ([string]::IsNullOrWhiteSpace($env:LOCALAPPDATA)) {
        throw 'LOCALAPPDATA is not set; this tool needs a local per-user directory to write to.'
    }
    return (Join-Path $env:LOCALAPPDATA 'db-ai-toolkit')
}

function Get-RegisteredServersSearchRoot {
    <#
    .SYNOPSIS
        The two directories Microsoft documents as holding RegSrvr*.xml.
    #>
    [OutputType([string[]])]
    param()
    $roots = @()
    foreach ($base in @($env:APPDATA, $env:LOCALAPPDATA)) {
        if (-not [string]::IsNullOrWhiteSpace($base)) {
            $roots += (Join-Path $base 'Microsoft\SQL Server Management Studio')
        }
    }
    return $roots
}

function Get-SourceVersionRank {
    # RegSrvr17.xml sorts above RegSrvr14.xml, and both above an unversioned RegSrvr.xml.
    param([string] $FileName)
    $m = [regex]::Match($FileName, '(?i)^RegSrvr(\d+)?\.xml$')
    if ($m.Success -and $m.Groups[1].Success) { return [int]$m.Groups[1].Value }
    return 0
}

function Test-RegisteredServersDocument {
    <#
    .SYNOPSIS
        Loads a candidate file and reports whether it is a registered-servers store.
    .DESCRIPTION
        Returns an object carrying the parsed document and the server count, or the reason it
        was rejected. A file that does not parse is not an error here: discovery must be able
        to skip it and say why.
    #>
    param([Parameter(Mandatory)][string] $Path)

    $result = [pscustomobject]@{
        Path        = $Path
        Document    = $null
        ServerCount = 0
        Reason      = $null
    }
    try {
        $xml = [xml](Get-Content -LiteralPath $Path -Raw -ErrorAction Stop)
    } catch {
        $result.Reason = "not well-formed XML ($($_.Exception.Message))"
        return $result
    }
    $store = $xml.SelectSingleNode("//*[local-name()='RegisteredServersStore' or local-name()='ServerGroup']")
    if ($null -eq $store) {
        $result.Reason = 'no RegisteredServers store element'
        return $result
    }
    $result.Document = $xml
    $result.ServerCount = @($xml.SelectNodes("//*[local-name()='RegisteredServer']")).Count
    return $result
}

function Resolve-RegisteredServersFile {
    <#
    .SYNOPSIS
        Finds the SSMS registered-servers file, and says which one it picked.
    .DESCRIPTION
        The file name encodes an SSMS version (RegSrvr17.xml here) and is not stable across
        machines or versions, so it is searched for, never hard-coded. Microsoft's own
        troubleshooting article names the file with a wildcard and lists two roots.

        The "at least one registered server" filter applies only to cold discovery. An
        explicitly named file, or the file a previous run already recorded, is accepted even
        when empty - otherwise deleting the last registration in SSMS would make the scripts
        fall back to some older file and keep credentials that should have been dropped.
    .PARAMETER RegisteredServersPath
        An explicit file. Wins over everything, and is accepted even if it holds no server.
        Also the way to point at a .regsrvr export or a test fixture.
    .PARAMETER EstablishedPath
        The path a previous run recorded (source.path). Accepted even if it now holds no
        server, because "you deleted them all" is a legitimate state.
    #>
    [CmdletBinding()]
    param(
        [string] $RegisteredServersPath,
        [string] $EstablishedPath
    )

    # 1 and 2: an explicit choice, from a parameter or the environment.
    $explicit = $RegisteredServersPath
    $explicitFrom = '-RegisteredServersPath'
    if ([string]::IsNullOrWhiteSpace($explicit) -and -not [string]::IsNullOrWhiteSpace($env:DB_AI_TOOLKIT_REGSRVR)) {
        $explicit = $env:DB_AI_TOOLKIT_REGSRVR
        $explicitFrom = 'DB_AI_TOOLKIT_REGSRVR'
    }
    if (-not [string]::IsNullOrWhiteSpace($explicit)) {
        if (-not (Test-Path -LiteralPath $explicit -PathType Leaf)) {
            throw "$explicitFrom points at '$explicit', which does not exist."
        }
        $probe = Test-RegisteredServersDocument -Path (Resolve-Path -LiteralPath $explicit).Path
        if ($null -eq $probe.Document) {
            throw "$explicitFrom points at '$explicit', which is $($probe.Reason)."
        }
        return [pscustomobject]@{
            Path        = $probe.Path
            Document    = $probe.Document
            ServerCount = $probe.ServerCount
            Origin      = $explicitFrom
            Rejected    = @()
            Runners     = @()
        }
    }

    # 3: discovery, recursive because older SSMS versions file it under a versioned subfolder.
    $roots = Get-RegisteredServersSearchRoot
    $found = @()
    foreach ($root in $roots) {
        if (Test-Path -LiteralPath $root -PathType Container) {
            $found += @(Get-ChildItem -LiteralPath $root -Filter 'RegSrvr*.xml' -File -Recurse -ErrorAction SilentlyContinue)
        }
    }

    $established = $null
    if (-not [string]::IsNullOrWhiteSpace($EstablishedPath)) {
        $established = $EstablishedPath.TrimEnd('\', '/')
    }

    $candidates = @()
    $rejected = @()
    foreach ($file in $found) {
        $probe = Test-RegisteredServersDocument -Path $file.FullName
        if ($null -eq $probe.Document) {
            $rejected += [pscustomobject]@{ Path = $file.FullName; Reason = $probe.Reason }
            continue
        }
        $isEstablished = ($null -ne $established) -and
                         ($file.FullName -ieq $established)
        if ($probe.ServerCount -eq 0 -and -not $isEstablished) {
            $rejected += [pscustomobject]@{
                Path   = $file.FullName
                Reason = 'holds no registered server (a fresh SSMS install leaves these behind)'
            }
            continue
        }
        $candidates += [pscustomobject]@{
            Path         = $file.FullName
            Document     = $probe.Document
            ServerCount  = $probe.ServerCount
            VersionRank  = (Get-SourceVersionRank -FileName $file.Name)
            LastWrite    = $file.LastWriteTime
            IsEstablished = $isEstablished
        }
    }

    if ($candidates.Count -eq 0) {
        $lines = @("No SSMS registered-servers file found. Searched, recursively:")
        foreach ($root in $roots) { $lines += "  $root" }
        if ($rejected.Count -gt 0) {
            $lines += 'Files found but rejected:'
            foreach ($r in $rejected) { $lines += "  $($r.Path): $($r.Reason)" }
        } else {
            $lines += 'No RegSrvr*.xml file existed under either root.'
        }
        $lines += 'Point at one with -RegisteredServersPath or $env:DB_AI_TOOLKIT_REGSRVR.'
        throw ($lines -join [Environment]::NewLine)
    }

    # An established source wins outright: staying on the file the previous run used is what
    # makes deletion work. Otherwise: highest version, then most recently written.
    $ordered = @($candidates | Sort-Object -Property @{ Expression = 'IsEstablished'; Descending = $true },
                                                     @{ Expression = 'VersionRank';   Descending = $true },
                                                     @{ Expression = 'LastWrite';     Descending = $true })
    $chosen = $ordered[0]

    return [pscustomobject]@{
        Path        = $chosen.Path
        Document    = $chosen.Document
        ServerCount = $chosen.ServerCount
        Origin      = $(if ($chosen.IsEstablished) { 'discovery (same file as the previous run)' } else { 'discovery' })
        Rejected    = $rejected
        Runners     = @($ordered | Select-Object -Skip 1)
    }
}

function Get-SourceFingerprint {
    <#
    .SYNOPSIS
        The provenance stamp written into every generated artifact.
    .DESCRIPTION
        The hash is what distinguishes "the file changed" from "a different file was picked".
        Comparing paths cannot tell those apart, and comparing timestamps cannot tell either.
    #>
    [CmdletBinding()]
    param([Parameter(Mandatory)][string] $Path)

    $item = Get-Item -LiteralPath $Path
    return [ordered]@{
        path          = $item.FullName
        lastWriteTime = $item.LastWriteTime.ToString('o')
        sizeBytes     = $item.Length
        sha256        = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
    }
}

function ConvertFrom-ConnectionString {
    <#
    .SYNOPSIS
        Parses an ADO.NET connection string into a case-insensitive lookup.
    .DESCRIPTION
        DbConnectionStringBuilder, not a regular expression. The encrypted password is stored
        quoted - password="AQAAAN..." - and a regex over [A-Za-z0-9+/=]+ captures nothing at
        all. That failure is expensive to diagnose because a truncated base64 blob surfaces
        later as a decryption error, which reads exactly like a wrong password.

        psbase is not decoration: PowerShell adapts IDictionary implementations so that
        $builder.ConnectionString = '...' assigns a dictionary *key* named ConnectionString
        instead of invoking the property that parses. Measured, not assumed.
    #>
    [CmdletBinding()]
    param([Parameter(Mandatory)][AllowEmptyString()][string] $ConnectionString)

    $builder = [System.Data.Common.DbConnectionStringBuilder]::new()
    $builder.psbase.ConnectionString = $ConnectionString

    $map = [System.Collections.Generic.Dictionary[string, string]]::new(
        [System.StringComparer]::OrdinalIgnoreCase)
    foreach ($key in $builder.psbase.Keys) {
        $map[[string]$key] = [string]$builder.psbase.Item($key)
    }
    return $map
}

function Get-ConnectionStringValue {
    param(
        [Parameter(Mandatory)] $Map,
        [Parameter(Mandatory)][string] $Kind
    )
    foreach ($key in $script:Synonyms[$Kind]) {
        $value = $null
        if ($Map.TryGetValue($key, [ref]$value) -and -not [string]::IsNullOrEmpty($value)) {
            return $value
        }
    }
    return $null
}

function Get-GroupSegment {
    <#
    .SYNOPSIS
        Turns an SFC Parent path into the user-visible group names.
    .DESCRIPTION
        Parent already carries the whole path, so no hierarchy has to be rebuilt from the
        ServerGroup elements:
          /RegisteredServersStore/ServerGroup/DatabaseEngineServerGroup/ServerGroup/FR_.PRD
        Splitting on '/ServerGroup/' and dropping the store root and the engine group leaves
        the group names, nested ones included.
    #>
    param([Parameter(Mandatory)][AllowEmptyString()][string] $Parent)

    $parts = $Parent -split [regex]::Escape($script:GroupSeparator)
    if ($parts.Count -lt 2 -or $parts[0].TrimEnd('/') -ne $script:StoreRoot) {
        throw "Unexpected Parent path '$Parent': it does not start with $script:StoreRoot."
    }
    if ($parts[1] -ne $script:EngineGroup) {
        throw ("Unexpected Parent path '$Parent': expected the $script:EngineGroup root. " +
               'Only Database Engine registrations are supported.')
    }
    return @($parts | Select-Object -Skip 2 | Where-Object { -not [string]::IsNullOrEmpty($_) })
}

function Resolve-AuthType {
    <#
    .SYNOPSIS
        Normalises the authentication mode to windows / sql / entra.
    .DESCRIPTION
        The connection string outranks the AuthenticationType integer, because the integer is
        what SSMS meant and the string is what will actually be sent.
    #>
    param(
        [Parameter(Mandatory)] $Map,
        [AllowEmptyString()][string] $AuthenticationType,
        [AllowEmptyString()][string] $ActiveDirectoryTenant
    )

    $authn = Get-ConnectionStringValue -Map $Map -Kind authn
    if ($authn -and $authn -match '(?i)^active\s*directory') { return 'entra' }
    if (-not [string]::IsNullOrWhiteSpace($ActiveDirectoryTenant)) { return 'entra' }

    $integrated = Get-ConnectionStringValue -Map $Map -Kind integrated
    if ($integrated -and $integrated -match '(?i)^(true|yes|sspi)$') { return 'windows' }

    switch ($AuthenticationType) {
        '0' { return 'windows' }
        '1' { return 'sql' }
    }
    if (Get-ConnectionStringValue -Map $Map -Kind user) { return 'sql' }
    return 'windows'
}

function ConvertTo-SqlqEncrypt {
    # go-mssqldb takes true/false/strict/disable; SSMS writes True/False/Mandatory/Optional.
    param([AllowNull()][string] $Value)
    if ([string]::IsNullOrWhiteSpace($Value)) { return 'true' }
    switch -Regex ($Value.Trim()) {
        '(?i)^(true|yes|mandatory)$'  { return 'true' }
        '(?i)^(false|no|optional)$'   { return 'false' }
        '(?i)^strict$'                { return 'strict' }
        '(?i)^disable$'               { return 'disable' }
        default                       { return $Value.Trim().ToLowerInvariant() }
    }
}

function ConvertTo-BooleanValue {
    param([AllowNull()][string] $Value)
    if ([string]::IsNullOrWhiteSpace($Value)) { return $false }
    return ($Value.Trim() -match '(?i)^(true|yes|sspi|1)$')
}

function Get-RegisteredServerEntry {
    <#
    .SYNOPSIS
        Reads every registered server out of the document, with its id already built.
    .DESCRIPTION
        The encrypted password is only attached when -IncludeEncryptedPassword is given. The
        export never passes it, so the export physically cannot write a blob into
        servers.json - a structural guarantee rather than a rule somebody has to remember.

        Ids are group-qualified always, not only when they would otherwise clash. Shortening
        an id while it happens to be unique makes it unstable: registering a new server would
        rename another server's profile, and the id is what a human types.
    .PARAMETER IncludeEncryptedPassword
        Attach the DPAPI blob, for the import script. The blob is copied, never decrypted.
    #>
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][xml] $Document,
        [switch] $IncludeEncryptedPassword
    )

    $entries = @()
    foreach ($node in $Document.SelectNodes("//*[local-name()='RegisteredServer']")) {
        $text = {
            param($name)
            $child = $node.SelectSingleNode("*[local-name()='$name']")
            if ($null -eq $child) { return '' }
            return $child.InnerText
        }

        $name = & $text 'Name'
        $parent = & $text 'Parent'
        $groupSegments = Get-GroupSegment -Parent $parent
        $groupPath = ($groupSegments -join '/')

        $idParts = @()
        foreach ($segment in $groupSegments) {
            $idParts += ConvertTo-RegisteredServerSlug -Text $segment -Subject 'group name'
        }
        $idParts += ConvertTo-RegisteredServerSlug -Text $name -Subject 'server name'
        $id = $idParts -join '/'

        $map = ConvertFrom-ConnectionString -ConnectionString (& $text 'ConnectionStringWithEncryptedPassword')
        $blob = Get-ConnectionStringValue -Map $map -Kind password

        $entries += [pscustomobject]@{
            Id                     = $id
            Name                   = $name
            Group                  = $groupPath
            GroupSegments          = $groupSegments
            ServerName             = & $text 'ServerName'
            Description            = & $text 'Description'
            ServerType             = & $text 'ServerType'
            AuthType               = Resolve-AuthType -Map $map `
                                        -AuthenticationType (& $text 'AuthenticationType') `
                                        -ActiveDirectoryTenant (& $text 'ActiveDirectoryTenant')
            Login                  = Get-ConnectionStringValue -Map $map -Kind user
            Database               = Get-ConnectionStringValue -Map $map -Kind database
            Encrypt                = ConvertTo-SqlqEncrypt (Get-ConnectionStringValue -Map $map -Kind encrypt)
            TrustServerCertificate = ConvertTo-BooleanValue (Get-ConnectionStringValue -Map $map -Kind trustCert)
            # "There is a password blob" is the test, never "the auth mode suggests there is
            # one": Active Directory Password and Active Directory Service Principal both
            # carry a password despite being Entra flows.
            HasStoredPassword      = -not [string]::IsNullOrEmpty($blob)
            EncryptedPassword      = $(if ($IncludeEncryptedPassword) { $blob } else { $null })
        }
    }

    Assert-NoIdCollision -Entries $entries
    return $entries
}

function Assert-NoIdCollision {
    <#
    .SYNOPSIS
        Refuses to continue when two registrations would share one id.
    .DESCRIPTION
        Run by both scripts, before either writes anything. The import builds its credential
        table on the same ids; if a collision appeared and the import ran first, it would file
        one account's password under another account's id, and the export would never get the
        chance to object. The failure mode is a password sent to a server it does not belong
        to - no error, no trace.
    #>
    [CmdletBinding()]
    param([Parameter(Mandatory)][AllowEmptyCollection()][array] $Entries)

    $clashes = @($Entries | Group-Object -Property Id | Where-Object { $_.Count -gt 1 })
    if ($clashes.Count -eq 0) { return }

    $lines = @('Two registered servers normalise to the same id. Nothing was written.')
    foreach ($clash in $clashes) {
        $lines += "  id '$($clash.Name)' is claimed by:"
        foreach ($e in $clash.Group) {
            $login = $(if ($e.Login) { $e.Login } else { '(no login)' })
            $lines += "    name '$($e.Name)' in group '$($e.Group)' -> $($e.ServerName) as $login"
        }
    }
    $lines += 'Rename one of them in SSMS so the two ids differ, then run this again.'
    throw ($lines -join [Environment]::NewLine)
}

function Write-JsonFileAtomic {
    <#
    .SYNOPSIS
        Writes JSON through a temporary file in the same directory, then renames it.
    .DESCRIPTION
        A script interrupted mid-write leaves the previous file intact rather than a truncated
        one. A truncated credentials.json would be read as "no credentials at all", which is
        the kind of failure that sends someone re-entering passwords in SSMS for an hour.

        This makes one file safe. It is not a transaction across files: see the README.
    #>
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string] $Path,
        [Parameter(Mandatory)] $Value,
        [int] $Depth = 12
    )

    $directory = Split-Path -Parent $Path
    if (-not (Test-Path -LiteralPath $directory -PathType Container)) {
        New-Item -ItemType Directory -Path $directory -Force | Out-Null
    }

    $json = ($Value | ConvertTo-Json -Depth $Depth)
    $temp = Join-Path $directory ('.{0}.{1}.tmp' -f (Split-Path -Leaf $Path), [guid]::NewGuid().ToString('N'))
    try {
        [System.IO.File]::WriteAllText($temp, $json, [System.Text.UTF8Encoding]::new($false))
        [System.IO.File]::Move($temp, $Path, $true)
    } finally {
        if (Test-Path -LiteralPath $temp -PathType Leaf) { Remove-Item -LiteralPath $temp -Force }
    }
}

function Read-JsonFile {
    <#
    .SYNOPSIS
        Reads a JSON file, returning $null when it is absent.
    #>
    [CmdletBinding()]
    param([Parameter(Mandatory)][string] $Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return $null }
    $raw = Get-Content -LiteralPath $Path -Raw
    if ([string]::IsNullOrWhiteSpace($raw)) { return $null }
    try {
        return $raw | ConvertFrom-Json
    } catch {
        throw "Cannot parse '$Path' as JSON: $($_.Exception.Message)"
    }
}

function Write-SourceChoice {
    <#
    .SYNOPSIS
        Prints the file that was chosen and the ones that were not.
    .DESCRIPTION
        Choosing silently between several server inventories is how a machine ends up
        synchronising the wrong file for weeks without anyone noticing.
    #>
    [CmdletBinding()]
    param([Parameter(Mandatory)] $Source)

    Write-Host "Source: $($Source.Path)"
    Write-Host "  found by: $($Source.Origin); $($Source.ServerCount) registered server(s)"
    foreach ($runner in $Source.Runners) {
        Write-Host "  not chosen: $($runner.Path) ($($runner.ServerCount) server(s))"
    }
    foreach ($reject in $Source.Rejected) {
        Write-Host "  skipped: $($reject.Path) - $($reject.Reason)"
    }
}

Export-ModuleMember -Function @(
    'ConvertTo-RegisteredServerSlug'
    'Get-ToolkitDataDirectory'
    'Resolve-RegisteredServersFile'
    'Get-SourceFingerprint'
    'ConvertFrom-ConnectionString'
    'Get-ConnectionStringValue'
    'Get-GroupSegment'
    'Get-RegisteredServerEntry'
    'Assert-NoIdCollision'
    'Write-JsonFileAtomic'
    'Read-JsonFile'
    'Write-SourceChoice'
    'ConvertTo-SqlqEncrypt'
    'Resolve-AuthType'
)
