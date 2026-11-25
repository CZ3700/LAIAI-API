package leader

import (
	"context"
	"database/sql"
	"sync"
	"time"
)

// PGStore provides PostgreSQL session-level advisory-lock leader election
// using a dedicated connection so the lock persists across heartbeats.
type PGStore struct {
	sqlDB  *sql.DB
	lockID int64

	mu       sync.Mutex
	conn     *sql.Conn
	acquired bool
}

// NewPGStore creates a PostgreSQL advisory lock store for singleton background workers.
// When sqlDB is nil, it falls back to single-instance mode (always leader).
func NewPGStore(sqlDB *sql.DB, lockID int64) *PGStore {
	return &PGStore{sqlDB: sqlDB, lockID: lockID}
}

func (s *PGStore) acquireOrRenew(ctx context.Context, _, _ string, _ time.Duration) (bool, error) {
	if s == nil || s.sqlDB == nil {
		return true, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.conn != nil {
		if err := s.conn.PingContext(ctx); err != nil {
			_ = s.conn.Close()
			s.conn = nil
			s.acquired = false
		}
	}
	if s.conn == nil {
		conn, err := s.sqlDB.Conn(ctx)
		if err != nil {
			return false, err
		}
		s.conn = conn
		s.acquired = false
	}
	if s.acquired {
		return true, nil
	}
	var got bool
	if err := s.conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", s.lockID).Scan(&got); err != nil {
		_ = s.conn.Close()
		s.conn = nil
		return false, err
	}
	s.acquired = got
	return got, nil
}

// Close releases the session advisory lock and returns the dedicated connection.
func (s *PGStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.conn == nil {
		return nil
	}
	if s.acquired {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = s.conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", s.lockID)
		cancel()
		s.acquired = false
	}
	err := s.conn.Close()
	s.conn = nil
	return err
}
