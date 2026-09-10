package main

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// 后台「存储网关」选项卡动作：节点增删改测、文件删除、网关运行设置。
// 全部经 handlePostAction 的管理员鉴权 + CSRF 校验后分发到这里。

func gwAdminRedirect(w http.ResponseWriter, anchor string) {
	target := "/admin"
	if anchor != "" {
		target += "#" + anchor
	}
	redirect(w, target)
}

// gwAdminViews 组装「存储网关」选项卡的展示数据：容量汇总、节点列表、文件索引。
func gwAdminViews() (summary map[string]any, nodeViews []map[string]any, fileViews []map[string]any) {
	s := loadSiteSettings()
	totalUsed := int64(dbFetchColumnInt("SELECT COALESCE(SUM(size_bytes), 0) FROM gw_files"))
	fileCount := dbFetchColumnInt("SELECT COUNT(*) FROM gw_files")

	nodeFree := int64(0)
	nodeTotal := int64(0)
	nodeCount := 0
	for _, n := range gwListNodes(true) {
		nodeCount++
		free := n.FreeBytes
		if n.TotalBytes > 0 {
			free = n.TotalBytes - n.UsedBytes
			if free < 0 {
				free = 0
			}
		}
		nodeFree += free
		nodeTotal += n.TotalBytes
	}

	localFree := int64(0)
	localUnlimited := s.GwLocalTotalBytes <= 0
	if !localUnlimited {
		localFree = s.GwLocalTotalBytes - gwLocalUsedBytes()
		if localFree < 0 {
			localFree = 0
		}
	}

	summary = map[string]any{
		"FileCount":     fileCount,
		"UsedStr":       gwFormatBytes(totalUsed),
		"LocalEnabled":  s.GwLocalEnabled,
		"LocalTotalStr": map[bool]string{true: "不限制", false: gwFormatBytes(s.GwLocalTotalBytes)}[localUnlimited],
		"LocalFreeStr":  map[bool]string{true: "不限制", false: gwFormatBytes(localFree)}[localUnlimited],
		"NodeCount":     nodeCount,
		"NodeFreeStr":   gwFormatBytes(nodeFree),
		"NodeTotalStr":  gwFormatBytes(nodeTotal),
		"ChunkStr":      gwFormatBytes(s.GwChunkSize),
		"MaxStr":        gwFormatBytes(s.GwMaxFileBytes),
		"ReserveStr":    gwFormatBytes(s.GwLocalReserveBytes),
		// 表单回显用原始字节（gwParseBytesInput 只解析整数与 K/M/G 后缀）
		"LocalTotalRaw": strconv.FormatInt(s.GwLocalTotalBytes, 10),
		"ReserveRaw":    strconv.FormatInt(s.GwLocalReserveBytes, 10),
		"ChunkRaw":      strconv.FormatInt(s.GwChunkSize, 10),
		"MaxRaw":        strconv.FormatInt(s.GwMaxFileBytes, 10),
	}

	nodeViews = []map[string]any{}
	for _, n := range gwListNodes(false) {
		free := n.FreeBytes
		if n.TotalBytes > 0 {
			free = n.TotalBytes - n.UsedBytes
			if free < 0 {
				free = 0
			}
		}
		lastSeen := n.LastSeenAt
		if lastSeen == "" {
			lastSeen = "从未上报"
		}
		nodeViews = append(nodeViews, map[string]any{
			"ID": n.ID, "Name": n.Name, "BaseURL": n.BaseURL, "Token": n.Token,
			"Enabled": n.Enabled, "AntiBot": n.AntiBot,
			"FreeStr": gwFormatBytes(free), "TotalStr": gwFormatBytes(n.TotalBytes),
			"UsedStr": gwFormatBytes(n.UsedBytes), "LastSeen": lastSeen,
		})
	}

	fileViews = []map[string]any{}
	nodeNames := map[int64]string{0: "本地"}
	for _, n := range gwListNodes(false) {
		nodeNames[n.ID] = n.Name
	}
	for _, row := range dbFetchAll("SELECT * FROM gw_files ORDER BY created_at DESC, file_key ASC LIMIT 100") {
		nodeID := int64(intVal(row, "node_id"))
		name, ok := nodeNames[nodeID]
		if !ok || nodeID > 0 && name == "" {
			name = "节点 #" + strconv.FormatInt(nodeID, 10)
		}
		fileViews = append(fileViews, map[string]any{
			"Key": str(row, "file_key"), "OrigName": str(row, "orig_name"),
			"Mime": str(row, "mime"), "Ext": str(row, "ext"),
			"SizeStr":   gwFormatBytes(int64(intVal(row, "size_bytes"))),
			"NodeName":  name,
			"CreatedAt": str(row, "created_at"),
		})
	}
	return summary, nodeViews, fileViews
}

func actionGwSaveNode(w http.ResponseWriter, r *http.Request) {
	s := peekSession(r)
	id := int64(atoiOr(r.FormValue("node_id"), 0))
	name := formValue(r, "node_name")
	baseURL := formValue(r, "node_base_url")
	token := formValue(r, "node_token")
	antiBot := 0
	if r.FormValue("node_anti_bot") == "1" {
		antiBot = 1
	}

	if name == "" || token == "" {
		setFlash(s, "节点名称与 token 不能为空。", "error")
		gwAdminRedirect(w, "gateway")
		return
	}
	if strings.Contains(baseURL, "node.php") {
		setFlash(s, "节点地址填 origin（伪静态根），不要带 node.php。", "error")
		gwAdminRedirect(w, "gateway")
		return
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		setFlash(s, "节点地址格式不正确，需为 http(s) 链接（如 https://n1.example.com）。", "error")
		gwAdminRedirect(w, "gateway")
		return
	}
	baseURL = strings.TrimRight(baseURL, "/")

	if id > 0 {
		if gwGetNode(id) == nil {
			setFlash(s, "节点不存在或已被删除。", "error")
			gwAdminRedirect(w, "gateway")
			return
		}
		dbUpdate("gw_nodes", map[string]any{
			"name": name, "base_url": baseURL, "token": token, "anti_bot": antiBot,
		}, "id = ?", id)
		setFlash(s, "节点「"+name+"」已更新。", "success")
	} else {
		dbInsert("gw_nodes", map[string]any{
			"name": name, "base_url": baseURL, "token": token,
			"anti_bot": antiBot, "enabled": 1,
		})
		setFlash(s, "节点「"+name+"」已添加，建议立即测试连通性。", "success")
	}
	gwAdminRedirect(w, "gateway")
}

func actionGwToggleNode(w http.ResponseWriter, r *http.Request) {
	s := peekSession(r)
	node := gwGetNode(int64(atoiOr(r.FormValue("node_id"), 0)))
	if node == nil {
		setFlash(s, "节点不存在或已被删除。", "error")
		gwAdminRedirect(w, "gateway")
		return
	}
	enabled := 0
	if !node.Enabled {
		enabled = 1
	}
	dbUpdate("gw_nodes", map[string]any{"enabled": enabled}, "id = ?", node.ID)
	state := "已停用"
	if enabled == 1 {
		state = "已启用"
	}
	setFlash(s, "节点「"+node.Name+"」"+state+"。", "success")
	gwAdminRedirect(w, "gateway")
}

func actionGwDeleteNode(w http.ResponseWriter, r *http.Request) {
	s := peekSession(r)
	node := gwGetNode(int64(atoiOr(r.FormValue("node_id"), 0)))
	if node == nil {
		setFlash(s, "节点不存在或已被删除。", "error")
		gwAdminRedirect(w, "gateway")
		return
	}
	dbDelete("gw_nodes", "id = ?", node.ID)
	setFlash(s, "节点「"+node.Name+"」已删除；该节点上的既有文件不再被索引，如需清理请先恢复节点再删除文件。", "success")
	gwAdminRedirect(w, "gateway")
}

func actionGwTestNode(w http.ResponseWriter, r *http.Request) {
	s := peekSession(r)
	node := gwGetNode(int64(atoiOr(r.FormValue("node_id"), 0)))
	if node == nil {
		setFlash(s, "节点不存在或已被删除。", "error")
		gwAdminRedirect(w, "gateway")
		return
	}
	if err := gwNodeStatRefresh(node, gwNodeProbeTimeout); err != nil {
		setFlash(s, "节点「"+node.Name+"」测试失败："+err.Error(), "error")
		gwAdminRedirect(w, "gateway")
		return
	}
	setFlash(s, "节点「"+node.Name+"」正常：剩余 "+gwFormatBytes(node.FreeBytes)+
		" / 总量 "+gwFormatBytes(node.TotalBytes)+"，文件占用 "+gwFormatBytes(node.UsedBytes)+"。", "success")
	gwAdminRedirect(w, "gateway")
}

func actionGwRefreshNodes(w http.ResponseWriter, r *http.Request) {
	s := peekSession(r)
	okCount, failCount := 0, 0
	lastErr := ""
	for _, node := range gwListNodes(true) {
		if err := gwNodeStatRefresh(node, gwNodeStatTimeout); err != nil {
			failCount++
			lastErr = err.Error()
		} else {
			okCount++
		}
	}
	switch {
	case okCount+failCount == 0:
		setFlash(s, "没有已启用的存储节点可刷新。", "error")
	case failCount == 0:
		setFlash(s, "已刷新 "+strconv.Itoa(okCount)+" 个节点的容量信息。", "success")
	default:
		setFlash(s, strconv.Itoa(okCount)+" 个成功，"+strconv.Itoa(failCount)+" 个失败（最后一个错误："+lastErr+"）", "error")
	}
	gwAdminRedirect(w, "gateway")
}

func actionGwDeleteFile(w http.ResponseWriter, r *http.Request) {
	s := peekSession(r)
	key := formValue(r, "file_key")
	row := gwGetByKey(key)
	if row == nil {
		setFlash(s, "文件不存在于网关索引。", "error")
		gwAdminRedirect(w, "gateway")
		return
	}
	if ok, errMsg := gwDeleteRow(row, false); ok {
		setFlash(s, "文件 "+key+" 已删除。", "success")
	} else {
		setFlash(s, "文件 "+key+" 删除失败："+errMsg, "error")
	}
	gwAdminRedirect(w, "gateway")
}

func actionGwSaveSettings(w http.ResponseWriter, r *http.Request) {
	s := peekSession(r)
	settings := loadSiteSettings()

	// ETag/If-Match 并发控制：防止设置被覆盖
	ifMatch := r.Header.Get("If-Match")
	if ifMatch != "" {
		currentETag := settingsETag(settings)
		if ifMatch != currentETag {
			w.Header().Set("ETag", currentETag)
			w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
			w.WriteHeader(http.StatusPreconditionFailed)
			_, _ = w.Write([]byte(`{"type":"about:blank","title":"Precondition Failed","status":412,"detail":"Settings have been modified. Please reload and try again."}`))
			return
		}
	}

	settings.GwLocalEnabled = r.FormValue("gw_local_enabled") == "1"
	settings.GwLocalTotalBytes = gwParseBytesInput(r.FormValue("gw_local_total_bytes"), settings.GwLocalTotalBytes, 0)
	settings.GwLocalReserveBytes = gwParseBytesInput(r.FormValue("gw_local_reserve_bytes"), settings.GwLocalReserveBytes, 1)
	settings.GwChunkSize = gwParseBytesInput(r.FormValue("gw_chunk_size"), settings.GwChunkSize, 64<<10)
	if settings.GwChunkSize > 64<<20 {
		settings.GwChunkSize = 64 << 20
	}
	settings.GwMaxFileBytes = gwParseBytesInput(r.FormValue("gw_max_file_bytes"), settings.GwMaxFileBytes, 1<<20)

	saveSiteSettings(settings)
	setFlash(s, "存储网关设置已保存。", "success")
	gwAdminRedirect(w, "gateway")
}

// settingsETag 计算设置的 ETag（基于关键字段的 hash）。
func settingsETag(s siteSettings) string {
	data := fmt.Sprintf("%v|%v|%v|%v|%v",
		s.GwLocalEnabled, s.GwLocalTotalBytes, s.GwLocalReserveBytes,
		s.GwChunkSize, s.GwMaxFileBytes)
	sum := md5.Sum([]byte(data))
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

// gwParseBytesInput 解析后台字节输入（支持 123 / 300MB / 2GB），非法时保留旧值。
func gwParseBytesInput(raw string, fallback int64, minBytes int64) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	lower := strings.ToLower(raw)
	mult := int64(1)
	switch {
	case strings.HasSuffix(lower, "gb"):
		mult, lower = 1<<30, strings.TrimSuffix(lower, "gb")
	case strings.HasSuffix(lower, "mb"):
		mult, lower = 1<<20, strings.TrimSuffix(lower, "mb")
	case strings.HasSuffix(lower, "kb"):
		mult, lower = 1<<10, strings.TrimSuffix(lower, "kb")
	case strings.HasSuffix(lower, "g"):
		mult, lower = 1<<30, strings.TrimSuffix(lower, "g")
	case strings.HasSuffix(lower, "m"):
		mult, lower = 1<<20, strings.TrimSuffix(lower, "m")
	case strings.HasSuffix(lower, "k"):
		mult, lower = 1<<10, strings.TrimSuffix(lower, "k")
	}
	n, err := strconv.ParseInt(strings.TrimSpace(lower), 10, 64)
	if err != nil || n < 0 {
		return fallback
	}
	n *= mult
	if n < minBytes {
		n = minBytes
	}
	return n
}
