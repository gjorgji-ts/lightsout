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

var _ = Describe("Schedule Lifecycle", Ordered, func() {
	const (
		scheduleNamespace = "lightsout-system"
	)

	Context("Schedule Deletion Cleanup", func() {
		const (
			testNamespace = "test-deletion-cleanup"
			scheduleName  = "test-deletion"
		)

		BeforeAll(func() {
			By("creating test namespace")
			cmd := exec.Command("kubectl", "create", "ns", testNamespace)
			_, _ = utils.Run(cmd)

			By("creating test deployments")
			createDeployment(testNamespace, "cleanup-deploy-1", 2)
			createDeployment(testNamespace, "cleanup-deploy-2", 3)
		})

		AfterAll(func() {
			By("cleaning up test namespace")
			cmd := exec.Command("kubectl", "delete", "ns", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should restore all workloads when schedule is deleted", func() {
			By("creating a LightsOutSchedule in downscale period")
			scheduleYAML := fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutSchedule
metadata:
  name: %s
  namespace: %s
spec:
  upscale: "0 0 31 12 *"
  downscale: "0 0 1 1 *"
  timezone: "UTC"
  namespaces:
    - %s
`, scheduleName, scheduleNamespace, testNamespace)

			scheduleFile := "/tmp/test-deletion.yaml"
			err := os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			cmd := exec.Command("kubectl", "apply", "-f", scheduleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for deployments to be scaled down")
			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "cleanup-deploy-1")
				g.Expect(replicas).To(Equal("0"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "cleanup-deploy-2")
				g.Expect(replicas).To(Equal("0"))
			}, 30*time.Second, 5*time.Second).Should(Succeed())

			By("verifying deployments have managed-by annotations")
			verifyDeploymentAnnotation(testNamespace, "cleanup-deploy-1", "lightsout.techsupport.mk/managed-by", scheduleName)
			verifyDeploymentAnnotation(testNamespace, "cleanup-deploy-2", "lightsout.techsupport.mk/managed-by", scheduleName)

			By("verifying deployments have managed-by labels")
			verifyDeploymentLabel(testNamespace, "cleanup-deploy-1", "lightsout.techsupport.mk/managed-by", scheduleName)
			verifyDeploymentLabel(testNamespace, "cleanup-deploy-2", "lightsout.techsupport.mk/managed-by", scheduleName)

			By("deleting the LightsOutSchedule")
			cmd = exec.Command("kubectl", "delete", "lightsoutschedule", scheduleName, "-n", scheduleNamespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for deployments to be restored to original replicas")
			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "cleanup-deploy-1")
				g.Expect(replicas).To(Equal("2"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "cleanup-deploy-2")
				g.Expect(replicas).To(Equal("3"))
			}, 30*time.Second, 5*time.Second).Should(Succeed())

			By("verifying annotations are removed after cleanup")
			verifyDeploymentAnnotationMissing(testNamespace, "cleanup-deploy-1", "lightsout.techsupport.mk/managed-by")
			verifyDeploymentAnnotationMissing(testNamespace, "cleanup-deploy-1", "lightsout.techsupport.mk/original-replicas")
			verifyDeploymentAnnotationMissing(testNamespace, "cleanup-deploy-2", "lightsout.techsupport.mk/managed-by")
			verifyDeploymentAnnotationMissing(testNamespace, "cleanup-deploy-2", "lightsout.techsupport.mk/original-replicas")

			By("verifying labels are removed after cleanup")
			verifyDeploymentLabelMissing(testNamespace, "cleanup-deploy-1", "lightsout.techsupport.mk/managed-by")
			verifyDeploymentLabelMissing(testNamespace, "cleanup-deploy-2", "lightsout.techsupport.mk/managed-by")

			By("verifying the schedule is actually deleted")
			cmd = exec.Command("kubectl", "get", "lightsoutschedule", scheduleName, "-n", scheduleNamespace)
			_, err = utils.Run(cmd)
			Expect(err).To(HaveOccurred(), "Schedule should be deleted")
		})
	})

	Context("Suspend Schedule", func() {
		const (
			testNamespace = "test-suspend-schedule"
			scheduleName  = "test-suspend"
		)

		BeforeAll(func() {
			By("creating test namespace")
			cmd := exec.Command("kubectl", "create", "ns", testNamespace)
			_, _ = utils.Run(cmd)

			By("creating test deployment")
			createDeployment(testNamespace, "suspend-test-app", 2)
		})

		AfterAll(func() {
			By("cleaning up test resources")
			cmd := exec.Command("kubectl", "delete", "lightsoutschedule", scheduleName, "-n", scheduleNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "ns", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should not perform scaling when schedule is suspended", func() {
			By("creating a LightsOutSchedule in downscale period")
			scheduleYAML := fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutSchedule
metadata:
  name: %s
  namespace: %s
spec:
  upscale: "0 0 31 12 *"
  downscale: "0 0 1 1 *"
  timezone: "UTC"
  namespaces:
    - %s
`, scheduleName, scheduleNamespace, testNamespace)

			scheduleFile := "/tmp/test-suspend.yaml"
			err := os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			cmd := exec.Command("kubectl", "apply", "-f", scheduleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for deployment to be scaled down")
			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "suspend-test-app")
				g.Expect(replicas).To(Equal("0"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("suspending the schedule")
			scheduleYAML = fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutSchedule
metadata:
  name: %s
  namespace: %s
spec:
  upscale: "0 0 31 12 *"
  downscale: "0 0 1 1 *"
  timezone: "UTC"
  suspend: true
  namespaces:
    - %s
`, scheduleName, scheduleNamespace, testNamespace)

			err = os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			cmd = exec.Command("kubectl", "apply", "-f", scheduleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying deployment stays at 0 even though schedule is in upscale period")
			// The schedule is now in upscale period but suspended, so no scaling should happen
			Consistently(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "suspend-test-app")
				g.Expect(replicas).To(Equal("0"))
			}, 20*time.Second, 5*time.Second).Should(Succeed())

			By("unsuspending the schedule")
			scheduleYAML = fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutSchedule
metadata:
  name: %s
  namespace: %s
spec:
  upscale: "0 0 1 1 *"
  downscale: "0 0 31 12 *"
  timezone: "UTC"
  suspend: false
  namespaces:
    - %s
`, scheduleName, scheduleNamespace, testNamespace)

			err = os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			cmd = exec.Command("kubectl", "apply", "-f", scheduleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying deployment is now scaled up")
			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "suspend-test-app")
				g.Expect(replicas).To(Equal("2"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())
		})
	})

	Context("Status Updates", func() {
		const (
			testNamespace = "test-status-updates"
			scheduleName  = "test-status"
		)

		BeforeAll(func() {
			By("creating test namespace")
			cmd := exec.Command("kubectl", "create", "ns", testNamespace)
			_, _ = utils.Run(cmd)

			By("creating test workloads")
			createDeployment(testNamespace, "status-deploy", 2)
			createStatefulSet(testNamespace, "status-sts", 1)
			createCronJob(testNamespace, "status-cj", false)
		})

		AfterAll(func() {
			By("cleaning up test resources")
			cmd := exec.Command("kubectl", "delete", "lightsoutschedule", scheduleName, "-n", scheduleNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "ns", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should correctly populate all status fields", func() {
			By("creating a LightsOutSchedule")
			scheduleYAML := fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutSchedule
metadata:
  name: %s
  namespace: %s
spec:
  upscale: "0 0 31 12 *"
  downscale: "0 0 1 1 *"
  timezone: "UTC"
  namespaces:
    - %s
`, scheduleName, scheduleNamespace, testNamespace)

			scheduleFile := "/tmp/test-status.yaml"
			err := os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			cmd := exec.Command("kubectl", "apply", "-f", scheduleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for reconciliation")
			Eventually(func(g Gomega) {
				state := getScheduleField(scheduleNamespace, scheduleName, "{.status.state}")
				g.Expect(state).To(Equal("Down"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying status.state is set")
			state := getScheduleField(scheduleNamespace, scheduleName, "{.status.state}")
			Expect(state).To(Equal("Down"))

			By("verifying status.nextUpscaleTime is set")
			nextUpscale := getScheduleField(scheduleNamespace, scheduleName, "{.status.nextUpscaleTime}")
			Expect(nextUpscale).NotTo(BeEmpty())

			By("verifying status.nextDownscaleTime is set")
			nextDownscale := getScheduleField(scheduleNamespace, scheduleName, "{.status.nextDownscaleTime}")
			Expect(nextDownscale).NotTo(BeEmpty())

			By("verifying status.namespaces lists the correct namespace")
			namespaces := getScheduleField(scheduleNamespace, scheduleName, "{.status.namespaces}")
			Expect(namespaces).To(ContainSubstring(testNamespace))

			By("verifying status.workloadStats shows correct counts")
			deploymentsManaged := getScheduleField(scheduleNamespace, scheduleName, "{.status.workloadStats.deploymentsManaged}")
			Expect(deploymentsManaged).To(Equal("1"))

			statefulsetsManaged := getScheduleField(scheduleNamespace, scheduleName, "{.status.workloadStats.statefulsetsManaged}")
			Expect(statefulsetsManaged).To(Equal("1"))

			cronjobsManaged := getScheduleField(scheduleNamespace, scheduleName, "{.status.workloadStats.cronjobsManaged}")
			Expect(cronjobsManaged).To(Equal("1"))

			By("verifying status.conditions contains Ready condition")
			cmd = exec.Command("kubectl", "get", "lightsoutschedule", scheduleName, "-n", scheduleNamespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("True"))
		})
	})
})
