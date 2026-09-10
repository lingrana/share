package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 资源分享中心 — Go 版。
//
// 与 PHP 版的行为对应关系见 go/README.md。单二进制 + SQLite（WAL），
// 静态资源与模板 embed 进二进制；封面/遗留媒体写 dataDir/assets。

func main() {
	dataFlag := flag.String("data", "", "数据目录（config.json / share.sqlite / assets），默认为二进制同目录的 data/")
	addrFlag := flag.String("addr", "", "监听地址，优先级低于 config.json 的 listen")
	flag.Parse()

	// 数据目录定位：-data > 环境变量 SHARE_DATA > 可执行文件旁 data/ > 工作目录 data/
	dataDir = *dataFlag
	if dataDir == "" {
		if env := os.Getenv("SHARE_DATA"); env != "" {
			dataDir = env
		} else if exe, err := os.Executable(); err == nil {
			dataDir = filepath.Join(filepath.Dir(exe), "data")
		} else {
			dataDir = "data"
		}
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatalf("无法创建数据目录 %s: %v", dataDir, err)
	}

	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	appConfig = cfg
	httpClientInsecure = cfg.HTTPInsecure

	// 数据库延迟到请求期初始化：未安装时全站引导到 /install 安装向导；
	// 已安装（config.json 有 db.driver）则直接连接。
	if appConfig.DB.Configured() {
		if err := bootstrapDB(); err != nil {
			log.Fatalf("初始化数据库失败: %v", err)
		}
	}

	initTemplates()
	go sessionGC()

	mux := http.NewServeMux()
	// 健康检查：无密钥、无内部拓扑（审查规范 2.11）
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/assets/", serveEmbeddedAsset)
	mux.HandleFunc("/thumb", handleThumb)
	mux.HandleFunc("/thumb/", handleThumb)
	// 内置存储网关：公开内容外链（/content/<key>），节点地址不对外暴露
	mux.HandleFunc("/content/", handleContent)
	mux.HandleFunc("/content", handleContent)
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		data, err := embeddedFS.ReadFile("assets/site-icon.png")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=2592000")
		_, _ = w.Write(data)
	})
	// PHP 版 MySQL 安装向导的旧路径：Go 版统一走 /install，旧路径一律 404
	mux.HandleFunc("/install_mysql.html", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	mux.HandleFunc("/", mainHandler)

	// 应用中间件链：限流 → CORS → X-Request-Id
	handler := rateLimitMiddleware(corsMiddleware(requestIdMiddleware(mux)))

	addr := cfg.Listen
	if *addrFlag != "" {
		addr = *addrFlag
	} else if env := os.Getenv("PORT"); env != "" {
		addr = ":" + strings.TrimPrefix(env, ":")
	}

	log.Printf("资源分享中心 (Go) 启动: http://0.0.0.0%s  数据目录: %s", addr, dataDir)
	if !appConfig.DB.Configured() {
		log.Printf("尚未安装数据库，首次请访问 /install 选择 SQLite 或 PostgreSQL。")
	} else if !passwordIsInitialized() {
		log.Printf("尚未设置管理员，首次请访问 /set-password 初始化。")
	}
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("HTTP 服务退出: %v", err)
	}
}
