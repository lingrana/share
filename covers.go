package main

import (
	"bytes"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// 封面处理，对应 helpers.php 的封面函数簇 + settings.php 的公告净化。
// Go 版统一用标准库 image 解码，统一生成「SVG 包裹 JPEG」的缩略图，
// 与 PHP 无 GD 时的 rawImageToSvgCover 行为一致（尺寸 480x320 内等比缩放）。

var coverExts = []string{"svg", "jpg", "jpeg", "png", "gif", "webp"}

func coversDirectory() string {
	dir := filepath.Join(dataDir, "assets", "covers")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

func coverFileBaseName(resourceID int64) string {
	if resourceID < 0 {
		resourceID = 0
	}
	return itoaSafe(int(resourceID))
}

func removeCoverFilesForBase(base string) {
	if base == "" {
		return
	}
	dir := coversDirectory()
	for _, ext := range coverExts {
		p := filepath.Join(dir, base+"."+ext)
		if fileExists(p) {
			_ = os.Remove(p)
		}
	}
}

func storageWriteCover(filename, contents string) string {
	filename = filepath.Base(strings.ReplaceAll(filename, "\\", "/"))
	if filename == "" || strings.Contains(filename, "..") || !coverNameRe.MatchString(filename) {
		return ""
	}
	if err := os.WriteFile(filepath.Join(coversDirectory(), filename), []byte(contents), 0o644); err != nil {
		return ""
	}
	return "/assets/covers/" + urlPathEscape(filename)
}

var coverNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// generateCoverSvg 生成类型色块 + 标题的占位封面。
func generateCoverSvg(resourceID int64, title, resourceType string) string {
	iconMap := map[string]string{
		"video": "🎬", "audio": "🎵", "image": "🖼️",
		"document": "📄", "other": "📦",
	}
	colorMap := map[string]string{
		"video": "#e74c3c", "audio": "#9b59b6", "image": "#3498db",
		"document": "#2ecc71", "other": "#95a5a6",
	}
	if resourceType == "software" {
		resourceType = "other"
	}
	icon, ok := iconMap[resourceType]
	if !ok {
		icon = "📦"
	}
	bg, ok := colorMap[resourceType]
	if !ok {
		bg = "#95a5a6"
	}
	title = strings.TrimSpace(title)
	shortTitle := truncateRunes(title, 20)
	if runeLen(title) > 20 {
		shortTitle += "…"
	}
	if shortTitle == "" {
		shortTitle = icon
	}

	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="400" height="260" viewBox="0 0 400 260">
  <rect width="400" height="260" fill="` + bg + `" rx="8"/>
  <text x="200" y="120" text-anchor="middle" font-size="64" fill="rgba(255,255,255,0.25)">` + icon + `</text>
  <text x="200" y="175" text-anchor="middle" font-size="22" font-weight="bold" fill="#fff" font-family="sans-serif">` + escapeXMLText(shortTitle) + `</text>
  <text x="200" y="210" text-anchor="middle" font-size="13" fill="rgba(255,255,255,0.6)" font-family="sans-serif">` + escapeXMLText(resourceType) + `</text>
</svg>`

	base := coverFileBaseName(resourceID)
	removeCoverFilesForBase(base)
	return storageWriteCover(base+".svg", svg)
}

func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

func truncateRunes(s string, max int) string {
	if runeLen(s) <= max {
		return s
	}
	out := []rune(s)
	return string(out[:max])
}

// rawImageToSvgCover 把任意图片字节包进 SVG（无缩放），对应 PHP 无 GD 回退路径。
func rawImageToSvgCover(resourceID int64, imageData []byte, mimeType string, w, h int) string {
	base := coverFileBaseName(resourceID)
	removeCoverFilesForBase(base)
	b64 := base64Std(imageData)
	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="` + itoaSafe(w) + `" height="` + itoaSafe(h) + `" viewBox="0 0 ` + itoaSafe(w) + ` ` + itoaSafe(h) + `">
  <image href="data:` + mimeType + `;base64,` + b64 + `" width="` + itoaSafe(w) + `" height="` + itoaSafe(h) + `"/>
</svg>`
	return storageWriteCover(base+".svg", svg)
}

// imageBytesToSvgCover 解码图片并等比缩放到 480x320 内，重新编码为 JPEG 包进 SVG。
func imageBytesToSvgCover(resourceID int64, data []byte) string {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return ""
	}
	srcW := src.Bounds().Dx()
	srcH := src.Bounds().Dy()
	if srcW < 1 || srcH < 1 {
		return ""
	}
	const maxWidth, maxHeight = 480, 320
	scale := 1.0
	if float64(srcW)/maxWidth > scale {
		scale = float64(maxWidth) / float64(srcW)
	}
	if float64(srcH)/maxHeight > scale {
		scale = float64(maxHeight) / float64(srcH)
	}
	if scale > 1 {
		scale = 1
	}
	dstW := maxInt(1, int(float64(srcW)*scale+0.5))
	dstH := maxInt(1, int(float64(srcH)*scale+0.5))

	// 标准库没有缩放器；近邻采样在缩略图场景足够
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	for y := 0; y < dstH; y++ {
		sy := y * srcH / dstH
		for x := 0; x < dstW; x++ {
			sx := x * srcW / dstW
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 82}); err != nil {
		return ""
	}
	return rawImageToSvgCover(resourceID, buf.Bytes(), "image/jpeg", dstW, dstH)
}

// saveUploadedCover 处理上传的封面文件，返回 /assets/covers/... 相对地址或空串。
func saveUploadedCover(resourceID int64, fileHeader *multipartFileInfo) string {
	if fileHeader == nil {
		return ""
	}
	data := fileHeader.data
	if len(data) == 0 {
		return ""
	}
	// 校验是可解码图片
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 {
		return ""
	}
	if s := imageBytesToSvgCover(resourceID, data); s != "" {
		return s
	}
	// 解码失败（如 webp 标准库不支持）时按原始字节包 SVG
	return rawImageToSvgCover(resourceID, data, fileHeader.contentType, cfg.Width, cfg.Height)
}

// resolveCoverForResource 对应 helpers.php 同名函数的优先级链。
func resolveCoverForResource(resourceID int64, title, resourceType, coverInput, shareInput string, uploadedFile *multipartFileInfo) string {
	title = strings.TrimSpace(title)
	coverInput = strings.TrimSpace(coverInput)
	shareInput = strings.TrimSpace(shareInput)
	if resourceType == "software" {
		resourceType = "other"
	}

	// 1. 上传的封面文件最高优先级
	if uploadedFile != nil {
		if stored := saveUploadedCover(resourceID, uploadedFile); stored != "" {
			return stored
		}
	}

	// 2. 前端占位符 [本地图片]：直接生成标准封面
	if strings.HasPrefix(coverInput, "[本地图片]") {
		if title != "" {
			return generateCoverSvg(resourceID, title, resourceType)
		}
		return ""
	}

	// 3. 自定义封面 URL / 现有封面
	if coverInput != "" && !strings.HasPrefix(coverInput, "/assets/covers/") && !gwOwnsUrl(coverInput) {
		if strings.HasPrefix(coverInput, "http://") || strings.HasPrefix(coverInput, "https://") {
			if stored := storeCoverImage(resourceID, coverInput, title); stored != "" {
				return stored
			}
		}
	} else if coverInput != "" {
		return coverInput
	}

	// 4. 从网盘链接抓取封面
	if shareInput != "" {
		if fetched := fetchCoverFromUrl(resourceID, shareInput, title); fetched != "" {
			return fetched
		}
	}

	// 5. 默认生成类型 SVG
	if title != "" {
		return generateCoverSvg(resourceID, title, resourceType)
	}
	return ""
}

func fetchCoverFromUrl(resourceID int64, rawURL, title string) string {
	parsed := parseCloudShareInput(rawURL, "other")
	link := parsed["url"]
	if link != "" {
		rawURL = link
		if parsed["code"] != "" && !regexp.MustCompile(`(?i)[?&]pwd=`).MatchString(rawURL) {
			sep := "?"
			if strings.Contains(rawURL, "?") {
				sep = "&"
			}
			rawURL += sep + "pwd=" + urlQueryEscape(parsed["code"])
		}
	}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return ""
	}

	if m := reBaiduShare.FindStringSubmatch(rawURL); m != nil {
		surl := m[1]
		pwd := parsed["code"]
		if pwd == "" {
			if pm := regexp.MustCompile(`(?i)[?&]pwd=([^&]+)`).FindStringSubmatch(rawURL); pm != nil {
				pwd = decodeURIComponent(pm[1])
			}
		}
		if baidu := fetchBaiduPanCover(surl, pwd); baidu != "" {
			if stored := storeCoverImage(resourceID, baidu, title); stored != "" {
				return stored
			}
			return baidu
		}
	}

	remote := fetchGenericOgImage(rawURL)
	if remote == "" {
		return ""
	}
	if stored := storeCoverImage(resourceID, remote, title); stored != "" {
		return stored
	}
	return remote
}

func storeCoverImage(resourceID int64, imageURL, title string) string {
	if !strings.HasPrefix(imageURL, "http://") && !strings.HasPrefix(imageURL, "https://") {
		return ""
	}
	bin := httpGetBytes(imageURL, 8*time.Second, map[string]string{
		"Accept": "image/avif,image/webp,image/apng,image/*,*/*;q=0.8",
	}, 8<<20)
	if len(bin) < 32 {
		return ""
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(bin))
	if err != nil {
		return ""
	}
	if s := imageBytesToSvgCover(resourceID, bin); s != "" {
		return s
	}
	return rawImageToSvgCover(resourceID, bin, "image/jpeg", cfg.Width, cfg.Height)
}

func fetchGenericOgImage(pageURL string) string {
	h := httpGet(pageURL, 8*time.Second, map[string]string{
		"Accept": "text/html,application/xhtml+xml",
	})
	if h == "" {
		return ""
	}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)<meta[^>]+property=["']og:image["'][^>]+content=["']([^"']+)["']`),
		regexp.MustCompile(`(?i)<meta[^>]+content=["']([^"']+)["'][^>]+property=["']og:image["']`),
		regexp.MustCompile(`(?i)<meta[^>]+name=["']twitter:image["'][^>]+content=["']([^"']+)["']`),
		regexp.MustCompile(`(?i)<meta[^>]+content=["']([^"']+)["'][^>]+name=["']twitter:image["']`),
		regexp.MustCompile(`(?i)<link[^>]+rel=["']image_src["'][^>]+href=["']([^"']+)["']`),
	}
	for _, p := range patterns {
		if m := p.FindStringSubmatch(h); m != nil {
			imgURL := strings.TrimSpace(m[1])
			if strings.HasPrefix(imgURL, "http://") || strings.HasPrefix(imgURL, "https://") {
				return imgURL
			}
		}
	}
	m := regexp.MustCompile(`(?i)<img[^>]+src=["']([^"']+\.(?:jpg|jpeg|png|webp))`).FindStringSubmatch(h)
	if m != nil {
		imgURL := strings.TrimSpace(m[1])
		if strings.HasPrefix(imgURL, "//") {
			imgURL = "https:" + imgURL
		}
		if strings.HasPrefix(imgURL, "/") {
			if u, err := urlParse(pageURL); err == nil {
				imgURL = u.Scheme + "://" + u.Host + imgURL
			}
		}
		if strings.HasPrefix(imgURL, "http://") || strings.HasPrefix(imgURL, "https://") {
			return imgURL
		}
	}
	return ""
}

func fetchBaiduPanCover(surl, pwd string) string {
	apiURL := "https://pan.baidu.com/share/wxlist?clienttype=0&app_id=250528&web=1&shorturl=" + urlQueryEscape(surl)
	if pwd != "" {
		apiURL += "&pwd=" + urlQueryEscape(pwd)
	}
	jsonBody := httpGet(apiURL, 8*time.Second, map[string]string{
		"Referer": "https://pan.baidu.com/s/" + surl,
		"Accept":  "application/json,text/html",
	})
	if jsonBody == "" {
		return ""
	}
	var data struct {
		Data struct {
			List []struct {
				Thumbs map[string]string `json:"thumbs"`
				Icon   string            `json:"icon"`
			} `json:"list"`
		} `json:"data"`
		List []struct {
			Thumbs map[string]string `json:"thumbs"`
			Icon   string            `json:"icon"`
		} `json:"list"`
	}
	if err := jsonUnmarshal(jsonBody, &data); err != nil {
		return ""
	}
	list := data.Data.List
	if len(list) == 0 {
		list = data.List
	}
	for _, file := range list {
		for _, key := range []string{"url3", "url2", "url1", "url"} {
			if t := file.Thumbs[key]; strings.HasPrefix(t, "http://") || strings.HasPrefix(t, "https://") {
				return t
			}
		}
		if strings.Contains(file.Icon, "http") {
			return file.Icon
		}
	}

	pageURL := "https://pan.baidu.com/s/" + surl
	if pwd != "" {
		pageURL += "?pwd=" + pwd
	}
	return fetchGenericOgImage(pageURL)
}
