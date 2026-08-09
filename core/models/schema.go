package models

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/Chocola-X/GopherInk/core/plugin"
	"github.com/Chocola-X/GopherInk/pkg/safeid"
)

const CurrentSchemaVersion = 1

func InitializeSchema(ctx context.Context, db *sql.DB, driver string) error {
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
			return fmt.Errorf("initialize schema: %w", err)
		}
	}
	return setSchemaVersion(ctx, db, NormalizeDialect(driver), CurrentSchemaVersion)
}

func setSchemaVersion(ctx context.Context, db *sql.DB, dialect Dialect, version int) error {
	value := strconv.Itoa(version)
	_, err := db.ExecContext(ctx, UpsertOptionSQL(dialect), "schema_version", 0, value)
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
			draftOf int(10) NOT NULL default '0'
		)`,
		`CREATE INDEX IF NOT EXISTS gb_contents_slug ON gb_contents (slug)`,
		`CREATE INDEX IF NOT EXISTS gb_contents_slugId ON gb_contents (slugId)`,
		`CREATE INDEX IF NOT EXISTS gb_contents_created ON gb_contents (created)`,
		`CREATE INDEX IF NOT EXISTS gb_contents_draftOf ON gb_contents (draftOf)`,
		`CREATE INDEX IF NOT EXISTS gb_contents_public_list ON gb_contents (type, status, draftOf, created)`,
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
		`CREATE INDEX IF NOT EXISTS gb_metas_type_slug ON gb_metas (type, slug)`,
		`CREATE INDEX IF NOT EXISTS gb_metas_type_sort ON gb_metas (type, sortOrder)`,
		`CREATE TABLE IF NOT EXISTS gb_relationships (
			cid int(10) NOT NULL,
			mid int(10) NOT NULL,
			PRIMARY KEY (cid, mid)
		)`,
		`CREATE INDEX IF NOT EXISTS gb_relationships_mid_cid ON gb_relationships (mid, cid)`,
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
		`CREATE INDEX IF NOT EXISTS gb_comments_cid_status_created ON gb_comments (cid, status, created)`,
		`CREATE INDEX IF NOT EXISTS gb_comments_status_created ON gb_comments (status, created)`,
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
		`CREATE UNIQUE INDEX IF NOT EXISTS gb_fields_cid_name ON gb_fields (cid, name)`,
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
			draftOf int(10) unsigned NOT NULL default '0',
			PRIMARY KEY (cid),
			KEY gb_contents_slug (slug),
			KEY gb_contents_slugId (slugId),
			KEY gb_contents_created (created),
			KEY gb_contents_draftOf (draftOf),
			KEY gb_contents_public_list (type, status, draftOf, created)
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
			KEY gb_metas_slug (slug),
			KEY gb_metas_type_slug (type, slug),
			KEY gb_metas_type_sort (type, sortOrder)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS gb_relationships (
			cid int(10) unsigned NOT NULL,
			mid int(10) unsigned NOT NULL,
			PRIMARY KEY (cid, mid),
			KEY gb_relationships_mid_cid (mid, cid)
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
			KEY gb_comments_cid (cid),
			KEY gb_comments_cid_status_created (cid, status, created),
			KEY gb_comments_status_created (status, created)
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
			KEY gb_fields_cid (cid),
			UNIQUE KEY gb_fields_cid_name (cid, name)
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

func CreatePluginTables(ctx context.Context, db *sql.DB, dialect string, owner string, tables []plugin.TableDefinition) error {
	for _, table := range tables {
		tableName := PluginTableName(owner, table.Name)
		stmt := buildCreateTableSQL(dialect, tableName, table)
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create plugin table %s: %w", tableName, err)
		}
		for _, idx := range table.Indexes {
			idxStmt := buildCreateIndexSQL(dialect, tableName, idx)
			if _, err := db.ExecContext(ctx, idxStmt); err != nil {
				return fmt.Errorf("create plugin index %s on %s: %w", idx.Name, tableName, err)
			}
		}
	}
	return nil
}

func DropPluginTables(ctx context.Context, db *sql.DB, dialect string, owner string, tables []plugin.TableDefinition) error {
	for _, table := range tables {
		tableName := PluginTableName(owner, table.Name)
		var stmt string
		switch Dialect(dialect) {
		case DialectMySQL:
			stmt = fmt.Sprintf("DROP TABLE IF EXISTS `%s`", tableName)
		case DialectPostgres:
			stmt = fmt.Sprintf("DROP TABLE IF EXISTS \"%s\"", tableName)
		default:
			stmt = fmt.Sprintf("DROP TABLE IF EXISTS \"%s\"", tableName)
		}
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("drop plugin table %s: %w", tableName, err)
		}
	}
	return nil
}

func PluginTableName(owner, name string) string {
	return plugin.DatabaseTableName(owner, name)
}

func buildCreateTableSQL(dialect string, tableName string, table plugin.TableDefinition) string {
	var sb strings.Builder
	switch Dialect(dialect) {
	case DialectMySQL:
		sb.WriteString(fmt.Sprintf("CREATE TABLE IF NOT EXISTS `%s` (", tableName))
	default:
		sb.WriteString(fmt.Sprintf("CREATE TABLE IF NOT EXISTS \"%s\" (", tableName))
	}
	cols := make([]string, 0, len(table.Columns))
	var pkCols []string
	for _, col := range table.Columns {
		cols = append(cols, buildColumnDef(dialect, col))
		if col.Primary && !col.AutoInc {
			pkCols = append(pkCols, safeid.SQL(col.Name))
		}
	}
	if len(pkCols) > 0 {
		cols = append(cols, "PRIMARY KEY ("+strings.Join(pkCols, ", ")+")")
	}
	sb.WriteString(strings.Join(cols, ", "))
	sb.WriteString(")")
	switch Dialect(dialect) {
	case DialectMySQL:
		sb.WriteString(" ENGINE=InnoDB DEFAULT CHARSET=utf8mb4")
	}
	return sb.String()
}

func buildColumnDef(dialect string, col plugin.ColumnDefinition) string {
	var sb strings.Builder
	sb.WriteString(safeid.SQL(col.Name) + " ")
	sb.WriteString(columnTypeSQL(dialect, col))
	if !col.Nullable {
		sb.WriteString(" NOT NULL")
	}
	if col.AutoInc {
		switch Dialect(dialect) {
		case DialectMySQL:
			sb.WriteString(" AUTO_INCREMENT")
		case DialectPostgres:
			if col.Type == plugin.ColInt64 {
				sb.WriteString(" GENERATED ALWAYS AS IDENTITY")
			}
		default:
			sb.WriteString(" PRIMARY KEY AUTOINCREMENT")
		}
		if col.Primary {
			switch Dialect(dialect) {
			case DialectMySQL, DialectPostgres:
				sb.WriteString(" PRIMARY KEY")
			}
		}
	}
	if col.Default != "" {
		sb.WriteString(" DEFAULT " + col.Default)
	}
	return sb.String()
}

func columnTypeSQL(dialect string, col plugin.ColumnDefinition) string {
	switch col.Type {
	case plugin.ColInt64:
		switch Dialect(dialect) {
		case DialectMySQL:
			return "bigint"
		case DialectPostgres:
			return "bigint"
		default:
			return "INTEGER"
		}
	case plugin.ColVarchar:
		length := col.Length
		if length <= 0 {
			length = 255
		}
		return fmt.Sprintf("varchar(%d)", length)
	case plugin.ColText:
		switch Dialect(dialect) {
		case DialectMySQL:
			return "longtext"
		default:
			return "text"
		}
	case plugin.ColFloat:
		switch Dialect(dialect) {
		case DialectMySQL:
			return "double"
		case DialectPostgres:
			return "double precision"
		default:
			return "real"
		}
	case plugin.ColDatetime:
		switch Dialect(dialect) {
		case DialectMySQL:
			return "datetime"
		case DialectPostgres:
			return "timestamptz"
		default:
			return "int64"
		}
	case plugin.ColBool:
		switch Dialect(dialect) {
		case DialectMySQL:
			return "tinyint(1)"
		case DialectPostgres:
			return "boolean"
		default:
			return "int64"
		}
	default:
		return "text"
	}
}

func buildCreateIndexSQL(dialect string, tableName string, idx plugin.IndexDefinition) string {
	idxName := fmt.Sprintf("idx_%s_%s", tableName, safeid.SQL(idx.Name))
	indexCols := make([]string, 0, len(idx.Columns))
	for _, column := range idx.Columns {
		indexCols = append(indexCols, safeid.SQL(column))
	}
	cols := strings.Join(indexCols, ", ")
	if idx.Unique {
		switch Dialect(dialect) {
		case DialectMySQL:
			return fmt.Sprintf("CREATE UNIQUE INDEX `%s` ON `%s` (%s)", idxName, tableName, cols)
		case DialectPostgres:
			return fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS \"%s\" ON \"%s\" (%s)", idxName, tableName, cols)
		default:
			return fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS \"%s\" ON \"%s\" (%s)", idxName, tableName, cols)
		}
	}
	switch Dialect(dialect) {
	case DialectMySQL:
		return fmt.Sprintf("CREATE INDEX `%s` ON `%s` (%s)", idxName, tableName, cols)
	case DialectPostgres:
		return fmt.Sprintf("CREATE INDEX IF NOT EXISTS \"%s\" ON \"%s\" (%s)", idxName, tableName, cols)
	default:
		return fmt.Sprintf("CREATE INDEX IF NOT EXISTS \"%s\" ON \"%s\" (%s)", idxName, tableName, cols)
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
			draftOf bigint NOT NULL default 0
		)`,
		`CREATE INDEX IF NOT EXISTS gb_contents_slug ON gb_contents (slug)`,
		`CREATE INDEX IF NOT EXISTS gb_contents_slugId ON gb_contents (slugId)`,
		`CREATE INDEX IF NOT EXISTS gb_contents_created ON gb_contents (created)`,
		`CREATE INDEX IF NOT EXISTS gb_contents_draftOf ON gb_contents (draftOf)`,
		`CREATE INDEX IF NOT EXISTS gb_contents_public_list ON gb_contents (type, status, draftOf, created)`,
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
		`CREATE INDEX IF NOT EXISTS gb_metas_type_slug ON gb_metas (type, slug)`,
		`CREATE INDEX IF NOT EXISTS gb_metas_type_sort ON gb_metas (type, sortOrder)`,
		`CREATE TABLE IF NOT EXISTS gb_relationships (
			cid bigint NOT NULL,
			mid bigint NOT NULL,
			PRIMARY KEY (cid, mid)
		)`,
		`CREATE INDEX IF NOT EXISTS gb_relationships_mid_cid ON gb_relationships (mid, cid)`,
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
		`CREATE INDEX IF NOT EXISTS gb_comments_cid_status_created ON gb_comments (cid, status, created)`,
		`CREATE INDEX IF NOT EXISTS gb_comments_status_created ON gb_comments (status, created)`,
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
		`CREATE UNIQUE INDEX IF NOT EXISTS gb_fields_cid_name ON gb_fields (cid, name)`,
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
