# 资源分享中心（Go 版）

`go/` 是 `php/` 主站的 Go 移植版：单二进制 + 可选 SQLite / PostgreSQL，PHP 版原样保留，两版可并存。

## 为什么是 Go

- **占用低**：SQLite 模式常驻内存约 15~30MB（PHP-FPM 池 + MySQL 一般 300MB+）；不需要 MySQL 进程。
- **部署简单**：单个 linux/amd64 静态二进制，无运行时依赖，systemd 一行启动；模板与 CSS/JS 全部 embed 进二进制。
- **数据库可选**：首次访问 `/install` 选择 **SQLite**（零配置单文件，推荐）或 **PostgreSQL**（填主机/端口/用户/密码/库名），选择后自动建库建表并写入 `data/config.json`。
- **行为对齐**：URL、表单、后台操作、网关 API 契约（`docs/API.md`）与 PHP 版一致，`style.css` / `app.js` 原样复用。
- **前台主题**：管理员在后台选择主题（内置仅「纸墨」）或上传 zip 主题包导入（可含布局/组件覆盖），前台统一跟随；访客可切换各主题的明暗变体；后台控制台固定默认主题。详见 `docs/THEMES.md`。

## 与 PHP 版的差异

| 项 | PHP 版 | Go 版 |
| --- | --- | --- |
| 数据库 | MySQL（安装向导写入 config.local.php） | SQLite 或 PostgreSQL（首次访问 `/install` 选择） |
| 安装流程 | `/install.html` MySQL 向导 + 安装锁 | 首次启动 → `/install` 选库建表 → `/set-password` 初始化管理员；安装后 `/install` 一律 404 |
| 部署目标 | Apache/Nginx + PHP 虚拟主机 | 任意能跑二进制的 VPS（2G 内存绰绰有余），前面可配 Nginx/Caddy 反代 |
| 静态资源版本号 | `?v=filemtime` | `?v=内容哈希`（启动时计算） |
| 页面缓存 | 系统临时目录文件缓存 | 进程内内存缓存（重启即失效，行为一致） |
| 会话 | PHP session 文件 | 进程内会话（重启后需重新登录） |
| 封面处理 | GD 缩放 → SVG 包裹 JPEG | 标准库 image 缩放 → 同样 SVG 包裹 JPEG |
| 广告外跳 | `/promo/签名-数据.html` | 同 HMAC-SHA256 签名算法，路径去掉 `.html` |
| URL | `/index.html` `/admin.html` … | `/index` `/admin` …（**不带 `.html` 即可访问**；带 `.html` 的旧链接仍然兼容） |
| MySQL 全文搜索 | MATCH AGAINST（可选） | 统一 LIKE 搜索 |

数据迁移（MySQL → SQLite/PG）不在本版范围；资源量小可直接后台重录，量大时后续可加导入脚本。

## 构建与本地运行

```bash
cd go
go build -o share-go.exe .        # Windows
./share-go.exe -addr :8080        # 数据目录默认为二进制旁的 data/
```

- 配置文件：`data/config.json`（首次安装时生成，`route_secret` 自动生成；数据库选择保存在 `db` 键）。
- 数据库：SQLite 为 `data/share.sqlite`，PostgreSQL 由配置指向外部实例；封面：`data/assets/covers/`；遗留媒体：`data/assets/media/`。
- 安装流程：首次访问任意页面 → `/install` 选 SQLite / PostgreSQL → `/set-password` 设管理员 → `/admin` 进后台。

## 部署到 Linux VPS

交叉编译（在本机执行）：

```bash
cd go
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-s -w" -o share-go-linux .
```

上传 `share-go-linux` 到 VPS，然后：

```bash
chmod +x share-go-linux
mkdir -p /opt/share && mv share-go-linux /opt/share/
SHARE_DATA=/opt/share/data /opt/share/share-go-linux -addr :8080
```

systemd 服务（`/etc/systemd/system/share.service`）：

```ini
[Unit]
Description=Share site (Go)
After=network.target

[Service]
WorkingDirectory=/opt/share
Environment=SHARE_DATA=/opt/share/data
ExecStart=/opt/share/share-go-linux -addr 127.0.0.1:8080
Restart=always
RestartSec=3
# 2G VPS 上的资源上限（Go 版常态 ~30MB，仅作兜底）
MemoryMax=256M

[Install]
WantedBy=multi-user.target
```

```bash
systemctl enable --now share
```

Nginx 反代（可选，配 TLS / 与网关同机共存）：

```nginx
server {
    listen 80;
    server_name your.domain;
    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        # 访客 IP 统计依赖 CF-Connecting-IP；不经 CF 时 Go 直接读 TCP 对端
    }
}
```

## 配置项（data/config.json）

| 键 | 说明 | 默认 |
| --- | --- | --- |
| `site_name` | 站点名（页面标题兜底） | 资源分享中心 |
| `route_secret` | 广告外跳 HMAC 密钥；为空自动生成 | 自动 |
| `listen` | 监听地址，`-addr` / `PORT` 可覆盖 | `:8080` |
| `db.driver` | `sqlite` / `postgres`（安装页选择；空 = 未安装） | 空 |
| `db.sqlite_path` | SQLite 文件路径（相对 data/ 或绝对） | `share.sqlite` |
| `db.host` / `db.port` / `db.user` / `db.password` / `db.dbname` / `db.sslmode` | PostgreSQL 连接信息 | — |
| `login_max_attempts` / `login_lock_minutes` | 登录限流 | 5 / 15 |
| `http_insecure` | 跳过 TLS 校验（仅缺 CA 包的主机临时用） | false |

### PostgreSQL 注意事项

- 需提前建库（`CREATE DATABASE share;`），程序自动建表与索引。
- 连接信息明文写入 `data/config.json`（权限 0600），请勿提交版本库。
- 方言差异由 `db.go` 的 `adaptSQL` 统一处理（`?`→`$n`、`INSERT OR IGNORE`→`ON CONFLICT DO NOTHING`、`MAX(0,x)`→`GREATEST`、`date(time)`→`time::date`、`json_extract`→`::jsonb->>`、`RETURNING id` 取自增 ID），业务层无感知；改写规则有单测覆盖（`go test ./...`）。
- 面向公网的 PG 实例建议 `sslmode=require` 或更高。

## 内置存储网关（share-storage 移植）

share-storage 的 PHP 网关已完整移植进本站：**一个二进制既是主站又是存储网关**，不再需要单独部署 PHP 网关；已部署的存储节点（`node.php`，免费虚拟主机即可）原样接入。

- **后台**：`/admin` →「🗄️ 存储网关」选项卡——节点增删改/测试连通/一键刷新容量、文件索引管理、运行设置（本地存储开关、容量上限、分片大小、单文件上限）。
- **外链**：资源「完整内容」经调度写入节点或本地磁盘（`data/gateway/files`），公开链接统一为本站 `/content/{file_id}`（支持 Range 播放/断点、ETag 304、`?download=1` 强制下载），节点真实地址不对外暴露，下载请求带 HMAC 时效签名。
- **调度**：本地存储开启时本地优先（剩余 ≥ 保底阈值），否则按节点自报剩余空间降序；小文件单请求直传（PUT 裸字节），失败自动降级分片会话转发（对应 node 的 `/upload-sessions` 三段式）。
- **反爬破解**：勾选「反爬」的节点自动求解 InfinityFree/VimHost 类主机的 slowAES JS 质询并持久化通行 cookie。
- **清理**：删除资源时按 `m<id>.` / `c<id>.` 前缀清理节点与本地文件（幂等，失败不阻塞主站删除）。
- **旧版迁移**：原「站点配置 → 存储网关」的 `gateway_url`/`gateway_token` 远程网关模式已移除，`config.json` 的 `remote_storage` 键不再读取；历史上保存的绝对外链仍照常渲染。

## 源码结构

| 文件 | 对应 PHP |
| --- | --- |
| `main.go` | `index.php` + `.htaccess` 路由 |
| `db.go` | `dev.php` 的 SQLite 层 + schema；PG 方言改写与 PG schema |
| `config.go` | `config.php` + `config.local.php` |
| `install.go` + `templates/install.tmpl` | `install_mysql.php`（Go 版为选 SQLite/PG） |
| `session.go` | `bootstrap.php` + `page_cache.php` |
| `auth.go` | `includes/auth.php`（bcrypt 与 PHP `password_hash` 双向兼容） |
| `resources.go` / `shares.go` | `includes/resources.php` / `shares.php` |
| `settings.go` / `sanitize.go` | `includes/settings.php` |
| `stats.go` | `includes/stats.php` |
| `gateway_store.go` / `gateway_proxy.go` / `gateway_content.go` / `gateway_admin.go` | share-storage `gateway/`（store.php / router.php / index.php / admin.php） |
| `actions.go` | `includes/actions.php` |
| `handlers.go` / `admin.go` | `index.php` 各 GET 分支 |
| `covers.go` | `helpers.php` 封面函数簇 |
| `templates/` | `views/*.php` |
| `assets/` | `php/assets/`（原样拷贝） |
