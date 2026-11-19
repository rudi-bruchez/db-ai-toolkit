# Review of `docs/superpowers/specs/2026-11-19-sqlq-fleet-design.md` (sqlq fleet mode)

Reviewer: Vibe (Mistral). No SQL Server was reachable or used; all connection evidence below comes
from fake TCP servers on `127.0.0.1:0` driven through the real `go-mssqldb v1.11.0` (the version in
`tools/go.mod`) and the real `database/sql` of Go 1.27, via a throwaway program
(`tools/cmd/zzreview`, since deleted) inside the working copy's `tools/` module. The working copy
is back to a clean `git status`; `go build ./cmd/sqlq` and `go test ./internal/sqlq/ ./cmd/sqlq/`
pass.

## Verified by running

Harness: `tools/cmd/zzreview/main.go`, run as `go build -o /tmp/zzreview ./cmd/zzreview && /tmp/zzreview`
(and once as `go build -race ... && /tmp/zzreview-race`). Each case does `sql.Open("sqlserver", dsn)`
with the same URL shape `Profile.DSN` builds, `db.SetMaxOpenConns(1)`, then one `db.Conn(ctx)`; the
fake server counts accepted TCP connections. DSN used `encrypt=disable` except where noted; the
extra param `&connection+timeout=N` is exactly what `ConnectTimeoutSec` produces in `dsn.go`.

Measurement matrix (error as seen by the caller of `db.Conn`, with `errors.Is`/`errors.As` results):

| Fake server | Accepts per one `db.Conn` | Elapsed | Exact error value | Is/As sees |
|---|---|---|---|---|
| closes on accept (run 1) | 1 | 0s | `*net.OpError` "read tcp …: read: connection reset by peer" | `syscall.ECONNRESET`, `*net.OpError` |
| closes on accept (run 2) | 1 | 0s | `*errors.errorString` "EOF" | `io.EOF` |
| closes after reading the prelogin (x3 calls) | 3 (1 per call) | 0s each | `*net.OpError` "…connection reset by peer" | `syscall.ECONNRESET` |
| accepts, never answers, ctx 5s, no `connection timeout` | 1 | **still blocked after 9s; still blocked 3s after `cancel()`** | — (never returns) | — |
| accepts, never answers, no ctx, no `connection timeout` | 1 | **still blocked after 12s** | — | — |
| sends garbage | 1 | 0s | `*errors.errorString` "invalid packet size, it is longer than buffer size" | **nothing** (no type) |
| dead port (nothing listening) | — | 0s | `*fmt.wrapError` "unable to open tcp connection with host '…': dial tcp …: connect: connection refused" | `syscall.ECONNREFUSED`, `*net.OpError` |
| DNS `.invalid` | — | 1ms | `*net.DNSError` "lookup no-such-host-zz.invalid: no such host" | `*net.DNSError` |
| dribbles 1 byte / 500 ms for 8 s, `connection timeout=2`, ctx 30s | 1 | 3.5s, ended by parse error, **the 2 s deadline never fired** | `*errors.errorString` "invalid packet size, it is shorter than header size" | nothing |
| silent, `connection timeout=3`, ctx 30s | 1 | 3.002s | `*net.OpError` "read tcp …: i/o timeout" | `os.ErrDeadlineExceeded`, `*net.OpError`, `net.Error(timeout=true)`; **not** `context.DeadlineExceeded` |
| answers a valid PRELOGIN, then silent, ctx 5s | 1 | 5.005s | `context.deadlineExceededError` "context deadline exceeded" | `context.DeadlineExceeded`, `net.Error(timeout=true)` |
| garbage with `encrypt=true` (sqlq's default) | 1 | 0s | `*errors.errorString` "invalid packet size, it is longer than buffer size" | nothing |

Also run: `errors.Is(mssql.RetryableError{}, driver.ErrBadConn)` → **true** (the driver's
post-send retry signal satisfies `ErrBadConn`, `error.go:131`).

`path.Match` matrix (spec §5.1), names `{"prd/a", "prd/a/b", "PRD/a", "prd/a\r", "prd/a ", "a", "prd/", "", "sta/b"}`:

| pattern | selects |
|---|---|
| `prd/*` | `"prd/a"`, `"prd/a\r"`, `"prd/a "`, `"prd/"` |
| `*` | `"a"`, `""` (only slash-free names — on a registered-servers fleet where every name contains `/`, `*` selects nothing) |
| `prd/*/*` | `"prd/a/b"` only |
| `prd/a` | `"prd/a"` only (not the CR twin `"prd/a\r"`) |
| `[` | `ErrBadPattern` |
| `""` | `""` only |

Race run (`-race`), 8 goroutines × 50 `Resolve` calls on one shared `*sqlq.Resolver` with a real
credentials file: 5 `WARNING: DATA RACE` reports at `internal/sqlq/secret.go:266/281/285`
(`loadStore`'s `loaded`/`store` fields), and 6 of the 8 goroutines received a spurious functional
error `credential store /tmp/…/credentials.json is unavailable` — a goroutine saw `loaded == true`
with `store == nil` because `loadStore` sets `loaded` before assigning `store`.

Defects these runs prove (ranked by cost of discovering them after implementation):

**1. A connection attempt against a silent tunnel is unbounded, and the per-attempt connect context
cannot bound it — the spec's own retry test would hang as written.** The spec splits one context
into "un contexte de connexion par tentative" bounded by "le délai de connexion (celui du profil)".
But in the driver, the PRELOGIN response is read by `readPrelogin` → `tdsBuffer.BeginRead` → raw
`conn.Read` (`tds.go:306`, `buf.go:158`), which has no `ctx.Done` select; the context is only
checked at token level (`token.go:1296`), i.e. from the login-response stage onward. Measured: with
ctx 5 s and no `connection timeout` in the DSN, `db.Conn` was still blocked after 9 s and still
blocked 3 s after `cancel()`; only after a valid prelogin does the ctx fire (5.005 s,
`context.DeadlineExceeded`). The only thing that bounds the prelogin read is the DSN
`connection timeout` (`timeoutConn` sets a per-read deadline, `net.go:21`) — which `dsn.go` sets
only when `ConnectTimeoutSec > 0`. So for any profile without `connectTimeoutSec`, a ZPA tunnel that
accepts TCP and then goes silent hangs the attempt forever, ctx or not, and `-connect-retries`
multiplies the hang. Section 6's own test — "ferme les deux premières connexions … puis ne répond
plus" — has the third attempt block in `readPrelogin` forever unless the test profile sets a
`connection timeout`. Two consequences the spec leaves open: (a) what the connect-context timeout
is when the profile sets none (reading "no timeout" = infinite hang; reading "fall back to
`-timeout`" still hangs, because the ctx does not interrupt the prelogin read — only the DSN
parameter does); (b) even with `connectTimeoutSec = D` set, D is a **per-read** deadline, not a
login deadline: the dribble server ran 3.5 s against `connection timeout=2` without the deadline
ever firing, so a slow-drip tunnel defeats it. Worst-case wall time of one call is therefore
unbounded as specified; with a sane default D the arithmetic is `-connect-retries 2` → 3 attempts +
5 s + 20 s delays + batch `-timeout`: 3×D + 25 + 30 ≈ 100–145 s, and `-connect-retries 5 -timeout 120`
→ 6×D + 85 + 120 ≈ 295 s with D=15 — past a "few minutes" agent tool limit, with no total deadline
anywhere in the spec. The spec must define a default `connection timeout` in the DSN when the profile
omits `connectTimeoutSec`, and a total wall-clock budget for the call.

**2. The shared resolver of §5.3 is not safe to share across goroutines as written today.** §5.3
says "le résolveur de secrets est créé une fois" and then runs profiles concurrently through
`execute`. `Resolver.loadStore` (`secret.go:266–285`) reads/writes `r.loaded` and `r.store` with no
lock; the `-race` run above shows 5 data races there, and the non-race run shows the functional
consequence: goroutines get `credential store … is unavailable` and the profile fails with exit 1 —
under `-parallel 4` on a DPAPI fleet, some profiles would spuriously fail on every multi run. The
spec must either add synchronization to `Resolver` (a change to `internal/sqlq` the spec does not
mention) or drop "créé une fois" and give each goroutine its own resolver (each then re-reads and
re-hashes the store — `warnIfStale` stats and hashes the RegSrvr file on every `Resolve` anyway).
Related, same section: `Warn` is a single callback on the shared resolver with no profile argument,
so attributing a warning to "le profil concerné" requires wrapping `Resolve` per goroutine — which
is exactly what sharing one `Resolver` prevents; and the warn closure appending to a shared slice
needs a mutex.

**3. "sqlq ne réessaie jamais après l'envoi du lot" is incidental, not structural, and the spec does
not make it structural.** The driver calls `checkBadConn(ctx, err, /*mayRetry=*/ true)` when
`sendQuery` fails (`mssql.go:784` and `:857`) — i.e. after the batch has been written to the socket,
so it may already have reached the server — and with `DisableRetry` at its Go ≥1.18 default of
`false` (`msdsn/conn_str_go118.go:10`) it returns `RetryableError`, for which
`errors.Is(err, driver.ErrBadConn)` is **true** (measured above). `database/sql`'s `db.retry`
(`sql.go:1578`, `maxBadConnRetries = 2`, plus a final `alwaysNewConn` pass) re-executes a DB-level
`QueryContext`/`ExecContext` up to 3 times on such an error. sqlq today escapes this only because
`execute` holds a `*sql.Conn` and `Conn.ExecContext/QueryContext` go through `execDC`/`queryDC`,
which have no retry loop (read in `sql.go:2027/2037`). The spec's §4 rewrite must therefore both
keep the held-`Conn` path and set `disableretry=true` in the DSN (`dsn.go` builds the URL, so it
currently never sets it); otherwise one refactor to `db.QueryContext` — the natural shape once the
connect and batch contexts are split — silently re-sends a batch after a mid-write failure, which
§2 forbids. During `db.Conn` itself the stdlib can also retry (`db.Conn` → `db.retry`, up to 3
`Connect` calls) whenever `Connect` returns an `ErrBadConn`-compatible error (e.g. `ResetSession`
failure); measured, the raw transport errors (EOF / ECONNRESET / ECONNREFUSED / DNS) are **not**
`ErrBadConn`-compatible, so one `db.Conn` opened exactly 1 TCP connection in every fake-server case
above — attempt counting at the `db.Conn` level is accurate for those, and only those.

**4. Type-based classification (§4.1's load-bearing claim) is impossible for exactly the TLS and
garbage cases, and `connect_timeout` has two different type signatures.** The garbage server
returns a bare `*errors.errorString` ("invalid packet size, …") with no type — `errors.Is/As` see
nothing, so only exact-text matching can file it under `connect_failed`; TLS handshake failures are
wrapped with `%v`, not `%w` (`tds.go`: `fmt.Errorf("TLS Handshake failed: %v", err)`), which
destroys the chain — `x509`/`tls` types are unreachable via `errors.As`, so the table's "erreur TLS
ou de certificat" row can only be implemented by text matching. And `connect_timeout` arrives as
**either** `context.DeadlineExceeded` (login-response stage, measured 5.005 s after a valid
prelogin) **or** `*net.OpError` with `os.ErrDeadlineExceeded` / `net.Error(timeout=true)` (the DSN
`connection timeout`, measured 3.002 s) — an implementation that only checks
`errors.Is(err, context.DeadlineExceeded)` files the profile-timeout case under `connect_failed`
and refuses to retry it. Finally, close-on-accept yields `io.EOF` in one run and
`ECONNRESET` in another (timing) — both are in the spec's `connect_transient` list and both are
`errors.Is`-able, so that part is fine, but the classifier must not assume one of them.

**5. The §5.4 reference-columns rule silently empties the top-level `rows` when the first
successful profile returns no result set.** "`columns` est `profile` suivi des colonnes du premier
profil réussi dans l'ordre des noms" — taken literally, a successful profile whose batch produces no
result set (e.g. only `PRINT`, or a `DECLARE`) is "réussi" with `columns: []`, so the top-level
`columns` becomes just `[profile]`, every profile that actually returned data gets
`columns_differ: true`, and all their rows land only in `profiles[].rows` while the top level shows
`rows: []`, `rowcount: 0`, `profiles_ok: N`, `error: null`. An agent reading the top-level rows —
the shape §5.4 tells it to read — concludes "no data anywhere", a silent wrong answer. The rule
should say "premier profil réussi *dont le premier jeu de résultats existe*", or mark the
result-set-less profile explicitly. Two adjacent gaps in the same output: the top-level object in
the §5.4 example has **no `truncated` field**, so a per-profile `-maxrows` cap is invisible at the
level the agent reads first; and a profile that fails *after* returning rows (exit 2, partial rows in
`profiles[].rows`) is excluded from the top-level `rows` with top-level `error: null` — §5.6's "read
`profiles_failed` first" is the only guard, and nothing in the JSON itself marks the top-level rows
as partial.

## Concluded by reading

Hypotheses, not yet run — each is a place where two plausible readings of the spec produce different
behaviour, or where the spec conflicts with a stated rule:

**6. Exit code for a multi run that selects an `unusable` profile is ambiguous (1 or 5).** §5.1
says the unusable profile "apparaît dans le résultat avec `category: usage`"; the §4.1 table maps
`usage` to exit 1, but the §5.5 table reserves 1 for "usage, avant tout lancement" and defines 5 as
"au moins un profil a échoué". Since the profile *is* in the result, the run did launch, so 5 seems
intended — but then `profiles[].exit_code` would be 1 for that entry while the top level is 5, and an
agent keying its reaction on the category (`usage` = "your invocation was malformed") versus the
top-level code behaves differently under the two readings. The spec should state it in one sentence.

**7. The `-list-profiles` preview and the run cannot diverge only if the preview is implemented with
the identical matcher.** Both use `path.Match` over `profiles.Names()`, so as specified they agree
(the matrix above is the exact semantics both get). The traps the matrix exposes: `*` selects
nothing on a grouped fleet (every registered-servers name contains `/`), so `-profiles-match '*'` is
a usage error there and the skill must steer to `-all`; `prd/*` also selects `"prd/"` and the
CR/space names `"prd/a\r"`, `"prd/a "` — the 18/11 trap — while the exact pattern `prd/a` does not.
JSON escaping makes the CR visible in the transcript, so this is a usability trap rather than a
silent one, but the preview is the only place an agent sees that a wildcard selected two
near-identical names.

**8. The prod guard in §5.6 has a window the spec does not close.** The rule is procedural: run
`-list-profiles` with the same selection, show it, wait for a yes that "couvre cette sélection". But
the run re-evaluates the pattern against the profiles file *at run time*, and `managedBy:
registered-servers` entries are rewritten on every export — a `prod` profile matching the pattern
can appear between the preview and the run, and nothing inside `sqlq` checks `environment` (by
design, like the single-profile rule today). The multi mode widens the blast radius from one named
profile to pattern × N instances; the skill text should require the preview to be the immediately
preceding command and the yes to name the pattern, which is the strongest reading available without
putting an environment check inside the tool.

**9. Warnings plumbing under multi mode has two underspecified corners.** (a) §3.2 requires
warnings on *every* emit after the resolver is created, including the save-failure emit inside
`run` (`main.go`'s `writeSavedQuery` error branch emits its own `Result`) — fine as designed, but it
is the easiest path to miss in implementation, and `fail()` emits a bare `Result` with no
`warnings` field at all, so `Result.MarshalJSON` must default it like `messages`. (b) §5.3 wants
resolver warnings in the `warnings` of "le profil concerné", but the shared `Resolver.Warn` has no
profile parameter and the store is cached after the first load — combined with defect 2, the clean
implementation is one resolver per goroutine with its own warning sink, which contradicts
"créé une fois" as written. Also note `RecordVerified` is an unlocked read-modify-write
(`registry.go`): §5.3 says the registry is updated *once*, with the first profile in name order that
succeeded — deterministic only if all profiles have finished before writing; implemented as "first to
finish" it is nondeterministic and concurrent `RecordVerified` calls lose entries.

## Not a problem

- One `db.Conn` call opened exactly **1** TCP connection in every transport-failure case (accepts
  counted server-side): `database/sql` does not retry raw driver errors (`io.EOF`, `ECONNRESET`,
  `ECONNREFUSED`, `*net.DNSError`) during connect, so §4.2's attempt counting at the `db.Conn`
  level is accurate for those.
- `io.EOF`, `syscall.ECONNRESET`, `syscall.ECONNREFUSED`, `*net.DNSError` and `mssql.Error` are all
  reachable through the driver's wrapping via `errors.Is`/`errors.As` (matrix above) — type-based
  classification works for the transport and server-error cases the spec lists.
- The resolver's warnings (`secret.go` `warnIfStale`/`r.warn`) cite only local file paths and
  profile names, never a secret — §3.3's claim holds; the retry-warning redaction requirement
  (§3.3) is correctly aimed at driver text, which can quote the DSN.
- `path.Match` returns `ErrBadPattern` for `[`, which §5.1 maps to a usage error — confirmed.
- The catalogue is read-only after `LoadCatalog` returns (`resolveCollisions` mutates before
  sharing), so loading it once and sharing it across goroutines is safe; only `RecordVerified`
  writes, and §5.3 scopes it to a single update.
- `emit`'s only stderr write is the JSON-encoding failure itself, which §3.2 explicitly keeps; every
  other path already goes through `fail`/`emit` in JSON form.
- The guard/row-cap/error-redaction contract of §2 is untouched by the spec; `sqlq` builds and
  `go test ./internal/sqlq/ ./cmd/sqlq/` passes in the working copy (run before deleting the
  harness).
- The `profile`/`sqlq_profile` key rule is coherent: any server-side `profile` column changes that
  profile's column list, so a colliding profile can never merge into a reference whose injected key
  is `profile` — the two readings (per-profile vs global injection) produce the same merge set. Only
  the doubly-pathological case of a server set containing both `profile` and `sqlq_profile` would
  violate "n'écrase jamais une valeur du serveur"; not worth a rule.

Set aside beyond the ten above: none — every other candidate I checked (per-read vs total
`ConnTimeout` semantics, `DialTimeout` default of 15 s per protocol bounding only the TCP dial,
`sqlexp` message queue, `unavailableCause` precedence over 18456) is covered by the paragraphs
above or turned out fine.
