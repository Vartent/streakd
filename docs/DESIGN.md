# streakd — an open-source streak engine

*Design document, 2026-07-02. Status: proposal, nothing implemented.*

---

## 1. Problem

Streak logic looks trivial ("did the user show up yesterday?") and is not. Evidence: three
of our own projects implemented it independently and all three have real bugs.

An audit of `restreak_server`, `kanji_cards`, and `addictive_game` found:

| | restreak_server | kanji_cards | addictive_game |
|---|---|---|---|
| Streak entity | N streaks/user, own table | 1/user, columns on `users` | login streak + duplicated client streaks |
| Day boundary | user-local 23:59:59 stored as UTC timestamp | user-local midnight, DATE compare | UTC date string |
| TZ representation | IANA string | IANA string in **two** tables that can disagree | fixed minute offset, no DST |
| Expiry | hourly cron is source of truth | lazy, **display goes stale** | lazy |
| Freezes | guards (earn 1/30d, cap 3) | none | none |
| Reminders | cron `*/15` with **exact-minute match** — times off the :15 grid never fire | window match, but **nags about already-dead streaks forever** | window match, but **promises rewards for a broken streak** |
| Testing clock | global mutable `UserNowTesting` | `nowFunc` | pure functions taking `today` |
| Lines spent | ~4,300 Go + ~2,200 TS | ~1,100 | ~450 |

Recurring failure modes, each independently reinvented and independently broken:

1. **Stale state** — lazy systems never reset the stored count, so profile screens show a
   streak that died a week ago (`kanji_cards/user_stats.go`), and reminder jobs read the
   stale number and send "don't lose your N-day streak" for dead streaks.
2. **Timezone drift** — notification times converted local→UTC once at creation fossilize
   the DST offset (`restreak_server` shipped a hardcoded repair migration for this);
   deadline timestamps stored in UTC need a "patch drift > 1h" heuristic inside the cron.
3. **Cron as correctness** — when the hourly resolver *is* the source of truth, a dead cron
   means wrong data, nondeterministic ~1h grace after midnight, and silent deadline rewrites.
4. **Concurrency** — two projects independently discovered lost updates under concurrent
   writes (kanji reproduced losing 48 of 50 concurrent increments) and added `FOR UPDATE`.
5. **Testing time** — all three hacked in a fake clock differently; one ships a test hook
   inside the production job body.

Total: ~8,000 lines across three codebases to get three inconsistent, partially broken
versions of the same feature. That is the product gap.

---

## 2. Market

**Direct SaaS incumbent exists: [Trophy](https://trophy.so).** Launched 1.0 end of 2025,
500K+ end users. Streaks (daily/weekly/monthly), per-user timezone tracking incl. tz
changes, freezes (auto-accumulation, cap, consumed at local midnight), admin streak
repair, metric-based conditions, reminder emails/push in local time, SDKs for 7 backend
languages. Pricing: free ≤ 100 MAU, ~$299/mo at 10K MAU + $0.015/user beyond. Also in the
space: GameLayer, StriveCloud, Voucherify (enterprise/marketing-led, sales-driven), and a
long tail of small streak-API startups (lynes, EngageFabric content plays).

**Duolingo open-sourced nothing.** Only design/PM blog posts (600+ experiments on the
streak feature; server-authoritative state, client caching). There is no "official"
reference implementation to compete with.

**The open-source quadrant is empty.** GitHub has: abandoned heavyweight gamification
platforms (gengine, Oasis — rules-engine scale, not drop-in), README-badge toys, and
localStorage habit trackers. There is **no focused, production-grade, self-hostable
streak engine** — nothing you can `docker run` or import that handles timezones, freezes,
lazy expiry, and at-risk reminders correctly.

### Verdict

- **As a venture-style paid SaaS: pessimistic.** Trophy is exactly this idea, executed,
  self-serve, cheap, two years ahead. A me-too SaaS from a solo dev has no wedge.
- **As an open-source engine: optimistic.** The self-host/library quadrant is empty, the
  problem is genuinely hard enough that "just write it" fails (we failed 3×), and every
  indie dev with a retention feature faces Trophy's per-MAU pricing or reimplementation.
  The classic OSS wedge applies: devs who won't ship user-activity data to a third party,
  won't take a per-MAU tax on a *feature*, or need offline/on-prem.
- **Economics: scratch-your-own-itch first.** Three internal consumers exist today. The
  SDK pays for itself even with zero external adoption; external adoption is upside, not
  the bet. Monetization (hosted sidecar under Trophy's price umbrella, pro dashboard,
  sponsorware) is a later option only if OSS traction appears. Do not build billing first.

---

## 3. Product shape

**One Go core, two consumption modes, one Postgres schema.**

```
                ┌──────────────────────────────┐
                │        streakd core          │
                │  (Go library, owns schema    │
                │   `streaks.*` in Postgres)   │
                └──────┬───────────────┬───────┘
        embedded mode  │               │  sidecar mode
                       ▼               ▼
        import "streakd" in     docker run streakd
        restreak_server /       → HTTP API + API keys
        kanji_cards backend     → addictive_game (Python),
        (same process, same       any future stack
        DB, no new infra)
```

- **Embedded (Go library)**: `go get`; the library runs its own migrations into a
  dedicated `streaks` schema in the host app's existing Postgres. No new service, no new
  DB. This serves restreak_server and kanji_cards directly.
- **Sidecar (HTTP service)**: the same core wrapped in a small HTTP API, shipped as a
  single Docker image with multi-app support and API keys. Serves addictive_game (Python)
  and anyone else. This binary *is* the future hosted product if that ever happens.
- **Thin clients**: TypeScript and Python packages generated from the OpenAPI spec; they
  contain no logic (server-authoritative, like Duolingo).

Why Go for the core: two of three consumers are Go+Postgres; single static binary is the
best sidecar/self-host story; `time.LoadLocation` + tzdata is solid.

Working name: `streakd`.

---

## 4. Design principles

These are the five decisions that fix the audited failure modes. Everything else follows.

1. **Lazy truth, cron for side effects only.** The effective streak state is a **pure
   function** `derive(stored, config, now, tz)` evaluated on every read. Reads are always
   correct even if no scheduler ever runs. The scheduler exists solely to *settle* state
   (persist freeze consumption, emit events near the moment they happen) and to fire
   at-risk reminders. A dead cron delays notifications; it can never corrupt data.
   *(Fixes: stale displays, cron-as-correctness, midnight grace nondeterminism.)*

2. **Local day keys, never UTC deadlines.** Store `last_earned_period` as a calendar date
   computed in the subject's IANA timezone at event time. Day arithmetic is date
   arithmetic — DST does not exist at the date level. No stored UTC timestamps that need
   drift-patching heuristics. *(Fixes: DST fossils, deadline rewrites.)*

3. **Earn-once ledger.** One row per (streak, period) with an accumulated amount and an
   idempotency key, written with `INSERT ... ON CONFLICT` under the streak row lock.
   Gives: same-day dedup, safe concurrent increments, undo/toggle, audit, and recount
   after timezone changes. *(Fixes: lost updates, double counting, no-undo.)*

4. **Timezone is a first-class versioned property with a generosity rule.** One server-side
   `SetTimezone` operation; on change the streak is re-derived under the new zone and the
   result may never be *worse* than before the change (traveling never breaks a streak;
   it may at most delay the next earnable day). Changes are logged and rate-limitable for
   abuse. *(Fixes: three ad-hoc tz sync channels, deadline-nuking on tz change.)*

5. **The clock is injected, and the simulator is a public API.** `WithClock(c)` on the
   engine; a shipped `streaktest` package can fast-forward days, cross DST transitions,
   and relocate subjects between zones in tests. Time-travel testing is a headline
   feature, not a hack. *(Fixes: global test flags in production jobs.)*

---

## 5. Domain model

```
Subject      — your user (or team, or device). external_id + IANA timezone.
Streak       — (subject, key) instance with its own config snapshot.
               Supports both "one login streak per user" (kanji, addictive_game)
               and "user creates N custom streaks" (restreak).
PeriodMark   — the earn-once ledger row: (streak, local_period, amount).
Event        — outbox row: what happened, for webhooks/polling.
```

### Config (per streak, snapshotted from an app-level template, overridable per instance)

```jsonc
{
  "period": "day",                  // day | week | month
  "weekday_mask": "1111100",        // day-period only; rest days can't break the streak
  "boundary_offset_min": 0,         // e.g. 180 → the "day" ends at 03:00 local (night-owl mode)
  "min_amount_per_period": 1,       // threshold; record() accumulates amount
  "target": null,                   // finite streak (restreak): close as success at N
  "freezes": {
    "initial": 0,
    "earn_every_n_periods": 30,     // restreak guards; null → disabled
    "max": 3,
    "auto_consume": true
  },
  "milestones": [7, 14, 30, 50, 100, 365],   // emit milestone events server-side
  "at_risk_reminder": { "local_time": "20:00", "decay": [1, 3, 7] }  // anti-spam gaps
}
```

### Schema (Postgres, owned schema `streaks`)

```sql
CREATE TABLE streaks.subjects (
  id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  app_id       BIGINT NOT NULL,              -- sidecar multi-tenancy; constant in embedded mode
  external_id  TEXT   NOT NULL,
  timezone     TEXT   NOT NULL DEFAULT 'UTC',  -- IANA; validated on write
  tz_changed_at TIMESTAMPTZ,
  UNIQUE (app_id, external_id)
);

CREATE TABLE streaks.streaks (
  id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  subject_id    BIGINT NOT NULL REFERENCES streaks.subjects,
  key           TEXT   NOT NULL,             -- 'practice', 'login', 'custom:42'
  config        JSONB  NOT NULL,
  current_count INT    NOT NULL DEFAULT 0,   -- as of settled_through, pre-derive
  longest       INT    NOT NULL DEFAULT 0,
  last_earned   DATE,                        -- period key (day / ISO-week start / month start), local
  freezes       INT    NOT NULL DEFAULT 0,
  freeze_progress INT  NOT NULL DEFAULT 0,   -- periods earned since last freeze grant
  status        TEXT   NOT NULL DEFAULT 'active',  -- active | completed | archived
  settled_through DATE,                      -- last period the settler processed
  UNIQUE (subject_id, key)
);

CREATE TABLE streaks.period_marks (
  streak_id     BIGINT NOT NULL REFERENCES streaks.streaks,
  local_period  DATE   NOT NULL,
  amount        INT    NOT NULL DEFAULT 0,
  tz_at_record  TEXT   NOT NULL,
  first_recorded TIMESTAMPTZ NOT NULL,
  last_recorded  TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (streak_id, local_period)      -- the earn-once guarantee
);

CREATE TABLE streaks.idempotency_keys (
  app_id BIGINT NOT NULL, key TEXT NOT NULL, streak_id BIGINT NOT NULL,
  result JSONB NOT NULL, created_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (app_id, key)                  -- pruned after 48h
);

CREATE TABLE streaks.events (
  id BIGSERIAL PRIMARY KEY,
  streak_id BIGINT NOT NULL,
  type TEXT NOT NULL,       -- extended | at_risk | freeze_earned | freeze_consumed
                            -- | broken | repaired | milestone | completed
  payload JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL,
  delivered_at TIMESTAMPTZ           -- webhook delivery bookkeeping
);

CREATE TABLE streaks.reminder_claims (           -- once-per-local-day dedup (kanji's pattern)
  streak_id BIGINT NOT NULL, local_day DATE NOT NULL,
  PRIMARY KEY (streak_id, local_day)
);
```

---

## 6. Semantics — the hard parts, specified

### 6.1 Period computation

`local_period(t, tz, cfg)`:
- shift: `t' = t - cfg.boundary_offset_min`
- `d = calendar date of t' in tz` (via IANA zone; DST handled by the tz database)
- period key: `day → d`; `week → ISO week start of d`; `month → first of month of d`

All comparisons are date arithmetic on period keys. "Yesterday" for a `day` streak with a
weekday mask = *previous scheduled day* (skip rest days). Rest days can never break or
extend a streak.

### 6.2 Derivation (the pure core)

```
derive(streak, now):
  p        = local_period(now, subject.tz, cfg)
  missed   = scheduled periods in (streak.last_earned, p) exclusive
  if len(missed) == 0:
      effective = current_count            (earned today or yesterday-equivalent)
  elif len(missed) <= streak.freezes and cfg.freezes.auto_consume:
      effective = current_count            (frozen, still alive)
      pending_freeze_spend = len(missed)
  else:
      effective = 0                        (broken; count resets on next earn)
  returns: effective count, earned_this_period, alive/frozen/broken,
           seconds_until_loss, freezes remaining after pending spend
```

`derive` is exported, deterministic, and side-effect-free — every read path (Get, List,
record responses) returns derived state, so **displays are never stale** regardless of
scheduler health.

### 6.3 Settlement

`settle(streak, through_period)` persists what `derive` concluded: decrements freezes with
`freeze_consumed` events per missed period, or zeroes `current_count` with a `broken`
event, advances `settled_through`. Called from three places, all idempotent under the row
lock: (a) the scheduler shortly after each subject's boundary crossing, (b) lazily inside
`Record` before applying the new mark, (c) lazily on read *only* if a write is already
cheap (optional knob). Events therefore fire near real time when the scheduler is
healthy, and catch up correctly when it isn't.

### 6.4 Record / undo

```
Record(subject, key, at=now, amount=1, idempotency_key=nil):
  lock streak row (SELECT ... FOR UPDATE)
  settle up to current period
  upsert period_marks += amount for local_period(at)
  if mark.amount crosses cfg.min_amount_per_period and period not yet earned:
      current_count = derive-alive ? current_count + 1 : 1
      longest = max(longest, current_count)
      freeze_progress++ → maybe freezes++ (cap), event freeze_earned
      events: extended, maybe milestone, maybe completed(target)
  return derived state + events emitted
```

- `at` may be slightly in the past (offline sync) but is clamped: no earning periods
  older than `last_earned` (monotonic) and no future periods.
- `Unrecord(subject, key, period)` reverses a mark (restreak's toggle): decrements the
  count only if that period had crossed the threshold and is the *latest* earned period.

### 6.5 Timezone change

`SetTimezone(subject, tz)`:
- recompute the current period under the new zone; re-derive.
- **generosity rule**: if the streak was alive under the old zone at the moment of the
  change, it remains alive; `last_earned` is never moved backward. A day already earned
  stays earned (marks are immutable history with `tz_at_record`).
- the change is an event (`payload: {from, to}`); abuse (tz-hopping to double-earn) is
  structurally impossible because `period_marks` PK is (streak, period) and periods are
  monotonic; hopping can only *delay* boundaries, and an optional rate limit
  (`max_tz_changes_per_week`) covers the rest.

### 6.6 Repair

`Repair(subject, key)` restores `current_count` to its value before the last `broken`
event (read from the event log), emits `repaired`. Policy (paid repair, one per month,
etc.) belongs to the host app — the engine just enforces "only the most recent break,
within `repair_window_days`". This is the Duolingo monetization hook and Trophy parity.

### 6.7 At-risk reminders (engine decides *when*, app decides *how*)

The engine never talks to Telegram/APNs/email — our three apps use three different
channels; delivery adapters don't belong in the core. Instead the scheduler emits
`at_risk` events: streak alive, not earned this period, subject's local time ≥ configured
reminder time, **window matching** (not exact-minute equality), once-per-local-day via
`reminder_claims` insert, with decay gaps (1/3/7 days) after ignored reminders, and
**never for broken streaks** — the event payload carries the *derived* count, fixing both
audited nag bugs. Apps consume events via webhook (sidecar) or Go callback/outbox poll
(embedded) and send through their own channel.

### 6.8 Scheduler

One worker loop (embeddable via `eng.RunScheduler(ctx)` or the sidecar's built-in),
tick every 5 min: subjects are bucketed by timezone, so each tick touches only zones that
just crossed a boundary (~1/24th of subjects/hour) plus pending reminder windows. Every
action is idempotent; after downtime it settles everything missed. Multiple instances are
safe (row locks + claims); no leader election needed at our scale.

---

## 7. API surface

### Go (embedded)

```go
eng, err := streakd.New(pool,
    streakd.WithClock(clock),            // defaults to real time
    streakd.WithEventHandler(onEvent),   // sync callback; outbox polling also available
)
err = eng.Migrate(ctx)

state, err := eng.Record(ctx, streakd.RecordReq{
    Subject: "tg:12345", Key: "practice",
    IdempotencyKey: "answer:9876",
})
state, err  = eng.Get(ctx, "tg:12345", "practice")      // always derived, never stale
states, err = eng.List(ctx, "tg:12345")
err = eng.SetTimezone(ctx, "tg:12345", "Asia/Tokyo")
state, err = eng.Repair(ctx, "tg:12345", "practice")
cal, err   = eng.Calendar(ctx, "tg:12345", "practice", "2026-07")  // flame calendar UI
go eng.RunScheduler(ctx)
```

### HTTP (sidecar) — mirrors 1:1

```
POST   /v1/subjects/{ext_id}/streaks/{key}/record     {amount?, at?, idempotency_key?}
DELETE /v1/subjects/{ext_id}/streaks/{key}/record/{period}
GET    /v1/subjects/{ext_id}/streaks/{key}
GET    /v1/subjects/{ext_id}/streaks
PUT    /v1/subjects/{ext_id}/timezone                 {tz}
POST   /v1/subjects/{ext_id}/streaks/{key}/repair
GET    /v1/subjects/{ext_id}/streaks/{key}/calendar?month=2026-07
POST   /v1/streaks/definitions                        (app-level templates)
GET    /v1/events?after=...          + configurable webhook w/ HMAC signature
```

### State payload (everything the three UIs render today)

```jsonc
{
  "key": "practice",
  "count": 12, "longest": 40,
  "state": "alive",                        // alive | frozen | broken
  "earned_this_period": true,
  "seconds_until_loss": 39600,             // for countdown UI
  "freezes": { "available": 2, "max": 3, "progress": 12, "needed": 30 },
  "milestone": { "reached": 7, "next": 14 },
  "target": { "goal": 100, "done": 12 }    // restreak finite streaks; null otherwise
}
```

### Test harness (public)

```go
sim := streaktest.New(t, eng, streaktest.StartAt("2026-03-07T21:00:00", "Europe/Berlin"))
sim.Record("u1", "practice")
sim.AdvanceDays(1)                         // crosses the DST spring-forward gap
sim.Record("u1", "practice")
sim.TravelTo("u1", "America/Los_Angeles")  // fly west same day
sim.ExpectState("u1", "practice", streaktest.Alive(2))
sim.ExpectNoEvent("broken")
```

Property-based invariants shipped as a reusable suite: count never negative; a period
earns at most once; tz change never decreases derived count; `derive(settle(s)) ==
derive(s)`; scheduler downtime never changes any derived read.

---

## 8. v1 scope

**In:** day/week/month periods, weekday masks, boundary offset, thresholds, freezes,
repair, targets, milestones, at-risk events with decay, tz change w/ generosity rule,
idempotent record/unrecord, calendar endpoint, outbox + webhooks, scheduler, Go lib +
sidecar Docker image, TS + Python thin clients, streaktest, migration docs.

**Out (v2+):** flex schedules ("any 5 days/week"), friend/shared streaks (Duolingo's
Friend Streak), leaderboards/XP/points (that's gamification-platform scope — staying
*the best streak engine* is the differentiation), dashboards, hosted billing.

**Proof of viability = migrating our own three apps:**
1. **kanji_cards** first (smallest surface, single streak/user): swap `progress.go`
   engine + push scheduler candidate query for embedded streakd; its 408 lines of streak
   tests become acceptance tests for the SDK. Kills the stale-display and dead-streak-nag
   bugs by construction.
2. **addictive_game**: run sidecar, wire `claim-daily` through the Python client; gem
   ladder stays app-side, driven by `extended` events. Finally connects the orphaned
   backend streak to the games.
3. **restreak_server** last (hardest: N streaks, guards, weekday masks, targets, toggles —
   all in-scope features): replaces the 276-line overnight job + 462-line repo; Telegram
   notifications become an `at_risk`/`broken` event consumer.

If the SDK can't cleanly absorb all three, the design is wrong — fix it before any
public release.

**License:** MIT (adoption over control; the moat is execution quality, not the license).

---

## 9. Monetization (honest)

Expected direct revenue: **~zero for the foreseeable future**, and that's fine — the SDK
is already worth building for internal use alone. If OSS traction appears, options in
order of realism: (1) hosted sidecar priced under Trophy's umbrella (their $299/10K MAU
leaves room for $29–49 indie tiers); (2) pro add-ons (dashboard, analytics, experiment
flags for milestone/freeze tuning); (3) GitHub Sponsors/sponsorware. The counterfactual
cost of *not* building it is real: a fourth reimplementation in the next project, plus
the live bugs in the current three.
