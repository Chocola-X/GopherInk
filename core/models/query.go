package models

import (
	"strings"

	"github.com/Chocola-X/GopherInk/pkg/sqlutil"
)

type Dialect string

const (
	DialectSQLite   Dialect = "sqlite"
	DialectMySQL    Dialect = "mysql"
	DialectPostgres Dialect = "postgres"
)

func NormalizeDialect(driver string) Dialect {
	switch strings.ToLower(driver) {
	case "mysql", "mariadb":
		return DialectMySQL
	case "postgres", "postgresql", "pgx":
		return DialectPostgres
	default:
		return DialectSQLite
	}
}

func Rebind(d Dialect, query string) string {
	return sqlutil.Rebind(string(d), query)
}

func UpsertOptionSQL(d Dialect) string {
	if d == DialectMySQL {
		return `INSERT INTO gb_options (name, user, value) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE value = VALUES(value)`
	}
	if d == DialectPostgres {
		return `INSERT INTO gb_options (name, "user", value) VALUES ($1, $2, $3) ON CONFLICT(name, "user") DO UPDATE SET value = EXCLUDED.value`
	}
	return `INSERT INTO gb_options (name, user, value) VALUES (?, ?, ?) ON CONFLICT(name, user) DO UPDATE SET value = excluded.value`
}

func LimitOffset(d Dialect, limit, offset int) (string, []any) {
	if limit <= 0 {
		return "", nil
	}
	return " LIMIT ? OFFSET ?", []any{limit, offset}
}
