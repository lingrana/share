package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// 会话存储在内存中（单进程部署，重启即失效，与 PHP 默认 session 文件行为相当）。
// 存放的只有登录态/CSRF/分享解锁/闪消息，不落库。
const sessionCookieName = "sssid"

type Session struct {
	ID            string
	CSRFToken     string
	AdminLoggedIn bool
	ShareAccess   map[string]bool
	Flash         string
	FlashType     string
	lastActive    time.Time
}

var (
	sessionMu    sync.Mutex
	sessionStore = map[string]*Session{}
)

func newSessionID() string {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func newSession() *Session {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	return &Session{
		ID:          newSessionID(),
		CSRFToken:   hex.EncodeToString(buf),
		ShareAccess: map[string]bool{},
		lastActive:  time.Now(),
	}
}

// startSession 等价 bootstrap.php 的 startAppSession：取回或创建会话并写 Cookie。
func startSession(w http.ResponseWriter, r *http.Request) *Session {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	if c, err := r.Cookie(sessionCookieName); err == nil {
		if s, ok := sessionStore[c.Value]; ok {
			s.lastActive = time.Now()
			return s
		}
	}
	s := newSession()
	sessionStore[s.ID] = s
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    s.ID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecureRequest(r),
	})
	return s
}

// peekSession 只读会话（不创建），用于公开页缓存判断与闪消息读取。
func peekSession(r *http.Request) *Session {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	if c, err := r.Cookie(sessionCookieName); err == nil {
		if s, ok := sessionStore[c.Value]; ok {
			s.lastActive = time.Now()
			return s
		}
	}
	return nil
}

// regenerateID 等价 session_regenerate_id(true)：登录后换发会话 ID。
func regenerateID(w http.ResponseWriter, r *http.Request, s *Session) {
	sessionMu.Lock()
	delete(sessionStore, s.ID)
	s.ID = newSessionID()
	s.lastActive = time.Now()
	sessionStore[s.ID] = s
	sessionMu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    s.ID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecureRequest(r),
	})
}

func destroySession(w http.ResponseWriter, r *http.Request) {
	sessionMu.Lock()
	if c, err := r.Cookie(sessionCookieName); err == nil {
		delete(sessionStore, c.Value)
	}
	sessionMu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func setFlash(s *Session, message, typ string) {
	if s == nil {
		return
	}
	sessionMu.Lock()
	s.Flash = message
	s.FlashType = typ
	sessionMu.Unlock()
}

// pullFlash 取走并清空闪消息。
func pullFlash(s *Session) (string, string) {
	if s == nil {
		return "", "success"
	}
	sessionMu.Lock()
	defer sessionMu.Unlock()
	f, t := s.Flash, s.FlashType
	if f != "" {
		s.Flash = ""
	}
	if t == "" {
		t = "success"
	}
	return f, t
}

func verifyCSRF(s *Session, token string) bool {
	if s == nil || token == "" {
		return false
	}
	sessionMu.Lock()
	defer sessionMu.Unlock()
	return subtle.ConstantTimeCompare([]byte(token), []byte(s.CSRFToken)) == 1
}

// sessionGC 每小时清理 7 天不活跃的会话，防内存缓慢增长。
func sessionGC() {
	for {
		time.Sleep(time.Hour)
		sessionMu.Lock()
		cutoff := time.Now().Add(-7 * 24 * time.Hour)
		for id, s := range sessionStore {
			if s.lastActive.Before(cutoff) {
				delete(sessionStore, id)
			}
		}
		sessionMu.Unlock()
	}
}

// needsSession 与 bootstrap.php 的判定一致：POST、后台、登录、设密码、统计、安装。
func needsSession(r *http.Request) bool {
	if r.Method == http.MethodPost {
		return true
	}
	p := r.URL.Path
	switch p {
	case "/admin", "/login", "/set-password", "/stats", "/install",
		"/admin.html", "/login.html", "/set-password.html", "/stats.html", "/install.html":
		return true
	}
	return false
}

// ==================== 公开页缓存（page_cache.php 的内存版） ====================

type cacheEntry struct {
	body  string
	expat time.Time
}

var (
	pageCacheMu sync.Mutex
	pageCache   = map[string]cacheEntry{}
)

const publicPageTTL = 60 * time.Second

func pageCacheHeaders(w http.ResponseWriter, ttlSeconds int) {
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d, s-maxage=%d, stale-while-revalidate=30", ttlSeconds, ttlSeconds))
	w.Header().Set("Vary", "Accept-Encoding")
}

// servePublicPageCache 命中条件与 PHP 一致：GET 且不带会话 Cookie。
func servePublicPageCache(w http.ResponseWriter, r *http.Request, key string) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if _, err := r.Cookie(sessionCookieName); err == nil {
		return false
	}
	pageCacheMu.Lock()
	entry, ok := pageCache[key]
	pageCacheMu.Unlock()
	if !ok || time.Now().After(entry.expat) {
		return false
	}
	pageCacheHeaders(w, 60)
	w.Header().Set("X-Page-Cache", "HIT")
	_, _ = w.Write([]byte(entry.body))
	return true
}

func savePublicPageCache(key, body string) {
	if body == "" {
		return
	}
	pageCacheMu.Lock()
	pageCache[key] = cacheEntry{body: body, expat: time.Now().Add(publicPageTTL)}
	pageCacheMu.Unlock()
}

func clearPublicPageCache(key string) {
	pageCacheMu.Lock()
	delete(pageCache, key)
	pageCacheMu.Unlock()
}

func clearAllPublicPageCaches() {
	pageCacheMu.Lock()
	pageCache = map[string]cacheEntry{}
	pageCacheMu.Unlock()
}
