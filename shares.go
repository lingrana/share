package main

import (
	"time"
)

// 分享逻辑，对应 includes/shares.php。

func loadShares() []map[string]any {
	return dbFetchAll("SELECT * FROM shares ORDER BY created_at DESC")
}

func getShareByToken(token string) map[string]any {
	if token == "" {
		return nil
	}
	return dbFetchOne("SELECT * FROM shares WHERE token = ?", token)
}

func addShare(data map[string]any) string {
	token := randomHex(8)
	var expiresAt any
	if data["expires_at"] != nil && data["expires_at"] != "" {
		expiresAt = data["expires_at"]
	}
	dbInsert("shares", map[string]any{
		"token":       token,
		"resource_id": intVal(data, "resource_id"),
		"title":       data["title"],
		"code":        data["code"],
		"created_at":  nowStr(),
		"expires_at":  expiresAt,
		"enabled":     1,
	})
	return token
}

func deleteShare(token string) {
	dbDelete("shares", "token = ?", token)
	clearPublicPageCache("share:" + token)
}

func toggleShare(token string) {
	share := getShareByToken(token)
	if share == nil {
		return
	}
	next := 1
	if intVal(share, "enabled") != 0 {
		next = 0
	}
	dbUpdate("shares", map[string]any{"enabled": next}, "token = ?", token)
	clearPublicPageCache("share:" + token)
}

func isShareEnabled(share map[string]any) bool {
	if share == nil {
		return false
	}
	return intVal(share, "enabled") != 0
}

func shareIsExpired(share map[string]any) bool {
	expiresAt := str(share, "expires_at")
	if expiresAt == "" {
		return false
	}
	t, err := time.ParseInLocation(phpTimeLayout, expiresAt, time.Local)
	if err != nil {
		return false
	}
	return t.Before(time.Now())
}
