/*
Copyright 2024.

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

package e2e

import (
	"fmt"
	"os/exec"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/seaweedfs/seaweedfs-operator/test/utils"
)

// Run e2e tests using the Ginkgo runner.
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	fmt.Fprintf(GinkgoWriter, "Starting seaweedfs-operator suite\n")
	RunSpecs(t, "e2e suite")
}

var _ = BeforeSuite(func() {
	By("prepare kind environment", func() {
		cmd := exec.Command("make", "kind-prepare")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
	})

	By("upload latest image to kind cluster", func() {
		cmd := exec.Command("make", "kind-load")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
	})

	By("install CRDs", func() {
		cmd := exec.Command("make", "install")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
	})

	By("deploy controller-manager", func() {
		cmd := exec.Command("make", "deploy")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
	})
})

var _ = AfterSuite(func() {
	By("cleanup", func() {
		cmd := exec.Command("make", "undeploy")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		cmd = exec.Command("make", "uninstall")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
	})
})
