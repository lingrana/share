package main

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed assets templates
var embeddedFS embed.FS

var (
	tmplSets map[string]*template.Template
	assetsV  string
)

// 页面模板集合：layout + 页面 + 公共片段（stats/ip 面板被后台和独立统计页共用）
var tmplPages = []string{
	"install", "home_public", "home_admin", "preview", "share", "login",
	"setpassword", "stats", "ipdetail", "404",
}

func initTemplates() {
	funcs := template.FuncMap{
		"numfmt": numberFormat,
		"safe":   func(s string) template.HTML { return template.HTML(s) },
		"jsattr": func(s string) template.HTMLAttr { return template.HTMLAttr(s) },
		"upper":  strings.ToUpper,
		"date10": func(s string) string {
			if len(s) >= 10 {
				return s[:10]
			}
			return s
		},
		"asset":        func(name string) string { return "/assets/" + name + "?v=" + assetsV },
		"plat":         getPlatformName,
		"typelbl":      getResourceTypeLabel,
		"splitTags":    splitTags,
		"seq":          seqInts,
		"default":      defaultVal,
		"docMediaView": docMediaViewData,
		"jsCopyAttr":   jsCopyAttr,
		"jsWriteAttr":  jsWriteAttr,
		"hasPrefix":    strings.HasPrefix,
		"resTypes": func() map[string]string {
			return map[string]string{
				"video": "🎬 视频", "document": "📄 文档", "audio": "🎵 音频",
				"image": "🖼️ 图片", "other": "📦 其他",
			}
		},
		"platformConfig": func() map[string]platformInfo { return getPlatformConfig() },
	}
	tmplSets = map[string]*template.Template{}
	for _, page := range tmplPages {
		set := template.Must(template.New("layout.tmpl").Funcs(funcs).ParseFS(embeddedFS,
			"templates/layout.tmpl",
			"templates/"+page+".tmpl",
			"templates/partials/stats_panel.tmpl",
			"templates/partials/ip_panel.tmpl",
		))
		tmplSets[page] = set
	}

	// 静态资源版本号：取内嵌资产内容的哈希前 8 位，内容变更自动失效缓存
	h := sha256.New()
	for _, name := range []string{"assets/style.css", "assets/app.js", "assets/site-icon.png"} {
		b, err := embeddedFS.ReadFile(name)
		if err != nil {
			continue
		}
		h.Write(b)
	}
	assetsV = hex.EncodeToString(h.Sum(nil))[:8]
}

// splitTags 把逗号分隔的标签串切成数组（模板里配合 range 使用）。
func splitTags(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

// seqInts 生成 1..n 的整数序列（分页循环用）。
func seqInts(n int) []int {
	if n <= 0 {
		return nil
	}
	out := make([]int, n)
	for i := range out {
		out[i] = i + 1
	}
	return out
}

// defaultVal 值为零值时返回默认值。
func defaultVal(v, def any) any {
	switch t := v.(type) {
	case int:
		if t == 0 {
			return def
		}
	case int64:
		if t == 0 {
			return def
		}
	case string:
		if t == "" {
			return def
		}
	case nil:
		return def
	}
	return v
}

// docMediaViewData 包装 documentMediaView 为模板友好的 struct。
type docMedia struct {
	Kind    string
	Payload string
}

func docMediaView(mediaURL string) docMedia {
	kind, payload := documentMediaView(mediaURL)
	return docMedia{Kind: kind, Payload: payload}
}

// docMediaViewData 供 FuncMap 使用（签名须为单返回值 func）。
func docMediaViewData(mediaURL string) docMedia { return docMediaView(mediaURL) }

// jsJSONAttr 复刻 PHP json_encode(JSON_HEX_TAG|HEX_APOS|HEX_QUOT|HEX_AMP) 的
// JS 字面量形态，用于 onclick 属性里的字符串字面量。
func jsJSONAttr(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return `""`
	}
	out := strings.TrimRight(buf.String(), "\n")
	return strings.NewReplacer(
		"<", `\u003C`, ">", `\u003E`, "&", `\u0026`,
		"'", `\u0027`, `"`, `\u0022`,
	).Replace(out)
}

// jsCopyAttr 生成 onclick="copyText(<json>, '<label>')" 整属性，用于标签内注入。
func jsCopyAttr(text, label string) template.HTMLAttr {
	lbl := label
	if lbl == "" {
		lbl = "-"
	}
	return template.HTMLAttr(`onclick="copyText(` + jsJSONAttr(text) + `, ` + jsJSONAttr(lbl) + `)"`)
}

// jsWriteAttr 生成 onclick="navigator.clipboard.writeText(<json>)" 整属性。
func jsWriteAttr(text string) template.HTMLAttr {
	return template.HTMLAttr(`onclick="navigator.clipboard.writeText(` + jsJSONAttr(text) + `)"`)
}

// renderPage 两段式渲染：先渲染页面块，再套布局，写出响应。
func renderPage(w http.ResponseWriter, page string, data map[string]any) {
	body, ok := renderPageString(page, data)
	if !ok {
		http.Error(w, "模板渲染失败", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(body))
}

// renderPageString 渲染完整页面为字符串（公开页缓存需要拿到底层字节）。
func renderPageString(page string, data map[string]any) (string, bool) {
	set, ok := tmplSets[page]
	if !ok {
		logErr("render: template not found: %s", page)
		return "", false
	}
	var content bytes.Buffer
	if err := set.ExecuteTemplate(&content, page, data); err != nil {
		logErr("render %s: %v", page, err)
		return "", false
	}
	title, _ := data["Title"].(string)
	if title == "" {
		title = appConfig.SiteName
	}
	csrf, _ := data["CSRFToken"].(string)
	layoutData := map[string]any{
		"Content":   template.HTML(content.String()),
		"Title":     title,
		"CSRFToken": csrf,
	}
	// 主题模型：data-theme = 管理员选择的主题（后台控制台固定 mono）；
	// data-mode = 明暗变体，访客/管理员各自在浏览器里切换（app.js 记 localStorage）。
	isAdmin, _ := data["IsAdmin"].(bool)
	if isAdmin {
		layoutData["ThemeID"] = "mono"
		layoutData["ThemeCSS"] = template.CSS("")
	} else {
		themeAttr, themeCSS, _ := resolveTheme(loadSiteSettings())
		layoutData["ThemeID"] = themeAttr
		layoutData["ThemeCSS"] = template.CSS(themeCSS)
	}
	layoutData["ThemeMode"] = "light" // 初始值；app.js 按访客偏好覆写为 dark
	var buf bytes.Buffer
	if err := set.ExecuteTemplate(&buf, "layout.tmpl", layoutData); err != nil {
		logErr("layout %s: %v", page, err)
		return "", false
	}
	return buf.String(), true
}

// serveEmbeddedAsset 处理 /assets/ 下的内嵌静态文件与磁盘上的封面/遗留媒体。
func serveEmbeddedAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/assets/")
	if name == "" || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	// 封面与遗留媒体在运行时写入 dataDir/assets，优先磁盘
	for _, kind := range []string{"covers", "media"} {
		if strings.HasPrefix(name, kind+"/") {
			local := path.Join(dataDir, "assets", path.Clean(name))
			if fileExists(local) {
				w.Header().Set("Cache-Control", "public, max-age=2592000")
				http.ServeFile(w, r, local)
				return
			}
			http.NotFound(w, r)
			return
		}
	}
	data, err := fs.ReadFile(embeddedFS, "assets/"+name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch {
	case strings.HasSuffix(name, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case strings.HasSuffix(name, ".js"):
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	case strings.HasSuffix(name, ".png"):
		w.Header().Set("Content-Type", "image/png")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("Cache-Control", "public, max-age=2592000")
	_, _ = w.Write(data)
}
