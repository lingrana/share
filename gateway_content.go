package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 内置存储网关 — 公开路由与主站上传中转。
// /content/<key> 公开外链（GET/HEAD，支持 ?download）；资源媒体的「完整内容」
// 在单个请求内完成直传或分片转发（无需跨请求会话，对应 share-storage 上传编排）。

// ==================== 幂等键存储 ====================

type idempotencyEntry struct {
	result  string
	created time.Time
}

var (
	idempotencyMu    sync.Mutex
	idempotencyStore = map[string]*idempotencyEntry{}
)

// cleanupIdempotency 清理过期的幂等键（24小时过期）
func cleanupIdempotency() {
	for {
		time.Sleep(time.Hour)
		idempotencyMu.Lock()
		cutoff := time.Now().Add(-24 * time.Hour)
		for k, v := range idempotencyStore {
			if v.created.Before(cutoff) {
				delete(idempotencyStore, k)
			}
		}
		idempotencyMu.Unlock()
	}
}

func init() {
	go cleanupIdempotency()
}

// checkIdempotency 检查幂等键，返回 (已有结果, 结果)。key 为空时跳过。
func checkIdempotency(key string) (bool, string) {
	if key == "" {
		return false, ""
	}
	idempotencyMu.Lock()
	defer idempotencyMu.Unlock()
	if entry, ok := idempotencyStore[key]; ok {
		return true, entry.result
	}
	return false, ""
}

// setIdempotency 存储幂等键结果。
func setIdempotency(key, result string) {
	if key == "" {
		return
	}
	idempotencyMu.Lock()
	idempotencyStore[key] = &idempotencyEntry{result: result, created: time.Now()}
	idempotencyMu.Unlock()
}

// ==================== /content/<key> 公开外链 ====================

var reContentKey = regexp.MustCompile(`^/content/([^/]+)/?$`)

func handleContent(w http.ResponseWriter, r *http.Request) {
	m := reContentKey.FindStringSubmatch(r.URL.Path)
	if m == nil {
		http.NotFound(w, r)
		return
	}
	key := m[1]
	method := r.Method
	if method != http.MethodGet && method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		gwProblemJSON(w, http.StatusMethodNotAllowed, "Method Not Allowed", "Method not allowed.")
		return
	}
	if !gwKeyIsValid(key) {
		gwProblemJSON(w, http.StatusNotFound, "Not Found", "The requested resource does not exist.")
		return
	}
	row := gwGetByKey(key)
	if row == nil {
		gwProblemJSON(w, http.StatusNotFound, "Not Found", "The requested resource does not exist.")
		return
	}
	dl := r.URL.Query().Get("download")
	download := r.URL.Query().Has("download") && dl != "0" && dl != "false"

	if method == http.MethodHead {
		gwServeHead(w, r, row, download)
		return
	}
	if row.NodeID == 0 {
		gwServeLocal(w, r, row, download)
		return
	}
	node := gwGetNode(row.NodeID)
	if node == nil {
		gwProblemJSON(w, http.StatusBadGateway, "Bad Gateway", "Storage node is unreachable.")
		return
	}
	gwProxyStream(w, r, row, node, download)
}

// gwServeHead HEAD 响应不接触节点：按索引行直接计算（对应 ssGwServePublic 的 HEAD 分支）。
func gwServeHead(w http.ResponseWriter, r *http.Request, row *gwFileRow, download bool) {
	etag := gwEtag(row)
	if inm := strings.TrimSpace(r.Header.Get("If-None-Match")); inm != "" && inm == etag {
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.WriteHeader(http.StatusNotModified)
		return
	}
	size := row.SizeBytes
	rng, invalid := gwParseClientRange(size, r.Header.Get("Range"))
	if invalid {
		w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(size, 10))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	start, end := int64(0), size-1
	if rng != nil {
		start, end = rng.Start, rng.End
	}
	gwSendBaseHeaders(w, row, download)
	w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	status := http.StatusOK
	if rng != nil {
		status = http.StatusPartialContent
		w.Header().Set("Content-Range", "bytes "+strconv.FormatInt(start, 10)+"-"+strconv.FormatInt(end, 10)+"/"+strconv.FormatInt(size, 10))
	}
	w.WriteHeader(status)
}

// ==================== 主站媒体上传（进程内直调，无双后端） ====================

const (
	ssFileIDMax   = 120
	ssOrigNameMax = 255
)

var blockedExtSet = func() map[string]bool {
	set := map[string]bool{}
	for _, e := range []string{"php", "php3", "php4", "php5", "php7", "php8", "phtml", "phar", "htaccess", "htpasswd", "cgi", "pl", "py", "sh", "asp", "aspx", "jsp", "ini", "log"} {
		set[e] = true
	}
	return set
}()

func ssSafeUploadExtension(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	ext = strings.TrimPrefix(ext, ".")
	ext = regexp.MustCompile(`[^a-z0-9]`).ReplaceAllString(ext, "")
	if ext == "" || len(ext) > 16 {
		return ""
	}
	if blockedExtSet[ext] || strings.HasPrefix(ext, "php") {
		return ""
	}
	return ext
}

// ssMediaFileID 资源媒体使用 m<resource_id>.<extension> 作为 file_id。
func ssMediaFileID(resourceID int64, ext string) string {
	if resourceID <= 0 || ext == "" {
		return ""
	}
	fileID := "m" + strconv.FormatInt(resourceID, 10) + "." + ext
	if len(fileID) > ssFileIDMax || strings.Contains(fileID, "..") || !fileIDRe.MatchString(fileID) {
		return ""
	}
	return fileID
}

// ssOrigName 展示用原始文件名：标题优先，兜底上传名。
func ssOrigName(title, ext, fallback string) string {
	base := strings.TrimSpace(title)
	if base == "" {
		base = strings.TrimSuffix(filepath.Base(fallback), filepath.Ext(fallback))
	}
	base = strings.Trim(base, " \t\n\r\x00\x0b.")
	if base == "" {
		base = "file"
	}
	base = sanitizeFilenameBase(base)
	name := base + "." + ext
	if truncated := truncateUTF8Safe(name, ssOrigNameMax); truncated != "" {
		return truncated
	}
	return "file." + ext
}

// gwUploadMedia 资源「完整内容」上传入口：校验 → 幂等检查 → 清理旧前缀 → 调度存储。
// 返回 /content/<key> 外链（相对路径），失败返回 "" 并在 ssLastError 留下原因。
// idempotencyKey 用于幂等重试（可选，空串跳过）。
func gwUploadMedia(resourceID int64, title string, file *multipartFileInfo, idempotencyKey ...string) string {
	ssSetError("")
	if file == nil || (file.data == nil && file.tmpPath == "") {
		ssSetError("上传临时文件不存在。")
		return ""
	}
	size := file.size
	if size <= 0 {
		if file.data != nil {
			size = int64(len(file.data))
		} else if st, err := os.Stat(file.tmpPath); err == nil {
			size = st.Size()
		}
	}
	if size <= 0 {
		ssSetError("空文件不会入库。")
		return ""
	}
	ext := ssSafeUploadExtension(file.origName)
	if ext == "" {
		ssSetError("不支持的文件扩展名。")
		return ""
	}
	fileID := ssMediaFileID(resourceID, ext)
	if fileID == "" {
		ssSetError("无法生成合法 file_id。")
		return ""
	}
	s := loadSiteSettings()
	if size > s.GwMaxFileBytes {
		ssSetError("文件超过单文件上限（" + gwFormatBytes(s.GwMaxFileBytes) + "），可在后台「存储网关」调整。")
		return ""
	}
	origName := ssOrigName(title, ext, file.origName)
	mime := strings.TrimSpace(file.contentType)
	if !mimeRe.MatchString(mime) {
		mime = ""
	}

	// 幂等检查：相同 Key 的请求直接返回上次结果
	if len(idempotencyKey) > 0 && idempotencyKey[0] != "" {
		if found, cachedResult := checkIdempotency(idempotencyKey[0]); found {
			return cachedResult
		}
	}

	// 扩展名变化时旧 file_id 不会被覆盖，先按前缀清理（幂等）
	gwDeleteByPrefix("m" + strconv.FormatInt(resourceID, 10) + ".")

	result := ""
	if link := gwStoreUpload(file, size, fileID, origName, mime); link != "" {
		result = link
	} else {
		if ssLastError == "" {
			ssSetError("文件存储失败，请检查存储节点或本地存储配置。")
		}
	}

	// 存储幂等键结果
	if len(idempotencyKey) > 0 && idempotencyKey[0] != "" {
		setIdempotency(idempotencyKey[0], result)
	}

	return result
}

// gwStoreUpload 整文件上传：依次尝试候选目标直到成功，返回 /content/<key>。
// 直传（单请求 PUT 裸字节）失败时对该节点降级为分片会话转发。
func gwStoreUpload(file *multipartFileInfo, size int64, key, origName, mime string) string {
	if file.tmpPath != "" {
		gwEnsureDirs()
	}
	candidates := gwCandidates(size)
	if len(candidates) == 0 {
		ssSetError("无可用存储目标：请先在后台「存储网关」添加节点，或启用本地存储。")
		return ""
	}
	src, closer, err := gwOpenSource(file)
	if err != nil {
		ssSetError("无法读取待上传文件。")
		return ""
	}
	if closer != nil {
		defer closer.Close()
	}

	lastError := ""
	for _, cand := range candidates {
		if cand.Local {
			if gwSaveLocal(src, key) {
				ext := gwExtSanitize(filepath.Ext(key))
				if mime == "" {
					mime = gwDetectMime(gwLocalPath(key), ext)
				}
				gwInsertFile(key, 0, size, mime, ext, origName)
				return gwContentURL(key)
			}
			lastError = "本地写入失败"
			continue
		}
		node := cand.Node
		err := gwNodePut(node, key, src, size)
		if err != nil {
			// 直传失败（节点可能限制大请求体）→ 降级分片会话
			if err2 := gwChunkedToNode(node, src, size, key, origName, mime); err2 == nil {
				ext := gwExtSanitize(filepath.Ext(key))
				if mime == "" {
					mime = gwMimeFromExt(ext)
				}
				gwInsertFile(key, node.ID, size, mime, ext, origName)
				return gwContentURL(key)
			} else {
				lastError = "节点 " + node.Name + "：" + firstNonEmpty(err2.Error(), err.Error())
			}
			continue
		}
		ext := gwExtSanitize(filepath.Ext(key))
		if mime == "" {
			mime = gwMimeFromExt(ext)
		}
		gwInsertFile(key, node.ID, size, mime, ext, origName)
		return gwContentURL(key)
	}
	ssSetError(lastError)
	return ""
}

// gwChunkedToNode 分片转发：建会话 → 按 gwChunkSize 逐片 POST → 合并。
func gwChunkedToNode(node *gwNode, src io.ReadSeeker, size int64, key, origName, mime string) error {
	chunkSize := loadSiteSettings().GwChunkSize
	if chunkSize < 64<<10 {
		chunkSize = 64 << 10
	}
	uploadID, err := gwNodeCreateSession(node, key, origName, size, mime)
	if err != nil {
		return err
	}
	fail := func(e error) error {
		gwNodeAbort(node, uploadID)
		return e
	}
	buf := make([]byte, chunkSize)
	offset := int64(0)
	for offset < size {
		want := size - offset
		if want > chunkSize {
			want = chunkSize
		}
		if _, err := src.Seek(offset, io.SeekStart); err != nil {
			return fail(errors.New("读取分片失败"))
		}
		n, err := io.ReadFull(src, buf[:want])
		if err != nil && n == 0 {
			return fail(errors.New("读取分片失败"))
		}
		if err := gwNodeChunk(node, uploadID, buf[:n]); err != nil {
			return fail(err)
		}
		offset += int64(n)
	}
	finalSize, err := gwNodeFinish(node, uploadID, key)
	if err != nil {
		return fail(err)
	}
	if finalSize != size {
		return fail(errors.New("节点合并后大小与声明不符"))
	}
	return nil
}

// gwOpenSource 把上传源统一为可 Seek 的读取器（内存数据包一层 Reader）。
func gwOpenSource(file *multipartFileInfo) (io.ReadSeeker, io.Closer, error) {
	if file.tmpPath != "" {
		fp, err := os.Open(file.tmpPath)
		if err != nil {
			return nil, nil, err
		}
		return fp, fp, nil
	}
	if file.data != nil {
		return bytes.NewReader(file.data), nil, nil
	}
	return nil, nil, os.ErrInvalid
}

// ==================== 删除清理（resources.go 调用） ====================

// storageDeleteURL 精确删除资源表中记录的网关文件，兼容旧版随机 key。
func storageDeleteURL(rawURL string) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !strings.HasPrefix(u.Path, "/content/") {
		return
	}
	key, err := url.PathUnescape(strings.TrimPrefix(u.Path, "/content/"))
	if err != nil || !gwKeyIsValid(key) {
		return
	}
	if row := gwGetByKey(key); row != nil {
		_, _ = gwDeleteRow(row, false)
	}
}

// storageDeleteForBase 网关侧按 m<id>. / c<id>. 前缀清理；失败不阻塞主站删除。
func storageDeleteForBase(kind, base string) {
	base = strings.TrimSpace(base)
	if base == "" || strings.Contains(base, "..") {
		return
	}
	id, err := strconv.Atoi(base)
	if err != nil || id < 0 {
		return
	}
	if kind == "covers" {
		removeCoverFilesForBase(base)
		if id > 0 {
			gwDeleteByPrefix("c" + base + ".")
		}
		return
	}
	if kind != "media" || id <= 0 {
		return
	}
	gwDeleteByPrefix("m" + base + ".")
}

// ==================== 小工具 ====================

// gwFormatBytes 容量展示（后台与错误提示用）。
func gwFormatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return strconv.FormatFloat(float64(n)/(1<<30), 'f', 1, 64) + " GB"
	case n >= 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MB"
	case n >= 1024:
		return strconv.FormatInt(n/1024, 10) + " KB"
	default:
		return strconv.FormatInt(n, 10) + " B"
	}
}
