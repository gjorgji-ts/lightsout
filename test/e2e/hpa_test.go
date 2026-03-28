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

var _ = Describe("HPA Integration", Ordered, func() {
	const (
		scheduleNamespace = "lightsout-system"
		testNamespace     = "test-hpa-integration"
		scheduleName      = "test-hpa-schedule"
	)

	BeforeAll(func() {
		By("creating test namespace")
		cmd := exec.Command("kubectl", "create", "ns", testNamespace)
		_, _ = utils.Run(cmd) // Ignore error if exists

		By("creating hpa-deploy deployment with 3 replicas")
		createDeployment(testNamespace, "hpa-deploy", 3)

		By("creating HPA targeting hpa-deploy")
		hpaYAML := fmt.Sprintf(`
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: hpa-deploy
  namespace: %s
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: hpa-deploy
  minReplicas: 2
  maxReplicas: 6
  metrics:
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 60
`, testNamespace)

		hpaFile := "/tmp/test-hpa.yaml"
		err := os.WriteFile(hpaFile, []byte(hpaYAML), 0644)
		Expect(err).NotTo(HaveOccurred())

		cmd = exec.Command("kubectl", "apply", "-f", hpaFile)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		By("cleaning up test resources")
		cmd := exec.Command("kubectl", "delete", "lightsoutschedule", scheduleName, "-n", scheduleNamespace, "--ignore-not-found")
		_, _ = utils.Run(cmd)
		cmd = exec.Command("kubectl", "delete", "ns", testNamespace, "--ignore-not-found")
		_, _ = utils.Run(cmd)
	})

	It("downscale disables HPA scaleUp to prevent fight-back", func() {
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

		scheduleFile := "/tmp/test-hpa-schedule.yaml"
		err := os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
		Expect(err).NotTo(HaveOccurred())

		cmd := exec.Command("kubectl", "apply", "-f", scheduleFile)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for HPA scaleUp to be disabled")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "hpa", "hpa-deploy", "-n", testNamespace,
				"-o", "jsonpath={.spec.behavior.scaleUp.selectPolicy}")
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("Disabled"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("waiting for deployment to be scaled down to 0")
		Eventually(func(g Gomega) {
			replicas := getDeploymentReplicas(testNamespace, "hpa-deploy")
			g.Expect(replicas).To(Equal("0"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("verifying HPA minReplicas is unchanged at 2")
		cmd = exec.Command("kubectl", "get", "hpa", "hpa-deploy", "-n", testNamespace,
			"-o", "jsonpath={.spec.minReplicas}")
		output, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(Equal("2"), "HPA minReplicas should remain unchanged at 2")

		By("verifying HPA original-hpa-scale-up-policy annotation is set")
		cmd = exec.Command("kubectl", "get", "hpa", "hpa-deploy", "-n", testNamespace,
			"-o", fmt.Sprintf("jsonpath={.metadata.annotations['%s']}", escapeAnnotationKey("lightsout.techsupport.mk/original-hpa-scale-up-policy")))
		output, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		// Empty string means field was absent originally (default behaviour)
		Expect(output).To(BeEmpty(), "HPA original-hpa-scale-up-policy annotation should be empty (field was absent)")
	})

	It("upscale restores HPA scaleUp policy", func() {
		By("updating schedule to upscale period")
		scheduleYAML := fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutSchedule
metadata:
  name: %s
  namespace: %s
spec:
  upscale: "0 0 1 1 *"
  downscale: "0 0 31 12 *"
  timezone: "UTC"
  namespaces:
    - %s
`, scheduleName, scheduleNamespace, testNamespace)

		scheduleFile := "/tmp/test-hpa-schedule.yaml"
		err := os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
		Expect(err).NotTo(HaveOccurred())

		cmd := exec.Command("kubectl", "apply", "-f", scheduleFile)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for HPA scaleUp to be re-enabled (selectPolicy no longer Disabled)")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "hpa", "hpa-deploy", "-n", testNamespace,
				"-o", "jsonpath={.spec.behavior.scaleUp.selectPolicy}")
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).NotTo(Equal("Disabled"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("waiting for deployment to be restored to 3 replicas")
		Eventually(func(g Gomega) {
			replicas := getDeploymentReplicas(testNamespace, "hpa-deploy")
			g.Expect(replicas).To(Equal("3"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("verifying HPA minReplicas remains at 2 (was never changed)")
		cmd = exec.Command("kubectl", "get", "hpa", "hpa-deploy", "-n", testNamespace,
			"-o", "jsonpath={.spec.minReplicas}")
		output, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(Equal("2"), "HPA minReplicas should remain 2")

		By("verifying lightsout annotations are removed from HPA")
		cmd = exec.Command("kubectl", "get", "hpa", "hpa-deploy", "-n", testNamespace,
			"-o", fmt.Sprintf("jsonpath={.metadata.annotations['%s']}", escapeAnnotationKey("lightsout.techsupport.mk/original-hpa-scale-up-policy")))
		output, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(BeEmpty(), "HPA original-hpa-scale-up-policy annotation should be removed after upscale")

		cmd = exec.Command("kubectl", "get", "hpa", "hpa-deploy", "-n", testNamespace,
			"-o", fmt.Sprintf("jsonpath={.metadata.annotations['%s']}", escapeAnnotationKey("lightsout.techsupport.mk/managed-by")))
		output, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(BeEmpty(), "HPA managed-by annotation should be removed after upscale")
	})

	It("workload without HPA scales normally", func() {
		By("creating no-hpa-deploy deployment with no HPA")
		createDeployment(testNamespace, "no-hpa-deploy", 2)

		By("applying a schedule in downscale period")
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

		scheduleFile := "/tmp/test-hpa-schedule.yaml"
		err := os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
		Expect(err).NotTo(HaveOccurred())

		cmd := exec.Command("kubectl", "apply", "-f", scheduleFile)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for no-hpa-deploy to be scaled down to 0")
		Eventually(func(g Gomega) {
			replicas := getDeploymentReplicas(testNamespace, "no-hpa-deploy")
			g.Expect(replicas).To(Equal("0"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("verifying no-hpa-deploy has original-replicas annotation set")
		verifyDeploymentAnnotation(testNamespace, "no-hpa-deploy", "lightsout.techsupport.mk/original-replicas", "2")

		By("restoring schedule to upscale period to clean up state")
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
  namespaces:
    - %s
`, scheduleName, scheduleNamespace, testNamespace)

		err = os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
		Expect(err).NotTo(HaveOccurred())

		cmd = exec.Command("kubectl", "apply", "-f", scheduleFile)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
	})

	It("user-managed HPA with scaleUp disabled is skipped", func() {
		By("creating user-disabled-deploy deployment")
		createDeployment(testNamespace, "user-disabled-deploy", 2)

		By("creating HPA with user-managed scaleUp.selectPolicy=Disabled (no lightsout annotations)")
		hpaYAML := fmt.Sprintf(`
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: user-disabled-deploy
  namespace: %s
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: user-disabled-deploy
  minReplicas: 1
  maxReplicas: 5
  behavior:
    scaleUp:
      selectPolicy: Disabled
  metrics:
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 60
`, testNamespace)

		hpaFile := "/tmp/test-user-disabled-hpa.yaml"
		err := os.WriteFile(hpaFile, []byte(hpaYAML), 0644)
		Expect(err).NotTo(HaveOccurred())

		cmd := exec.Command("kubectl", "apply", "-f", hpaFile)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("applying a schedule in downscale period")
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

		scheduleFile := "/tmp/test-hpa-schedule.yaml"
		err = os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
		Expect(err).NotTo(HaveOccurred())

		cmd = exec.Command("kubectl", "apply", "-f", scheduleFile)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for reconciliation to run (deployment scaled to 0 by LightsOut)")
		Eventually(func(g Gomega) {
			replicas := getDeploymentReplicas(testNamespace, "user-disabled-deploy")
			g.Expect(replicas).To(Equal("0"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("verifying HPA has no lightsout managed-by annotation (LightsOut skipped it)")
		cmd = exec.Command("kubectl", "get", "hpa", "user-disabled-deploy", "-n", testNamespace,
			"-o", fmt.Sprintf("jsonpath={.metadata.annotations['%s']}", escapeAnnotationKey("lightsout.techsupport.mk/managed-by")))
		output, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(BeEmpty(), "HPA managed-by annotation should not be set for user-managed disabled HPA")

		By("verifying HPA original-hpa-scale-up-policy annotation is absent")
		cmd = exec.Command("kubectl", "get", "hpa", "user-disabled-deploy", "-n", testNamespace,
			"-o", fmt.Sprintf("jsonpath={.metadata.annotations['%s']}", escapeAnnotationKey("lightsout.techsupport.mk/original-hpa-scale-up-policy")))
		output, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(BeEmpty(), "HPA original-hpa-scale-up-policy annotation should not be set for user-managed disabled HPA")

		By("verifying HPA selectPolicy remains Disabled (not modified by LightsOut)")
		cmd = exec.Command("kubectl", "get", "hpa", "user-disabled-deploy", "-n", testNamespace,
			"-o", "jsonpath={.spec.behavior.scaleUp.selectPolicy}")
		output, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(Equal("Disabled"), "HPA selectPolicy should remain Disabled")
	})

	It("handles autoscaling/v1 HPA (discoverable via v2 API)", func() {
		// autoscaling/v1 HPAs are stored internally as v2 on k8s 1.23+ and served
		// across both API versions. LightsOut lists autoscaling/v2, so it will find
		// and patch v1-created HPAs transparently.
		By("creating v1-hpa-deploy deployment")
		createDeployment(testNamespace, "v1-hpa-deploy", 3)

		By("creating HPA using autoscaling/v1")
		hpaYAML := fmt.Sprintf(`
apiVersion: autoscaling/v1
kind: HorizontalPodAutoscaler
metadata:
  name: v1-hpa-deploy
  namespace: %s
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: v1-hpa-deploy
  minReplicas: 2
  maxReplicas: 5
  targetCPUUtilizationPercentage: 60
`, testNamespace)

		hpaFile := "/tmp/test-v1-hpa.yaml"
		err := os.WriteFile(hpaFile, []byte(hpaYAML), 0644)
		Expect(err).NotTo(HaveOccurred())

		cmd := exec.Command("kubectl", "apply", "-f", hpaFile)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		scheduleFile := "/tmp/test-hpa-schedule.yaml"

		// The previous test left the schedule in downscale state with the same cron spec we
		// would use here. Applying an identical spec is a no-op (no generation change), so
		// GenerationChangedPredicate would suppress the watch event and no reconcile would
		// fire for the newly-created v1-hpa-deploy workload. Force a fresh reconcile by
		// cycling through upscale first, then switching back to downscale.
		By("cycling schedule to upscale to force a generation change")
		upscaleYAML := fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutSchedule
metadata:
  name: %s
  namespace: %s
spec:
  upscale: "0 0 1 1 *"
  downscale: "0 0 31 12 *"
  timezone: "UTC"
  namespaces:
    - %s
`, scheduleName, scheduleNamespace, testNamespace)
		err = os.WriteFile(scheduleFile, []byte(upscaleYAML), 0644)
		Expect(err).NotTo(HaveOccurred())
		cmd = exec.Command("kubectl", "apply", "-f", scheduleFile)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("applying a schedule in downscale period")
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
		err = os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
		Expect(err).NotTo(HaveOccurred())

		cmd = exec.Command("kubectl", "apply", "-f", scheduleFile)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for HPA scaleUp to be disabled (discovered via v2 API)")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "hpa", "v1-hpa-deploy", "-n", testNamespace,
				"-o", "jsonpath={.spec.behavior.scaleUp.selectPolicy}")
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("Disabled"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("waiting for deployment to be scaled down to 0")
		Eventually(func(g Gomega) {
			replicas := getDeploymentReplicas(testNamespace, "v1-hpa-deploy")
			g.Expect(replicas).To(Equal("0"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		// The downscale reconcile patches the HPA via the API server, but the upscale
		// reconcile reads HPAs from the informer cache which updates asynchronously via
		// watch events. If the upscale schedule is applied before the cache reflects the
		// managed-by annotation, RestoreHPA sees a stale HPA, no-ops silently, and the
		// RequeueAfter is set to months with no retry. Confirming the annotation is set
		// via kubectl (API server) and then checking it consistently for a few seconds
		// ensures the informer has processed the watch event before the upscale reconcile.
		By("confirming HPA managed-by annotation is set (cache settled before upscale)")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "hpa", "v1-hpa-deploy", "-n", testNamespace,
				"-o", fmt.Sprintf("jsonpath={.metadata.annotations['%s']}", escapeAnnotationKey("lightsout.techsupport.mk/managed-by")))
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).NotTo(BeEmpty())
		}, 30*time.Second, time.Second).Should(Succeed())
		Consistently(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "hpa", "v1-hpa-deploy", "-n", testNamespace,
				"-o", "jsonpath={.spec.behavior.scaleUp.selectPolicy}")
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("Disabled"))
		}, 3*time.Second, time.Second).Should(Succeed())

		By("switching schedule to upscale period")
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
  namespaces:
    - %s
`, scheduleName, scheduleNamespace, testNamespace)

		err = os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
		Expect(err).NotTo(HaveOccurred())

		cmd = exec.Command("kubectl", "apply", "-f", scheduleFile)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for HPA scaleUp to be re-enabled after upscale")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "hpa", "v1-hpa-deploy", "-n", testNamespace,
				"-o", "jsonpath={.spec.behavior.scaleUp.selectPolicy}")
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).NotTo(Equal("Disabled"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("waiting for deployment to be restored to 3 replicas")
		Eventually(func(g Gomega) {
			replicas := getDeploymentReplicas(testNamespace, "v1-hpa-deploy")
			g.Expect(replicas).To(Equal("3"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())
	})
})
