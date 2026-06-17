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
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/test/utils"
)

// This spec verifies the backup/restore controllers end-to-end against a real
// cluster: a SeaweedBackup reconciles into an fs.meta.save snapshot Job, a
// SeaweedRestore into an fs.meta.load Job, spec.backup.dataMirror into a
// continuous weed filer.backup Deployment (+ replication.toml Secret), and the
// leader-elected scheduler creates SeaweedBackups from spec.backup.schedule.
//
// The referenced Seaweed cluster is intentionally minimal (master + volume) and
// the SeaweedFS pods are never required to become ready — the controllers only
// need the Seaweed CR to exist to render the child resources — so this stays in
// the light e2e group that runs on every PR. The backup PVC is likewise never
// provisioned; we assert on the rendered Job/Deployment, not on their pods
// completing.
var _ = Describe("Backup and restore", Ordered, func() {
	var (
		ctx           context.Context
		k8sClient     client.Client
		restCfg       *rest.Config
		testNamespace = "test-backup"
		seaweedName   = "test-seaweed-backup"
	)

	clusterKey := func() types.NamespacedName {
		return types.NamespacedName{Name: seaweedName, Namespace: testNamespace}
	}
	named := func(name string) types.NamespacedName {
		return types.NamespacedName{Name: name, Namespace: testNamespace}
	}

	BeforeAll(func() {
		ctx = context.Background()
		k8sClient, restCfg = utils.NewE2EClient()
		utils.EnsureNamespace(ctx, k8sClient, testNamespace)

		concurrentStart := true
		diskCount := int32(1)
		forcePath := true
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
				Backup: &seaweedv1.BackupSpec{
					Storages: map[string]seaweedv1.BackupStorageSpec{
						"pvc": {
							Type:       seaweedv1.BackupStorageFilesystem,
							Filesystem: &seaweedv1.FilesystemBackupStore{ExistingClaim: "backup-pvc", MountPath: "/backup"},
						},
						"s3": {
							Type: seaweedv1.BackupStorageS3,
							S3: &seaweedv1.S3BackupStore{
								Bucket:         "test-backups",
								Region:         "us-east-1",
								Directory:      "/",
								ForcePathStyle: &forcePath,
							},
						},
					},
					DataMirror: []seaweedv1.BackupMirrorSpec{{StorageName: "s3", FilerPath: "/"}},
				},
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

	It("renders an fs.meta.save snapshot Job for an on-demand SeaweedBackup", func() {
		backup := &seaweedv1.SeaweedBackup{
			ObjectMeta: metav1.ObjectMeta{Name: "adhoc-snap", Namespace: testNamespace},
			Spec:       seaweedv1.SeaweedBackupSpec{ClusterName: seaweedName, StorageName: "pvc", FilerPath: "/"},
		}
		Expect(k8sClient.Create(ctx, backup)).To(Succeed())

		job := &batchv1.Job{}
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, named("adhoc-snap-bkp"), job)).To(Succeed())

			// Owned by the SeaweedBackup so it is garbage-collected on delete.
			g.Expect(job.OwnerReferences).NotTo(BeEmpty())
			owner := job.OwnerReferences[0]
			g.Expect(owner.Kind).To(Equal("SeaweedBackup"))
			g.Expect(owner.Name).To(Equal("adhoc-snap"))
			g.Expect(owner.Controller).NotTo(BeNil())
			g.Expect(*owner.Controller).To(BeTrue())

			containers := job.Spec.Template.Spec.Containers
			g.Expect(containers).To(HaveLen(1))
			g.Expect(containers[0].Command).To(HaveLen(3))
			script := containers[0].Command[2]
			g.Expect(script).To(ContainSubstring("fs.meta.save -o /backup/" + seaweedName + "/adhoc-snap/filer.meta.gz /"))
			g.Expect(script).To(ContainSubstring("shell -master=" + seaweedName + "-master-0."))
		}, time.Minute*2, time.Second*5).Should(Succeed())

		By("reflecting Running status onto the SeaweedBackup")
		Eventually(func(g Gomega) {
			fetched := &seaweedv1.SeaweedBackup{}
			g.Expect(k8sClient.Get(ctx, named("adhoc-snap"), fetched)).To(Succeed())
			g.Expect(fetched.Status.JobName).To(Equal("adhoc-snap-bkp"))
			g.Expect(fetched.Status.Phase).To(Equal(seaweedv1.BackupPhaseRunning))
		}, time.Minute, time.Second*5).Should(Succeed())
	})

	It("garbage-collects the snapshot Job when the SeaweedBackup is deleted", func() {
		Expect(k8sClient.Delete(ctx, &seaweedv1.SeaweedBackup{
			ObjectMeta: metav1.ObjectMeta{Name: "adhoc-snap", Namespace: testNamespace},
		})).To(Succeed())

		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, named("adhoc-snap-bkp"), &batchv1.Job{}))
		}, time.Minute*2, time.Second*5).Should(BeTrue())
	})

	It("manages a continuous data-mirror Deployment and its replication.toml Secret", func() {
		Eventually(func(g Gomega) {
			dep := &appsv1.Deployment{}
			g.Expect(k8sClient.Get(ctx, named(seaweedName+"-backup-mirror-s3"), dep)).To(Succeed())

			g.Expect(dep.OwnerReferences).NotTo(BeEmpty())
			g.Expect(dep.OwnerReferences[0].Kind).To(Equal("Seaweed"))

			containers := dep.Spec.Template.Spec.Containers
			g.Expect(containers).To(HaveLen(1))
			argv := strings.Join(containers[0].Command, " ")
			g.Expect(argv).To(ContainSubstring("filer.backup"))
			g.Expect(argv).To(ContainSubstring("-initialSnapshot"))
			g.Expect(argv).To(ContainSubstring("-config_dir=/etc/seaweedfs"))

			secret := &corev1.Secret{}
			g.Expect(k8sClient.Get(ctx, named(seaweedName+"-backup-mirror-s3-config"), secret)).To(Succeed())
			g.Expect(secret.Data).To(HaveKey("replication.toml"))
			g.Expect(string(secret.Data["replication.toml"])).To(ContainSubstring("[sink.s3]"))
			g.Expect(string(secret.Data["replication.toml"])).To(ContainSubstring(`bucket = "test-backups"`))
		}, time.Minute*2, time.Second*5).Should(Succeed())
	})

	It("prunes the data mirror when removed from the cluster spec", func() {
		Eventually(func(g Gomega) {
			fetched := &seaweedv1.Seaweed{}
			g.Expect(k8sClient.Get(ctx, clusterKey(), fetched)).To(Succeed())
			fetched.Spec.Backup.DataMirror = nil
			g.Expect(k8sClient.Update(ctx, fetched)).To(Succeed())
		}, time.Minute, time.Second*5).Should(Succeed())

		Eventually(func() bool {
			depGone := apierrors.IsNotFound(k8sClient.Get(ctx, named(seaweedName+"-backup-mirror-s3"), &appsv1.Deployment{}))
			secretGone := apierrors.IsNotFound(k8sClient.Get(ctx, named(seaweedName+"-backup-mirror-s3-config"), &corev1.Secret{}))
			return depGone && secretGone
		}, time.Minute*2, time.Second*5).Should(BeTrue())
	})

	It("renders an fs.meta.load restore Job for a SeaweedRestore", func() {
		restore := &seaweedv1.SeaweedRestore{
			ObjectMeta: metav1.ObjectMeta{Name: "restore-1", Namespace: testNamespace},
			Spec: seaweedv1.SeaweedRestoreSpec{
				ClusterName:  seaweedName,
				BackupSource: &seaweedv1.BackupSource{StorageName: "pvc", MetaPath: seaweedName + "/adhoc-snap/filer.meta.gz"},
				FilerPath:    "/",
			},
		}
		Expect(k8sClient.Create(ctx, restore)).To(Succeed())

		Eventually(func(g Gomega) {
			job := &batchv1.Job{}
			g.Expect(k8sClient.Get(ctx, named("restore-1-rst"), job)).To(Succeed())
			g.Expect(job.OwnerReferences).NotTo(BeEmpty())
			g.Expect(job.OwnerReferences[0].Kind).To(Equal("SeaweedRestore"))

			containers := job.Spec.Template.Spec.Containers
			g.Expect(containers).To(HaveLen(1))
			script := containers[0].Command[2]
			g.Expect(script).To(ContainSubstring("fs.meta.load /backup/" + seaweedName + "/adhoc-snap/filer.meta.gz"))
		}, time.Minute*2, time.Second*5).Should(Succeed())
	})

	It("fires a scheduled SeaweedBackup from spec.backup.schedule", func() {
		By("adding a frequent schedule to the cluster")
		Eventually(func(g Gomega) {
			fetched := &seaweedv1.Seaweed{}
			g.Expect(k8sClient.Get(ctx, clusterKey(), fetched)).To(Succeed())
			fetched.Spec.Backup.Schedule = []seaweedv1.BackupScheduleSpec{{
				Name:        "frequent",
				Schedule:    "* * * * *",
				StorageName: "pvc",
				Keep:        2,
				FilerPath:   "/",
			}}
			g.Expect(k8sClient.Update(ctx, fetched)).To(Succeed())
		}, time.Minute, time.Second*5).Should(Succeed())

		By("waiting for the scheduler to create a labelled SeaweedBackup")
		Eventually(func(g Gomega) {
			list := &seaweedv1.SeaweedBackupList{}
			g.Expect(k8sClient.List(ctx, list,
				client.InNamespace(testNamespace),
				client.MatchingLabels{seaweedv1.LabelBackupSchedule: "frequent"},
			)).To(Succeed())
			g.Expect(len(list.Items)).To(BeNumerically(">=", 1))
			g.Expect(list.Items[0].Spec.ClusterName).To(Equal(seaweedName))
			g.Expect(list.Items[0].Spec.StorageName).To(Equal("pvc"))
		}, time.Minute*3, time.Second*10).Should(Succeed())

		By("suspending the schedule so it stops creating backups")
		Eventually(func(g Gomega) {
			fetched := &seaweedv1.Seaweed{}
			g.Expect(k8sClient.Get(ctx, clusterKey(), fetched)).To(Succeed())
			for i := range fetched.Spec.Backup.Schedule {
				fetched.Spec.Backup.Schedule[i].Suspend = true
			}
			g.Expect(k8sClient.Update(ctx, fetched)).To(Succeed())
		}, time.Minute, time.Second*5).Should(Succeed())
	})
})
