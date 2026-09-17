# registered-servers

Turns the SQL Server instances registered in SSMS into connection profiles an AI agent can
use, without any password appearing in clear text — not in a file, not in the agent's context,
not in a session transcript.

> **Status: implemented, and not yet used against a live instance.** The scripts run and the
> `sqlq` side is tested, including against a synthetic DPAPI secret; no connection to a real
> server has been made from a generated profile. The design lives in `TASK.md`, which is
> deliberately **not tracked in git**. This README is the part that ships, so it carries the
> warnings.

## Read this before you use it

### It gives the agent your existing accounts, with their existing privileges

This tool imports the credentials SSMS already holds: the accounts **you** use to administer
your instances. It creates no login, restricts no permission, and checks none. On the machine
this was designed against, most registered servers sit in groups named for production, and one
of the logins is named `dba`.

Inventorying is not approving. The export lists every registered server, but generates a
profile **only** for ids you have put in `approved.json`, which it never rewrites. Approve
servers one at a time. Writing a connection profile by hand used to be the moment you could ask
yourself whether querying that instance was a good idea; generating all of them at once removes
that moment.

Every row an agent reads also leaves this machine for a model provider. That is worth deciding
per server, not once for the whole estate.

`"mode": "readonly"` in a generated profile does **not** change this. It is a text filter in
the client, in the same family as an editor's confirmation prompt. It prevents accidents. It
removes no permission on the server, and an agent that goes around the tool connects with the
account's full rights.

### `readonly` protects integrity, not confidentiality

The write guard rejects statements that mutate — `INSERT`, `UPDATE`, `DELETE`, `DROP`, `EXEC`
and friends. **Any `SELECT` passes it, by design.** Rows an agent reads leave this machine for
a model provider, and `-maxrows` bounds the volume, never the sensitivity: fifty rows of a
personnel table are fifty identifiable people.

So do not read "read-only" as "harmless". On a production database, a read-only credential is
the one with the most value to an attacker: it reads everything and leaves no trace of change.

### Queries are attributed to a named person

Generated profiles authenticate as the account SSMS held — often someone's named login, or a
shared one. `sqlq` sets `app name = db-ai-toolkit/sqlq`, which helps a DBA who looks, but the
server's audit trail, `sys.dm_exec_sessions` and `SUSER_SNAME()` all record **the login**. When
something goes wrong on that instance, the trace names a human, who cannot show it was the
agent. On a shared account, the agent's activity cannot be separated from anyone else's.

A dedicated login is not only about reducing rights. It is what makes the audit trail true.

> **Give the agent a login that *cannot* write — `db_datareader`, `VIEW DEFINITION`,
> `VIEW SERVER STATE`, and nothing else — register that login in SSMS, and import only it. If
> the only thing standing between the agent and your production is this tool, your production
> is not protected.**

Provisioning those logins is real work, one per instance, and this tool does not do it for
you. Until it is done, understand what you are handing over.

### What the encryption does and does not buy you

Passwords stay encrypted with **DPAPI, current-user scope** — the same mechanism SSMS itself
uses. That protects them against another user of the machine, a backup, a disk read offline,
and a synced folder.

It does **not** protect them against anything already running under your Windows account. The
security boundary is "who has my Windows session", and it is exactly the boundary SSMS already
gave you. This tool matches it; it does not improve on it.

### The hook is accident prevention, not a boundary

A `PreToolUse` hook is planned, to stop the agent from reading the credential file or calling
DPAPI directly. It filters command text, so it can be walked around — by a wildcard, a copy
under another name, .NET reflection, an encoded command, or by editing the tool and rebuilding
it. What it buys is an agent that does not dump 27 encrypted blobs into a transcript out of
curiosity. Do not read it as a guarantee.

## Getting started

```powershell
.\Export-RegisteredServers.ps1        # inventories every registered server; generates nothing yet
```

The first run creates an empty `approved.json` and generates **no** profile. Open
`servers.json`, decide one server at a time, and list the ids you approve:

```json
{ "approved": ["fr-rec/example-server"] }
```

Then run both scripts. The export names the login each generated profile will authenticate as —
read that list; it is the point of printing it.

```powershell
.\Export-RegisteredServers.ps1              # inventory + sqlq profiles. No secrets touched.
.\Import-RegisteredServerCredentials.ps1    # encrypted blobs. Run this yourself; never let an agent run it.
```

`approved.json` is never rewritten by the export. Removing an id from it removes the profile on
the next run.

### The hook

`Block-CredentialAccess.ps1` is a `PreToolUse` hook that refuses agent commands touching
`credentials.json`, the SSMS `RegSrvr*.xml`, or DPAPI directly. It is not installed for you —
add it to `.claude/settings.json`; the script's `.EXAMPLE` block has the snippet.

## Files

| Path | Contents | Read by |
|---|---|---|
| `%LOCALAPPDATA%\db-ai-toolkit\servers.json` | Inventory. **No secrets**, but it is the estate map. | anyone, including the agent |
| `%LOCALAPPDATA%\db-ai-toolkit\approved.json` | The ids you have approved. Yours; never rewritten. | the export |
| `%LOCALAPPDATA%\db-ai-toolkit\credentials.json` | Encrypted blobs, plus the server/login each was imported for | the `sqlq` secret resolver only |
| `~\.config\db-ai-toolkit\mssql-profiles.json` | `sqlq` connection profiles | `sqlq` |

Queries go through `sqlq` (see `plugins/sqlserver-toolkit/`). This folder adds no second way to
run a query, on purpose: a second path would not carry `sqlq`'s read-only guard, row cap, or
error redaction.

Profiles generated here are marked `"managedBy": "registered-servers"`. Anything in
`mssql-profiles.json` without that marker is yours and is never modified or deleted.

## Keeping it in sync

Both scripts regenerate their output in full from the SSMS file every time they run. Add,
change, rename or delete a registration in SSMS, run them again, and the result follows. They
are safe to re-run.

Re-run **both**. Running only the export refreshes the inventory while leaving old credentials
in place — the tool warns when it detects that, but re-running both is simpler than reading
warnings.

Consequence of full regeneration: `RegSrvr*.xml` is the only source. Anything you add to
`credentials.json` by hand is erased on the next run.

## When something fails

**`Login failed for user` (error 18456) — do not retry.** Repeated failed logins can lock the
account out, and a burst of failed administrator logins from a workstation is indistinguishable
from a credential-stuffing attempt in the server's security log. Stop, fix the password in SSMS
first, then re-run `Import-RegisteredServerCredentials.ps1`. Re-importing before the password is
correct in SSMS just copies the old one again and repeats the failure.

**Decryption failed / "Key not valid for use in specified state"** — the blob was encrypted on
another machine or under another account, and this machine does not hold the key. **Re-running
the import does not fix this**: it copies the same undecryptable blob. Open SSMS on *this*
machine, re-enter the password, save — SSMS re-encrypts it with the local key — and only then
re-run the import.

**A credential refuses to be used** — the server or login of a registration changed while its
name and group stayed the same, so the stored credential no longer matches where the profile
now points. This refusal is deliberate: it stops a password being sent to a server it does not
belong to. Re-run the import.

## Non-Windows

DPAPI is a Windows mechanism. A profile backed by it cannot be used elsewhere; `sqlq` says so
when you select that profile, and keeps every other profile in the file usable.
