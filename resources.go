package main

import (
	"fmt"
	"sort"
	"strings"
)

// 文件夹与资源操作，对应 includes/resources.php。

// ==================== 文件夹 ====================

func getFolderById(id int) map[string]any {
	if id <= 0 {
		return nil
	}
	return dbFetchOne("SELECT * FROM folders WHERE id = ?", id)
}

func getRootFolder() map[string]any {
	return dbFetchOne("SELECT * FROM folders WHERE parent_id IS NULL")
}

func isRootFolderId(folderID int) bool {
	root := getRootFolder()
	return root != nil && intVal(root, "id") == folderID
}

func getChildFolders(parentID int) []map[string]any {
	if isRootFolderId(parentID) {
		// 根的直接子目录两种存法都兼容：parent_id = 根 id 或 parent_id IS NULL
		return dbFetchAll(
			"SELECT * FROM folders WHERE (parent_id = ? OR (parent_id IS NULL AND id <> ?)) ORDER BY sort_order ASC, name ASC",
			parentID, parentID)
	}
	return dbFetchAll("SELECT * FROM folders WHERE parent_id = ? ORDER BY sort_order ASC, name ASC", parentID)
}

func getAllChildFolderIds(folderID int) []int {
	ids := []int{folderID}
	for _, child := range getChildFolders(folderID) {
		ids = append(ids, getAllChildFolderIds(intVal(child, "id"))...)
	}
	return ids
}

func createFolder(name string, parentID int) int64 {
	parentPath := "/"
	if parentID > 0 {
		if parent := getFolderById(parentID); parent != nil {
			parentPath = str(parent, "path")
		}
	}
	path := strings.TrimRight(parentPath, "/") + "/" + name

	var parentArg any
	if parentID > 0 {
		parentArg = parentID
	} else {
		parentArg = nil
	}
	maxSort := dbFetchColumn("SELECT MAX(sort_order) FROM folders WHERE parent_id = ?", parentArg)
	nextSort := 1
	if n, ok := maxSort.(int); ok {
		nextSort = n + 1
	}
	return dbInsert("folders", map[string]any{
		"name":       name,
		"parent_id":  parentArg,
		"path":       path,
		"sort_order": nextSort,
	})
}

func renameFolder(id int, newName string) {
	folder := getFolderById(id)
	if folder == nil {
		return
	}
	parentPath := "/"
	if pid := intVal(folder, "parent_id"); pid > 0 {
		if parent := getFolderById(pid); parent != nil {
			parentPath = str(parent, "path")
		}
	}
	newPath := strings.TrimRight(parentPath, "/") + "/" + newName
	dbUpdate("folders", map[string]any{"name": newName, "path": newPath}, "id = ?", id)
	updateDescendantPaths(id, newPath)
}

// updateDescendantPaths 递归重写整棵子树的 path，避免孙级目录残留旧祖先名。
func updateDescendantPaths(parentID int, parentPath string) {
	children := dbFetchAll("SELECT id, name FROM folders WHERE parent_id = ?", parentID)
	for _, child := range children {
		childPath := strings.TrimRight(parentPath, "/") + "/" + str(child, "name")
		dbUpdate("folders", map[string]any{"path": childPath}, "id = ?", intVal(child, "id"))
		updateDescendantPaths(intVal(child, "id"), childPath)
	}
}

func deleteFolder(id int) {
	childIds := getAllChildFolderIds(id)
	// childIds[0] 是自身；存在真实子目录时才按集合删资源
	if len(childIds) > 1 {
		placeholders := placeholdersFor(childIds)
		args := intsToAny(childIds)
		rows := dbFetchAll("SELECT id FROM resources WHERE folder_id IN ("+placeholders+")", args...)
		for _, row := range rows {
			deleteResourceFiles(intVal(row, "id"))
		}
		dbDelete("resource_links", "resource_id IN (SELECT id FROM resources WHERE folder_id IN ("+placeholders+"))", args...)
		dbDelete("resources", "folder_id IN ("+placeholders+")", args...)
	}
	// 递归删子文件夹
	for _, child := range dbFetchAll("SELECT id FROM folders WHERE parent_id = ?", id) {
		deleteFolder(intVal(child, "id"))
	}
	dbDelete("folders", "id = ?", id)
}

func getFolderPath(folderID int) []map[string]any {
	var path []map[string]any
	current := getFolderById(folderID)
	for current != nil {
		path = append([]map[string]any{current}, path...)
		if pid := intVal(current, "parent_id"); pid > 0 {
			current = getFolderById(pid)
		} else {
			current = nil
		}
	}
	return path
}

type breadcrumb struct {
	Name string
	ID   int
	Path string
}

func buildFolderBreadcrumbs(folderID int) []breadcrumb {
	var out []breadcrumb
	for _, item := range getFolderPath(folderID) {
		out = append(out, breadcrumb{
			Name: str(item, "name"),
			ID:   intVal(item, "id"),
			Path: str(item, "path"),
		})
	}
	return out
}

// ==================== 资源 ====================

func getResourceById(id int) map[string]any {
	if id <= 0 {
		return nil
	}
	return dbFetchOne("SELECT * FROM resources WHERE id = ?", id)
}

type pagedResult struct {
	Items   []map[string]any
	Total   int
	Pages   int
	Page    int
	PerPage int
}

// folderScopeSQL 生成 folder_id 过滤（根目录同时收纳 folder_id IS NULL 的资源）。
func folderScopeSQL(folderID int) (string, []any) {
	ids := getAllChildFolderIds(folderID)
	placeholders := placeholdersFor(ids)
	args := intsToAny(ids)
	if isRootFolderId(folderID) {
		return "(folder_id IS NULL OR folder_id IN (" + placeholders + "))", args
	}
	return "folder_id IN (" + placeholders + ")", args
}

func getResourcesByFolder(folderID, page, perPage int, search string, onlyEnabled bool) pagedResult {
	where, params := folderScopeSQL(folderID)
	if onlyEnabled {
		where += " AND enabled = 1"
	}
	if search != "" {
		pattern := "%" + likeEscape(search) + "%"
		where += " AND (title LIKE ? ESCAPE '\\' OR description LIKE ? ESCAPE '\\' OR tags LIKE ? ESCAPE '\\')"
		params = append(params, pattern, pattern, pattern)
	}
	total := dbCount("resources", where, params...)
	offset := (page - 1) * perPage
	rows := dbFetchAll(
		"SELECT * FROM resources WHERE "+where+" ORDER BY created_at DESC LIMIT ? OFFSET ?",
		append(params, perPage, offset)...)
	return pagedResult{
		Items: rows, Total: total,
		Pages: (total + perPage - 1) / perPage,
		Page:  page, PerPage: perPage,
	}
}

func createResource(data map[string]any) int64 {
	if data["resource_type"] == nil || data["resource_type"] == "" {
		data["resource_type"] = "other"
	}
	if data["enabled"] == nil {
		data["enabled"] = 1
	}
	if data["cover_url"] == nil {
		data["cover_url"] = ""
	}
	if data["media_url"] == nil {
		data["media_url"] = ""
	}
	if data["tags"] == nil {
		data["tags"] = ""
	}
	if data["description"] == nil {
		data["description"] = ""
	}
	return dbInsert("resources", data)
}

func updateResource(id int64, data map[string]any) {
	if len(data) == 0 {
		return
	}
	if _, ok := data["updated_at"]; !ok {
		data["updated_at"] = nowStr()
	}
	dbUpdate("resources", data, "id = ?", id)
	for _, share := range dbFetchAll("SELECT token FROM shares WHERE resource_id = ?", id) {
		clearPublicPageCache("share:" + str(share, "token"))
	}
}

func deleteResource(id int) {
	resource := getResourceById(id)
	for _, share := range dbFetchAll("SELECT token FROM shares WHERE resource_id = ?", id) {
		clearPublicPageCache("share:" + str(share, "token"))
	}
	dbDelete("resource_links", "resource_id = ?", id)
	dbDelete("shares", "resource_id = ?", id)
	if resource != nil {
		storageDeleteURL(str(resource, "cover_url"))
		storageDeleteURL(str(resource, "media_url"))
	}
	deleteResourceFiles(id)
	dbDelete("resources", "id = ?", id)
}

// deleteResourceFiles：网关侧按 m<id>. 前缀清理；c<id>. 仅清理历史残留。失败不阻塞删除。
func deleteResourceFiles(id int) {
	storageDeleteForBase("covers", fmt.Sprintf("%d", id))
	storageDeleteForBase("media", fmt.Sprintf("%d", id))
}

func incrementViewCount(id int) {
	dbExecute("UPDATE resources SET view_count = view_count + 1 WHERE id = ?", id)
}

func toggleResourceEnabled(id int) {
	res := getResourceById(id)
	if res == nil {
		return
	}
	next := 1
	if intVal(res, "enabled") != 0 {
		next = 0
	}
	dbUpdate("resources", map[string]any{"enabled": next}, "id = ?", id)
}

func getResourceCount() int { return dbCount("resources", "1") }

func getFolderResourceCount(folderID int) int {
	where, params := folderScopeSQL(folderID)
	return dbCount("resources", where+" AND enabled = 1", params...)
}

// getDescendantResourceCounts 一次计算 rootFolderId 各直接子目录（含子树）的启用资源数，
// 替代逐卡片递归查询的 N+1。
func getDescendantResourceCounts(rootFolderID int) map[int]int {
	childrenOf := map[int][]int{}
	for _, row := range dbFetchAll("SELECT id, parent_id FROM folders") {
		pid := intVal(row, "parent_id")
		childrenOf[pid] = append(childrenOf[pid], intVal(row, "id"))
	}
	directCounts := map[int]int{}
	for _, row := range dbFetchAll("SELECT folder_id, COUNT(*) AS cnt FROM resources WHERE enabled = 1 GROUP BY folder_id") {
		directCounts[intVal(row, "folder_id")] = intVal(row, "cnt")
	}

	memo := map[int]int{}
	visiting := map[int]bool{}
	var sumSubtree func(int) int
	sumSubtree = func(folderID int) int {
		if v, ok := memo[folderID]; ok {
			return v
		}
		if visiting[folderID] {
			return 0 // 目录树出现环时兜底
		}
		visiting[folderID] = true
		sum := directCounts[folderID]
		for _, childID := range childrenOf[folderID] {
			sum += sumSubtree(childID)
		}
		delete(visiting, folderID)
		memo[folderID] = sum
		return sum
	}

	counts := map[int]int{}
	// 根的直接子目录 parent_id 可能是根 id，也可能是 NULL（聚到键 0），两个键都取并排除根自身
	seen := map[int]bool{}
	for _, childID := range append(append([]int{}, childrenOf[rootFolderID]...), childrenOf[0]...) {
		if childID == rootFolderID || seen[childID] {
			continue
		}
		seen[childID] = true
		counts[childID] = sumSubtree(childID)
	}
	return counts
}

// ==================== 资源链接 ====================

func getResourceLinks(resourceID int) []map[string]any {
	return dbFetchAll("SELECT * FROM resource_links WHERE resource_id = ? ORDER BY sort_order ASC", resourceID)
}

func addResourceLink(data map[string]any) int64 {
	resourceID := anyInt(data["resource_id"])
	if resourceID <= 0 {
		return 0
	}
	var parentArg any = resourceID
	maxSort := dbFetchColumn("SELECT MAX(sort_order) FROM resource_links WHERE resource_id = ?", parentArg)
	nextSort := 1
	if n, ok := maxSort.(int); ok {
		nextSort = n + 1
	}
	if data["platform"] == nil || data["platform"] == "" {
		data["platform"] = "other"
	}
	if data["code"] == nil {
		data["code"] = ""
	}
	if data["password"] == nil {
		data["password"] = ""
	}
	return dbInsert("resource_links", map[string]any{
		"resource_id": resourceID,
		"platform":    data["platform"],
		"url":         data["url"],
		"code":        data["code"],
		"password":    data["password"],
		"sort_order":  nextSort,
	})
}

func incrementLinkClick(id int) {
	dbExecute("UPDATE resource_links SET click_count = click_count + 1 WHERE id = ?", id)
}

// ==================== 搜索 ====================

func searchResources(query string, page, perPage int, onlyEnabled bool) pagedResult {
	query = strings.TrimSpace(query)
	where := "(title LIKE ? ESCAPE '\\' OR description LIKE ? ESCAPE '\\' OR tags LIKE ? ESCAPE '\\')"
	pattern := "%" + likeEscape(query) + "%"
	params := []any{pattern, pattern, pattern}
	if onlyEnabled {
		where += " AND enabled = 1"
	}
	total := dbCount("resources", where, params...)
	offset := (page - 1) * perPage
	rows := dbFetchAll(
		"SELECT r.*, f.name AS folder_name FROM resources r LEFT JOIN folders f ON r.folder_id = f.id WHERE "+
			where+" ORDER BY r.view_count DESC, r.created_at DESC LIMIT ? OFFSET ?",
		append(params, perPage, offset)...)
	return pagedResult{
		Items: rows, Total: total,
		Pages: (total + perPage - 1) / perPage,
		Page:  page, PerPage: perPage,
	}
}

// anyInt 把 int/int64/float64 统一转 int（LastInsertId 返回 int64）。
func anyInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// ==================== 占位符工具 ====================

func placeholdersFor(ids []int) string {
	if len(ids) == 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
}

func intsToAny(ids []int) []any {
	out := make([]any, len(ids))
	for i, v := range ids {
		out[i] = v
	}
	return out
}

var _ = sort.Ints
