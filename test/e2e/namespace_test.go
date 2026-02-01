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

var _ = Describe("Namespace Targeting", Ordered, func() {
	const (
		scheduleNamespace = "lightsout-system"
	)

	Context("Namespace Selector", func() {
		const (
			nsTeamAlpha     = "ns-team-alpha"
			nsTeamBeta      = "ns-team-beta"
			nsTeamAlphaProd = "ns-team-alpha-prod"
			scheduleName    = "test-ns-selector"
		)

		BeforeAll(func() {
			By("creating test namespaces with different labels")
			createNamespaceWithLabels(nsTeamAlpha, map[string]string{"team": "alpha"})
			createNamespaceWithLabels(nsTeamBeta, map[string]string{"team": "beta"})
			createNamespaceWithLabels(nsTeamAlphaProd, map[string]string{"team": "alpha", "env": "prod"})

			By("creating deployments in each namespace")
			createDeployment(nsTeamAlpha, "app-alpha", 2)
			createDeployment(nsTeamBeta, "app-beta", 2)
			createDeployment(nsTeamAlphaProd, "app-alpha-prod", 2)
		})

		AfterAll(func() {
			By("cleaning up test resources")
			cmd := exec.Command("kubectl", "delete", "lightsoutschedule", scheduleName, "-n", scheduleNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "ns", nsTeamAlpha, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "ns", nsTeamBeta, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "ns", nsTeamAlphaProd, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should only scale workloads in namespaces matching the selector", func() {
			By("creating a LightsOutSchedule with namespace selector for team=alpha")
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
  namespaceSelector:
    matchLabels:
      team: alpha
`, scheduleName, scheduleNamespace)

			scheduleFile := "/tmp/test-ns-selector.yaml"
			err := os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			cmd := exec.Command("kubectl", "apply", "-f", scheduleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for alpha team deployments to be scaled down")
			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(nsTeamAlpha, "app-alpha")
				g.Expect(replicas).To(Equal("0"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(nsTeamAlphaProd, "app-alpha-prod")
				g.Expect(replicas).To(Equal("0"))
			}, 30*time.Second, 5*time.Second).Should(Succeed())

			By("verifying beta team deployment is NOT scaled")
			Consistently(func(g Gomega) {
				replicas := getDeploymentReplicas(nsTeamBeta, "app-beta")
				g.Expect(replicas).To(Equal("2"))
			}, 15*time.Second, 5*time.Second).Should(Succeed())

			By("verifying beta deployment has no lightsout annotations")
			verifyDeploymentAnnotationMissing(nsTeamBeta, "app-beta", "lightsout.techsupport.mk/managed-by")

			By("verifying schedule status lists correct namespaces")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "lightsoutschedule", scheduleName, "-n", scheduleNamespace,
					"-o", "jsonpath={.status.namespaces}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring(nsTeamAlpha))
				g.Expect(output).To(ContainSubstring(nsTeamAlphaProd))
				g.Expect(output).NotTo(ContainSubstring(nsTeamBeta))
			}, 30*time.Second, 5*time.Second).Should(Succeed())
		})
	})

	Context("Explicit Namespaces List", func() {
		const (
			nsIncluded    = "ns-included"
			nsNotIncluded = "ns-not-included"
			scheduleName  = "test-explicit-ns"
		)

		BeforeAll(func() {
			By("creating test namespaces")
			cmd := exec.Command("kubectl", "create", "ns", nsIncluded)
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "create", "ns", nsNotIncluded)
			_, _ = utils.Run(cmd)

			By("creating deployments in each namespace")
			createDeployment(nsIncluded, "included-app", 2)
			createDeployment(nsNotIncluded, "not-included-app", 2)
		})

		AfterAll(func() {
			By("cleaning up test resources")
			cmd := exec.Command("kubectl", "delete", "lightsoutschedule", scheduleName, "-n", scheduleNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "ns", nsIncluded, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "ns", nsNotIncluded, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should only scale workloads in explicitly listed namespaces", func() {
			By("creating a LightsOutSchedule with explicit namespace list")
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
`, scheduleName, scheduleNamespace, nsIncluded)

			scheduleFile := "/tmp/test-explicit-ns.yaml"
			err := os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			cmd := exec.Command("kubectl", "apply", "-f", scheduleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for included namespace deployment to be scaled down")
			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(nsIncluded, "included-app")
				g.Expect(replicas).To(Equal("0"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying not-included namespace deployment is NOT scaled")
			Consistently(func(g Gomega) {
				replicas := getDeploymentReplicas(nsNotIncluded, "not-included-app")
				g.Expect(replicas).To(Equal("2"))
			}, 15*time.Second, 5*time.Second).Should(Succeed())
		})
	})

	Context("Exclude Namespaces", func() {
		const (
			nsDev1       = "ns-dev-1"
			nsDev2       = "ns-dev-2"
			scheduleName = "test-exclude-ns"
		)

		BeforeAll(func() {
			By("creating test namespaces with same label")
			createNamespaceWithLabels(nsDev1, map[string]string{"env": "dev"})
			createNamespaceWithLabels(nsDev2, map[string]string{"env": "dev"})

			By("creating deployments in each namespace")
			createDeployment(nsDev1, "dev-app-1", 2)
			createDeployment(nsDev2, "dev-app-2", 2)
		})

		AfterAll(func() {
			By("cleaning up test resources")
			cmd := exec.Command("kubectl", "delete", "lightsoutschedule", scheduleName, "-n", scheduleNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "ns", nsDev1, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "ns", nsDev2, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should exclude specified namespaces from scaling", func() {
			By("creating a LightsOutSchedule with namespace selector and exclusion")
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
  namespaceSelector:
    matchLabels:
      env: dev
  excludeNamespaces:
    - %s
`, scheduleName, scheduleNamespace, nsDev2)

			scheduleFile := "/tmp/test-exclude-ns.yaml"
			err := os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			cmd := exec.Command("kubectl", "apply", "-f", scheduleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for dev-1 namespace deployment to be scaled down")
			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(nsDev1, "dev-app-1")
				g.Expect(replicas).To(Equal("0"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying excluded dev-2 namespace deployment is NOT scaled")
			Consistently(func(g Gomega) {
				replicas := getDeploymentReplicas(nsDev2, "dev-app-2")
				g.Expect(replicas).To(Equal("2"))
			}, 15*time.Second, 5*time.Second).Should(Succeed())

			By("verifying excluded deployment has no lightsout annotations")
			verifyDeploymentAnnotationMissing(nsDev2, "dev-app-2", "lightsout.techsupport.mk/managed-by")
		})
	})
})

// Helper function for creating namespaces with labels
func createNamespaceWithLabels(name string, labels map[string]string) {
	cmd := exec.Command("kubectl", "create", "ns", name)
	_, _ = utils.Run(cmd) // Ignore error if exists

	for key, value := range labels {
		cmd = exec.Command("kubectl", "label", "ns", name, fmt.Sprintf("%s=%s", key, value), "--overwrite")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
	}
}
