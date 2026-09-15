package normalize

import (
	"sort"
	"strconv"
	"strings"
)

func RebuildIndexed(attrs map[string]any, prefix string) (any, bool) {
	start := prefix + "."
	keys := make([]string, 0)
	for key := range attrs {
		if strings.HasPrefix(key, start) && len(key) > len(start) {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil, false
	}
	sort.Strings(keys)
	var result any
	for _, key := range keys {
		result = setPath(result, strings.Split(strings.TrimPrefix(key, start), "."), attrs[key])
	}
	return result, true
}

func setPath(node any, path []string, value any) any {
	if len(path) == 0 {
		return value
	}
	if index, err := strconv.Atoi(path[0]); err == nil && index >= 0 {
		items, _ := node.([]any)
		if len(items) <= index {
			items = append(items, make([]any, index-len(items)+1)...)
		}
		items[index] = setPath(items[index], path[1:], value)
		return items
	}
	object, _ := node.(map[string]any)
	if object == nil {
		object = make(map[string]any)
	}
	object[path[0]] = setPath(object[path[0]], path[1:], value)
	return object
}
