package main

import (
	"net/http"
	"strings"
)

// 首次安装向导：选择 SQLite 或 PostgreSQL，建库建表并写入 config.json。
// 未安装时全站强制跳到 /install；安装完成后 /install 一律 404（对应 PHP 版安装锁）。

func handleInstallPage(w http.ResponseWriter, r *http.Request) {
	if dbConfigured() {
		http.NotFound(w, r)
		return
	}
	s := startSession(w, r)
	flash, flashType := pullFlash(s)
	renderPage(w, "install", map[string]any{
		"Flash":     flash,
		"FlashType": flashType,
		"CSRFToken": s.CSRFToken,
		"Title":     "安装 - " + appConfig.SiteName,
	})
}

// bootstrapDB 按当前配置连接数据库、建表并初始化根目录。
func bootstrapDB() error {
	if err := openDBWithConfig(appConfig.DB); err != nil {
		return err
	}
	ensureRootFolder()
	return nil
}

func actionInstallDB(w http.ResponseWriter, r *http.Request) {
	if dbConfigured() {
		http.NotFound(w, r)
		return
	}
	s := startSession(w, r)

	driver := r.FormValue("driver")
	switch driver {
	case "sqlite":
		appConfig.DB = DBConfig{Driver: "sqlite", SQLitePath: "share.sqlite"}
		if err := bootstrapDB(); err != nil {
			appConfig.DB = DBConfig{}
			setFlash(s, "SQLite 初始化失败: "+err.Error(), "error")
			redirect(w, "/install")
			return
		}
	case "postgres":
		host := formValue(r, "host")
		port := atoiOr(r.FormValue("port"), 5432)
		user := formValue(r, "user")
		password := r.FormValue("password")
		dbname := formValue(r, "dbname")
		sslmode := formValue(r, "sslmode")
		if sslmode == "" {
			sslmode = "disable"
		}
		if host == "" || user == "" || dbname == "" {
			setFlash(s, "主机、用户名和数据库名不能为空。", "error")
			redirect(w, "/install")
			return
		}
		if port <= 0 || port > 65535 {
			setFlash(s, "端口无效（1-65535）。", "error")
			redirect(w, "/install")
			return
		}
		appConfig.DB = DBConfig{
			Driver: "postgres", Host: host, Port: port,
			User: user, Password: password, DBName: dbname, SSLMode: sslmode,
		}
		if err := bootstrapDB(); err != nil {
			appConfig.DB = DBConfig{} // 失败回滚，安装页保持可用
			setFlash(s, "PostgreSQL 连接失败: "+err.Error(), "error")
			redirect(w, "/install")
			return
		}
	default:
		setFlash(s, "请选择数据库类型。", "error")
		redirect(w, "/install")
		return
	}

	if err := saveConfig(appConfig); err != nil {
		setFlash(s, "数据库已连通，但 config.json 写入失败: "+err.Error(), "error")
		redirect(w, "/install")
		return
	}
	setFlash(s, "数据库安装完成（"+strings.ToUpper(driver)+"），请设置管理员账号。", "success")
	redirect(w, "/set-password")
}
