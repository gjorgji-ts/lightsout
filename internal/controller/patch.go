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

// internal/controller/patch.go
package controller

import (
	"context"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// applyMergePatch sends patch as a JSON merge patch (RFC 7386). A nil value removes
// the key it sits under. A merge patch carries no resourceVersion, so it cannot fail
// with "the object has been modified" against an object whose owning operator rewrites
// its status continuously.
//
// Every write in this package goes through here. Nothing uses client.MergeFrom, which
// diffs a modified copy against the original, because that is unsafe for removals on a
// typed object: ObjectMeta.Labels and ObjectMeta.Annotations are `json:",omitempty"`,
// so deleting the last key this controller knows about leaves an empty map, omitempty
// drops it from the marshalled copy, and the generated patch reads `"labels": null`.
// That deletes every label on the server, including any another controller added after
// our read. Unstructured objects escape it, because they marshal their Object map
// directly, but relying on that difference means reasoning about which kind of object
// is in hand at every call site.
//
// Naming the keys explicitly removes the question. It also drops the DeepCopy that
// diffing needs.
//
// The one write that does NOT use this is the custom resource spec write in
// customresource.go, which must stay an Update. See the comment there.
func applyMergePatch(ctx context.Context, c client.Client, obj client.Object, patch objectPatch) error {
	encoded, err := json.Marshal(patch.build())
	if err != nil {
		return fmt.Errorf("building the merge patch: %w", err)
	}
	return c.Patch(ctx, obj, client.RawPatch(types.MergePatchType, encoded))
}

// Spec field names used in the patches this package builds.
const (
	fieldReplicas = "replicas"
	fieldSuspend  = "suspend"
)

// objectPatch describes one merge patch. Every field is optional: an empty patch is
// an empty object, which the API server accepts as a no-op.
type objectPatch struct {
	// SetLabels and SetAnnotations write each key. Keys not named here keep their
	// current value, so this never disturbs metadata belonging to anyone else.
	SetLabels      map[string]string
	SetAnnotations map[string]string
	// RemoveAnnotations and RemoveLabels null each named key, which removes it.
	// Naming a key that is already absent is a no-op.
	RemoveAnnotations []string
	RemoveLabels      []string
	// Spec is merged into the object's spec, and nests for a deeper field. Name only
	// the fields to change. A nil value removes the field it sits under.
	Spec map[string]any
}

func (p objectPatch) build() map[string]any {
	metadata := make(map[string]any, 2)
	if labels := mergeKeys(p.SetLabels, p.RemoveLabels); len(labels) > 0 {
		metadata["labels"] = labels
	}
	if annotations := mergeKeys(p.SetAnnotations, p.RemoveAnnotations); len(annotations) > 0 {
		metadata["annotations"] = annotations
	}

	patch := make(map[string]any, 2)
	if len(metadata) > 0 {
		patch["metadata"] = metadata
	}
	if len(p.Spec) > 0 {
		patch["spec"] = p.Spec
	}
	return patch
}

// mergeKeys combines the keys to write and the keys to remove into one map. A removed
// key carries a nil value, which a merge patch reads as "delete this key".
func mergeKeys(set map[string]string, remove []string) map[string]any {
	merged := make(map[string]any, len(set)+len(remove))
	for k, v := range set {
		merged[k] = v
	}
	for _, k := range remove {
		merged[k] = nil
	}
	return merged
}
