# 前台主题系统（管理员手册 + 主题作者规范）

> 本文档是前台主题的**完整说明**：机制、变量契约、明暗变体、布局修改、zip 包规范、从零写一个主题的步骤与常见问题。
> 代码位置：`go/theme.go`（解析/校验/zip）、`go/actions.go`（`update_theme` / `import_theme` / `delete_theme`）、`assets/style.css`（设计变量与组件样式）。

---

## 1. 机制总览

```
管理员后台（**🎨 前台主题** 独立选项卡，位于 ⚙️ 站点配置 左侧）
   │  ① 选择主题（内置"纸墨"或已导入的自定义主题）
   │  ② 上传主题压缩包（.zip）导入新主题
   ▼
settings 表：default_theme（当前生效主题 id）+ custom_themes（导入的主题包）
   │
   ▼  每次渲染页面时
layout.tmpl：<html data-theme="<主题 id>" data-mode="light|dark">
             + （自定义主题时）注入 <style id="custom-theme">
   │
   ▼
assets/style.css 按 data-theme × data-mode 应用变量与样式 → 整站换肤/换布局
```

**两个独立维度：**

| 维度 | 由谁决定 | 载体 | 说明 |
| --- | --- | --- | --- |
| `data-theme`（主题） | **管理员** | settings 表 `default_theme` | 配色 + 布局 + 组件样式的设计包，可含布局修改 |
| `data-mode`（明暗） | **访客自己** | 浏览器 localStorage `mono_mode` | 默认跟随系统 `prefers-color-scheme`；导航栏「🌙 深色 / ☀️ 浅色」按钮随时切换，明暗切换**不会改动管理员选的主题** |

**四条铁律：**

1. **只有管理员能切换主题与导入主题包**（`/admin` 后台，需登录 + CSRF）。
2. **每个主题自带 light / dark 两个变体**——明暗是主题的内部属性，不是独立主题。
3. **主题可以修改布局与组件样式**：导入的 CSS 除了变量，还可以覆盖组件选择器（网格列数、卡片形状、字号、圆角等）。
4. **后台控制台固定使用默认主题 `mono`**（管理员仍可用明暗按钮切其暗色变体）；主题不作用于后台。

**哪些页面跟随主题：** `/index`、`/folder/*`、`/resource/*`、`/share/*`、`/login`、`/set-password`、`/install`、404 页。
**哪些页面不跟随：** `/admin`（控制台所有选项卡）。
**生效速度：** 切换/删除主题立即清空分享页缓存（`clearAllPublicPageCaches()`），无需重启。

---

## 2. 主题作者规范（写一个主题需要知道的全部）

### 2.1 页面骨架（你的 CSS 作用对象）

前台页面由 `templates/layout.tmpl` 包裹，所有主题可作用的 DOM 都在这棵树里：

```
html[data-theme="<id>"][data-mode="light|dark"]
└── body
    ├── header.mono-nav > .nav-inner
    │     ├── a.nav-brand（站点名 + 图标 .nav-brand-img + 徽章 .nav-badge）
    │     └── .nav-actions（主题明暗按钮 .theme-toggle-btn + 其他按钮 .mono-btn）
    ├── div.page-container          ← 版心容器（各页可加 max-width 内联样式）
    │   ├── .flash-bar               ← 操作提示条（[ SUCCESS ] / [ ERROR ]）
    │   ├── .hero-editorial          ← 首页搜索区（.mono-search-box / .mono-search-input / .mono-search-btn）
    │   ├── .mono-social-banner      ← 社交链接横幅（.mono-announcement-tag / .mono-social-banner-links）
    │   ├── .mono-breadcrumb         ← 面包屑（a + span.sep）
    │   ├── .section-header > .section-title  ← 区块标题
    │   ├── .folder-grid > .folder-card        ← 目录网格/卡片（.folder-card-meta / .folder-card-name）
    │   │     └── .folder-scroller（>4 个目录时）> .folder-nav-prev / .folder-nav-next
    │   ├── .resource-grid[data-cols] > .resource-card  ← 资源网格/卡片
    │   │     ├── .resource-cover-box > .resource-cover-img + .resource-type-tag
    │   │     └── .resource-body > .resource-title / .resource-desc / .resource-tags-wrap > .mono-tag
    │   │           └── .resource-footer-meta
    │   ├── .mono-pagination > .mono-page-btn(.active)  ← 分页
    │   ├── table.link-download-table           ← 资源/分享页下载渠道表格
    │   ├── .preview-headline / .preview-meta-row  ← 详情页大标题与元信息
    │   └── .modal-overlay(.is-open) > .modal-sheet ← 模态框（.modal-title / .modal-close-x / 公告 #announcementModal）
    └── footer.mono-footer > .footer-inner（.footer-clock 页脚时钟）

表单控件：.mono-input / .mono-select / .mono-textarea / .mono-form-group / .mono-form-label
按钮：.mono-btn（+.mono-btn-sm/.mono-btn-lg/.mono-btn-filled 反色实心）
标签：.mono-tag；后台表格：.admin-table
```

### 2.2 设计变量（换肤层，必读）

整套 UI 由 9 个 CSS 设计变量驱动（定义在 `assets/style.css` 的 `:root`，全站引用 167 处），**没有其他硬编码颜色**：

| 变量 | 建议 | 用途 | 引用次数 |
| --- | --- | --- | --- |
| `--bg` | **必须** | 页面/卡片/输入框背景色 | 41 |
| `--fg` | **必须** | 正文、标题、线框强调色（与 bg 反差构成整个风格） | 37 |
| `--border` | 强烈建议 | 全站 2px 线框（按钮、表格、模态框） | 28 |
| `--muted-fg` | 建议 | 次要文字（面包屑、提示语、日期） | 10 |
| `--border-light` | 建议 | 次级分隔线、输入框内框 | 8 |
| `--muted-bg` | 建议 | 次级背景（斑马纹、代码块） | 5 |
| `--font-serif` | 可选 | 大标题衬线字体栈（默认 Playfair Display / 思源宋体系） | 9 |
| `--font-body` | 可选 | 正文字体栈 | 4 |
| `--font-mono` | 可选 | 等宽标签/按钮/表单字体栈（默认 JetBrains Mono 系） | 25 |

**明暗变体的分工：**

- `:root { … }`（或 `:root[data-theme="<id>"]`）— 浅色变体。
- `:root[data-mode="dark"] { … }` — **全局暗色兜底**（mono 的暗色即定义在此）。你的主题若不写暗色块，访客切到深色时自动落到这里（只换 6 个颜色变量，效果基本成立）。
- `:root[data-theme="<id>"][data-mode="dark"] { … }` — **主题专属暗色变体**，特异性最高，想给暗色定制完整体验就写这个块。

**可选装饰：** `body::before` 是全站「印刷纸张纹理」层（4px 横向重复渐变）。浅底主题建议按底色重定义 rgba 色，深底主题可 `background-image: none` 关闭。

### 2.3 布局与组件覆盖（改布局层）

主题 CSS 允许包含任意白名单内的选择器与声明，布局、字号、间距、圆角都可以随主题变化：

```css
/* 例：双列网格 + 圆角卡片 + 隐藏描述 */
:root[data-theme="custom_xxxx"] .resource-grid { grid-template-columns: repeat(2, 1fr); }
:root[data-theme="custom_xxxx"] .resource-card { border-radius: 12px; }
:root[data-theme="custom_xxxx"] .resource-desc { display: none; }
```

建议所有选择器都带 `:root[data-theme="<id>"]` 前缀（更精确、防污染）；不带前缀也可以——自定义 CSS 只注入前台页面的 `<style id="custom-theme">`，天然不作用于后台。

### 2.4 CSS 白名单（安全红线，导入时强制）

| 规则 | 说明 |
| --- | --- |
| 字符白名单 | 仅允许字母/数字/空白与 `#.,:;()'"{}*/[]-_>@!+~^|=`；**反斜杠、`<`、`&` 被拦截** |
| 拦截效果 | `</style>` 逃逸、HTML/事件注入（`onerror=` 等）从根上不可行 |
| 长度 ≤ 32KB | 每个页面的 `<style>` 注入体积可控 |
| 至少一个规则块 | 必须包含 `{ … }`，防止导入无效文本 |
| 其他约束 | 包 ≤2MB；theme.json ≤8KB；包内条目 ≤64（防压缩炸弹） |

作为主题作者请勿在主题里引用外部资源（`@import`、外链 `url(...)`）——主题应当自包含。

### 2.5 生命周期与生效

1. 导入 zip → 解析校验 → 存入 `settings` 表 `custom_themes`（JSON：`id`/`name`/`description`/`css`），id 自动生成为 `custom_<8位hex>`。
2. 后台「当前生效主题」选中它 → `default_theme` 更新 → 清空分享页缓存 → 前台下一次渲染即新主题。
3. 删除主题：从 `custom_themes` 移除；若它正在使用，`default_theme` 自动清空回落 `mono`。内置主题不可删除。
4. 旧版本遗留的 `default_theme="dark"` 自动映射为 `mono`（暗色现为访客端明暗变体）。

---

## 3. 主题压缩包规范（.zip）

### 3.1 包结构

```
我的主题.zip
├── theme.css     ← 必须。主题 CSS（变量 + 可选布局/组件覆盖），≤32KB，UTF-8
└── theme.json    ← 可选。{"name": "主题名", "description": "一句话说明"}
```

- 两个文件可以放在包根目录，或放进**单层子目录**（如 `my-theme/theme.css`），都会被识别；其余文件忽略。
- 包 ≤2MB，条目 ≤64，防路径穿越（含 `..` 的条目名整包拒绝）。

### 3.2 theme.json 字段

| 字段 | 必填 | 规则 |
| --- | --- | --- |
| `name` | 否* | 1-40 字符，中文/字母/数字/空格/`·_-` |
| `description` | 否 | ≤200 字，后台主题列表展示 |

\* 缺 `theme.json` 或缺 `name` 时，回退用 zip 文件名（去扩展名）做主题名；文件名不合法才报错。

### 3.3 导入步骤（管理员）

1. 后台切到 **🎨 前台主题** 选项卡（站点配置左侧）→ **📥 导入自定义主题（压缩包）**，选择 .zip 上传。
2. 导入成功后，主题出现在「🗂️ 已导入的自定义主题」表格（含真实 id 与体积）。
3. 在上方「当前生效主题」下拉框选中 → **应用主题**。
4. 打开前台任意页面查看效果；用导航栏按钮切换明暗，验证暗色变体。

---

## 4. 实战：从零写一个「深海蓝」主题

**第 1 步**：建目录 `deep-sea/`，写 `deep-sea/theme.css`：

```css
/* ============ 浅色变体 ============ */
:root[data-theme="custom_xxxx"] {
    --bg: #F0F6FC;            /* 淡海雾底 */
    --fg: #0B2545;            /* 深海墨蓝 */
    --border: #0B2545;
    --muted-bg: #E1ECF7;
    --muted-fg: #4A6789;
    --border-light: #C3D8EC;
}
:root[data-theme="custom_xxxx"] body::before { background-image: none; }

/* ============ 暗色变体 ============ */
:root[data-theme="custom_xxxx"][data-mode="dark"] {
    --bg: #050D18;
    --fg: #A9CCE3;
    --muted-bg: #0C1B2E;
    --muted-fg: #56779B;
    --border: #A9CCE3;
    --border-light: #12263D;
}

/* ============ 布局覆盖：圆角 + 双列 ============ */
:root[data-theme="custom_xxxx"] .resource-card,
:root[data-theme="custom_xxxx"] .folder-card { border-radius: 10px; }
:root[data-theme="custom_xxxx"] .resource-grid { grid-template-columns: repeat(2, 1fr); }
```

**第 2 步**：写 `deep-sea/theme.json`：

```json
{ "name": "深海蓝", "description": "海雾蓝底 + 深海墨蓝，含圆角与双列布局" }
```

**第 3 步**：把 `theme.css`、`theme.json` 打成 zip（放包根目录或单层子目录均可）。到后台上传导入。

**第 4 步**：启用并排错——

- 导入被拒「CSS 含不允许的字符」→ 检查是否有 `<`、`&`、反斜杠；颜色值笔误（如 `#GGHHII`）不会被拒但会静默失效，用浏览器开发者工具查。
- 启用后前台没变化 → 确认看的是前台页面（后台永远 mono）；强制刷新一次。
- 暗色下文字看不清 → 暗色块缺变量落到了全局兜底的黑白组合，补全即可。

**第 5 步**（可选）：把写好的主题包交给其他站长直接导入——包本身自包含，无需改站点代码。

---

## 5. 内置主题

当前内置仅一个：**`mono` 纸墨（默认）** —— 极简黑白画报：浅色为纸白墨黑（`:root` 默认值），暗色为纯黑高对比（`:root[data-mode="dark"]` 兜底）。后台「当前生效主题」选回它即可随时恢复默认。

> 历史版本曾内置 `dark`（独立暗色主题）、`parchment`、`sepia`、`cyber`，已移除；旧设置里的 `dark` id 会自动映射为 mono，其余自定义主题不受影响。

---

## 6. FAQ

**Q：访客能自己换主题吗？**
不能换主题（管理员统一指定），但可以切明暗——导航栏按钮只改访客自己浏览器的 `data-mode`（localStorage 记忆，默认跟随系统），不会影响其他访客。

**Q：明暗切换的默认行为？**
首次访问跟随系统 `prefers-color-scheme`；访客点过按钮后以 localStorage 为准。

**Q：为什么我的主题在后台不生效？**
后台固定 mono（铁律 4），请打开前台页面查看。

**Q：切换主题后分享页还是旧配色？**
正常情况下切换会立即清缓存；若直接改数据库绕过了程序，缓存要等 60 秒自然过期。

**Q：主题里能用图片/字体文件吗？**
压缩包里除 theme.css/theme.json 外的文件会被忽略——主题不能带资源文件。如需自定义字体，在 CSS 里用系统字体栈（如 `'Noto Serif SC', serif`）。

**Q：如何回滚到默认？**
「当前生效主题」选 **纸墨（默认）** → 应用主题；或删除正在使用的自定义主题，自动回落。

**Q：升级版本会不会弄坏我的主题？**
变量契约（2.2 节 9 个变量）是稳定接口；组件类名（2.1 节）大版本间尽量不动，但不承诺——布局覆盖层建议只做「锦上添花」的调整，核心可读性不要依赖它。

---

## 7. 开发者附注

- 解析入口：`theme.go → resolveTheme()`；zip 解析：`parseThemeZip()`；校验：`validateThemeCSS()`；明暗切换：`assets/app.js`（`data-mode` + localStorage `mono_mode`）。
- 测试：`theme_test.go`（校验规则、zip 解析、解析回落、残留块检查）；端到端覆盖见审查记录。
- 新增内置主题：`assets/style.css` 追加变量块 + `theme.go → builtinThemes()` 注册（当前策略：内置只保留 mono，新主题一律走 zip 导入）。
