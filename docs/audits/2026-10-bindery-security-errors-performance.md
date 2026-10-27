# Security, error response and performance audit, October 2026

This is the findings document for a three part review of Bindery: security, the shape and content of API error responses, and performance on a large library. Every finding was checked against a running binary or a failing test before anything was changed, then fixed in a narrow pull request. This page records what was found, what was fixed and where, what was refuted or turned out weaker than expected, and what was left alone on purpose.

Status text in this page follows one rule: "fixed in #N" names the pull request that carries the fix. All six fix pull requests are merged, #3185 and #3187 last, each after an independent review that added fixes. Where a review changed the outcome, the section says so.

## 1. Scope and method

**Commit audited.** The plan was written against `5e1a4c59` (2026-10-10, 27 commits before the harness baseline). The verification harness was built and its baseline captured at `5fcb780c` (2026-10-11, the v1.41.0 era), because main moved while the plan was being turned into a harness. Every finding was re-verified on the tree it was fixed against, and each fix pull request says which commit it was cut from. Where the two commits differ in a way that matters, the finding is described against `5fcb780c`.

**Passes.**

| Pass | What it did |
|-|-|
| Read through | The security paths (auth, OIDC, path containment, outbound HTTP, password hashing), the API error paths, and the hot database and HTTP paths |
| Verify on a running binary | A built binary on a scratch data directory, driven by a script that records status, content type and body for ten error cases and diffs them before and after each fix |
| Verify on a large database | A synthetic database, `EXPLAIN QUERY PLAN` and wall time per query, then HTTP load against the same data |
| Fail before, pass after | Every fix has a test that fails on the old code. Each guard was then broken on purpose on a compiling tree to check a test notices (section 6) |
| Independent review | Each fix pull request was reviewed by someone other than its author. The review found real defects, some introduced by the fixes, and they were fixed before merge |

**Synthetic database.** One seed, deterministic, no `ANALYZE` (like a real install):

| Table | Rows |
|-|-|
| authors | 2,000 |
| books | 20,000 (every tenth one wanted, so 2,000 wanted) |
| book_files | 30,000 |
| downloads | 50,000 (90 percent in a terminal state, spread over a year; 10 percent active) |
| history | 200,000 |
| logs | 100,000 |
| pending_releases | 2,000 (added so the `book_id` query had something to read) |

**Timing caveat.** Timings were taken on a six CPU development machine that other work shared the whole time. The one minute load average was 2 to 7 during most measurements and above 28 at times, and the script that waits for a quiet machine never reported quiet. Runs were interleaved before and after so noise hit both sides, and each table below gives the spread (minimum to maximum) next to a median where there is one. Large effects (a 400x change) are far outside that spread. Small ones (a few percent) are inside it and are labelled as such. Read ratios, not absolute milliseconds.

**Not covered.**

- A frontend XSS pass over the React app.
- The OPDS feed builder, beyond the N+1 noted in section 7.
- The Grimmory client, beyond its missing dial guard (section 3.3).
- Third party dependency review (handled by the existing CI scanners).
- Anything that needs a real indexer, download client, Calibre or Audiobookshelf instance. The harness uses fixtures.

## 2. Summary

Severity is this audit's own judgement of impact on a default install, not a CVSS score.

| Id | Theme | Severity | Status |
|-|-|-|-|
| S1 | File delete containment passed every path when no library root resolved | Low (unreachable on a default install) | Fixed in #3175 |
| S2 | Containment accepted a symlink that resolves outside every root; the review found two more bypasses | Medium | Fixed in #3175 |
| S3 | With an outbound proxy, the dial time address check was skipped for bypass listed hosts | Low | Fixed in #3175 |
| S4 | Download client and Audiobookshelf control clients followed redirects and dialled link local addresses | Low | Fixed in #3175 |
| S5 | Password change had no rate limit on the current password check | Medium | Fixed in #3175 |
| S6 | The public OIDC provider list returned discovery errors and group names | Low | Fixed in #3175 |
| S7 | `/auth/oidc/redirect-base` open to any user; forwarded values unvalidated | Low | Fixed in #3175, closes #1640 |
| S8 | Argon2 parameters in a stored hash were unbounded | Low | Fixed in #3175 |
| S9 | `file:` SQLite DSNs built by string concatenation | Low | Fixed in #3175 |
| E1 | Queue grab and retry relayed raw errors and chose 400 by substring | Medium | Fixed in #3174 |
| E2 | Metadata provider errors relayed with request URL and API key | Medium | Fixed in #3174 |
| E3 | Root folder and manual import echoed filesystem errors | Low | Fixed in #3174 |
| E4 | Connection tests relayed full upstream bodies | Medium | Fixed in #3174 |
| E5 | Database errors relayed as 400 text | Low | Fixed in #3174 |
| E6 | A store failure on a lookup answered 404 | Low | Fixed in #3176 |
| E7 | A handler panic answered an empty 500 with the stack on stderr only | Low | Fixed in #3176 |
| E8 | Unknown API paths and wrong methods answered text or an empty body | Low | Fixed in #3176 |
| E9 | Oversize bodies read as malformed (400, not 413) | Low | Fixed in #3176 for the swept handlers, rest tracked (section 4.5) |
| E10 | CSRF refusal labelled `text/plain`; OPDS 429 and 503 plain text | Low | Fixed in #3176 |
| E11 | Prowlarr test returned `ok` as a string | Low | Fixed in #3176 |
| P1 | `bookCTE` built every file of a format for every book query | High on large libraries | Fixed in #3185 |
| P2 | One autocommit per log line on the single connection | Medium | Fixed in #3185 |
| P3 | Four lookups walked whole tables | Medium for history, negligible for the other three | Fixed in #3185 |
| P4 | The queue listed and sent the whole downloads table, which never shrank | High on aged installs | Fixed in #3187 |
| P5 | The wanted list was one unpaged array | Low (bytes only) | Fixed in #3187 |
| P6 | Hashed build files had no cache lifetime | Low | Fixed in #3177 |
| P7 | A large download was cut at the 120 second server write timeout | High for audiobooks | Fixed in #3177 |
| P8 | Zips deflated audio that is already compressed | Medium | Fixed in #3177 |
| P9 | SABnzbd and NZBGet pollers did one lookup per item; startup recovery read the whole table per row | Medium | Fixed in #3187 |
| P10 | Metadata clients kept two idle connections per host | Low | Fixed in #3177 |
| P11 | Cache eviction scanned every entry under the write lock | Low until the cache is large | Fixed in #3185 |
| Auth reads | Auth does several database reads per request | n/a | Tracked in #1468, not changed |
| Metadata N+1 | Calibre author lookup and author add enrichment are N+1 per book | n/a | Tracked in #1469, not changed |
| Link local opt in | Opt in for link local indexer addresses | n/a | Tracked in #2036, not changed |
| Cross tenant existence | A cross tenant existence disclosure on a 409 | n/a | Tracked in a private security advisory |
| Accepted risks | See section 3.2 | Low | Accepted, documented only |
| Remaining error leaks | See section 4.4 | Low to medium | Reported, not fixed |

The pull requests and what each carries:

| PR | Title | Carries |
|-|-|-|
| #3174 | Stop raw database, filesystem and upstream text reaching clients | E1 to E5 |
| #3176 | One JSON error shape everywhere | E6 to E11 |
| #3175 | Security hardening, closes #1640 | S1 to S9 |
| #3185 | Book lookups, history indexes, batched log writes, bounded cache eviction | P1, P2, P3, P11 |
| #3177 | Asset caching, uncapped file downloads, stored zips | P6, P7, P8, P10 |
| #3187 | Trim finished downloads, page the wanted list, stop polling whole tables | P4, P5, P9 |

Security fixes shipped as plain public pull requests under `### Security` in the changelog, with no advisory, because none of them needed a coordinated disclosure.

## 3. Security

### 3.1 Findings S1 to S9

All nine were re-verified with a failing test before the fix. All landed in #3175.

**S1, zero roots fail open.** `LibraryRoots.Contains` returned true when no library root resolved. The callers are the book delete, per file delete and author delete handlers (the "delete files too" path). On a default install the situation cannot arise: `BINDERY_LIBRARY_DIR` defaults to `/books` and an empty value falls back to the default, so at least one root always exists. It is reachable only if the root folder lister fails and no static default exists. *Fix:* `Contains` returns false and logs one warning naming the cause. Manual import and adoption already refused with no roots and are untouched.

**S2, lexical symlink fallback.** When a path did not resolve to a location inside a root, `Contains` fell back to a lexical prefix check, which accepted a symlink inside a root that points outside every root. A missing file under a directory symlink that escapes the root was accepted as well. The review of the first fix found two further bypasses, fixed before merge:

- A stored path containing a `..` segment was checked after `Clean` folded it away, while the kernel resolves the symlink first, so `root/link/../x` was checked as `root/x`. Any `..` segment is now refused.
- A dangling symlink or a symlink loop makes `EvalSymlinks` fail the same way a missing name does, so the "deepest existing ancestor" walk appended the tail and accepted the path. An existing but unresolvable component now counts as not contained.

*Fix:* the path (deepest existing ancestor plus the missing tail) and each root are resolved with `EvalSymlinks` and compared resolved to resolved. There is no lexical fallback. A root that is itself a symlink to another volume now works for both the link path and the real path (the real path was refused before).

**S3, proxy bypass skipped the dial check.** With `BINDERY_OUTBOUND_PROXY` set, a destination on the bypass list was dialled by a transport with no dial time policy check. Confirmed against a loopback target (request succeeded), and against `169.254.169.254` (a real connect was attempted). *Fix:* `GuardedTransport` always installs the guarded dial. Only the dial to the proxy's own host and port is exempt. The image proxy, notifier, Prowlarr client and OIDC discovery now share `GuardedTransport` instead of four near copies.

**S4, control clients.** The SABnzbd, NZBGet, qBittorrent, Deluge and Audiobookshelf clients followed a 302 to `http://169.254.169.254/` (the dial then failed or timed out rather than being refused), and a configured link local host got a connect attempt. Transmission and rTorrent already refused any redirect by pinning the exact RPC target, so only their dial guard is new. *Fix:* `httpsec.DirectGuardedTransport(PolicyLANLoopback)` plus `httpsec.RedirectGuard` (ten hops, each hop validated). The policy is `PolicyLANLoopback`, so loopback, RFC1918, ULA and container hostnames still work. These clients stay off `BINDERY_OUTBOUND_PROXY` as documented.

**S5, password change throttle.** Six wrong current passwords produced six 401 responses. *Fix:* the login limiter keyed on the account, acquired before the key derivation, released if the client went away while queued, reset on success. Lockout after six wrong attempts per window, same as login.

**S6, public provider list.** The unauthenticated OIDC provider list included `allowed_groups`, `last_attempt` and `status.last_error`, whose text included the resolver address. *Fix:* non admins get id, name, issuer, client id, scopes and `status.state`. Admins (session, API key, or an auth mode that grants admin) keep the full view. The login page needs only the reduced fields.

**S7, redirect base (#1640).** `/auth/oidc/redirect-base` answered 200 to a plain user, and forwarded values were taken as given (a `javascript` scheme and a host containing a path were accepted). *Fix:* the route is under `RequireAdmin` (its only consumer is the admin OIDC settings screen). `X-Forwarded-Proto` must be `http` or `https`, `X-Forwarded-Host` must be a DNS name, IPv4 or bracketed IPv6 with an optional port, otherwise it is ignored and the request host is used. A comma separated list is therefore ignored too; a proxy chain that appends should send one value or set `BINDERY_OIDC_REDIRECT_BASE_URL`.

**S8, argon2 parameters.** The memory, time and parallelism values read from a stored hash were unbounded: a hash with a 4 GiB memory parameter would make a login attempt allocate it, and `t=0` or `p=0` panics inside argon2 (caught by the recoverer, so a 500, not a crash). *Fix:* checked before a key derivation slot is taken: memory 1 to 262,144 KiB (256 MiB), time 1 to 10, parallelism 1 to 8. Bindery's own constants (65,536 KiB, 1, 4) are inside the range and have not changed since the first commit.

**S9, DSN concatenation.** The Calibre reader and the Readarr importer built `file:` DSNs by string concatenation, so a directory name containing URI syntax changed how SQLite opened the file (it created a stray empty file, and read a different one). *Fix:* the DSN is built with `net/url` through a shared `db.SQLiteFileURI`, which the restore code already used privately.

**Behaviour changes a user can notice** (all in the #3175 description and the changelog):

- Deleting a book or author with files skips files under a symlinked directory that points outside the library roots. A warning is logged, the database row is still removed, and the single file delete answers 409 for a skipped file. Bindery never creates library symlinks itself.
- A download client saved on a link local address now fails its test and calls with `points to link-local address`. Saving one was already refused, so only rows from an older version are affected.
- With an outbound proxy, a bypass listed destination that the feature's policy blocks (loopback for a cover image or webhook) is now refused instead of fetched.
- Non admin API consumers of the provider list no longer see `allowed_groups` or `last_error`.

### 3.2 Accepted risks and document only items

These were verified and deliberately not changed. Each is a conscious trade off or sits behind a trust boundary that is already crossed.

| Item | Verified evidence | Why accepted |
|-|-|-|
| `calibre.binary_path` is executed | The settings handler checks only that the path is an existing regular executable file. `internal/calibre/client.go` runs it with `exec.CommandContext`, no shell. Setting it needs admin | An admin can already add a download client, a root folder and a library path, so choosing an executable is the same trust level as the container |
| A root folder may be `/` | `RootFolderHandler.Create` requires only an existing directory. With `/` as a root, `LibraryRoots.Contains("/etc/passwd")` is true, so the S1 and S2 containment gate is defeated by that one admin choice | Admin only route. Rejecting `/` (and perhaps a home or data directory) is a possible follow up |
| OPDS Basic and session auth set a user id without a role | `opds_auth.go` calls `auth.WithUserID` and not `WithUserRole`. Role is enforced separately by `opdsRoleAllowed`, which refuses requesters. No `/opds` handler reads the role or uses `RequireAdmin` | No impact today. It is a trap only if an admin gated handler is ever mounted under `/opds`, which would see an empty role and refuse |
| `True-Client-IP` is not stripped by the proxy middleware | `realip.TrustedRealIP` reads it only when the TCP peer is a trusted proxy and the `X-Forwarded-For` walk produced nothing. From an untrusted peer the middleware is skipped, and nothing else in the repository reads that header | Not spoofable from an untrusted peer. Residual: a trusted proxy that forwards a client supplied `True-Client-IP` without setting `X-Forwarded-For` would be believed |
| OIDC state and nonce use a plain `!=` | State is compared with the value in the signed flow cookie, nonce with the claim of a signature verified ID token | Not a usable timing oracle: the attacker does not hold the compared value and state is single use per flow. `subtle.ConstantTimeCompare` would be hygiene only |
| CGNAT `100.64.0.0/10` and other special ranges are allowed under `PolicyStrict` | `ValidateIP(100.64.0.1, PolicyStrict)` returns nil. `checkIP` blocks the unspecified address, loopback, link local, the cloud metadata addresses, RFC1918 and ULA, and nothing else, so multicast and other special use ranges pass too | Not a cloud metadata address, but some providers use addresses inside CGNAT for internal services (one cloud puts its metadata service at `100.100.100.200`). Worth adding to the strict blocklist later |
| Login CSRF through a `text/plain` form | `POST /auth/login` is exempt from the CSRF checks and decodes JSON whatever the content type. A test with `Content-Type: text/plain` and a JSON shaped body returned 200 with a `bindery_session` cookie (`HttpOnly; SameSite=Lax`) | Needs the attacker's own valid credentials, and the effect is signing the victim into the attacker's account. `SameSite=Lax` does not stop a top level cross site POST from setting the cookie. Requiring `Content-Type: application/json` on login would close it. Low severity |
| A symlinked author folder is not followed on delete | After S2, `/books/Author` pointing at `/mnt/disk2/Author` resolves outside the roots, so a delete with files skips it | The maintainer chose to fail closed. Users who link author folders to another volume must add the target (`/mnt/disk2/Author`, or its parent) as a root folder. An opt in "linked folder" setting is a listed follow up |

**Residual SSRF items from the review of #3175** (reported, not changed):

- A 307 or 308 redirect makes Go's HTTP client replay a replayable request body at the new location. For a control client that is the login request. `RedirectGuard` refuses forbidden targets, not cross host ones, so a redirect to another host that passes the policy would receive the body. Transmission and rTorrent pin the exact target and are not affected.
- `RedirectGuard` validates each hop against the policy and does not pin it to the configured host. Under `PolicyLANLoopback` that is still any loopback, RFC1918 or ULA address.
- A destination whose host and port equal the proxy's own host and port is exempt from the dial check (`isProxyAddr` compares those two values only). Under a strict policy this lets a feature be pointed at the proxy address itself.
- A hostname that resolves to a link local AAAA record beside an ordinary A record is refused whole, by design (`NewDialContext` validates every resolved address and refuses the dial if any is forbidden). The cost is a rare false refusal on a dual stack name, in exchange for not trying only the allowed address.

### 3.3 Clients without a dial guard

These outbound clients do not use `GuardedTransport` or `DirectGuardedTransport`. All of them take an admin configured URL or a fixed provider endpoint. Listed so a later change can decide per client.

| Client | Today |
|-|-|
| Calibre plugin client (`internal/calibre/plugin_client.go`) | Plain `http.Client` with a timeout, URL from the admin's Calibre settings |
| Grimmory client (`internal/grimmory/client.go`) | Own `http.Transport` with a 5 second dial timeout only, URL from admin settings |
| Calibre cover fetch client (`internal/calibre/metadata.go`) | Validates the first URL and each redirect against `PolicyStrict`, but has no dial time check, so a hostname that resolves to a forbidden address at connect time is not refused |
| OPDS `baseURL` (`internal/api/opds.go`) | Builds feed links from `X-Forwarded-Proto` and `X-Forwarded-Host` without the validation S7 added for the OIDC base. Untrusted peers have those headers stripped, so only a trusted proxy can set them |
| Metadata providers (Open Library, Hardcover, Google Books and others) | Fixed public endpoints, never dial guarded. `MetadataTransport()` (P10) adds no guard and removes none |

### 3.4 Tracked elsewhere

Cross referenced only, not touched by this audit:

- #1468, authentication does several database reads per request.
- #1469, the Calibre author lookup and author add enrichment are N+1 per book.
- #2036, opt in for link local indexer addresses.
- A cross tenant existence disclosure on a 409 response is tracked in a private security advisory.

## 4. Error responses

### 4.1 The contract

The audit's reference for the error format is the new "Error format" section in [API.md](../API.md). In short:

| Rule | Detail |
|-|-|
| Shape | Every error from `/api` and `/api/v1` is JSON: `{"error": "text"}`, safe to show to a user |
| Source of text | Bindery's own messages (validation, not found) are specific. Anything that came from the database, the filesystem or another service becomes a fixed message and is logged server side |
| 404 | An unknown route, or a missing (or invisible) row. Never a store failure |
| 405 | JSON, with the `Allow` header kept |
| 413 | An oversize JSON body (limit 1 MiB globally). Used to be a 400. Only handlers moved onto `decodeJSON` answer it (section 4.5) |
| 500 | `{"error": "internal server error"}`. A handler panic also carries `request_id`, which matches the log entry in Settings, Logs |
| 502 | Another service failed. Generic text, except on the connection test endpoints, which relay the redacted first line (at most 200 characters) |
| 200 with an error inside | The indexer test, Prowlarr test and OIDC discovery test report `{"ok": false, "error": "..."}`. Download client tests answer 502 |
| Exceptions | The Calibre bridge adds a machine readable `code`; OPDS feeds and file downloads speak their own protocol; the image proxy errors are plain text |

New helpers in `internal/api/errors.go` (#3174, extended in #3176): `writeNotFoundOrServerError`, `decodeJSON` with a shared oversize body mapping, `writeUpstreamDetail`, and sentinel errors that map to 400, 404 and 503 instead of substring matching. `internal/api/recoverer.go` (#3176) replaced chi's recoverer.

### 4.2 Findings E1 to E11

| Id | What was wrong | Fix | PR |
|-|-|-|-|
| E1 | Grab and retry answered 502 with the raw error (for example a download client message carrying an API key, or `sql: database is closed`), and chose 400 versus 502 by searching the message for "no enabled" | Sentinels: no client is 400 with `"code":"no_download_client"`, a send failure is a generic 502, anything else is a 500. The bulk retry and delete per row messages got the same treatment. The web client now classifies on status and code | #3174 |
| E2 | Provider failures on relink, rebind, enrich, add book and adoption relayed the request URL, with API key and resolver address | `writeUpstreamError`, generic 502 text | #3174 |
| E3 | Root folder create and manual import echoed `os.Stat` text (paths) | Fixed text per cause, error logged | #3174 |
| E4 | Download client and Prowlarr tests relayed the whole upstream body, including later lines (HTML, stack traces) | First line only, redacted, 200 character cap, connection failures are 502 not 400, refused or timed out connections get Bindery's own hint | #3174 |
| E5 | User role change, Fix Match preview, CSV import and Audiobookshelf start and rollback relayed database text as 400 | Sentinels map to 400, 404 or 503, everything else is a 500 | #3174 |
| E6 | A store failure on a lookup answered 404 | See section 4.3 | #3176 |
| E7 | A panic answered an empty 500 and the stack went to stderr only | `api.Recoverer`: logs panic, request id and stack through `slog` (so it reaches the in app log), answers JSON with `request_id`, re panics `http.ErrAbortHandler`, leaves a started response alone, ignores upgrade requests | #3176 |
| E8 | `GET /api/v1/nope` answered text `404 page not found`, `POST /api/v1/health` an empty 405 | JSON 404 and 405 installed on both API sub routers, `Allow` preserved | #3176 |
| E9 | An oversize body answered 400 | `decodeJSON` (and the `io.ReadAll` sites) answer 413 in the swept handlers. A wrong comment in `migrate.go` was fixed: `MaxBytesReader` does not write the 413 itself | #3176 |
| E10 | The CSRF refusal was JSON labelled `text/plain` (`http.Error` resets the type), and OPDS Basic 429 and 503 were plain text | Direct header and body writes, JSON for OPDS | #3176 |
| E11 | Prowlarr test returned `"ok":"true"` as a string | Boolean. The web client, types and tests changed with it | #3176 |

**Behaviour changes visible to API clients** are tabulated in the #3174 and #3176 descriptions and in `docs/API.md`. The notable ones: oversize body 400 to 413, store failure on a lookup 404 to 500, download client probe failure 400 to 502, provider failure text now fixed, Prowlarr `ok` string to boolean, JSON 404 and 405 where there was text or nothing.

### 4.3 The 404 versus 500 sweep (E6)

The plan listed about 50 grep matches. On inspection 24 handler sites were real "error means 404" bugs, plus the manual import book lookup. Each now answers 500 for a store error and keeps its own 404 or 400 text for a missing row. A test per handler group runs against an open store (missing id, own message) and a closed one (500).

| File group | Handlers |
|-|-|
| Profiles and formats | `delay_profiles.go`, `metadata_profiles.go`, `custom_formats.go`, `import_lists.go`, `notifications.go` |
| Download clients | `download_clients.go`, `download_client_diagnose.go` |
| Indexers | `indexers.go` |
| Releases and files | `pending.go`, `history.go`, `files.go`, `genre_apply.go`, `books.go` (four reload sites after a file delete) |
| Manual import | `manual_import.go` (book lookup in match download and validate import) |

Sites that matched the grep and were deliberately not changed: places that continue past the error, places that already answered 500, profile lookups that swallow the error and fall back to a default, bulk handlers that report per item, and handlers with a specific message of their own. The four `books.go` reload sites are converted but cannot be exercised with a closed store, because the first lookup fails earlier; they are covered by review only.

### 4.4 Review changes, deviation, and what remains

**Usability restored by the review of #3174.** A fixed message is only safe if it still says what is wrong, and a few lost that. The review put back one message per cause for root folder create (missing, not readable, a parent that is a file), a network failure naming the client and the `host:port` the operator configured (with the hint) on the download client test, Bindery's own hint on a refused or unreachable client at grab and bulk retry, the provider pause text (daily quota, rejected token) instead of "metadata provider unavailable", a 404 for a role change to admin on an unknown user, and a fallback sentence so a connection test never shows a blank reason.

**Deviation from the plan: `Redact: true` everywhere.** The plan set `providerhttp.Request.Redact` for every provider. That flattens the transport error and drops its chain, while `metadata.IsProviderUnavailable` and the discovery failure breaker rely on `errors.As` to `*url.Error` and `net.Error`. Setting it everywhere would stop network failures counting as provider outages. Instead `providerhttp.transportErr` always calls `httpsec.RedactURLError`, which scrubs the URL inside the error in place and keeps the chain. The response layer no longer relays provider text at all, so the scrub matters for logs.

**Free text redaction gap, fixed.** `RedactSecrets` only redacted URL shaped parameters (after `?`, `&` or `;`). The harness case for a connection test whose upstream body said `apikey=... token=... password=...` still relayed those secrets. The #3174 review made `upstreamDetail` also redact header and field shaped credentials, and made `RedactSecrets` cover `#token=` fragments. Tests: `TestUpstreamDetail_FreeTextCredentials`, `TestUpstreamDetail_CapNeverSplitsARune`.

**Remaining leaks, reported but not fixed.**

| Item | Detail |
|-|-|
| `downloads.error_message` (top item) | The grab path stores the raw download client error with `SetError` (`internal/api/queue.go`). The queue API returns that text to every user who can see their downloads, unredacted. The same raw text goes into the `download_failed` history row and the notification payload. Redacting on output (and on write) is the first follow up |
| Refresh all authors status | `GET /api/v1/authors/refresh-all/status` serves the stored job status verbatim, and the job stores `"failed to list authors: " + err.Error()` (database error text). The route has no `RequireAdmin` group |
| Admin only relays | Grimmory test and sync, import list handlers, the Audiobookshelf `lanTimeoutHint` and `writeProbeError`, and a few similar sites return `err.Error()` to an admin. Lower risk (admin sees their own configuration) but not redacted |
| Logs | The in app log and stdout are not redacted for free text secrets. URL shaped secrets are scrubbed on the provider path; free text is not |
| `"error": err.Error()` | 85 call sites remain in `internal/api` (counted at `462609ff`). Many relay Bindery written validation text, which is intended; they were not triaged one by one |

### 4.5 Gaps in the new contract

- **413 is partial.** `decodeJSON` is used at 25 call sites in 12 handler files (the swept groups plus the queue, books, indexers and migrate handlers). 45 handler sites still call `json.NewDecoder(r.Body)` directly, in 23 files. For those, an oversize body still reads as malformed. Creating an author is the visible case: `POST /api/v1/author` answers `400 invalid request body` for a 2 MiB body. The docs say so.
- **Metrics skip panicked requests.** The metrics middleware records after `next.ServeHTTP` returns, with no `defer`, and it sits below the recoverer. A request that panics unwinds through it before the recoverer runs, so it is never counted.
- **The logged stack is uncapped.** `Recoverer` logs `debug.Stack()` in full. A deep stack makes a large log entry.
- **`writeServerError` has no `request_id`.** Only the panic 500 carries one.

## 5. Performance

All numbers are from the seeded database in section 1 unless stated. The numbers quoted here were taken from the build notes and the pull request descriptions, with the same noise caveat.

### 5.1 P1, the `bookCTE` query (#3185)

**Confirmed, but not as literally planned.** `EXPLAIN QUERY PLAN` shows no `SCAN book_files` line, because the format index is used. It does show both CTEs `MATERIALIZE`d over every row of their format and joined through an automatic index, which is a whole table build to return one row. For `GetByID` the plan before was `MATERIALIZE first_ebook`, `MATERIALIZE first_audiobook`, `SEARCH books USING INTEGER PRIMARY KEY`, `SCAN fe`, `SEARCH fa USING AUTOMATIC COVERING INDEX`. The list pages carry the same two `MATERIALIZE` lines. After the fix the plan is `SEARCH bf USING INDEX idx_book_files_format_book (format=? AND book_id=?)` twice and nothing else.

*Fix:* the file path columns use `COALESCE(NULLIF(books.ebook_file_path,''), (SELECT bf.path ... WHERE bf.book_id=books.id ORDER BY bf.id LIMIT 1), '')`. The stored column still wins and the subquery runs only for books with an empty stored path. `ORDER BY id LIMIT 1` returns the same row as the old `GROUP BY ... HAVING id = MIN(id)`. `bookCTE` stays as an empty constant so call sites compile.

Query time in milliseconds, median of three runs (minimum to maximum):

| Query | Before | After |
|-|-|-|
| `GetByID` | 54.16 (52.71 to 56.67) | 0.13 (0.12 to 0.17) |
| Books list page 1 | 66.48 (61.99 to 68.59) | 0.68 (0.62 to 1.17) |
| Books list, offset 10,000 | 103.56 (100.00 to 106.79) | 26.25 (23.16 to 31.30) |
| Wanted list | 97.97 (84.89 to 111.81) | 20.42 (20.08 to 23.19) |
| Book count | 2.91 | 2.94 (unchanged, needs no CTE) |

HTTP, 8 concurrent clients, medians of three interleaved runs with the spread in brackets:

| Endpoint | Before p50 / p95 ms | After p50 / p95 ms |
|-|-|-|
| `/api/v1/wanted/missing`, 500 requests | 991 (814 to 1122) / 1736 (1356 to 1901) | 326 (307 to 408) / 573 (490 to 729) |
| `/api/v1/queue`, 100 requests, 30 MB body | 9418 (7864 to 11436) / 18566 (12307 to 19397) | 6806 (6780 to 8909) / 12041 (11387 to 16856) |

The two queue ranges overlap, and the queue cost is dominated by 50,000 download rows and 30 MB of JSON (P4), so no queue win is claimed for P1.

**Review of #3185: equivalence and the worst case.** The rewrite changes SQL that feeds every book screen, so the review added a permanent differential test, `TestBookQueries_EquivalentToCTEShape` (`internal/db/books_equivalence_test.go`), which runs the old CTE shape and the new subquery shape side by side. Over 217 books by 38 columns (8,246 raw cells) and 15,945 result rows across 12,480 filtered pages, there were zero differences. Listing all 20,000 books went from 283 ms to 185 ms, and no query shape got slower. That includes the worst case for the new form, every stored path empty so every row runs the subqueries, which went from about 1,046 ms to 233 ms for list all.

### 5.2 P2, log write batching (#3185)

**Confirmed as a design change. The benefit is shown by a microbenchmark, not by HTTP load.** The log writer committed one autocommit per record on the single database connection. It now collects a batch (up to 100 records, or 250 ms after the first record), then writes it in one transaction with a prepared statement, without holding a connection while the batch fills. A full 512 slot channel still drops records rather than block the caller. A failed batch is retried row by row, and the first failure of a run is printed once on stderr (not through `slog`, which would feed back into the logger).

`BenchmarkLogWrite`, file database with production pragmas, nanoseconds per record, three runs:

| Mode | Run 1 | Run 2 | Run 3 |
|-|-|-|-|
| Autocommit per row | 107,609 | 110,836 | 108,772 |
| Batched, 100 per transaction | 13,438 | 13,940 | 13,698 |

That is about 8x less connection time per record. The HTTP flood (2,000 debug lines through the writer) gave rows per second during the run of 1336, 1624, 1946 before and 1929, 1962, 3494 after, with request p50 of 4.36, 3.82, 3.10 ms before and 3.36, 2.98, 2.09 ms after, and no rows dropped in any run. The flood is limited by the request rate, not the writer (the channel never filled), so it shows no regression and a small plausible gain, not a measured win.

**Trade off, stated plainly.** A crash or power loss can lose up to 250 ms of the newest rows from the `logs` table. A normal shutdown flushes the batch in hand. stdout and the in memory ring buffer are unaffected.

### 5.3 P3, migration 107 (#3185)

Four indexes in `107_perf_indexes.sql`. Forward only.

| Index | Query it serves | Plan before to after | Median ms before to after |
|-|-|-|-|
| `idx_history_event_type_created_at` on `(event_type, created_at DESC, id DESC)` | History page and count by event type | `SCAN history USING INDEX idx_history_created_at_desc` to `SEARCH ... (event_type=?)`, count `SCAN history` to a covering index | Rare event type page 214 to 0.02, simple 227 to 0.02, count 24.9 to 1.7, common event type page 0.30 to 0.12 |
| `idx_pending_releases_book_id` | `WHERE book_id=?` | `SCAN` to `SEARCH` | 0.16 to 0.04 (2,000 rows) |
| `idx_downloads_added_at` | `ORDER BY added_at DESC` | `SCAN` plus `USE TEMP B-TREE FOR ORDER BY` to `SCAN USING INDEX` | 295 (279 to 346) to 261 (245 to 269), about 11 percent, inside the machine's noise |
| `idx_authors_quality_profile_id` | Counts of authors using a profile | `SCAN authors` to a covering `SEARCH` | 0.20 to 0.15 (2,000 rows, negligible) |

**Deviation from the plan.** The plan asked for `(event_type, created_at DESC)`. The page query orders by `created_at DESC, id DESC`, and with two columns SQLite still sorts every row of the event type in a temp b-tree, so the index carries `id DESC` as a third column. A test fails without it.

**Startup cost.** Creating the indexes on the seeded database (200,000 history rows, 50,000 downloads) took 535, 488 and 704 ms from log gap preflight to database ready, about 0.5 s median, against 4, 15 and 4 ms for a start with nothing to apply. The one minute load average was about 7.

**Review of #3185: startup cost, re-measured.** On a database of 20,000 books, 200,000 history rows and 50,000 downloads, migration 107 took about 0.43 s. The backfill that came in with #3158 added about 0.23 s on top, so about 0.66 s to database ready, against 4 ms for the baseline. Extrapolated, a history table of a million rows would cost roughly 2 to 3 s once, on the first start after the upgrade.

**Weaker than the plan said (P3, partly).** Only the history index is a large win. The pending releases and authors tables are tiny (2,000 rows each), so their gain is negligible, and the downloads index is a small gain inside the noise. They were kept because the plan shows the scan or sort gone and they help any later `LIMIT` query.

### 5.4 P4, the queue and download retention (#3187)

**Confirmed.** `enrichedQueueItems` listed the whole downloads table, 50,000 rows and 30.0 MB, and nothing ever deleted a row.

*Fix, view:* `DownloadRepo.ListActive` lists everything except an `imported` row older than 7 days (age is `imported_at`, then `completed_at`, then `added_at`). The queue hides only those imported rows. Every other state, including `failed`, stays listed however old. The Sonarr style `/api/queue` already excluded imported rows and is unchanged.

*Fix, retention:* a scheduler job `download-trim` (every 6 hours, beside `log-trim`) deletes `imported` and `failed` rows older than `BINDERY_DOWNLOAD_RETENTION_DAYS` (default 90, `0` disables the job). Age is `COALESCE(imported_at, dead_at, completed_at, grabbed_at, added_at)`; the review added `TestTrimTerminal_AgeIsWhenTheRowFinished` because the builder's fixtures set every stamp to the same value and could not tell the fallbacks apart. The first pass runs about 6 hours after the upgraded process starts and removes every eligible row in one go, which on an install that has run for a year is most of the table. Setting `BINDERY_DOWNLOAD_RETENTION_DAYS=0` before the first start keeps the rows. The changelog and `docs/DEPLOYMENT.md` say so. It never deletes a live state, `importFailed` or `importBlocked`, and keeps a row while a surviving row names the same torrent or usenet job (the shared removal guard from #3051). Batches of 500 with a 250 ms pause, one short statement each. On the seeded database 30,859 rows were removed in 16.2 s wall clock (62 batches, almost all of it the pauses), a concurrent query on the single connection waited at most 62 ms on a loaded machine, and a second pass removed 0 rows in 5.5 ms. History rows, the blocklist and every book are untouched, and nothing references `downloads` by foreign key.

| Measure | Before | After |
|-|-|-|
| `/api/v1/queue` response, bytes | 29,966,675 | 9,192,702 |
| `/api/v1/queue` p50, c=8, n=60, three runs (ms) | 9423, 11684, 9482 (median 9482) | 4976, 4056, 2928 (median 4056) |
| `/api/v1/queue` single request, quieter moment | 1.64 to 1.85 s | 0.64 to 0.68 s |
| Seeded table after trimming | 50,000 rows | 19,141 rows |

Read plainly: about 2.3x faster at p50 under load and 3.3x fewer bytes. It stays at 9.2 MB because the seed makes 30 percent of the rows `failed` or `importFailed` (all kept) and 10 percent active, far more than a real install has. Imported and failed rows are 82 percent of the seeded table. Only those older than 90 days were removed, which is 61.7 percent (30,859 of 50,000).

**The trade off, stated plainly.** `downloads.guid` is unique, and an `imported` row whose book still exists is what makes a grab of that release answer "already imported". Once the row is trimmed, Bindery no longer remembers that exact release. If the book becomes wanted again (say its file was deleted) and that same release is picked, it can be grabbed again. Scheduler search only searches wanted books, so there is no upgrade loop. If the refusal should be kept, the alternative is to trim only failed rows and imports whose book is gone, which trims about 12 percent of the seeded table instead of 82 percent. `BINDERY_DOWNLOAD_RETENTION_DAYS=0` restores the old behaviour.

**Review of #3187.** The review found no data loss and no re-grab defect. Nothing references `downloads` by foreign key, and telemetry reads history, not downloads. The only consumers of guid uniqueness are the manual grab (409) and the scheduler auto grab. Before the change, a book whose row was old enough to trim and that became wanted again was stuck for ever, because auto grab returned at the best result without trying another release. After the change it re-grabs once, and the import stamps a fresh `imported_at`, so the new row is protected for another 90 days. There is no loop.

### 5.5 P5, the wanted list (#3187)

**Confirmed for bytes, refuted for server time.** `GET /api/v1/wanted/missing` with no `limit` is unchanged (a bare array of every wanted book). With `limit` it answers `{items,total,limit,offset}` (a `limit` of 0, a negative value or a non numeric one falls back to 100, anything above 500 is capped at 500, and an `offset` past the end returns empty `items` with the real `total`), and the Wanted page now pages and searches on the server. The count query skips the `book_files` CTEs when there is no search.

| Measure | Before | After |
|-|-|-|
| Response bytes, 2,000 wanted books | 2,292,450 (bare array) | 57,092 (`?limit=50`) |
| p50, bare, c=8, n=300 (ms) | 1290, 909, 807 (median 909) | 971, 1007, 744 (median 971) |
| p50, `?limit=50` (ms) | not available (the old binary ignores `limit`) | 1498, 1506, 1065 (median 1498) |
| Single request probe | 0.13 to 0.17 s | 0.14 to 0.21 s for `?limit=50` |

About 40x fewer bytes, but **server time per request did not improve**, because the page query still built the `book_files` CTEs (P1). No latency win is claimed for P5. These numbers were taken before P1 landed; the paged form on a tree that has both #3185 and #3187 was not re-measured.

### 5.6 P6, immutable asset caching (#3177)

**Confirmed.** All 90 files under `assets/` in a Vite build (88 JavaScript chunks including the lazy locale chunks, one CSS file, one PNG) are named `name-<8 character hash>.ext`, and the baseline response had no `Cache-Control`. Now hashed files under `assets/` get `public, max-age=31536000, immutable`. `index.html` and the stale asset 404 stay `no-store`, and unhashed files from `web/public` (favicon, manifest, `theme-bootstrap.js`) are untouched. The handler sees the path with any URL base prefix stripped, so a deployment under `/bindery` works (tested for both).

### 5.7 P7, the 120 second download cut (#3177)

**Confirmed.** A 2 GiB sparse file served at 10 MB/s was cut by the server `WriteTimeout`: curl exit 18 with 1,259,639,496 of 2,147,483,648 bytes after 120.13 s. The timeout bounds the whole response, and the metrics wrapper had no `Unwrap`, so a `ResponseController` could not reach the server's writer.

*Fix:* `statusRecorder` gains `Unwrap` and `Flush`. File, zip and Calibre bridge file responses use a rolling write deadline: every write pushes the deadline to now plus 5 minutes (`downloadStallTimeout`, updated about once a second). A client that keeps reading is never cut off, and one that stops reading is dropped after 5 minutes rather than holding a connection and a goroutine for ever. The compressor skips `/api/v1/book/{id}/file` and `/opds/book/{id}/file` (it already skipped audio, epub and zip by content type, so the skip matters for `text/plain` books with a Range request and removes the wrapper).

| Measure | Before | After |
|-|-|-|
| 2 GiB at 10 MB/s | truncated at 120.1 s, 1,259,639,496 bytes, curl exit 18 | complete, 2,147,483,648 bytes in 204.79 s, curl exit 0 |

**Note on the after number.** The 204.79 s run was taken on the first version of the fix, which cleared the deadline. The review changed that to the rolling deadline, because clearing it let a stalled client hold the connection for ever. The merged form is covered by an end to end test through the real middleware stack against a server with a one second `WriteTimeout` (`TestDownloadOutlastsServerWriteTimeout`), and by tests for a stalled client being dropped and a steady one outlasting the stall timeout. The slow run was not repeated on the final code.

### 5.8 P8, stored zips (#3177)

**Confirmed.** Zip downloads deflated mp3, m4b, epub and other already compressed files. Eight 100 MiB random "mp3" files plus a text file, streamed to a discard writer, three rounds:

| | CPU time per run | Zip size |
|-|-|-|
| Deflate (before) | 13.57, 13.70, 13.75 s (wall 13.54, 13.66, 13.71 s) | 839,118,130 bytes |
| Store (after) | 0.13, 0.13, 0.12 s (wall 0.13, 0.12, 0.12 s) | 838,861,838 bytes |

`zipMethod` chooses by extension: store for mp3, m4b, m4a, aac, flac, ogg, opus, wma, epub, jpg, jpeg, png, gif, webp and zip; deflate for pdf, txt, cue, nfo and anything unknown. The machine was shared, but the gap is two orders of magnitude.

### 5.9 P9, pollers (#3187)

**Confirmed.** The SABnzbd and NZBGet pollers did one `WHERE sabnzbd_nzo_id=?` select per history item. A counting database driver in the test shows 200 slots running 200 selects before and 1 (`ListByNzoIDs`) now. Two further sites turned up on the same path:

- `blockStaleImportFailures` listed the whole downloads table at the end of every poll, per client. It now reads only the two states it acts on (`ListByStatuses(downloading, importFailed)`): one whole table read per poll before, 0 after.
- Startup recovery of interrupted imports (`RecoverInterruptedImports`) listed the whole table once for each recovered row (`downloadByID`). With 25 wedged rows the counting test shows 25 whole table reads before and 0 after (now `GetByID`). The review of #3187 also timed startup with 50 wedged rows on the seeded database: 20.0 s to a healthy API on the baseline, 0.3 s after the fix (0.2 s with none wedged).

**Not changed.** The Transmission, qBittorrent, Deluge and rTorrent pollers still list the whole table once per client per 15 second tick, because they build the set of claimed torrent hashes from every row (finished ones too) to stop legacy reconciliation claiming a hash a finished row already owns. Narrowing that changes behaviour, and P4 retention shrinks the table instead. Follow up (section 7).

### 5.10 P10, idle connections (#3177)

**Confirmed.** Two bursts of 8 parallel requests, held until all were in flight: the default client dialled 14 connections, the metadata transport 8. `httpsec.MetadataTransport()` is a clone of `DefaultProxyTransport()` with `MaxIdleConnsPerHost` 16, used by the metadata provider packages. The review made the clone shared per base transport, because some clients (Hardcover) are built per request, and a clone per call would give each its own idle pool that was never reused. A new base (an outbound proxy being installed) gets a new clone. The clone adds no dial guard and removes none; the guarded transports are not touched.

### 5.11 P11, TTL cache eviction (#3185)

**Confirmed.** `evictEarliestLocked` ranged over every entry under the write lock on each overflowing set. It now inspects at most 32 entries (Go's map iteration starts at a random bucket) and evicts the earliest expiry among them. Caches of 32 entries or fewer are still exact. Eviction is now approximate for larger caches: the victim is the earliest expiring of a random sample, not the global earliest. With a single TTL the old exact earliest expiry was effectively the oldest written entry (first in, first out), so the practical change is that a larger cache drops an old entry rather than the oldest one. The size cap, TTL expiry and the no self eviction rule are unchanged.

`BenchmarkTTLCache_SetOverflow`, one overflowing set (the "before" is the same code with the sample forced to the whole map):

| Entries | Before | After |
|-|-|-|
| 10,000 | 140,474 ns | 811 ns |
| 100,000 | 1,322,825 ns (1.32 ms) | 823 ns (0.8 microseconds) |

Hold time no longer grows with the cache.

### 5.12 Refuted, or weaker than the plan said

| Finding | What the evidence showed |
|-|-|
| P1 | Real, but the plan's expected `SCAN book_files` line does not appear. The cost is the full `MATERIALIZE` per query |
| P2 | Shown by microbenchmark only. The HTTP flood is request limited, and 2,000 lines per second did not lose rows before the change either |
| P3 | Partly. Only the history index is a large win. Pending releases and authors are tiny tables, and the downloads index is inside the noise |
| P4 | The queue p50 gain is real but modest on this seed, and the response stays at 9.2 MB because the seed is far more failure heavy than a real install |
| P5 | Bytes yes, server time no, until P1 lands |
| E2 as planned | `Redact: true` everywhere would have broken provider outage detection (section 4.4) |
| S1 | Confirmed but unreachable on a default install |
| E6 | 24 real sites, not the 50 the plan counted |

### 5.13 Tracked separately

#1468 (authentication does several database reads per request) and #1469 (the metadata N+1 on Calibre author lookup and author add enrichment) are not changed by this audit.

## 6. Verification

### 6.1 Harness

The harness lives outside the repository, in the audit session's scratch directory, and is not committed. It is parameterised by binary, port and data directory so several runs can go at once. Its pieces:

| Script | What it does |
|-|-|
| `run.sh` | Starts and stops an instance on a free port with a clean environment (API key, library and download directories, debug logging), creates a first admin and a non admin user, logs in and fetches the CSRF token |
| `errshape.sh` | Runs ten error cases against a fresh instance and records status, content type and body in a diffable text file. Cases: unknown path, wrong method, root folder with a missing path, grab with an unresolvable release URL, download client test against a listener that answers 500 with a secret looking body, oversize bodies, OIDC provider list (unauthenticated and non admin), redirect base as a non admin, a static asset's headers, and a large file download at a limited rate |
| `explain.sh` | `EXPLAIN QUERY PLAN` plus wall time for the key queries on a seeded database, using the same SQLite engine as the binary, with a cross check against the system `sqlite3` CLI. It can read the query text from a branch worktree so an after run measures the branch's own SQL |
| `load.sh` | 500 requests at concurrency 8 against chosen paths (100 for the 30 MB queue), p50, p95, p99, bytes per response, and log rows per second during the run |
| `probe.sh` | One seeded instance, three timed requests per path, plus startup time |
| `seed/seedtool` | A small Go program in its own module that fills a migrated database with the row counts in section 1. Its dependencies are BSD-3-Clause or MIT; nothing ships |

To rerun: build a branch binary with `make build`, copy it under a unique name, then call `errshape.sh` and `load.sh` with that binary and diff the output against the captures taken at the baseline commit `5fcb780c`. Start the branch binary once on a seeded copy before `explain.sh` so migration 107 applies. Run `load.sh` alone on a quiet machine; it is timing sensitive.

### 6.2 Commands per pull request

Each pull request ran, as its description records:

- `gofmt`, `go vet`, `golangci-lint run` (v2.11.4, concurrency 2, 0 issues)
- `go test -p 2 ./cmd/... ./internal/...`
- `make smoke` (or the smoke suite against a freshly built binary when `web/` was unchanged)
- the web typecheck, lint and full test run when `web/` changed (#3174, #3176 and #3187)
- `go test -race` on the packages named in the descriptions where it applies (`internal/httpsec`, `internal/db`, `internal/metadata`)

`internal/api` is not run under `-race` locally; CI shards it.

### 6.3 New tests per pull request

**#3174 (E1 to E5).** `internal/api/error_leak_test.go` and `error_review_test.go`: `TestErrorLeak_GrabSendFailureIsGeneric502`, `..._GrabDatabaseErrorIsGeneric500`, `..._GrabNoClientIs400WithCode`, `..._RetrySendFailureIsGeneric502`, `..._RetryNoClientIs400WithCode`, `..._BulkRetryPerRowMessageIsGeneric`, `..._BulkDeletePerRowDatabaseErrorIsGeneric`, `..._EnrichAudiobookProviderError`, `..._RebindProviderError`, `..._RelinkAuthorProviderErrors`, `..._AddBookAndAdoptionLookupErrors`, `..._RootFolderCreateMissingPath`, `..._ManualImportPathNotAccessible`, `..._ReassignPreviewErrors`, `..._SetRole`, `..._SetRoleDatabaseError`, `..._MigrateErrors`, `..._MigrateUploadErrors`, `..._ABSImportErrors`, `..._DownloadClientTestConfigUpstreamBody`, `..._DownloadClientTestSavedUpstreamBody`, `..._ProwlarrTestAndSyncUpstreamBody`, plus `TestUpstreamDetail`, `TestUpstreamDetail_FreeTextCredentials`, `TestUpstreamDetail_CapNeverSplitsARune`, `TestDecodeJSON`, `TestWriteNotFoundOrServerError`, `TestRootFolderStatMessage_OneMessagePerCause`, `TestUpstreamErrorMessage_ProviderHold`, `TestWriteGrabError_NetworkFailureKeepsHint`, `TestDownloadClientTestConfig_ConnectionFailureIs502`, `TestProwlarrTestAndSync_StorageErrorIs500NotNotFound`. In `internal/metadata/providerhttp`: `TestDoScrubsURLSecretsWithoutDroppingTheChain`. In `internal/httpsec`: `redact_fragment_test.go`. Web: `web/src/api/core.test.ts`.

**#3176 (E6 to E11).** `internal/api/errors_test.go`: `TestNotFoundSweep_ConfigHandlers`, `..._NotificationHandlers`, `..._DownloadClientHandlers`, `..._IndexerHandlers`, `..._ReleaseHandlers`, `..._GenreHandlers`, `..._MatchDownloadBookLookup`, `..._ValidateImportBookLookup`. `internal/api/errors_413_test.go`: `TestOversizeBody_Is413`. `internal/api/recoverer_test.go`: `TestRecoverer_PanicIsJSON500WithRequestID`, `..._AbortHandlerRepanics`, `..._KeepsAStartedResponse`, `..._UpgradeRequestGetsNoBody`, `..._PassesThroughNormalResponsesAndFlusher`. `cmd/bindery/api_notfound_routes_test.go`: `TestAPIUnknownRouteIsJSON404`, `TestAPIWrongMethodIsJSON405WithAllow`, `TestNonAPIRoutesKeepTheirBehaviour`. `internal/api/opds_auth_errors_test.go`: `TestOPDS_BasicRefusalsAreJSON`. `internal/auth/middleware_test.go` (content type), a smoke subtest on the real binary, and the Prowlarr and `IndexersTab` tests updated for the boolean.

**#3175 (S1 to S9).** `TestLibraryRoots_NoConfigurationFailsClosed`, `..._ListerErrorWithoutDefaultsFailsClosed`, `TestLibraryRoots_Contains_SymlinkOutOfRootRefused`, `..._SymlinkedRoot`, `..._MissingFileUnderRootAllowed` `..._UnresolvableSymlinkRefused` and `..._DotDotAfterSymlinkRefused` (added by the review); `TestGuardedTransport_ProxyBypassedHostIsPolicyChecked`, `..._ProxyNoProxyMetadataHostRefused`, `..._ProxyItselfIsNotPolicyChecked`, `..._ProxyBypassedLoopbackAllowedUnderLANLoopback`, `TestIsProxyAddr`, `TestDirectGuardedTransport_*`, `TestRedirectGuard`; `redirect_guard_test.go` in each of the six download client packages and `internal/abs`; `TestChangePassword_WrongCurrentIsRateLimited`, `..._SuccessResetsLimiter` and `TestChangePassword_AbandonedRefundedAndLoginUnaffected`; `TestGetProviders_PublicViewHidesDiscoveryErrorAndGroups`, `..._AdminViewKeepsDetail`, `..._APIKeyAndModeGrantKeepDetail`; `TestOIDCRedirectBaseRouteRequiresAdmin`, `TestResolveOIDCRedirectBase_ForwardedValuesValidated`; `TestVerifyPassword_OutOfRangeParamsRefusedBeforeKDF`, `TestArgonParamsAcceptable`, `TestVerifyPassword_DefaultHashStillVerifies`; `TestOpenReader_PathWithURISyntaxIsTakenLiterally`, `TestImportReadarr_PathWithURISyntaxIsTakenLiterally`, `TestSQLiteFileURI`.

**#3177 (P6, P7, P8, P10).** `TestSPAHandler_CacheControl` (14 paths, with and without a URL base); `TestHTTPMiddleware_ResponseControllerReachesServerWriter`; `TestStallDeadlineWriter_RollsTheDeadlineForward`, `..._UnsupportedWriterPassesThrough`, `TestFileDownload_ExtendsWriteDeadline`, `..._DeadlineUnsupportedStillServes`, `..._RangeRequestIsPartialContent`, `..._StalledClientIsDropped`, `..._SteadyClientOutlastsStallTimeout`, `TestBridgeFile_ExtendsWriteDeadline`, `TestDownloadOutlastsServerWriteTimeout`, `TestDownloadRangeThroughRealStack`; `TestStreamZip_StoresAlreadyCompressedFormats`; `TestIsBookDownloadPath`, `TestCompressUnlessDownload`; `TestMetadataTransport_ClonesBaseAndRaisesIdleConns`, `..._FollowsTheBaseWhenItChanges`, `..._KeepsProxyAndDialHook`, `..._DoesNotWeakenGuardedTransports`, `..._ReusesConnectionsAcrossBursts`.

**#3185 (P1, P2, P3, P11).** `TestBookQueries_PlanBindsFileLookupToBook`, `TestBookQueries_FirstFilePerFormat`, `TestBookQueries_EquivalentToCTEShape`; `TestMigrate107PerfIndexesExistAndAreUsed` (indexes exist and are used, no temp b-tree on the history page or the downloads list), `TestMigrate107IsIdempotent`; `TestLogHandler_BatchesIntoFewTransactions`, `..._PreservesOrder`, `..._StopFlushesPartialBatch`, `..._FlushesAfterMaxWait`, `..._DoesNotHoldConnectionWhileWaiting`, `..._FailedBatchIsSalvagedAndReportedOnce`; `TestTTLCache_EvictionInspectsBoundedSample`, `..._OverflowSetStaysAtCapAndKeepsWrite`, `..._SampledVictimIsAmongEarlierExpiring`.

**#3187 (P4, P5, P9).** `internal/db/downloads_retention_test.go` (`TestListActive_StateTable`, `..._FallsBackThroughStamps`, `TestTrimTerminal`, `..._KeepsRowsThatShareAJobWithSurvivors`, `..._Batches`, `..._AgeIsWhenTheRowFinished`), `internal/scheduler/download_trim_test.go` (`TestTrimFinishedDownloads_UsesTheRetentionWindow`, `..._ZeroKeepsEverything`, `TestStart_DownloadTrimJob`), `internal/config/download_retention_test.go` (`TestLoad_DownloadRetentionDays`), `internal/api/queue_active_test.go` (`TestQueueList_ShowsEverythingButOldImports`), `internal/api/wanted_paging_test.go` (`TestListWanted_NoLimitKeepsTheBareArray`, `..._LimitReturnsTheEnvelope`, `..._PagesAreStableWhenTitlesTie`, `..._PagedFlagsUnmonitoredAuthors`, `TestListScope_WantedPaged_NonAdminIsolated`), `internal/importer/scanner_poll_queries_test.go` with a recording `database/sql` driver (`TestCheckSABnzbdDownloads_OneLookupForTheWholePoll`, the NZBGet twin, `..._RepeatedSlotActsOnce`, `TestRecoverInterruptedImports_NoWholeTableListPerRow`), and web `WantedPage.test.tsx`, `listStateInUrl.test.tsx`, `nordicLocales.render.test.tsx`.

### 6.4 Mutation checks

A mutation check breaks one guard on a tree that still compiles and confirms a named test fails. The trees were restored after each.

| PR | Mutations | Result |
|-|-|-|
| #3174 | 14 reverts of individual guards, then a second round (provider error sites, the Prowlarr 404, the provider scrub, the code field). Web: reverting to the substring match | All caught by named `TestErrorLeak_*` tests. The web revert fails 3 of 4 `core.test.ts` cases |
| #3176 | 34: the sweep per file group and manual import, body handling (never 413, two `ReadAll` sites), eight recoverer mutations, four not found and 405 mutations, three auth, two Prowlarr | All 34 caught. Not mutation tested: the `main.go` wiring of `api.Recoverer` (no panic route in the smoke suite), and the four `books.go` reload sites |
| #3175 | One per guard: S1 allow, S2 lexical fallback and unresolved root, S3 unguarded dial and guarded proxy dial, S4 no dial guard, no redirect validation, no `CheckRedirect`, S5 no limiter, S6 both views, S7 no `RequireAdmin` and unchecked proto and host, S8 bounds off and cap too loose, S9 both DSNs | All killed. One survived at first (SABnzbd without `CheckRedirect`, because the dial guard also refuses the redirect, which is defence in depth); the client tests were strengthened to require the hop refusal and it was then killed |
| #3177 | No hash check, immutable never set or set on every static path, `Unwrap` returning nil, `Flush` removed, deadline call removed or set to an hour, zip always deflate, `.m4b` or `.epub` dropped from the store list, store as the default, compress skip bypassed or the OPDS prefix broken, idle limit back to 2, no clone, clone of `http.DefaultTransport` | All killed |
| #3185 | Seven on the log writer (batch size 1, no flush on stop, no row retry, stderr report on every failure, reversed order, no timer exit, transaction held across the wait), the history index without `id DESC`, the sample size forced to the whole map. The review closed three guards that had survived the builder's tests: the production batch limit constant, a blocking send in the log handler's `Handle`, and the just inserted key guard in cache eviction | All caught after the review. The held transaction mutation deadlocks the package and times out; `DoesNotHoldConnectionWhileWaiting` is the test that names it |
| #3187 | 19, including chunk size 1 for `ListByNzoIDs`, trim deleting `importBlocked`, the shared job guard off, the cutoff ignored, no batch limit, zero retention still trimming, the job registered at 0 days, config rejecting `0`, the queue window changed, and four on the paged wanted list | All 19 caught (table in the #3187 description) |

## 7. Follow ups to file

Listed here only. None has been filed as an issue yet.

| Follow up | Where | Why |
|-|-|-|
| OPDS N+1 | `internal/opds/builder.go` (series feeds around lines 299 to 312 and 497 to 503, and author lookups in the book loops around 365 to 393) | Each book or series is fetched with a separate `GetByID` |
| `filepath.Walk` to `WalkDir` in the scanner | `internal/importer/scanner.go` (`walkRoot` and the download path walk), `scanner_handoff.go`, `flatten.go` | `Walk` calls `Lstat` on every entry |
| Recommender series loops | `internal/recommender/profile.go` and `candidates.go` | A `series.List` followed by `series.GetByID` per series, in three places |
| The remaining `decodeJSON` sweep | 45 handler sites on `json.NewDecoder(r.Body)` (section 4.5), starting with `POST /api/v1/author` | Oversize bodies answer 400 instead of 413 |
| `downloads.error_message` redaction on output and on write | `internal/api/queue.go` | The top remaining leak (section 4.4) |
| Torrent pollers list the whole table per tick | Transmission, qBittorrent, Deluge and rTorrent pollers | They build claimed hashes from every row (section 5.9) |
| `fillAuthorProgress` counts `status = 'imported'` | `internal/db/requests.go` | A partly owned dual format book is `wanted` yet owns files; `InLibraryPredicate` (`internal/db/in_library.go`) exists for exactly this rule |
| OPDS, Calibre and Grimmory syncers key on `status == imported` | `internal/opds/builder.go`, `internal/calibre/syncer.go`, `internal/grimmory/syncer.go` | Partly owned books are skipped |
| Metrics skip panicked requests | `internal/metrics/metrics.go` | Record in a `defer`, or move the recoverer inside the metrics middleware |
| Calibre test endpoint `ok` string | `internal/api/calibre.go` (two sites) | Still `"ok": "true"`, like Prowlarr was (E11) |
| Linked folder support for symlinked author directories | Settings, opt in | Lets a user mark a symlink target as part of the library without making it a root folder (section 3.2) |
| Uncapped logged stack, `request_id` on every 500 | `internal/api/recoverer.go`, `writeServerError` | Section 4.5 |
| Unguarded clients | Calibre plugin client, Grimmory client, Calibre cover fetch client, OPDS `baseURL` | Section 3.3 |
| Reject `/` as a root folder | `internal/api/root_folders.go` | Section 3.2 |
| Require `application/json` on `/auth/login` | `internal/api/auth.go` | Closes the login CSRF note in section 3.2 |
| Bundle size | `web/` | See the build output below |

**Bundle size.** Built from `462609ff` with `npm ci` and `npm run build` (Vite 8.3.1, 327 modules). The build wrote 90 files under `assets/` (one entry chunk, route and settings tab chunks, lazy locale chunks, one CSS file, one PNG), 1,722,677 bytes of JavaScript in total and 1,895,895 bytes for all of `dist/`. The largest chunks:

| Chunk | Size | Gzip |
|-|-|-|
| `index` (entry, loaded on every page) | 473.72 kB | 146.77 kB |
| `sv` (Swedish locale, lazy) | 105.79 kB | 34.16 kB |
| `nb` (Norwegian locale, lazy) | 103.42 kB | 33.95 kB |
| `AuthorDetailPage` | 67.24 kB | 16.51 kB |
| `BookDetailPage` | 66.77 kB | 16.47 kB |
| `ImportPage` | 60.97 kB | 17.12 kB |
| `GeneralTab` (settings) | 56.86 kB | 12.46 kB |
| `ko` (Korean locale, lazy) | 47.62 kB | 15.97 kB |
| `CalibreTab` (settings) | 44.05 kB | 8.97 kB |
| `ABSTab` (settings) | 40.78 kB | 8.92 kB |
| `index` CSS | 92.56 kB | 15.82 kB |

The entry chunk is the only one every visitor downloads. The audit did not profile what is in it, so whether it can shrink is open. Vite raised no chunk size warning.
