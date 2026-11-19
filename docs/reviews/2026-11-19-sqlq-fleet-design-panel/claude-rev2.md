# Review of the revised spec sqlq fleet (rev 2), Claude, adversarial

Spec: docs/superpowers/specs/2026-11-19-sqlq-fleet-design.md at c10d922. Driver go-mssqldb v1.11.0.
Throwaway programs: rev2-work/repo/tools/cmd/zzrev2 (fake TDS servers on 127.0.0.1:0, encrypt=disable)
and rev2-work/repo/tools/cmd/zzrev2cat (catalogue). Nothing in the real repository was touched; no
process left running.

## Verified by running

1. The watchdog does not bound the phases that have no TCP socket yet, and the spec removes the
bound they had. 4.2 says the dialer keeps the TCP conn and the watchdog closes it at S, and that
-timeout now starts once the connection is open "dans un contexte distinct"; it never says what
context db.Conn receives. Implemented literally (watchdog on the TCP conn, db.Conn(context.Background())),
a named instance without port whose SQL Browser (UDP 1434) does not answer:
`zzrev2 browser` -> `named instance, mute browser, -connect-timeout 2: elapsed=15.01s watchdogFired=true
dials=[udp 127.0.0.1:1434] err=no instance matching 'INST' returned from host '127.0.0.1'`.
The watchdog fired at 2 s with nothing to close; the call came back after the driver's own
`dial timeout` (15 s default, msdsn/conn_str.go:579). The same holds for an unanswered TCP SYN and for
net.LookupIP (protocol.go:83, no context). In the other direction, -connect-timeout 60 cannot extend the
dial past those 15 s. Worse, the fedauth token fetch (prepareLogin and adalTokenProvider receive the
db.Conn context, tds.go) was bounded by -timeout today and would be bounded by nothing but azidentity's
own defaults: closing the socket does not interrupt an HTTP token request. So "-connect-timeout S :
l'échéance de chaque tentative, de la numérotation à la fin du login" is false, and the 6 test
("rend la main entre 2 et 4 s") cannot see it, since every fake server accepts on 127.0.0.1. Fix: db.Conn
gets context.WithTimeout(S) in addition to the watchdog, and the DSN gets `dial timeout` = S; the
watchdog must also follow a second dial (login routing, tds.go:1430 `goto initiate_connection`, as an
Azure SQL redirect does), not "the" conn.

2. -saved on a fleet: the rule in 5.3 defines only "the entry differs" (source or hash), but the common
divergence is presence versus absence, and the spec does not say what absence does. -save-query only
ever writes into the profile's own directory (checkSave -> ProfileDir), so every saved query is
profile-scoped. `go run ./cmd/zzrev2cat` with edition-check saved for prd/a, a different text saved
for prd/c, nothing for prd/b:
`profile prd/a: found=true source=personal hash=08c4ce91a953` /
`profile prd/b: found=false` / `profile prd/c: found=true source=personal hash=8d05a70e893b`.
Whether prd/b is a refusal of the whole call (then a query saved through -save-query can never run on
a fleet, although the skill tells the agent to offer -save-query), a per-profile code 1 (then the run
exits 5 with N-1 failures), or a silent skip, is undecided, and it changes the output contract and the
5.7 rules. The same question arises for an entry rejected in one profile's view by a collision with
_generic. Decide it in the spec; the likely answer is "only bundled, tsql-scripts and _generic entries
run on a fleet; a profile-scoped entry is refused, with the instruction to move it to _generic".

3. The bounded-open test of section 6 contradicts the triage's own measurement and will fail on a
correct implementation. It requires a return "entre 2 et 4 s avec connect_timeout, y compris quand le
profil a connectTimeoutSec à 1". `zzrev2 ct1` (mute server, connection timeout=1, watchdog at 2 s):
`elapsed=1.00s watchdogFired=false class=deadline err=read tcp ...: i/o timeout`. The per-read deadline
fires first, as triage.md says ("le délai par lecture tire d'abord (1,00 s)"); the "valid PRELOGIN then
silent" server does the same. Only the byte-dribbling server reaches the watchdog. An implementer
holding this test will either loosen it or stop passing connectTimeoutSec during the open, which
contradicts 4.2 ("garde son effet actuel"). Write the window as "between min(S, connectTimeoutSec) and 2S".

4. The disableretry test pins nothing but the DSN string. Section 6: "un faux serveur qui coupe pendant
la réception du lot reçoit une seule connexion". `zzrev2 disableretry` (cut after the batch is
received): one TCP connection in all four cases, disableretry false or true, *sql.Conn or the pool.
`zzrev2 disableretry2` (RST before the batch is sent, which is what makes sendQuery fail):
`disableretry=false sql.Conn tcp connections=1`, `disableretry=false db.ExecContext tcp connections=2`,
`disableretry=true sql.Conn 1`, `disableretry=true db.ExecContext 1`. Through sqlq's *sql.Conn the
count is 1 with or without the flag, so the behavioural half of the test passes if disableretry is
removed. To make it bite, the test must drive the pool path (db.ExecContext) with an RST before the
send, or be dropped in favour of the DSN assertion alone, said as such.

Also run, no defect: msdsn.Parse of the URL form honours `disableretry=true` (DisableRetry=true;
default false; DialTimeout 15s). A fake server answering 18456 at login gets exactly one TCP connection
and the error is an mssql.Error.

## Concluded by reading

5. A fleet where every selected profile is unusable exits 0. 5.6: code 0 when "tous les profils tentés
ont réussi (les profils skipped ne comptent pas)", and profiles_skipped is "sans effet sur le code de
sortie". `-all` on Linux over a DPAPI fleet (every generated SQL-auth profile) gives selected 31,
skipped 31, ok 0, rows [], exit 0: a vacuous success, where "aucun profil choisi" is code 1 and the
single-profile run of an unusable profile is an error. Zero profiles attempted should be code 1.

6. The documented bound on repeated authentications is not the real one, and the evaluation order adds
a window. 4.4 justifies "1 réessai" by "deux tentatives d'authentification au lieu d'une", but 4.5 then
allows the agent one relance of a connect_transient/connect_timeout profile, and 5.7 one targeted
relance after a fleet run: each relance is again 1+1 attempts, so the wrong-password-plus-lost-18456
case reaches 4 authentications, near a common lockout threshold of 5. Separately, 4.3 evaluates "le
chien de garde a tiré" before "une mssql.Error dans la chaîne": if the timer fires between the driver
returning a received 18456 and the Stop, the call is classed connect_timeout and retried. Put the
mssql.Error test first (a server error proves the socket was not the failure), and state the bound
including the agent's relance.

7. -deadline closes connections instead of cancelling the batch. 5.4: in-progress profiles "sont
abandonnés (leur connexion fermée)". SQL Server notices a dropped client only when it next writes to
the network, so a heavy SELECT still sorting or aggregating keeps running, with its locks and grant,
on what may be a prod instance; cancelling the batch context first makes the driver send an attention,
as -timeout does today. Also, `deadline` is not in the 4.3 table, and the exit_code of such a profile
is undefined although profiles[].exit_code is "le code qu'aurait rendu l'appel simple". And -timeout 120
(the AGENTS.md example) under the default -deadline 100 turns every slow profile into deadline without
a word.

8. The CR rule closes a path the 18/11 trap did not take. TASKS03.md says "CRLF de Python dans les noms
de profils" in the shell loop, and Export-RegisteredServers slugifies every name (ConvertTo-RegisteredServerSlug),
so the 31 generated profiles could not carry a CR: the CR was in the argument the loop built. Marking
hand-written names with CR or edge spaces unusable is harmless, but "fermé à l'endroit où tous les
chemins passent" is not true; a `-profile` or `-profiles-match` argument ending in CR still gets
"unknown profile" or "aucun profil choisi". Either strip and refuse control characters in those two
arguments with a message that says so, or drop the claim.

9. The TLS prefix rule turns a deterministic failure into a retry. "TLS Handshake failed: cannot read
handshake packet: " is classed connect_transient, but an old instance that resets on Go's ClientHello
(TLS 1.2 minimum) produces the same text every time: 20 s plus one futile attempt per call, then the
agent's allowed relance. No authentication is involved, so the cost is time only. Also, a per-read
connectTimeoutSec expiry during TLS lands in this same flat string and is classed transient rather
than timeout; harmless, but the table should say so.

10. AGENTS.md says "there is never a second format to parse" and lists exit codes 0 to 4; 5.5 and 5.6
knowingly add a second shape and code 5, and 5.7 lists only the agent rules to carry over. Name those two
AGENTS.md sentences (and the skill's exit-code paragraph) in the change list, or the published contract
contradicts the binary.

## Not a problem

- disableretry exists in v1.11.0, is parsed from the URL form, and turns off checkBadConn's RetryableError.
- mssql.NewConnector uses the same no-process driver as sql.Open("sqlserver"): no placeholder rewriting.
- An EOF after the login packet arrives as *net.OpError{Read, io.EOF}: errors.Is(io.EOF) holds, 4.4's "Read: EOF" is right.
- Sequential resolution on one Resolver removes the loadStore race; a store read failure then fails every DPAPI profile consistently.
- The store-level stale warning is repeated in every DPAPI profile's warnings: noise, not a fault.
- Fingerprint of name and environment only: the user is shown nothing else, and writes are refused in multi mode.
- Generated profiles only get environment "prod" (Test-ProdGroup), so the exact match on "prod" is consistent.
- Watchdog firing just after a successful db.Conn: rare, and the implementer can test Stop()'s return.
- Resolver warnings with local paths in stdout: they already reached the transcript through stderr.
