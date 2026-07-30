package services

import (
	"context"
	"database/sql"

	"github.com/Chocola-X/GopherInk/core/models"
)

type writerContextKey struct{}

func WithWriter(ctx context.Context) context.Context {
	return context.WithValue(ctx, writerContextKey{}, true)
}

type DB interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	PingContext(context.Context) error
	Dialect() models.Dialect
	RawWriter() *sql.DB
}

var (
	_ DB = (*SQLDB)(nil)
	_ DB = (*DBRouter)(nil)
)

type SQLDB struct {
	db      *sql.DB
	dialect models.Dialect
}

func NewSQLDB(db *sql.DB, driver string) *SQLDB {
	return &SQLDB{db: db, dialect: models.NormalizeDialect(driver)}
}

func (d *SQLDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.db.QueryContext(ctx, models.Rebind(d.dialect, query), args...)
}

func (d *SQLDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.db.QueryRowContext(ctx, models.Rebind(d.dialect, query), args...)
}

func (d *SQLDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.db.ExecContext(ctx, models.Rebind(d.dialect, query), args...)
}

func (d *SQLDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return d.db.BeginTx(ctx, opts)
}

func (d *SQLDB) PingContext(ctx context.Context) error {
	return d.db.PingContext(ctx)
}

func (d *SQLDB) Dialect() models.Dialect {
	return d.dialect
}

func (d *SQLDB) RawWriter() *sql.DB {
	return d.db
}

type DBRouter struct {
	Writer  *sql.DB
	Reader  *sql.DB
	dialect models.Dialect
}

func NewDBRouter(writer, reader *sql.DB, driver ...string) *DBRouter {
	if reader == nil {
		reader = writer
	}
	dialect := models.DialectSQLite
	if len(driver) > 0 {
		dialect = models.NormalizeDialect(driver[0])
	}
	return &DBRouter{Writer: writer, Reader: reader, dialect: dialect}
}

func (r *DBRouter) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if force, _ := ctx.Value(writerContextKey{}).(bool); force {
		return r.writer().QueryContext(ctx, models.Rebind(r.dialect, query), args...)
	}
	return r.reader().QueryContext(ctx, models.Rebind(r.dialect, query), args...)
}

func (r *DBRouter) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if force, _ := ctx.Value(writerContextKey{}).(bool); force {
		return r.writer().QueryRowContext(ctx, models.Rebind(r.dialect, query), args...)
	}
	return r.reader().QueryRowContext(ctx, models.Rebind(r.dialect, query), args...)
}

func (r *DBRouter) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return r.writer().ExecContext(ctx, models.Rebind(r.dialect, query), args...)
}

func (r *DBRouter) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return r.writer().BeginTx(ctx, opts)
}

func (r *DBRouter) PingContext(ctx context.Context) error {
	if err := r.writer().PingContext(ctx); err != nil {
		return err
	}
	if r.Reader != nil && r.Reader != r.Writer {
		return r.Reader.PingContext(ctx)
	}
	return nil
}

func (r *DBRouter) Dialect() models.Dialect {
	return r.dialect
}

func (r *DBRouter) RawWriter() *sql.DB {
	return r.writer()
}

func (r *DBRouter) reader() *sql.DB {
	if r != nil && r.Reader != nil {
		return r.Reader
	}
	return r.writer()
}

func (r *DBRouter) writer() *sql.DB {
	if r != nil && r.Writer != nil {
		return r.Writer
	}
	return nil
}
