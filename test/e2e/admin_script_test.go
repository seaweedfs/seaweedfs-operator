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
	"context"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/test/utils"
)

// This spec verifies the AdminScript controller end-to-end against a real
// cluster: applying an AdminScript reconciles into a native CronJob that runs
// `weed shell` against the referenced cluster's masters, the CronJob tracks
// spec updates, and it is garbage-collected when the AdminScript is deleted.
//
// The referenced Seaweed cluster is intentionally minimal (master + volume,
// no admin component) so the spec also guards the no-admin code path. The
// SeaweedFS pods are never required to become ready — the AdminScript
// controller only needs the Seaweed CR to exist to render the CronJob — so
// this stays in the light (non-"integration") e2e group that runs on every PR.
var _ = Describe("AdminScript scheduled weed shell", Ordered, func() {
	var (
		ctx           context.Context
		k8sClient     client.Client
		restCfg       *rest.Config
		testNamespace = "test-adminscript"
		seaweedName   = "test-seaweed-adminscript"
		scriptName    = "nightly-balance"
		schedule      = "0 2 * * *"
		script        = "lock\nvolume.balance -force\nunlock"
	)

	scriptKey := func() types.NamespacedName {
		return types.NamespacedName{Name: scriptName, Namespace: testNamespace}
	}
	cronKey := func() types.NamespacedName {
		return types.NamespacedName{Name: scriptName, Namespace: testNamespace}
	}

	BeforeAll(func() {
		ctx = context.Background()
		k8sClient, restCfg = utils.NewE2EClient()
		utils.EnsureNamespace(ctx, k8sClient, testNamespace)

		concurrentStart := true
		diskCount := int32(1)
		seaweed := &seaweedv1.Seaweed{
			ObjectMeta: metav1.ObjectMeta{Name: seaweedName, Namespace: testNamespace},
			Spec: seaweedv1.SeaweedSpec{
				Image: "chrislusf/seaweedfs:latest",
				Master: &seaweedv1.MasterSpec{
					Replicas:        1,
					ConcurrentStart: &concurrentStart,
				},
				Volume: &seaweedv1.VolumeSpec{
					Replicas: 1,
					VolumeServerConfig: seaweedv1.VolumeServerConfig{
						ResourceRequirements: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse("1Gi"),
							},
						},
					},
				},
				VolumeServerDiskCount: &diskCount,
			},
		}
		Expect(k8sClient.Create(ctx, seaweed)).To(Succeed())
	})

	BeforeEach(func() {
		DeferCleanup(func() {
			utils.CollectDiagnostics(ctx, k8sClient, restCfg, testNamespace)
		})
	})

	AfterAll(func() {
		utils.DeleteNamespace(ctx, k8sClient, testNamespace)
	})

	It("renders a CronJob that runs weed shell against the cluster masters", func() {
		adminScript := &seaweedv1.AdminScript{
			ObjectMeta: metav1.ObjectMeta{Name: scriptName, Namespace: testNamespace},
			Spec: seaweedv1.AdminScriptSpec{
				ClusterRef: seaweedv1.AdminScriptClusterRef{Name: seaweedName},
				Schedule:   schedule,
				Script:     script,
			},
		}
		Expect(k8sClient.Create(ctx, adminScript)).To(Succeed())

		cron := &batchv1.CronJob{}
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, cronKey(), cron)).To(Succeed())

			g.Expect(cron.Spec.Schedule).To(Equal(schedule))
			// Default concurrency must be Forbid so admin scripts never overlap.
			g.Expect(cron.Spec.ConcurrencyPolicy).To(Equal(batchv1.ForbidConcurrent))

			// Owned by the AdminScript so it is garbage-collected on delete.
			g.Expect(cron.OwnerReferences).NotTo(BeEmpty())
			owner := cron.OwnerReferences[0]
			g.Expect(owner.Kind).To(Equal("AdminScript"))
			g.Expect(owner.Name).To(Equal(scriptName))
			g.Expect(owner.Controller).NotTo(BeNil())
			g.Expect(*owner.Controller).To(BeTrue())

			containers := cron.Spec.JobTemplate.Spec.Template.Spec.Containers
			g.Expect(containers).To(HaveLen(1))
			c := containers[0]

			// The script is replayed via printf into the weed invocation, which
			// is passed as positional parameters and run via "$@".
			g.Expect(len(c.Command)).To(BeNumerically(">=", 6))
			g.Expect(c.Command[2]).To(Equal(`printf '%s\n' "$WEED_SHELL_SCRIPT" | "$@"`))
			argv := strings.Join(c.Command[4:], " ")
			g.Expect(argv).To(HavePrefix("weed"))
			g.Expect(argv).To(ContainSubstring("shell -master=" + seaweedName + "-master-0."))

			var scriptEnv string
			for _, e := range c.Env {
				if e.Name == "WEED_SHELL_SCRIPT" {
					scriptEnv = e.Value
				}
			}
			g.Expect(scriptEnv).To(Equal(script))
		}, time.Minute*2, time.Second*5).Should(Succeed())

		By("reflecting reconcile status back onto the AdminScript")
		Eventually(func(g Gomega) {
			fetched := &seaweedv1.AdminScript{}
			g.Expect(k8sClient.Get(ctx, scriptKey(), fetched)).To(Succeed())
			g.Expect(fetched.Status.Phase).To(Equal(seaweedv1.AdminScriptPhaseActive))
			g.Expect(fetched.Status.CronJobName).To(Equal(scriptName))
			cond := meta.FindStatusCondition(fetched.Status.Conditions, seaweedv1.AdminScriptConditionReady)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		}, time.Minute, time.Second*5).Should(Succeed())
	})

	It("tracks spec updates onto the managed CronJob", func() {
		newSchedule := "*/30 * * * *"
		Eventually(func(g Gomega) {
			fetched := &seaweedv1.AdminScript{}
			g.Expect(k8sClient.Get(ctx, scriptKey(), fetched)).To(Succeed())
			fetched.Spec.Schedule = newSchedule
			suspend := true
			fetched.Spec.Suspend = &suspend
			g.Expect(k8sClient.Update(ctx, fetched)).To(Succeed())
		}, time.Minute, time.Second*5).Should(Succeed())

		Eventually(func(g Gomega) {
			cron := &batchv1.CronJob{}
			g.Expect(k8sClient.Get(ctx, cronKey(), cron)).To(Succeed())
			g.Expect(cron.Spec.Schedule).To(Equal(newSchedule))
			g.Expect(cron.Spec.Suspend).NotTo(BeNil())
			g.Expect(*cron.Spec.Suspend).To(BeTrue())
		}, time.Minute, time.Second*5).Should(Succeed())

		Eventually(func(g Gomega) {
			fetched := &seaweedv1.AdminScript{}
			g.Expect(k8sClient.Get(ctx, scriptKey(), fetched)).To(Succeed())
			g.Expect(fetched.Status.Phase).To(Equal(seaweedv1.AdminScriptPhaseSuspended))
		}, time.Minute, time.Second*5).Should(Succeed())
	})

	It("garbage-collects the CronJob when the AdminScript is deleted", func() {
		adminScript := &seaweedv1.AdminScript{
			ObjectMeta: metav1.ObjectMeta{Name: scriptName, Namespace: testNamespace},
		}
		Expect(k8sClient.Delete(ctx, adminScript)).To(Succeed())

		Eventually(func() bool {
			cron := &batchv1.CronJob{}
			err := k8sClient.Get(ctx, cronKey(), cron)
			return apierrors.IsNotFound(err)
		}, time.Minute*2, time.Second*5).Should(BeTrue())
	})
})
