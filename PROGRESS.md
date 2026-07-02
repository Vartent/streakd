# PROGRESS

## 2026-07-02 — streakd v0 built, kanji_cards migrated to it

- **Status**:
  - streakd (github.com/Vartent/streakd, pushed to main @ 5c3a461): Phases 0-4+6 of docs/PLAN.md DONE AND VERIFIED. Pure core (period math, Derive/Settle/Apply/Replay oracle, DST matrix, kanji travel cases ported), Postgres store + goose migrations (own `streaks` schema, own version table), transactional engine (Record with FOR UPDATE + idempotency keys, Unrecord via replay, SetTimezone generosity rule, Repair, outbox), scheduler (settle + at_risk with reminder_claims dedup, advisory-lock multi-instance), public root API, streaktest harness. `go test -race ./...` green 4/4 consecutive runs; core coverage 97.4%; 5/5 mutation spot-checks killed; 50-goroutine race test = exactly one earn.
  - kanji_cards backend: cutover to embedded streakd DONE AND VERIFIED live (docker image rebuilt, container restarted). UpdateStreakBy/GetUserStats delegate via models hooks; new GET /api/user/streak; tz header propagates to engine; prefs PUT mirrors reminder into engine (verified live: 21:00 landed in config); legacy scheduler/tests/livetest deleted; users.current_streak columns kept as rollback data, no longer written. Seed migrates legacy columns then Recount()s every streak so state == ledger replay (critic bug fixed; pinned by internal/streak/seed_test.go, PASSES: 20-day streak keeps 2 freezes, mid-lapse 15-day stays alive, dead reads 0). Live API verified: lapsed streak reads 0 (user bug #2 fixed), record → count 1 + loss_at end of tomorrow Moscow, same-day re-record idempotent, /api/user/stats embeds full view + back-compat fields. `time/tzdata` imported in main (alpine image has no tzdata).
  - kanji_cards native UI: DONE, NOT device-tested (user must run app). StreakInfoModal (count/status/freezes/longest/milestone), StreakBadge tappable (covers dictionaries+lessons headers), profile streak card opens modal, StreakProvider refreshes on AppState foreground (client half of midnight-flame bug), i18n keys in en/ru/ja/es/vi/zh. `npx tsc --noEmit` clean; 10 failing jest tests verified PRE-EXISTING at HEAD (expo-notifications mock), none new.
  - NOT DONE (deferred per plan phases): sidecar HTTP/Docker (Phase 5), TS/Python clients (Phase 7), restreak_server + addictive_game migrations (Phase 8b/c), 14-day shadow mode (user requested direct cutover; legacy columns retained instead).
- **Next steps**:
  1. User tests kanji native app: tap streak badge in dictionaries/lessons headers + profile card → modal; verify flame greys out after local midnight and lapsed streak shows 0.
  2. kanji_cards changes are UNCOMMITTED in the working tree (mixed with the user's own pre-existing WIP) — user decides when to commit.
  3. Phase 5 (sidecar + docker image) when user confirms kanji works.
  4. Phase 8b: addictive_game via sidecar; Phase 8c: restreak_server (needs targets/masks/toggle — all engine-supported).
  5. Optional: idempotency-key pruning job (created_at index exists, no cleanup yet).
- **Decisions pinned this session**:
  - Kanji streak policy: daily, milestones [3,7,14,30,50,100,365], freezes earn 1/7 days, max 2, auto-consume, initial 0.
  - Seed must never hand-write derived state: insert config+marks, then Recount (critic-verified).
  - Reminders: engine emits at_risk events; app owns delivery. Engine never talks to push channels.
  - Direct cutover instead of shadow mode (user asked for wire+test now); legacy columns kept unwritten as rollback.
  - Error propagation on streak-engine failure in CompleteSession/UpdateUserTimezone kept (retry converges; critic RISK accepted).
- **Gotchas**:
  - Boundary offsets must be wall-clock arithmetic, not real-duration (DST nights) — fixed in core.PeriodKey, mutation-tested.
  - goose embed needs fs.Sub(migrationsFS, "migrations"); version table streaks.goose_version requires schema pre-created.
  - Both DB-backed streakd test packages share one DB: cross-package pg_advisory_lock in TestMain (id 0x5f7265616b7431) or they flake under parallel `go test ./...`.
  - kanji alpine image has NO tzdata → `_ "time/tzdata"` import in cmd/server/main.go is load-bearing.
  - Go module proxy caches @main; pin by commit with GOPROXY=direct go get github.com/Vartent/streakd@<sha>.
  - Dev DB guard hook blocks DROP SCHEMA (correctly); streaks schema backup at /tmp/streaks_schema_backup_*.sql if ever needed.
  - Ephemeral test Postgres container `streakd-test-pg` runs on port 5599 (streakd + kanji seed tests use it; safe to remove, tests skip without it).
  - kanji dev DB: kanji count is now 2218 (CLAUDE.md says 430 — stale).
  - Test login: test@test.com / password123 (user id 194).
- **Key files**:
  - streakd: internal/core/*.go (semantics), internal/engine/{record,admin,scheduler}.go, internal/store/*, streakd.go, streaktest/, docs/{DESIGN,PLAN}.md.
  - kanji backend: internal/streak/service.go (adapter+seed+recount), internal/models/{streak_hook,progress,user_stats,user,push}.go, internal/services/{training_handler,push_handler}.go, cmd/server/main.go, internal/streak/seed_test.go.
  - kanji native: src/components/{StreakInfoModal,StreakBadge}.tsx, src/app/providers/StreakProvider.tsx, src/screens/profile/ProfileScreen.tsx, src/i18n/locales/*.json.
