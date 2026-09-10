package main

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// covers.go / helpers.go 需要的小工具集中放这里，避免循环依赖与重复实现。

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func base64Std(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

func urlPathEscape(s string) string {
	return url.PathEscape(s)
}

func urlQueryEscape(s string) string {
	return url.QueryEscape(s)
}

func urlParse(raw string) (*url.URL, error) {
	return url.Parse(raw)
}

func jsonUnmarshal(s string, v any) error {
	return json.Unmarshal([]byte(s), v)
}

func tlsConfigOrInsecure() *tls.Config {
	if httpClientInsecure {
		return &tls.Config{InsecureSkipVerify: true} //nolint:gosec // 与 PHP http_insecure 配置对齐
	}
	return nil
}

// httpGetBytes 与 httpGet 类似但返回字节，用于抓图片。
func httpGetBytes(rawURL string, timeout time.Duration, headers map[string]string, limit int64) []byte {
	client := &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{TLSClientConfig: tlsConfigOrInsecure()},
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil
	}
	return data
}

// multipartFileInfo 对应 PHP 的 $_FILES 单文件结构。
type multipartFileInfo struct {
	fieldName   string
	origName    string
	contentType string
	size        int64
	data        []byte // 上传文件统一读入内存（封面图片体量小；媒体大文件走流式，见 gateway.go）
	tmpPath     string // 大文件落盘路径，非空时用 Path 流式处理
}

// parseMultipartFormFiles 解析 multipart 表单，收集文件与普通字段。
// maxMemory 控制内存中保留的字节数，超过部分落到临时文件。
func parseMultipartFormFiles(r *http.Request, maxMemory int64) (map[string]string, map[string]*multipartFileInfo, error) {
	if err := r.ParseMultipartForm(maxMemory); err != nil {
		return nil, nil, err
	}
	fields := map[string]string{}
	for k, vs := range r.MultipartForm.Value {
		if len(vs) > 0 {
			fields[k] = vs[0]
		}
	}
	files := map[string]*multipartFileInfo{}
	for name, fhs := range r.MultipartForm.File {
		if len(fhs) == 0 {
			continue
		}
		fh := fhs[0]
		info := &multipartFileInfo{
			fieldName:   name,
			origName:    filepath.Base(fh.Filename),
			contentType: fh.Header.Get("Content-Type"),
			size:        fh.Size,
		}
		// 大于 32MB 的文件走临时路径流式处理，避免占内存
		if fh.Size > 32<<20 {
			f, err := fh.Open()
			if err == nil {
				tmp, err := os.CreateTemp("", "ssupload-*")
				if err == nil {
					_, err = io.Copy(tmp, f)
					tmp.Close()
					if err == nil {
						info.tmpPath = tmp.Name()
					}
				}
				f.Close()
			}
		} else {
			f, err := fh.Open()
			if err == nil {
				data, err := io.ReadAll(f)
				f.Close()
				if err == nil {
					info.data = data
				}
			}
		}
		files[name] = info
	}
	return fields, files, nil
}

func (f *multipartFileInfo) reader() (*bytes.Reader, *os.File) {
	if f.tmpPath != "" {
		if fp, err := os.Open(f.tmpPath); err == nil {
			return nil, fp
		}
		return nil, nil
	}
	if f.data != nil {
		return bytes.NewReader(f.data), nil
	}
	return nil, nil
}

func (f *multipartFileInfo) cleanup() {
	if f.tmpPath != "" {
		_ = os.Remove(f.tmpPath)
	}
}

// truncateUTF8Safe 按字节数截断且不破坏 UTF-8（网关 orig_name 限长用）。
func truncateUTF8Safe(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && !utf8.ValidString(s[:maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}

func sanitizeFilenameBase(s string) string {
	return strings.NewReplacer("/", "_", "\\", "_", "\x00", "_").Replace(s)
}

var _ = time.Now
