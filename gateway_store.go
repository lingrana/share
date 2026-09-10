package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 内置存储网关 — 存储核心，对应 share-storage gateway/includes/store.php。
// 调度策略：启用本地存储时本地优先（剩余容量 ≥ 保底阈值），否则/其后按节点自报
// 剩余空间降序尝试。文件索引统一在 gw_files，node_id = 0 表示本地。
// 对外统一输出 /content/<key>（代理流式输出，节点真实地址不对外暴露）。

const (
	gwKeyMaxLen          = 120
	gwOrigNameMax        = 255
	gwNodeProbeTimeout   = 8 * time.Second
	gwNodeStatTimeout    = 6 * time.Second
	gwNodeUploadTimeout  = 15 * time.Minute
	gwSignatureTTL       = time.Hour
	gwLocalTailThreshold = 50 << 20 // 混合模式下本地兜底候选的最低富余
)

var (
	fileIDRe       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	mimeRe         = regexp.MustCompile(`^[\w][\w\-.+/]*$`)
	gwBlockedExtRe = regexp.MustCompile(`^(php[0-9]*|phtml|phar|htaccess|htpasswd|cgi|pl|py|sh|asp|aspx|jsp|ini|log)$`)
)

// ssLastError 上次存储操作的错误摘要（资源表单提示用）。
var ssLastError string

func storageLastError() string { return ssLastError }

func ssSetError(msg string) { ssLastError = msg }

// gwHTTP 节点请求共用客户端：不跟随重定向，TLS 校验可由 http_insecure 关闭。
var gwHTTP = &http.Client{
	Transport: &http.Transport{TLSClientConfig: tlsConfigOrInsecure()},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// ==================== key 与 MIME ====================

// gwKeyIsValid 文件 key 白名单：字母数字开头，仅含 [A-Za-z0-9._-]，
// 禁止路径分隔符、.. 与危险脚本扩展名（对应 storeKeyIsValid）。
func gwKeyIsValid(key string) bool {
	if key == "" || len(key) > gwKeyMaxLen {
		return false
	}
	if !fileIDRe.MatchString(key) {
		return false
	}
	if strings.Contains(key, "..") {
		return false
	}
	dot := strings.LastIndexByte(key, '.')
	if dot < 0 {
		return true
	}
	return !gwBlockedExtRe.MatchString(strings.ToLower(key[dot+1:]))
}

// gwIsBlockedExtension 可安全保存到本地/远端的扩展名；危险脚本扩展名改用 .bin。
func gwIsBlockedExtension(ext string) bool {
	return gwBlockedExtRe.MatchString(strings.ToLower(ext))
}

func gwExtSanitize(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	ext = regexp.MustCompile(`[^a-z0-9]`).ReplaceAllString(ext, "")
	if len(ext) > 32 {
		ext = ext[:32]
	}
	return ext
}

// gwNewKey 生成随机 key：16 位 hex + 安全扩展名。
func gwNewKey(ext string) string {
	ext = gwExtSanitize(ext)
	if ext == "" || len(ext) > 32 || gwIsBlockedExtension(ext) {
		ext = "bin"
	}
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16) + "." + ext
	}
	return hex.EncodeToString(buf) + "." + ext
}

var gwMimeMap = map[string]string{
	"jpg": "image/jpeg", "jpeg": "image/jpeg", "png": "image/png",
	"gif": "image/gif", "webp": "image/webp", "svg": "image/svg+xml",
	"ico": "image/x-icon", "bmp": "image/bmp", "tif": "image/tiff", "tiff": "image/tiff",
	"avif": "image/avif", "heic": "image/heic", "heif": "image/heif", "jxl": "image/jxl",
	"mp3": "audio/mpeg", "m4a": "audio/mp4", "wav": "audio/wav", "ogg": "audio/ogg",
	"flac": "audio/flac", "aac": "audio/aac", "opus": "audio/opus",
	"mp4": "video/mp4", "webm": "video/webm", "mkv": "video/x-matroska", "mov": "video/quicktime",
	"txt": "text/plain", "md": "text/markdown", "pdf": "application/pdf",
	"zip": "application/zip", "rar": "application/vnd.rar", "7z": "application/x-7z-compressed",
}

func gwMimeFromExt(ext string) string {
	if m, ok := gwMimeMap[strings.ToLower(ext)]; ok {
		return m
	}
	return "application/octet-stream"
}

// gwDetectMime 探测本地文件真实 MIME（前 512 字节），未知时回退扩展名映射。
func gwDetectMime(path, fallbackExt string) string {
	fp, err := os.Open(path)
	if err == nil {
		defer fp.Close()
		buf := make([]byte, 512)
		n, _ := io.ReadFull(fp, buf)
		if n > 0 {
			if m := http.DetectContentType(buf[:n]); m != "" && m != "application/octet-stream" {
				return m
			}
		}
	}
	return gwMimeFromExt(fallbackExt)
}

// ==================== 本地存储 ====================

func gwRootDir() string  { return filepath.Join(dataDir, "gateway") }
func gwFilesDir() string { return filepath.Join(gwRootDir(), "files") }
func gwTmpDir() string   { return filepath.Join(gwRootDir(), "tmp") }

func gwEnsureDirs() {
	_ = os.MkdirAll(gwFilesDir(), 0o755)
	_ = os.MkdirAll(gwTmpDir(), 0o755)
}

// gwLocalPath key 已通过 gwKeyIsValid（无路径分隔符与 ..），可直接拼接。
func gwLocalPath(key string) string { return filepath.Join(gwFilesDir(), key) }

// gwLocalUsedBytes 本地已用空间：索引中落在本地（node_id=0）的文件大小合计。
func gwLocalUsedBytes() int64 {
	return int64(dbFetchColumnInt("SELECT COALESCE(SUM(size_bytes), 0) FROM gw_files WHERE node_id = 0"))
}

// gwLocalFree 本地剩余容量。GwLocalTotalBytes=0 表示不限制，返回 -1 哨兵。
func gwLocalFree(s siteSettings) int64 {
	if s.GwLocalTotalBytes <= 0 {
		return -1
	}
	free := s.GwLocalTotalBytes - gwLocalUsedBytes()
	if free < 0 {
		free = 0
	}
	return free
}

// gwSaveLocal 把上传源落到本地文件，成功返回 true。
func gwSaveLocal(src io.ReadSeeker, key string) bool {
	gwEnsureDirs()
	dest := gwLocalPath(key)
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return false
	}
	fp, err := os.Create(dest)
	if err != nil {
		return false
	}
	_, copyErr := io.Copy(fp, src)
	closeErr := fp.Close()
	return copyErr == nil && closeErr == nil
}

// ==================== 节点注册与状态 ====================

type gwNode struct {
	ID         int64
	Name       string
	BaseURL    string
	Token      string
	Enabled    bool
	AntiBot    bool
	Cookie     string
	FreeBytes  int64
	TotalBytes int64
	UsedBytes  int64
	LastSeenAt string
}

type gwFileRow struct {
	Key       string
	NodeID    int64
	SizeBytes int64
	Mime      string
	Ext       string
	OrigName  string
	CreatedAt string
}

func gwNodeFromRow(row map[string]any) *gwNode {
	if row == nil {
		return nil
	}
	return &gwNode{
		ID:         int64(intVal(row, "id")),
		Name:       str(row, "name"),
		BaseURL:    strings.TrimRight(str(row, "base_url"), "/"),
		Token:      str(row, "token"),
		Enabled:    intVal(row, "enabled") != 0,
		AntiBot:    intVal(row, "anti_bot") != 0,
		Cookie:     str(row, "cookie"),
		FreeBytes:  int64(intVal(row, "free_bytes")),
		TotalBytes: int64(intVal(row, "total_bytes")),
		UsedBytes:  int64(intVal(row, "used_bytes")),
		LastSeenAt: str(row, "last_seen_at"),
	}
}

func gwFileFromRow(row map[string]any) *gwFileRow {
	if row == nil {
		return nil
	}
	return &gwFileRow{
		Key:       str(row, "file_key"),
		NodeID:    int64(intVal(row, "node_id")),
		SizeBytes: int64(intVal(row, "size_bytes")),
		Mime:      str(row, "mime"),
		Ext:       str(row, "ext"),
		OrigName:  str(row, "orig_name"),
		CreatedAt: str(row, "created_at"),
	}
}

func gwListNodes(enabledOnly bool) []*gwNode {
	q := "SELECT * FROM gw_nodes"
	if enabledOnly {
		q += " WHERE enabled = 1"
	}
	q += " ORDER BY free_bytes DESC, id ASC"
	out := []*gwNode{}
	for _, row := range dbFetchAll(q) {
		if n := gwNodeFromRow(row); n != nil {
			out = append(out, n)
		}
	}
	return out
}

func gwGetNode(id int64) *gwNode {
	if id <= 0 {
		return nil
	}
	return gwNodeFromRow(dbFetchOne("SELECT * FROM gw_nodes WHERE id = ?", id))
}

func gwGetByKey(key string) *gwFileRow {
	return gwFileFromRow(dbFetchOne("SELECT * FROM gw_files WHERE file_key = ?", key))
}

func gwNodeURL(node *gwNode, path string) string {
	return strings.TrimRight(node.BaseURL, "/") + "/" + strings.TrimLeft(path, "/")
}

// gwNodeSetHeaders 节点请求头：token 鉴权 + 反爬挑战通行 cookie。
func gwNodeSetHeaders(h http.Header, node *gwNode) {
	h.Set("X-SS-Token", node.Token)
	if node.AntiBot && node.Cookie != "" && node.Cookie != "direct" {
		h.Set("Cookie", node.Cookie)
	}
}

// gwNodeRequest 发起一次节点请求，返回 (状态码, 响应体)；连接失败状态码为 0。
func gwNodeRequest(ctx context.Context, method, rawURL string, headers http.Header, body io.Reader, contentLength int64, timeout time.Duration) (int, []byte) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return 0, nil
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("User-Agent", "SS-Gateway/1.0")
	if contentLength > 0 {
		req.ContentLength = contentLength
	}
	resp, err := gwHTTP.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, data
}

// gwEnsureCookie 确保反爬节点持有通行 cookie（force = 重新求解）。
// 探测无挑战的主机记为 direct，之后不再探测；结果持久化到 gw_nodes.cookie。
func gwEnsureCookie(node *gwNode, force bool) {
	if !node.AntiBot {
		return
	}
	if !force && node.Cookie != "" {
		return
	}
	_, body := gwNodeRequest(nil, http.MethodGet, gwNodeURL(node, "/stats"), nil, nil, 0, gwNodeProbeTimeout)
	cookie := gwSolveChallenge(string(body))
	if cookie == "" {
		cookie = "direct" // 未见到挑战页（或求解失败）——先按直连处理，命中挑战会再次强制重解
	}
	dbUpdate("gw_nodes", map[string]any{"cookie": cookie}, "id = ?", node.ID)
	node.Cookie = cookie
}

// gwNodeStatRefresh 实时拉取节点空间并落库；不可达时返回错误摘要。
func gwNodeStatRefresh(node *gwNode, timeout time.Duration) error {
	if node.AntiBot {
		gwEnsureCookie(node, false)
	}
	h := http.Header{}
	gwNodeSetHeaders(h, node)
	status, body := gwNodeRequest(nil, http.MethodGet, gwNodeURL(node, "/stats"), h, nil, 0, timeout)
	if gwIsChallenge(string(body)) {
		gwEnsureCookie(node, true)
		status, body = gwNodeRequest(nil, http.MethodGet, gwNodeURL(node, "/stats"), h, nil, 0, timeout)
	}
	var data map[string]any
	if status != http.StatusOK || json.Unmarshal(body, &data) != nil {
		switch {
		case status == 0:
			return errors.New("无法连接节点——多为超时、DNS 未生效或主机防火墙拦截")
		case status == 401 || status == 403:
			return fmt.Errorf("token 不一致（节点返回 %d），请核对节点配置里的 token 与节点后台当前 token", status)
		default:
			return fmt.Errorf("节点响应异常 HTTP %d：%s", status, gwTruncateForLog(string(body), 240))
		}
	}
	free := gwJSONInt(data, "free_bytes", "free")
	total := gwJSONInt(data, "total_bytes", "total")
	used := gwJSONInt(data, "used_bytes", "used")
	dbUpdate("gw_nodes", map[string]any{
		"free_bytes": free, "total_bytes": total, "used_bytes": used, "last_seen_at": nowStr(),
	}, "id = ?", node.ID)
	node.FreeBytes, node.TotalBytes, node.UsedBytes, node.LastSeenAt = free, total, used, nowStr()
	return nil
}

// gwJSONInt 从节点 JSON 响应取整数字段，支持备选键名。
func gwJSONInt(data map[string]any, keys ...string) int64 {
	for _, k := range keys {
		v, ok := data[k]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case float64:
			return int64(t)
		case string:
			if n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64); err == nil {
				return n
			}
		case int64:
			return t
		case int:
			return int64(t)
		}
	}
	return 0
}

func gwTruncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// ==================== 上传调度 ====================

type gwCandidate struct {
	Local bool
	Node  *gwNode
}

// gwCandidates 候选存储目标（读取当前站点设置）。
func gwCandidates(size int64) []gwCandidate {
	return gwCandidatesWithSettings(loadSiteSettings(), size)
}

// gwCandidatesWithSettings 候选存储目标：仅使用节点，按自报剩余空间降序。
func gwCandidatesWithSettings(s siteSettings, size int64) []gwCandidate {
	list := []gwCandidate{}
	for _, node := range gwListNodes(true) {
		if node.TotalBytes > 0 {
			if node.TotalBytes-node.UsedBytes >= size {
				list = append(list, gwCandidate{Node: node})
			}
			continue
		}
		// free_bytes = 0 表示从未上报（未知），交给节点端保底校验兜底
		if node.FreeBytes == 0 || node.FreeBytes-size > gwLocalTailThreshold {
			list = append(list, gwCandidate{Node: node})
		}
	}
	return list
}

// gwNodePut 整文件 PUT 到节点（裸字节流），返回错误摘要。
func gwNodePut(node *gwNode, key string, src io.ReadSeeker, size int64) error {
	if node.AntiBot {
		gwEnsureCookie(node, false)
	}
	rawURL := gwNodeURL(node, "/files/"+url.PathEscape(key))
	do := func() (int, []byte) {
		if _, err := src.Seek(0, io.SeekStart); err != nil {
			return 0, nil
		}
		h := http.Header{}
		gwNodeSetHeaders(h, node)
		return gwNodeRequest(nil, http.MethodPut, rawURL, h, src, size, gwNodeUploadTimeout)
	}
	status, body := do()
	if gwIsChallenge(string(body)) {
		gwEnsureCookie(node, true)
		status, body = do()
	}
	if status < 200 || status >= 300 {
		return gwProblemError(body, status, "节点上传失败")
	}
	return nil
}

// gwNodeCreateSession 创建节点分片上传会话，返回 upload_id。
func gwNodeCreateSession(node *gwNode, fileID, origName string, size int64, mime string) (string, error) {
	if node.AntiBot {
		gwEnsureCookie(node, false)
	}
	payload := map[string]any{
		"orig_name":  origName,
		"size_bytes": size,
		"file_id":    fileID,
		"mime":       mime,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", errors.New("会话请求编码失败")
	}
	rawURL := gwNodeURL(node, "/upload-sessions")
	do := func() (int, []byte) {
		h := http.Header{}
		gwNodeSetHeaders(h, node)
		h.Set("Content-Type", "application/json; charset=utf-8")
		return gwNodeRequest(nil, http.MethodPost, rawURL, h, bytes.NewReader(data), int64(len(data)), 30*time.Second)
	}
	status, body := do()
	if gwIsChallenge(string(body)) {
		gwEnsureCookie(node, true)
		status, body = do()
	}
	var resp map[string]any
	if status < 200 || status >= 300 || json.Unmarshal(body, &resp) != nil {
		return "", gwProblemError(body, status, "节点分片会话创建失败")
	}
	id, _ := resp["upload_id"].(string)
	if id == "" {
		return "", errors.New("节点未返回 upload_id")
	}
	return id, nil
}

// gwNodeChunk 追加一个分片（multipart 字段名必须是 file）。
func gwNodeChunk(node *gwNode, uploadID string, chunk []byte) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", "part.bin")
	if err != nil {
		return err
	}
	if _, err := part.Write(chunk); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	rawURL := gwNodeURL(node, "/upload-sessions/"+url.PathEscape(uploadID)+"/parts")
	do := func() (int, []byte) {
		h := http.Header{}
		gwNodeSetHeaders(h, node)
		h.Set("Content-Type", mw.FormDataContentType())
		return gwNodeRequest(nil, http.MethodPost, rawURL, h, bytes.NewReader(buf.Bytes()), int64(buf.Len()), gwNodeUploadTimeout)
	}
	status, body := do()
	if gwIsChallenge(string(body)) {
		gwEnsureCookie(node, true)
		status, body = do()
	}
	if status < 200 || status >= 300 {
		return gwProblemError(body, status, "分片追加失败")
	}
	return nil
}

// gwNodeFinish 通知节点合并分片为最终文件，返回实际大小。
func gwNodeFinish(node *gwNode, uploadID, key string) (int64, error) {
	payload, _ := json.Marshal(map[string]any{"file_id": key})
	rawURL := gwNodeURL(node, "/upload-sessions/"+url.PathEscape(uploadID)+":complete")
	do := func() (int, []byte) {
		h := http.Header{}
		gwNodeSetHeaders(h, node)
		h.Set("Content-Type", "application/json; charset=utf-8")
		return gwNodeRequest(nil, http.MethodPost, rawURL, h, bytes.NewReader(payload), int64(len(payload)), 90*time.Second)
	}
	status, body := do()
	if gwIsChallenge(string(body)) {
		gwEnsureCookie(node, true)
		status, body = do()
	}
	var resp map[string]any
	if status < 200 || status >= 300 || json.Unmarshal(body, &resp) != nil {
		return 0, gwProblemError(body, status, "分片合并失败")
	}
	size := gwJSONInt(resp, "size_bytes", "size")
	if size <= 0 {
		gwNodeAbort(node, uploadID)
		return 0, errors.New("空文件不会被存储")
	}
	return size, nil
}

func gwNodeAbort(node *gwNode, uploadID string) {
	h := http.Header{}
	gwNodeSetHeaders(h, node)
	_, _ = gwNodeRequest(nil, http.MethodDelete, gwNodeURL(node, "/upload-sessions/"+url.PathEscape(uploadID)), h, nil, 0, 15*time.Second)
}

// gwNodeDelete 删除节点上的单个文件。
func gwNodeDelete(node *gwNode, key string) bool {
	if node.AntiBot {
		gwEnsureCookie(node, false)
	}
	rawURL := gwNodeURL(node, "/files/"+url.PathEscape(key))
	do := func() (int, []byte) {
		h := http.Header{}
		gwNodeSetHeaders(h, node)
		return gwNodeRequest(nil, http.MethodDelete, rawURL, h, nil, 0, 30*time.Second)
	}
	status, body := do()
	if gwIsChallenge(string(body)) {
		gwEnsureCookie(node, true)
		status, _ = do()
	}
	return status == http.StatusNoContent || status == http.StatusOK
}

// gwProblemError 把节点 problem+json 摘要为可读错误。
func gwProblemError(body []byte, status int, fallback string) error {
	var js map[string]any
	if len(body) > 0 && json.Unmarshal(body, &js) == nil {
		if d, ok := js["detail"].(string); ok && d != "" {
			return errors.New(d)
		}
		if t, ok := js["title"].(string); ok && t != "" {
			return errors.New(t)
		}
	}
	if status == 0 {
		return errors.New("无法连接存储节点，请检查节点地址是否可从本站服务器访问")
	}
	return fmt.Errorf("%s（HTTP %d）", fallback, status)
}

// ==================== 索引操作 ====================

// gwInsertFile 确定性 key 重复上传时覆盖索引。
func gwInsertFile(key string, nodeID, size int64, mime, ext, origName string) {
	exists := gwGetByKey(key)
	if exists != nil {
		dbUpdate("gw_files", map[string]any{
			"node_id": nodeID, "size_bytes": size, "mime": mime, "ext": ext, "orig_name": origName,
		}, "file_key = ?", key)
		return
	}
	dbInsert("gw_files", map[string]any{
		"file_key": key, "node_id": nodeID, "size_bytes": size,
		"mime": mime, "ext": ext, "orig_name": origName, "created_at": nowStr(),
	})
}

// gwDeleteRow 删除单个文件（节点/本地 + 索引）。force = 节点删除失败也删索引。
func gwDeleteRow(row *gwFileRow, force bool) (bool, string) {
	if row == nil {
		return false, "索引不存在"
	}
	ok := true
	errMsg := ""
	if row.NodeID == 0 {
		path := gwLocalPath(row.Key)
		if fileExists(path) {
			if err := os.Remove(path); err != nil {
				ok = false
				errMsg = "本地文件删除失败"
			}
		}
	} else {
		node := gwGetNode(row.NodeID)
		switch {
		case node == nil:
			ok = force
			errMsg = "节点不存在"
		case !gwNodeDelete(node, row.Key):
			ok = force
			errMsg = "节点删除失败（节点可能离线）"
		}
	}
	if ok {
		dbDelete("gw_files", "file_key = ?", row.Key)
	}
	return ok, errMsg
}

// gwDeleteByPrefix 按前缀批量删除（主站媒体/历史封面清理用），尽力而为返回条数。
func gwDeleteByPrefix(prefix string) int {
	if prefix == "" || len(prefix) > 100 || strings.Contains(prefix, "..") || !fileIDRe.MatchString(prefix) {
		return 0
	}
	deleted := 0
	for _, rowMap := range dbFetchAll("SELECT * FROM gw_files WHERE file_key LIKE ?", prefix+"%") {
		row := gwFileFromRow(rowMap)
		if row == nil {
			continue
		}
		if ok, _ := gwDeleteRow(row, true); ok {
			deleted++
		}
	}
	return deleted
}

// ==================== 对外链接与元信息 ====================

// gwContentURL 对外统一文件 URL：本站相对路径，节点地址不暴露。
func gwContentURL(key string) string {
	return "/content/" + url.PathEscape(key)
}

// gwOwnsUrl 判断外链是否为本站网关内容（covers.go 用，等价旧 ssRemoteOwnsUrl）。
func gwOwnsUrl(rawURL string) bool {
	rawURL = strings.TrimSpace(rawURL)
	return strings.HasPrefix(rawURL, "/content/")
}

// gwMD5Hex 小写 hex 的 md5（与 PHP md5() 一致）。
func gwMD5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// gwEtag 与节点端一致：md5(key|size)。
func gwEtag(row *gwFileRow) string {
	return `"` + gwMD5Hex(row.Key+"|"+strconv.FormatInt(row.SizeBytes, 10)) + `"`
}

// gwEffectiveMime 兼容早期索引中的 application/octet-stream。
func gwEffectiveMime(row *gwFileRow) string {
	mime := strings.TrimSpace(row.Mime)
	if mime != "" && mime != "application/octet-stream" {
		return mime
	}
	ext := gwExtSanitize(row.Ext)
	if ext == "" {
		ext = gwExtSanitize(filepath.Ext(row.Key))
	}
	fallback := gwMimeFromExt(ext)
	if fallback != "application/octet-stream" {
		return fallback
	}
	if mime != "" {
		return mime
	}
	return fallback
}

// gwDisposition 下载响应头；orig_name 优先，剔除引号与换行。
func gwDisposition(download bool, row *gwFileRow) string {
	if !download {
		return "inline"
	}
	name := strings.TrimSpace(row.OrigName)
	if name == "" {
		name = row.Key
	}
	name = strings.NewReplacer("\"", "", "\r", "", "\n", "").Replace(name)
	if name == "" {
		name = row.Key
	}
	return `attachment; filename="` + name + `"; filename*=UTF-8''` + url.PathEscape(name)
}
