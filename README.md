# streakd

An open-source streak engine: timezone-correct day boundaries, freezes, repair,
at-risk reminder events — the retention feature you keep reimplementing, done once.

Status: **pre-alpha**. See [docs/DESIGN.md](docs/DESIGN.md) and
[docs/PLAN.md](docs/PLAN.md).

## Modes

- **Embedded** — import the Go library, it owns a `streaks` schema in your
  existing Postgres. No new infrastructure.
- **Sidecar** — run the same engine as a container with an HTTP API for any stack.
  (Planned, Phase 5.)

## Why

Streak logic looks trivial and is not: per-user IANA timezones, DST, travel,
idempotent recording under concurrency, freeze consumption, lazy expiry that never
shows stale counts, and reminders that do not nag about already-dead streaks.
streakd's state is a pure function of an append-only ledger — a dead scheduler can
delay a notification but can never corrupt a streak.

## License

MIT
