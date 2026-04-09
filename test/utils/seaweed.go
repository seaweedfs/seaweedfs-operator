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

// Shared e2e helpers: client construction, namespace management, wait-for-ready
// polling, and on-failure diagnostics collection. Used by every spec in
// test/e2e so the individual specs can focus on the behavior under test.
package utils

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:golint,revive
	. "github.com/onsi/gomega"    //nolint:golint,revive
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/yaml"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// OperatorNamespace is the namespace where the controller-manager runs.
// Matches the NAMESPACE variable in the Makefile.
const OperatorNamespace = "seaweedfs-operator-system"

// NewE2EClient builds a controller-runtime client wired with the core +
// Seaweed scheme. Fails the spec on error so callers can keep their setup
// concise.
func NewE2EClient() (client.Client, *rest.Config) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(seaweedv1.AddToScheme(scheme))

	cfg := config.GetConfigOrDie()
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred(), "failed to build e2e client")
	return c, cfg
}

// EnsureNamespace creates the namespace if it does not already exist.
// Idempotent — safe to call from BeforeAll. If a previous run left the
// namespace in a terminating state, waits up to two minutes for it to
// fully disappear before creating it, so a child resource Create does
// not race against namespace finalization.
func EnsureNamespace(ctx context.Context, c client.Client, name string) {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		existing := &corev1.Namespace{}
		err := c.Get(ctx, types.NamespacedName{Name: name}, existing)
		if errors.IsNotFound(err) {
			// Namespace is fully gone; safe to (re-)create.
			break
		}
		Expect(err).NotTo(HaveOccurred(), "failed to get namespace %s", name)
		// If it already exists and is Active, nothing to do.
		if existing.Status.Phase == corev1.NamespaceActive {
			return
		}
		// Terminating (or another intermediate phase): wait and retry.
		if time.Now().After(deadline) {
			Fail(fmt.Sprintf("namespace %s stuck in %s", name, existing.Status.Phase))
		}
		time.Sleep(2 * time.Second)
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	err := c.Create(ctx, ns)
	if err != nil && !errors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred(), "failed to create namespace %s", name)
	}
	// One more sanity loop: wait until the namespace is reported Active
	// before returning so the first child resource Create does not fail
	// with NamespaceTerminating.
	deadline = time.Now().Add(30 * time.Second)
	for {
		existing := &corev1.Namespace{}
		if err := c.Get(ctx, types.NamespacedName{Name: name}, existing); err == nil &&
			existing.Status.Phase == corev1.NamespaceActive {
			return
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// DeleteNamespace issues a background delete. Callers typically defer this
// from BeforeAll; tests do not wait for full deletion to keep runtime down.
//
// NotFound is swallowed because AfterAll often runs after the namespace
// is already torn down (for example by a previous spec's cleanup). Any
// other error is surfaced via Expect so a flaky teardown does not leave
// stale state behind that would poison later runs in the same suite.
func DeleteNamespace(ctx context.Context, c client.Client, name string) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := c.Delete(ctx, ns); err != nil && !errors.IsNotFound(err) {
		Expect(err).NotTo(HaveOccurred(), "failed to delete namespace %s", name)
	}
}

// WaitForSeaweedReady polls the Seaweed CR until its Ready condition is True
// or the timeout expires. Logs the per-component status on every poll so a
// timeout failure in CI is self-diagnosing.
func WaitForSeaweedReady(ctx context.Context, c client.Client, key types.NamespacedName, timeout time.Duration) {
	Eventually(func() bool {
		sw := &seaweedv1.Seaweed{}
		if err := c.Get(ctx, key, sw); err != nil {
			fmt.Fprintf(GinkgoWriter, "get seaweed %s: %v\n", key, err)
			return false
		}
		fmt.Fprintf(GinkgoWriter,
			"Seaweed %s status: master=%d/%d, volume=%d/%d, filer=%d/%d, admin=%d/%d, worker=%d/%d\n",
			key,
			sw.Status.Master.ReadyReplicas, sw.Status.Master.Replicas,
			sw.Status.Volume.ReadyReplicas, sw.Status.Volume.Replicas,
			sw.Status.Filer.ReadyReplicas, sw.Status.Filer.Replicas,
			sw.Status.Admin.ReadyReplicas, sw.Status.Admin.Replicas,
			sw.Status.Worker.ReadyReplicas, sw.Status.Worker.Replicas,
		)
		for _, cond := range sw.Status.Conditions {
			if cond.Type == "Ready" && cond.Status == metav1.ConditionTrue {
				return true
			}
		}
		return false
	}, timeout, 5*time.Second).Should(BeTrue(),
		"Seaweed %s did not become Ready within %s", key, timeout)
}

// CollectDiagnostics dumps operator logs, CR yaml, pods, and events from the
// given namespaces. Intended as a DeferCleanup registered at the start of a
// spec so a failure leaves a self-contained record of what went wrong.
// Silent on success — only writes when the current spec has failed.
func CollectDiagnostics(ctx context.Context, c client.Client, cfg *rest.Config, namespaces ...string) {
	if !CurrentSpecReport().Failed() {
		return
	}
	w := GinkgoWriter
	fmt.Fprintf(w, "\n======== e2e diagnostics (spec failed) ========\n")

	// Operator logs first — the most likely place the root cause lives.
	clientset, err := GetClientset(cfg)
	if err == nil {
		pods := &corev1.PodList{}
		if err := c.List(ctx, pods, client.InNamespace(OperatorNamespace),
			client.MatchingLabels{"control-plane": "controller-manager"}); err == nil {
			for _, p := range pods.Items {
				fmt.Fprintf(w, "\n--- operator logs: %s ---\n", p.Name)
				req := clientset.CoreV1().Pods(OperatorNamespace).GetLogs(p.Name, &corev1.PodLogOptions{
					Container: "manager",
				})
				stream, err := req.Stream(ctx)
				if err != nil {
					fmt.Fprintf(w, "(failed to stream logs: %v)\n", err)
					continue
				}
				buf := make([]byte, 4096)
				for {
					n, err := stream.Read(buf)
					if n > 0 {
						w.Write(buf[:n]) //nolint:errcheck
					}
					if err != nil {
						break
					}
				}
				stream.Close() //nolint:errcheck
			}
		}
	}

	for _, ns := range namespaces {
		fmt.Fprintf(w, "\n--- namespace %s: seaweeds ---\n", ns)
		list := &seaweedv1.SeaweedList{}
		if err := c.List(ctx, list, client.InNamespace(ns)); err == nil {
			for i := range list.Items {
				y, _ := yaml.Marshal(&list.Items[i])
				fmt.Fprintln(w, string(y))
			}
		}

		fmt.Fprintf(w, "\n--- namespace %s: pods ---\n", ns)
		pods := &corev1.PodList{}
		if err := c.List(ctx, pods, client.InNamespace(ns)); err == nil {
			for _, p := range pods.Items {
				fmt.Fprintf(w, "%s  phase=%s  ready=%v\n",
					p.Name, p.Status.Phase, isPodReady(&p))
				for _, cs := range p.Status.ContainerStatuses {
					if cs.State.Waiting != nil {
						fmt.Fprintf(w, "  %s waiting: %s: %s\n",
							cs.Name, cs.State.Waiting.Reason, cs.State.Waiting.Message)
					}
					if cs.State.Terminated != nil {
						fmt.Fprintf(w, "  %s terminated: %s (exit %d): %s\n",
							cs.Name, cs.State.Terminated.Reason, cs.State.Terminated.ExitCode,
							cs.State.Terminated.Message)
					}
				}
			}
		}

		fmt.Fprintf(w, "\n--- namespace %s: recent events ---\n", ns)
		events := &corev1.EventList{}
		if err := c.List(ctx, events, client.InNamespace(ns)); err == nil {
			for _, e := range events.Items {
				fmt.Fprintf(w, "%s  %s  %s: %s\n",
					e.LastTimestamp.Format(time.RFC3339), e.Type, e.Reason, e.Message)
			}
		}
	}
	fmt.Fprintf(w, "======== end diagnostics ========\n\n")
}

func isPodReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
