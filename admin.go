package main

import (
	"net/http"
	"strings"
)

// 后台页面：登录页、设密码页、管理面板、独立统计页。
// 对应 index.php 的 mode=admin / login / set_password / stats 分支。

// handleLoginPage 登录页。
func handleLoginPage(w http.ResponseWriter, r *http.Request) {
	s := startSession(w, r)
	flash, flashType := pullFlash(s)
	renderPage(w, "login", map[string]any{
		"Flash":     flash,
		"FlashType": flashType,
		"CSRFToken": s.CSRFToken,
		"Title":     "管理员登录 - " + appConfig.SiteName,
	})
}

// handleSetPasswordPage 设密码页（无管理员时是初始化，已有管理员时须验证当前密码）。
func handleSetPasswordPage(w http.ResponseWriter, r *http.Request) {
	s := startSession(w, r)
	flash, flashType := pullFlash(s)
	hasAdmin := passwordIsInitialized()
	title := "初始化管理员设置 - " + appConfig.SiteName
	if hasAdmin {
		title = "修改管理员密码 - " + appConfig.SiteName
	}
	renderPage(w, "setpassword", map[string]any{
		"Flash":           flash,
		"FlashType":       flashType,
		"HasAdmin":        hasAdmin,
		"CurrentUsername": dbFetchColumnStr("SELECT username FROM users ORDER BY id LIMIT 1"),
		"CSRFToken":       s.CSRFToken,
		"Title":           title,
	})
}

// handleStatsPage 独立统计页（需管理员）。
func handleStatsPage(w http.ResponseWriter, r *http.Request) {
	s := startSession(w, r)
	if !isAdminLoggedIn(s) {
		redirect(w, "/login")
		return
	}
	flash, flashType := pullFlash(s)
	renderPage(w, "stats", map[string]any{
		"Stats":        computeStats(),
		"IPDetail":     computeIpDetail(),
		"SiteSettings": loadSiteSettings(),
		"Flash":        flash,
		"FlashType":    flashType,
		"CSRFToken":    s.CSRFToken,
		"Title":        "访问统计 - " + appConfig.SiteName,
	})
}

// handleAdmin 管理面板（index.php mode=admin）。
func handleAdmin(w http.ResponseWriter, r *http.Request) {
	s := startSession(w, r)
	if !isAdminLoggedIn(s) {
		renderPage(w, "login", map[string]any{
			"CSRFToken": s.CSRFToken,
			"Title":     "管理员登录 - " + appConfig.SiteName,
		})
		return
	}

	flash, flashType := pullFlash(s)
	q := r.URL.Query()
	page := maxInt(1, atoiOr(q.Get("page"), 1))
	adminSearch := strings.TrimSpace(q.Get("q"))
	currentFolderID := atoiOr(q.Get("folder"), 0)
	currentResourceID := atoiOr(q.Get("resource"), 0)

	data := map[string]any{
		"IsAdmin":      true,
		"Flash":        flash,
		"FlashType":    flashType,
		"SiteSettings": loadSiteSettings(),
		"Stats":        computeStats(),
		"IPDetail":     computeIpDetail(),
	}

	if currentResourceID > 0 {
		// 编辑资源
		editing := getResourceById(currentResourceID)
		data["EditingResource"] = editing
		if editing != nil {
			data["EditingLinks"] = getResourceLinks(currentResourceID)
			if fid := intVal(editing, "folder_id"); fid > 0 {
				data["Breadcrumbs"] = buildFolderBreadcrumbs(fid)
				data["CurrentFolder"] = getFolderById(fid)
			}
		} else {
			data["EditingLinks"] = []map[string]any{}
		}
		data["Folders"] = dbFetchAll("SELECT id, name, path FROM folders ORDER BY path ASC")
	} else {
		if currentFolderID > 0 {
			data["CurrentFolder"] = getFolderById(currentFolderID)
			if getFolderById(currentFolderID) == nil {
				currentFolderID = 0
			}
		} else {
			if root := getRootFolder(); root != nil {
				currentFolderID = intVal(root, "id")
				data["CurrentFolder"] = root
			}
		}

		if currentFolderID > 0 {
			data["Breadcrumbs"] = buildFolderBreadcrumbs(currentFolderID)
			data["SubFolders"] = getChildFolders(currentFolderID)
			data["FolderCounts"] = getDescendantResourceCounts(currentFolderID)
			data["Resources"] = getResourcesByFolder(currentFolderID, page, 24, adminSearch, false)
		} else {
			data["Resources"] = pagedResult{Items: []map[string]any{}, Total: 0, Pages: 1, Page: 1}
		}
		if adminSearch != "" {
			// 后台搜索框：在当前目录树内搜（与 PHP getResourcesByFolder(search) 一致）
			data["Resources"] = getResourcesByFolder(currentFolderID, page, 24, adminSearch, false)
		}
		data["ResourceCount"] = map[string]int{
			"count": map[bool]int{true: getFolderResourceCount(currentFolderID), false: getResourceCount()}[currentFolderID > 0],
		}
		data["AllShares"] = loadShares()
		// 「移动资源」弹窗的目录下拉需要全量目录列表（编辑表单也用同一份）
		data["Folders"] = dbFetchAll("SELECT id, name, path FROM folders ORDER BY path ASC")
	}
	data["CurrentFolderID"] = currentFolderID
	data["Search"] = adminSearch
	data["Page"] = page
	data["Title"] = "管理后台 - " + appConfig.SiteName
	data["ReadIps"] = getAnnouncementReadIps(100)
	data["CSRFToken"] = s.CSRFToken
	data["DBDriver"] = appConfig.DB.Driver
	data["DBHost"] = appConfig.DB.Host
	data["DBPort"] = appConfig.DB.Port
	data["DBName"] = appConfig.DB.DBName
	data["ActiveThemeID"] = loadSiteSettings().DefaultTheme
	data["BuiltinThemes"] = builtinThemes()
	data["CustomThemes"] = loadCustomThemes()

	// 「存储网关」选项卡：容量汇总、节点列表、文件索引、编辑中的节点
	gwSummary, gwNodeViews, gwFileViews := gwAdminViews()
	data["GwSummary"] = gwSummary
	data["GwNodeViews"] = gwNodeViews
	data["GwFileViews"] = gwFileViews
	if editID := atoiOr(r.URL.Query().Get("gw_edit"), 0); editID > 0 {
		data["GwEditing"] = gwGetNode(int64(editID))
	}

	renderPage(w, "home_admin", data)
}
