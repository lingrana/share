package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// 主题系统：管理员在后台选择/导入主题，前台只跟随显示；后台面板固定默认黑白主题。
//
// 主题 = 一段 CSS，作用于 html[data-theme="<id>"]，只允许覆盖设计变量（--bg、--fg 等）
// 与主题作用域内的纹理/装饰。变量契约见 go/docs/THEMES.md。

// themeMeta 主题元数据（内置与导入共用）。
type themeMeta struct {
	ID          string `json:"id"`          // slug：^[a-z0-9][a-z0-9_-]{0,31}$
	Name        string `json:"name"`        // 显示名（1~40 字符）
	Description string `json:"description"` // 可选说明（≤200 字符）
	Builtin     bool   `json:"-"`           // 内置主题不可删除/覆盖导入
}

// customThemeStore 存 settings 表 k=custom_themes，v=JSON 数组。
type customThemeStore struct {
	Themes []customTheme `json:"themes"`
}

type customTheme struct {
	themeMeta
	CSS string `json:"css"` // 主题 CSS 正文（≤32KB），限定字符白名单
}

var (
	themeIDRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	themeNameRe = regexp.MustCompile(`^[\p{Han}\p{L}0-9 _·\-]{1,40}$`)
	// CSS 白名单：字母数字、空白（\s 含换行制表）与 #.{}:,:%()'"*/[]-_>@!+~^|=。
	// 拦截反斜杠（\9 hack、字符串逃逸）与 <、&（HTML 上下文逃逸）。
	themeCSSRe = regexp.MustCompile(`^[\w\s#.,:;%()'"{}*/\[\]\-_>@!+~^|=]*$`)
)

// builtinThemes 内置主题清单（CSS 附在 assets/style.css 的 data-theme 块中）。
// 每个主题自带浅色与暗色两个变体：明暗由访客经 data-mode 切换，不作为独立主题。
func builtinThemes() []themeMeta {
	return []themeMeta{
		{ID: "mono", Name: "纸墨（默认）", Description: "极简黑白画报：浅色纸白墨黑，暗色纯黑高对比。", Builtin: true},
	}
}

// isBuiltinTheme 判断 id 是否内置（"dark" 是旧版主题 id，现归入 mono 的暗色变体）。
func isBuiltinTheme(id string) bool {
	if id == "dark" {
		return true
	}
	for _, t := range builtinThemes() {
		if t.ID == id {
			return true
		}
	}
	return false
}

// loadCustomThemes 从 settings 表读导入的主题列表。
func loadCustomThemes() []customTheme {
	v := dbFetchColumnStr("SELECT v FROM settings WHERE k = 'custom_themes'")
	if v == "" {
		return nil
	}
	var store customThemeStore
	if err := json.Unmarshal([]byte(v), &store); err != nil {
		logErr("custom_themes 解析失败: %v", err)
		return nil
	}
	return store.Themes
}

// saveCustomThemes 写回 settings 表。
func saveCustomThemes(list []customTheme) {
	if len(list) == 0 {
		dbDelete("settings", "k = 'custom_themes'")
		return
	}
	raw, err := json.Marshal(customThemeStore{Themes: list})
	if err != nil {
		logErr("custom_themes 序列化失败: %v", err)
		return
	}
	saveSettingKv("custom_themes", string(raw))
}

// getCustomTheme 按 id 取导入主题。
func getCustomTheme(id string) *customTheme {
	list := loadCustomThemes()
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	return nil
}

// validateThemeCSS 校验导入的 CSS：非空、长度上限、字符白名单。
// 主题允许覆盖设计变量，也允许调整布局/组件样式（选择器 + 属性都在白名单内）；
// 反斜杠、<、& 被拦截，从根上排除 </style> 逃逸与 HTML 注入。
func validateThemeCSS(css string) error {
	css = strings.TrimSpace(css)
	if css == "" {
		return fmt.Errorf("CSS 内容为空")
	}
	if len(css) > 32<<10 {
		return fmt.Errorf("CSS 超过 32KB 上限")
	}
	if !themeCSSRe.MatchString(css) {
		return fmt.Errorf("CSS 含不允许的字符（反斜杠、<、& 等被拦截）")
	}
	if !strings.Contains(css, "{") || !strings.Contains(css, "}") {
		return fmt.Errorf("CSS 缺少规则块（至少包含一组 选择器 { 声明 }）")
	}
	return nil
}

// resolveTheme 解析当前生效主题：返回 (data-theme 属性值, 自定义 CSS, 元信息)。
// 未知 id 回落默认 mono。旧版本的 default_theme="dark" 视为 mono（暗色现为
// 访客端 data-mode 明暗切换，不再作为独立主题）。
func resolveTheme(s siteSettings) (string, string, themeMeta) {
	id := s.DefaultTheme
	if id == "dark" {
		id = "mono"
	}
	if id == "" || id == "mono" {
		return "mono", "", builtinThemes()[0]
	}
	if isBuiltinTheme(id) {
		for _, t := range builtinThemes() {
			if t.ID == id {
				return id, "", t
			}
		}
	}
	if t := getCustomTheme(id); t != nil {
		return t.ID, t.CSS, t.themeMeta
	}
	// id 失效（主题被删但 default_theme 未清）→ 回落默认
	return "mono", "", builtinThemes()[0]
}

// themeIDValid 供安装/设置表单校验。
func themeIDValid(id string) bool { return themeIDRe.MatchString(id) }

// themePackage 主题压缩包解析结果。
type themePackage struct {
	Name        string // theme.json 的 name
	Description string // theme.json 的 description（可选）
	CSS         string // theme.css 正文
}

// zipSizeLimit / entryLimit 防压缩炸弹：包 ≤2MB，解压后 CSS ≤32KB。
const (
	themeZipMaxBytes = 2 << 20
	themeEntryMax    = 64
)

// parseThemeZip 从 zip 字节解析主题包：
//   - 必须含 theme.css（UTF-8）
//   - 可选 theme.json：{"name": "...", "description": "..."}
//   - 其余文件忽略；防路径穿越（..、绝对路径）与压缩炸弹
//
// 名称回退顺序：theme.json.name → zip 文件名去扩展名。
func parseThemeZip(data []byte, zipFilename string) (*themePackage, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("压缩包为空")
	}
	if len(data) > themeZipMaxBytes {
		return nil, fmt.Errorf("压缩包超过 2MB 上限")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("不是有效的 zip 文件")
	}
	if len(zr.File) > themeEntryMax {
		return nil, fmt.Errorf("压缩包含文件过多（> %d）", themeEntryMax)
	}

	var (
		css     string
		name    string
		desc    string
		hasMeta bool
	)
	for _, f := range zr.File {
		clean := filepath.ToSlash(f.Name)
		base := path.Base(clean)
		if base == "." || base == "" {
			continue
		}
		// 只认顶层或单层目录下的 theme.css / theme.json，拒绝路径穿越
		dir := path.Dir(clean)
		if dir != "." && strings.Contains(dir, "..") {
			return nil, fmt.Errorf("压缩包内路径不合法: %s", f.Name)
		}
		switch strings.ToLower(base) {
		case "theme.css":
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("读取 theme.css 失败: %v", err)
			}
			raw, err := io.ReadAll(io.LimitReader(rc, 33<<10))
			rc.Close()
			if err != nil {
				return nil, fmt.Errorf("theme.css 读取失败: %v", err)
			}
			css = string(raw)
		case "theme.json":
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("读取 theme.json 失败: %v", err)
			}
			raw, err := io.ReadAll(io.LimitReader(rc, 8<<10))
			rc.Close()
			if err != nil {
				return nil, fmt.Errorf("theme.json 读取失败: %v", err)
			}
			var meta struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			}
			if err := json.Unmarshal(raw, &meta); err != nil {
				return nil, fmt.Errorf("theme.json 解析失败: %v", err)
			}
			name, desc, hasMeta = strings.TrimSpace(meta.Name), strings.TrimSpace(meta.Description), true
		}
	}

	if css == "" {
		return nil, fmt.Errorf("压缩包中缺少 theme.css")
	}
	if !hasMeta || name == "" {
		// 回退：zip 文件名去扩展名
		base := strings.TrimSuffix(filepath.Base(zipFilename), filepath.Ext(zipFilename))
		base = strings.TrimSpace(base)
		if !themeNameRe.MatchString(base) {
			return nil, fmt.Errorf("theme.json 缺少 name，且压缩包文件名不能用作主题名")
		}
		name = base
	}
	if !themeNameRe.MatchString(name) {
		return nil, fmt.Errorf("主题名称不合法（1-40 字符：中文/字母/数字/空格/·_-）")
	}
	if utf8.RuneCountInString(desc) > 200 {
		return nil, fmt.Errorf("description 超过 200 字")
	}
	if err := validateThemeCSS(css); err != nil {
		return nil, err
	}
	return &themePackage{Name: name, Description: desc, CSS: css}, nil
}

// importThemeZip 校验入库并返回生成的主题 id。
func importThemeZip(data []byte, zipFilename string) (string, error) {
	pkg, err := parseThemeZip(data, zipFilename)
	if err != nil {
		return "", err
	}
	id := "custom_" + randomHex(4)
	for getCustomTheme(id) != nil {
		id = "custom_" + randomHex(4)
	}
	list := loadCustomThemes()
	list = append(list, customTheme{
		themeMeta: themeMeta{ID: id, Name: pkg.Name, Description: pkg.Description},
		CSS:       pkg.CSS,
	})
	saveCustomThemes(list)
	return id, nil
}
