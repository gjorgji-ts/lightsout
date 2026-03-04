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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gjorgji-ts/lightsout/test/utils"
)

var _ = Describe("LightsOutNamespaceSchedule", Ordered, func() {
	Context("Basic Scaling", func() {
		const (
			testNamespace = "test-ns-schedule-basic"
			scheduleName  = "ns-basic-schedule"
		)

		BeforeAll(func() {
			By("creating test namespace")
			cmd := exec.Command("kubectl", "create", "ns", testNamespace)
			_, _ = utils.Run(cmd)

			By("creating test deployment")
			createDeployment(testNamespace, "ns-basic-app", 3)
		})

		AfterAll(func() {
			cmd := exec.Command("kubectl", "delete", "lightsoutnamespaceschedule", scheduleName, "-n", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "ns", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should scale down workloads in the schedule's own namespace", func() {
			By("creating a LightsOutNamespaceSchedule in downscale period")
			scheduleYAML := fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutNamespaceSchedule
metadata:
  name: %s
  namespace: %s
spec:
  upscale: "0 0 31 12 *"
  downscale: "0 0 1 1 *"
  timezone: "UTC"
`, scheduleName, testNamespace)

			scheduleFile := "/tmp/test-ns-basic.yaml"
			err := os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			cmd := exec.Command("kubectl", "apply", "-f", scheduleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for deployment to be scaled down")
			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "ns-basic-app")
				g.Expect(replicas).To(Equal("0"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying annotations are set correctly")
			verifyDeploymentAnnotation(testNamespace, "ns-basic-app", "lightsout.techsupport.mk/original-replicas", "3")
			verifyDeploymentAnnotation(testNamespace, "ns-basic-app", "lightsout.techsupport.mk/managed-by", scheduleName)

			By("verifying schedule status is Down")
			Eventually(func(g Gomega) {
				state := getNamespaceScheduleField(testNamespace, scheduleName, "{.status.state}")
				g.Expect(state).To(Equal("Down"))
			}, 30*time.Second, 5*time.Second).Should(Succeed())
		})

		It("should restore workloads when schedule moves to upscale period", func() {
			By("updating schedule to upscale period")
			scheduleYAML := fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutNamespaceSchedule
metadata:
  name: %s
  namespace: %s
spec:
  upscale: "0 0 1 1 *"
  downscale: "0 0 31 12 *"
  timezone: "UTC"
`, scheduleName, testNamespace)

			scheduleFile := "/tmp/test-ns-basic.yaml"
			err := os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			cmd := exec.Command("kubectl", "apply", "-f", scheduleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for deployment to be restored")
			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "ns-basic-app")
				g.Expect(replicas).To(Equal("3"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying annotations are removed")
			verifyDeploymentAnnotationMissing(testNamespace, "ns-basic-app", "lightsout.techsupport.mk/original-replicas")
			verifyDeploymentAnnotationMissing(testNamespace, "ns-basic-app", "lightsout.techsupport.mk/managed-by")

			By("verifying schedule status is Up")
			Eventually(func(g Gomega) {
				state := getNamespaceScheduleField(testNamespace, scheduleName, "{.status.state}")
				g.Expect(state).To(Equal("Up"))
			}, 30*time.Second, 5*time.Second).Should(Succeed())
		})
	})

	// Global Schedule Precedence tests that when a LightsOutNamespaceSchedule exists in a namespace,
	// a LightsOutSchedule targeting that namespace via label selector skips it entirely.
	// The key assertion is the managed-by annotation value: it identifies which schedule
	// actually acted on each workload, proving the skipping logic worked.
	Context("Global Schedule Precedence", func() {
		const (
			nsWithLocal    = "test-ns-precedence-local"
			nsWithoutLocal = "test-ns-precedence-global"
			globalName     = "test-precedence-global"
			localName      = "test-precedence-local"
			scheduleNs     = "lightsout-system"
		)

		BeforeAll(func() {
			By("creating test namespaces with shared label")
			createNamespaceWithLabels(nsWithLocal, map[string]string{"precedence-test": "true"})
			createNamespaceWithLabels(nsWithoutLocal, map[string]string{"precedence-test": "true"})

			By("creating deployments in both namespaces")
			createDeployment(nsWithLocal, "local-app", 2)
			createDeployment(nsWithoutLocal, "global-app", 2)
		})

		AfterAll(func() {
			cmd := exec.Command("kubectl", "delete", "lightsoutschedule", globalName, "-n", scheduleNs, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "lightsoutnamespaceschedule", localName, "-n", nsWithLocal, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "ns", nsWithLocal, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "ns", nsWithoutLocal, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should skip namespaces that have a local schedule and not overwrite their managed-by annotation", func() {
			By("creating a LightsOutNamespaceSchedule in the first namespace (downscale)")
			localYAML := fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutNamespaceSchedule
metadata:
  name: %s
  namespace: %s
spec:
  upscale: "0 0 31 12 *"
  downscale: "0 0 1 1 *"
  timezone: "UTC"
`, localName, nsWithLocal)

			err := os.WriteFile("/tmp/test-precedence-local.yaml", []byte(localYAML), 0644)
			Expect(err).NotTo(HaveOccurred())
			cmd := exec.Command("kubectl", "apply", "-f", "/tmp/test-precedence-local.yaml")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("creating a LightsOutSchedule targeting both namespaces via label selector (downscale)")
			globalYAML := fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutSchedule
metadata:
  name: %s
  namespace: %s
spec:
  upscale: "0 0 31 12 *"
  downscale: "0 0 1 1 *"
  timezone: "UTC"
  namespaceSelector:
    matchLabels:
      precedence-test: "true"
`, globalName, scheduleNs)

			err = os.WriteFile("/tmp/test-precedence-global.yaml", []byte(globalYAML), 0644)
			Expect(err).NotTo(HaveOccurred())
			cmd = exec.Command("kubectl", "apply", "-f", "/tmp/test-precedence-global.yaml")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for the global-only namespace deployment to be scaled down")
			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(nsWithoutLocal, "global-app")
				g.Expect(replicas).To(Equal("0"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying global-app is managed by the global schedule")
			verifyDeploymentAnnotation(nsWithoutLocal, "global-app", "lightsout.techsupport.mk/managed-by", globalName)

			By("waiting for the local-schedule namespace deployment to be scaled down by the namespace schedule")
			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(nsWithLocal, "local-app")
				g.Expect(replicas).To(Equal("0"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying local-app is managed by the namespace schedule, not the global schedule")
			verifyDeploymentAnnotation(nsWithLocal, "local-app", "lightsout.techsupport.mk/managed-by", localName)

			By("verifying the global schedule status omits the namespace that has a local schedule")
			Eventually(func(g Gomega) {
				nsList := getScheduleField(scheduleNs, globalName, "{.status.namespaces}")
				g.Expect(nsList).To(ContainSubstring(nsWithoutLocal))
				g.Expect(nsList).NotTo(ContainSubstring(nsWithLocal))
			}, 30*time.Second, 5*time.Second).Should(Succeed())
		})
	})

	// Global Ownership Transfer tests that when a LightsOutNamespaceSchedule is created in a
	// namespace where a global LightsOutSchedule has already scaled workloads down, the namespace
	// schedule claims ownership of those workloads and can restore them independently.
	// This is the core "namespace overrides global" precedence guarantee for the transition case.
	Context("Global Ownership Transfer", func() {
		const (
			testNamespace = "test-ns-ownership-transfer"
			globalName    = "test-ownership-global"
			localName     = "test-ownership-local"
			scheduleNs    = "lightsout-system"
		)

		BeforeAll(func() {
			By("creating test namespace with label for global schedule targeting")
			createNamespaceWithLabels(testNamespace, map[string]string{"ownership-transfer-test": "true"})

			By("creating test deployment")
			createDeployment(testNamespace, "ownership-app", 3)
		})

		AfterAll(func() {
			cmd := exec.Command("kubectl", "delete", "lightsoutschedule", globalName, "-n", scheduleNs, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "lightsoutnamespaceschedule", localName, "-n", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "ns", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should transfer ownership from global to namespace schedule and restore workloads", func() {
			By("creating a LightsOutSchedule targeting the namespace in downscale period")
			globalYAML := fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutSchedule
metadata:
  name: %s
  namespace: %s
spec:
  upscale: "0 0 31 12 *"
  downscale: "0 0 1 1 *"
  timezone: "UTC"
  namespaceSelector:
    matchLabels:
      ownership-transfer-test: "true"
`, globalName, scheduleNs)

			err := os.WriteFile("/tmp/test-ownership-global.yaml", []byte(globalYAML), 0644)
			Expect(err).NotTo(HaveOccurred())
			cmd := exec.Command("kubectl", "apply", "-f", "/tmp/test-ownership-global.yaml")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for deployment to be scaled down by the global schedule")
			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "ownership-app")
				g.Expect(replicas).To(Equal("0"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying deployment is owned by the global schedule")
			verifyDeploymentAnnotation(testNamespace, "ownership-app", "lightsout.techsupport.mk/managed-by", globalName)
			verifyDeploymentAnnotation(testNamespace, "ownership-app", "lightsout.techsupport.mk/original-replicas", "3")

			By("creating a LightsOutNamespaceSchedule in upscale period — this overrides the global schedule")
			localYAML := fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutNamespaceSchedule
metadata:
  name: %s
  namespace: %s
spec:
  upscale: "0 0 1 1 *"
  downscale: "0 0 31 12 *"
  timezone: "UTC"
`, localName, testNamespace)

			err = os.WriteFile("/tmp/test-ownership-local.yaml", []byte(localYAML), 0644)
			Expect(err).NotTo(HaveOccurred())
			cmd = exec.Command("kubectl", "apply", "-f", "/tmp/test-ownership-local.yaml")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for deployment to be restored by the namespace schedule")
			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "ownership-app")
				g.Expect(replicas).To(Equal("3"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying the namespace schedule fully released ownership annotations")
			verifyDeploymentAnnotationMissing(testNamespace, "ownership-app", "lightsout.techsupport.mk/managed-by")
			verifyDeploymentAnnotationMissing(testNamespace, "ownership-app", "lightsout.techsupport.mk/original-replicas")
		})
	})

	Context("Deletion Cleanup", func() {
		const (
			testNamespace = "test-ns-schedule-deletion"
			scheduleName  = "ns-deletion-schedule"
		)

		BeforeAll(func() {
			By("creating test namespace")
			cmd := exec.Command("kubectl", "create", "ns", testNamespace)
			_, _ = utils.Run(cmd)

			By("creating test deployments")
			createDeployment(testNamespace, "ns-del-app-1", 2)
			createDeployment(testNamespace, "ns-del-app-2", 3)
		})

		AfterAll(func() {
			cmd := exec.Command("kubectl", "delete", "ns", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should restore all workloads when the namespace schedule is deleted", func() {
			By("creating a LightsOutNamespaceSchedule in downscale period")
			scheduleYAML := fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutNamespaceSchedule
metadata:
  name: %s
  namespace: %s
spec:
  upscale: "0 0 31 12 *"
  downscale: "0 0 1 1 *"
  timezone: "UTC"
`, scheduleName, testNamespace)

			scheduleFile := "/tmp/test-ns-deletion.yaml"
			err := os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			cmd := exec.Command("kubectl", "apply", "-f", scheduleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for deployments to be scaled down")
			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "ns-del-app-1")
				g.Expect(replicas).To(Equal("0"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "ns-del-app-2")
				g.Expect(replicas).To(Equal("0"))
			}, 30*time.Second, 5*time.Second).Should(Succeed())

			By("deleting the LightsOutNamespaceSchedule")
			cmd = exec.Command("kubectl", "delete", "lightsoutnamespaceschedule", scheduleName, "-n", testNamespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for deployments to be restored to original replicas")
			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "ns-del-app-1")
				g.Expect(replicas).To(Equal("2"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "ns-del-app-2")
				g.Expect(replicas).To(Equal("3"))
			}, 30*time.Second, 5*time.Second).Should(Succeed())

			By("verifying annotations are removed after cleanup")
			verifyDeploymentAnnotationMissing(testNamespace, "ns-del-app-1", "lightsout.techsupport.mk/managed-by")
			verifyDeploymentAnnotationMissing(testNamespace, "ns-del-app-1", "lightsout.techsupport.mk/original-replicas")
			verifyDeploymentAnnotationMissing(testNamespace, "ns-del-app-2", "lightsout.techsupport.mk/managed-by")
			verifyDeploymentAnnotationMissing(testNamespace, "ns-del-app-2", "lightsout.techsupport.mk/original-replicas")

			By("verifying the schedule is actually deleted")
			cmd = exec.Command("kubectl", "get", "lightsoutnamespaceschedule", scheduleName, "-n", testNamespace)
			_, err = utils.Run(cmd)
			Expect(err).To(HaveOccurred(), "Schedule should be deleted")
		})
	})
})

func getNamespaceScheduleField(namespace, name, jsonpath string) string {
	cmd := exec.Command("kubectl", "get", "lightsoutnamespaceschedule", name, "-n", namespace,
		"-o", fmt.Sprintf("jsonpath=%s", jsonpath))
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	return output
}
