package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Vartent/streakd/internal/core"
)

// Querier is satisfied by *pgxpool.Pool, *pgx.Conn and pgx.Tx.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Subject mirrors streaks.subjects.
type Subject struct {
	ID         int64
	ExternalID string
	Timezone   string
}

// Streak mirrors streaks.streaks; State/Config are the core-typed columns.
type Streak struct {
	ID        int64
	SubjectID int64
	Key       string
	Config    core.Config
	State     core.State
	Status    string
}

var ErrNotFound = errors.New("store: not found")

// dateOrNil converts a core.Date to a DATE parameter (nil for zero).
func dateOrNil(d core.Date) any {
	if d.IsZero() {
		return nil
	}
	return d.Time()
}

func scanDate(t *time.Time) core.Date {
	if t == nil {
		return core.Date{}
	}
	return core.Date{Y: t.Year(), M: t.Month(), D: t.Day()}
}

// UpsertSubject returns the subject, creating it with defaultTZ on first sight.
func UpsertSubject(ctx context.Context, q Querier, appID int64, externalID, defaultTZ string) (Subject, error) {
	var s Subject
	err := q.QueryRow(ctx, `
		INSERT INTO streaks.subjects (app_id, external_id, timezone)
		VALUES ($1, $2, $3)
		ON CONFLICT (app_id, external_id) DO UPDATE SET external_id = EXCLUDED.external_id
		RETURNING id, external_id, timezone
	`, appID, externalID, defaultTZ).Scan(&s.ID, &s.ExternalID, &s.Timezone)
	if err != nil {
		return Subject{}, fmt.Errorf("store: upsert subject: %w", err)
	}
	return s, nil
}

func GetSubject(ctx context.Context, q Querier, appID int64, externalID string) (Subject, error) {
	var s Subject
	err := q.QueryRow(ctx, `
		SELECT id, external_id, timezone FROM streaks.subjects
		WHERE app_id = $1 AND external_id = $2
	`, appID, externalID).Scan(&s.ID, &s.ExternalID, &s.Timezone)
	if errors.Is(err, pgx.ErrNoRows) {
		return Subject{}, ErrNotFound
	}
	if err != nil {
		return Subject{}, fmt.Errorf("store: get subject: %w", err)
	}
	return s, nil
}

func SetSubjectTimezone(ctx context.Context, q Querier, subjectID int64, tz string, at time.Time) error {
	_, err := q.Exec(ctx, `
		UPDATE streaks.subjects SET timezone = $2, tz_changed_at = $3 WHERE id = $1
	`, subjectID, tz, at)
	if err != nil {
		return fmt.Errorf("store: set timezone: %w", err)
	}
	return nil
}

// UpsertStreakLocked returns the streak row locked FOR UPDATE, creating it
// with cfg on first activity. The insert-then-lock dance keeps concurrent
// first-activity calls serialized on the unique index.
func UpsertStreakLocked(ctx context.Context, q Querier, subjectID int64, key string, cfg core.Config) (Streak, error) {
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return Streak{}, fmt.Errorf("store: marshal config: %w", err)
	}
	_, err = q.Exec(ctx, `
		INSERT INTO streaks.streaks (subject_id, key, config, freezes)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (subject_id, key) DO NOTHING
	`, subjectID, key, cfgJSON, cfg.Normalized().Freezes.Initial)
	if err != nil {
		return Streak{}, fmt.Errorf("store: insert streak: %w", err)
	}
	return GetStreakLocked(ctx, q, subjectID, key)
}

// GetStreakLocked loads an existing streak row FOR UPDATE.
func GetStreakLocked(ctx context.Context, q Querier, subjectID int64, key string) (Streak, error) {
	return scanStreak(q.QueryRow(ctx, `
		SELECT id, subject_id, key, config, current_count, longest,
		       last_earned, settled_through, freezes, freeze_progress, status
		FROM streaks.streaks WHERE subject_id = $1 AND key = $2
		FOR UPDATE
	`, subjectID, key))
}

// GetStreak reads without locking (read paths).
func GetStreak(ctx context.Context, q Querier, subjectID int64, key string) (Streak, error) {
	return scanStreak(q.QueryRow(ctx, `
		SELECT id, subject_id, key, config, current_count, longest,
		       last_earned, settled_through, freezes, freeze_progress, status
		FROM streaks.streaks WHERE subject_id = $1 AND key = $2
	`, subjectID, key))
}

func scanStreak(row pgx.Row) (Streak, error) {
	var (
		st             Streak
		cfgJSON        []byte
		lastEarned     *time.Time
		settledThrough *time.Time
	)
	err := row.Scan(&st.ID, &st.SubjectID, &st.Key, &cfgJSON, &st.State.CurrentCount, &st.State.Longest,
		&lastEarned, &settledThrough, &st.State.Freezes, &st.State.FreezeProgress, &st.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return Streak{}, ErrNotFound
	}
	if err != nil {
		return Streak{}, fmt.Errorf("store: scan streak: %w", err)
	}
	if err := json.Unmarshal(cfgJSON, &st.Config); err != nil {
		return Streak{}, fmt.Errorf("store: unmarshal config: %w", err)
	}
	st.Config = st.Config.Normalized()
	st.State.LastEarned = scanDate(lastEarned)
	st.State.SettledThrough = scanDate(settledThrough)
	return st, nil
}

// ListStreaks returns all streaks of a subject (unlocked).
func ListStreaks(ctx context.Context, q Querier, subjectID int64) ([]Streak, error) {
	rows, err := q.Query(ctx, `
		SELECT id, subject_id, key, config, current_count, longest,
		       last_earned, settled_through, freezes, freeze_progress, status
		FROM streaks.streaks WHERE subject_id = $1 ORDER BY key
	`, subjectID)
	if err != nil {
		return nil, fmt.Errorf("store: list streaks: %w", err)
	}
	defer rows.Close()
	var out []Streak
	for rows.Next() {
		st, err := scanStreak(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func SaveStreakState(ctx context.Context, q Querier, streakID int64, s core.State) error {
	_, err := q.Exec(ctx, `
		UPDATE streaks.streaks
		SET current_count = $2, longest = $3, last_earned = $4, settled_through = $5,
		    freezes = $6, freeze_progress = $7, updated_at = now()
		WHERE id = $1
	`, streakID, s.CurrentCount, s.Longest, dateOrNil(s.LastEarned), dateOrNil(s.SettledThrough),
		s.Freezes, s.FreezeProgress)
	if err != nil {
		return fmt.Errorf("store: save streak state: %w", err)
	}
	return nil
}

func UpdateStreakConfig(ctx context.Context, q Querier, streakID int64, cfg core.Config) error {
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("store: marshal config: %w", err)
	}
	if _, err := q.Exec(ctx, `
		UPDATE streaks.streaks SET config = $2, updated_at = now() WHERE id = $1
	`, streakID, cfgJSON); err != nil {
		return fmt.Errorf("store: update config: %w", err)
	}
	return nil
}

// AddMark accumulates activity onto the period ledger row and returns the new
// total amount for the period.
func AddMark(ctx context.Context, q Querier, streakID int64, period core.Date, amount int, tz string, at time.Time) (int, error) {
	var total int
	err := q.QueryRow(ctx, `
		INSERT INTO streaks.period_marks (streak_id, local_period, amount, tz_at_record, first_recorded, last_recorded)
		VALUES ($1, $2, $3, $4, $5, $5)
		ON CONFLICT (streak_id, local_period)
		DO UPDATE SET amount = streaks.period_marks.amount + EXCLUDED.amount, last_recorded = EXCLUDED.last_recorded
		RETURNING amount
	`, streakID, period.Time(), amount, tz, at).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("store: add mark: %w", err)
	}
	return total, nil
}

func GetMarkAmount(ctx context.Context, q Querier, streakID int64, period core.Date) (int, error) {
	var amount int
	err := q.QueryRow(ctx, `
		SELECT amount FROM streaks.period_marks WHERE streak_id = $1 AND local_period = $2
	`, streakID, period.Time()).Scan(&amount)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("store: get mark: %w", err)
	}
	return amount, nil
}

func DeleteMark(ctx context.Context, q Querier, streakID int64, period core.Date) (bool, error) {
	tag, err := q.Exec(ctx, `
		DELETE FROM streaks.period_marks WHERE streak_id = $1 AND local_period = $2
	`, streakID, period.Time())
	if err != nil {
		return false, fmt.Errorf("store: delete mark: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// EarnedPeriods returns the ledger of periods whose amount crossed the
// threshold, ascending — the Replay/Recount input.
func EarnedPeriods(ctx context.Context, q Querier, streakID int64, minAmount int) ([]core.Date, error) {
	rows, err := q.Query(ctx, `
		SELECT local_period FROM streaks.period_marks
		WHERE streak_id = $1 AND amount >= $2 ORDER BY local_period
	`, streakID, minAmount)
	if err != nil {
		return nil, fmt.Errorf("store: earned periods: %w", err)
	}
	defer rows.Close()
	var out []core.Date
	for rows.Next() {
		var t time.Time
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, core.Date{Y: t.Year(), M: t.Month(), D: t.Day()})
	}
	return out, rows.Err()
}

// MarksBetween returns period->amount for calendar rendering.
func MarksBetween(ctx context.Context, q Querier, streakID int64, from, to core.Date) (map[string]int, error) {
	rows, err := q.Query(ctx, `
		SELECT local_period, amount FROM streaks.period_marks
		WHERE streak_id = $1 AND local_period BETWEEN $2 AND $3
	`, streakID, from.Time(), to.Time())
	if err != nil {
		return nil, fmt.Errorf("store: marks between: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var t time.Time
		var amount int
		if err := rows.Scan(&t, &amount); err != nil {
			return nil, err
		}
		out[core.Date{Y: t.Year(), M: t.Month(), D: t.Day()}.String()] = amount
	}
	return out, rows.Err()
}

// InsertEvent appends to the transactional outbox.
func InsertEvent(ctx context.Context, q Querier, appID, streakID int64, subjectExternalID, key string, e core.Event) (int64, error) {
	payload, err := json.Marshal(map[string]any{"period": e.Period.String(), "count": e.Count})
	if err != nil {
		return 0, err
	}
	var id int64
	err = q.QueryRow(ctx, `
		INSERT INTO streaks.events (app_id, streak_id, subject_external_id, streak_key, type, payload)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id
	`, appID, streakID, subjectExternalID, key, string(e.Type), payload).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: insert event: %w", err)
	}
	return id, nil
}

// LookupIdempotency returns the memoized result for key, if any.
func LookupIdempotency(ctx context.Context, q Querier, appID int64, key string) ([]byte, bool, error) {
	var result []byte
	err := q.QueryRow(ctx, `
		SELECT result FROM streaks.idempotency_keys WHERE app_id = $1 AND key = $2
	`, appID, key).Scan(&result)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: lookup idempotency: %w", err)
	}
	return result, true, nil
}

func SaveIdempotency(ctx context.Context, q Querier, appID int64, key string, streakID int64, result []byte, at time.Time) error {
	_, err := q.Exec(ctx, `
		INSERT INTO streaks.idempotency_keys (app_id, key, streak_id, result, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, appID, key, streakID, result, at)
	if err != nil {
		return fmt.Errorf("store: save idempotency: %w", err)
	}
	return nil
}

// ClaimReminder inserts the once-per-local-day claim; false means another
// worker (or an earlier tick) already claimed it.
func ClaimReminder(ctx context.Context, q Querier, streakID int64, day core.Date) (bool, error) {
	tag, err := q.Exec(ctx, `
		INSERT INTO streaks.reminder_claims (streak_id, local_day) VALUES ($1, $2)
		ON CONFLICT DO NOTHING
	`, streakID, day.Time())
	if err != nil {
		return false, fmt.Errorf("store: claim reminder: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}
