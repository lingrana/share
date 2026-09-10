package main

import "testing"

// 模板集合解析冒烟：initTemplates 内部 template.Must 会在语法错误时 panic。
// home_admin 等新增选项卡改动后此测试兜底语法回归。
func TestTemplatesParse(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("模板解析失败: %v", r)
		}
	}()
	initTemplates()
	for _, page := range tmplPages {
		if tmplSets[page] == nil {
			t.Errorf("模板集合 %s 未注册", page)
		}
	}
}
