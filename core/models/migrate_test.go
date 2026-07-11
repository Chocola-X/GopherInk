package models

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestRunVersionedMigrationsReturnsUnexpectedErrors(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE gb_options (name varchar(64) NOT NULL, user int(10) NOT NULL default '0', value text, PRIMARY KEY (name, user))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO gb_options (name, user, value) VALUES ('schema_version', 0, '0')`); err != nil {
		t.Fatal(err)
	}
	if err := RunVersionedMigrations(ctx, db); err == nil {
		t.Fatal("expected migration error when content/user tables are missing")
	}
}

func TestPostgresSchemaSmoke(t *testing.T) {
	stmts := postgresSchema()
	if len(stmts) == 0 {
		t.Fatal("postgres schema is empty")
	}
	joined := strings.Join(stmts, "\n")
	for _, want := range []string{"bigserial PRIMARY KEY", "CREATE TABLE IF NOT EXISTS gb_contents", "CREATE TABLE IF NOT EXISTS gb_comments", "CREATE TABLE IF NOT EXISTS gb_options"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("postgres schema missing %q", want)
		}
	}
	if strings.Contains(joined, "AUTOINCREMENT") || strings.Contains(joined, "AUTO_INCREMENT") {
		t.Fatal("postgres schema should not contain sqlite/mysql autoincrement syntax")
	}
}

func TestMigrateV6AddsDefaultPageHTMLSuffix(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE gb_options (name varchar(64) NOT NULL, user int(10) NOT NULL default '0', value text, PRIMARY KEY (name, user))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO gb_options (name, user, value) VALUES ('schema_version', 0, '5'), ('permalink_page', 0, '/page/{slug}')`); err != nil {
		t.Fatal(err)
	}
	if err := RunVersionedMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRowContext(ctx, `SELECT value FROM gb_options WHERE name = 'permalink_page' AND user = 0`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "/page/{slug}.html" {
		t.Fatalf("permalink_page = %q, want /page/{slug}.html", got)
	}

	if _, err := db.ExecContext(ctx, `UPDATE gb_options SET value = '/docs/{slug}' WHERE name = 'permalink_page'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE gb_options SET value = '5' WHERE name = 'schema_version'`); err != nil {
		t.Fatal(err)
	}
	if err := RunVersionedMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT value FROM gb_options WHERE name = 'permalink_page' AND user = 0`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "/docs/{slug}" {
		t.Fatalf("custom permalink_page = %q, want /docs/{slug}", got)
	}
}

func TestQueryHelperRebindsPostgresPlaceholders(t *testing.T) {
	query := Rebind(DialectPostgres, `SELECT * FROM gb_options WHERE name = ? AND user = ?`)
	if query != `SELECT * FROM gb_options WHERE name = $1 AND user = $2` {
		t.Fatalf("rebind = %q", query)
	}
	if got := Rebind(DialectMySQL, `SELECT ?`); got != `SELECT ?` {
		t.Fatalf("mysql rebind = %q", got)
	}
}
