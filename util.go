package main

import "sort"

// sortedKeys 返回 map 键的稳定排序，保证 INSERT/UPDATE 语句可复现。
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func str(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

func intVal(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	if n, ok := m[key].(int); ok {
		return n
	}
	return 0
}
