//go:build e2e
// +build e2e

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

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gjorgji-ts/lightsout/test/utils"
)

// Sync waves order the scaling by the ArgoCD sync-wave annotation and wait for each wave
// before starting the next. The assertion that matters is the wait: a later wave must stay
// untouched while the wave before it has not settled.
//
// Two deployments carry that, one per direction:
//
//   - data-tier, wave -1, ignores SIGTERM and reports ready 30s after it starts. Slow to
//     come up, so the upscale holds the app tier behind it.
//   - app-tier, wave 1, ignores SIGTERM with a 90s grace period. Slow to go away, so the
//     downscale holds the data tier behind it.
//
// Both resolve on their own, so neither spec needs to poke the cluster to finish.
var _ = Describe("Sync waves", Ordered, func() {
	const (
		scheduleNamespace = "lightsout-system"
		testNamespace     = "test-sync-waves"
		scheduleName      = "test-sync-waves"
		dataTier          = "data-tier"
		appTier           = "app-tier"
		scheduleFile      = "/tmp/test-sync-wave-schedule.yaml"
	)

	// applySchedule writes the schedule in the given period. Sync waves need the argoCD
	// block, and warmupTimeout caps each wave. It is long here so a wave that is waited
	// for is never given up on mid-spec, which would hide the hold this suite asserts.
	applySchedule := func(upscale, downscale string) {
		scheduleYAML := fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutSchedule
metadata:
  name: %s
  namespace: %s
spec:
  upscale: "%s"
  downscale: "%s"
  timezone: "UTC"
  namespaces:
    - %s
  argoCD:
    syncWaves: true
    warmupTimeout: 10m
`, scheduleName, scheduleNamespace, upscale, downscale, testNamespace)

		Expect(os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)).To(Succeed())
		cmd := exec.Command("kubectl", "apply", "-f", scheduleFile)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
	}

	BeforeAll(func() {
		By("creating test namespace")
		cmd := exec.Command("kubectl", "create", "ns", testNamespace)
		_, _ = utils.Run(cmd) // Ignore error if exists

		By("creating a deployment in wave -1 that is slow to report ready")
		createWaveDeployment(testNamespace, dataTier, "-1", 1, 5, 30)

		By("creating a deployment in wave 1 that is slow to shut down")
		createWaveDeployment(testNamespace, appTier, "1", 2, 90, 0)
	})

	AfterAll(func() {
		By("cleaning up test resources")
		cmd := exec.Command("kubectl", "delete", "lightsoutschedule", scheduleName,
			"-n", scheduleNamespace, "--ignore-not-found")
		_, _ = utils.Run(cmd)
		cmd = exec.Command("kubectl", "delete", "ns", testNamespace, "--ignore-not-found")
		_, _ = utils.Run(cmd)
	})

	It("should hold the lower wave down until the higher wave has no pods left", func() {
		By("creating a LightsOutSchedule in downscale period with sync waves enabled")
		applySchedule("0 0 31 12 *", "0 0 1 1 *")

		By("waiting for wave 1 to be scaled down")
		Eventually(func(g Gomega) {
			g.Expect(getDeploymentReplicas(testNamespace, appTier)).To(Equal("0"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		// The hold is asserted before the status field. A regression that scales
		// everything in one pass then fails on the behaviour, not on a missing status.
		By("verifying wave -1 stays up while the wave 1 pods are still terminating")
		Expect(getPodCount(testNamespace, appTier)).NotTo(BeZero(),
			"the wave 1 pods should still be terminating, so the gate has something to wait for")
		Consistently(func(g Gomega) {
			g.Expect(getDeploymentReplicas(testNamespace, dataTier)).To(Equal("1"))
		}, 20*time.Second, 5*time.Second).Should(Succeed())

		By("verifying the schedule reports the wave it is waiting on")
		Eventually(func(g Gomega) {
			g.Expect(getScheduleField(scheduleNamespace, scheduleName, "{.status.waveProgress.wave}")).To(Equal("1"))
		}, 30*time.Second, 5*time.Second).Should(Succeed())

		By("waiting for the wave 1 pods to go away")
		Eventually(func(g Gomega) {
			g.Expect(getPodCount(testNamespace, appTier)).To(BeZero())
		}, 3*time.Minute, 5*time.Second).Should(Succeed())

		By("verifying wave -1 is scaled down once wave 1 has settled")
		Eventually(func(g Gomega) {
			g.Expect(getDeploymentReplicas(testNamespace, dataTier)).To(Equal("0"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("verifying the schedule reports Down")
		Eventually(func(g Gomega) {
			g.Expect(getScheduleField(scheduleNamespace, scheduleName, "{.status.state}")).To(Equal("Down"))
		}, 30*time.Second, 5*time.Second).Should(Succeed())
	})

	It("should hold the higher wave at zero until the lower wave reports ready", func() {
		By("updating the schedule to the upscale period")
		applySchedule("0 0 1 1 *", "0 0 31 12 *")

		By("waiting for wave -1 to be scaled up")
		Eventually(func(g Gomega) {
			g.Expect(getDeploymentReplicas(testNamespace, dataTier)).To(Equal("1"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("verifying wave 1 stays at zero while the wave -1 pod is not ready")
		Consistently(func(g Gomega) {
			g.Expect(getDeploymentReplicas(testNamespace, appTier)).To(Equal("0"))
		}, 20*time.Second, 5*time.Second).Should(Succeed())

		By("verifying the schedule reports the wave it is waiting on")
		Eventually(func(g Gomega) {
			g.Expect(getScheduleField(scheduleNamespace, scheduleName, "{.status.waveProgress.wave}")).To(Equal("-1"))
		}, 30*time.Second, 5*time.Second).Should(Succeed())

		By("waiting for the wave -1 pod to report ready")
		Eventually(func(g Gomega) {
			g.Expect(getDeploymentReadyReplicas(testNamespace, dataTier)).To(Equal("1"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("verifying wave 1 is restored once wave -1 is ready")
		Eventually(func(g Gomega) {
			g.Expect(getDeploymentReplicas(testNamespace, appTier)).To(Equal("2"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("verifying the annotations are removed from both waves")
		verifyDeploymentAnnotationMissing(testNamespace, dataTier, "lightsout.techsupport.mk/original-replicas")
		verifyDeploymentAnnotationMissing(testNamespace, appTier, "lightsout.techsupport.mk/original-replicas")

		By("verifying the schedule reports Up and clears the wave it was waiting on")
		Eventually(func(g Gomega) {
			g.Expect(getScheduleField(scheduleNamespace, scheduleName, "{.status.state}")).To(Equal("Up"))
		}, 30*time.Second, 5*time.Second).Should(Succeed())
	})

	It("should scale a workload without the annotation as wave 0", func() {
		By("creating a deployment with no sync-wave annotation")
		createDeployment(testNamespace, "unwaved", 1)

		By("moving the schedule back to the downscale period")
		applySchedule("0 0 31 12 *", "0 0 1 1 *")

		By("verifying the unannotated deployment is scaled down with the rest")
		Eventually(func(g Gomega) {
			g.Expect(getDeploymentReplicas(testNamespace, "unwaved")).To(Equal("0"))
		}, 4*time.Minute, 5*time.Second).Should(Succeed())
	})
})

// createWaveDeployment creates a deployment in a sync wave, with pods that take
// gracePeriod seconds to die and readyDelay seconds to report ready.
//
// The container ignores SIGTERM, so the kubelet waits out the whole grace period before
// it sends SIGKILL. That is what makes a downscale wave take a known, observable time to
// settle. A readyDelay of zero leaves the readiness probe off, and the pod is ready as
// soon as it runs.
func createWaveDeployment(namespace, name, wave string, replicas, gracePeriod, readyDelay int) {
	probe := ""
	if readyDelay > 0 {
		probe = fmt.Sprintf(`
        readinessProbe:
          exec:
            command: ["true"]
          initialDelaySeconds: %d
          periodSeconds: 2`, readyDelay)
	}

	yaml := fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
  annotations:
    argocd.argoproj.io/sync-wave: "%s"
spec:
  replicas: %d
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
      labels:
        app: %s
    spec:
      terminationGracePeriodSeconds: %d
      containers:
      - name: slow
        image: busybox
        command: ["sh", "-c", "trap '' TERM; sleep 3600"]
        resources:
          limits:
            memory: "32Mi"
            cpu: "10m"%s
`, name, namespace, wave, replicas, name, name, gracePeriod, probe)

	file := fmt.Sprintf("/tmp/wave-%s.yaml", name)
	Expect(os.WriteFile(file, []byte(yaml), 0644)).To(Succeed())

	cmd := exec.Command("kubectl", "apply", "-f", file)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())

	By(fmt.Sprintf("waiting for %s to report %d ready replicas", name, replicas))
	Eventually(func(g Gomega) {
		g.Expect(getDeploymentReadyReplicas(namespace, name)).To(Equal(fmt.Sprintf("%d", replicas)))
	}, 3*time.Minute, 5*time.Second).Should(Succeed())
}

func getDeploymentReadyReplicas(namespace, name string) string {
	cmd := exec.Command("kubectl", "get", "deployment", name, "-n", namespace,
		"-o", "jsonpath={.status.readyReplicas}")
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	return output
}

// getPodCount returns how many pods of a deployment still exist, terminating ones
// included. A downscale wave settles when this reaches zero. A replica count cannot show
// that, because scaling to zero writes the spec and leaves the pods to shut down.
func getPodCount(namespace, app string) int {
	cmd := exec.Command("kubectl", "get", "pods", "-n", namespace,
		"-l", fmt.Sprintf("app=%s", app), "-o", "jsonpath={.items[*].metadata.name}")
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	return len(strings.Fields(output))
}
