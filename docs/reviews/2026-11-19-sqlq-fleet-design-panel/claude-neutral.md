# Review of docs/superpowers/specs/2026-11-19-sqlq-fleet-design.md (Claude, neutral prompt)

Working copy: a clone at 25139a4. All evidence below comes from a throwaway program (`tools/cmd/zzreview`, since deleted) that imported the real `go-mssqldb` v1.11.0 and `tools/internal/sqlq`, built the DSN with `sqlq.Profile.DSN` exactly as `execute` does, and pointed `database/sql` at fake TDS servers on `127.0.0.1:0`. The fakes ranged from "close on accept" to "valid PRELOGIN reply, then stall", "LOGINACK, then never answer the batch", and hand-built ERROR tokens 18456 and 4060+18456. One run used the real `sqlq` binary from the working copy with a profiles file I wrote, pointing at a fake server. Go 1.27.0, linux/amd64.

## Verified by running

### 1. A stalled connection hangs sqlq forever; no context bounds PRELOGIN or TLS, so `connect_timeout` never happens on a real profile and one stalled instance blocks a whole fleet run

Section 4.2 builds on the premise that "aujourd'hui un seul contexte couvre les deux" (connect and batch), and on the plan to give each attempt "son propre délai de connexion (celui du profil)". Both are false in practice. go-mssqldb uses the context for the TCP dial only (its own comment: "using the provided context for dialing only"): `readPrelogin` and the TLS handshake read the socket with no deadline. `database/sql` calls `Connector.Connect(ctx)` synchronously and does not abandon it when the context expires. No profile has a connect timeout either: `connectTimeoutSec` appears nowhere except `profile.go` and `dsn.go`, and the registered-servers export never writes it, so all 31 fleet profiles have none. Measured with `db.Conn`:

    == never-answer (connTimeout=0 ctx=5s)
      STILL BLOCKED after 40s (guard), tcp_accepts=1
    == stall-after-prelogin-response (connTimeout=0 ctx=5s)
      STILL BLOCKED after 40s (guard), tcp_accepts=1
    == stall-after-login-sent (encrypt=disable connTimeout=0 ctx=5s)
      elapsed=5s  err="context deadline exceeded"     <- ctx is honored only once LOGIN7 is sent

And end to end with today's binary (`-timeout 5`, a server that accepts and stays silent):

    ZZ_PWD=x timeout 45 sqlq -profiles profiles.json -profile fake/a -query "SELECT 1" -timeout 5 ...; echo "exit=$?"
    exit=124          (45 s, killed by timeout(1), no JSON at all)

Consequences for the design. The `connect_timeout` category, and its retry, are unreachable for every generated profile. The per-attempt context the spec introduces bounds nothing in the phase where a ZPA tunnel that accepts and then stalls would hang. Under `-parallel 4`, four such instances block the four slots for good: the fleet call never prints its one JSON object and never exits with 5, which is exactly the "incomplete fleet result" the design exists to prevent. The retry test in section 6 inherits the problem. Its server "ferme les deux premières connexions dès l'acceptation puis ne répond plus". Read as "accepts and stays silent", the third attempt hangs and `go test` stalls until its own 10-minute timeout. Read as "the listener is closed", the last error is `ECONNREFUSED`, so the test asserts `connect_transient` and never exercises a timeout. The design needs its own wall-clock bound per attempt, independent of the driver: a deadline set on the `net.Conn` through a custom dialer, or `db.Conn` run in a goroutine and abandoned when the deadline passes. It should also state that a fleet run has an overall bound.

### 2. The profile's `connection timeout` is a per-read idle deadline for the whole session, not a connect deadline, and its error is not `context.DeadlineExceeded`

The obvious fix for point 1 (set `connectTimeoutSec`, which is what "le délai de connexion du profil" names) breaks batches. go-mssqldb wraps the socket in `timeoutConn`, which calls `SetDeadline(now+timeout)` before every `Read` and `Write`, for the life of the session. Measured with a fake server that sends LOGINACK, then never answers the batch, `connectTimeoutSec: 3` and `-timeout` 30 s:

    [connected; preamble returned after 3s: read tcp ...: i/o timeout | Is(os.ErrDeadlineExceeded) As(net.Error,timeout=true)]

So any statement whose first packet takes longer than the "connect" timeout fails mid-batch, as `transport`, exit 2. That covers a sort-heavy DMV query, or a `WAITFOR` after `RAISERROR ... WITH NOWAIT`, which AGENTS.md itself recommends. The same run shows the connect-phase timeout surfaces as `*net.OpError > *poll.DeadlineExceededError`, for which `errors.Is(err, os.ErrDeadlineExceeded)` is true and `errors.Is(err, context.DeadlineExceeded)` is false. The section 6 classification table lists `context.DeadlineExceeded` only. An implementation that passes that table classifies a real timed-out connect as `connect_failed` and does not retry it.

### 3. One shared Resolver under `-parallel` fails 45 % of DPAPI resolutions with a misleading "credential store unavailable"

Section 5.3: "Le résolveur de secrets est créé une fois". `Resolver.loadStore` sets `r.loaded = true` before it reads the file and stores `r.store` afterwards, with no lock. A concurrent caller sees `loaded && store == nil` and returns an error. Measured: 200 rounds of 4 goroutines calling `Resolve` on one Resolver (DPAPI profiles, valid store, stub `Unprotect`):

    437  ok:ab
    363  credential store /tmp/zzrace.../credentials.json is unavailable

`go build -race` reports `DATA RACE` at `secret.go:266` (read) against `secret.go:272` (write). In the fleet design this is a resolver failure, so category `usage`, exit 1, never retried. Its text sends the agent and the user toward the credential store, which is not where the problem is. The design must either serialize `Resolve` (a mutex, or resolving every selected profile up front, sequentially) or create one Resolver per profile. The second option repeats the store read but also solves the attribution problem in hypothesis A below.

### 4. EOF or RST during the TLS handshake comes back as a flat string, so by the table's own rule a cut after PRELOGIN is `connect_failed` and is not retried

With the default `encrypt=true`, a cut after the PRELOGIN reply produces:

    err="TLS Handshake failed: cannot read handshake packet: EOF"                          chain=*errors.errorString  (none)
    err="TLS Handshake failed: cannot read handshake packet: read tcp ...: connection reset by peer"  (none)
    err="TLS Handshake failed: cannot read handshake packet: read tcp ...: i/o timeout"    (none)

`tds.go` formats this one with `%v` (the strict-encryption path a few lines earlier uses `%w`), so `errors.Is(io.EOF)`, `ECONNRESET` and the timeout checks all fail. The spec's table places "erreur TLS ou de certificat" in `connect_failed`, and its fallback ("sauf là où le pilote ne donne pas de type : le cas est alors listé dans un test avec le message exact observé") allows text matching without saying it applies here. There are two readings. Read strictly, a tunnel drop one round trip later than the 18/11 case is never retried, and `connect_failed` tells the agent the problem is durable. Read with a text fallback on `TLS Handshake failed:`, a genuine certificate failure has to be told apart from a transport one inside the same string. The spec should pick one, and list these three exact messages. For comparison, a close on accept gives a bare `io.EOF` (`err="EOF"`, `Is(io.EOF)`), which matches the field note's wording: the 18/11 failures were probably this shape, but nothing proves it.

### 5. A timeout during the preamble is neither `context.DeadlineExceeded` nor a typed driver error

With no profile timeout and a 6 s context, the preamble `ExecContext` on a connection that stalls after login returns after 11 s (6 s plus the driver's wait for the attention acknowledgment):

    Invalid TDS stream: did not get cancellation confirmation from the server (current response: context deadline exceeded) | (none)

A classifier that walks the error chain, as section 4.1 asks, sees no deadline and no transport type. It falls through to `sql_error` or `transport` instead of `query_timeout`. `execute` must check `ctx.Err()` before classifying an error from the preamble or the batch. The spec should say so; today `collect` does it for the batch, but the preamble path does not.

### 6. "une clé `profile` en tête" cannot hold with `Row = map[string]any`

`encoding/json` sorts map keys: `json.Marshal(map[string]any{"profile":..., "edition":..., "Profile":...})` gives `{"Profile":"x","edition":"Standard","profile":"prd/a"}`. The key is not first, and the collision rule ("une colonne `profile`") is case-sensitive, while SQL Server column names and PowerShell's `ConvertFrom-Json` are not. A server column `Profile` and the added `profile` are two JSON keys that differ only by case. This is a loud failure on Windows, not a silent one, so it ranks low, but the spec should say "case-insensitive" and drop "en tête", or change `Row`.

## Concluded by reading

### A. The rule "une instance absente des lignes n'a pas répondu" is false under `columns_differ` (hypothesis, silent wrong answer)

Section 5.4 takes the reference columns from the first successful profile in name order, compares names and types exactly, and moves any mismatching profile's rows out of the top-level `rows`. Section 5.6 then tells the agent that an instance absent from `rows` did not answer. On a fleet with mixed versions, a DMV column whose type or presence changed between releases, or a `DatabaseTypeName` that differs for the same expression, puts whole instances under `profiles[].rows` while `profiles_failed` stays 0. So does a profile whose first set has no columns because the query branched. The agent then has two wrong readings available: "it did not answer", which contradicts `profiles_failed: 0`, or "it returned nothing", which is what the field note's purge-Ola and edition questions would turn into a wrong fleet table. Worse, if the odd instance sorts first, it becomes the reference and the other 30 drop out of `rows`. The spec needs a top-level count (`profiles_columns_differ`), and the agent rule must name the three cases: failed, `columns_differ`, zero rows.

### B. The top-level `rowcount` changes meaning, and there is no top-level `truncated` (hypothesis, silent undercount)

In a single-profile result, `rowcount` is the true number of rows the server sent and `truncated` says when `rows` is shorter. In multi mode, the top-level `rowcount` "compte les lignes de `rows`", meaning rows kept after each profile's `-maxrows` cap, and `truncated` exists only per profile. A fleet question such as "how many nonconforming databases" summed from the top level is silently low as soon as one instance passed 50 rows. Nothing at the top level flags it, and the skill teaches "if `truncated` is `true`, say so and give the real `rowcount`". The top level needs `truncated` (any profile truncated) and a true total, or a different field name.

### C. Warnings cannot be attributed to "le profil concerné" with the current Resolver API (hypothesis)

`Resolver.Warn` is one callback fixed at construction. Under `-parallel`, a single shared callback cannot know which goroutine's `Resolve` it is reporting for. Two of the three messages ("the registered-servers file %s is gone", "%s has changed since the credentials were imported") do not name the profile, so the text cannot be routed either. The spec's sentence is not implementable without a per-call warning sink: a `Resolve(p, warn)` signature, or one Resolver per profile, which also fixes point 3.

### D. No overall bound on a fleet run, against an agent's command timeout (hypothesis)

Even with point 1 fixed, a run where ZPA rejects everything with EOF costs 5 s + 20 s of waits per profile at the default `-connect-retries 2`. That is about 25 s for each of ceil(31/4) = 8 waves at `-parallel 4`, about 200 s in all, before any per-attempt timeout is counted. With timeouts, `-connect-retries 5` adds 85 s of waits per profile. Under ZPA a stopped instance also looks like EOF, so this is not only the "tunnel down" case. Claude Code's shell tool defaults to 2 minutes, and a killed `sqlq` emits nothing. The spec should define a global deadline (a `-deadline`, or a cap computed from the flags), and emit the partial result with the unfinished profiles marked, rather than nothing.

### E. Retrying an EOF that arrives after LOGIN7 was sent can repeat a failed login (hypothesis)

The fake server that reads LOGIN7 and closes gives `err="Read: EOF"`, `*net.OpError`, `Is(io.EOF)`. That is `connect_transient` by the table, so it is retried. By then the server may have processed the login. If the password is wrong and the tunnel drops the 18456 reply, `sqlq` makes up to three failed authentications per profile per call. That is the lockout and credential-stuffing pattern AGENTS.md forbids for the agent. The risk is narrow, but the spec states the principle absolutely ("jamais 18456"). It should either restrict the retry to failures before LOGIN7 is written (the driver does not expose this, so it needs a dialer wrapper that counts bytes written) or acknowledge the case.

Set aside, one line each: a `heavy: true` `-saved` entry run on a `prod` selection is not covered by section 5.6, which only asks for the selection's yes, while the skill asks for a separate yes on the cost. `missingQueryError` with the profile-free catalog would print `cannot run on profile ""` and `-list-queries -profile ` with an empty name. Under `path.Match`, `'prd/*'` silently skips nested groups (`prd/fr/x`), since export ids follow every group segment; `-list-profiles` with the same selection mitigates this.

## Not a problem

- An 18456 at login is a typed `mssql.Error`, one TCP accept: `database/sql` does not retry it on its own.
- `io.EOF` at connect is not `driver.ErrBadConn`: one TCP accept per `db.Conn`, so `database/sql` does not multiply sqlq's attempts.
- 4060 followed by 18456 arrives as `mssql.Error{Number:18456, All:[4060,18456]}`: `errors.As` plus `All` lets `database_unavailable` take precedence, as the spec says.
- `ECONNREFUSED` is wrapped with `%w` (`*fmt.wrapError > *net.OpError > *os.SyscallError > syscall.Errno`): `errors.Is` works.
- An RST on accept gives `*net.OpError` with `Is(syscall.ECONNRESET)`: classifiable.
- A DNS failure is `*net.DNSError` (also a `net.Error`, `timeout=false`): distinguishable, as long as the classifier does not treat every `net.Error` as transient.
- A garbage reply gives `invalid packet size, it is longer than buffer size`, untyped: `connect_failed`, which is the right category.
- The context is honored once LOGIN7 is sent (`context deadline exceeded` at 5 s): the login-token phase is bounded, only PRELOGIN and TLS are not.
- Moving resolver warnings off stderr: the only other stderr writer in `main.go` is `emit`'s failure path, as the spec says; `flag` output is already discarded.

Cleanup: `tools/cmd/zzreview`, `tools/cmd/zzjson` and the built binaries are deleted, `git status` is clean in the working copy and in the live checkout, and the fake server task has been stopped. The two `zzreview` processes still running on the machine belong to other panel members' worktrees (`wt-codex-directive`, `wt-vibe-glm-neutral`), and I left them alone.
