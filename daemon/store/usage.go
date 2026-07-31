package store

import "time"

// Durable usage history.
//
// The live meter only ever summed the sessions currently in memory, so the moment a session ended
// its spend vanished — there was no "today", no "this week", and no way to see what a finished job
// had actually cost. These rows are the record that makes those questions answerable.
//
// One row per usage event, not a running total per session: totals can be derived from rows, but
// rows cannot be recovered from a total, and the per-model breakdown depends on having them.

// UsageEvent is one increment of token/cost usage.
type UsageEvent struct {
	TS        int64
	SessionID string
	Provider  string
	Model     string
	ProjectID string
	InTokens  int
	OutTokens int
	CostUSD   float64
}

// UsageBucket is usage aggregated over some grouping (a provider, a model, a day).
type UsageBucket struct {
	Key       string  `json:"key"`
	InTokens  int     `json:"input_tokens"`
	OutTokens int     `json:"output_tokens"`
	CostUSD   float64 `json:"cost_usd"`
	Events    int     `json:"events"`
}

func (s *Store) initUsage() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS usage_events (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		ts         INTEGER NOT NULL,
		session_id TEXT NOT NULL,
		provider   TEXT NOT NULL,
		model      TEXT,
		project_id TEXT,
		in_tokens  INTEGER NOT NULL DEFAULT 0,
		out_tokens INTEGER NOT NULL DEFAULT 0,
		cost_usd   REAL NOT NULL DEFAULT 0
	)`); err != nil {
		return err
	}
	// Every query here is "usage since a timestamp", so that's the index.
	_, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS usage_ts ON usage_events(ts)`)
	return err
}

// AppendUsage records one increment. Zero-valued events are dropped: providers emit usage frames
// that carry only a cost update or only tokens, and rows with nothing in them would inflate the
// event counts without adding information.
func (s *Store) AppendUsage(e UsageEvent) error {
	if s == nil || s.db == nil {
		return nil
	}
	if e.InTokens == 0 && e.OutTokens == 0 && e.CostUSD == 0 {
		return nil
	}
	if e.TS == 0 {
		e.TS = time.Now().Unix()
	}
	_, err := s.db.Exec(
		`INSERT INTO usage_events (ts, session_id, provider, model, project_id, in_tokens, out_tokens, cost_usd)
		 VALUES (?,?,?,?,?,?,?,?)`,
		e.TS, e.SessionID, e.Provider, e.Model, e.ProjectID, e.InTokens, e.OutTokens, e.CostUSD,
	)
	return err
}

// UsageSince aggregates usage from a timestamp, grouped by the given column.
// groupBy must be one of provider, model, session_id, project_id — it is interpolated into SQL, so
// it is validated against that list rather than taken from the caller verbatim.
func (s *Store) UsageSince(since int64, groupBy string) ([]UsageBucket, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	switch groupBy {
	case "provider", "model", "session_id", "project_id":
	default:
		groupBy = "provider"
	}
	rows, err := s.db.Query(
		`SELECT COALESCE(`+groupBy+`, ''), SUM(in_tokens), SUM(out_tokens), SUM(cost_usd), COUNT(*)
		 FROM usage_events WHERE ts >= ?
		 GROUP BY `+groupBy+` ORDER BY SUM(cost_usd) DESC`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageBucket
	for rows.Next() {
		var b UsageBucket
		if err := rows.Scan(&b.Key, &b.InTokens, &b.OutTokens, &b.CostUSD, &b.Events); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// UsageTotal sums everything since a timestamp.
func (s *Store) UsageTotal(since int64) (UsageBucket, error) {
	if s == nil || s.db == nil {
		return UsageBucket{}, nil
	}
	var b UsageBucket
	err := s.db.QueryRow(
		`SELECT COALESCE(SUM(in_tokens),0), COALESCE(SUM(out_tokens),0), COALESCE(SUM(cost_usd),0), COUNT(*)
		 FROM usage_events WHERE ts >= ?`, since,
	).Scan(&b.InTokens, &b.OutTokens, &b.CostUSD, &b.Events)
	return b, err
}

// FirstUsageSince returns the timestamp of the earliest usage at or after `since` (0 = none).
//
// This is what anchors a rolling window: Claude's subscription limits reset on a window that starts
// with your FIRST activity, not on the clock hour, so the reset time can only be derived from when
// the usage actually began.
func (s *Store) FirstUsageSince(since int64) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	var ts int64
	err := s.db.QueryRow(`SELECT COALESCE(MIN(ts),0) FROM usage_events WHERE ts >= ?`, since).Scan(&ts)
	return ts, err
}

// PruneUsage drops rows older than a cutoff, so the table doesn't grow without bound.
func (s *Store) PruneUsage(before int64) error {
	if s == nil || s.db == nil {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM usage_events WHERE ts < ?`, before)
	return err
}
