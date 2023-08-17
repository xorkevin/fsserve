package db

import (
	"context"
	"database/sql"
	"errors"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
	"xorkevin.dev/forge/model/sqldb"
	"xorkevin.dev/kerrors"
	"xorkevin.dev/klog"
)

type (
	SQLClient struct {
		log    *klog.LevelLogger
		dsn    string
		client *sql.DB
	}

	sqlrows struct {
		log  *klog.LevelLogger
		ctx  context.Context
		rows *sql.Rows
	}

	sqlrow struct {
		row *sql.Row
	}
)

var (
	// ErrConn is returned on a db connection error
	ErrConn errConn
	// ErrClient is returned for unknown client errors
	ErrClient errClient
	// ErrNotFound is returned when a row is not found
	ErrNotFound errNotFound
	// ErrUnique is returned when a unique constraint is violated
	ErrUnique errUnique
)

type (
	errConn     struct{}
	errClient   struct{}
	errNotFound struct{}
	errUnique   struct{}
)

func (e errConn) Error() string {
	return "DB connection error"
}

func (e errClient) Error() string {
	return "DB client error"
}

func (e errNotFound) Error() string {
	return "Row not found"
}

func (e errUnique) Error() string {
	return "Unique constraint violated"
}

func errWithKind(err error, kind error, msg string) error {
	return kerrors.New(kerrors.OptInner(err), kerrors.OptKind(ErrNotFound), kerrors.OptMsg("Not found"), kerrors.OptSkip(2))
}

func wrapDBErr(err error, fallbackmsg string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return errWithKind(err, ErrNotFound, "Not found")
	}
	var perr *sqlite.Error
	if errors.As(err, &perr) {
		switch perr.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE:
			return errWithKind(err, ErrUnique, "Unique constraint violated")
		}
	}
	return errWithKind(err, nil, fallbackmsg)
}

func NewSQLClient(log klog.Logger, dsn string) *SQLClient {
	return &SQLClient{
		log: klog.NewLevelLogger(log),
		dsn: dsn,
	}
}

func (s *SQLClient) Init() error {
	client, err := sql.Open("sqlite", s.dsn)
	if err != nil {
		return kerrors.WithMsg(err, "Failed creating sqlite db client")
	}
	s.client = client
	return nil
}

// ExecContext implements [sqldb.Executor]
func (s *SQLClient) ExecContext(ctx context.Context, query string, args ...interface{}) (sqldb.Result, error) {
	r, err := s.client.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, wrapDBErr(err, "Failed executing command")
	}
	return r, nil
}

// QueryContext implements [sqldb.Executor]
func (s *SQLClient) QueryContext(ctx context.Context, query string, args ...interface{}) (sqldb.Rows, error) {
	rows, err := s.client.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapDBErr(err, "Failed executing query")
	}
	return &sqlrows{
		log:  s.log,
		ctx:  klog.ExtendCtx(context.Background(), ctx),
		rows: rows,
	}, nil
}

// QueryRowContext implements [sqldb.Executor]
func (s *SQLClient) QueryRowContext(ctx context.Context, query string, args ...interface{}) sqldb.Row {
	return &sqlrow{
		row: s.client.QueryRowContext(ctx, query, args...),
	}
}

// PingContext pings the db
func (s *SQLClient) PingContext(ctx context.Context) error {
	if err := s.client.PingContext(ctx); err != nil {
		return wrapDBErr(err, "Failed to ping db")
	}
	return nil
}

// Close closes the db client
func (s *SQLClient) Close() error {
	if err := s.client.Close(); err != nil {
		return wrapDBErr(err, "Failed to close db client")
	}
	return nil
}

// Next implements [sqldb.Rows]
func (r *sqlrows) Next() bool {
	return r.rows.Next()
}

// Scan implements [sqldb.Rows]
func (r *sqlrows) Scan(dest ...interface{}) error {
	if err := r.rows.Scan(dest...); err != nil {
		return wrapDBErr(err, "Failed scanning row")
	}
	return nil
}

// Err implements [sqldb.Rows]
func (r *sqlrows) Err() error {
	if err := r.rows.Err(); err != nil {
		return wrapDBErr(err, "Failed iterating rows")
	}
	return nil
}

// Close implements [sqldb.Rows]
func (r *sqlrows) Close() error {
	if err := r.rows.Close(); err != nil {
		err := wrapDBErr(err, "Failed closing rows")
		r.log.Err(r.ctx, kerrors.WithMsg(err, "Failed closing rows"))
		return err
	}
	return nil
}

// Scan implements [sqldb.Row]
func (r *sqlrow) Scan(dest ...interface{}) error {
	if err := r.row.Scan(dest...); err != nil {
		return wrapDBErr(err, "Failed scanning row")
	}
	return nil
}

// Err implements [sqldb.Row]
func (r *sqlrow) Err() error {
	if err := r.row.Err(); err != nil {
		return wrapDBErr(err, "Failed executing query")
	}
	return nil
}
