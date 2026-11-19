# Review of `docs/superpowers/specs/2026-11-19-sqlq-fleet-design.md`

Working copy: `wt-vibe-glm-neutral`, Go 1.27, driver `github.com/microsoft/go-mssqldb v1.11.0`
(the version of `tools/go.mod`). The throwaway programs described below lived under
`tools/cmd/zzreview/` and were deleted after running; no background process or file outside the
working copy survives (except this report).

---

## Verified by running

**How.** A throwaway `main` (package `tools/cmd/zzreview`) built TDS-less fake servers with
`net.Listen` on `127.0.0.1:0` (close on accept; RST via `SO_LINGER=0`; a hand-rolled PRELOGIN
response offering encryption, then reset during the TLS-in-TDS handshake; a server that accepts
and never answers; a garbage answer), and drove them through the real call path of the design:
`sqlq.Profile.DSN` from `tools/internal/sqlq/dsn.go` (so `encrypt=true`,
`trustservercertificate=true`, `connection timeout` only when `ConnectTimeoutSec > 0`), then
`sql.Open` + `db.Conn(ctx)`. Each accepted connection was counted. A second throwaway test drove
`sqlq.Resolver` from concurrent goroutines under `-race`. `sqlq` from this working copy was
also run against a profiles file and a synthetic credentials store, both written by hand.

### A. Classification material, per fake server (one `db.Conn` call each)

| Fake server | errorString (verdict) | `errors.Is` verdicts that matter |
|---|---|---|
| dial to a closed port | `unable to open tcp connection with host '127.0.0.1:p': dial tcp ...: connect: connection refused` | `ECONNREFUSED=true` (driver wraps the dial error with `%w`) |
| server closes on accept, run 1 | `EOF` | `io.EOF=true` |
| server closes on accept, run 2 | `read tcp ...: read: connection reset by peer` | `ECONNRESET=true` (the same incident surfaces as EOF or ERESET depending on timing; the design's `connect_transient` covers both — good) |
| RST before the prelogin answer | `read tcp ...: read: connection reset by peer` | `ECONNRESET=true` |
| valid PRELOGIN (encryption required), then RST **during the TLS handshake** | `TLS Handshake failed: cannot read handshake packet: read tcp ...: read: connection reset by peer` | `ECONNRESET=false`, `io.EOF=false`, `*net.OpError` not found — **the chain is destroyed** |
| valid PRELOGIN, clean FIN during the TLS handshake | same text, `connection reset by peer` | same: unclassifiable, and even the message text says "reset" for a clean close |
| garbage prelogin | `invalid respones, expected packet type 4, PRELOGIN RESPONSE` | nothing matches (correctly `connect_failed`) |
| silent server, profile `ConnectTimeoutSec=1` | `read tcp ...: i/o timeout` | `os.ErrDeadlineExceeded=true`, **`context.DeadlineExceeded=false`** |
| silent server, profile `ConnectTimeoutSec=0`, `db.Conn` ctx = 4 s | — | **no verdict: `db.Conn` had not returned after 38 s when the harness killed it** (see B) |

Accepted-connection counts: exactly 1 per `db.Conn` in every completed scenario;
`errors.Is(err, driver.ErrBadConn)` was false in all of them and `errors.As(*mssql.Error)` too.
So `database/sql` performs no hidden retry at `db.Conn` (`DB.Conn` uses `db.retry`, but the
driver's `Connector.Connect` never returns `driver.ErrBadConn`): a `-connect-retries`
implementation genuinely owns the attempt count, and `connect_attempts` is well-defined.

### B. The context does not bound the login phase; the profile's "connection timeout" may not exist

The silent-server probe (profile with `ConnectTimeoutSec=0`, `db.Conn` called under a 4 s
context) never returned: killed at 38 s, well past the context deadline, with exactly one
accepted connection. The driver applies `timeoutConn` deadlines only when `ConnTimeout > 0`
(`newTimeoutConn(conn, p.ConnTimeout)`), and nothing else interrupts the prelogin/login reads:
`database/sql` passes `ctx` to `Connector.Connect` and then blocks on the driver's return.
With `ConnectTimeoutSec=1` the same probe returned in 1.001 s with `read tcp ...: i/o timeout`
(`os.ErrDeadlineExceeded=true`, `context.DeadlineExceeded=false`).

This matters to the design's own targets: `registered-servers/Export-RegisteredServers.ps1`
never writes `connectTimeoutSec` (verified by reading the exporter: it writes `auth`, `user`,
`passwordDpapi`, `mode`, `encrypt`, `trustServerCertificate`, `environment`, `managedBy`,
`source` — no `connectTimeoutSec`). Every generated profile of the 31-instance fleet therefore
has no connection timeout at all.

### C. The premises of section 3 (warnings) are real

Running the current `sqlq` with a hand-written profiles file and synthetic credentials store
whose `source.path` does not exist prints the resolver warning on stderr before the JSON:

```
sqlq: the registered-servers file /tmp/…/gone.xml is gone; credentials already imported still work
{"profile":"zz",…,"error":{"number":0,…,"message":"profile \"zz\": cannot decrypt credential \"k\" …}}
```

Under `2>&1` the merged stream is exactly what the field notes describe: a first line that is
not JSON.

### D. The shared resolver races under concurrency

`go test -race ./cmd/zzreview/` on a test that calls `Resolve` from 8 goroutines on one
`&sqlq.Resolver` (DPAPI profile, synthetic store, stubbed `Unprotect`) reports:

```
WARNING: DATA RACE
Read at … by goroutine 10:
  github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq.(*Resolver).loadStore()
      …/internal/sqlq/secret.go:266
Previous write at … by goroutine 17:
  github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq.(*Resolver).loadStore()
      …/internal/sqlq/secret.go:272
  (via Resolve, resolveDpapi)
```

### Defects, ranked by what they would cost after the code is written

**1. The per-attempt connection timeout is the profile's — and for every generated profile it
does not exist, so a fleet run against a silent tunnel never ends (severity: silent
unanswer).** § 4.2 states "Chaque tentative a son propre délai de connexion (celui du profil)".
Verified above (B): when `connectTimeoutSec` is 0 the DSN carries no `connection timeout`, the
driver sets no deadline after the dial, and neither a context nor `database/sql` can interrupt
the login. The design's factual premise in the same sentence — "aujourd'hui un seul contexte
couvre les deux" — is false in the only sense that matters: today's single `-timeout` context
already fails to bound the login (the 38 s hang is the current binary's behaviour too). After
the change, one half-open ZPA channel hangs its worker forever; with `-parallel 4`, four of
them park the whole fleet call with no error, no `profiles_failed`, no exit code — the exact
"one result per instance" answer the feature exists to produce is never produced. The design
must set a connection timeout in the DSN whenever the profile lacks one (the driver's own dial
default is 15 s; `timeoutConn` is the only deadline the login honours), or make the per-attempt
connect context enforceable some other way. Note that its own § 6 retry test
("ferme les deux premières connexions … puis ne répond plus") silently assumes a bounded third
attempt: as written, that test hangs too unless the fixture also sets a connection timeout the
spec never requires.

**2. A transport cut after the prelogin answer is unclassifiable by the mechanism the design
mandates, and lands in `connect_failed`, never retried (severity: wrong conclusion about the
instance).** § 4.1 mandates classification "sur la chaîne d'erreurs entière (`errors.Is`,
`errors.As`, `mssql.Error.All`), pas sur le texte du message", with a carve-out only for driver
errors that carry no type. Verified above (A): under `encrypt=true` — sqlq's default when the
profile does not override it (`dsn.go`) — a reset or close between the prelogin response and
the end of the TLS handshake surfaces as `TLS Handshake failed: cannot read handshake packet:
read tcp …: read: connection reset by peer`, wrapped by the driver with `%v`
(`tds.go:1292`): `errors.Is(ECONNRESET)` and `errors.Is(io.EOF)` are both false. Per the table
that error bins in `connect_failed` ("un nom introuvable ou un certificat refusé ne guérit pas
en 20 s"), whose skill text then tells the agent it is durable — for what is plausibly the
10/11-style transient. The design's escape hatch exists but names this case nowhere: § 6's
classification matrix has "erreur TLS" only as a `connect_failed` fixture and no case for the
"TLS Handshake failed: …" message. Two extra facts from the run make the gap sharper: the
`encrypt=strict` path wraps with `%w` instead (`tds.go:1162`), so classification would work
there but not under `encrypt=true` — the two spellings must both appear in the "message exact
observé" list; and a *clean FIN* also reports "connection reset by peer", so a message-only
fallback matching on "EOF" would miss it. Cheapest fix: classify `errors.As(*net.OpError)` with
`Op == "read"` failures whose text carries the handshake prefix as `connect_transient`, pinned
by a test against a fake server like the one above.

**3. The design never names `os.ErrDeadlineExceeded`, which is the only form a driver-fired
connection timeout ever takes (severity: the retryable category is unreachable in its main
manifestation).** Verified above (A): with `connectTimeoutSec=1` against a silent server the
error is `*net.OpError{Err: os.ErrDeadlineExceeded}` ("i/o timeout"), and
`errors.Is(err, context.DeadlineExceeded)` is **false**. § 4.1 defines `connect_timeout` as
"délai de connexion dépassé (`connection timeout` du profil, ou le contexte)", but § 6's
classification test table contains only `context.DeadlineExceeded`. An implementation built to
the tested list (`errors.Is` chain per § 4.1) files the profile-connection-timeout case in
`connect_failed` — unclassified, never retried — the exact opposite of the stated intent. The
matrix needs an `os.ErrDeadlineExceeded` row pinned by phase (at login: `connect_timeout`;
after the batch it would need a different reading, see also "not a problem" below).

**4. One resolver, parallel workers: a data race the design does not mention (severity:
flaky builds today, per-profile warning mis-routing in use).** § 5.3 says "Le résolveur de
secrets est créé une fois", used from `-parallel` workers on `-saved`/DPAPI fleets; verified
above (D): `Resolver.loadStore` (`secret.go:266/272`) races on `r.loaded`/`r.store` —
`go test -race` fails on the existing code under exactly the concurrency the design introduces.
Relatedly, § 3.2 prescribes a warn callback that appends to one slice, while § 5.3 requires
warnings "vont dans le `warnings` du profil concerné" — a single shared callback cannot route
per profile once four resolutions interleave. The design should say: resolver made
goroutine-safe, and the warning target captured per profile (e.g. a per-worker buffer), rather
than leaving every implementer to rediscover both.

---

## Concluded by reading

These are argued from the code and the spec text, not from an execution that isolates them;
treat them as strong hypotheses.

**5. § 5.2's refusal of profile-scoped `-saved` entries cannot be implemented from the § 5.3
load.** § 5.3 loads the catalogue "une fois, sans profil", and § 5.2 refuses "une entrée
enregistrée pour un profil (`profiles/<profil>/`)". But `LoadCatalog`
(`internal/sqlq/catalog.go`) walks `personal/profiles/<p>/` **only** when `cfg.Profile != ""`;
in the profile-free view profile-scoped entries are invisible, so the refusal has nothing to
match against. The visible consequence: when a profile-scoped file collides with a bundled,
tsql-scripts or `_generic` name, the single-profile run on that profile executes the
profile-scoped script, while the fleet run silently executes the generic/bundled one for every
profile — same `-saved` name, different SQL, no `columns_differ`, no warning. The design must
specify loading the profile trees for the refusal check (or publishing a per-name
"profile-bound" marker) — as written, the rule contradicts the load model it depends on.

**6. § 4.1's priority rule is self-contradictory on the exact chains it tries to order.** "L'ordre
de priorité est celui du tableau en remontant" makes `connect_transient` (row 3) beat
`login_failed` (row 6) and `database_unavailable` (row 7); the very next clause says the latter
two "l'emportent sur une erreur de transport de la même chaîne". One of the two sentences has
to give. The practical impact is limited (a raw net error and an `mssql.Error` rarely share one
chain — login errors arrive typed through `doneStruct.getError()`; more in "not a problem"),
but two implementers following the text literally can produce different classifiers for a
chain mixing them, and the tests of § 6 would not distinguish. One sentence fixing the
precedence (server-sent errors always outrank transport, or a strict table scan order) closes
it.

**7. The CR-in-profile-name trap that motivates the whole feature is never handled.** § 1
quotes the 18/11 incident ("des noms de profils terminés par CR") as a pitfall of the shell
loop, and the design then never mentions CR again. Verified with the current binary today: a
profile named `"friend\r"` (escaped CR in the JSON, exactly what a CRLF-writing generator
produces) loads, lists as `friend\r` next to `friend`, and cannot be typed as
`-profile friend\r` on a command line — while `-all` and `-profiles-match 'friend*'` will
run both, displaying two visually identical profile names in `rows` and `profiles[]`. The
multi-profile mode thus inherits the third pitfall it was justified by, in a quieter form.
`LoadProfiles` already refuses an inline `password` loudly; refusing names that contain
control characters would close the trap at the same chokepoint, and would be cheaper than
living with an undisplayable name forever.

**8. The multi-profile shape leaves two channels unspecified where the published contract has
them (minor, but the skill text will have to guess).** (a) An `unusable` profile "apparaît dans
le résultat avec `category: usage`" — but does it count in `profiles_failed`, and therefore
force exit code 5? Both readings are defensible; under the first, a fleet of one Windows-only
profile plus N usable ones always exits 5 on this machine, and § 5.6 then tells the agent to do
"une seule relance ciblée" of a profile that is machine-unusable by construction. (b) The
multi object of § 5.4 has no top-level `messages`, while § 3.1 keeps the catalogue and registry
notes in `messages` "car la documentation du skill les y annonce". For a `-saved` fleet run,
"le catalogue est chargé une fois" — where do its notes (a missing tsql-scripts source, a
disabled personal layer) go? Duplicated per profile, top level, or dropped are three different
behaviours, and the skill will promise one of them.

Two further observations, not defects on their own: the `-connect-retries 2` default changes
the observable latency of every single-profile connect failure from ~0 to at least 5 s + 20 s
of sleeps (a refused port stops being a 200 ms answer); the design should say out loud that
this is the new default experience. And § 5.3's "délais entre tentatives : 5 s puis 20 s" are
fixed constants with no flag — fine, but note they interact with `-parallel`: a ten-profile
fleet where half refuse gives roughly 25 s serial-per-worker, not the elapsed of § 5.4.

---

## Not a problem

Checked and fine, one line each:

- `io.EOF` and raw `ECONNRESET` (the cuts *before* the prelogin answer — the bulk of ZPA
  incidents) arrive unwrapped from the driver and are classifiable by `errors.Is`: both
  spellings of the close-on-accept case land in `connect_transient`, whichever the timing
  produces.
- `ECONNREFUSED` survives the driver's `unable to open tcp connection …: %w` wrapper;
  `errors.Is(syscall.ECONNREFUSED)` is true — `connect_transient` is implementable as
  specified.
- `database/sql` adds no retries of its own at `db.Conn` (exactly one dial per call in every
  probe; the driver never returns `driver.ErrBadConn` from `Connector.Connect`), so
  `connect_attempts` and the retry loop are entirely sqlq's to own.
- Login errors arrive typed: a normal failed login reaches `connect()`'s `doneStruct` branch
  (`doneError` bit, not `doneSrvError`) and returns an `mssql.Error` carrying the `All` chain
  with a "login error: " prefix — `errors.As` finds it, so `login_failed` and
  `database_unavailable`-over-`transport` classification is implementable, consistently with
  the existing `unavailableCause` behaviour.
- A garbage reply produces a plain untyped driver error → `connect_failed`, not retried:
  the desired outcome for `connect_failed`.
- The premise of § 3.2 is real (verified with the current binary): the resolver warning goes to
  stderr before the JSON and breaks `2>&1` parsing exactly as the field notes say, and
  grep confirms those are the only two stderr writers in `sqlq` (`emit`'s failure and the
  resolver callback), so the design's inventory of stderr is complete.
- Exit codes 0–4 and their meanings are kept for single-profile calls (checked against
  `execute`/`guard`/`run` paths); code 3-before-launch in multi mode matches the single mode's
  guard outcomes, and § 5.2's refusal list does not change any single-mode behaviour.
- `-list-profiles` publishing no server/database names is preserved by § 5.1's selection
  APIs (`-profiles-match`, `-all` reuse the same `describeProfiles`), so the estate-map
  minimisation rule of AGENTS.md is not weakened by the fleet mode.
- The `verified` registry write in § 5.3 (single write, first-in-name-order success) is safe
  against the parallel workers of § 5.3: `RecordVerified` re-reads and replaces the file
  atomically, and the design defers it to one call.
- `path.Match` in § 5.1 gives exactly the documented semantics (`*` stops at `/`, invalid
  pattern = usage error): the `'prd/*'` group example is correct, and profile names with `/`
  (SSMS groups, as `ProfileDir` already handles) are matchable as designed.

---

Defects counted: 8 (4 verified by running, 4 concluded by reading); 2 lower-severity
observations folded into § 8's paragraph rather than listed as defects; nothing else set aside.
