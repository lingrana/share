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
  <a href="#docker-部署">Docker</a> ·
  <a href="#配置说明">配置</a> ·
  <a href="#部署指南">部署</a>
</p>

---

## 功能特性

- **单二进制部署** - 无外部依赖，模板/JS/CSS 全部 embed 进二进制
- **双数据库支持** - SQLite（零配置）或 PostgreSQL
- **存储网关** - 内置存储调度，支持多节点分布式存储
- **HMAC 签名** - 下载链接带时效签名，防止盗链
- **反爬破解** - 自动求解 JS 质询，兼容免费主机
- **主题系统** - 支持自定义主题包导入
- **管理后台** - 资源/文件夹/分享/统计/存储网关一站式管理

## 快速开始

### 本地运行

```bash
# 克隆仓库
git clone https://github.com/lingrana/share.git
cd share

# 编译运行
go build -o share-go .
./share-go
```

访问 `http://localhost:61201`，首次使用请访问 `/install` 选择数据库，然后 `/set-password` 设置管理员密码。

### Docker 部署

```bash
# 运行容器
docker run -d \
  --name share \
  -p 61201:61201 \
  -v share-data:/app/data \
  ghcr.io/lingrana/share:latest
```

### Docker Compose

```yaml
version: '3.8'
services:
  share:
    image: ghcr.io/lingrana/share:latest
    ports:
      - "61201:61201"
    volumes:
      - share-data:/app/data
    environment:
      - TZ=Asia/Shanghai
    restart: unless-stopped

volumes:
  share-data:
```

## 配置说明

| 环境变量 | 说明 | 默认值 |
|---------|------|--------|
| `SHARE_DATA` | 数据目录路径 | `./data` |
| `PORT` | 监听端口 | `61201` |
| `CORS_ORIGINS` | CORS 允许来源（逗号分隔） | 空（不允许） |
| `TZ` | 时区 | `UTC` |

### 配置文件

首次运行后会在数据目录生成 `config.json`：

```json
{
  "site_name": "资源分享中心",
  "listen": ":61201",
  "db": {
    "driver": "sqlite",
    "sqlite_path": "share.sqlite"
  }
}
```

## 部署指南

### Linux systemd

```bash
# 交叉编译
GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build -ldflags="-s -w" -o share-go .

# 上传到服务器
scp share-go user@server:/opt/share/

# 创建服务文件 /etc/systemd/system/share.service
```

```ini
[Unit]
Description=Share Resource Center
After=network.target

[Service]
WorkingDirectory=/opt/share
Environment=SHARE_DATA=/opt/share/data
ExecStart=/opt/share/share-go
Restart=always
RestartSec=3
MemoryMax=256M

[Install]
WantedBy=multi-user.target
```

```bash
systemctl enable --now share
```

### Nginx 反向代理

```nginx
server {
    listen 80;
    server_name your.domain;

    location / {
        proxy_pass http://127.0.0.1:61201;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

## 存储网关

内置存储网关支持将文件分发到多个存储节点：

1. **添加节点** - 后台 → 存储网关 → 添加节点（填入节点 URL 和 Token）
2. **节点部署** - 部署 [share-storage](https://github.com/lingrana/share-storage) 的 `node.php`
3. **自动调度** - 按节点剩余空间自动选择最优节点

### 节点特性

- 分片上传支持（大文件自动分片）
- HMAC 时效签名（防盗链）
- 反爬自动破解（InfinityFree/VimHost）
- 节点健康检查

## 安全特性

- **登录限流** - 5次失败锁定15分钟
- **CSRF 防护** - 表单提交带 Token 验证
- **XSS 防护** - 所有输出 HTML 转义
- **SQL 注入防护** - 参数化查询
- **限流器** - 内存令牌桶限流（10 req/s/IP）
- **安全 Cookie** - HttpOnly + SameSite + Secure

## 开发

```bash
# 运行测试
go test ./...

# 构建
go build -o share-go .

# 交叉编译
GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build -o share-go-linux .
```

## License

MIT
