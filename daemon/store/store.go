// Package store is the daemon's durable local state, backed by a single SQLite
// database (~/.oculus/oculus.db). It uses the pure-Go modernc.org/sqlite driver so
// the daemon builds and cross-compiles without cgo. SQLite (via libSQL) keeps the
// door open to a Turso embedded-replica later without an SQL rewrite.
//
// Today it persists user-set session names so a rename survives daemon restarts.
package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store is a handle to the daemon's local SQLite database. Its methods are safe for
// concurrent use (database/sql owns an internal connection pool).
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and applies migrations.
func Open(path string) (*Store, error) {
	// _pragma busy_timeout so concurrent writers wait briefly instead of erroring with
	// SQLITE_BUSY; journal_mode=WAL for better read/write concurrency.
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// A local file DB serializes writes anyway; a single connection avoids WAL lock churn.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS session_names (
		id         TEXT PRIMARY KEY,
		name       TEXT NOT NULL,
		updated_at INTEGER NOT NULL
	)`)
	return err
}

// Close releases the database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// SetName persists a user-set name for a session. A blank name clears the row so the
// session reverts to its derived title (mirrors the in-memory rename semantics).
func (s *Store) SetName(id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		_, err := s.db.Exec(`DELETE FROM session_names WHERE id = ?`, id)
		return err
	}
	_, err := s.db.Exec(
		`INSERT INTO session_names(id, name, updated_at) VALUES(?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET name = excluded.name, updated_at = excluded.updated_at`,
		id, name, time.Now().Unix(),
	)
	return err
}

// Name returns the persisted name for a session, if one was set.
func (s *Store) Name(id string) (string, bool) {
	var name string
	err := s.db.QueryRow(`SELECT name FROM session_names WHERE id = ?`, id).Scan(&name)
	if err != nil {
		return "", false
	}
	return name, name != ""
}

// Names returns every persisted session name keyed by session id.
func (s *Store) Names() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT id, name FROM session_names`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}
