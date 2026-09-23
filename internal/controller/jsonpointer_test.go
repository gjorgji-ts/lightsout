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
	"reflect"
	"testing"
)

func TestParsePointer(t *testing.T) {
	tests := []struct {
		name    string
		pointer string
		want    []string
		wantErr bool
	}{
		{"simple path", "/spec/replicas", []string{"spec", "replicas"}, false},
		{"escaped slash", "/metadata/annotations/cnpg.io~1hibernation",
			[]string{"metadata", "annotations", "cnpg.io/hibernation"}, false},
		{"escaped tilde", "/spec/od~0d", []string{"spec", "od~d"}, false},
		// "~01" must decode to "~1", not "/": "~1" is substituted before "~0".
		{"tilde then one", "/spec/a~01b", []string{"spec", "a~1b"}, false},
		{"wildcard", "/spec/nodeSets/*/count", []string{"spec", "nodeSets", "*", "count"}, false},
		{"empty", "", nil, true},
		{"no leading slash", "spec/replicas", nil, true},
		{"empty segment", "/spec//replicas", nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePointer(tt.pointer)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got %v", tt.pointer, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parsePointer(%q) = %v, want %v", tt.pointer, got, tt.want)
			}
		})
	}
}

func TestFormatPointerRoundTrips(t *testing.T) {
	for _, pointer := range []string{
		"/spec/replicas",
		"/metadata/annotations/cnpg.io~1hibernation",
		"/spec/nodeSets/0/count",
		"/spec/a~0b",
	} {
		tokens, err := parsePointer(pointer)
		if err != nil {
			t.Fatalf("parsePointer(%q): %v", pointer, err)
		}
		if got := formatPointer(tokens); got != pointer {
			t.Errorf("formatPointer(parsePointer(%q)) = %q", pointer, got)
		}
	}
}

func TestExpandPointer(t *testing.T) {
	doc := map[string]any{
		"metadata": map[string]any{
			"name": "es",
		},
		"spec": map[string]any{
			"nodeSets": []any{
				map[string]any{"name": "masters", "count": int64(3)},
				map[string]any{"name": "data", "count": int64(5)},
			},
			"replicas": int64(2),
		},
	}

	tests := []struct {
		name    string
		pointer string
		want    [][]string
	}{
		{"scalar", "/spec/replicas", [][]string{{"spec", "replicas"}}},
		{
			"wildcard over array",
			"/spec/nodeSets/*/count",
			[][]string{{"spec", "nodeSets", "0", "count"}, {"spec", "nodeSets", "1", "count"}},
		},
		{"explicit index", "/spec/nodeSets/1/count", [][]string{{"spec", "nodeSets", "1", "count"}}},
		{"index out of range", "/spec/nodeSets/9/count", nil},
		// A missing leaf is still returned: setting it is how a new annotation is added.
		{"missing leaf is creatable", "/metadata/labels", [][]string{{"metadata", "labels"}}},
		// A missing intermediate is creatable too, as long as nothing after it
		// needs the document's shape to resolve. This is the CloudNativePG case:
		// a Cluster with no annotations at all still has to gain one.
		{"missing intermediate is creatable", "/metadata/labels/app",
			[][]string{{"metadata", "labels", "app"}}},
		{"missing intermediate several levels deep", "/spec/missing/deep",
			[][]string{{"spec", "missing", "deep"}}},
		// A wildcard after a missing segment has nothing to expand against.
		{"wildcard after missing segment", "/spec/missing/*/count", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens, err := parsePointer(tt.pointer)
			if err != nil {
				t.Fatalf("parsePointer: %v", err)
			}
			got := expandPointer(doc, tokens)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("expandPointer(%q) = %v, want %v", tt.pointer, got, tt.want)
			}
		})
	}
}

func TestSetGetDeletePointerValue(t *testing.T) {
	doc := map[string]any{
		"spec": map[string]any{
			"nodeSets": []any{
				map[string]any{"name": "masters", "count": int64(3)},
			},
		},
	}

	t.Run("set existing scalar inside array element", func(t *testing.T) {
		tokens := []string{"spec", "nodeSets", "0", "count"}
		if err := setPointerValue(doc, tokens, int64(0)); err != nil {
			t.Fatalf("setPointerValue: %v", err)
		}
		got, ok := getPointerValue(doc, tokens)
		if !ok || got != int64(0) {
			t.Errorf("after set: got %v (present=%v), want 0", got, ok)
		}
	})

	t.Run("set creates intermediate objects", func(t *testing.T) {
		tokens := []string{"metadata", "annotations", "cnpg.io/hibernation"}
		if _, ok := getPointerValue(doc, tokens); ok {
			t.Fatal("expected annotation to be absent before set")
		}
		if err := setPointerValue(doc, tokens, "on"); err != nil {
			t.Fatalf("setPointerValue: %v", err)
		}
		got, ok := getPointerValue(doc, tokens)
		if !ok || got != "on" {
			t.Errorf("after set: got %v (present=%v), want \"on\"", got, ok)
		}
	})

	t.Run("delete removes the field entirely", func(t *testing.T) {
		tokens := []string{"metadata", "annotations", "cnpg.io/hibernation"}
		if err := deletePointerValue(doc, tokens); err != nil {
			t.Fatalf("deletePointerValue: %v", err)
		}
		if _, ok := getPointerValue(doc, tokens); ok {
			t.Error("expected annotation to be absent after delete")
		}
	})

	t.Run("array index out of range is an error", func(t *testing.T) {
		if err := setPointerValue(doc, []string{"spec", "nodeSets", "7", "count"}, int64(0)); err == nil {
			t.Error("expected an error for an out-of-range array index")
		}
	})

	t.Run("delete of a missing parent is a no-op", func(t *testing.T) {
		if err := deletePointerValue(doc, []string{"status", "gone", "field"}); err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})
}

func TestNormalizeJSONValue(t *testing.T) {
	// Replica counts decode from JSON as float64 but must round-trip through an
	// unstructured object as integers.
	if got := normalizeJSONValue(float64(3)); got != int64(3) {
		t.Errorf("normalizeJSONValue(3.0) = %#v, want int64(3)", got)
	}
	if got := normalizeJSONValue(float64(1.5)); got != 1.5 {
		t.Errorf("normalizeJSONValue(1.5) = %#v, want 1.5", got)
	}
	if got := normalizeJSONValue("on"); got != "on" {
		t.Errorf("normalizeJSONValue(\"on\") = %#v", got)
	}
	if got := normalizeJSONValue(true); got != true {
		t.Errorf("normalizeJSONValue(true) = %#v", got)
	}

	nested := normalizeJSONValue(map[string]any{
		"count": float64(2),
		"list":  []any{float64(1), "x"},
	})
	want := map[string]any{
		"count": int64(2),
		"list":  []any{int64(1), "x"},
	}
	if !reflect.DeepEqual(nested, want) {
		t.Errorf("normalizeJSONValue(nested) = %#v, want %#v", nested, want)
	}
}
