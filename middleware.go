package main

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// 中间件：CORS、X-Request-Id、限流。

// ==================== CORS ====================

// corsAllowedOrigins 允许的跨域来源白名单（逗号分隔）。
// 生产环境应配置具体域名，空串表示不允许跨域。
var corsAllowedOrigins string

func init() {
	corsAllowedOrigins = getCorsOrigins()
}

func getCorsOrigins() string {
	if v := os.Getenv("CORS_ORIGINS"); v != "" {
		return v
	}
	return ""
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && corsAllowedOrigins != "" {
			allowed := strings.Split(corsAllowedOrigins, ",")
			for _, a := range allowed {
				if strings.TrimSpace(a) == origin {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
					w.Header().Set("Access-Control-Allow-Headers", "Range, If-None-Match")
					w.Header().Set("Access-Control-Allow-Credentials", "true")
					w.Header().Set("Access-Control-Max-Age", "86400")
					break
				}
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ==================== X-Request-Id ====================

func requestIdMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("X-Request-Id")
		if reqID == "" {
			reqID = generateRequestID()
		}
		w.Header().Set("X-Request-Id", reqID)
		next.ServeHTTP(w, r)
	})
}

func generateRequestID() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// ==================== 内存限流器 ====================

type rateLimiter struct {
	mu       sync.Mutex
	clients  map[string]*rateLimitEntry
	rate     int           // 窗口内允许请求数
	window   time.Duration // 时间窗口
	burst    int           // 突发容量
}

type rateLimitEntry struct {
	tokens   int
	lastTime time.Time
}

var (
	// 全局限流器：每 IP 每秒 10 请求，突发 20
	globalLimiter = &rateLimiter{
		clients: make(map[string]*rateLimitEntry),
		rate:    10,
		window:  time.Second,
		burst:   20,
	}
)

func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	entry, exists := rl.clients[key]
	if !exists {
		rl.clients[key] = &rateLimitEntry{tokens: rl.burst - 1, lastTime: now}
		return true
	}

	elapsed := now.Sub(entry.lastTime)
	refill := int(elapsed.Seconds() * float64(rl.rate))
	if refill > 0 {
		entry.tokens += refill
		if entry.tokens > rl.burst {
			entry.tokens = rl.burst
		}
		entry.lastTime = now
	}

	if entry.tokens <= 0 {
		return false
	}
	entry.tokens--
	return true
}

// 清理过期条目（每分钟执行一次）
func (rl *rateLimiter) cleanup() {
	for {
		time.Sleep(time.Minute)
		rl.mu.Lock()
		cutoff := time.Now().Add(-2 * rl.window)
		for k, v := range rl.clients {
			if v.lastTime.Before(cutoff) {
				delete(rl.clients, k)
			}
		}
		rl.mu.Unlock()
	}
}

func init() {
	go globalLimiter.cleanup()
}

func rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := getClientIp(r)
		if !globalLimiter.allow(ip) {
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"type":"about:blank","title":"Too Many Requests","status":429,"detail":"Rate limit exceeded. Please try again later."}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ==================== Secure Cookie 改进 ====================

// isSecureRequest 判断请求是否通过 HTTPS（支持反向代理 X-Forwarded-Proto）。
func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto == "https" {
		return true
	}
	if proto := r.Header.Get("X-Forwarded-Scheme"); proto == "https" {
		return true
	}
	return false
}
