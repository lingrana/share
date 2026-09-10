package main

import (
	"encoding/json"
	"strconv"
	"time"
)

// 站点设置与公告，对应 includes/settings.php。
// 设置以 k/v 行存 settings 表；loadSiteSettings 每请求调用，直接查库（SQLite 本地读开销极低）。

type socialLink struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type siteSettings struct {
	Announcement             string
	AnnouncementIntervalDays int
	SocialLinks              []socialLink
	GridColumns              int
	DefaultTheme             string // 前台主题 id（空 = mono 默认；自定义主题为 custom_*）
	// 内置存储网关（对应 share-storage gateway 的运行设置）
	GwLocalEnabled      bool  // 本地磁盘兜底存储
	GwLocalTotalBytes   int64 // 本地容量上限，0 = 不限制
	GwLocalReserveBytes int64 // 本地保底剩余空间，低于此值不再写本地
	GwChunkSize         int64 // 分片上传单片大小
	GwMaxFileBytes      int64 // 单文件上限
}

func defaultSiteSettings() siteSettings {
	return siteSettings{
		AnnouncementIntervalDays: 1,
		SocialLinks:              []socialLink{},
		GridColumns:              4,
		GwLocalEnabled:           false,
		GwLocalTotalBytes:        0,
		GwLocalReserveBytes:      300 << 20,
		GwChunkSize:              2 << 20,
		GwMaxFileBytes:           1 << 30,
	}
}

func loadSiteSettings() siteSettings {
	s := defaultSiteSettings()
	for _, row := range dbFetchAll("SELECT k, v FROM settings") {
		k, v := str(row, "k"), str(row, "v")
		switch k {
		case "announcement":
			s.Announcement = v
		case "announcement_interval_days":
			n := parseIntSafe(v)
			if n < 0 {
				n = 0
			}
			s.AnnouncementIntervalDays = n
		case "social_links":
			var links []socialLink
			if err := json.Unmarshal([]byte(v), &links); err == nil {
				s.SocialLinks = links
			} else {
				s.SocialLinks = []socialLink{}
			}
		case "grid_columns":
			n := parseIntSafe(v)
			if n == 3 || n == 5 {
				s.GridColumns = n
			} else {
				s.GridColumns = 4
			}
		case "gw_local_enabled":
			s.GwLocalEnabled = v == "1"
		case "gw_local_total_bytes":
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
				s.GwLocalTotalBytes = n
			}
		case "gw_local_reserve_bytes":
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				s.GwLocalReserveBytes = n
			}
		case "gw_chunk_size":
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				s.GwChunkSize = n
			}
		case "gw_max_file_bytes":
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				s.GwMaxFileBytes = n
			}
		case "default_theme":
			s.DefaultTheme = v
		}
	}
	return s
}

func saveSiteSettings(s siteSettings) {
	socialJSON, _ := json.Marshal(s.SocialLinks)
	localEnabled := "0"
	if s.GwLocalEnabled {
		localEnabled = "1"
	}
	kv := map[string]string{
		"announcement":               s.Announcement,
		"announcement_interval_days": itoaSafe(maxInt(0, s.AnnouncementIntervalDays)),
		"social_links":               string(socialJSON),
		"grid_columns":               itoaSafe(s.GridColumns),
		"gw_local_enabled":           localEnabled,
		"gw_local_total_bytes":       strconv.FormatInt(maxInt64(0, s.GwLocalTotalBytes), 10),
		"gw_local_reserve_bytes":     strconv.FormatInt(maxInt64(1, s.GwLocalReserveBytes), 10),
		"gw_chunk_size":              strconv.FormatInt(maxInt64(1, s.GwChunkSize), 10),
		"gw_max_file_bytes":          strconv.FormatInt(maxInt64(1, s.GwMaxFileBytes), 10),
		"default_theme":              s.DefaultTheme,
	}
	for k, v := range kv {
		saveSettingKv(k, v)
	}
	clearAllPublicPageCaches()
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func saveAnnouncement(html string, intervalDays int) {
	saveSettingKv("announcement", sanitizeAnnouncementHtml(html))
	saveSettingKv("announcement_interval_days", itoaSafe(maxInt(0, intervalDays)))
}

// shouldShowAnnouncementToIp 对应 settings.php 同名函数。
func shouldShowAnnouncementToIp(ip string, s siteSettings) bool {
	if s.Announcement == "" {
		return false
	}
	if ip == "" {
		return true
	}
	read := dbFetchOne("SELECT * FROM announcement_reads WHERE ip = ?", ip)
	if read == nil {
		return true
	}
	if intVal(read, "force_popup") != 0 {
		return true
	}
	if s.AnnouncementIntervalDays <= 0 {
		return true // 0 天表示每次访问都弹
	}
	readAt, err := time.ParseInLocation(phpTimeLayout, str(read, "read_at"), time.Local)
	if err != nil {
		return true
	}
	return time.Since(readAt).Hours()/24 >= float64(s.AnnouncementIntervalDays)
}

func markAnnouncementRead(ip string) {
	if ip == "" {
		return
	}
	existing := dbFetchOne("SELECT ip FROM announcement_reads WHERE ip = ?", ip)
	if existing != nil {
		dbUpdate("announcement_reads", map[string]any{"read_at": nowStr(), "force_popup": 0}, "ip = ?", ip)
	} else {
		dbInsert("announcement_reads", map[string]any{"ip": ip, "read_at": nowStr(), "force_popup": 0})
	}
}

func setAnnouncementForcePopup(ip string, force bool) {
	if ip == "" {
		return
	}
	fv := 0
	if force {
		fv = 1
	}
	existing := dbFetchOne("SELECT ip FROM announcement_reads WHERE ip = ?", ip)
	if existing != nil {
		dbUpdate("announcement_reads", map[string]any{"force_popup": fv}, "ip = ?", ip)
	} else {
		dbInsert("announcement_reads", map[string]any{
			"ip": ip, "read_at": time.Unix(0, 0).Format(phpTimeLayout), "force_popup": fv,
		})
	}
}

func getAnnouncementReadIps(limit int) []map[string]any {
	return dbFetchAll("SELECT * FROM announcement_reads ORDER BY read_at DESC LIMIT ?", limit)
}

func parseIntSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			// 允许负号开头
			if c == '-' && n == 0 {
				continue
			}
			break
		}
		n = n*10 + int(c-'0')
	}
	if len(s) > 0 && s[0] == '-' {
		n = -n
	}
	return n
}

func itoaSafe(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
