# streakd — implementation plan

*2026-07-02. Companion to [DESIGN.md](./DESIGN.md). Repo: `github.com/Vartent/streakd`.*

The plan is ordered so that the riskiest logic (timezone/period math) is built and
hardened first, everything is verifiable at every step, and the three internal apps act
as the acceptance suite before any public release. Each phase has an explicit **exit
gate**; a phase is not done until its gate passes. No phase starts before the previous
gate is green.

---

## 0. Ground rules

- **TDD for the core.** `internal/core` (pure logic) is written test-first; its tests
  encode the semantics spec (DESIGN §6) line by line. Target: 95%+ coverage on
  `internal/core`, no coverage theater elsewhere — integration tests carry the rest.
- **`main` is always green and releasable.** CI must pass (lint, `go vet`,
  `go test -race`, integration) before merge. Solo project: short-lived branches,
  squash-merge, no force-push to `main`.
- **Every bug found after a phase closes gets a regression test in the same commit as
  the fix.** No exceptions — this is the whole point of the project.
- **The oracle invariant is enforced everywhere:** replaying the `period_marks` ledger
  from scratch must always reproduce the stored `current_count`/`longest`/`freezes`.
  A `Recount()` function implements the replay; every integration test asserts
  `Recount() == stored state` at the end. Any divergence is a P0 bug.
- No emojis anywhere in code, commits, or output. MIT license from the first commit.

## 1. Pinned technical decisions

| Decision | Choice | Rationale |
|---|---|---|
| Language / min version | Go, `go 1.25` | two internal consumers are Go; single static binary for sidecar |
| Module path | `github.com/Vartent/streakd` | public embedded API at module root |
| DB | PostgreSQL 14+ only | all three internal apps run Postgres; no SQLite in v1 |
| DB driver | `jackc/pgx/v5` (pool) | row locks, native types, no ORM |
| Migrations | `pressly/goose/v3` via `embed.FS`, dedicated `streaks` schema, own `streaks.goose_version` table | familiar (restreak uses goose); embedded = sidecar and library migrate themselves |
| HTTP | stdlib `net/http` (1.22+ pattern routing) | zero framework deps in a public lib |
| API contract | spec-first `api/openapi.yaml`; `oapi-codegen` for server stubs, `openapi-generator` for TS/Python clients | clients stay logic-free by construction |
| Timezones | IANA via stdlib; **`import _ "time/tzdata"` in `cmd/streakd`** | scratch/distroless image has no tzdata on disk — this is a classic production landmine, pinned here |
| Clock | `Clock` interface injected into engine; real clock default | time-travel tests; no globals |
| Scheduler | in-process ticker loop (5 min), advisory-lock guarded | no external cron dependency; multi-instance safe |
| Docker | multi-stage build → distroless static, `linux/amd64+arm64` | `docker run` quickstart |
| CI | GitHub Actions: lint (`golangci-lint`), `go vet`, `go test -race`, integration job with `postgres:16` service container, docker build | free for public repos |
| Release | `goreleaser`: binaries + `ghcr.io/vartent/streakd` image on git tag | one-command releases |
| Versioning | SemVer; `v0.x` until the three internal apps are migrated; then `v1.0.0` | API freedom while it matters |

## 2. Repository layout

```
streakd/
  streakd.go, options.go, ...   -- public embedded Go API (root package)
  streaktest/                   -- public time-travel test harness (Phase 6)
  cmd/streakd/                  -- sidecar entrypoint (env config, tzdata import)
  internal/core/                -- PURE: period math, config, derive; no DB, no clock reads
  internal/store/               -- pgx queries, goose migrations (embed.FS)
  internal/engine/              -- transactional orchestration: Record/Get/settle/scheduler/events
  internal/httpapi/             -- sidecar HTTP handlers, API-key auth, webhook dispatcher
  api/openapi.yaml
  clients/typescript/           -- generated + hand-written README
  clients/python/               -- generated + hand-written README
  docs/DESIGN.md, docs/PLAN.md, docs/SEMANTICS.md
  examples/docker-compose.yml
  .github/workflows/ci.yml, release.yml
```

Dependency rule (enforced by review + `depguard`): `core` imports nothing internal;
`store` imports `core`; `engine` imports both; `httpapi` imports `engine`. The public
root package is a thin veneer over `engine`.

---

## 3. Phases

### Phase 0 — Scaffolding (S)

Repo hygiene before any logic: `go.mod`, MIT `LICENSE`, `README.md` (one paragraph +
"status: pre-alpha"), `.golangci.yml`, CI workflow running lint/vet/test against an
empty package, `docs/` with DESIGN + PLAN, issue templates off, branch protection on
`main` (CI required).

**Exit gate:** CI badge green on a trivial `go test ./...`; `docker build` of a
hello-world `cmd/streakd` succeeds.

### Phase 1 — Pure core: period math + derive (L, highest risk)

The heart. No database, no HTTP, no goroutines — pure functions only.

1. `core.Config` parsing/validation (period, weekday mask, boundary offset, threshold,
   freezes, target, milestones) with exhaustive invalid-input tests.
2. `core.PeriodKey(t, tz, cfg)` — local day/ISO-week/month computation with boundary
   offset (DESIGN §6.1).
3. `core.PrevScheduled / MissedBetween` — weekday-mask-aware period stepping.
4. `core.Derive(state, cfg, now, tz)` — the pure derivation (DESIGN §6.2): effective
   count, alive/frozen/broken, pending freeze spend, seconds_until_loss.
5. `core.Apply` — pure state transition for "period earned" (count, longest, freeze
   accrual, milestone/target detection); `settle` and `Record` will be thin
   transactional wrappers around `Derive`+`Apply`.

Test matrix (all table-driven, all must exist before the phase closes):

- DST: Europe/Berlin spring-forward and fall-back nights; activity recorded inside the
  skipped/repeated hour; boundary_offset interacting with DST.
- Exotic zones: `Australia/Lord_Howe` (30-min DST), `Asia/Kathmandu` (+05:45),
  `Pacific/Kiritimati` (+14).
- Timezone travel east/west mid-streak (port kanji's `streak_timezone_test.go` cases
  verbatim — they are the spec).
- Weekday masks: weekend-only streaks, mask where "yesterday" is 3 days ago, mask
  changes mid-streak.
- Week periods across ISO week 52/53/1; month periods Jan 31 → Feb 28; leap day.
- Freeze arithmetic: exact-freeze-count gap, gap exceeding freezes by one, accrual cap.
- Property-based tests (`testing/quick` or `rapid`): count never negative; a period
  earns at most once; derive is idempotent (`Derive(Derive(...)) == Derive(...)`);
  tz change never lowers derived count (generosity rule).

**Exit gate:** full matrix green; mutation-testing spot check on `Derive` (manually
flip 5 conditions, confirm at least one test fails for each); coverage ≥ 95% on
`internal/core`.

### Phase 2 — Storage + transactional engine (L)

1. Goose migrations for the full DESIGN §5 schema.
2. `store`: typed queries; every state mutation goes through `SELECT ... FOR UPDATE`
   on the streak row.
3. `engine.Record` (settle → upsert mark → apply → emit events → idempotency-key
   memoization), `engine.Unrecord`, `engine.Get/List` (derived, never stale),
   `engine.SetTimezone` (generosity rule), `engine.Repair`, `engine.Calendar`,
   `engine.Recount` (the oracle).
4. Event rows written in the same transaction as the state change (outbox pattern).

Tests (integration, real Postgres in CI):

- The kanji race repro, upgraded: 50 goroutines × `Record` on one streak → exactly +1
  earn, no lost updates, `-race` clean. Also: concurrent `Record`+`SetTimezone`,
  concurrent `Record`+`Unrecord`.
- Idempotency: same key twice → identical response, one mark; key reuse across streaks
  rejected.
- Full oracle sweep: after every integration scenario, `Recount == stored`.
- Crash-consistency: kill the transaction between settle and apply (test hook) →
  retry converges, no double freeze spend.

**Exit gate:** integration suite green under `-race`; a 10k-subject seed script runs
`Record` p50 < 5ms, p99 < 25ms on a laptop Postgres (sanity, not SLA).

### Phase 3 — Scheduler + events (M)

1. Ticker loop: tz-bucketed boundary crossings → `settle` (freeze consumption /
   `broken` near real time); at-risk reminder windows with `reminder_claims` dedup and
   decay gaps; never fires for broken streaks.
2. Multi-instance safety via Postgres advisory lock per tick batch.
3. Embedded event consumption: `WithEventHandler` callback + `PollEvents(after)` API.

Tests: simulated clock driving 3 subjects in 3 zones through 10 days including a DST
night — assert exact event sequence and timing windows; scheduler killed for 48h then
restarted → catches up, emits each event exactly once (dedup proven); two scheduler
instances racing → no duplicate events.

**Exit gate:** the 10-day simulation asserts the complete, ordered event log; downtime
catch-up test green.

### Phase 4 — Public embedded API + docs of record (S)

Freeze the root-package API (`New`, `Migrate`, `Record`, `Get`, `List`, `SetTimezone`,
`Repair`, `Calendar`, `RunScheduler`, options). Write `docs/SEMANTICS.md` — the
normative spec extracted from DESIGN §6, kept in lockstep with the core tests
(each SEMANTICS clause references the test that enforces it).

**Exit gate:** `go doc` output reviewed; example in README compiles as a doctest
(`Example_` functions); API reviewed once with fresh eyes after a day's gap.

### Phase 5 — Sidecar: HTTP API, multi-app, Docker (M)

1. `api/openapi.yaml` spec-first, mirroring the embedded API 1:1 (DESIGN §7).
2. `oapi-codegen` server stubs; handlers are thin engine calls; API-key auth per app
   (`apps` table, hashed keys); structured request logs.
3. Webhook dispatcher: per-app URL + HMAC-SHA256 signature header, at-least-once with
   exponential backoff (1m→1h, 24h max age), `delivered_at` bookkeeping; polling
   endpoint `GET /v1/events?after=` as the fallback consumer.
4. `cmd/streakd`: env config (`DATABASE_URL`, `WEBHOOK_*`, `PORT`), graceful shutdown,
   `/healthz` + `/readyz`, tzdata import, auto-migrate on boot (flag-gated).
5. Multi-stage Dockerfile (distroless static), `examples/docker-compose.yml`
   (postgres + streakd + a 20-line fake "app" webhook receiver).

Tests: e2e suite that runs the compose stack in CI — record via HTTP, cross a simulated
midnight (test-only `X-Streakd-Clock` header, enabled by env flag, never in release
builds... **no** — clock override via a separate test binary build tag, not a header;
headers in prod images are how restreak got its `UserNowTesting` hole), receive the
webhook, verify HMAC. Contract test: generated TS/Python clients compile against the
spec.

**Exit gate:** `docker compose up` quickstart from the README works on a clean machine
(timed: under 5 minutes from clone to first webhook); e2e green in CI; image size
< 30 MB.

### Phase 6 — streaktest harness (M)

Public `streaktest` package (DESIGN §7): simulated clock, `AdvanceDays`, `TravelTo`,
event and state assertions; works against both embedded engine and a live sidecar
(HTTP mode drives the flag-gated test clock). Port the full kanji streak test suite
(408 lines) into `streaktest` scenarios as executable acceptance tests.

**Exit gate:** kanji's ported suite green; harness README with 3 copy-paste examples.

### Phase 7 — Clients: TypeScript + Python (S)

Generated from the OpenAPI spec, published as `@vartent/streakd` (npm) and `streakd`
(PyPI) — names checked/reserved early in Phase 5. Hand-written: 30-line README each,
retry/idempotency-key helper. No logic beyond transport.

**Exit gate:** both clients exercised by the compose e2e suite; `npm publish --dry-run`
and `twine check` pass.

### Phase 8 — Internal rollout: the real acceptance test (L, calendar-bound)

Order: kanji_cards → addictive_game → restreak_server (ascending feature surface).
Each app follows the same **shadow → cutover → delete** protocol:

1. **Shadow**: run streakd sidecar next to the existing logic; every real user action
   is dual-written (existing engine + `streakd.Record`); a nightly job diffs derived
   streakd state against the legacy state for every active user and logs divergences.
2. **Cutover** only after **14 consecutive days with zero unexplained divergences**
   (explained ones — e.g. streakd fixing kanji's stale-display bug — are documented
   and are the desired behavior). Reads switch to streakd; legacy write path stays as
   dead code for one release.
3. **Delete** the legacy streak code and its cron entries in the following release.

Per-app notes:
- **kanji_cards**: single streak/user; push scheduler candidates move to `at_risk`
  webhook consumer — kills the dead-streak-nag bug at cutover.
- **addictive_game**: Python client; connects the currently orphaned backend streak to
  the games; gem ladder stays app-side driven by `extended` events; `tz_offset` int
  replaced by IANA tz from Telegram WebApp locale or explicit picker.
- **restreak_server**: N streaks/user, guards→freezes mapping, weekday masks, targets,
  toggle→`Unrecord`; the 276-line overnight job and 462-line repo are replaced; its
  Telegram notifier becomes an event consumer. Any semantics the SDK cannot express
  cleanly = design bug → fix in streakd core, never with an app-side workaround.

**Exit gate (per app):** 14-day zero-divergence report, cutover shipped, legacy code
deleted, app's own test suite green. Phase 8 closes when all three are done.

### Phase 9 — v0.1.0 public release (S)

goreleaser tag pipeline (binaries + ghcr image), README rewritten around the 5-minute
compose quickstart, SEMANTICS.md linked prominently ("this is the part you don't want
to write yourself"), CHANGELOG, a short "why not Trophy / when to use which" section
(honest, links to them), Show HN / r/golang / r/SideProject posts drafted. `v1.0.0` is
tagged only after all three internal apps run on it in production for a month.

**Exit gate:** a stranger can go from README to a working streak with webhook
notifications in under 15 minutes without asking anything.

---

## 4. Test strategy summary

| Layer | Tooling | What it proves |
|---|---|---|
| Pure core | table-driven + property-based, no DB | period math, DST, masks, freezes, derive — the semantics spec |
| Engine | integration vs real Postgres, `-race` | locking, idempotency, oracle invariant, crash convergence |
| Scheduler | simulated-clock scenario runs | event exactness, catch-up after downtime, multi-instance dedup |
| Sidecar | compose e2e in CI | HTTP contract, auth, webhooks + HMAC, image actually boots with tzdata |
| Clients | generated-code contract tests | spec/client drift |
| Whole system | shadow-mode diffs on 3 real apps | production semantics vs three battle-tested (and battle-broken) implementations |

## 5. Risks and mitigations

| Risk | Mitigation |
|---|---|
| Timezone edge case not covered by the matrix | property tests + oracle replay + shadow mode on real users are three independent nets; any escape becomes a core regression test |
| Scope creep toward a gamification platform (XP, leaderboards) | DESIGN §8 non-goals are pinned; new features need a written case in docs/ first |
| Solo-dev stall mid-project | phases are independently shippable; after Phase 2 the embedded lib is already usable in kanji_cards even if the sidecar never ships |
| Postgres coupling limits adoption | accepted for v1 (all internal apps are Postgres); storage interface is isolated in `store` so a driver seam exists, but no second backend before v1.0 |
| Test-clock hook leaks into production (restreak's mistake) | clock override compiled only under a build tag; release workflow greps the binary for the tag symbol and fails if present |
| Webhook consumer downtime loses notifications | at-least-once with 24h retry + polling endpoint as catch-up; events are rows, never fire-and-forget |
| npm/PyPI/ghcr name squatting | reserve names during Phase 5, before any public noise |

## 6. Effort estimate

Rough, in focused evenings (~3h): Phase 0: 1 · Phase 1: 6–8 · Phase 2: 6–8 ·
Phase 3: 4 · Phase 4: 2 · Phase 5: 5–6 · Phase 6: 3 · Phase 7: 2 ·
Phase 8: 4–6 spread over ~6 weeks of calendar time (shadow windows) · Phase 9: 2.
Total ≈ 35–42 evenings; first production value (kanji_cards on embedded lib in shadow)
possible after ~15.

## 7. Immediate next actions

1. Phase 0 scaffolding commit (license, CI, lint, empty packages).
2. Reserve `streakd` on PyPI / `@vartent/streakd` on npm (5 minutes, cheap insurance).
3. Start Phase 1 with the ported kanji timezone tests as the first red tests.
