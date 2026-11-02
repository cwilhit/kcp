/*
Copyright 2026 The kcp Authors.

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

package garbagecollector

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	upstreamgc "k8s.io/kubernetes/pkg/controller/garbagecollector"

	kcpmetadata "github.com/kcp-dev/client-go/metadata"
	"github.com/kcp-dev/logicalcluster/v3"
)

// A metadata-only finalizer update still validates the full object in the
// request version. Legacy APIExport v1alpha1 cannot represent a v1alpha2 Secret
// label selector, so a GC ownerReference using v1alpha1 must not select that
// version for GET/PATCH/DELETE. Exercise the real scoped HTTP client, including
// the original cluster, patch bytes and deletion preconditions.
func TestMetadataClientAPIExportVersion(t *testing.T) {
	for _, tc := range []struct {
		name, group, version, resource, namespace, wantPath string
	}{
		{"legacy export", "apis.kcp.io", "v1alpha1", "apiexports", "", "/clusters/tenant/apis/apis.kcp.io/v1alpha2/apiexports/provider"},
		{"current export", "apis.kcp.io", "v1alpha2", "apiexports", "", "/clusters/tenant/apis/apis.kcp.io/v1alpha2/apiexports/provider"},
		{"other legacy resource", "apis.kcp.io", "v1alpha1", "apibindings", "", "/clusters/tenant/apis/apis.kcp.io/v1alpha1/apibindings/provider"},
		{"unrelated group", "example.test", "v1alpha1", "apiexports", "", "/clusters/tenant/apis/example.test/v1alpha1/apiexports/provider"},
		{"namespaced core resource", "", "v1", "pods", "default", "/clusters/tenant/api/v1/namespaces/default/pods/provider"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			patch := []byte(`{"metadata":{"resourceVersion":"42","finalizers":[]}}`)
			uid := types.UID("original-owner")
			rv := "42"
			methods := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				methods = append(methods, r.Method)
				if r.URL.Path != tc.wantPath {
					t.Errorf("request path = %q, want %q", r.URL.Path, tc.wantPath)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				switch r.Method {
				case http.MethodPatch:
					if string(body) != string(patch) {
						t.Errorf("patch changed: %s", body)
					}
				case http.MethodDelete:
					var options metav1.DeleteOptions
					if err := json.Unmarshal(body, &options); err != nil {
						t.Error(err)
					}
					if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != uid || options.Preconditions.ResourceVersion == nil || *options.Preconditions.ResourceVersion != rv {
						t.Errorf("delete preconditions changed: %s", body)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"apiVersion":"meta.k8s.io/v1","kind":"PartialObjectMetadata","metadata":{"name":"provider","uid":"original-owner","resourceVersion":"42"}}`))
			}))
			defer server.Close()
			client, err := kcpmetadata.NewForConfig(&rest.Config{Host: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			resource := newCtxMetadataClient(client).Resource(schema.GroupVersionResource{Group: tc.group, Version: tc.version, Resource: tc.resource}).Namespace(tc.namespace)
			ctx := upstreamgc.WithCluster(context.Background(), logicalcluster.Name("tenant"))
			obj, err := resource.Get(ctx, "provider", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if obj.UID != uid || obj.ResourceVersion != rv {
				t.Fatalf("identity changed: %#v", obj.ObjectMeta)
			}
			if _, err := resource.Patch(ctx, "provider", types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
				t.Fatal(err)
			}
			if err := resource.Delete(ctx, "provider", metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}}); err != nil {
				t.Fatal(err)
			}
			if len(methods) != 3 {
				t.Errorf("requests = %v, want GET/PATCH/DELETE", methods)
			}
		})
	}
}
