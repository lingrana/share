package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 内置存储网关 — 公开内容服务，对应 share-storage gateway 的 /content/<key>：
// 本地文件按 Range 读，节点文件流式代理（HMAC 时效签名，Range 透传）。

// ==================== HMAC 下载签名 ====================

// gwHMACHex 小写 hex 的 HMAC-SHA256（与 PHP hash_hmac('sha256', ...) 一致）。
func gwHMACHex(token, data string) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte(data))
	return hex.EncodeToString(mac.Sum(nil))
}

// gwSignDownloadURL 生成节点下载 URL：?e=<unix>&s=HMAC-SHA256(key|e, token)。
// 与节点端 requireFileSignature 完全一致（窗口 [now-60, now+86400]）。
func gwSignDownloadURL(node *gwNode, key string) string {
	e := time.Now().Add(gwSignatureTTL).Unix()
	sig := gwHMACHex(node.Token, key+"|"+strconv.FormatInt(e, 10))
	return gwNodeURL(node, "/files/"+url.PathEscape(key)) + "?e=" + strconv.FormatInt(e, 10) + "&s=" + sig
}

// ==================== 反爬挑战（slowAES / aes.js 质询） ====================

var (
	gwChallengeNumsRe   = regexp.MustCompile(`toNumbers\("([0-9a-fA-F]{32,64})"\)`)
	gwChallengeCookieRe = regexp.MustCompile(`document\.cookie="([^"]+?)="\s*\+`)
	gwResponseRangeHdRe = regexp.MustCompile(`(?i)bytes=(\d*)-(\d*)`)
)

// gwIsChallenge 节点响应是否为反爬挑战页（aes.js，InfinityFree/VimHost 类主机）。
func gwIsChallenge(body string) bool {
	return strings.Contains(body, "toNumbers") &&
		(strings.Contains(body, "aes.js") || strings.Contains(body, "slowAES"))
}

// gwSolveChallenge 解析挑战页并计算通行 cookie：等价页面内
// slowAES.decrypt(c, 2, a, b)，即 AES-CBC 解密（a=密钥 b=IV c=密文），
// 成功返回 "名字=值"，失败返回 ""。
func gwSolveChallenge(body string) string {
	if !gwIsChallenge(body) {
		return ""
	}
	nums := gwChallengeNumsRe.FindAllStringSubmatch(body, -1)
	if len(nums) < 3 {
		return ""
	}
	cm := gwChallengeCookieRe.FindStringSubmatch(body)
	if cm == nil {
		return ""
	}
	key, err1 := hex.DecodeString(nums[0][1])
	iv, err2 := hex.DecodeString(nums[1][1])
	ct, err3 := hex.DecodeString(nums[2][1])
	if err1 != nil || err2 != nil || err3 != nil {
		return ""
	}
	var block cipher.Block
	block, err := aes.NewCipher(key)
	if err != nil {
		return ""
	}
	if len(ct) == 0 || len(ct)%block.BlockSize() != 0 || len(iv) != block.BlockSize() {
		return ""
	}
	plain := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ct) // 与 OPENSSL_ZERO_PADDING 一致：不去填充
	return cm[1] + "=" + hex.EncodeToString(plain)
}

// ==================== Range 解析 ====================

type gwRange struct {
	Start int64
	End   int64
}

// gwParseClientRange 解析客户端单段 Range（对应 storeParseClientRange）。
// 返回 invalid=true 表示显式非法（应回 416）；r=nil 且 invalid=false 表示无 Range。
func gwParseClientRange(size int64, header string) (r *gwRange, invalid bool) {
	if header == "" {
		return nil, false
	}
	m := gwResponseRangeHdRe.FindStringSubmatch(header)
	if m == nil {
		return nil, false
	}
	if m[1] == "" && m[2] == "" {
		return nil, true
	}
	var start, end int64
	if m[1] == "" {
		suffix, err := strconv.ParseInt(m[2], 10, 64)
		if err != nil || suffix <= 0 {
			return nil, true
		}
		start = size - suffix
		if start < 0 {
			start = 0
		}
		end = size - 1
	} else {
		start, _ = strconv.ParseInt(m[1], 10, 64)
		if m[2] != "" {
			end, _ = strconv.ParseInt(m[2], 10, 64)
			if end > size-1 {
				end = size - 1
			}
		} else {
			end = size - 1
		}
	}
	if start > end || start >= size {
		return nil, true
	}
	return &gwRange{Start: start, End: end}, false
}

// ==================== 响应头 ====================

func gwSendBaseHeaders(w http.ResponseWriter, row *gwFileRow, download bool) {
	h := w.Header()
	h.Set("Content-Type", gwEffectiveMime(row))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Accept-Ranges", "bytes")
	h.Set("ETag", gwEtag(row))
	h.Set("Cache-Control", "public, max-age=3600")
	h.Set("Content-Disposition", gwDisposition(download, row))
}

func gwProblemJSON(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	// 使用 json.Marshal 正确转义字符串，防止注入
	type problemResp struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Status int    `json:"status"`
		Detail string `json:"detail"`
	}
	resp := problemResp{Type: "about:blank", Title: title, Status: status, Detail: detail}
	b, _ := json.Marshal(resp)
	_, _ = w.Write(b)
}

// ==================== 本地输出 ====================

// gwServeLocal 网关本地文件输出（Range / 304 / 416 交给 ServeContent）。
func gwServeLocal(w http.ResponseWriter, r *http.Request, row *gwFileRow, download bool) {
	path := gwLocalPath(row.Key)
	fp, err := os.Open(path)
	if err != nil {
		gwProblemJSON(w, http.StatusNotFound, "Not Found", "The requested resource does not exist.")
		return
	}
	defer fp.Close()
	gwSendBaseHeaders(w, row, download)
	http.ServeContent(w, r, row.Key, time.Now(), fp)
}

// ==================== 节点代理输出 ====================

// gwProxyStream 从节点流式代理输出。节点 URL 带 HMAC 时效签名，Range 透传，
// 状态码 / Content-Range / Content-Length 按节点响应转发。
// 延迟发头：收到节点首个数据块才确定状态码并发送响应头——若节点"发了状态行就断流"
// （免费主机防护/网络问题），可干净地返回 502，而不是 200 + 空文件体。
func gwProxyStream(w http.ResponseWriter, r *http.Request, row *gwFileRow, node *gwNode, download bool) {
	if node.AntiBot {
		gwEnsureCookie(node, false)
	}
	etag := gwEtag(row)
	if inm := strings.TrimSpace(r.Header.Get("If-None-Match")); inm != "" && inm == etag {
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.WriteHeader(http.StatusNotModified)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, gwSignDownloadURL(node, row.Key), nil)
	if err != nil {
		gwProblemJSON(w, http.StatusBadGateway, "Bad Gateway", "Storage node is unreachable.")
		return
	}
	if cr := r.Header.Get("Range"); cr != "" {
		req.Header.Set("Range", cr)
	}
	gwNodeSetHeaders(req.Header, node)
	resp, err := gwHTTP.Do(req)
	if err != nil {
		gwProblemJSON(w, http.StatusBadGateway, "Bad Gateway", "Storage node is unreachable.")
		return
	}
	defer resp.Body.Close()

	status := resp.StatusCode
	if status == http.StatusNotModified || status == http.StatusRequestedRangeNotSatisfiable {
		gwForwardBodylessStatus(w, status, row, resp.Header)
		return
	}

	// 只转发与 body 强相关的头；其余（Set-Cookie/Server 等）不外泄
	h := w.Header()
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		h.Set("Content-Range", cr)
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		h.Set("Content-Length", cl)
	}

	buf := make([]byte, 64<<10)
	var first []byte
	for len(first) == 0 {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			first = buf[:n]
			break
		}
		if rerr != nil {
			// 连节点的响应体一个字节都没收到（连接失败，或状态行之后立即断流）
			gwProblemJSON(w, http.StatusBadGateway, "Bad Gateway", "Storage node is unreachable.")
			return
		}
	}

	gwSendBaseHeaders(w, row, download)
	w.WriteHeader(status)
	if _, err := w.Write(first); err != nil {
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.Flush()
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			_ = rc.Flush()
		}
		if rerr != nil {
			return
		}
	}
}

// gwIsBodylessStatus 节点合法地不带 body 的响应（304 / 416）。
func gwIsBodylessStatus(status int) bool {
	return status == http.StatusNotModified || status == http.StatusRequestedRangeNotSatisfiable
}

// gwForwardBodylessStatus 转发 304 / 416 状态。
// 代理是延迟发头的，这类响应一个字节 body 都没有，不能当成"节点断流"降级成 502。
func gwForwardBodylessStatus(w http.ResponseWriter, status int, row *gwFileRow, src http.Header) bool {
	if !gwIsBodylessStatus(status) {
		return false
	}
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	if status == http.StatusNotModified {
		h.Set("ETag", gwEtag(row))
		h.Set("Cache-Control", "public, max-age=3600")
		w.WriteHeader(status)
		return true
	}
	h.Set("Accept-Ranges", "bytes")
	if cr := src.Get("Content-Range"); cr != "" {
		h.Set("Content-Range", cr)
	} else {
		h.Set("Content-Range", "bytes */"+strconv.FormatInt(row.SizeBytes, 10))
	}
	w.WriteHeader(status)
	return true
}
