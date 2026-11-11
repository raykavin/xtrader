// Package store keeps the settings and the data of xtrader in PostgreSQL: the
// users of the dashboard, the prompts, the agents with their accounts and
// language models, Telegram, and the orders, decisions and journal of each
// agent. Only the controller uses it, the agents going through its API.
//
// The schema is the one of sql/migrations, applied by cmd/migrator, never by
// GORM: the store only checks its version (see CheckSchema). The secrets, the
// API keys and the tokens, are encrypted with the key of config.yml, each one
// sealed for its place (see seal).
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	glog "gorm.io/gorm/logger"

	gormbox "github.com/raykavin/gobox/database/gorm"
	"github.com/raykavin/gobox/secure"

	"github.com/raykavin/xtrader/config"
	"github.com/raykavin/xtrader/tools/log"
)

// SchemaVersion is the version of sql/migrations the store needs.
const SchemaVersion = 47

var (
	// ErrNotFound is the error of a record that doesn't exist.
	ErrNotFound = errors.New("store: not found")
	// ErrConflict is the error of a record whose name is taken.
	ErrConflict = errors.New("store: already exists")
	// ErrInvalid is the error of a record refused, eg: a name out of the rules.
	ErrInvalid = errors.New("store: invalid")
	// ErrSchema is the error of a database without the schema of the store.
	ErrSchema = errors.New("store: schema out of date")
)

// Store is the database of xtrader.
type Store struct {
	db     *gorm.DB
	cipher secure.Encryptor
}

// Open connects to the database of cfg, the secrets encrypted with key.
func Open(cfg config.Database, key []byte) (*Store, error) {
	cipher, err := secure.NewAESGCMEncryptor(key)
	if err != nil {
		return nil, err
	}
	options := gormbox.DefaultGormConfig()
	options.DSN = cfg.DSN
	options.Dialector = "postgres"
	options.MaxOpenConns = cfg.MaxOpenConns
	options.MaxIdleConns = cfg.MaxIdleConns
	options.ConnMaxLifetime = cfg.ConnMaxLifetime
	options.GormConfig = &gorm.Config{
		// the values of the queries left out of the log: the secrets and the
		// password hashes are values
		Logger: glog.New(gormWriter{}, glog.Config{SlowThreshold: 500 * time.Millisecond,
			LogLevel: gormbox.ParseLoggerLevel(cfg.LogLevel), IgnoreRecordNotFoundError: true,
			ParameterizedQueries: true}),
		SkipDefaultTransaction: true,
		PrepareStmt:            true,
		TranslateError:         true,
		// PostgreSQL keeps microseconds
		NowFunc: func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) },
	}
	db, err := gormbox.New(options)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	return &Store{db: db, cipher: cipher}, nil
}

// Close closes the connections to the database.
func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// Ping checks the connection to the database.
func (s *Store) Ping(ctx context.Context) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

// CheckSchema fails unless the migrations applied to the database are
// SchemaVersion at least, and none of them stopped halfway (dirty): the
// controller never migrates, cmd/migrator does.
func (s *Store) CheckSchema(ctx context.Context) error {
	var row struct {
		Version int64
		Dirty   bool
	}
	err := s.db.WithContext(ctx).Raw("SELECT version, dirty FROM schema_migrations LIMIT 1").Scan(&row).Error
	switch {
	case err != nil:
		return fmt.Errorf("%w: no migration applied (%v): run cmd/migrator up", ErrSchema, err)
	case row.Dirty:
		return fmt.Errorf("%w: the migration %d stopped halfway: fix it by hand, then run cmd/migrator up",
			ErrSchema, row.Version)
	case row.Version < SchemaVersion:
		return fmt.Errorf("%w: version %d, %d needed: run cmd/migrator up", ErrSchema, row.Version, SchemaVersion)
	}
	return nil
}

// txKey is the key of the transaction in the context, see Transaction.
type txKey struct{}

// Transaction runs fn in a transaction, carried by its context: the methods of
// the store called with it take part in it. A transaction already in the
// context is joined.
func (s *Store) Transaction(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, ok := ctx.Value(txKey{}).(*gorm.DB); ok {
		return fn(ctx)
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(context.WithValue(ctx, txKey{}, tx))
	})
}

// conn returns the transaction of ctx, else the connection.
func (s *Store) conn(ctx context.Context) *gorm.DB {
	if tx, ok := ctx.Value(txKey{}).(*gorm.DB); ok {
		return tx
	}
	return s.db.WithContext(ctx)
}

// notFound turns gorm.ErrRecordNotFound into ErrNotFound, of what, a
// duplicate key into ErrConflict and a check of its table into ErrInvalid:
// what is the record as the dashboard tells it, singular, eg: "o agente gold",
// its detail a sentence, eg: "o agente gold não existe".
func notFound(err error, what string) error {
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return fmt.Errorf("%w: %s não existe", ErrNotFound, what)
	case errors.Is(err, gorm.ErrDuplicatedKey):
		return fmt.Errorf("%w: %s já existe", ErrConflict, what)
	case errors.Is(err, gorm.ErrCheckConstraintViolated):
		return fmt.Errorf("%w: %s tem um valor inválido", ErrInvalid, what)
	}
	return err
}

// notSeeded is notFound for a row the seeds create, eg: the settings of the
// news, telling how to create it.
func notSeeded(err error, what string) error {
	err = notFound(err, what)
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) || errors.Is(err, ErrInvalid) {
		return fmt.Errorf("%w: rode o cmd/migrator up, que aplica os dados iniciais", err)
	}
	return err
}

// gormWriter writes the log of GORM, the slow queries and the errors, to the
// log of xtrader.
type gormWriter struct{}

func (gormWriter) Printf(format string, args ...interface{}) {
	log.Warnf("[DB] "+format, args...)
}

var _ glog.Writer = gormWriter{}
