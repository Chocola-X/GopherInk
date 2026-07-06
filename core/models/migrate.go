package models

import (
	"context"
	"database/sql"
	"fmt"
)

func Migrate(ctx context.Context, db *sql.DB, driver string) error {
	var stmts []string
	switch driver {
	case "mysql", "mariadb":
		stmts = mysqlSchema()
	default:
		stmts = sqliteSchema()
	}

	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	ensureColumn(ctx, db, `ALTER TABLE gb_contents ADD COLUMN sortOrder int(10) default '0'`)
	ensureColumn(ctx, db, `ALTER TABLE gb_users ADD COLUMN role varchar(16) default 'visitor'`)

	return nil
}

func ensureColumn(ctx context.Context, db *sql.DB, stmt string) {
	_, _ = db.ExecContext(ctx, stmt)
}

func sqliteSchema() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS gb_contents (
			cid INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			title varchar(150) default NULL,
			slug varchar(150) default NULL,
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
			parent int(10) default '0'
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS gb_contents_slug ON gb_contents (slug)`,
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
			PRIMARY KEY (cid),
			UNIQUE KEY gb_contents_slug (slug),
			KEY gb_contents_created (created)
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
