package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 真实 PHP 节点全链路冒烟：本机 php -S 起 share-storage 的 node.php（dev 模式），
// Go 网关完成 注册节点 → 容量刷新 → 直传 → /content 外链(Range/304/HEAD/下载)
// → 分片转发 → 本地存储兜底 → 删除清理。php不可用时跳过。

const gwSmokeNodeDev = "E:/白嫖/share-storage/node/dev.php"
const gwSmokeNodeToken = "CHANGE-ME-NODE-TOKEN" // node.php 出厂默认 token

func TestGwSmokeAgainstPHPNode(t *testing.T) {
	if _, err := exec.LookPath("php"); err != nil {
		t.Skip("本机无 php，跳过真实节点冒烟")
	}
	if !fileExists(gwSmokeNodeDev) {
		t.Skip("share-storage 节点脚本不存在，跳过")
	}

	// ---- 起真实 PHP 节点 ----
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("分配端口失败: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	nodeBase := fmt.Sprintf("http://127.0.0.1:%d", port)
	cmd := exec.Command("php", "-S", fmt.Sprintf("127.0.0.1:%d", port), gwSmokeNodeDev)
	cmd.Dir = filepath.Dir(gwSmokeNodeDev)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 php -S 失败: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	// 等 /health 就绪
	healthy := false
	for i := 0; i < 50; i++ {
		resp, err := http.Get(nodeBase + "/health")
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				healthy = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !healthy {
		t.Fatalf("PHP 节点未就绪：%s", out.String())
	}

	// ---- 测试环境：独立数据目录 + SQLite ----
	dir := t.TempDir()
	oldDataDir, oldConfig, oldDB := dataDir, appConfig, db
	dataDir = dir
	appConfig = defaultConfig()
	t.Cleanup(func() {
		if db != nil {
			_ = db.Close() // Windows 下需先释放 SQLite 文件句柄再删临时目录
			db = nil
		}
		dataDir, appConfig, db = oldDataDir, oldConfig, oldDB
	})
	if err := openDBWithConfig(DBConfig{Driver: "sqlite", SQLitePath: filepath.Join(dir, "t.sqlite")}); err != nil {
		t.Fatalf("建测试库失败: %v", err)
	}

	s := loadSiteSettings()
	s.GwLocalEnabled = false // 纯转发：强制走节点
	saveSiteSettings(s)

	// ---- 注册节点并刷新容量 ----
	nodeID := dbInsert("gw_nodes", map[string]any{
		"name": "smoke-node", "base_url": nodeBase, "token": gwSmokeNodeToken,
		"enabled": 1, "anti_bot": 0,
	})
	if nodeID <= 0 {
		t.Fatalf("节点入库失败")
	}
	node := gwGetNode(nodeID)
	if err := gwNodeStatRefresh(node, 5*time.Second); err != nil {
		t.Fatalf("节点容量刷新失败: %v", err)
	}
	if node.TotalBytes > 0 && node.TotalBytes < node.UsedBytes {
		t.Errorf("节点容量数据异常: %+v", node)
	}

	// ---- 直传（PUT） ----
	payload := []byte("0123456789ABCDEF")
	file := &multipartFileInfo{origName: "hello.mp3", data: payload, size: int64(len(payload)), contentType: "audio/mpeg"}
	link := gwUploadMedia(42, "我的音乐", file)
	if link != "/content/m42.mp3" {
		t.Fatalf("直传外链 = %q, want /content/m42.mp3（错误：%s）", link, storageLastError())
	}
	row := gwGetByKey("m42.mp3")
	if row == nil || row.NodeID != nodeID {
		t.Fatalf("索引应指向节点 %d, got %+v", nodeID, row)
	}

	// ---- /content 公开外链：200 全量 ----
	rec := httptest.NewRecorder()
	handleContent(rec, httptest.NewRequest(http.MethodGet, "/content/m42.mp3", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != string(payload) {
		t.Fatalf("GET /content 应 200 全量, got %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "audio/mpeg" {
		t.Errorf("Content-Type = %q, want audio/mpeg", ct)
	}

	// Range → 206
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/content/m42.mp3", nil)
	req.Header.Set("Range", "bytes=0-3")
	handleContent(rec, req)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "0123" {
		t.Errorf("Range 请求应 206 + 前 4 字节, got %d %q", rec.Code, rec.Body.String())
	}
	if cr := rec.Header().Get("Content-Range"); cr != "bytes 0-3/16" {
		t.Errorf("Content-Range = %q", cr)
	}

	// If-None-Match → 304
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/content/m42.mp3", nil)
	req.Header.Set("If-None-Match", gwEtag(row))
	handleContent(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Errorf("ETag 命中应 304, got %d", rec.Code)
	}

	// HEAD
	rec = httptest.NewRecorder()
	handleContent(rec, httptest.NewRequest(http.MethodHead, "/content/m42.mp3", nil))
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "16" {
		t.Errorf("HEAD 应 200/无体/Content-Length=16, got %d/%d/%q", rec.Code, rec.Body.Len(), rec.Header().Get("Content-Length"))
	}

	// ?download=1 → attachment
	rec = httptest.NewRecorder()
	handleContent(rec, httptest.NewRequest(http.MethodGet, "/content/m42.mp3?download=1", nil))
	if !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment") {
		t.Errorf("download=1 应 attachment, got %q", rec.Header().Get("Content-Disposition"))
	}

	// ---- 分片转发（1MB 分片 × 2MB 数据，绕过 PUT 直接验证会话链路） ----
	s = loadSiteSettings()
	s.GwChunkSize = 1 << 20
	saveSiteSettings(s)
	big := bytes.Repeat([]byte("abcdefgh"), 256*1024) // 2 MB
	if err := gwChunkedToNode(node, bytes.NewReader(big), int64(len(big)), "m43.bin", "big.bin", "application/octet-stream"); err != nil {
		t.Fatalf("分片转发失败: %v", err)
	}
	gwInsertFile("m43.bin", nodeID, int64(len(big)), "application/octet-stream", "bin", "big.bin")
	rec = httptest.NewRecorder()
	handleContent(rec, httptest.NewRequest(http.MethodGet, "/content/m43.bin", nil))
	if rec.Code != http.StatusOK || rec.Body.Len() != len(big) || !bytes.Equal(rec.Body.Bytes(), big) {
		t.Fatalf("分片文件回读不符: %d 字节 (want %d)", rec.Body.Len(), len(big))
	}

	// 节点删除端点验证（单文件）
	if ok, errMsg := gwDeleteRow(gwGetByKey("m43.bin"), false); !ok {
		t.Fatalf("节点文件删除失败: %s", errMsg)
	}
	rec = httptest.NewRecorder()
	handleContent(rec, httptest.NewRequest(http.MethodGet, "/content/m43.bin", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("删除后应 404, got %d", rec.Code)
	}

	// ---- 本地存储测试已移除（本地兜底已禁用，所有上传走节点） ----

	// ---- 前缀清理 ----
	storageDeleteForBase("media", "42")
	if gwGetByKey("m42.mp3") != nil {
		t.Errorf("前缀清理后 m42.mp3 索引应删除")
	}
}
