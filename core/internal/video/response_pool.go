package video

import (
	"context"
	"database/sql"
	"errors"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const responseSocketTimeout = 2 * time.Second

var errResponsePoolConfiguration = errors.New("video response pool configuration unavailable")

type responseDriverLogger struct{}

func (responseDriverLogger) Print(...any) {}

// Clone the validated primary identity. Never build a second DSN from pieces,
// use a replica, or change the shared runtime/migration connection settings.
func responsePoolConfig(primaryDSN string) (*drivermysql.Config, error) {
	cfg, err := drivermysql.ParseDSN(primaryDSN)
	if err != nil || cfg.DBName == "" || !cfg.ParseTime {
		return nil, errResponsePoolConfiguration
	}
	out := cfg.Clone()
	out.Timeout = responseSocketTimeout
	out.ReadTimeout = responseSocketTimeout
	out.WriteTimeout = responseSocketTimeout
	out.Logger = responseDriverLogger{}
	return out, nil
}

// ConfigureResponseInbox is a startup-only operation. The pool is available
// even when new registrations are disabled; disabling the write switch must
// not discard evidence captured by an already authorized producer.
func (s *Service) ConfigureResponseInbox(primaryDSN string, enabled bool) error {
	if s == nil || s.DB == nil || s.DB.Statement == nil || s.DB.Dialector == nil || s.DB.Dialector.Name() != "mysql" || s.responseDB != nil {
		return errResponsePoolConfiguration
	}
	if _, ok := s.DB.Statement.ConnPool.(gorm.TxCommitter); ok {
		return errResponsePoolConfiguration
	}
	primary, ok := s.DB.Dialector.(*gormmysql.Dialector)
	supplied, parseErr := drivermysql.ParseDSN(primaryDSN)
	if !ok || primary.Config == nil || primary.DSNConfig == nil || parseErr != nil || primary.DSNConfig.FormatDSN() != supplied.FormatDSN() {
		return errResponsePoolConfiguration
	}
	cfg, err := responsePoolConfig(primaryDSN)
	if err != nil {
		return err
	}
	connector, err := drivermysql.NewConnector(cfg)
	if err != nil {
		return errResponsePoolConfiguration
	}
	pool := sql.OpenDB(connector)
	pool.SetMaxOpenConns(2)
	pool.SetMaxIdleConns(1)
	pool.SetConnMaxLifetime(5 * time.Minute)
	// GORM's automatic version read/ping uses Background. Avoid both and make
	// the only startup network probe explicitly bounded and safe to report.
	db, err := gorm.Open(gormmysql.New(gormmysql.Config{Conn: pool, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Discard})
	if err != nil {
		_ = pool.Close()
		return errResponsePoolConfiguration
	}
	ctx, cancel := context.WithTimeout(context.Background(), responseSocketTimeout)
	defer cancel()
	if err := pool.PingContext(ctx); err != nil {
		_ = pool.Close()
		return errResponsePoolConfiguration
	}
	s.responseDB = db
	s.ResponseInboxEnabled = enabled
	return nil
}

// Only call after all users of the dedicated pool have drained. Closing it
// never closes the primary DB, and it never clears the dispatch pause latch.
func (s *Service) CloseResponseInbox() error {
	if s == nil || s.responseDB == nil {
		return nil
	}
	pool, err := s.responseDB.DB()
	if err != nil {
		return errResponsePoolConfiguration
	}
	if err := pool.Close(); err != nil {
		return errResponsePoolConfiguration
	}
	return nil
}
