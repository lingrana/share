package main

import (
	"bytes"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// 公告富文本净化，对应 settings.php sanitizeAnnouncementHtml。
// 允许结构化标签白名单；script/iframe 等整块删除；未知标签保留内容；
// 属性仅放行 a[title]/img[src,alt,title]/通用 style,class；a 加 target/rel。

var allowedAnnouncementTags = map[string]bool{
	"div": true, "span": true, "table": true, "thead": true, "tbody": true,
	"tr": true, "td": true, "th": true, "p": true, "br": true, "strong": true,
	"b": true, "em": true, "i": true, "u": true, "s": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"ul": true, "ol": true, "li": true, "a": true, "blockquote": true,
	"code": true, "pre": true, "hr": true, "img": true,
}

var blockedAnnouncementTags = map[string]bool{
	"script": true, "iframe": true, "object": true, "embed": true, "form": true,
	"input": true, "button": true, "textarea": true, "select": true, "option": true,
	"svg": true, "math": true, "meta": true, "link": true,
}

// safeURLRe 匹配 http(s) 绝对链接、站内绝对路径与页内锚点。
var safeURLRe = regexp.MustCompile(`(?i)^(https?://|/|#)`)

func announcementAttrAllowed(tag, attr string) bool {
	switch tag {
	case "a":
		return attr == "href" || attr == "title"
	case "img":
		return attr == "src" || attr == "alt" || attr == "title"
	}
	return attr == "style" || attr == "class"
}

// sanitizeAnnouncementHtml 返回净化后的 HTML 片段；输入为空时返回空串。
func sanitizeAnnouncementHtml(input string) string {
	if strings.TrimSpace(input) == "" {
		return ""
	}
	// 解析为独立片段（不依赖外层文档上下文）
	root, err := html.ParseFragment(strings.NewReader(input), &html.Node{
		Type: html.ElementNode, Data: "div", DataAtom: atom.Div,
	})
	if err != nil {
		return html.EscapeString(input) // 解析失败退化为纯文本
	}

	// ParseFragment 返回的顶层节点是游离的（无 Parent），
	// 先挂到合成父节点上，removeChild/unwrap 才能生效
	wrapper := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	for _, top := range root {
		wrapper.AppendChild(top)
	}

	sanitizeNode(wrapper)

	var out bytes.Buffer
	for c := wrapper.FirstChild; c != nil; {
		next := c.NextSibling
		_ = html.Render(&out, c)
		wrapper.RemoveChild(c)
		c = next
	}
	return strings.TrimSpace(out.String())
}

func sanitizeNode(n *html.Node) {
	// 自底向上先处理子节点，保证删除时子树已净化
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		sanitizeNode(c)
		c = next
	}
	if n.Type != html.ElementNode {
		return
	}
	tag := strings.ToLower(n.Data)
	if tag == "" {
		return
	}

	if blockedAnnouncementTags[tag] {
		removeKeepNothing(n)
		return
	}

	// 过滤属性；URL 类属性仅放行 http(s)/相对路径/锚点，拦截 javascript: 与 data:
	var kept []html.Attribute
	for _, attr := range n.Attr {
		name := strings.ToLower(attr.Key)
		if !announcementAttrAllowed(tag, name) {
			continue
		}
		if (tag == "a" && name == "href") || (tag == "img" && name == "src") {
			val := strings.TrimSpace(attr.Val)
			if val == "" || !safeURLRe.MatchString(val) {
				continue
			}
		}
		kept = append(kept, html.Attribute{Key: name, Val: attr.Val})
	}
	n.Attr = kept

	if tag == "a" {
		href := attrValue(n, "href")
		if href != "" && !strings.HasPrefix(href, "#") {
			n.Attr = append(n.Attr,
				html.Attribute{Key: "target", Val: "_blank"},
				html.Attribute{Key: "rel", Val: "noopener nofollow"})
		}
	}

	// 白名单以外的标签：解除包裹，保留子内容
	if !allowedAnnouncementTags[tag] {
		unwrapNode(n)
	}
}

func attrValue(n *html.Node, key string) string {
	for _, attr := range n.Attr {
		if strings.ToLower(attr.Key) == key {
			return attr.Val
		}
	}
	return ""
}

// removeKeepNothing 整块删除节点（script/iframe 等）。
func removeKeepNothing(n *html.Node) {
	if n.Parent != nil {
		n.Parent.RemoveChild(n)
	}
}

// unwrapNode 用子节点替换节点自身（保留内容）。
func unwrapNode(n *html.Node) {
	parent := n.Parent
	if parent == nil {
		return
	}
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		parent.InsertBefore(c, n)
		c = next
	}
	parent.RemoveChild(n)
}
