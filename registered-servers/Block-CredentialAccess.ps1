<#
.SYNOPSIS
    PreToolUse hook: stops an agent reading the credential store, the SSMS source file, or
    calling DPAPI directly.

.DESCRIPTION
    Accident prevention, not a security boundary. Read that sentence before relying on this.

    A hook filters command text. It is walked around without any malice at all - by a wildcard,
    by copying the file under another name, by .NET reflection, by -EncodedCommand, or by
    editing sqlq's resolver and rebuilding it, which the agent is allowed to do because the
    repository is writable. Enumerating those routes to block them one by one is a race that
    cannot be won.

    What it does buy: an agent that does not dump 27 encrypted blobs into a transcript out of
    curiosity while diagnosing a connection failure, and does not get into the habit of going
    around sqlq. That is worth having, and it is all this is.

    The SSMS source file is covered as well as our own. It holds the same blobs, and the agent
    has a perfectly legitimate reason to open it: the design document makes it the single
    source of truth for every failure mode it describes. Guarding the copy and leaving the
    original is guarding nothing.

    Reads the hook payload as JSON on stdin. Exit 0 allows; exit 2 blocks and feeds stderr
    back to the agent.

.EXAMPLE
    Add to .claude/settings.json:

    {
      "hooks": {
        "PreToolUse": [
          {
            "matcher": "Bash|PowerShell|Read|Edit|Write|Glob|Grep|NotebookEdit",
            "hooks": [
              {
                "type": "command",
                "command": "pwsh -NoProfile -File \"C:/path/to/registered-servers/Block-CredentialAccess.ps1\""
              }
            ]
          }
        ]
      }
    }
#>
[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# A hook that throws is a hook that blocks everything. Anything unexpected below allows the
# call through: this is accident prevention, and breaking the agent's every tool call to
# protect blobs that are useless without the DPAPI key would be a bad trade.
try {
    $payload = [Console]::In.ReadToEnd()
    if ([string]::IsNullOrWhiteSpace($payload)) { exit 0 }
    $event_ = $payload | ConvertFrom-Json
} catch {
    exit 0
}

function Get-Field {
    param($Object, [string] $Name)
    if ($null -eq $Object) { return '' }
    if ($Object.PSObject.Properties.Name -notcontains $Name) { return '' }
    $value = $Object.$Name
    if ($null -eq $value) { return '' }
    return [string]$value
}

$toolName = Get-Field $event_ 'tool_name'
$toolInput = $null
if ($event_.PSObject.Properties.Name -contains 'tool_input') { $toolInput = $event_.tool_input }

# Everything this tool call might be pointed at, as one blob of text to scan.
$subject = @(
    (Get-Field $toolInput 'command')
    (Get-Field $toolInput 'file_path')
    (Get-Field $toolInput 'path')
    (Get-Field $toolInput 'pattern')
    (Get-Field $toolInput 'notebook_path')
    (Get-Field $toolInput 'content')
) -join "`n"

if ([string]::IsNullOrWhiteSpace($subject)) { exit 0 }

$reasons = @()

# 1. Our own credential store, whatever path it is reached by.
if ($subject -match '(?i)credentials\.json') {
    $reasons += @'
credentials.json holds the encrypted passwords. Nothing in it helps you: the blobs are
DPAPI ciphertext, and reading them only copies 27 of them into this transcript.
Use sqlq, which decrypts one credential in its own process and never prints it.
'@
}

# 2. The SSMS source file, which holds the same blobs. Matched by the documented wildcard
#    rather than by one machine's file name: the name encodes an SSMS version.
if ($subject -match '(?i)RegSrvr[^\\/"'']*\.xml') {
    $reasons += @'
The SSMS registered-servers file carries the same encrypted passwords as credentials.json.
Read %LOCALAPPDATA%\db-ai-toolkit\servers.json instead: it is the same inventory with the
secrets left out. To refresh the credentials, ask the user to run
Import-RegisteredServerCredentials.ps1 themselves; it prints counts and no values.
'@
}

# 3. Calling DPAPI directly, by either of its two usual names.
if ($subject -match '(?i)(CryptUnprotectData|ProtectedData|\bUnprotect\b)') {
    $reasons += @'
Decrypting a stored password directly puts it in clear text in this session. sqlq does the
decryption inside its own process, for one profile, and redacts it out of every error text.
There is no task here that needs the value itself.
'@
}

if ($reasons.Count -eq 0) { exit 0 }

$message = @("Blocked by registered-servers ($toolName).") + $reasons
[Console]::Error.WriteLine(($message -join [Environment]::NewLine))
exit 2
