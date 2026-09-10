package main

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// 路由与请求分发，对应 index.php + .htaccess 的伪静态规则。

var (
	reFolder   = regexp.MustCompile(`^/folder/(\d+)(?:\.html)?$`)
	reResource = regexp.MustCompile(`^/resource/(\d+)(?:\.html)?$`)
	reLink     = regexp.MustCompile(`^/link/(\d+)$`)
	reShare    = regexp.MustCompile(`^/share/([A-Za-z0-9_-]+)(?:\.html)?$`)
	rePromo    = regexp.MustCompile(`^/promo/([A-Za-z0-9_-]+-[A-Za-z0-9_-]+)(?:\.html)?$`)
)

func redirect(w http.ResponseWriter, url string) {
	w.Header().Set("Location", url)
	w.WriteHeader(http.StatusFound)
}

func mainHandler(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path

	// POST 请求体统一设上限（审查规范 2.10#5/#6）：网关直传在请求内同步进行，给足大文件余量
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 2<<30) // 2 GiB
	}

	// 安装模式：未选择数据库前，除安装页外一律重定向到 /install
	if !dbConfigured() {
		if r.Method == http.MethodPost {
			handlePostAction(w, r)
			return
		}
		if p == "/install" || p == "/install.html" {
			handleInstallPage(w, r)
			return
		}
		redirect(w, "/install")
		return
	}

	if r.Method == http.MethodPost {
		handlePostAction(w, r)
		return
	}

	// 安装锁：已安装后 /install 一律 404（对应 PHP 版行为）
	if p == "/install" || p == "/install.html" {
		http.NotFound(w, r)
		return
	}

	switch p {
	case "/", "/index", "/index.html":
		handlePublicHome(w, r)
		return
	case "/admin", "/admin.html":
		handleAdmin(w, r)
		return
	case "/login", "/login.html":
		handleLoginPage(w, r)
		return
	case "/set-password", "/set-password.html":
		handleSetPasswordPage(w, r)
		return
	case "/stats", "/stats.html":
		handleStatsPage(w, r)
		return
	case "/folder", "/folder.html":
		// 兼容：跳回首页
		redirect(w, "/index")
		return
	}

	if m := reFolder.FindStringSubmatch(p); m != nil {
		id, _ := strconv.Atoi(m[1])
		handlePublicHome(w, r, id)
		return
	}
	if m := reResource.FindStringSubmatch(p); m != nil {
		id, _ := strconv.Atoi(m[1])
		handleResourceDetail(w, r, id)
		return
	}
	if m := reLink.FindStringSubmatch(p); m != nil {
		id, _ := strconv.Atoi(m[1])
		handleLinkClick(w, r, id)
		return
	}
	if m := reShare.FindStringSubmatch(p); m != nil {
		handleSharePage(w, r, m[1])
		return
	}
	if m := rePromo.FindStringSubmatch(p); m != nil {
		handlePromoClick(w, r, m[1])
		return
	}

	renderNotFound(w, r)
}

// handlePublicHome 前台浏览/搜索（index.php 公共浏览段）。
func handlePublicHome(w http.ResponseWriter, r *http.Request, folderIDs ...int) {
	folderID := 0
	if len(folderIDs) > 0 {
		folderID = folderIDs[0]
	}
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	page := maxInt(1, atoiOr(r.URL.Query().Get("page"), 1))

	data := map[string]any{
		"IsAdmin":      false,
		"Search":       search,
		"Flash":        "",
		"FlashType":    "success",
		"Breadcrumbs":  []breadcrumb{},
		"SubFolders":   []map[string]any{},
		"Resources":    pagedResult{Items: []map[string]any{}, Total: 0, Pages: 1, Page: 1},
		"FolderCounts": map[int]int{},
	}

	if search != "" {
		data["Resources"] = searchResources(search, page, 24, true)
	} else {
		if folderID <= 0 {
			if root := getRootFolder(); root != nil {
				folderID = intVal(root, "id")
			}
		}
		if folderID > 0 {
			data["CurrentFolder"] = getFolderById(folderID)
			data["Breadcrumbs"] = buildFolderBreadcrumbs(folderID)
			data["SubFolders"] = getChildFolders(folderID)
			data["FolderCounts"] = getDescendantResourceCounts(folderID)
			if folderID > 0 && getFolderById(folderID) != nil {
				data["Resources"] = getResourcesByFolder(folderID, page, 24, "", true)
			}
		}
	}
	data["SiteSettings"] = loadSiteSettings()
	data["ClientIP"] = getClientIp(r)
	data["ShowAnnouncement"] = shouldShowAnnouncementToIp(getClientIp(r), data["SiteSettings"].(siteSettings))

	s := peekSession(r)
	flash, flashType := pullFlash(s)
	data["Flash"], data["FlashType"] = flash, flashType

	renderPage(w, "home_public", data)
	clientIP := getClientIp(r)
	go recordVisit(clientIP, "public")
}

// handleResourceDetail 资源详情页（index.php mode=resource）。
func handleResourceDetail(w http.ResponseWriter, r *http.Request, id int) {
	resource := getResourceById(id)
	if resource == nil || (intVal(resource, "enabled") == 0 && !isAdminLoggedIn(peekSession(r))) {
		renderNotFound(w, r)
		return
	}
	var folderID int
	if fid := intVal(resource, "folder_id"); fid > 0 {
		folderID = fid
	}
	data := map[string]any{
		"Resource":     resource,
		"Links":        getResourceLinks(id),
		"Folder":       getFolderById(folderID),
		"Breadcrumbs":  buildFolderBreadcrumbs(folderID),
		"SiteSettings": loadSiteSettings(),
	}
	renderPage(w, "preview", data)
	if isAdminLoggedIn(peekSession(r)) && r.URL.Query().Get("admin_preview") == "1" {
		// 管理员预览不计数
		return
	}
	go incrementViewCount(id)
}

// handleLinkClick 点击链接跳转（mode=link）。
func handleLinkClick(w http.ResponseWriter, r *http.Request, id int) {
	if id > 0 {
		incrementLinkClick(id)
		link := dbFetchOne("SELECT url FROM resource_links WHERE id = ?", id)
		if link != nil {
			writeAccessLog(r, "link_click", map[string]any{"link_id": id, "url": str(link, "url")})
			redirect(w, str(link, "url"))
			return
		}
	}
	redirect(w, "/index")
}

// handlePromoClick 广告点击外跳（mode=promo_click）。
func handlePromoClick(w http.ResponseWriter, r *http.Request, token string) {
	target, err := decodePromoClickUrl(token)
	if err != nil {
		s := startSession(w, r)
		setFlash(s, err.Error(), "error")
		redirect(w, "/index")
		return
	}
	recordAdClick()
	writeAccessLog(r, "ad_click", map[string]any{"url": target})
	redirect(w, target)
}

// handleSharePage 分享页（mode=share）。
func handleSharePage(w http.ResponseWriter, r *http.Request, token string) {
	cacheKey := "share:" + token
	var s *Session

	share := getShareByToken(token)
	if share == nil {
		renderNotFound(w, r)
		return
	}

	// 带提取码的分享需要会话；公开分享保持可缓存
	hasSessionCookie := false
	if _, err := r.Cookie(sessionCookieName); err == nil {
		hasSessionCookie = true
	}
	publicCacheable := str(share, "code") == "" && !hasSessionCookie
	if !publicCacheable {
		s = startSession(w, r)
	} else {
		s = peekSession(r)
		if servePublicPageCache(w, r, cacheKey) {
			go recordVisit(getClientIp(r), "share")
			return
		}
	}

	if !isShareEnabled(share) {
		ss := startSession(w, r)
		setFlash(ss, "该分享已禁用。", "error")
		redirect(w, "/index")
		return
	}
	if shareIsExpired(share) {
		ss := startSession(w, r)
		setFlash(ss, "该分享已过期。", "error")
		redirect(w, "/index")
		return
	}

	unlocked := str(share, "code") == "" || (s != nil && s.ShareAccess[token])
	resource := getResourceById(intVal(share, "resource_id"))
	// 资源已下架时按「分享失效」处理（管理员预览除外）
	if resource != nil && intVal(resource, "enabled") == 0 && !isAdminLoggedIn(s) {
		resource = nil
	}
	links := []map[string]any{}
	if resource != nil {
		links = getResourceLinks(intVal(share, "resource_id"))
	}

	data := map[string]any{
		"Share":        share,
		"Token":        token,
		"Unlocked":     unlocked,
		"Resource":     resource,
		"Links":        links,
		"SiteSettings": loadSiteSettings(),
		"Title":        "专属分享 // " + str(share, "title") + " - " + appConfig.SiteName,
	}
	flash, flashType := pullFlash(s)
	data["Flash"], data["FlashType"] = flash, flashType
	if s != nil {
		data["CSRFToken"] = s.CSRFToken
	}

	if publicCacheable {
		// 与 page_cache.php 一致：匿名 GET 的公开分享页缓存 60s
		if body, ok := renderPageString("share", data); ok {
			savePublicPageCache(cacheKey, body)
			pageCacheHeaders(w, 60)
			w.Header().Set("X-Page-Cache", "MISS")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(body))
		} else {
			http.Error(w, "模板渲染失败", http.StatusInternalServerError)
			return
		}
	} else {
		renderPage(w, "share", data)
	}
	go recordVisit(getClientIp(r), "share")
}

// renderNotFound 404 页。
func renderNotFound(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	data := map[string]any{
		"SiteSettings": loadSiteSettings(),
		"Title":        "404 页面未找到 - " + appConfig.SiteName,
	}
	flash, flashType := pullFlash(peekSession(r))
	data["Flash"], data["FlashType"] = flash, flashType
	renderPage(w, "404", data)
}

// handleThumb 缩略图占位（外链模式无缩略图）。
func handleThumb(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="100" height="100" viewBox="0 0 100 100"><rect fill="#f0f0f0" width="100" height="100"/><text x="50" y="55" text-anchor="middle" fill="#999" font-size="14">No Image</text></svg>`))
}

func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
