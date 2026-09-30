package v1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGeneratedCRDsCarryNoVariableReference: Flux's postBuild substitution reads every object a
// Kustomization builds, CRDs included, and a literal "${" that is not a variable fails the whole build
// ("unable to parse variable name"). A validation rule or field description written with one makes the
// CRDs undeliverable by any Flux install that substitutes, so none may carry one.
func TestGeneratedCRDsCarryNoVariableReference(t *testing.T) {
	files, err := filepath.Glob("../../config/crd/bases/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no generated CRDs found: %v", err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, "${") {
				t.Errorf("%s:%d carries a literal variable reference: %s", filepath.Base(f), i+1, strings.TrimSpace(line))
			}
		}
	}
}
