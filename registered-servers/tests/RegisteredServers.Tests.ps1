#Requires -Version 7.0
<#
    Pester suite for the export and import scripts.

    What is tested here is the handful of properties that have been found broken more than
    once by review rather than by running the thing: the destination recorded beside each
    credential, where the files are allowed to be written, and the structural promise that no
    secret ever reaches the inventory. Those are the classes worth mechanizing - a review
    finds them one instance at a time, forever.

    Nothing here touches the real estate. Every run is given both -RegisteredServersPath and
    -DataDirectory pointing inside Pester's TestDrive, so the machine's own RegSrvr*.xml is
    never read and %LOCALAPPDATA% is never written. The blob is synthetic and is never
    decrypted: the property under test is that the same bytes travel, not what they mean.

    The scripts are run in a child pwsh process on purpose. They report refusal with a
    non-zero exit code, and a script that calls exit inside the test host would take the test
    host with it.

    Run:  Invoke-Pester registered-servers/tests
    Needs Pester 5 or later: Install-Module Pester -Scope CurrentUser
#>

BeforeAll {
    $script:Root = Split-Path -Parent $PSScriptRoot
    $script:ImportScript = Join-Path $Root 'Import-RegisteredServerCredentials.ps1'
    $script:ExportScript = Join-Path $Root 'Export-RegisteredServers.ps1'
    Import-Module (Join-Path $Root 'RegisteredServers.psm1') -Force

    # Distinctive enough that finding it anywhere it should not be is unambiguous.
    $script:Blob = 'U1lOVEhFVElDLU5PVC1BLVJFQUwtQkxPQg=='

    # A UNC path, written literally. The assertion is not decoration: a toolchain that eats
    # one of the two leading separators turns every UNC test below into a test of a local
    # relative path, which passes while checking nothing.
    $script:UncPath = '\\file-server\share\db-ai-toolkit'
    $separator = [char]92
    if (-not ($script:UncPath[0] -eq $separator -and $script:UncPath[1] -eq $separator)) {
        throw 'the UNC fixture lost a separator; these tests would silently stop testing UNC'
    }

    function New-Registration {
        param(
            [string] $Name = 'payroll',
            [string] $Group = 'SITE_.PRD',
            [string] $Server = 'SRV-OLD',
            [string] $Login = 'svc'
        )
        return @{ Name = $Name; Group = $Group; Server = $Server; Login = $Login }
    }

    # Writes a registered-servers file shaped like the one SSMS keeps: the password lives
    # inside the connection string, quoted, while the server name is its own element. Both
    # come from the same registration, which is why the import can never detect a rebinding
    # by comparing them - it has to compare against what it wrote last time.
    function Write-TestStore {
        param([Parameter(Mandatory)][string] $Path, [Parameter(Mandatory)][object[]] $Registrations)

        $lines = @(
            '<?xml version="1.0"?>'
            '<RegisteredServersStore xmlns="http://schemas.microsoft.com/sqlserver/2008/06/servers">'
            '  <ServerGroup>'
        )
        foreach ($r in $Registrations) {
            $cs = "Data Source=$($r.Server);User ID=$($r.Login);Password=`"$script:Blob`";Encrypt=True"
            $parent = '/RegisteredServersStore/ServerGroup/DatabaseEngineServerGroup/ServerGroup/' + $r.Group
            $lines += @(
                '    <RegisteredServer>'
                "      <Name>$($r.Name)</Name>"
                "      <Parent>$parent</Parent>"
                "      <ServerName>$($r.Server)</ServerName>"
                '      <AuthenticationType>1</AuthenticationType>'
                "      <ConnectionStringWithEncryptedPassword>$cs</ConnectionStringWithEncryptedPassword>"
                '    </RegisteredServer>'
            )
        }
        $lines += @('  </ServerGroup>', '</RegisteredServersStore>')
        Set-Content -LiteralPath $Path -Value ($lines -join [Environment]::NewLine) -Encoding UTF8
    }

    function Invoke-Toolkit {
        param([Parameter(Mandatory)][string] $Script, [Parameter(Mandatory)][hashtable] $Arguments)

        $argv = @()
        foreach ($key in $Arguments.Keys) {
            $value = $Arguments[$key]
            if ($value -is [bool]) {
                if ($value) { $argv += "-$key" }
            } else {
                $argv += "-$key"
                $argv += [string]$value
            }
        }
        $output = & pwsh -NoProfile -File $Script @argv 2>&1
        return [pscustomobject]@{
            ExitCode = $LASTEXITCODE
            Output   = ($output | Out-String)
        }
    }

    function Get-StoredBinding {
        param([Parameter(Mandatory)][string] $DataDirectory, [Parameter(Mandatory)][string] $Id)
        $store = Get-Content (Join-Path $DataDirectory 'credentials.json') -Raw | ConvertFrom-Json
        return $store.credentials.$Id.boundTo
    }
}

Describe 'the destination recorded beside a credential' {
    BeforeEach {
        $script:xml = Join-Path $TestDrive 'RegSrvr17.xml'
        $script:data = Join-Path $TestDrive 'data'
        New-Item -ItemType Directory -Path $script:data -Force | Out-Null
        Write-TestStore -Path $script:xml -Registrations @(New-Registration)
        $script:first = Invoke-Toolkit -Script $ImportScript -Arguments @{
            RegisteredServersPath = $script:xml; DataDirectory = $script:data
        }
    }

    It 'is written for every imported credential' {
        $first.ExitCode | Should -Be 0
        $bound = Get-StoredBinding -DataDirectory $data -Id 'site-prd/payroll'
        $bound.server | Should -Be 'SRV-OLD'
        $bound.login | Should -Be 'svc'
    }

    It 'never carries the blob into the output the operator sees' {
        $first.Output | Should -Not -Match 'U1lOVEhFVElD'
    }

    Context 'when the registration is repointed while keeping its password' {
        BeforeEach {
            Write-TestStore -Path $script:xml -Registrations @(New-Registration -Server 'SRV-NEW')
            $script:second = Invoke-Toolkit -Script $ImportScript -Arguments @{
                RegisteredServersPath = $script:xml; DataDirectory = $script:data
            }
        }

        It 'refuses, because importing is what would authorize the old password for the new server' {
            $second.ExitCode | Should -Not -Be 0
        }

        It 'leaves the stored binding untouched, so the credential stays refused rather than moved' {
            (Get-StoredBinding -DataDirectory $data -Id 'site-prd/payroll').server | Should -Be 'SRV-OLD'
        }

        It 'names both destinations and the way out' {
            # 'Stopping' and 'Nothing was written' belong to the refusal alone. Asserting only
            # the server names and -AcceptRebind passed with the refusal removed: the summary
            # printed after a successful rebind mentions all three too, so the test could not
            # tell "stopped and explained" from "did it and mentioned it".
            $second.Output | Should -Match 'Stopping'
            $second.Output | Should -Match 'Nothing was written'
            $second.Output | Should -Match 'SRV-OLD'
            $second.Output | Should -Match 'SRV-NEW'
            $second.Output | Should -Match 'AcceptRebind'
        }

        It 'moves the binding when the operator says the move was theirs' {
            $accepted = Invoke-Toolkit -Script $ImportScript -Arguments @{
                RegisteredServersPath = $script:xml; DataDirectory = $script:data; AcceptRebind = $true
            }
            $accepted.ExitCode | Should -Be 0
            (Get-StoredBinding -DataDirectory $data -Id 'site-prd/payroll').server | Should -Be 'SRV-NEW'
        }
    }
}

Describe 'where the generated files may be written' {
    BeforeAll {
        $script:xml = Join-Path $TestDrive 'store.xml'
        Write-TestStore -Path $script:xml -Registrations @(New-Registration)
    }

    It 'refuses a UNC directory in <script>' -ForEach @(
        @{ Script = 'import' }
        @{ Script = 'export' }
    ) {
        $target = if ($Script -eq 'import') { $ImportScript } else { $ExportScript }
        $run = Invoke-Toolkit -Script $target -Arguments @{
            RegisteredServersPath = $script:xml; DataDirectory = $script:UncPath
        }
        $run.ExitCode | Should -Not -Be 0
        $run.Output | Should -Match 'UNC path'
    }

    It 'accepts a local one' {
        $data = Join-Path $TestDrive 'local-data'
        New-Item -ItemType Directory -Path $data -Force | Out-Null
        $run = Invoke-Toolkit -Script $ImportScript -Arguments @{
            RegisteredServersPath = $script:xml; DataDirectory = $data
        }
        $run.ExitCode | Should -Be 0
    }
}

Describe 'the promise that the inventory holds no secret' {
    # The export asks for entries without -IncludeEncryptedPassword, so it cannot write a blob
    # even by accident. This checks the promise from the outside, and checks its own premise
    # first: a fixture whose blob never reached the XML would make the assertion vacuous.
    It 'writes an inventory with no trace of the encrypted password' {
        $xml = Join-Path $TestDrive 'inventory.xml'
        $data = Join-Path $TestDrive 'inventory-data'
        $profileFile = Join-Path $TestDrive 'mssql-profiles.json'
        New-Item -ItemType Directory -Path $data -Force | Out-Null
        Write-TestStore -Path $xml -Registrations @(New-Registration)

        (Get-Content $xml -Raw) | Should -Match ([regex]::Escape($script:Blob))

        $run = Invoke-Toolkit -Script $ExportScript -Arguments @{
            RegisteredServersPath = $xml; DataDirectory = $data; ProfilePath = $profileFile
        }
        $run.ExitCode | Should -Be 0

        $inventory = Get-Content (Join-Path $data 'servers.json') -Raw
        $inventory | Should -Not -Match 'U1lOVEhFVElD'
        $inventory | Should -Match 'SRV-OLD'
        $run.Output | Should -Not -Match 'U1lOVEhFVElD'
    }
}

Describe 'ids' {
    It 'carries the group, so two registrations sharing a name stay apart' {
        $p = Join-Path $TestDrive 'ids.xml'
        Write-TestStore -Path $p -Registrations @(
            (New-Registration -Name 'payroll' -Group 'SITE_.PRD')
            (New-Registration -Name 'payroll' -Group 'SITE_.STA' -Server 'SRV-STA')
        )
        $entries = @(Get-RegisteredServerEntry -Document ([xml](Get-Content $p -Raw)))
        ($entries | ForEach-Object { $_.Id }) | Should -Be @('site-prd/payroll', 'site-sta/payroll')
    }

    It 'refuses to continue when two registrations would share one id' {
        $p = Join-Path $TestDrive 'collision.xml'
        Write-TestStore -Path $p -Registrations @(
            (New-Registration -Name 'payroll' -Group 'SITE_.PRD' -Login 'svc')
            (New-Registration -Name 'payroll' -Group 'SITE_.PRD' -Login 'dba')
        )
        # Filing one account's password under another account's id is the failure this
        # prevents, and it has to happen before anything is written.
        { Get-RegisteredServerEntry -Document ([xml](Get-Content $p -Raw)) } | Should -Throw
    }
}
