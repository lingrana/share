package main

import (
	"strings"
	"testing"
)

// 公告净化器：URL scheme 白名单与危险标签剥离（审查规范 C/输入校验）。
func TestSanitizeAnnouncementURLSchemes(t *testing.T) {
	cases := []struct{ in, mustNotContain, mustContain string }{
		{`<a href="javascript:alert(1)">x</a>`, "javascript", ""},
		{`<a href="data:text/html,x">x</a>`, "data:", ""},
		{`<a href="https://ok.example">ok</a>`, "", `href="https://ok.example"`},
		{`<a href="/relative">r</a>`, "", `href="/relative"`},
		{`<a href="#anchor">a</a>`, "", `href="#anchor"`},
		{`<img src="data:text/html;base64,x">`, "data:", ""},
		{`<img src="/assets/covers/1.svg">`, "", `src="/assets/covers/1.svg"`},
	}
	for _, c := range cases {
		out := sanitizeAnnouncementHtml(c.in)
		if c.mustNotContain != "" && strings.Contains(out, c.mustNotContain) {
			t.Errorf("sanitize(%q) = %q, 不应包含 %q", c.in, out, c.mustNotContain)
		}
		if c.mustContain != "" && !strings.Contains(out, c.mustContain) {
			t.Errorf("sanitize(%q) = %q, 应包含 %q", c.in, out, c.mustContain)
		}
	}
}

func TestSanitizeAnnouncementStripsDangerous(t *testing.T) {
	out := sanitizeAnnouncementHtml(`<p>hi</p><script>alert(1)</script><iframe src="x"></iframe><img src=x onerror=alert(2)>`)
	if strings.Contains(out, "<script") || strings.Contains(out, "<iframe") || strings.Contains(out, "onerror") {
		t.Fatalf("危险内容未被移除: %q", out)
	}
	if !strings.Contains(out, "<p>hi</p>") {
		t.Fatalf("合法内容被误删: %q", out)
	}
}
