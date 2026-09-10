package main

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 复现用户报告的三个问题中的两个（服务端行为）：
// 1) 上传的资源删除后，节点上的文件应跟着删除
// 3) 后台「社交媒体与交流群」应能保存

const (
	gwSmokeNodeDev   = "E:/白嫖/share-storage/node/dev.php"
	gwSmokeNodeToken = "CHANGE-ME-NODE-TOKEN" // node.php 出厂默认 token
)

// gwIssueStartNode 起一个真实 PHP 节点，返回 base URL。
func gwIssueStartNode(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("php"); err != nil {
		t.Skip("本机无 php，跳过")
	}
	if !fileExists(gwSmokeNodeDev) {
		t.Skip("share-storage 节点脚本不存在，跳过")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("分配端口失败: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	cmd := exec.Command("php", "-S", fmt.Sprintf("127.0.0.1:%d", port), gwSmokeNodeDev)
	cmd.Dir = filepath.Dir(gwSmokeNodeDev)
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 php -S 失败: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	for i := 0; i < 50; i++ {
		resp, err := http.Get(base + "/health")
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return base
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("PHP 节点未就绪")
	return ""
}

// gwIssueEnv 独立数据目录 + SQLite + 管理员会话。
func gwIssueEnv(t *testing.T) *Session {
	t.Helper()
	dir := t.TempDir()
	oldDataDir, oldConfig, oldDB := dataDir, appConfig, db
	dataDir = dir
	appConfig = defaultConfig()
	t.Cleanup(func() {
		if db != nil {
			_ = db.Close()
			db = nil
		}
		dataDir, appConfig, db = oldDataDir, oldConfig, oldDB
	})
	if err := openDBWithConfig(DBConfig{Driver: "sqlite", SQLitePath: filepath.Join(dir, "t.sqlite")}); err != nil {
		t.Fatalf("建测试库失败: %v", err)
	}
	initTemplates()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/admin", nil)
	sess := startSession(w, r)
	sess.AdminLoggedIn = true
	return sess
}

func gwIssuePostForm(t *testing.T, sess *Session, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/admin", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	rec := httptest.NewRecorder()
	handlePostAction(rec, req)
	return rec
}

// ---- 问题 3：社交链接保存 ----

func TestIssueSocialLinksSave(t *testing.T) {
	sess := gwIssueEnv(t)

	form := url.Values{}
	form.Set("action", "update_site_content")
	form.Set("csrf", sess.CSRFToken)
	form.Add("social_name[]", "交流群")
	form.Add("social_url[]", "https://qm.qq.com/abc")
	form.Add("social_name[]", "GitHub")
	form.Add("social_url[]", "https://github.com/example")
	form.Add("social_name[]", "裸域名")
	form.Add("social_url[]", "t.me/mychannel")
	form.Set("grid_columns", "4")
	rec := gwIssuePostForm(t, sess, form)

	saved := loadSiteSettings()
	if len(saved.SocialLinks) != 3 {
		t.Fatalf("社交链接应保存 3 条, got %d 条 %+v（HTTP %d）", len(saved.SocialLinks), saved.SocialLinks, rec.Code)
	}
	if saved.SocialLinks[0].Name != "交流群" || saved.SocialLinks[0].URL != "https://qm.qq.com/abc" {
		t.Errorf("第一条不符: %+v", saved.SocialLinks[0])
	}
	if saved.SocialLinks[2].URL != "https://t.me/mychannel" {
		t.Errorf("裸域名应原样保存: %+v", saved.SocialLinks[2])
	}

	// 后台页应回显
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/admin", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	handleAdmin(w, r)
	body, _ := io.ReadAll(w.Body)
	if !strings.Contains(string(body), `value="交流群"`) || !strings.Contains(string(body), `value="https://qm.qq.com/abc"`) {
		t.Errorf("后台页应回显已保存的社交链接")
	}
}

// ---- 问题 1：资源删除 → 节点文件清理 ----

func TestIssueResourceDeletePurgesNode(t *testing.T) {
	nodeBase := gwIssueStartNode(t)
	sess := gwIssueEnv(t)

	s := loadSiteSettings()
	s.GwLocalEnabled = false // 纯转发：媒体强制上节点
	saveSiteSettings(s)
	nodeID := dbInsert("gw_nodes", map[string]any{
		"name": "issue-node", "base_url": nodeBase, "token": gwSmokeNodeToken, "enabled": 1,
	})

	// 真实 create_resource：multipart 表单 + media_file 上传到节点
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("action", "create_resource")
	_ = mw.WriteField("csrf", sess.CSRFToken)
	_ = mw.WriteField("title", "节点清理测试")
	_ = mw.WriteField("folder_id", "0")
	_ = mw.WriteField("resource_type", "audio")
	_ = mw.WriteField("tags", "")
	part, err := mw.CreateFormFile("media_file", "hello.mp3")
	if err != nil {
		t.Fatalf("构造文件字段失败: %v", err)
	}
	payload := []byte("0123456789ABCDEF")
	_, _ = part.Write(payload)
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/admin", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	rec := httptest.NewRecorder()
	handlePostAction(rec, req)

	res := dbFetchOne("SELECT id, media_url FROM resources ORDER BY id DESC LIMIT 1")
	if res == nil {
		t.Fatalf("资源未创建（HTTP %d）", rec.Code)
	}
	resID := int64(intVal(res, "id"))
	mediaURL := str(res, "media_url")
	wantKey := "m" + strconv.FormatInt(resID, 10) + ".mp3"
	if mediaURL != "/content/"+wantKey {
		t.Fatalf("media_url = %q, want /content/%s（上传失败原因：%s）", mediaURL, wantKey, storageLastError())
	}
	row := gwGetByKey(wantKey)
	if row == nil || row.NodeID != nodeID {
		t.Fatalf("索引应指向节点 %d, got %+v", nodeID, row)
	}

	// 节点上确实存在该文件（带签名 GET 应 200）
	node := gwGetNode(nodeID)
	checkURL := gwSignDownloadURL(node, wantKey)
	resp, err := http.Get(checkURL)
	if err != nil {
		t.Fatalf("节点读取失败: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("删除前节点文件应 200, got %d", resp.StatusCode)
	}

	// 真实 delete_resource
	delForm := url.Values{}
	delForm.Set("action", "delete_resource")
	delForm.Set("csrf", sess.CSRFToken)
	delForm.Set("resource_id", strconv.FormatInt(resID, 10))
	gwIssuePostForm(t, sess, delForm)

	if gwGetByKey(wantKey) != nil {
		t.Errorf("删除资源后 gw_files 索引应清空")
	}
	resp2, err := http.Get(gwSignDownloadURL(node, wantKey))
	if err != nil {
		t.Fatalf("节点读取失败: %v", err)
	}
	io.Copy(io.Discard, resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("删除资源后节点文件应 404, got %d —— 节点文件未被清理", resp2.StatusCode)
	}
}
