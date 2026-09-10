# Share - 资源分享中心

<p align="center">
  <img src="assets/site-icon.png" width="120" alt="Share Logo">
</p>

<p align="center">
  <strong>轻量级资源分享平台 · 单二进制部署 · SQLite/PostgreSQL</strong>
</p>

<p align="center">
  <a href="#功能特性">功能</a> ·
  <a href="#快速开始">快速开始</a> ·
  <a href="#api-文档">API</a> ·
  <a href="#配置说明">配置</a> ·
  <a href="#部署指南">部署</a>
</p>

---

## 功能特性

- **单二进制部署** - 无外部依赖，模板/JS/CSS 全部 embed
- **双数据库** - SQLite（零配置）或 PostgreSQL
- **存储网关** - 内置调度，支持多节点分布式存储
- **HMAC 签名** - 下载链接带时效签名
- **主题系统** - 支持自定义主题包

## 快速开始

```bash
git clone https://github.com/lingrana/share.git
cd share
go build -o share-go .
./share-go
```

访问 `http://localhost:61201`，首次访问 `/install` 选择数据库，`/set-password` 设置管理员。

## API 文档

### 公开 API

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/health` | 健康检查 |
| GET | `/content/{file_id}` | 获取文件内容 |
| HEAD | `/content/{file_id}` | 获取文件元信息 |
| GET | `/index` | 首页 |
| GET | `/resource/{id}` | 资源详情 |
| GET | `/share/{token}` | 分享页 |

### 文件下载 API

```
GET /content/{file_id}
```

**参数：**

| 参数 | 位置 | 类型 | 说明 |
|------|------|------|------|
| `file_id` | path | string | 文件 ID（如 `m1.mp3`） |
| `download` | query | bool | 强制下载（默认 inline 预览） |

**请求头：**

| Header | 说明 |
|--------|------|
| `Range` | 分段下载（如 `bytes=0-1023`） |
| `If-None-Match` | 条件请求，返回 304 |

**响应：**

| 状态码 | 说明 |
|--------|------|
| 200 | 成功 |
| 206 | 分段内容 |
| 304 | 未修改 |
| 404 | 文件不存在 |
| 416 | Range 无法满足 |

**响应头：**

| Header | 说明 |
|--------|------|
| `Content-Type` | MIME 类型 |
| `Content-Length` | 文件大小 |
| `ETag` | 文件标识 |
| `Accept-Ranges` | 支持分段（bytes） |
| `Content-Disposition` | 下载/预览 |

**示例：**

```bash
# 预览
curl http://localhost:61201/content/m1.mp3

# 下载
curl -O "http://localhost:61201/content/m1.mp3?download=1"

# 分段下载
curl -r 0-1023 -o part1.mp3 http://localhost:61201/content/m1.mp3
```

### 管理 API（需登录）

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/admin` | 管理后台操作 |
| GET | `/admin?folder={id}` | 文件夹管理 |
| GET | `/admin?resource={id}` | 资源编辑 |
| GET | `/admin?gw_edit={id}` | 节点编辑 |

**管理操作（POST form）：**

| action | 参数 | 说明 |
|--------|------|------|
| `gw_save_node` | `node_id`, `node_name`, `node_base_url`, `node_token` | 保存节点 |
| `gw_toggle_node` | `node_id` | 启用/禁用节点 |
| `gw_delete_node` | `node_id` | 删除节点 |
| `gw_test_node` | `node_id` | 测试节点连通 |
| `gw_refresh_nodes` | - | 刷新所有节点容量 |
| `gw_delete_file` | `file_key` | 删除网关文件 |

### 认证

管理 API 需要登录会话，通过 Cookie 传递：

```
Cookie: sssid={session_id}
```

## 文件结构

```
share/
├── main.go              # 入口 + 路由
├── config.go            # 配置加载
├── db.go                # 数据库（SQLite/PostgreSQL）
├── session.go           # 会话管理
├── auth.go              # 认证鉴权
├── handlers.go          # 请求分发
├── admin.go             # 后台页面
├── resources.go         # 资源管理
├── shares.go            # 分享管理
├── settings.go          # 站点设置
├── stats.go             # 统计
├── middleware.go         # 中间件（CORS/限流/RequestID）
├── helpers.go           # 工具函数
├── sanitize.go          # 输入清理
├── covers.go            # 封面处理
├── templates.go         # 模板渲染
├── theme.go             # 主题系统
├── install.go           # 安装向导
├── actions.go           # POST 操作
├── upload_util.go       # 上传工具
├── util.go              # 通用工具
│
├── gateway_store.go     # 存储网关核心
├── gateway_content.go   # 文件内容服务
├── gateway_proxy.go     # 节点代理
├── gateway_admin.go     # 网关管理
│
├── assets/              # 静态资源
│   ├── style.css
│   ├── app.js
│   └── site-icon.png
│
├── templates/           # 页面模板
│   ├── layout.tmpl
│   ├── home_public.tmpl
│   ├── home_admin.tmpl
│   ├── preview.tmpl
│   ├── share.tmpl
│   ├── login.tmpl
│   ├── setpassword.tmpl
│   ├── install.tmpl
│   ├── stats.tmpl
│   ├── 404.tmpl
│   └── partials/
│
├── docs/                # 文档
│   └── THEMES.md
│
├── Dockerfile           # Docker 构建
├── .github/workflows/   # CI/CD
│   └── docker.yml
├── go.mod
├── go.sum
└── README.md
```

## 配置说明

| 环境变量 | 说明 | 默认值 |
|---------|------|--------|
| `SHARE_DATA` | 数据目录 | `./data` |
| `PORT` | 监听端口 | `61201` |
| `CORS_ORIGINS` | CORS 白名单 | 空 |
| `TZ` | 时区 | `UTC` |

### config.json

```json
{
  "site_name": "资源分享中心",
  "listen": ":61201",
  "route_secret": "自动生成",
  "db": {
    "driver": "sqlite",
    "sqlite_path": "share.sqlite"
  }
}
```

## Docker 部署

```bash
docker run -d \
  --name share \
  -p 61201:61201 \
  -v share-data:/app/data \
  ghcr.io/lingrana/share:latest
```

### Docker Compose

```yaml
services:
  share:
    image: ghcr.io/lingrana/share:latest
    ports:
      - "61201:61201"
    volumes:
      - share-data:/app/data
    restart: unless-stopped

volumes:
  share-data:
```

## 安全特性

- 登录限流（5次/15分钟）
- CSRF 防护
- XSS 防护（输出转义）
- 参数化查询（防 SQL 注入）
- 内存限流器（10 req/s/IP）
- 安全 Cookie（HttpOnly + SameSite + Secure）

## License

MIT
