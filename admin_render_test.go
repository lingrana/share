package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// 后台整页渲染回归：真实 SQLite + 管理员会话，完整执行 home_admin 模板，
// 兜底「存储网关」新选项卡的模板运行期错误（缺键、类型不符等）。

func TestAdminPageRendersWithGatewayTab(t *testing.T) {
	initTemplates()

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

	// 预置节点与文件索引，让「存储网关」表格有内容可渲染
	nodeID := dbInsert("gw_nodes", map[string]any{
		"name": "render-node", "base_url": "https://n1.example.com", "token": "tok",
		"enabled": 1, "anti_bot": 1, "free_bytes": 1024, "total_bytes": 2048, "used_bytes": 1024,
	})
	if nodeID <= 0 {
		t.Fatalf("节点入库失败")
	}
	dbInsert("gw_files", map[string]any{
		"file_key": "m7.mp3", "node_id": nodeID, "size_bytes": 7,
		"mime": "audio/mpeg", "ext": "mp3", "orig_name": "x.mp3", "created_at": "2026-01-01 00:00:00",
	})

	// 管理员会话
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/admin", nil)
	s := startSession(w, r)
	s.AdminLoggedIn = true

	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodGet, "/admin", nil)
	r2.AddCookie(&http.Cookie{Name: sessionCookieName, Value: s.ID})
	handleAdmin(w2, r2)

	if w2.Code != http.StatusOK {
		t.Fatalf("后台页应 200, got %d", w2.Code)
	}
	body, _ := io.ReadAll(w2.Body)
	html := string(body)
	for _, want := range []string{
		"存储网关", "data-tab-target=\"gateway\"", "render-node",
		"添加节点", "文件索引", "m7.mp3", "刷新全部节点容量",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("后台页缺少 %q", want)
		}
	}

	// 编辑态渲染（gw_edit=1 应回填节点表单且不报错）
	w3 := httptest.NewRecorder()
	r3 := httptest.NewRequest(http.MethodGet, "/admin?gw_edit="+itoaSafe(int(nodeID)), nil)
	r3.AddCookie(&http.Cookie{Name: sessionCookieName, Value: s.ID})
	handleAdmin(w3, r3)
	if w3.Code != http.StatusOK {
		t.Fatalf("编辑态后台页应 200, got %d", w3.Code)
	}
	body3, _ := io.ReadAll(w3.Body)
	if !strings.Contains(string(body3), `value="render-node"`) || !strings.Contains(string(body3), `value="https://n1.example.com"`) {
		t.Errorf("编辑态应回填节点表单")
	}
	if !strings.Contains(string(body3), "保存节点") {
		t.Errorf("编辑态应显示保存节点按钮")
	}
}
