package normalize

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const maxIndexedArrayIndex = 10000 // ponytail: bounds sparse-slice allocation; long chats stay under this

var ErrIndexedPath = errors.New("indexed attribute path rejected")

func RebuildIndexed(attrs map[string]any, prefix string) (any, bool) {
	value, ok, err := rebuildIndexed(attrs, prefix)
	return value, ok && err == nil
}

func rebuildIndexed(attrs map[string]any, prefix string) (any, bool, error) {
	start := prefix + "."
	keys := make([]string, 0)
	for key := range attrs {
		if strings.HasPrefix(key, start) && len(key) > len(start) {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil, false, nil
	}
	sort.Strings(keys)
	var result any
	for _, key := range keys {
		var err error
		result, err = setPath(result, strings.Split(strings.TrimPrefix(key, start), "."), attrs[key])
		if err != nil {
			return nil, false, fmt.Errorf("%w: %s: %v", ErrIndexedPath, key, err)
		}
	}
	return result, true, nil
}

func setPath(node any, path []string, value any) (any, error) {
	if len(path) == 0 {
		return value, nil
	}
	if numeric(path[0]) {
		index, err := strconv.ParseUint(path[0], 10, 64)
		if err != nil || index > maxIndexedArrayIndex {
			return nil, fmt.Errorf("array index must be at most %d", maxIndexedArrayIndex)
		}
		items, _ := node.([]any)
		if len(items) <= int(index) {
			items = append(items, make([]any, int(index)-len(items)+1)...)
		}
		items[index], err = setPath(items[index], path[1:], value)
		return items, err
	}
	object, _ := node.(map[string]any)
	if object == nil {
		object = make(map[string]any)
	}
	child, err := setPath(object[path[0]], path[1:], value)
	if err != nil {
		return nil, err
	}
	object[path[0]] = child
	return object, nil
}

func numeric(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
