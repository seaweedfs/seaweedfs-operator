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
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/test/utils"
)

// dirStatus is the subset of the master's /dir/status JSON the spec needs:
// the per-volume-server volume counts, keyed by the server's node Url
// (<host>:<port>) — the same identifier the operator drains by.
type dirStatus struct {
	Topology struct {
		DataCenters []struct {
			Racks []struct {
				DataNodes []struct {
					Url     string `json:"Url"`
					Volumes int64  `json:"Volumes"`
				} `json:"DataNodes"`
			} `json:"Racks"`
		} `json:"DataCenters"`
	} `json:"Topology"`
}

// volumesByNode flattens a dirStatus into Url -> volume count.
func (d dirStatus) volumesByNode() map[string]int64 {
	out := map[string]int64{}
	for _, dc := range d.Topology.DataCenters {
		for _, rack := range dc.Racks {
			for _, dn := range rack.DataNodes {
				out[dn.Url] = dn.Volumes
			}
		}
	}
	return out
}

func (d dirStatus) totalVolumes() int64 {
	var n int64
	for _, v := range d.volumesByNode() {
		n += v
	}
	return n
}

// This spec verifies the volume-server evacuation gate end-to-end against a real
// cluster: when volume.replicas is lowered, the operator drains the doomed
// volume server's data onto the remaining server BEFORE deleting its pod, so no
// volumes are lost. It needs running, registered volume servers (the gate reads
// the master topology), so it lives in the heavier "integration" e2e group.
var _ = Describe("Volume server evacuation on scale-down", Ordered, Label("integration"), func() {
	var (
		ctx           context.Context
		k8sClient     client.Client
		restCfg       *rest.Config
		testNamespace = "test-volume-evacuation"
		seaweedName   = "test-seaweed-evac"
		masterPod     string
	)

	volumeStsKey := types.NamespacedName{Name: seaweedName + "-volume", Namespace: testNamespace}
	seaweedKey := types.NamespacedName{Name: seaweedName, Namespace: testNamespace}
	// doomedNodePrefix matches the volume server at ordinal 1 — the pod a
	// 2 -> 1 scale-down removes — by its master node Url.
	doomedNodePrefix := fmt.Sprintf("%s-volume-1.", seaweedName)

	// masterGet performs an HTTP GET against the master over a fresh
	// port-forward. A per-call forward keeps the helper robust across the
	// multi-minute drain poll, where a single long-lived forward can drop.
	masterGet := func(path string) (string, error) {
		stopCh := make(chan struct{}, 1)
		readyCh := make(chan struct{})
		localPort, err := utils.GetFreePort()
		if err != nil {
			return "", err
		}
		if err := utils.RunPortForward(restCfg, testNamespace, masterPod,
			[]string{fmt.Sprintf("%d:%d", localPort, seaweedv1.MasterHTTPPort)}, stopCh, readyCh); err != nil {
			return "", err
		}
		defer close(stopCh)
		select {
		case <-readyCh:
		case <-time.After(15 * time.Second):
			return "", fmt.Errorf("port-forward to %s not ready", masterPod)
		}
		resp, err := resty.New().SetTimeout(15 * time.Second).R().
			Get(fmt.Sprintf("http://localhost:%d%s", localPort, path))
		if err != nil {
			return "", err
		}
		if resp.StatusCode() != http.StatusOK {
			return "", fmt.Errorf("GET %s: status %d: %s", path, resp.StatusCode(), resp.String())
		}
		return resp.String(), nil
	}

	fetchTopology := func() (dirStatus, error) {
		var ds dirStatus
		body, err := masterGet("/dir/status")
		if err != nil {
			return ds, err
		}
		if err := json.Unmarshal([]byte(body), &ds); err != nil {
			return ds, fmt.Errorf("parse /dir/status: %w (body: %s)", err, body)
		}
		return ds, nil
	}

	BeforeAll(func() {
		ctx = context.Background()
		k8sClient, restCfg = utils.NewE2EClient()
		utils.EnsureNamespace(ctx, k8sClient, testNamespace)
	})

	BeforeEach(func() {
		DeferCleanup(func() {
			utils.CollectDiagnostics(ctx, k8sClient, restCfg, testNamespace)
		})
	})

	AfterAll(func() {
		seaweed := &seaweedv1.Seaweed{}
		if err := k8sClient.Get(ctx, seaweedKey, seaweed); err == nil {
			_ = k8sClient.Delete(ctx, seaweed)
		}
		utils.DeleteNamespace(ctx, k8sClient, testNamespace)
	})

	It("drains a volume server before removing it and preserves its volumes", func() {
		concurrentStart := true
		diskCount := int32(1)
		// An explicit per-server max lets /vol/grow create volumes on the tiny
		// test PVCs (with the default -max=0 the server derives its cap from
		// free disk and a small PVC computes to ~0 writable volumes).
		maxVolumeCounts := int32(10)
		seaweed := &seaweedv1.Seaweed{
			ObjectMeta: metav1.ObjectMeta{Name: seaweedName, Namespace: testNamespace},
			Spec: seaweedv1.SeaweedSpec{
				Image:                 "chrislusf/seaweedfs:latest",
				VolumeServerDiskCount: &diskCount,
				Master: &seaweedv1.MasterSpec{
					Replicas:        1,
					ConcurrentStart: &concurrentStart,
				},
				Volume: &seaweedv1.VolumeSpec{
					Replicas: 2,
					VolumeServerConfig: seaweedv1.VolumeServerConfig{
						MaxVolumeCounts: &maxVolumeCounts,
						ResourceRequirements: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse("2Gi"),
							},
						},
					},
				},
			},
		}

		By("creating a master + 2-volume-server cluster and waiting for Ready")
		Expect(k8sClient.Create(ctx, seaweed)).To(Succeed())
		utils.WaitForSeaweedReady(ctx, k8sClient, seaweedKey, 7*time.Minute)

		By("locating the master pod for port-forwarding")
		clientset, err := utils.GetClientset(restCfg)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func(g Gomega) {
			pods, err := clientset.CoreV1().Pods(testNamespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("app.kubernetes.io/component=master,app.kubernetes.io/instance=%s", seaweedName),
			})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(pods.Items).NotTo(BeEmpty(), "no master pods found")
			masterPod = pods.Items[0].Name
		}, time.Minute, time.Second*5).Should(Succeed())

		By("growing volumes so both servers host data")
		// replication=000 keeps every volume to a single copy, so the doomed
		// server's volumes can always move onto the one remaining server.
		Eventually(func(g Gomega) {
			_, err := masterGet("/vol/grow?count=10&replication=000")
			g.Expect(err).NotTo(HaveOccurred())
			ds, err := fetchTopology()
			g.Expect(err).NotTo(HaveOccurred())
			byNode := ds.volumesByNode()
			g.Expect(byNode).To(HaveLen(2), "expected two registered volume servers")
			var doomed int64 = -1
			for url, n := range byNode {
				if strings.HasPrefix(url, doomedNodePrefix) {
					doomed = n
				}
			}
			g.Expect(doomed).To(BeNumerically(">", 0), "doomed server holds no volumes to evacuate")
		}, time.Minute*2, time.Second*5).Should(Succeed())

		ds, err := fetchTopology()
		Expect(err).NotTo(HaveOccurred())
		totalBefore := ds.totalVolumes()
		Expect(totalBefore).To(BeNumerically(">", 0))
		GinkgoWriter.Printf("volumes before scale-down: %d across %v\n", totalBefore, ds.volumesByNode())

		By("scaling the volume servers from 2 down to 1")
		Eventually(func(g Gomega) {
			fetched := &seaweedv1.Seaweed{}
			g.Expect(k8sClient.Get(ctx, seaweedKey, fetched)).To(Succeed())
			fetched.Spec.Volume.Replicas = 1
			g.Expect(k8sClient.Update(ctx, fetched)).To(Succeed())
		}, time.Minute, time.Second*5).Should(Succeed())

		By("emitting a VolumeServerEvacuating event for the doomed server")
		Eventually(func(g Gomega) {
			events := &corev1.EventList{}
			g.Expect(k8sClient.List(ctx, events, client.InNamespace(testNamespace))).To(Succeed())
			found := false
			for _, e := range events.Items {
				if e.InvolvedObject.Name == seaweedName && e.Reason == "VolumeServerEvacuating" {
					found = true
				}
			}
			g.Expect(found).To(BeTrue(), "no VolumeServerEvacuating event was recorded")
		}, time.Minute*3, time.Second*5).Should(Succeed())

		By("removing the drained pod only after its volumes moved, losing none")
		Eventually(func(g Gomega) {
			sts := &appsv1.StatefulSet{}
			g.Expect(k8sClient.Get(ctx, volumeStsKey, sts)).To(Succeed())
			g.Expect(sts.Spec.Replicas).NotTo(BeNil())
			g.Expect(*sts.Spec.Replicas).To(Equal(int32(1)), "volume StatefulSet did not shrink to 1")
			g.Expect(sts.Status.Replicas).To(Equal(int32(1)), "doomed volume pod was not removed")

			ds, err := fetchTopology()
			g.Expect(err).NotTo(HaveOccurred())
			byNode := ds.volumesByNode()
			// The doomed server is gone from the topology...
			for url := range byNode {
				g.Expect(url).NotTo(HavePrefix(doomedNodePrefix), "doomed server still registered")
			}
			// ...and every volume it held now lives on the surviving server.
			g.Expect(ds.totalVolumes()).To(Equal(totalBefore), "volumes were lost during scale-down")
		}, time.Minute*6, time.Second*10).Should(Succeed())

		By("returning the cluster to Ready at the new size")
		utils.WaitForSeaweedReady(ctx, k8sClient, seaweedKey, 3*time.Minute)
	})
})
