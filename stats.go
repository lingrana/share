package main

import (
	"sort"
	"time"
)

// 访问统计，对应 includes/stats.php。UPSERT/日期函数已改写为 SQLite 方言。

// recordVisit 在响应后异步记账；ip 必须由调用方在 handler 返回前取好
// （net/http 的 Request 在 ServeHTTP 返回后不可再使用）。
func recordVisit(ip, kind string) {
	day := todayStr()
	shareIncr := 0
	if kind == "share" {
		shareIncr = 1
	}

	// UPSERT 原子计数，避免竞态丢计数；PG 要求列名用表名限定避免歧义
	dbExecute(
		"INSERT INTO visitor_stats (stat_date, visits, shares, ad_clicks) VALUES (?, 1, ?, 0) "+
			"ON CONFLICT (stat_date) DO UPDATE SET visits = visitor_stats.visits + 1, shares = visitor_stats.shares + ?",
		day, shareIncr, shareIncr)
	dbExecute(
		"INSERT INTO stats_totals (k, v) VALUES ('total_visits', 1) ON CONFLICT (k) DO UPDATE SET v = stats_totals.v + 1")
	if kind == "share" {
		dbExecute(
			"INSERT INTO stats_totals (k, v) VALUES ('total_shares', 1) ON CONFLICT (k) DO UPDATE SET v = stats_totals.v + 1")
	}

	if ip != "" {
		dbExecute("INSERT OR IGNORE INTO daily_ips (stat_date, ip) VALUES (?, ?)", day, ip)
		dbExecute(
			"INSERT INTO unique_ips (ip, first_seen, last_seen) VALUES (?, ?, ?) "+
				"ON CONFLICT (ip) DO UPDATE SET last_seen = excluded.last_seen",
			ip, day, day)
	}

	// 低频维护：每日首次访问时清理一次 180 天前的数据
	lastCleanup := int64(dbFetchColumnInt("SELECT v FROM stats_totals WHERE k = 'last_cleanup'"))
	if lastCleanup == 0 || time.Unix(lastCleanup, 0).Format("2006-01-02") != day {
		cutoff := time.Now().AddDate(0, 0, -180).Format("2006-01-02")
		dbDelete("visitor_stats", "stat_date < ?", cutoff)
		dbDelete("daily_ips", "stat_date < ?", cutoff)
		dbDelete("access_log", "time < ?", cutoff+" 00:00:00")
		dbExecute(
			"INSERT INTO stats_totals (k, v) VALUES ('last_cleanup', ?) ON CONFLICT (k) DO UPDATE SET v = excluded.v",
			time.Now().Unix())
	}
}

func recordAdClick() {
	day := todayStr()
	dbExecute(
		"INSERT INTO visitor_stats (stat_date, visits, shares, ad_clicks) VALUES (?, 0, 0, 1) "+
			"ON CONFLICT (stat_date) DO UPDATE SET ad_clicks = visitor_stats.ad_clicks + 1",
		day)
	dbExecute(
		"INSERT INTO stats_totals (k, v) VALUES ('total_ad_clicks', 1) ON CONFLICT (k) DO UPDATE SET v = stats_totals.v + 1")
}

// resetVisitorStats 清空选中的统计项（actions.php purge_stats）。
func resetVisitorStats(options map[string]bool) {
	clearAll := len(options) == 0 || options["all"]
	today := todayStr()

	// 1. 今日访问量
	if clearAll || options["today_visits"] {
		todayVisits := dbFetchColumnInt("SELECT visits FROM visitor_stats WHERE stat_date = ?", today)
		if todayVisits > 0 {
			dbExecute("UPDATE visitor_stats SET visits = 0 WHERE stat_date = ?", today)
			dbExecute("UPDATE stats_totals SET v = MAX(0, v - ?) WHERE k = 'total_visits'", todayVisits)
		}
	}

	// 2. 累计/历史访问量
	if clearAll || options["total_visits"] {
		dbExecute("DELETE FROM visitor_stats WHERE stat_date < ?", today)
		if options["today_visits"] || clearAll {
			dbExecute("DELETE FROM visitor_stats")
			dbDelete("stats_totals", "k = 'total_visits'")
		} else {
			todayVisits := dbFetchColumnInt("SELECT visits FROM visitor_stats WHERE stat_date = ?", today)
			dbExecute("UPDATE stats_totals SET v = ? WHERE k = 'total_visits'", todayVisits)
		}
	}

	// 3. 今日独立访客
	if clearAll || options["today_uv"] {
		dbExecute("DELETE FROM daily_ips WHERE stat_date = ?", today)
	}

	// 4. 累计独立访客
	if clearAll || options["total_uv"] {
		dbExecute("DELETE FROM unique_ips")
		if options["today_uv"] || clearAll {
			dbExecute("DELETE FROM daily_ips")
		} else {
			dbExecute("DELETE FROM daily_ips WHERE stat_date < ?", today)
			dbExecute("INSERT OR IGNORE INTO unique_ips (ip, first_seen, last_seen) "+
				"SELECT ip, stat_date, stat_date FROM daily_ips WHERE stat_date = ?", today)
		}
	}

	// 5. 今日下载次数
	if clearAll || options["today_downloads"] {
		adjustLinkClicksForDay(today, "GREATEST(0, click_count - ?)", func(linkID, cnt int) {
			dbExecute("UPDATE resource_links SET click_count = MAX(0, click_count - ?) WHERE id = ?", cnt, linkID)
		})
		dbExecute("DELETE FROM access_log WHERE event = 'link_click' AND date(time) = ?", today)
	}

	// 6. 累计下载次数
	if clearAll || options["total_downloads"] {
		if options["today_downloads"] || clearAll {
			dbExecute("UPDATE resource_links SET click_count = 0")
			dbExecute("DELETE FROM access_log WHERE event = 'link_click'")
		} else {
			dbExecute("DELETE FROM access_log WHERE event = 'link_click' AND date(time) < ?", today)
			dbExecute("UPDATE resource_links SET click_count = 0")
			adjustLinkClicksForDay(today, "?", func(linkID, cnt int) {
				dbExecute("UPDATE resource_links SET click_count = ? WHERE id = ?", cnt, linkID)
			})
		}
	}
}

// adjustLinkClicksForDay 从 access_log 里聚合当日 link_click，按回调调整链接计数。
// CASE 守卫空 context：SQLite 的 json_extract 与 PG 的 ::jsonb 遇非法 JSON 都会报错。
func adjustLinkClicksForDay(today string, _ string, apply func(linkID, cnt int)) {
	rows := dbFetchAll(
		"SELECT CASE WHEN context LIKE '{%' THEN json_extract(context, '$.link_id') END AS link_id, COUNT(*) AS cnt "+
			"FROM access_log WHERE event = 'link_click' AND date(time) = ? "+
			"AND CASE WHEN context LIKE '{%' THEN json_extract(context, '$.link_id') END IS NOT NULL GROUP BY link_id", today)
	for _, row := range rows {
		lid := anyInt(row["link_id"])
		cnt := intVal(row, "cnt")
		if lid > 0 && cnt > 0 {
			apply(lid, cnt)
		}
	}
}

type dayDetail struct {
	Visits    int
	Ips       int
	IPList    []string
	Downloads int
	Shares    int
	AdClicks  int
}

type ipSummary struct {
	Dates      []string
	ActiveDays int
	Downloads  int
}

type statsResult struct {
	TotalVisits    int
	TodayVisits    int
	TodayUniqueIPs int
	TotalUniqueIPs int
	TodayDownloads int
	TotalDownloads int
	ShareVisits    int
	TodayIPList    []string
	RecentDays     map[string]dayDetail
	RecentKeys     []string // 排序后的日期键
}

func computeStats() statsResult {
	today := todayStr()

	todayRow := dbFetchOne("SELECT * FROM visitor_stats WHERE stat_date = ?", today)
	totalVisits := dbFetchColumnInt("SELECT v FROM stats_totals WHERE k = 'total_visits'")
	totalShares := dbFetchColumnInt("SELECT v FROM stats_totals WHERE k = 'total_shares'")
	totalIPs := dbCount("unique_ips", "1")

	totalDownloads := dbFetchColumnInt("SELECT SUM(click_count) FROM resource_links")
	todayDownloads := dbFetchColumnInt(
		"SELECT COUNT(*) FROM access_log WHERE event = 'link_click' AND date(time) = ?", today)

	todayIPList := []string{}
	for _, row := range dbFetchAll("SELECT ip FROM daily_ips WHERE stat_date = ?", today) {
		todayIPList = append(todayIPList, str(row, "ip"))
	}

	// 近 7 天明细：一次取每日数据，避免循环内逐日查询
	cutoff := time.Now().AddDate(0, 0, -6).Format("2006-01-02")
	recentRows := dbFetchAll(
		"SELECT vs.*, (SELECT COUNT(*) FROM daily_ips di WHERE di.stat_date = vs.stat_date) AS ip_count "+
			"FROM visitor_stats vs WHERE vs.stat_date >= ? ORDER BY vs.stat_date ASC", cutoff)

	downloadsByDay := map[string]int{}
	for _, row := range dbFetchAll(
		"SELECT date(time) AS d, COUNT(*) AS cnt FROM access_log WHERE event = 'link_click' AND time >= ? GROUP BY date(time)",
		cutoff+" 00:00:00") {
		downloadsByDay[str(row, "d")] = intVal(row, "cnt")
	}
	ipsByDay := map[string][]string{}
	for _, row := range dbFetchAll("SELECT stat_date, ip FROM daily_ips WHERE stat_date >= ?", cutoff) {
		d := str(row, "stat_date")
		ipsByDay[d] = append(ipsByDay[d], str(row, "ip"))
	}

	recentDays := map[string]dayDetail{}
	var keys []string
	for _, row := range recentRows {
		dateKey := str(row, "stat_date")
		keys = append(keys, dateKey)
		recentDays[dateKey] = dayDetail{
			Visits:    intVal(row, "visits"),
			Ips:       intVal(row, "ip_count"),
			IPList:    ipsByDay[dateKey],
			Downloads: downloadsByDay[dateKey],
			Shares:    intVal(row, "shares"),
		}
	}
	sort.Strings(keys)

	return statsResult{
		TotalVisits:    totalVisits,
		TodayVisits:    intVal(todayRow, "visits"),
		TodayUniqueIPs: len(todayIPList),
		TotalUniqueIPs: totalIPs,
		TodayDownloads: todayDownloads,
		TotalDownloads: totalDownloads,
		ShareVisits:    totalShares,
		TodayIPList:    todayIPList,
		RecentDays:     recentDays,
		RecentKeys:     keys,
	}
}

type ipDetailResult struct {
	AllDays  map[string]dayDetail
	DayKeys  []string
	ByIP     map[string]ipSummary
	IPKeys   []string // 排序后的 IP
	TotalIPs int
}

func computeIpDetail() ipDetailResult {
	cutoff := time.Now().AddDate(0, 0, -6).Format("2006-01-02")

	dailyRows := dbFetchAll(
		"SELECT stat_date, ip FROM daily_ips WHERE stat_date >= ? ORDER BY stat_date DESC", cutoff)

	statsByDay := map[string]map[string]any{}
	for _, row := range dbFetchAll("SELECT * FROM visitor_stats WHERE stat_date >= ?", cutoff) {
		statsByDay[str(row, "stat_date")] = row
	}

	allDays := map[string]dayDetail{}
	var dayKeys []string
	byIP := map[string]ipSummary{}
	var ipKeys []string

	for _, row := range dailyRows {
		dateKey := str(row, "stat_date")
		ip := str(row, "ip")

		day, ok := allDays[dateKey]
		if !ok {
			dayStats := statsByDay[dateKey]
			day = dayDetail{
				Visits:   intVal(dayStats, "visits"),
				AdClicks: intVal(dayStats, "ad_clicks"),
				Shares:   intVal(dayStats, "shares"),
				IPList:   []string{},
			}
			dayKeys = append(dayKeys, dateKey)
		}
		day.IPList = append(day.IPList, ip)
		day.Ips = len(day.IPList)
		allDays[dateKey] = day

		entry, ok := byIP[ip]
		if !ok {
			entry = ipSummary{Dates: []string{}}
			ipKeys = append(ipKeys, ip)
		}
		entry.Dates = append(entry.Dates, dateKey)
		entry.ActiveDays++
		byIP[ip] = entry
	}

	for _, row := range dbFetchAll(
		"SELECT ip, COUNT(*) AS cnt FROM access_log WHERE event = 'link_click' AND time >= ? GROUP BY ip",
		cutoff+" 00:00:00") {
		ip := str(row, "ip")
		cnt := intVal(row, "cnt")
		entry, ok := byIP[ip]
		if !ok {
			entry = ipSummary{Dates: []string{}}
			ipKeys = append(ipKeys, ip)
		}
		entry.Downloads = cnt
		byIP[ip] = entry
	}

	// 与 PHP 版排序一致：下载量 desc → 活跃天数 desc → 最近活跃日期 desc
	sort.SliceStable(ipKeys, func(i, j int) bool {
		a, b := byIP[ipKeys[i]], byIP[ipKeys[j]]
		if a.Downloads != b.Downloads {
			return a.Downloads > b.Downloads
		}
		if a.ActiveDays != b.ActiveDays {
			return a.ActiveDays > b.ActiveDays
		}
		return firstDate(a.Dates) > firstDate(b.Dates)
	})
	sort.Sort(sort.Reverse(sort.StringSlice(dayKeys)))

	return ipDetailResult{
		AllDays:  allDays,
		DayKeys:  dayKeys,
		ByIP:     byIP,
		IPKeys:   ipKeys,
		TotalIPs: len(ipKeys),
	}
}

func firstDate(dates []string) string {
	if len(dates) == 0 {
		return ""
	}
	return dates[0]
}
