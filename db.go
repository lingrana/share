package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// db 双数据库支持：SQLite（默认，零配置）与 PostgreSQL（/install 选择）。
// 查询统一用 SQLite 风格书写（? 占位符），postgres 由 adaptSQL 改写。
var db *sql.DB
var dbDriverName string // "sqlite" | "postgres"

func openDBWithConfig(c DBConfig) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	var driver, dsn string
	switch c.Driver {
	case "sqlite":
		driver = "sqlite"
		path := c.SQLitePath
		if path == "" {
			path = "share.sqlite"
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(dataDir, path)
		}
		dsn = "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)&_pragma=synchronous(NORMAL)"
	case "postgres":
		driver = "pgx"
		if c.Host == "" || c.User == "" || c.DBName == "" {
			return errors.New("PostgreSQL 连接信息不完整（host / user / dbname）")
		}
		port := c.Port
		if port <= 0 || port > 65535 {
			port = 5432
		}
		u := url.URL{Scheme: "postgres", Host: net.JoinHostPort(c.Host, strconv.Itoa(port)), Path: "/" + c.DBName}
		u.User = url.UserPassword(c.User, c.Password)
		q := url.Values{}
		ssl := c.SSLMode
		if ssl == "" {
			ssl = "disable"
		}
		q.Set("sslmode", ssl)
		u.RawQuery = q.Encode()
		dsn = u.String()
	default:
		return errors.New("未知数据库驱动: " + c.Driver)
	}

	var err error
	db, err = sql.Open(driver, dsn)
	if err != nil {
		return err
	}
	dbDriverName = c.Driver
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	if driver == "pgx" {
		db.SetConnMaxLifetime(30 * time.Minute)
	}
	if err := db.Ping(); err != nil {
		db = nil
		dbDriverName = ""
		return err
	}
	if err := initSchema(); err != nil {
		db = nil
		dbDriverName = ""
		return err
	}
	return nil
}

func initSchema() error {
	tables := sqliteTables()
	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_parent ON folders (parent_id)",
		"CREATE INDEX IF NOT EXISTS idx_folder ON resources (folder_id)",
		"CREATE INDEX IF NOT EXISTS idx_type ON resources (resource_type)",
		"CREATE INDEX IF NOT EXISTS idx_enabled ON resources (enabled)",
		"CREATE INDEX IF NOT EXISTS idx_rl_resource ON resource_links (resource_id)",
		"CREATE INDEX IF NOT EXISTS idx_shares_resource ON shares (resource_id)",
		"CREATE INDEX IF NOT EXISTS idx_al_event ON access_log (event)",
		"CREATE INDEX IF NOT EXISTS idx_al_time ON access_log (time)",
		"CREATE INDEX IF NOT EXISTS idx_gw_files_node ON gw_files (node_id)",
	}
	if dbDriverName == "postgres" {
		tables = postgresTables()
	}
	for _, q := range tables {
		if _, err := db.Exec(q); err != nil {
			return fmt.Errorf("建表失败: %w", err)
		}
	}
	for _, q := range indexes {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// sqliteTables 沿用 PHP 版 dev.php 的 SQLite schema。
func sqliteTables() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS folders (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			parent_id INTEGER,
			path TEXT NOT NULL,
			sort_order INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS resources (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			folder_id INTEGER,
			title TEXT NOT NULL,
			description TEXT,
			cover_url TEXT DEFAULT '',
			media_url TEXT DEFAULT '',
			resource_type TEXT DEFAULT 'other',
			tags TEXT DEFAULT '',
			view_count INTEGER NOT NULL DEFAULT 0,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS resource_links (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			resource_id INTEGER NOT NULL,
			platform TEXT NOT NULL,
			url TEXT NOT NULL,
			code TEXT DEFAULT '',
			password TEXT DEFAULT '',
			sort_order INTEGER NOT NULL DEFAULT 0,
			click_count INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS shares (
			token TEXT PRIMARY KEY,
			resource_id INTEGER NOT NULL,
			title TEXT NOT NULL,
			code TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			expires_at TEXT DEFAULT NULL,
			enabled INTEGER NOT NULL DEFAULT 1
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			k TEXT PRIMARY KEY,
			v TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS visitor_stats (
			stat_date TEXT PRIMARY KEY,
			visits INTEGER NOT NULL DEFAULT 0,
			shares INTEGER NOT NULL DEFAULT 0,
			ad_clicks INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS stats_totals (
			k TEXT PRIMARY KEY,
			v INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS unique_ips (
			ip TEXT PRIMARY KEY,
			first_seen TEXT NOT NULL,
			last_seen TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS daily_ips (
			stat_date TEXT NOT NULL,
			ip TEXT NOT NULL,
			PRIMARY KEY (stat_date, ip)
		)`,
		`CREATE TABLE IF NOT EXISTS announcement_reads (
			ip TEXT PRIMARY KEY,
			read_at TEXT NOT NULL,
			force_popup INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS access_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			time TEXT NOT NULL,
			event TEXT NOT NULL,
			ip TEXT NOT NULL,
			uri TEXT DEFAULT '',
			context TEXT
		)`,
		// 内置存储网关（对应 share-storage gateway 的 ss_nodes / ss_files）
		`CREATE TABLE IF NOT EXISTS gw_nodes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			base_url TEXT NOT NULL,
			token TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			anti_bot INTEGER NOT NULL DEFAULT 0,
			cookie TEXT DEFAULT '',
			free_bytes INTEGER NOT NULL DEFAULT 0,
			total_bytes INTEGER NOT NULL DEFAULT 0,
			used_bytes INTEGER NOT NULL DEFAULT 0,
			last_seen_at TEXT DEFAULT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS gw_files (
			file_key TEXT NOT NULL UNIQUE,
			node_id INTEGER NOT NULL DEFAULT 0,
			size_bytes INTEGER NOT NULL DEFAULT 0,
			mime TEXT DEFAULT '',
			ext TEXT DEFAULT '',
			orig_name TEXT DEFAULT '',
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
	}
}

// postgresTables 等价 schema 的 PostgreSQL 版本。
// 时间戳沿用 TEXT 列存 'YYYY-MM-DD HH:MI:SS'，默认值用 to_char(now()) 与 SQLite 对齐。
func postgresTables() []string {
	const tsDefault = "to_char(now(), 'YYYY-MM-DD HH24:MI:SS')"
	return []string{
		`CREATE TABLE IF NOT EXISTS users (
			id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT ` + tsDefault + `
		)`,
		`CREATE TABLE IF NOT EXISTS folders (
			id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			name TEXT NOT NULL,
			parent_id BIGINT,
			path TEXT NOT NULL,
			sort_order INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT ` + tsDefault + `
		)`,
		`CREATE TABLE IF NOT EXISTS resources (
			id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			folder_id BIGINT,
			title TEXT NOT NULL,
			description TEXT,
			cover_url TEXT DEFAULT '',
			media_url TEXT DEFAULT '',
			resource_type TEXT DEFAULT 'other',
			tags TEXT DEFAULT '',
			view_count BIGINT NOT NULL DEFAULT 0,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL DEFAULT ` + tsDefault + `,
			updated_at TEXT NOT NULL DEFAULT ` + tsDefault + `
		)`,
		`CREATE TABLE IF NOT EXISTS resource_links (
			id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			resource_id BIGINT NOT NULL,
			platform TEXT NOT NULL,
			url TEXT NOT NULL,
			code TEXT DEFAULT '',
			password TEXT DEFAULT '',
			sort_order INTEGER NOT NULL DEFAULT 0,
			click_count BIGINT NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS shares (
			token TEXT PRIMARY KEY,
			resource_id BIGINT NOT NULL,
			title TEXT NOT NULL,
			code TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			expires_at TEXT DEFAULT NULL,
			enabled INTEGER NOT NULL DEFAULT 1
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			k TEXT PRIMARY KEY,
			v TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS visitor_stats (
			stat_date TEXT PRIMARY KEY,
			visits BIGINT NOT NULL DEFAULT 0,
			shares BIGINT NOT NULL DEFAULT 0,
			ad_clicks BIGINT NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS stats_totals (
			k TEXT PRIMARY KEY,
			v BIGINT NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS unique_ips (
			ip TEXT PRIMARY KEY,
			first_seen TEXT NOT NULL,
			last_seen TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS daily_ips (
			stat_date TEXT NOT NULL,
			ip TEXT NOT NULL,
			PRIMARY KEY (stat_date, ip)
		)`,
		`CREATE TABLE IF NOT EXISTS announcement_reads (
			ip TEXT PRIMARY KEY,
			read_at TEXT NOT NULL,
			force_popup INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS access_log (
			id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			time TEXT NOT NULL,
			event TEXT NOT NULL,
			ip TEXT NOT NULL,
			uri TEXT DEFAULT '',
			context TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS gw_nodes (
			id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			name TEXT NOT NULL,
			base_url TEXT NOT NULL,
			token TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			anti_bot INTEGER NOT NULL DEFAULT 0,
			cookie TEXT DEFAULT '',
			free_bytes BIGINT NOT NULL DEFAULT 0,
			total_bytes BIGINT NOT NULL DEFAULT 0,
			used_bytes BIGINT NOT NULL DEFAULT 0,
			last_seen_at TEXT DEFAULT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS gw_files (
			file_key TEXT NOT NULL UNIQUE,
			node_id BIGINT NOT NULL DEFAULT 0,
			size_bytes BIGINT NOT NULL DEFAULT 0,
			mime TEXT DEFAULT '',
			ext TEXT DEFAULT '',
			orig_name TEXT DEFAULT '',
			created_at TEXT NOT NULL DEFAULT ` + tsDefault + `
		)`,
	}
}

// ensureRootFolder 保证根目录存在（parent_id IS NULL 的唯一行）。
func ensureRootFolder() {
	var id int
	err := db.QueryRow("SELECT id FROM folders WHERE parent_id IS NULL").Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		_, _ = db.Exec("INSERT INTO folders (name, parent_id, path, sort_order) VALUES ('/', NULL, '/', 0)")
	}
}

// ==================== 查询封装 ====================
// 行统一转成 map[string]any：INTEGER→int，TEXT→string，NULL→nil。
// 模板与业务代码直接按字段名取值，贴近 PHP 版的关联数组风格。

// adaptSQL 把 SQLite 风格 SQL 改写为 PostgreSQL 方言：
//   - ? 占位符 → $n
//   - INSERT OR IGNORE → ON CONFLICT DO NOTHING
//   - MAX(0, x) 两参标量形式 → GREATEST（PG 的 MAX 只接受单参）
//   - date(time)（TEXT 列）→ (time::date)（PG 没有 date(text) 函数）
//   - json_extract(col, '$.key') → (col::jsonb ->> 'key')
//
// ON CONFLICT ... DO UPDATE SET col = col + 1 / excluded.col 两种方言通用，无需改写。
func adaptSQL(query string) string {
	if dbDriverName != "postgres" {
		return query
	}
	s := query
	if strings.Contains(s, "INSERT OR IGNORE INTO") {
		s = strings.ReplaceAll(s, "INSERT OR IGNORE INTO", "INSERT INTO")
		if !strings.Contains(strings.ToUpper(s), "ON CONFLICT") {
			s += " ON CONFLICT DO NOTHING"
		}
	}
	s = strings.ReplaceAll(s, "MAX(0,", "GREATEST(0,")
	s = reDateCol.ReplaceAllString(s, "(time::date)")
	s = pgJSONExtract(s)
	// 占位符重编号（本项目 SQL 的字符串字面量内不含裸 ?）
	n := 0
	out := make([]byte, 0, len(s)+8)
	for i := 0; i < len(s); i++ {
		if s[i] == '?' {
			n++
			out = append(out, '$')
			out = append(out, []byte(strconv.Itoa(n))...)
		} else {
			out = append(out, s[i])
		}
	}
	return string(out)
}

var reDateCol = regexp.MustCompile(`\bdate\(time\)`)

// pgJSONExtract 把 json_extract(context, '$.key') 改写为 PG 的 ->> 操作符。
func pgJSONExtract(s string) string {
	if !strings.Contains(s, "json_extract(") {
		return s
	}
	re := regexp.MustCompile(`json_extract\(\s*([a-zA-Z_][a-zA-Z0-9_.]*)\s*,\s*'\$\.([a-zA-Z_][a-zA-Z0-9_]*)'\s*\)`)
	return re.ReplaceAllString(s, `($1::jsonb->>'$2')`)
}

func normalizeValue(v any) any {
	switch t := v.(type) {
	case int64:
		return int(t)
	case []byte:
		return string(t)
	default:
		return v
	}
}

func rowsToMaps(rows *sql.Rows) ([]map[string]any, error) {
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			m[c] = normalizeValue(vals[i])
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func dbFetchAll(query string, args ...any) []map[string]any {
	if db == nil {
		log.Printf("dbFetchAll: 数据库未连接（未安装？）")
		return nil
	}
	query = adaptSQL(query)
	rows, err := db.Query(query, args...)
	if err != nil {
		log.Printf("dbFetchAll: %v", err) // 不输出完整 SQL，防止敏感数据泄露
		return nil
	}
	res, err := rowsToMaps(rows)
	if err != nil {
		log.Printf("dbFetchAll scan: %v", err)
		return nil
	}
	return res
}

func dbFetchOne(query string, args ...any) map[string]any {
	res := dbFetchAll(query, args...)
	if len(res) == 0 {
		return nil
	}
	return res[0]
}

func dbFetchColumn(query string, args ...any) any {
	row := dbFetchOne(query, args...)
	if row == nil {
		return nil
	}
	for _, v := range row {
		return v
	}
	return nil
}

func dbFetchColumnInt(query string, args ...any) int {
	v := dbFetchColumn(query, args...)
	if n, ok := v.(int); ok {
		return n
	}
	return 0
}

func dbFetchColumnStr(query string, args ...any) string {
	v := dbFetchColumn(query, args...)
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func dbExecute(query string, args ...any) int64 {
	if db == nil {
		log.Printf("dbExecute: 数据库未连接（未安装？）")
		return 0
	}
	query = adaptSQL(query)
	res, err := db.Exec(query, args...)
	if err != nil {
		log.Printf("dbExecute: %v", err) // 不输出完整 SQL，防止敏感数据泄露
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}

// postgresHasID 表示哪些 PostgreSQL 表有 id 列（GENERATED ALWAYS AS IDENTITY）。
// settings / shares / announcement_reads / gw_files 没有 id 列，不能用 RETURNING id。
var postgresHasID = map[string]bool{
	"users": true, "folders": true, "resources": true, "resource_links": true,
	"gw_nodes": true, "access_log": true,
}

func dbInsert(table string, data map[string]any) int64 {
	if db == nil {
		log.Printf("dbInsert: 数据库未连接（未安装？）")
		return 0
	}
	cols := sortedKeys(data)
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",")
	q := "INSERT INTO \"" + table + "\" (\"" + strings.Join(cols, "\", \"") + "\") VALUES (" + placeholders + ")"
	args := make([]any, len(cols))
	for i, c := range cols {
		args[i] = data[c]
	}
	if dbDriverName == "postgres" {
		// PG 无 LastInsertId，走 RETURNING（仅对有 id 列的表）
		if !postgresHasID[table] {
			if _, err := db.Exec(adaptSQL(q), args...); err != nil {
				log.Printf("dbInsert %s: %v", table, err)
			}
			return 0
		}
		var id int64
		if err := db.QueryRow(adaptSQL(q+" RETURNING id"), args...).Scan(&id); err != nil {
			log.Printf("dbInsert %s: %v", table, err)
			return 0
		}
		return id
	}
	res, err := db.Exec(q, args...)
	if err != nil {
		log.Printf("dbInsert %s: %v", table, err)
		return 0
	}
	id, _ := res.LastInsertId()
	return id
}

func dbUpdate(table string, data map[string]any, where string, whereArgs ...any) int64 {
	cols := sortedKeys(data)
	sets := make([]string, len(cols))
	args := make([]any, 0, len(cols)+len(whereArgs))
	for i, c := range cols {
		sets[i] = "\"" + c + "\" = ?"
		args = append(args, data[c])
	}
	args = append(args, whereArgs...)
	return dbExecute("UPDATE \""+table+"\" SET "+strings.Join(sets, ", ")+" WHERE "+where, args...)
}

func dbDelete(table, where string, args ...any) int64 {
	return dbExecute("DELETE FROM \""+table+"\" WHERE "+where, args...)
}

func dbCount(table, where string, args ...any) int {
	if where == "1" {
		where = "true"
	}
	return dbFetchColumnInt("SELECT COUNT(*) AS c FROM \""+table+"\" WHERE "+where, args...)
}
