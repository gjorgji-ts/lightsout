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

var _ = Describe("Timezone Handling", Ordered, func() {
	const scheduleNamespace = "lightsout-system"

	Context("Default Timezone", func() {
		const (
			testNamespace = "test-tz-default"
			scheduleName  = "test-tz-default"
		)

		BeforeAll(func() {
			By("creating test namespace")
			cmd := exec.Command("kubectl", "create", "ns", testNamespace)
			_, _ = utils.Run(cmd)

			By("creating test deployment")
			createDeployment(testNamespace, "tz-default-app", 2)

			By("waiting for deployment to be available")
			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "tz-default-app")
				g.Expect(replicas).To(Equal("2"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())
		})

		AfterAll(func() {
			By("cleaning up test resources")
			cmd := exec.Command("kubectl", "delete", "lightsoutschedule", scheduleName,
				"-n", scheduleNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "ns", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should default empty timezone to UTC via the mutating webhook", func() {
			By("creating a schedule with no timezone field")
			scheduleYAML := fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutSchedule
metadata:
  name: %s
  namespace: %s
spec:
  upscale: "0 0 31 12 *"
  downscale: "0 0 1 1 *"
  namespaces:
    - %s
`, scheduleName, scheduleNamespace, testNamespace)

			scheduleFile := "/tmp/test-tz-default.yaml"
			err := os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			cmd := exec.Command("kubectl", "apply", "-f", scheduleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying the mutating webhook defaulted timezone to UTC")
			Eventually(func(g Gomega) {
				tz := getScheduleField(scheduleNamespace, scheduleName, "{.spec.timezone}")
				g.Expect(tz).To(Equal("UTC"))
			}, 30*time.Second, 5*time.Second).Should(Succeed())

			By("verifying the schedule correctly enters downscale period under UTC")
			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "tz-default-app")
				g.Expect(replicas).To(Equal("0"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())
		})
	})

	Context("Named Timezone", func() {
		const (
			testNamespace = "test-tz-named"
			scheduleName  = "test-tz-named"
		)

		BeforeAll(func() {
			By("creating test namespace")
			cmd := exec.Command("kubectl", "create", "ns", testNamespace)
			_, _ = utils.Run(cmd)

			By("creating test deployment")
			createDeployment(testNamespace, "tz-named-app", 2)

			By("waiting for deployment to be available")
			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "tz-named-app")
				g.Expect(replicas).To(Equal("2"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())
		})

		AfterAll(func() {
			By("cleaning up test resources")
			cmd := exec.Command("kubectl", "delete", "lightsoutschedule", scheduleName,
				"-n", scheduleNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "ns", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should correctly apply a schedule using a non-UTC named timezone", func() {
			By("creating a schedule with America/New_York timezone in downscale period")
			scheduleYAML := fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutSchedule
metadata:
  name: %s
  namespace: %s
spec:
  upscale: "0 0 31 12 *"
  downscale: "0 0 1 1 *"
  timezone: "America/New_York"
  namespaces:
    - %s
`, scheduleName, scheduleNamespace, testNamespace)

			scheduleFile := "/tmp/test-tz-named.yaml"
			err := os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			cmd := exec.Command("kubectl", "apply", "-f", scheduleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying the timezone field is stored correctly")
			Eventually(func(g Gomega) {
				tz := getScheduleField(scheduleNamespace, scheduleName, "{.spec.timezone}")
				g.Expect(tz).To(Equal("America/New_York"))
			}, 30*time.Second, 5*time.Second).Should(Succeed())

			By("verifying the schedule correctly scales down workloads under America/New_York timezone")
			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "tz-named-app")
				g.Expect(replicas).To(Equal("0"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())
		})

		It("should correctly upscale when the schedule switches to an upscale period", func() {
			By("updating the schedule to an upscale period (still with America/New_York timezone)")
			scheduleYAML := fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutSchedule
metadata:
  name: %s
  namespace: %s
spec:
  upscale: "0 0 1 1 *"
  downscale: "0 0 31 12 *"
  timezone: "America/New_York"
  namespaces:
    - %s
`, scheduleName, scheduleNamespace, testNamespace)

			scheduleFile := "/tmp/test-tz-named.yaml"
			err := os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			cmd := exec.Command("kubectl", "apply", "-f", scheduleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying the deployment is restored to original replicas")
			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "tz-named-app")
				g.Expect(replicas).To(Equal("2"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())
		})
	})
})
