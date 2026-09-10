package main

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// POST 动作分发，对应 includes/actions.php。
// 入口统一做 CSRF 校验；匿名动作显式列出，其余全部要求管理员登录。

func timeNowAdd(d time.Duration) string {
	return time.Now().Add(d).Format(phpTimeLayout)
}

const timeHour = time.Hour

func formValue(r *http.Request, key string) string {
	return strings.TrimSpace(r.FormValue(key))
}

func handlePostAction(w http.ResponseWriter, r *http.Request) {
	// PostFormValue 只读表单体，避免 URL query 覆盖 action/csrf
	action := r.PostFormValue("action")
	if action == "" {
		action = r.PostFormValue("_action")
	}

	// 公告已读与安装动作为匿名可用；公告已读豁免 CSRF（无状态变更）
	if action != "read_announcement" && action != "install_db" {
		verifyPostCSRF(w, r)
	}

	switch action {
	case "install_db":
		actionInstallDB(w, r)
	case "login":
		actionLogin(w, r)
	case "logout":
		logoutAdmin(peekSession(r))
		redirect(w, "/index")
	case "set_password":
		actionSetPassword(w, r)
	case "unlock_share":
		actionUnlockShare(w, r)
	case "read_announcement":
		actionReadAnnouncement(w, r)
	default:
		// 以下操作需要管理员登录
		if !isAdminLoggedIn(peekSession(r)) {
			s := startSession(w, r)
			setFlash(s, "请先登录。", "error")
			redirect(w, "/login")
			return
		}
		switch action {
		case "create_folder":
			actionCreateFolder(w, r)
		case "rename_folder":
			actionRenameFolder(w, r)
		case "delete_folder":
			actionDeleteFolder(w, r)
		case "create_resource":
			actionCreateResource(w, r)
		case "update_resource":
			actionUpdateResource(w, r)
		case "move_resource":
			actionMoveResource(w, r)
		case "delete_resource":
			actionDeleteResource(w, r)
		case "reset_views":
			actionResetViews(w, r)
		case "toggle_resource":
			actionToggleResource(w, r)
		case "create_share":
			actionCreateShare(w, r)
		case "toggle_share":
			actionToggleShare(w, r)
		case "delete_share":
			actionDeleteShare(w, r)
		case "update_announcement":
			actionUpdateAnnouncement(w, r)
		case "force_announcement_ip":
			actionForceAnnouncementIP(w, r)
		case "delete_announcement_ip":
			actionDeleteAnnouncementIP(w, r)
		case "update_theme":
			actionUpdateTheme(w, r)
		case "import_theme":
			actionImportTheme(w, r)
		case "delete_theme":
			actionDeleteTheme(w, r)
		case "update_site_content":
			actionUpdateSiteContent(w, r)
		case "gw_save_node":
			actionGwSaveNode(w, r)
		case "gw_toggle_node":
			actionGwToggleNode(w, r)
		case "gw_delete_node":
			actionGwDeleteNode(w, r)
		case "gw_test_node":
			actionGwTestNode(w, r)
		case "gw_refresh_nodes":
			actionGwRefreshNodes(w, r)
		case "gw_delete_file":
			actionGwDeleteFile(w, r)
		case "gw_save_settings":
			actionGwSaveSettings(w, r)
		case "purge_stats":
			actionPurgeStats(w, r)
		case "link_click":
			actionLinkClick(w, r)
		default:
			s := peekSession(r)
			setFlash(s, "未知操作。", "error")
			redirect(w, "/index")
		}
	}
}

// verifyPostCSRF 对应 helpers.php verifyCsrf：失败回跳同源 Referer。
func verifyPostCSRF(w http.ResponseWriter, r *http.Request) {
	s := startSession(w, r)
	if verifyCSRF(s, r.PostFormValue("csrf")) {
		return
	}
	setFlash(s, "页面凭证已过期，请返回重试。", "error")
	target := "/index"
	if ref := r.Referer(); ref != "" {
		refURL, err1 := urlParse(ref)
		reqURL, err2 := urlParse(r.URL.String())
		if err1 == nil && err2 == nil && strings.EqualFold(refURL.Host, reqURL.Host) && refURL.Path != "" {
			target = refURL.Path
		}
	}
	redirect(w, target)
}

// ==================== 匿名动作 ====================

func actionLogin(w http.ResponseWriter, r *http.Request) {
	username := formValue(r, "username")
	password := r.FormValue("password")

	if !passwordIsInitialized() {
		redirect(w, "/set-password")
		return
	}
	s := startSession(w, r)

	if err := assertLoginAllowed(); err != nil {
		setFlash(s, err.Error(), "error")
		redirect(w, "/login")
		return
	}

	if loginAdmin(username, password) {
		regenerateID(w, r, s)
		s.AdminLoggedIn = true
		clearLoginFailures()
		writeAccessLog(r, "admin_login_success", map[string]any{"username": username})
		redirect(w, "/admin")
		return
	}

	markLoginFailure()
	writeAccessLog(r, "admin_login_failed", map[string]any{"username": username})
	setFlash(s, "登录失败，请检查用户名和密码。", "error")
	redirect(w, "/login")
}

func actionSetPassword(w http.ResponseWriter, r *http.Request) {
	username := formValue(r, "username")
	password := r.FormValue("password")
	confirm := r.FormValue("confirm_password")
	s := startSession(w, r)

	if passwordIsInitialized() {
		// 复用登录限流，否则此处会成为绕过锁定的爆破入口
		if err := assertLoginAllowed(); err != nil {
			setFlash(s, err.Error(), "error")
			redirect(w, "/set-password")
			return
		}
		currentUser := dbFetchColumnStr("SELECT username FROM users ORDER BY id LIMIT 1")
		current := r.FormValue("current_password")
		if current == "" || !loginAdmin(currentUser, current) {
			markLoginFailure()
			writeAccessLog(r, "set_password_denied", map[string]any{"username": username})
			setFlash(s, "当前密码不正确。", "error")
			redirect(w, "/set-password")
			return
		}
		clearLoginFailures()
	}

	if username == "" || password == "" {
		setFlash(s, "用户名和密码不能为空。", "error")
		redirect(w, "/set-password")
		return
	}
	if password != confirm {
		setFlash(s, "两次输入的密码不一致。", "error")
		redirect(w, "/set-password")
		return
	}

	setAdminPassword(username, password)
	regenerateID(w, r, s)
	s.AdminLoggedIn = true
	setFlash(s, "管理员账号设置成功。", "success")
	redirect(w, "/admin")
}

func actionUnlockShare(w http.ResponseWriter, r *http.Request) {
	token := r.FormValue("share_token")
	code := r.FormValue("share_code")
	share := getShareByToken(token)
	s := startSession(w, r)

	if share == nil {
		setFlash(s, "分享不存在。", "error")
		redirect(w, "/index")
		return
	}
	if str(share, "code") != "" &&
		subtle.ConstantTimeCompare([]byte(str(share, "code")), []byte(code)) == 1 {
		s.ShareAccess[token] = true
		writeAccessLog(r, "share_unlock_success", map[string]any{"token": token})
	} else {
		writeAccessLog(r, "share_unlock_failed", map[string]any{"token": token})
		setFlash(s, "提取码错误。", "error")
	}
	redirect(w, shareUrl(token))
}

func actionReadAnnouncement(w http.ResponseWriter, r *http.Request) {
	markAnnouncementRead(getClientIp(r))
	accept := r.Header.Get("Accept")
	if r.Header.Get("X-Requested-With") != "" || strings.Contains(accept, "application/json") {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"ok":true}`))
		return
	}
	redirect(w, "/index")
}

// ==================== 管理员动作：文件夹 ====================

func actionCreateFolder(w http.ResponseWriter, r *http.Request) {
	name := formValue(r, "folder_name")
	parentID := atoiOr(r.FormValue("parent_id"), 0)
	s := peekSession(r)

	if name == "" {
		setFlash(s, "文件夹名称不能为空。", "error")
	} else {
		createFolder(name, parentID)
		setFlash(s, "文件夹已创建。", "success")
	}
	redirect(w, adminUrl(parentID))
}

func actionRenameFolder(w http.ResponseWriter, r *http.Request) {
	id := atoiOr(r.FormValue("folder_id"), 0)
	newName := formValue(r, "new_name")
	s := peekSession(r)

	if id > 0 && newName != "" {
		renameFolder(id, newName)
		setFlash(s, "文件夹已重命名。", "success")
		redirect(w, "/admin")
		return
	}
	setFlash(s, "参数无效。", "error")
	redirect(w, "/admin")
}

func actionDeleteFolder(w http.ResponseWriter, r *http.Request) {
	id := atoiOr(r.FormValue("folder_id"), 0)
	s := peekSession(r)
	folder := getFolderById(id)
	if folder != nil {
		parentID := intVal(folder, "parent_id")
		deleteFolder(id)
		setFlash(s, "文件夹已删除。", "success")
		redirect(w, adminUrl(parentID))
		return
	}
	setFlash(s, "文件夹不存在。", "error")
	redirect(w, "/admin")
}

// ==================== 管理员动作：资源 ====================

// parseSubmittedLinks 解析表单多行网盘链接（create/update 共用）。
func parseSubmittedLinks(r *http.Request) ([]map[string]any, string) {
	platforms := r.Form["link_platform[]"]
	urls := r.Form["link_url[]"]
	codes := r.Form["link_code[]"]
	passwords := r.Form["link_password[]"]

	var links []map[string]any
	firstURL := ""
	for i, raw := range urls {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if firstURL == "" {
			firstURL = raw
		}
		platform := "other"
		if i < len(platforms) {
			platform = platforms[i]
		}
		parsed := parseCloudShareInput(raw, platform)
		linkURL := parsed["url"]
		if linkURL == "" {
			linkURL = raw
		}
		code := ""
		if i < len(codes) {
			code = strings.TrimSpace(codes[i])
		}
		if code == "" && parsed["code"] != "" {
			code = parsed["code"]
		}
		password := ""
		if i < len(passwords) {
			password = passwords[i]
		}
		links = append(links, map[string]any{
			"platform": parsed["platform"],
			"url":      linkURL,
			"code":     code,
			"password": password,
		})
	}
	return links, firstURL
}

// resolveSubmittedMediaFile 处理「完整内容」提交：优先经内置网关上传文件，其次取文本外链。
// 返回 (外链（"" 表示不变/清空）, 提示消息)。
func resolveSubmittedMediaFile(r *http.Request, resourceID int64, title string) (string, string) {
	_, files, _ := parseMultipartFormFiles(r, 8<<20)
	file := files["media_file"]
	if file == nil || (file.data == nil && file.tmpPath == "") {
		return normalizeSubmittedMediaUrl(r.FormValue("media_url")), ""
	}
	defer file.cleanup() // 成功失败都要清理（>32MB 的上传会落临时文件）
	link := gwUploadMedia(resourceID, title, file)
	if link == "" {
		detail := storageLastError()
		if detail != "" {
			return "", detail
		}
		return "", "完整内容存储失败，请检查后台「存储网关」的节点或本地存储配置。"
	}
	return link, ""
}

// getCoverUpload 取封面上传文件（如有）。
func getCoverUpload(r *http.Request) *multipartFileInfo {
	_, files, _ := parseMultipartFormFiles(r, 8<<20)
	return files["cover_file"]
}

func actionCreateResource(w http.ResponseWriter, r *http.Request) {
	folderID := atoiOr(r.FormValue("folder_id"), 0)
	title := formValue(r, "title")
	description := formValue(r, "description")
	coverInput := formValue(r, "cover_url")
	resourceType := r.FormValue("resource_type")
	if resourceType == "" {
		resourceType = "other"
	}
	tags := formValue(r, "tags")
	s := peekSession(r)

	if title == "" {
		setFlash(s, "资源标题不能为空。", "error")
		redirect(w, adminUrl(folderID))
		return
	}

	parsedLinks, firstURL := parseSubmittedLinks(r)

	// 先建资源拿到 id，封面/媒体文件按 id 命名，避免同名资源互相覆盖
	var folderArg any
	if folderID > 0 {
		folderArg = folderID
	}
	resourceID := createResource(map[string]any{
		"folder_id":     folderArg,
		"title":         title,
		"description":   description,
		"cover_url":     "",
		"media_url":     "",
		"resource_type": resourceType,
		"tags":          tags,
		"enabled":       1,
	})

	coverUpload := getCoverUpload(r)
	coverURL := resolveCoverForResource(resourceID, title, resourceType, coverInput, firstURL, coverUpload)

	update := map[string]any{"cover_url": coverURL}
	mediaURL, mediaNotice := resolveSubmittedMediaFile(r, resourceID, title)
	if mediaURL != "" {
		update["media_url"] = mediaURL
		// 分类与完整内容类型不符时自动纠正（如默认「视频」却传了图片）
		if inferred := mediaUrlResourceType(mediaURL); inferred != "" && inferred != resourceType {
			update["resource_type"] = inferred
			mediaNotice = strings.TrimSpace(mediaNotice + " 已按完整内容类型把分类调整为「" + getResourceTypeLabel(inferred) + "」。")
		}
	}
	updateResource(resourceID, update)

	for _, link := range parsedLinks {
		link["resource_id"] = resourceID
		addResourceLink(link)
	}

	setFlash(s, "资源已创建。"+mediaNotice, "success")
	redirect(w, adminUrl(folderID))
}

func actionUpdateResource(w http.ResponseWriter, r *http.Request) {
	id := int64(atoiOr(r.FormValue("resource_id"), 0))
	title := formValue(r, "title")
	description := formValue(r, "description")
	coverURL := formValue(r, "cover_url")
	if coverURL == "" {
		coverURL = formValue(r, "cover_url_current")
	}
	resourceType := r.FormValue("resource_type")
	if resourceType == "" {
		resourceType = "other"
	}
	tags := formValue(r, "tags")
	enabled := 0
	if r.FormValue("enabled") != "" {
		enabled = 1
	}
	folderID := atoiOr(r.FormValue("folder_id"), 0)
	s := peekSession(r)

	if id > 0 && title != "" {
		parsedLinks, firstURL := parseSubmittedLinks(r)

		coverUpload := getCoverUpload(r)
		coverURL = resolveCoverForResource(id, title, resourceType, coverURL, firstURL, coverUpload)
		update := map[string]any{
			"title":         title,
			"description":   description,
			"cover_url":     coverURL,
			"resource_type": resourceType,
			"tags":          tags,
			"enabled":       enabled,
		}
		mediaNotice := ""
		if r.FormValue("media_url_clear") != "" {
			update["media_url"] = ""
		} else {
			var mediaURL string
			mediaURL, mediaNotice = resolveSubmittedMediaFile(r, id, title)
			if mediaURL != "" {
				update["media_url"] = mediaURL
				if inferred := mediaUrlResourceType(mediaURL); inferred != "" && inferred != resourceType {
					update["resource_type"] = inferred
					mediaNotice = strings.TrimSpace(mediaNotice + " 已按完整内容类型把分类调整为「" + getResourceTypeLabel(inferred) + "」。")
				}
			}
		}
		// 所属目录：目标存在才写入，0 = 移到根目录
		if folderID == 0 || getFolderById(folderID) != nil {
			var folderArg any
			if folderID > 0 {
				folderArg = folderID
			}
			update["folder_id"] = folderArg
		}
		updateResource(id, update)

		dbDelete("resource_links", "resource_id = ?", id)
		for _, link := range parsedLinks {
			link["resource_id"] = id
			addResourceLink(link)
		}

		setFlash(s, "资源已更新。"+mediaNotice, "success")
	}
	redirect(w, "/admin?resource="+fmt.Sprintf("%d", id))
}

func actionMoveResource(w http.ResponseWriter, r *http.Request) {
	id := atoiOr(r.FormValue("resource_id"), 0)
	targetID := atoiOr(r.FormValue("folder_id"), 0)
	s := peekSession(r)
	resource := map[string]any(nil)
	if id > 0 {
		resource = getResourceById(id)
	}
	if resource == nil {
		setFlash(s, "资源不存在。", "error")
		redirect(w, "/admin")
		return
	}
	fromID := intVal(resource, "folder_id")
	if targetID > 0 && getFolderById(targetID) == nil {
		setFlash(s, "目标文件夹不存在。", "error")
		redirect(w, adminUrl(fromID))
		return
	}
	if fromID == targetID {
		setFlash(s, "资源已在该目录中，未做移动。", "success")
		redirect(w, adminUrl(targetID))
		return
	}
	var folderArg any
	if targetID > 0 {
		folderArg = targetID
	}
	updateResource(int64(id), map[string]any{"folder_id": folderArg})
	target := map[string]any(nil)
	if targetID > 0 {
		target = getFolderById(targetID)
	}
	dest := "根目录"
	if target != nil {
		dest = str(target, "path")
	}
	setFlash(s, "资源「"+str(resource, "title")+"」已移动到 "+dest+"。", "success")
	redirect(w, adminUrl(targetID))
}

func actionDeleteResource(w http.ResponseWriter, r *http.Request) {
	id := atoiOr(r.FormValue("resource_id"), 0)
	s := peekSession(r)
	resource := getResourceById(id)
	if resource != nil {
		folderID := intVal(resource, "folder_id")
		deleteResource(id)
		setFlash(s, "资源已删除。", "success")
		redirect(w, adminUrl(folderID))
		return
	}
	setFlash(s, "资源不存在。", "error")
	redirect(w, "/admin")
}

func actionResetViews(w http.ResponseWriter, r *http.Request) {
	id := atoiOr(r.FormValue("resource_id"), 0)
	s := peekSession(r)
	if id > 0 {
		dbExecute("UPDATE resources SET view_count = 0 WHERE id = ?", id)
		res := getResourceById(id)
		folderID := 0
		if res != nil {
			folderID = intVal(res, "folder_id")
		}
		setFlash(s, "浏览量已清零。", "success")
		redirect(w, adminUrl(folderID))
		return
	}
	redirect(w, "/admin")
}

func actionToggleResource(w http.ResponseWriter, r *http.Request) {
	id := atoiOr(r.FormValue("resource_id"), 0)
	s := peekSession(r)
	if id > 0 {
		res := getResourceById(id)
		toggleResourceEnabled(id)
		setFlash(s, "状态已切换。", "success")
		folderID := 0
		if res != nil {
			folderID = intVal(res, "folder_id")
		}
		redirect(w, adminUrl(folderID))
		return
	}
	redirect(w, "/admin")
}

// ==================== 管理员动作：分享 ====================

var expiresChoices = map[string]func() string{
	"7days":  func() string { return timeNowAdd(7 * 24 * timeHour) },
	"30days": func() string { return timeNowAdd(30 * 24 * timeHour) },
}

func actionCreateShare(w http.ResponseWriter, r *http.Request) {
	resourceID := atoiOr(r.FormValue("resource_id"), 0)
	title := formValue(r, "share_title")
	code := formValue(r, "share_code")
	expires := r.FormValue("share_expires")
	s := peekSession(r)

	if resourceID <= 0 {
		setFlash(s, "资源不存在。", "error")
		redirect(w, "/admin")
		return
	}

	var expiresAt any
	if gen, ok := expiresChoices[expires]; ok {
		expiresAt = gen()
	}

	token := addShare(map[string]any{
		"resource_id": resourceID,
		"title":       title,
		"code":        code,
		"expires_at":  expiresAt,
	})

	setFlash(s, "分享已创建，Token: "+token, "success")
	redirect(w, "/admin")
}

func actionToggleShare(w http.ResponseWriter, r *http.Request) {
	token := r.FormValue("share_token")
	s := peekSession(r)
	if token != "" {
		toggleShare(token)
		setFlash(s, "分享状态已切换。", "success")
	}
	redirect(w, "/admin")
}

func actionDeleteShare(w http.ResponseWriter, r *http.Request) {
	token := r.FormValue("share_token")
	s := peekSession(r)
	if token != "" {
		deleteShare(token)
		setFlash(s, "分享已删除。", "success")
	}
	redirect(w, "/admin")
}

// ==================== 管理员动作：公告与设置 ====================

func actionUpdateAnnouncement(w http.ResponseWriter, r *http.Request) {
	htmlContent := r.FormValue("announcement")
	days := atoiOr(r.FormValue("announcement_interval_days"), 1)
	saveAnnouncement(htmlContent, days)
	setFlash(peekSession(r), "公告已更新。", "success")
	redirect(w, "/admin#announcement")
}

func actionForceAnnouncementIP(w http.ResponseWriter, r *http.Request) {
	targetIP := formValue(r, "ip")
	s := peekSession(r)
	if targetIP != "" {
		setAnnouncementForcePopup(targetIP, true)
		setFlash(s, "已设置 IP ["+targetIP+"] 下次访问时强制再次弹出公告。", "success")
	}
	redirect(w, "/admin#announcement")
}

func actionDeleteAnnouncementIP(w http.ResponseWriter, r *http.Request) {
	targetIP := formValue(r, "ip")
	s := peekSession(r)
	if targetIP != "" {
		dbDelete("announcement_reads", "ip = ?", targetIP)
		setFlash(s, "已重置 IP ["+targetIP+"] 的已读记录。", "success")
	}
	redirect(w, "/admin#announcement")
}

func actionUpdateSiteContent(w http.ResponseWriter, r *http.Request) {
	settings := loadSiteSettings()
	s := peekSession(r)

	// 社交链接
	socialNames := r.Form["social_name[]"]
	socialUrls := r.Form["social_url[]"]
	var socialLinks []socialLink
	for i := range socialUrls {
		if i >= len(socialNames) {
			break
		}
		name := strings.TrimSpace(socialNames[i])
		linkURL := strings.TrimSpace(socialUrls[i])
		// 仅接受 http/https 外链，杜绝 javascript: 等伪协议入库
		if name != "" && linkURL != "" && validExternalUrl(linkURL) {
			socialLinks = append(socialLinks, socialLink{Name: name, URL: linkURL})
		}
	}
	if socialLinks == nil {
		socialLinks = []socialLink{}
	}
	settings.SocialLinks = socialLinks

	cols := atoiOr(r.FormValue("grid_columns"), 4)
	if cols != 3 && cols != 5 {
		cols = 4
	}
	settings.GridColumns = cols

	saveSiteSettings(settings)
	setFlash(s, "站点设置已更新。", "success")
	redirect(w, "/admin")
}

// ==================== 管理员动作：前台主题 ====================

// actionUpdateTheme 切换前台生效主题（内置或已导入的自定义主题）。
func actionUpdateTheme(w http.ResponseWriter, r *http.Request) {
	themeID := formValue(r, "theme_id")
	s := peekSession(r)
	known := themeID == "" || isBuiltinTheme(themeID) || getCustomTheme(themeID) != nil
	if !known {
		setFlash(s, "主题不存在或已被删除。", "error")
		redirect(w, "/admin#themes")
		return
	}
	saveSettingKv("default_theme", themeID)
	clearAllPublicPageCaches() // 分享页缓存里带旧主题，需失效
	setFlash(s, "前台主题已切换。", "success")
	redirect(w, "/admin#themes")
}

// actionImportTheme 导入自定义主题压缩包（仅管理员）：解析 zip → 校验 → 入库。
func actionImportTheme(w http.ResponseWriter, r *http.Request) {
	s := peekSession(r)
	_, files, err := parseMultipartFormFiles(r, 4<<20) // 主题包 ≤2MB，给点余量
	if err != nil {
		setFlash(s, "上传读取失败，请重试。", "error")
		redirect(w, "/admin#themes")
		return
	}
	file := files["theme_zip"]
	if file == nil || len(file.data) == 0 {
		setFlash(s, "请选择主题压缩包（.zip）。", "error")
		redirect(w, "/admin#themes")
		return
	}
	if !strings.EqualFold(filepath.Ext(file.origName), ".zip") {
		setFlash(s, "仅支持 .zip 压缩包。", "error")
		redirect(w, "/admin#themes")
		return
	}
	name, err := importThemeZip(file.data, file.origName)
	if err != nil {
		setFlash(s, "主题导入失败："+err.Error(), "error")
		redirect(w, "/admin#themes")
		return
	}
	setFlash(s, "主题「"+name+"」已导入，可在上方选择启用。", "success")
	redirect(w, "/admin#themes")
}

// actionDeleteTheme 删除导入的主题；若正在使用则回落默认 mono。
func actionDeleteTheme(w http.ResponseWriter, r *http.Request) {
	themeID := formValue(r, "theme_id")
	s := peekSession(r)
	if isBuiltinTheme(themeID) || !strings.HasPrefix(themeID, "custom_") {
		setFlash(s, "内置主题不可删除。", "error")
		redirect(w, "/admin#themes")
		return
	}
	var kept []customTheme
	removed := ""
	for _, t := range loadCustomThemes() {
		if t.ID == themeID {
			removed = t.Name
			continue
		}
		kept = append(kept, t)
	}
	saveCustomThemes(kept)
	if loadSiteSettings().DefaultTheme == themeID {
		saveSettingKv("default_theme", "")
		clearAllPublicPageCaches()
	}
	if removed != "" {
		setFlash(s, "主题「"+removed+"」已删除。", "success")
	} else {
		setFlash(s, "主题不存在。", "error")
	}
	redirect(w, "/admin#themes")
}

func actionPurgeStats(w http.ResponseWriter, r *http.Request) {
	targets := r.Form["clear_targets[]"]
	options := map[string]bool{}
	for _, t := range targets {
		options[t] = true
	}
	resetVisitorStats(options)
	setFlash(peekSession(r), "选中的统计数据已清除。", "success")
	redirect(w, "/admin#stats")
}

func actionLinkClick(w http.ResponseWriter, r *http.Request) {
	linkID := atoiOr(r.FormValue("link_id"), 0)
	if linkID > 0 {
		incrementLinkClick(linkID)
		link := dbFetchOne("SELECT url FROM resource_links WHERE id = ?", linkID)
		if link != nil {
			writeAccessLog(r, "link_click", map[string]any{"link_id": linkID, "url": str(link, "url")})
			redirect(w, str(link, "url"))
			return
		}
	}
	redirect(w, "/index")
}
