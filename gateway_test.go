package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// 内置存储网关单元测试：key 校验、MIME、Range、ETag、HMAC、反爬求解、调度语义。

// ==================== key 校验 ====================

func TestGwKeyIsValid(t *testing.T) {
	valid := []string{"m1.mp3", "a", "A-b_9.zip", "abc.123.tar.gz", strings.Repeat("x", 120)}
	for _, k := range valid {
		if !gwKeyIsValid(k) {
			t.Errorf("gwKeyIsValid(%q) = false, want true", k)
		}
	}
	invalid := []string{
		"", strings.Repeat("a", 121),
		".mp3", // 非字母数字开头
		"-abc", // 同上
		"a/b",  // 路径分隔符
		"a\\b", // 路径分隔符
		"a..b", // ..
		"a.php", "a.phtml", "a.phar", "x.htaccess", "y.cgi", "z.jsp", "f.INI", "g.Log", "h.php8",
	}
	for _, k := range invalid {
		if gwKeyIsValid(k) {
			t.Errorf("gwKeyIsValid(%q) = true, want false", k)
		}
	}
}

func TestGwNewKeyBlockedExt(t *testing.T) {
	for _, ext := range []string{"php", "PHP5", "phtml", "log", "", "危险!!"} {
		key := gwNewKey(ext)
		if !gwKeyIsValid(key) {
			t.Errorf("gwNewKey(%q) = %q, 生成的 key 不合法", ext, key)
		}
		if !strings.HasSuffix(key, ".bin") {
			t.Errorf("gwNewKey(%q) = %q, 非法扩展名应回退 .bin", ext, key)
		}
	}
	if key := gwNewKey("MP3"); !strings.HasSuffix(key, ".mp3") {
		t.Errorf("gwNewKey(MP3) = %q, want .mp3 后缀", key)
	}
}

// ==================== MIME ====================

func TestGwMimeFromExt(t *testing.T) {
	cases := map[string]string{
		"jpg": "image/jpeg", "mp3": "audio/mpeg", "mp4": "video/mp4",
		"PDF": "application/pdf", "unknown": "application/octet-stream",
	}
	for ext, want := range cases {
		if got := gwMimeFromExt(ext); got != want {
			t.Errorf("gwMimeFromExt(%q) = %q, want %q", ext, got, want)
		}
	}
}

func TestGwEffectiveMimeFallback(t *testing.T) {
	row := &gwFileRow{Key: "m1.xyz"}
	if got := gwEffectiveMime(row); got != "application/octet-stream" {
		t.Errorf("未知扩展名应回退 octet-stream, got %q", got)
	}
	row2 := &gwFileRow{Key: "m2.bin", Ext: "mp4"}
	if got := gwEffectiveMime(row2); got != "video/mp4" {
		t.Errorf("octet-stream 索引应回退扩展名映射, got %q", got)
	}
	row3 := &gwFileRow{Key: "m3.mp3", Mime: "audio/mpeg"}
	if got := gwEffectiveMime(row3); got != "audio/mpeg" {
		t.Errorf("已有 mime 应原样保留, got %q", got)
	}
}

// ==================== Range ====================

func TestGwParseClientRange(t *testing.T) {
	if r, invalid := gwParseClientRange(100, ""); r != nil || invalid {
		t.Errorf("空 Range 应返回 nil,false; got %v,%v", r, invalid)
	}
	r, invalid := gwParseClientRange(100, "bytes=0-")
	if invalid || r == nil || r.Start != 0 || r.End != 99 {
		t.Errorf("bytes=0- 解析错误: %+v invalid=%v", r, invalid)
	}
	r, _ = gwParseClientRange(100, "bytes=10-19")
	if r == nil || r.Start != 10 || r.End != 19 {
		t.Errorf("bytes=10-19 解析错误: %+v", r)
	}
	r, _ = gwParseClientRange(100, "bytes=90-999")
	if r == nil || r.Start != 90 || r.End != 99 {
		t.Errorf("末段越界应收敛到 size-1: %+v", r)
	}
	r, _ = gwParseClientRange(100, "bytes=-10")
	if r == nil || r.Start != 90 || r.End != 99 {
		t.Errorf("bytes=-10 应解析为最后 10 字节: %+v", r)
	}
	for _, h := range []string{"bytes=-", "bytes=-0", "bytes=99-10", "bytes=100-"} {
		r, invalid := gwParseClientRange(100, h)
		if !invalid || r != nil {
			t.Errorf("Range %q 应判为 invalid; got %+v invalid=%v", h, r, invalid)
		}
	}
	if r, invalid := gwParseClientRange(100, "items=0-5"); r != nil || invalid {
		t.Errorf("非 bytes Range 应按无 Range 处理: %v %v", r, invalid)
	}
}

// ==================== ETag / Content-Disposition ====================

func TestGwEtag(t *testing.T) {
	row := &gwFileRow{Key: "m1.mp3", SizeBytes: 12345}
	want := `"` + gwMD5Hex("m1.mp3|12345") + `"`
	if got := gwEtag(row); got != want {
		t.Errorf("gwEtag = %q, want %q（与节点端 md5(key|size) 一致）", got, want)
	}
}

func TestGwDisposition(t *testing.T) {
	row := &gwFileRow{Key: "m1.mp3", OrigName: "坏\"名字\n换行.mp3"}
	d := gwDisposition(true, row)
	if !strings.HasPrefix(d, `attachment; filename="`) {
		t.Errorf("下载应为 attachment, got %q", d)
	}
	// filename 段内不允许再有引号或换行（头注入防护）
	inner := d[len(`attachment; filename="`):]
	if i := strings.IndexByte(inner, '"'); i < 0 || strings.ContainsAny(inner[:i], "\r\n") {
		t.Errorf("filename 内应剔除引号与换行: %q", d)
	}
	if strings.Contains(d, `"`+row.OrigName+`"`) {
		t.Errorf("原始危险文件名不应原样出现: %q", d)
	}
	if got := gwDisposition(false, row); got != "inline" {
		t.Errorf("非下载应 inline, got %q", got)
	}
}

// ==================== 反爬挑战求解 ====================

func gwAESCBCEncrypt(t *testing.T, key, iv, plain []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	if len(plain)%block.BlockSize() != 0 {
		t.Fatalf("plain 长度需为块大小整数倍")
	}
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, plain)
	return out
}

func TestGwSolveChallenge(t *testing.T) {
	key := []byte("0123456789abcdef")
	iv := []byte("fedcba9876543210")
	plain := []byte("cookievalue16by.") // 16 字节，对齐 AES 块
	ct := gwAESCBCEncrypt(t, key, iv, plain)
	body := `<html><script src="https://example.com/aes.js"></script><script>
toNumbers("` + hex.EncodeToString(key) + `");
toNumbers("` + hex.EncodeToString(iv) + `");
toNumbers("` + hex.EncodeToString(ct) + `");
document.cookie="ss_clear=" + slowAES.decrypt(c, 2, a, b) + "; expires=...";
</script></html>`
	want := "ss_clear=" + hex.EncodeToString(plain)
	if got := gwSolveChallenge(body); got != want {
		t.Errorf("gwSolveChallenge = %q, want %q", got, want)
	}

	if got := gwSolveChallenge("<html>hello</html>"); got != "" {
		t.Errorf("普通页面应返回空, got %q", got)
	}
	if got := gwSolveChallenge(`toNumbers("` + strings.Repeat("aa", 16) + `") aes.js`); got != "" {
		t.Errorf("字段不全应返回空, got %q", got)
	}
}

// ==================== HMAC 下载签名 ====================

func TestGwSignDownloadURL(t *testing.T) {
	node := &gwNode{ID: 1, Name: "n1", BaseURL: "https://n1.example.com/", Token: "tok"}
	u := gwSignDownloadURL(node, "m1.mp3")
	if !strings.HasPrefix(u, "https://n1.example.com/files/m1.mp3?e=") {
		t.Fatalf("签名 URL 前缀不对: %q", u)
	}
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatalf("URL 解析失败: %v", err)
	}
	q := parsed.Query()
	e, s := q.Get("e"), q.Get("s")
	if e == "" || s == "" {
		t.Fatalf("签名 URL 缺 e/s 参数: %q", u)
	}
	if _, err := strconv.ParseInt(e, 10, 64); err != nil {
		t.Errorf("e 应为纯数字: %q", e)
	}
	if want := gwHMACHex("tok", "m1.mp3|"+e); s != want {
		t.Errorf("签名不匹配: got %q want %q", s, want)
	}
}

// ==================== bodyless 状态分支 ====================

func TestGwIsBodylessStatus(t *testing.T) {
	if !gwIsBodylessStatus(http.StatusNotModified) || !gwIsBodylessStatus(http.StatusRequestedRangeNotSatisfiable) {
		t.Errorf("304/416 应为 bodyless")
	}
	if gwIsBodylessStatus(http.StatusOK) || gwIsBodylessStatus(http.StatusPartialContent) {
		t.Errorf("200/206 不属于 bodyless")
	}
}

// ==================== 304 / 416 转发（带 ResponseWriter） ====================

func TestGwForwardBodylessStatusWrites(t *testing.T) {
	row := &gwFileRow{Key: "m1.mp3", SizeBytes: 1000}

	rec := httptest.NewRecorder()
	if !gwForwardBodylessStatus(rec, http.StatusNotModified, row, http.Header{}) {
		t.Fatalf("304 应被处理")
	}
	if rec.Code != http.StatusNotModified || rec.Header().Get("ETag") != gwEtag(row) {
		t.Errorf("304 响应头不符: %d %q", rec.Code, rec.Header().Get("ETag"))
	}

	rec = httptest.NewRecorder()
	h := http.Header{}
	h.Set("Content-Range", "bytes 5-9/1000")
	if !gwForwardBodylessStatus(rec, http.StatusRequestedRangeNotSatisfiable, row, h) {
		t.Fatalf("416 应被处理")
	}
	if rec.Code != http.StatusRequestedRangeNotSatisfiable || rec.Header().Get("Content-Range") != "bytes 5-9/1000" {
		t.Errorf("416 应透传节点 Content-Range: %d %q", rec.Code, rec.Header().Get("Content-Range"))
	}

	rec = httptest.NewRecorder()
	h2 := http.Header{}
	if !gwForwardBodylessStatus(rec, http.StatusRequestedRangeNotSatisfiable, row, h2) {
		t.Fatalf("416 应被处理")
	}
	if want := "bytes */1000"; rec.Header().Get("Content-Range") != want {
		t.Errorf("节点无 Content-Range 时应合成 %q, got %q", want, rec.Header().Get("Content-Range"))
	}

	rec = httptest.NewRecorder()
	if gwForwardBodylessStatus(rec, http.StatusOK, row, http.Header{}) {
		t.Errorf("200 不属于 bodyless")
	}
}

// ==================== 候选调度（仅节点模式） ====================

func TestGwCandidatesNodeSemantics(t *testing.T) {
	oldDB := db
	db = nil
	t.Cleanup(func() { db = oldDB })

	s := defaultSiteSettings()
	s.GwLocalEnabled = false

	// 无节点时应无候选
	cands := gwCandidatesWithSettings(s, 1)
	if len(cands) != 0 {
		t.Fatalf("无节点应无候选; got %+v", cands)
	}

	// 关闭本地且无节点应无候选
	s.GwLocalEnabled = false
	if cands := gwCandidatesWithSettings(s, 1); len(cands) != 0 {
		t.Fatalf("关闭本地且无节点应无候选; got %+v", cands)
	}
}

// ==================== 设置解析 ====================

func TestGwParseBytesInput(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"1024", 1024},
		{"1KB", 1024},
		{"2mb", 2 << 20},
		{"3 GB", 3 << 30},
		{"0", 0},
		{"", 99},    // 空 → fallback
		{"abc", 99}, // 非法 → fallback
		{"-5", 99},  // 负数 → fallback
	}
	for _, c := range cases {
		if got := gwParseBytesInput(c.in, 99, 0); got != c.want {
			t.Errorf("gwParseBytesInput(%q) = %d, want %d", c.in, got, c.want)
		}
	}
	if got := gwParseBytesInput("1", 99, 1024); got != 1024 {
		t.Errorf("低于下限应抬到 minBytes, got %d", got)
	}
}

// ==================== 本地 304 / HEAD 语义（不走节点） ====================

func TestGwServeHeadHeaders(t *testing.T) {
	row := &gwFileRow{Key: "m1.mp3", SizeBytes: 1000, Mime: "audio/mpeg"}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/content/m1.mp3", nil)
	gwServeHead(rec, req, row, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD 应 200, got %d", rec.Code)
	}
	if cl := rec.Header().Get("Content-Length"); cl != "1000" {
		t.Errorf("HEAD Content-Length = %q, want 1000", cl)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD 不应有 body")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "audio/mpeg" {
		t.Errorf("Content-Type = %q", ct)
	}

	// 条件请求命中 ETag → 304
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodHead, "/content/m1.mp3", nil)
	req.Header.Set("If-None-Match", gwEtag(row))
	gwServeHead(rec, req, row, false)
	if rec.Code != http.StatusNotModified {
		t.Errorf("ETag 命中应 304, got %d", rec.Code)
	}

	// 非法 Range → 416
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodHead, "/content/m1.mp3", nil)
	req.Header.Set("Range", "bytes=5000-")
	gwServeHead(rec, req, row, false)
	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("越界 Range 应 416, got %d", rec.Code)
	}
}

// ==================== 上传源封装 ====================

func TestGwOpenSourceInMemory(t *testing.T) {
	f := &multipartFileInfo{data: []byte("hello")}
	src, closer, err := gwOpenSource(f)
	if err != nil || closer != nil {
		t.Fatalf("内存源应无 closer 且无错误: %v", err)
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	got, err := io.ReadAll(src)
	if err != nil || string(got) != "hello" {
		t.Fatalf("内存源读取: %q %v", got, err)
	}
	// Seeker 接口满足（节点直传与分片都要 seek）
	if _, ok := src.(io.ReadSeeker); !ok {
		t.Fatalf("源应实现 io.ReadSeeker")
	}
}

func TestGwUploadMediaValidations(t *testing.T) {
	// 不依赖数据库/网络的校验分支
	if link := gwUploadMedia(1, "t", nil); link != "" {
		t.Errorf("nil 文件应失败")
	}
	if link := gwUploadMedia(1, "t", &multipartFileInfo{data: []byte{}}); link != "" {
		t.Errorf("空文件应失败")
	}
	if link := gwUploadMedia(1, "t", &multipartFileInfo{data: []byte("x"), origName: "a.php", size: 1}); link != "" {
		t.Errorf("危险扩展名应失败")
	}
	if link := gwUploadMedia(0, "t", &multipartFileInfo{data: []byte("x"), origName: "a.mp3", size: 1}); link != "" {
		t.Errorf("非法资源 id 应失败")
	}
}

// ==================== bytes.Reader 契约（gwNodePut 依赖） ====================

func TestGwSourceSeekAfterPartialRead(t *testing.T) {
	data := []byte("0123456789")
	src := bytes.NewReader(data)
	buf := make([]byte, 4)
	if _, err := io.ReadFull(src, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if pos, _ := src.Seek(0, io.SeekStart); pos != 0 {
		t.Fatalf("seek 后应回到 0, got %d", pos)
	}
	again, _ := io.ReadAll(src)
	if !bytes.Equal(again, data) {
		t.Fatalf("seek 后应能完整重读")
	}
}
