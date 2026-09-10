package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestMain 为包内测试打开临时 SQLite：主题存取（settings 表）依赖数据库。
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sharetest")
	if err != nil {
		fmt.Println("tempdir:", err)
		os.Exit(1)
	}
	dataDir = dir
	if err := openDBWithConfig(DBConfig{Driver: "sqlite", SQLitePath: "test.sqlite"}); err != nil {
		fmt.Println("test db:", err)
		os.Exit(1)
	}
	code := m.Run()
	db.Close()
	os.RemoveAll(dir)
	os.Exit(code)
}

// 主题校验：必填变量、长度上限、字符白名单（对应 THEMES.md 4.3）。
func TestValidateThemeCSS(t *testing.T) {
	valid := `:root { --bg: #001018; --fg: #EAF6FF; --border: #EAF6FF; }`
	if err := validateThemeCSS(valid); err != nil {
		t.Fatalf("合法主题被拒: %v", err)
	}
	if err := validateThemeCSS(""); err == nil {
		t.Fatal("空 CSS 应被拒")
	}
	if err := validateThemeCSS("/* only comment */"); err == nil {
		t.Fatal("无规则块应被拒")
	}
	// 布局修改型主题：无变量定义、仅组件覆盖 —— 主题允许改布局
	layout := ".resource-grid { grid-template-columns: repeat(2, 1fr); }\n.mono-btn { border-radius: 6px; }"
	if err := validateThemeCSS(layout); err != nil {
		t.Fatalf("布局型主题被拒: %v", err)
	}
	if err := validateThemeCSS(":root{--bg:#000;--fg:#fff}a{background:url(x</style>)}"); err == nil {
		t.Fatal("含 < 的 url 值应被拒")
	}
	huge := ":root{--bg:#000;--fg:#fff;/*" + strings.Repeat("x", 33<<10) + "*/}"
	if err := validateThemeCSS(huge); err == nil {
		t.Fatal("超长 CSS 应被拒")
	}
	for _, bad := range []string{
		`:root{--bg:#000;--fg:#fff}a[href^="java\0"]{}`, // 反斜杠
		`:root{--bg:#000;--fg:#fff}</style><script>`,    // HTML 逃逸（< 被拦）
		`:root{--bg:#000;--fg:#fff}/*&*/`,               // & 被拦
	} {
		if err := validateThemeCSS(bad); err == nil {
			t.Fatalf("危险 CSS 应被拒: %q", bad[:30])
		}
	}
}

// 解析：内置/自定义/未知 id 的回落行为。
func TestResolveTheme(t *testing.T) {
	set := siteSettings{DefaultTheme: ""}
	attr, css, meta := resolveTheme(set)
	if attr != "mono" || css != "" || meta.ID != "mono" {
		t.Fatalf("空应回落 mono, got %q %q %q", attr, css, meta.ID)
	}

	// 旧版 default_theme="dark" 兼容映射为 mono（暗色现为访客端 data-mode）
	set.DefaultTheme = "dark"
	attr, css, meta = resolveTheme(set)
	if attr != "mono" || meta.ID != "mono" {
		t.Fatalf("dark 兼容映射错误: %q %q", attr, meta.ID)
	}

	// 自定义主题
	list := []customTheme{
		{themeMeta: themeMeta{ID: "custom_deadbeef", Name: "测试"}, CSS: ":root{--bg:#000;--fg:#fff}"},
	}
	saveCustomThemes(list)
	defer saveCustomThemes(nil)

	set.DefaultTheme = "custom_deadbeef"
	attr, css, meta = resolveTheme(set)
	if attr != "custom_deadbeef" || !strings.Contains(css, "--fg:#fff") || meta.Name != "测试" {
		t.Fatalf("自定义解析错误: %q %q %+v", attr, css, meta)
	}

	// 未知 id 回落
	set.DefaultTheme = "custom_gone"
	attr, _, meta = resolveTheme(set)
	if attr != "mono" || meta.ID != "mono" {
		t.Fatalf("未知 id 应回落 mono: %q %q", attr, meta.ID)
	}
}

// 删除正在使用的主题 → saveSettingKv 置空由 action 层负责；此处验证清单存取。
func TestCustomThemeStore(t *testing.T) {
	defer saveCustomThemes(nil)
	if loadCustomThemes() != nil {
		t.Fatal("初始应为空")
	}
	list := []customTheme{
		{themeMeta: themeMeta{ID: "custom_a", Name: "甲"}, CSS: ":root{--bg:#000;--fg:#fff}"},
		{themeMeta: themeMeta{ID: "custom_b", Name: "乙"}, CSS: ":root{--bg:#111;--fg:#eee}"},
	}
	saveCustomThemes(list)
	got := loadCustomThemes()
	if len(got) != 2 || got[0].Name != "甲" || got[1].ID != "custom_b" {
		t.Fatalf("存取不一致: %+v", got)
	}
	if getCustomTheme("custom_b") == nil || getCustomTheme("custom_z") != nil {
		t.Fatal("getCustomTheme 查找错误")
	}
}

// 内置清单：后台下拉框数据源，mono 必须存在。
func TestBuiltinThemesList(t *testing.T) {
	ids := map[string]bool{}
	for _, th := range builtinThemes() {
		ids[th.ID] = true
		if !themeIDValid(th.ID) {
			t.Fatalf("内置主题 id 不合法: %q", th.ID)
		}
	}
	if !ids["mono"] {
		t.Fatal("缺少内置主题 mono")
	}
	if ids["dark"] {
		t.Fatal("dark 不应作为独立主题（已并入 mono 暗色变体）")
	}
	if !isBuiltinTheme("dark") {
		t.Fatal("旧 id dark 应保持内置识别以便兼容")
	}
}

// 主题样式块与内置清单一致性：style.css 必须为每个非 mono 内置主题提供变量块。
func TestBuiltinThemeCSSExists(t *testing.T) {
	css, err := embeddedFS.ReadFile("assets/style.css")
	if err != nil {
		t.Fatalf("读 style.css: %v", err)
	}
	s := string(css)
	// 全局暗色兜底必须存在（mono 暗色变体）
	if !strings.Contains(s, `:root[data-mode="dark"]`) {
		t.Fatal("style.css 缺少全局暗色兜底")
	}
	// 已删除的内置主题不应残留样式块
	for _, gone := range []string{"parchment", "sepia", "cyber"} {
		if strings.Contains(s, `data-theme="`+gone+`"`) {
			t.Fatalf("style.css 残留已删除主题 %s 的样式块", gone)
		}
	}
}

// zip 包解析：正常包 / 缺 theme.css / 非法 CSS / 名称回退。
func TestParseThemeZip(t *testing.T) {
	buildZip := func(files map[string]string) []byte {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for name, content := range files {
			w, err := zw.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			w.Write([]byte(content))
		}
		zw.Close()
		return buf.Bytes()
	}

	good := buildZip(map[string]string{
		"theme.json": `{"name": "深海蓝", "description": "测试"}`,
		"theme.css":  ":root { --bg: #001018; --fg: #EAF6FF; }",
	})
	pkg, err := parseThemeZip(good, "any.zip")
	if err != nil {
		t.Fatalf("合法包被拒: %v", err)
	}
	if pkg.Name != "深海蓝" || !strings.Contains(pkg.CSS, "--bg") {
		t.Fatalf("解析结果错误: %+v", pkg)
	}

	// 无 theme.json → 回退 zip 文件名
	pkg, err = parseThemeZip(buildZip(map[string]string{"theme.css": ":root{--bg:#000;--fg:#fff}"}), "我的主题.zip")
	if err != nil || pkg.Name != "我的主题" {
		t.Fatalf("文件名回退失败: %+v, %v", pkg, err)
	}

	// 缺 theme.css
	if _, err := parseThemeZip(buildZip(map[string]string{"readme.txt": "hi"}), "x.zip"); err == nil {
		t.Fatal("缺 theme.css 应被拒")
	}
	// 非法 CSS（< 拦截）
	if _, err := parseThemeZip(buildZip(map[string]string{"theme.css": "</style><script>x"}), "x.zip"); err == nil {
		t.Fatal("非法 CSS 应被拒")
	}
	// 不是 zip
	if _, err := parseThemeZip([]byte("not a zip"), "x.zip"); err == nil {
		t.Fatal("非 zip 内容应被拒")
	}
}
