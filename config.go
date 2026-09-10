package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// DBConfig 数据库配置：首次安装（/install）时选择 SQLite 或 PostgreSQL。
type DBConfig struct {
	Driver     string `json:"driver"` // "sqlite" | "postgres"；空串表示尚未安装
	SQLitePath string `json:"sqlite_path,omitempty"`
	Host       string `json:"host,omitempty"`
	Port       int    `json:"port,omitempty"`
	User       string `json:"user,omitempty"`
	Password   string `json:"password,omitempty"`
	DBName     string `json:"dbname,omitempty"`
	SSLMode    string `json:"sslmode,omitempty"`
}

func (c *DBConfig) Configured() bool {
	return c != nil && (c.Driver == "sqlite" || c.Driver == "postgres")
}

// Config 对应 PHP 版 config.php + config.local.php 的合并结果。
type Config struct {
	SiteName         string   `json:"site_name"`
	RouteSecret      string   `json:"route_secret"`
	Listen           string   `json:"listen"`
	LoginMaxAttempts int      `json:"login_max_attempts"`
	LoginLockMinutes int      `json:"login_lock_minutes"`
	HTTPInsecure     bool     `json:"http_insecure"`
	DB               DBConfig `json:"db"`
}

// dbConfigured 是否已完成数据库安装。
func dbConfigured() bool { return appConfig.DB.Configured() }

func defaultConfig() *Config {
	return &Config{
		SiteName:         "资源分享中心",
		RouteSecret:      "",
		Listen:           ":61201",
		LoginMaxAttempts: 5,
		LoginLockMinutes: 15,
	}
}

var appConfig *Config
var dataDir string

func configPath() string { return filepath.Join(dataDir, "config.json") }

// loadConfig 读取（或初始化）config.json。route_secret 为空时自动生成，
// 与 PHP 版一致：未配置密钥时拒绝一切广告外跳签名。
func loadConfig() (*Config, error) {
	cfg := defaultConfig()
	raw, err := os.ReadFile(configPath())
	if err == nil {
		if err := json.Unmarshal(raw, cfg); err != nil {
			return nil, errors.New("config.json 解析失败: " + err.Error())
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if cfg.RouteSecret == "" {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return nil, err
		}
		cfg.RouteSecret = hex.EncodeToString(buf)
		if err := saveConfig(cfg); err != nil {
			return nil, err
		}
	}
	if cfg.LoginMaxAttempts <= 0 {
		cfg.LoginMaxAttempts = 5
	}
	if cfg.LoginLockMinutes <= 0 {
		cfg.LoginLockMinutes = 15
	}
	if cfg.Listen == "" {
		cfg.Listen = ":61201"
	}
	return cfg, nil
}

func saveConfig(cfg *Config) error {
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(), append(raw, '\n'), 0o600)
}
