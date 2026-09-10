package main

import "testing"

// 验证 adaptSQL 的 SQLite→PostgreSQL 改写规则（无需真实 PG 实例）。
func setPG(t *testing.T) {
	t.Helper()
	old := dbDriverName
	dbDriverName = "postgres"
	t.Cleanup(func() { dbDriverName = old })
}

func TestAdaptSQLPlaceholders(t *testing.T) {
	setPG(t)
	got := adaptSQL("SELECT * FROM t WHERE a = ? AND b = ? AND c = ?")
	want := "SELECT * FROM t WHERE a = $1 AND b = $2 AND c = $3"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestAdaptSQLPassthroughOnSQLite(t *testing.T) {
	dbDriverName = "sqlite"
	q := "SELECT * FROM t WHERE a = ? AND date(time) = ?"
	if got := adaptSQL(q); got != q {
		t.Fatalf("sqlite 应原样返回，got %q", got)
	}
}

func TestAdaptSQLInsertOrIgnore(t *testing.T) {
	setPG(t)
	got := adaptSQL("INSERT OR IGNORE INTO daily_ips (stat_date, ip) VALUES (?, ?)")
	want := "INSERT INTO daily_ips (stat_date, ip) VALUES ($1, $2) ON CONFLICT DO NOTHING"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestAdaptSQLGreatest(t *testing.T) {
	setPG(t)
	got := adaptSQL("UPDATE stats_totals SET v = MAX(0, v - ?) WHERE k = ?")
	want := "UPDATE stats_totals SET v = GREATEST(0, v - $1) WHERE k = $2"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestAdaptSQLDateFunc(t *testing.T) {
	setPG(t)
	got := adaptSQL("SELECT date(time) AS d, COUNT(*) AS cnt FROM access_log WHERE event = ? AND date(time) = ? GROUP BY date(time)")
	want := "SELECT (time::date) AS d, COUNT(*) AS cnt FROM access_log WHERE event = $1 AND (time::date) = $2 GROUP BY (time::date)"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestAdaptSQLJSONExtract(t *testing.T) {
	setPG(t)
	got := adaptSQL("SELECT CASE WHEN context LIKE '{%' THEN json_extract(context, '$.link_id') END AS link_id, COUNT(*) AS cnt FROM access_log WHERE event = ?")
	want := "SELECT CASE WHEN context LIKE '{%' THEN (context::jsonb->>'link_id') END AS link_id, COUNT(*) AS cnt FROM access_log WHERE event = $1"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestAdaptSQLOnConflictKept(t *testing.T) {
	setPG(t)
	q := "INSERT INTO visitor_stats (stat_date, visits, shares, ad_clicks) VALUES (?, 1, ?, 0) " +
		"ON CONFLICT (stat_date) DO UPDATE SET visits = visits + 1, shares = shares + ?"
	got := adaptSQL(q)
	want := "INSERT INTO visitor_stats (stat_date, visits, shares, ad_clicks) VALUES ($1, 1, $2, 0) " +
		"ON CONFLICT (stat_date) DO UPDATE SET visits = visits + 1, shares = shares + $3"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
