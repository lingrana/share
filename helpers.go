package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 通用助手，对应 includes/helpers.php（去掉 GD/封面以外的网络部分放本文件，
// 封面生成与抓取在 covers.go）。

const phpTimeLayout = "2006-01-02 15:04:05"

func nowStr() string   { return time.Now().Format(phpTimeLayout) }
func todayStr() string { return time.Now().Format("2006-01-02") }

// ==================== URL 构造（helpers.php） ====================
// Go 版统一使用无后缀短路径：/index /admin /folder/1 /resource/2 /share/<token>；
// 同时保留 .html 后缀兼容旧链接。

func shareUrl(token string) string {
	return "/share/" + url.PathEscape(token)
}

func adminUrl(folderID int) string {
	if folderID <= 0 {
		return "/admin"
	}
	return fmt.Sprintf("/admin?folder=%d", folderID)
}

// promoClickUrl / decodePromoClickUrl：HMAC 签名的外跳链接，防止开放跳转。
func promoClickUrl(rawURL string) string {
	secret := appConfig.RouteSecret
	if secret == "" {
		return "/index"
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("ad|" + rawURL))
	sig := hex.EncodeToString(mac.Sum(nil))[:16]
	body := base64.RawURLEncoding.EncodeToString([]byte(rawURL))
	return "/promo/" + sig + "-" + body
}

var promoTokenRe = regexp.MustCompile(`^([a-f0-9]{16})-([A-Za-z0-9_-]+)$`)

func decodePromoClickUrl(token string) (string, error) {
	secret := appConfig.RouteSecret
	// 未配置路由密钥时拒绝一切外跳，防止使用公开默认密钥伪造签名
	if secret == "" {
		return "", fmt.Errorf("无效广告链接。")
	}
	m := promoTokenRe.FindStringSubmatch(token)
	if m == nil {
		return "", fmt.Errorf("无效广告链接。")
	}
	body := strings.NewReplacer("-", "+", "_", "/").Replace(m[2])
	if pad := len(body) % 4; pad > 0 {
		body += strings.Repeat("=", 4-pad)
	}
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return "", fmt.Errorf("无效广告链接。")
	}
	target := string(raw)
	if !validExternalUrl(target) {
		return "", fmt.Errorf("无效广告链接。")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("ad|" + target))
	expected := hex.EncodeToString(mac.Sum(nil))[:16]
	if subtle.ConstantTimeCompare([]byte(expected), []byte(m[1])) != 1 {
		return "", fmt.Errorf("广告链接校验失败。")
	}
	return target, nil
}

// ==================== 客户端 IP ====================

// getClientIp：Cloudflare 的 CF-Connecting-IP 不可伪造（CF 用真实 TCP 对端覆写），
// 未过 CF 的直连请求回退 RemoteAddr。仅用于统计展示，无安全决策依赖。
func getClientIp(r *http.Request) string {
	if r == nil {
		return "" // 网关客户端失败日志无请求上下文
	}
	ip := strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))
	if ip != "" && isValidIP(ip) {
		return ip
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 && !strings.Contains(host, "]") {
		host = host[:i]
	}
	host = strings.Trim(host, "[]")
	if strings.Contains(host, "]") { // [::1]:61201 形式
		host = strings.Trim(strings.Split(host, "]")[0], "[")
	}
	return host
}

func isValidIP(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) == 4 {
		ok := true
		for _, p := range parts {
			n, err := strconv.Atoi(p)
			if err != nil || n < 0 || n > 255 {
				ok = false
			}
		}
		if ok {
			return true
		}
	}
	return strings.Contains(s, ":") // IPv6 粗判，够统计用
}

// ==================== 访问日志 ====================

func writeAccessLog(r *http.Request, event string, context map[string]any) {
	ctxJSON := ""
	if len(context) > 0 {
		b, err := json.Marshal(context)
		if err == nil {
			ctxJSON = string(b)
		}
	}
	uri := ""
	if r != nil {
		uri = r.URL.RequestURI()
	}
	dbInsert("access_log", map[string]any{
		"time":    nowStr(),
		"event":   event,
		"ip":      getClientIp(r),
		"uri":     uri,
		"context": ctxJSON,
	})
}

// ==================== 外链校验（settings.php） ====================

var schemeRe = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.\-]*://`)

func normalizeExternalUrl(raw string) string {
	u := strings.TrimSpace(raw)
	if u == "" {
		return ""
	}
	if strings.HasPrefix(u, "//") {
		return "https:" + u
	}
	if !schemeRe.MatchString(u) {
		return "https://" + u
	}
	return u
}

func validExternalUrl(raw string) bool {
	u := strings.TrimSpace(raw)
	if u == "" {
		return false
	}
	if strings.HasPrefix(u, "/") {
		return true
	}
	parsed, err := url.Parse(normalizeExternalUrl(u))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	return scheme == "http" || scheme == "https"
}

// ==================== 网盘分享文本解析（helpers.php） ====================

var (
	reURLInText  = regexp.MustCompile(`(?i)https?://[^\s]+`)
	rePwdParam   = regexp.MustCompile(`(?i)[?&]pwd=([^&\s]+)`)
	reExtractPwd = regexp.MustCompile(`提取码[:：\s]*([A-Za-z0-9]{3,8})`)
	reSharePwd   = regexp.MustCompile(`(?:密码|口令)[:：\s]*([A-Za-z0-9]{3,8})`)
	reBareDomain = regexp.MustCompile(`^[A-Za-z0-9._~:/?#\[\]@!$&'()*+,;=%-]+$`)
	reBaiduShare = regexp.MustCompile(`(?i)pan\.baidu\.com/s/([^?/]+)`)
)

func trimTrailingJunk(s string) string {
	return strings.TrimRight(s, ".,;）)")
}

// parseCloudShareInput 从粘贴文本里提取链接、提取码并识别平台。
func parseCloudShareInput(raw, platform string) map[string]string {
	raw = strings.TrimSpace(raw)
	link := ""
	code := ""

	if m := reURLInText.FindString(raw); m != "" {
		link = trimTrailingJunk(m)
	}
	if m := rePwdParam.FindStringSubmatch(raw); m != nil {
		code = decodeURIComponent(m[1])
	} else if m := reExtractPwd.FindStringSubmatch(raw); m != nil {
		code = m[1]
	} else if m := reSharePwd.FindStringSubmatch(raw); m != nil {
		code = m[1]
	}

	if link == "" && reBareDomain.MatchString(raw) && strings.Contains(raw, ".") {
		if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
			link = raw
		} else {
			link = "https://" + raw
		}
	}

	if platform == "other" || platform == "" {
		platform = detectCloudPlatform(firstNonEmpty(link, raw))
	}
	return map[string]string{"url": link, "code": code, "platform": platform}
}

func decodeURIComponent(s string) string {
	if v, err := url.QueryUnescape(s); err == nil {
		return v
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func detectCloudPlatform(text string) string {
	t := strings.ToLower(text)
	switch {
	case strings.Contains(t, "pan.baidu.com") || strings.Contains(t, "yun.baidu.com"):
		return "baidu"
	case strings.Contains(t, "pan.quark.cn") || strings.Contains(t, "quark.cn"):
		return "quark"
	case strings.Contains(t, "alipan.com") || strings.Contains(t, "aliyundrive.com"):
		return "aliyun"
	case strings.Contains(t, "lanzou"):
		return "lanzou"
	case strings.Contains(t, "115.com") || strings.Contains(t, "115cdn.com") || strings.Contains(t, "anxia.com"):
		return "115"
	}
	return "other"
}

// ==================== 平台展示配置（resources.php） ====================

type platformInfo struct {
	Name  string
	Color string
}

func getPlatformConfig() map[string]platformInfo {
	return map[string]platformInfo{
		"baidu":  {"百度网盘", "#306CFF"},
		"quark":  {"夸克网盘", "#6A5ACD"},
		"aliyun": {"阿里云盘", "#FF6A00"},
		"lanzou": {"蓝奏云", "#2E8B57"},
		"115":    {"115网盘", "#E74C3C"},
		"other":  {"其他", "#888"},
	}
}

func getPlatformName(platform string) string {
	if info, ok := getPlatformConfig()[platform]; ok {
		return info.Name
	}
	return platform
}

// ==================== 资源类型（helpers.php） ====================

func getResourceTypeLabel(typ string) string {
	switch typ {
	case "video":
		return "视频"
	case "audio":
		return "音频"
	case "image":
		return "图片"
	case "document":
		return "文档"
	default:
		return "其他"
	}
}

var mediaExtTypeMap = map[string]string{
	"jpg": "image", "jpeg": "image", "png": "image", "gif": "image", "webp": "image",
	"svg": "image", "bmp": "image", "avif": "image", "ico": "image",
	"mp3": "audio", "wav": "audio", "ogg": "audio", "oga": "audio", "m4a": "audio",
	"flac": "audio", "aac": "audio", "opus": "audio",
	"mp4": "video", "webm": "video", "mkv": "video", "mov": "video", "avi": "video",
	"m4v": "video", "ogv": "video",
	"txt": "document", "md": "document", "pdf": "document",
}

// mediaUrlResourceType 按完整内容扩展名推断资源分类。
func mediaUrlResourceType(mediaURL string) string {
	if mediaURL == "" {
		return ""
	}
	ext := strings.ToLower(pathExt(mediaURL))
	return mediaExtTypeMap[ext]
}

func pathExt(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "."); i >= 0 {
		if j := strings.LastIndexAny(p[:i], "/?"); j >= 0 && p[j] == '?' {
			return ""
		}
		return p[i+1:]
	}
	return ""
}

// ==================== HTTP GET（helpers.php httpGet） ====================

var httpClientInsecure bool

func httpGet(rawURL string, timeout time.Duration, headers map[string]string) string {
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			// 与 PHP 版一致：默认校验证书，缺 CA 包的主机可临时配置 http_insecure
			TLSClientConfig: tlsConfigOrInsecure(),
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 { // 对齐 PHP CURLOPT_MAXREDIRS
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil || len(body) == 0 {
		return ""
	}
	// PHP 版：非空 body 且状态码 < 500 都可用
	if resp.StatusCode >= 500 {
		return ""
	}
	return string(body)
}

// ==================== 文档完整内容展示（helpers.php documentMediaView） ====================

// documentMediaView 决定文档类 media_url 的展示方式：
//
//	text  → 正文直接渲染；image → 按图片展示；link → 降级为打开/下载链接
func documentMediaView(mediaURL string) (string, string) {
	ext := strings.ToLower(pathExt(mediaURL))
	safe := strings.HasPrefix(mediaURL, "/") || reURLInText.MatchString(mediaURL)
	if !safe {
		return "link", mediaURL
	}
	if ext != "txt" && ext != "md" {
		return "image", mediaURL
	}

	// 本站遗留媒体目录：路径固定前缀，杜绝拼接读取任意文件
	if strings.HasPrefix(mediaURL, "/assets/media/") {
		decoded := decodedURLPath(mediaURL)
		file := filepath.Join(dataDir, "assets", "media", strings.TrimPrefix(decoded, "/assets/media/"))
		if b, err := os.ReadFile(file); err == nil {
			return "text", string(b)
		}
		return "link", mediaURL
	}

	// 网关外链：网关支持 Range，限制拉取量，超限降级为链接
	if reURLInText.MatchString(mediaURL) {
		text := httpGet(mediaURL, 12*time.Second, map[string]string{"Range": "bytes=0-2097151"})
		if text != "" && len(text) < 2097152 {
			return "text", text
		}
	}
	return "link", mediaURL
}

// normalizeSubmittedMediaUrl 完整内容地址归一：仅接受 http(s) 外链或站内绝对路径。
func normalizeSubmittedMediaUrl(raw string) string {
	u := strings.TrimSpace(raw)
	if u == "" {
		return ""
	}
	if strings.HasPrefix(u, "/") {
		return u
	}
	if reURLInText.MatchString(u) && (strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")) {
		return u
	}
	return ""
}

// ==================== 杂项 ====================

func randomHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func decodedURLPath(p string) string {
	if v, err := url.PathUnescape(p); err == nil {
		return v
	}
	return p
}

func likeEscape(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(v)
}

func numberFormat(n int) string {
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	return strings.Join(parts, ",")
}

func logErr(format string, args ...any) {
	log.Printf(format, args...)
}

func jsonMarshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// escapeXMLText 对应 PHP htmlspecialchars(ENT_XML1|ENT_QUOTES)，用于 SVG 内嵌文本。
func escapeXMLText(s string) string {
	return html.EscapeString(s)
}
