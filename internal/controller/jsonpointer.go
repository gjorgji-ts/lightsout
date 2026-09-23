/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// wildcardToken matches every element of an array or every key of an object at
// that position in a pointer.
const wildcardToken = "*"

// parsePointer splits an RFC 6901 JSON Pointer into its unescaped tokens.
// "~1" decodes to "/" and "~0" to "~", in that order, as the RFC requires.
func parsePointer(pointer string) ([]string, error) {
	if pointer == "" {
		return nil, fmt.Errorf("empty JSON Pointer")
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("JSON Pointer %q must start with %q", pointer, "/")
	}

	parts := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	tokens := make([]string, len(parts))
	for i, part := range parts {
		if part == "" {
			return nil, fmt.Errorf("JSON Pointer %q has an empty segment", pointer)
		}
		// "~1" first, then "~0": decoding in the other order would turn the
		// escaped sequence "~01" into "/" instead of the literal "~1".
		part = strings.ReplaceAll(part, "~1", "/")
		tokens[i] = strings.ReplaceAll(part, "~0", "~")
	}
	return tokens, nil
}

// formatPointer renders concrete tokens back into an RFC 6901 pointer string.
// Used as the key under which a field's original value is recorded, so restore
// targets exactly the element that was overwritten.
func formatPointer(tokens []string) string {
	var b strings.Builder
	for _, token := range tokens {
		b.WriteString("/")
		token = strings.ReplaceAll(token, "~", "~0")
		b.WriteString(strings.ReplaceAll(token, "/", "~1"))
	}
	return b.String()
}

// expandPointer resolves tokens against root and returns every concrete token
// path they match, with wildcards expanded against the object's actual shape.
//
// Segments missing from an existing object are still returned as long as no
// wildcard remains: the path is then fully determined, and setting it creates
// whatever is missing along the way. That is what lets a resource gain an
// annotation when it carries no annotations at all. A missing segment followed
// by a wildcard yields nothing, because there is no shape to expand against.
func expandPointer(root any, tokens []string) [][]string {
	var out [][]string
	walkPointer(root, tokens, nil, &out)
	return out
}

// containsWildcard reports whether any token still needs the document's shape to
// resolve.
func containsWildcard(tokens []string) bool {
	return slices.Contains(tokens, wildcardToken)
}

func walkPointer(node any, tokens []string, prefix []string, out *[][]string) {
	if len(tokens) == 0 {
		*out = append(*out, slices.Clone(prefix))
		return
	}

	token, rest := tokens[0], tokens[1:]

	switch typed := node.(type) {
	case map[string]any:
		if token == wildcardToken {
			// Sort so expansion order is stable across reconciles.
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			slices.Sort(keys)
			for _, key := range keys {
				walkPointer(typed[key], rest, append(prefix, key), out)
			}
			return
		}
		child, ok := typed[token]
		if !ok {
			// Nothing here yet. The path is still usable as long as what remains
			// needs no expansion, because setting it will create the missing
			// objects. A remaining wildcard has nothing to match, so it is dropped.
			if !containsWildcard(rest) {
				full := append(slices.Clone(prefix), token)
				*out = append(*out, append(full, rest...))
			}
			return
		}
		walkPointer(child, rest, append(prefix, token), out)

	case []any:
		if token == wildcardToken {
			for i := range typed {
				walkPointer(typed[i], rest, append(prefix, strconv.Itoa(i)), out)
			}
			return
		}
		index, err := strconv.Atoi(token)
		if err != nil || index < 0 || index >= len(typed) {
			return
		}
		walkPointer(typed[index], rest, append(prefix, token), out)
	}
}

// getPointerValue returns the value at a concrete token path, and whether the
// field is present at all. Absence is reported separately from a null value so
// restore can tell "remove this field again" from "write null back".
func getPointerValue(root any, tokens []string) (any, bool) {
	node := root
	for _, token := range tokens {
		switch typed := node.(type) {
		case map[string]any:
			child, ok := typed[token]
			if !ok {
				return nil, false
			}
			node = child
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, false
			}
			node = typed[index]
		default:
			return nil, false
		}
	}
	return node, true
}

// setPointerValue writes value at a concrete token path, creating intermediate
// objects as needed. Array elements must already exist: growing an array would
// mean inventing entries the operator never declared.
func setPointerValue(root map[string]any, tokens []string, value any) error {
	if len(tokens) == 0 {
		return fmt.Errorf("cannot set the document root")
	}

	var node any = root
	for i, token := range tokens[:len(tokens)-1] {
		switch typed := node.(type) {
		case map[string]any:
			child, ok := typed[token]
			if !ok || child == nil {
				child = map[string]any{}
				typed[token] = child
			}
			node = child
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil {
				return fmt.Errorf("segment %q at %s is not an array index", token, formatPointer(tokens[:i+1]))
			}
			if index < 0 || index >= len(typed) {
				return fmt.Errorf("array index %d at %s is out of range", index, formatPointer(tokens[:i+1]))
			}
			node = typed[index]
		default:
			return fmt.Errorf("cannot descend into %s: not an object or array", formatPointer(tokens[:i+1]))
		}
	}

	leaf := tokens[len(tokens)-1]
	switch typed := node.(type) {
	case map[string]any:
		typed[leaf] = value
		return nil
	case []any:
		index, err := strconv.Atoi(leaf)
		if err != nil {
			return fmt.Errorf("segment %q at %s is not an array index", leaf, formatPointer(tokens))
		}
		if index < 0 || index >= len(typed) {
			return fmt.Errorf("array index %d at %s is out of range", index, formatPointer(tokens))
		}
		typed[index] = value
		return nil
	default:
		return fmt.Errorf("cannot set %s: parent is not an object or array", formatPointer(tokens))
	}
}

// deletePointerValue removes the field at a concrete token path. Used to restore
// a field that did not exist before downscale set it. Array elements are left in
// place: removing one would renumber its siblings.
func deletePointerValue(root map[string]any, tokens []string) error {
	if len(tokens) == 0 {
		return fmt.Errorf("cannot delete the document root")
	}

	parent, ok := getPointerValue(root, tokens[:len(tokens)-1])
	if !ok {
		return nil
	}

	leaf := tokens[len(tokens)-1]
	switch typed := parent.(type) {
	case map[string]any:
		delete(typed, leaf)
		return nil
	case []any:
		// Nothing sensible to delete: leave the element alone.
		return nil
	default:
		return fmt.Errorf("cannot delete %s: parent is not an object", formatPointer(tokens))
	}
}

// normalizeJSONValue converts a decoded JSON value into the subset of types that
// unstructured objects accept. Numbers decode to float64, but replica counts and
// other integer fields must round-trip as integers, so whole floats become int64.
func normalizeJSONValue(value any) any {
	switch typed := value.(type) {
	case float64:
		if typed == math.Trunc(typed) && !math.IsInf(typed, 0) &&
			typed >= math.MinInt64 && typed <= math.MaxInt64 {
			return int64(typed)
		}
		return typed
	case int:
		return int64(typed)
	case int32:
		return int64(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = normalizeJSONValue(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = normalizeJSONValue(item)
		}
		return out
	default:
		return value
	}
}
