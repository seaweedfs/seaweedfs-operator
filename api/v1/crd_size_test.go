package v1

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// TestGeneratedCRDsFitClientSideApply: client-side `kubectl apply` copies the whole object, as JSON, into
// the last-applied-configuration annotation, and the API server caps an object's annotations at 256 KiB in
// total. A CRD past the cap cannot be installed or upgraded with plain `kubectl apply` (including
// `helm template | kubectl apply`), so every generated CRD, and the Helm chart's copy, must fit.
func TestGeneratedCRDsFitClientSideApply(t *testing.T) {
	for _, f := range generatedCRDFiles(t) {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var manifest []string
		for _, line := range strings.Split(string(raw), "\n") {
			if !strings.HasPrefix(line, "{{") {
				manifest = append(manifest, line)
			}
		}
		reader := utilyaml.NewYAMLReader(bufio.NewReader(strings.NewReader(strings.Join(manifest, "\n"))))
		for {
			doc, err := reader.Read()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("%s: %v", filepath.Base(f), err)
			}
			jsonDoc, err := utilyaml.ToJSON(doc)
			if err != nil {
				t.Fatalf("%s: %v", filepath.Base(f), err)
			}
			if bytes.Equal(bytes.TrimSpace(jsonDoc), []byte("null")) {
				continue
			}
			obj := &unstructured.Unstructured{}
			if err := obj.UnmarshalJSON(jsonDoc); err != nil {
				t.Fatalf("%s: %v", filepath.Base(f), err)
			}
			lastApplied, err := runtime.Encode(unstructured.UnstructuredJSONScheme, obj)
			if err != nil {
				t.Fatalf("%s: %v", filepath.Base(f), err)
			}
			size := len(corev1.LastAppliedConfigAnnotation) + len(lastApplied)
			for k, v := range obj.GetAnnotations() {
				size += len(k) + len(v)
			}
			if size > apivalidation.TotalAnnotationSizeLimitB {
				t.Errorf("%s: %s needs %d bytes of annotations under client-side apply, over the %d-byte limit",
					filepath.Base(f), obj.GetName(), size, apivalidation.TotalAnnotationSizeLimitB)
			}
		}
	}
}
