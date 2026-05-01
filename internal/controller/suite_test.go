/*


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
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// Shared test environment populated by TestMain. Individual tests that
// need a real apiserver call mustEnvtest() to fetch the config + client
// or get a clean Skip when the suite couldn't start (e.g., the
// kubebuilder envtest binaries weren't downloaded). Pure-Go tests in
// this package don't depend on these globals and run regardless.
var (
	envtestEnv    *envtest.Environment
	envtestCfg    *rest.Config
	envtestClient client.Client
)

// TestMain bootstraps an in-process kube-apiserver via envtest and
// installs the operator's CRDs so tests can exercise round-trip
// behaviors that the controller-runtime fake client doesn't reproduce
// (apiserver-side defaulting, CEL admission, ResourceVersion semantics).
//
// If KUBEBUILDER_ASSETS is unset or envtest fails to start, m.Run()
// proceeds anyway — envtest-only tests Skip via mustEnvtest while pure
// unit tests stay green. This means `go test ./...` works on a
// developer machine that has not run setup-envtest, and `make test`
// (which sets KUBEBUILDER_ASSETS) exercises the full suite.
func TestMain(m *testing.M) {
	if err := startEnvtest(); err != nil {
		fmt.Fprintf(os.Stderr, "envtest unavailable, envtest-tagged tests will Skip: %v\n", err)
	}
	code := m.Run()
	if envtestEnv != nil {
		_ = envtestEnv.Stop()
	}
	os.Exit(code)
}

func startEnvtest() error {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		return fmt.Errorf("KUBEBUILDER_ASSETS unset; run `make test` or set the env var to a setup-envtest assets dir")
	}

	envtestEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join(projectRoot(), "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := envtestEnv.Start()
	if err != nil {
		envtestEnv = nil
		return fmt.Errorf("start envtest: %w", err)
	}
	envtestCfg = cfg

	scheme := clientgoscheme.Scheme
	utilruntime.Must(seaweedv1.AddToScheme(scheme))

	cli, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		_ = envtestEnv.Stop()
		envtestEnv = nil
		envtestCfg = nil
		return fmt.Errorf("build envtest client: %w", err)
	}
	envtestClient = cli
	return nil
}

// mustEnvtest returns the running test environment's config+client, or
// Skips the calling test when envtest didn't start. Use as the first
// line in any test that creates real apiserver objects.
func mustEnvtest(t *testing.T) (*rest.Config, client.Client) {
	t.Helper()
	if envtestCfg == nil || envtestClient == nil {
		t.Skip("envtest not running; set KUBEBUILDER_ASSETS or run via `make test`")
	}
	return envtestCfg, envtestClient
}

// projectRoot resolves the operator repo root by walking up from this
// file's location until it sees a go.mod. Lets tests run from any cwd.
func projectRoot() string {
	_, thisFile, _, ok := goruntime.Caller(0)
	if !ok {
		panic("runtime.Caller(0) failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic(fmt.Sprintf("could not find project root from %s", filepath.Dir(thisFile)))
		}
		dir = parent
	}
}
