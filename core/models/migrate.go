package models

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const CurrentSchemaVersion = 5

func Migrate(ctx context.Context, db *sql.DB, driver string) error {
	var stmts []string
	switch driver {
	case "mysql", "mariadb":
		stmts = mysqlSchema()
	case "postgres", "postgresql", "pgx":
		stmts = postgresSchema()
	default:
		stmts = sqliteSchema()
	}

	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	if driver == "postgres" || driver == "postgresql" || driver == "pgx" {
		return setSchemaVersion(ctx, db, CurrentSchemaVersion)
	}
	return RunVersionedMigrations(ctx, db)
}

func ensureColumn(ctx context.Context, db *sql.DB, stmt string) error {
	_, err := db.ExecContext(ctx, stmt)
	if err == nil || isDuplicateColumnError(err) {
		return nil
	}
	return err
}

func isDuplicateColumnError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "duplicate column") ||
		strings.Contains(message, "duplicate column name") ||
		strings.Contains(message, "column already exists")
}

func RunVersionedMigrations(ctx context.Context, db *sql.DB) error {
	version, err := schemaVersion(ctx, db)
	if err != nil {
		return err
	}
	migrations := []struct {
		Version int
		Run     func(context.Context, *sql.DB) error
	}{
		{Version: 1, Run: migrateV1},
		{Version: 2, Run: migrateV2},
		{Version: 3, Run: migrateV3},
		{Version: 4, Run: migrateV4},
		{Version: 5, Run: migrateV5},
	}
	for _, migration := range migrations {
		if version >= migration.Version {
			continue
		}
		if err := migration.Run(ctx, db); err != nil {
			return fmt.Errorf("schema migration %d: %w", migration.Version, err)
		}
		if err := setSchemaVersion(ctx, db, migration.Version); err != nil {
			return err
		}
		version = migration.Version
	}
	if version == 0 {
		return setSchemaVersion(ctx, db, CurrentSchemaVersion)
	}
	return nil
}

func migrateV1(ctx context.Context, db *sql.DB) error {
	if err := ensureColumn(ctx, db, `ALTER TABLE gb_contents ADD COLUMN sortOrder int(10) default '0'`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, `ALTER TABLE gb_users ADD COLUMN role varchar(16) default 'visitor'`); err != nil {
		return err
	}
	return nil
}

func migrateV2(ctx context.Context, db *sql.DB) error {
	if err := ensureColumn(ctx, db, `ALTER TABLE gb_contents ADD COLUMN draftOf int(10) default '0'`); err != nil {
		return err
	}
	// Create index for draftOf column (ignore errors if index already exists)
	_, _ = db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS gb_contents_draftOf ON gb_contents (draftOf)`)
	return nil
}

func migrateV3(ctx context.Context, db *sql.DB) error {
	return replaceContentSlugIndex(ctx, db)
}

func migrateV4(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `UPDATE gb_options SET value = ? WHERE name = ? AND user = 0 AND value = ?`, "/post/{slug}.html", "permalink_post", "/post/{slug}")
	if err == nil {
		return nil
	}
	_, err = db.ExecContext(ctx, `UPDATE gb_options SET value = $1 WHERE name = $2 AND "user" = 0 AND value = $3`, "/post/{slug}.html", "permalink_post", "/post/{slug}")
	return err
}

func migrateV5(ctx context.Context, db *sql.DB) error {
	if err := ensureColumn(ctx, db, `ALTER TABLE gb_contents ADD COLUMN slugId int(10) default '0'`); err != nil {
		return err
	}
	_, _ = db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS gb_contents_slugId ON gb_contents (slugId)`)
	return initializeContentSlugIDs(ctx, db)
}

func initializeContentSlugIDs(ctx context.Context, db *sql.DB) error {
	for _, typ := range []string{"post", "page"} {
		if err := initializeContentSlugIDsForType(ctx, db, typ); err != nil {
			return err
		}
	}
	return nil
}

type contentSlugSeed struct {
	CID     int64
	Slug    string
	Status  string
	DraftOf int64
	SlugID  int64
}

func initializeContentSlugIDsForType(ctx context.Context, db *sql.DB, typ string) error {
	rows, err := db.QueryContext(ctx, `SELECT cid, COALESCE(slug,''), status, COALESCE(draftOf,0), COALESCE(slugId,0) FROM gb_contents WHERE type = ? ORDER BY cid ASC`, typ)
	if err != nil {
		rows, err = db.QueryContext(ctx, `SELECT cid, COALESCE(slug,''), status, COALESCE(draftOf,0), COALESCE(slugId,0) FROM gb_contents WHERE type = $1 ORDER BY cid ASC`, typ)
	}
	if err != nil {
		return err
	}
	var items []contentSlugSeed
	for rows.Next() {
		var item contentSlugSeed
		if err := rows.Scan(&item.CID, &item.Slug, &item.Status, &item.DraftOf, &item.SlugID); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	nextID := int64(1)
	byCID := map[int64]int64{}
	publishedNext := int64(1)
	for _, item := range items {
		if item.DraftOf != 0 {
			continue
		}
		slugID := item.SlugID
		if slugID <= 0 {
			slugID = nextID
			nextID++
		} else if slugID >= nextID {
			nextID = slugID + 1
		}
		byCID[item.CID] = slugID
		if err := updateContentSlugID(ctx, db, item.CID, slugID); err != nil {
			return err
		}
		if looksGeneratedFallbackSlug(item.Slug) {
			if err := updateContentSlug(ctx, db, item.CID, ""); err != nil {
				return err
			}
		}
		if item.Status == "publish" && slugID >= publishedNext {
			publishedNext = slugID + 1
		}
	}
	for _, item := range items {
		if item.DraftOf == 0 {
			continue
		}
		slugID := byCID[item.DraftOf]
		if slugID <= 0 {
			slugID = nextID
			nextID++
		}
		if err := updateContentSlugID(ctx, db, item.CID, slugID); err != nil {
			return err
		}
		if looksGeneratedFallbackSlug(item.Slug) {
			if err := updateContentSlug(ctx, db, item.CID, ""); err != nil {
				return err
			}
		}
	}
	return setOption(ctx, db, "content_slug_id_next_"+typ, strconv.FormatInt(publishedNext, 10))
}

func updateContentSlugID(ctx context.Context, db *sql.DB, cid, slugID int64) error {
	if _, err := db.ExecContext(ctx, `UPDATE gb_contents SET slugId = ? WHERE cid = ?`, slugID, cid); err == nil {
		return nil
	}
	_, err := db.ExecContext(ctx, `UPDATE gb_contents SET slugId = $1 WHERE cid = $2`, slugID, cid)
	return err
}

func updateContentSlug(ctx context.Context, db *sql.DB, cid int64, slug string) error {
	if _, err := db.ExecContext(ctx, `UPDATE gb_contents SET slug = ? WHERE cid = ?`, slug, cid); err == nil {
		return nil
	}
	_, err := db.ExecContext(ctx, `UPDATE gb_contents SET slug = $1 WHERE cid = $2`, slug, cid)
	return err
}

func setOption(ctx context.Context, db *sql.DB, name, value string) error {
	if _, err := db.ExecContext(ctx, `
		INSERT INTO gb_options (name, user, value) VALUES (?, 0, ?)
		ON CONFLICT(name, user) DO UPDATE SET value = excluded.value
	`, name, value); err == nil {
		return nil
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO gb_options (name, user, value) VALUES (?, 0, ?)
		ON DUPLICATE KEY UPDATE value = VALUES(value)
	`, name, value); err == nil {
		return nil
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO gb_options (name, "user", value) VALUES ($1, 0, $2)
		ON CONFLICT(name, "user") DO UPDATE SET value = EXCLUDED.value
	`, name, value)
	return err
}

func looksGeneratedFallbackSlug(value string) bool {
	if value == "post" {
		return true
	}
	if !strings.HasPrefix(value, "post-") {
		return false
	}
	_, err := strconv.Atoi(strings.TrimPrefix(value, "post-"))
	return err == nil
}

func replaceContentSlugIndex(ctx context.Context, db *sql.DB) error {
	_, _ = db.ExecContext(ctx, `DROP INDEX IF EXISTS gb_contents_slug`)
	_, _ = db.ExecContext(ctx, `ALTER TABLE gb_contents DROP INDEX gb_contents_slug`)
	if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS gb_contents_slug ON gb_contents (slug)`); err == nil || isDuplicateIndexError(err) {
		return nil
	}
	_, err := db.ExecContext(ctx, `CREATE INDEX gb_contents_slug ON gb_contents (slug)`)
	if err == nil || isDuplicateIndexError(err) {
		return nil
	}
	return err
}

func isDuplicateIndexError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "already exists") ||
		strings.Contains(message, "duplicate key name") ||
		(strings.Contains(message, "relation") && strings.Contains(message, "already exists"))
}

func schemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var raw string
	err := db.QueryRowContext(ctx, `SELECT value FROM gb_options WHERE name = ? AND user = 0`, "schema_version").Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		err = db.QueryRowContext(ctx, `SELECT value FROM gb_options WHERE name = $1 AND "user" = 0`, "schema_version").Scan(&raw)
	}
	if err == nil {
		version, _ := strconv.Atoi(raw)
		return version, nil
	}
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return 0, err
}

func setSchemaVersion(ctx context.Context, db *sql.DB, version int) error {
	value := strconv.Itoa(version)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO gb_options (name, user, value) VALUES (?, 0, ?)
		ON CONFLICT(name, user) DO UPDATE SET value = excluded.value
	`, "schema_version", value); err == nil {
		return nil
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO gb_options (name, user, value) VALUES (?, 0, ?)
		ON DUPLICATE KEY UPDATE value = VALUES(value)
	`, "schema_version", value)
	if err == nil {
		return nil
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO gb_options (name, "user", value) VALUES ($1, 0, $2)
		ON CONFLICT(name, "user") DO UPDATE SET value = EXCLUDED.value
	`, "schema_version", value)
	return err
}

func sqliteSchema() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS gb_contents (
			cid INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			title varchar(150) default NULL,
			slug varchar(150) default NULL,
			slugId int(10) default '0',
			created int(10) default '0',
			modified int(10) default '0',
			text text,
			sortOrder int(10) default '0',
			authorId int(10) default '0',
			template varchar(32) default NULL,
			type varchar(16) default 'post',
			status varchar(16) default 'publish',
			password varchar(64) default NULL,
			commentsNum int(10) default '0',
			allowComment char(1) default '1',
			allowPing char(1) default '0',
			allowFeed char(1) default '1',
			parent int(10) default '0',
			draftOf int(10) default '0'
		)`,
		`CREATE INDEX IF NOT EXISTS gb_contents_slug ON gb_contents (slug)`,
		`CREATE INDEX IF NOT EXISTS gb_contents_slugId ON gb_contents (slugId)`,
		`CREATE INDEX IF NOT EXISTS gb_contents_created ON gb_contents (created)`,
		`CREATE TABLE IF NOT EXISTS gb_users (
			uid INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			name varchar(32) default NULL,
			password varchar(128) default NULL,
			mail varchar(150) default NULL,
			url varchar(150) default NULL,
			screenName varchar(32) default NULL,
			created int(10) default '0',
			activated int(10) default '0',
			logged int(10) default '0',
			role varchar(16) default 'visitor',
			authCode varchar(64) default NULL
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS gb_users_name ON gb_users (name)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS gb_users_mail ON gb_users (mail)`,
		`CREATE TABLE IF NOT EXISTS gb_options (
			name varchar(64) NOT NULL,
			user int(10) NOT NULL default '0',
			value text,
			PRIMARY KEY (name, user)
		)`,
		`CREATE TABLE IF NOT EXISTS gb_metas (
			mid INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			name varchar(150) default NULL,
			slug varchar(150) default NULL,
			type varchar(32) NOT NULL,
			description varchar(150) default NULL,
			count int(10) default '0',
			sortOrder int(10) default '0',
			parent int(10) default '0'
		)`,
		`CREATE INDEX IF NOT EXISTS gb_metas_slug ON gb_metas (slug)`,
		`CREATE TABLE IF NOT EXISTS gb_relationships (
			cid int(10) NOT NULL,
			mid int(10) NOT NULL,
			PRIMARY KEY (cid, mid)
		)`,
		`CREATE TABLE IF NOT EXISTS gb_comments (
			coid INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			cid int(10) default '0',
			created int(10) default '0',
			author varchar(150) default NULL,
			authorId int(10) default '0',
			ownerId int(10) default '0',
			mail varchar(150) default NULL,
			url varchar(255) default NULL,
			ip varchar(64) default NULL,
			agent varchar(511) default NULL,
			text text,
			type varchar(16) default 'comment',
			status varchar(16) default 'approved',
			parent int(10) default '0'
		)`,
		`CREATE INDEX IF NOT EXISTS gb_comments_cid ON gb_comments (cid)`,
		`CREATE TABLE IF NOT EXISTS gb_fields (
			fid INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			cid int(10) NOT NULL default '0',
			name varchar(150) NOT NULL,
			type varchar(16) NOT NULL default 'str',
			strValue text,
			intValue int(10) default '0',
			floatValue real default '0'
		)`,
		`CREATE INDEX IF NOT EXISTS gb_fields_cid ON gb_fields (cid)`,
		`CREATE TABLE IF NOT EXISTS gb_revisions (
			rid INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			cid int(10) NOT NULL default '0',
			created int(10) default '0',
			authorId int(10) default '0',
			title varchar(150) default NULL,
			slug varchar(150) default NULL,
			text text,
			status varchar(16) default 'draft',
			password varchar(64) default NULL,
			sortOrder int(10) default '0',
			template varchar(32) default NULL,
			parent int(10) default '0',
			allowComment char(1) default '1',
			allowPing char(1) default '0',
			allowFeed char(1) default '1'
		)`,
		`CREATE INDEX IF NOT EXISTS gb_revisions_cid ON gb_revisions (cid)`,
	}
}

func mysqlSchema() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS gb_contents (
			cid int(10) unsigned NOT NULL AUTO_INCREMENT,
			title varchar(150) default NULL,
			slug varchar(150) default NULL,
			slugId int(10) unsigned default '0',
			created int(10) unsigned default '0',
			modified int(10) unsigned default '0',
			text longtext,
			sortOrder int(10) unsigned default '0',
			authorId int(10) unsigned default '0',
			template varchar(32) default NULL,
			type varchar(16) default 'post',
			status varchar(16) default 'publish',
			password varchar(64) default NULL,
			commentsNum int(10) unsigned default '0',
			allowComment char(1) default '1',
			allowPing char(1) default '0',
			allowFeed char(1) default '1',
			parent int(10) unsigned default '0',
			draftOf int(10) unsigned default '0',
			PRIMARY KEY (cid),
			KEY gb_contents_slug (slug),
			KEY gb_contents_slugId (slugId),
			KEY gb_contents_created (created),
			KEY gb_contents_draftOf (draftOf)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS gb_users (
			uid int(10) unsigned NOT NULL AUTO_INCREMENT,
			name varchar(32) default NULL,
			password varchar(128) default NULL,
			mail varchar(150) default NULL,
			url varchar(150) default NULL,
			screenName varchar(32) default NULL,
			created int(10) unsigned default '0',
			activated int(10) unsigned default '0',
			logged int(10) unsigned default '0',
			role varchar(16) default 'visitor',
			authCode varchar(64) default NULL,
			PRIMARY KEY (uid),
			UNIQUE KEY gb_users_name (name),
			UNIQUE KEY gb_users_mail (mail)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS gb_options (
			name varchar(64) NOT NULL,
			user int(10) unsigned NOT NULL default '0',
			value text,
			PRIMARY KEY (name, user)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS gb_metas (
			mid int(10) unsigned NOT NULL AUTO_INCREMENT,
			name varchar(150) default NULL,
			slug varchar(150) default NULL,
			type varchar(32) NOT NULL,
			description varchar(150) default NULL,
			count int(10) unsigned default '0',
			sortOrder int(10) unsigned default '0',
			parent int(10) unsigned default '0',
			PRIMARY KEY (mid),
			KEY gb_metas_slug (slug)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS gb_relationships (
			cid int(10) unsigned NOT NULL,
			mid int(10) unsigned NOT NULL,
			PRIMARY KEY (cid, mid)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS gb_comments (
			coid int(10) unsigned NOT NULL AUTO_INCREMENT,
			cid int(10) unsigned default '0',
			created int(10) unsigned default '0',
			author varchar(150) default NULL,
			authorId int(10) unsigned default '0',
			ownerId int(10) unsigned default '0',
			mail varchar(150) default NULL,
			url varchar(255) default NULL,
			ip varchar(64) default NULL,
			agent varchar(511) default NULL,
			text text,
			type varchar(16) default 'comment',
			status varchar(16) default 'approved',
			parent int(10) unsigned default '0',
			PRIMARY KEY (coid),
			KEY gb_comments_cid (cid)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS gb_fields (
			fid int(10) unsigned NOT NULL AUTO_INCREMENT,
			cid int(10) unsigned NOT NULL default '0',
			name varchar(150) NOT NULL,
			type varchar(16) NOT NULL default 'str',
			strValue longtext,
			intValue int(10) default '0',
			floatValue double default '0',
			PRIMARY KEY (fid),
			KEY gb_fields_cid (cid)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS gb_revisions (
			rid int(10) unsigned NOT NULL AUTO_INCREMENT,
			cid int(10) unsigned NOT NULL default '0',
			created int(10) unsigned default '0',
			authorId int(10) unsigned default '0',
			title varchar(150) default NULL,
			slug varchar(150) default NULL,
			text longtext,
			status varchar(16) default 'draft',
			password varchar(64) default NULL,
			sortOrder int(10) unsigned default '0',
			template varchar(32) default NULL,
			parent int(10) unsigned default '0',
			allowComment char(1) default '1',
			allowPing char(1) default '0',
			allowFeed char(1) default '1',
			PRIMARY KEY (rid),
			KEY gb_revisions_cid (cid)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	}
}

func postgresSchema() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS gb_contents (
			cid bigserial PRIMARY KEY,
			title varchar(150) default NULL,
			slug varchar(150) default NULL,
			slugId bigint default 0,
			created bigint default 0,
			modified bigint default 0,
			text text,
			sortOrder bigint default 0,
			authorId bigint default 0,
			template varchar(32) default NULL,
			type varchar(16) default 'post',
			status varchar(16) default 'publish',
			password varchar(64) default NULL,
			commentsNum bigint default 0,
			allowComment char(1) default '1',
			allowPing char(1) default '0',
			allowFeed char(1) default '1',
			parent bigint default 0,
			draftOf bigint default 0
		)`,
		`CREATE INDEX IF NOT EXISTS gb_contents_slug ON gb_contents (slug)`,
		`CREATE INDEX IF NOT EXISTS gb_contents_slugId ON gb_contents (slugId)`,
		`CREATE INDEX IF NOT EXISTS gb_contents_created ON gb_contents (created)`,
		`CREATE TABLE IF NOT EXISTS gb_users (
			uid bigserial PRIMARY KEY,
			name varchar(32) default NULL,
			password varchar(128) default NULL,
			mail varchar(150) default NULL,
			url varchar(150) default NULL,
			screenName varchar(32) default NULL,
			created bigint default 0,
			activated bigint default 0,
			logged bigint default 0,
			role varchar(16) default 'visitor',
			authCode varchar(64) default NULL
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS gb_users_name ON gb_users (name)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS gb_users_mail ON gb_users (mail)`,
		`CREATE TABLE IF NOT EXISTS gb_options (
			name varchar(64) NOT NULL,
			"user" bigint NOT NULL default 0,
			value text,
			PRIMARY KEY (name, "user")
		)`,
		`CREATE TABLE IF NOT EXISTS gb_metas (
			mid bigserial PRIMARY KEY,
			name varchar(150) default NULL,
			slug varchar(150) default NULL,
			type varchar(32) NOT NULL,
			description varchar(150) default NULL,
			count bigint default 0,
			sortOrder bigint default 0,
			parent bigint default 0
		)`,
		`CREATE INDEX IF NOT EXISTS gb_metas_slug ON gb_metas (slug)`,
		`CREATE TABLE IF NOT EXISTS gb_relationships (
			cid bigint NOT NULL,
			mid bigint NOT NULL,
			PRIMARY KEY (cid, mid)
		)`,
		`CREATE TABLE IF NOT EXISTS gb_comments (
			coid bigserial PRIMARY KEY,
			cid bigint default 0,
			created bigint default 0,
			author varchar(150) default NULL,
			authorId bigint default 0,
			ownerId bigint default 0,
			mail varchar(150) default NULL,
			url varchar(255) default NULL,
			ip varchar(64) default NULL,
			agent varchar(511) default NULL,
			text text,
			type varchar(16) default 'comment',
			status varchar(16) default 'approved',
			parent bigint default 0
		)`,
		`CREATE INDEX IF NOT EXISTS gb_comments_cid ON gb_comments (cid)`,
		`CREATE TABLE IF NOT EXISTS gb_fields (
			fid bigserial PRIMARY KEY,
			cid bigint NOT NULL default 0,
			name varchar(150) NOT NULL,
			type varchar(16) NOT NULL default 'str',
			strValue text,
			intValue bigint default 0,
			floatValue double precision default 0
		)`,
		`CREATE INDEX IF NOT EXISTS gb_fields_cid ON gb_fields (cid)`,
		`CREATE TABLE IF NOT EXISTS gb_revisions (
			rid bigserial PRIMARY KEY,
			cid bigint NOT NULL default 0,
			created bigint default 0,
			authorId bigint default 0,
			title varchar(150) default NULL,
			slug varchar(150) default NULL,
			text text,
			status varchar(16) default 'draft',
			password varchar(64) default NULL,
			sortOrder bigint default 0,
			template varchar(32) default NULL,
			parent bigint default 0,
			allowComment char(1) default '1',
			allowPing char(1) default '0',
			allowFeed char(1) default '1'
		)`,
		`CREATE INDEX IF NOT EXISTS gb_revisions_cid ON gb_revisions (cid)`,
	}
}
