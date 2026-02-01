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

var _ = Describe("Safety Protections", Ordered, func() {
	const (
		scheduleNamespace = "lightsout-system"
	)

	Context("Workload Exclusion by Label", func() {
		const (
			testNamespace = "test-exclude-labels"
			scheduleName  = "test-exclude-labels"
		)

		BeforeAll(func() {
			By("creating test namespace")
			cmd := exec.Command("kubectl", "create", "ns", testNamespace)
			_, _ = utils.Run(cmd)

			By("creating deployments with different labels")
			createDeploymentWithLabels(testNamespace, "web-app", 2, map[string]string{"app": "web"})
			createDeploymentWithLabels(testNamespace, "critical-app", 2, map[string]string{"app": "critical", "lightsout-exclude": "true"})
		})

		AfterAll(func() {
			By("cleaning up test resources")
			cmd := exec.Command("kubectl", "delete", "lightsoutschedule", scheduleName, "-n", scheduleNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "ns", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should not scale workloads matching excludeLabels", func() {
			By("creating a LightsOutSchedule with excludeLabels")
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
  excludeLabels:
    matchLabels:
      lightsout-exclude: "true"
`, scheduleName, scheduleNamespace, testNamespace)

			scheduleFile := "/tmp/test-exclude-labels.yaml"
			err := os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			cmd := exec.Command("kubectl", "apply", "-f", scheduleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for web-app to be scaled down")
			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "web-app")
				g.Expect(replicas).To(Equal("0"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying critical-app is NOT scaled (excluded by label)")
			Consistently(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "critical-app")
				g.Expect(replicas).To(Equal("2"))
			}, 15*time.Second, 5*time.Second).Should(Succeed())

			By("verifying critical-app has no lightsout annotations")
			verifyDeploymentAnnotationMissing(testNamespace, "critical-app", "lightsout.techsupport.mk/managed-by")

			By("verifying critical-app has no lightsout labels")
			verifyDeploymentLabelMissing(testNamespace, "critical-app", "lightsout.techsupport.mk/managed-by")
		})
	})

	Context("Already Scaled Workloads", func() {
		const (
			testNamespace = "test-already-scaled"
			scheduleName  = "test-already-scaled"
		)

		BeforeAll(func() {
			By("creating test namespace")
			cmd := exec.Command("kubectl", "create", "ns", testNamespace)
			_, _ = utils.Run(cmd)

			By("creating a deployment already at 0 replicas (user-managed)")
			createDeployment(testNamespace, "user-scaled-down", 0)
		})

		AfterAll(func() {
			By("cleaning up test resources")
			cmd := exec.Command("kubectl", "delete", "lightsoutschedule", scheduleName, "-n", scheduleNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "ns", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should not claim user-managed zero-replica workloads", func() {
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

			scheduleFile := "/tmp/test-already-scaled.yaml"
			err := os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			cmd := exec.Command("kubectl", "apply", "-f", scheduleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for reconciliation")
			time.Sleep(15 * time.Second)

			By("verifying deployment stays at 0 and has no annotations (not claimed)")
			replicas := getDeploymentReplicas(testNamespace, "user-scaled-down")
			Expect(replicas).To(Equal("0"))
			verifyDeploymentAnnotationMissing(testNamespace, "user-scaled-down", "lightsout.techsupport.mk/managed-by")
			verifyDeploymentAnnotationMissing(testNamespace, "user-scaled-down", "lightsout.techsupport.mk/original-replicas")

			By("verifying deployment has no lightsout labels (not claimed)")
			verifyDeploymentLabelMissing(testNamespace, "user-scaled-down", "lightsout.techsupport.mk/managed-by")

			By("switching to upscale period")
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
  namespaces:
    - %s
`, scheduleName, scheduleNamespace, testNamespace)

			err = os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			cmd = exec.Command("kubectl", "apply", "-f", scheduleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying deployment stays at 0 (user intent preserved)")
			Consistently(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "user-scaled-down")
				g.Expect(replicas).To(Equal("0"))
			}, 20*time.Second, 5*time.Second).Should(Succeed())
		})
	})

	Context("User-Suspended CronJob Protection", func() {
		const (
			testNamespace = "test-user-suspended-cj"
			scheduleName  = "test-user-suspended-cj"
		)

		BeforeAll(func() {
			By("creating test namespace")
			cmd := exec.Command("kubectl", "create", "ns", testNamespace)
			_, _ = utils.Run(cmd)

			By("creating a user-suspended cronjob")
			createCronJob(testNamespace, "user-suspended-cj", true)
		})

		AfterAll(func() {
			By("cleaning up test resources")
			cmd := exec.Command("kubectl", "delete", "lightsoutschedule", scheduleName, "-n", scheduleNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "ns", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should not resume user-suspended cronjobs", func() {
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
  workloadTypes:
    - CronJob
`, scheduleName, scheduleNamespace, testNamespace)

			scheduleFile := "/tmp/test-user-suspended-cj.yaml"
			err := os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			cmd := exec.Command("kubectl", "apply", "-f", scheduleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for reconciliation")
			time.Sleep(15 * time.Second)

			By("verifying cronjob stays suspended and is not marked as lightsout-managed")
			suspended := getCronJobSuspended(testNamespace, "user-suspended-cj")
			Expect(suspended).To(Equal("true"))

			By("switching to upscale period")
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
  namespaces:
    - %s
  workloadTypes:
    - CronJob
`, scheduleName, scheduleNamespace, testNamespace)

			err = os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			cmd = exec.Command("kubectl", "apply", "-f", scheduleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for reconciliation")
			time.Sleep(15 * time.Second)

			By("verifying cronjob remains suspended (user intent preserved)")
			suspended = getCronJobSuspended(testNamespace, "user-suspended-cj")
			Expect(suspended).To(Equal("true"))

			By("verifying cronjob has no lightsout annotations (not managed)")
			verifyCronJobAnnotationMissing(testNamespace, "user-suspended-cj", "lightsout.techsupport.mk/managed-by")

			By("verifying cronjob has no lightsout labels (not managed)")
			verifyCronJobLabelMissing(testNamespace, "user-suspended-cj", "lightsout.techsupport.mk/managed-by")
		})
	})

	Context("Multiple Schedules Protection", func() {
		const (
			testNamespace = "test-multi-schedule"
			scheduleNameA = "test-schedule-a"
			scheduleNameB = "test-schedule-b"
		)

		BeforeAll(func() {
			By("creating test namespace")
			createNamespaceWithLabels(testNamespace, map[string]string{"multi-schedule": "true"})

			By("creating test deployment")
			createDeployment(testNamespace, "shared-app", 2)
		})

		AfterAll(func() {
			By("cleaning up test resources")
			cmd := exec.Command("kubectl", "delete", "lightsoutschedule", scheduleNameA, "-n", scheduleNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "lightsoutschedule", scheduleNameB, "-n", scheduleNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "ns", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should prevent one schedule from taking over another's workloads", func() {
			By("creating Schedule A in downscale period")
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
`, scheduleNameA, scheduleNamespace, testNamespace)

			scheduleFile := "/tmp/test-schedule-a.yaml"
			err := os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			cmd := exec.Command("kubectl", "apply", "-f", scheduleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for Schedule A to scale down the deployment")
			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "shared-app")
				g.Expect(replicas).To(Equal("0"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying deployment is managed by Schedule A")
			verifyDeploymentAnnotation(testNamespace, "shared-app", "lightsout.techsupport.mk/managed-by", scheduleNameA)
			verifyDeploymentLabel(testNamespace, "shared-app", "lightsout.techsupport.mk/managed-by", scheduleNameA)

			By("creating Schedule B targeting the same namespace (also in downscale)")
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
  namespaces:
    - %s
`, scheduleNameB, scheduleNamespace, testNamespace)

			scheduleFile = "/tmp/test-schedule-b.yaml"
			err = os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			cmd = exec.Command("kubectl", "apply", "-f", scheduleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for Schedule B to reconcile")
			time.Sleep(15 * time.Second)

			By("verifying deployment is still managed by Schedule A (not taken over by B)")
			verifyDeploymentAnnotation(testNamespace, "shared-app", "lightsout.techsupport.mk/managed-by", scheduleNameA)
			verifyDeploymentLabel(testNamespace, "shared-app", "lightsout.techsupport.mk/managed-by", scheduleNameA)

			By("deleting Schedule A")
			cmd = exec.Command("kubectl", "delete", "lightsoutschedule", scheduleNameA, "-n", scheduleNamespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for deployment to be restored")
			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "shared-app")
				g.Expect(replicas).To(Equal("2"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying deployment annotations are removed")
			verifyDeploymentAnnotationMissing(testNamespace, "shared-app", "lightsout.techsupport.mk/managed-by")

			By("verifying deployment labels are removed")
			verifyDeploymentLabelMissing(testNamespace, "shared-app", "lightsout.techsupport.mk/managed-by")
		})
	})
})

// Helper function for creating deployments with labels
func createDeploymentWithLabels(namespace, name string, replicas int, labels map[string]string) {
	metadataLabelsYAML := ""
	for k, v := range labels {
		metadataLabelsYAML += fmt.Sprintf("    %s: \"%s\"\n", k, v)
	}

	yaml := fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
  labels:
    app: %s
%sspec:
  replicas: %d
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
      labels:
        app: %s
    spec:
      containers:
      - name: nginx
        image: nginx:alpine
        resources:
          limits:
            memory: "64Mi"
            cpu: "50m"
`, name, namespace, name, metadataLabelsYAML, replicas, name, name)

	file := fmt.Sprintf("/tmp/deploy-%s.yaml", name)
	err := os.WriteFile(file, []byte(yaml), 0644)
	Expect(err).NotTo(HaveOccurred())

	cmd := exec.Command("kubectl", "apply", "-f", file)
	_, err = utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())

	// Wait for deployment to be created (don't wait for ready if replicas is 0)
	if replicas > 0 {
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "deployment", name, "-n", namespace,
				"-o", "jsonpath={.status.availableReplicas}")
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal(fmt.Sprintf("%d", replicas)))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())
	} else {
		// Just wait for the deployment to exist
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "deployment", name, "-n", namespace)
			_, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
		}, 30*time.Second, 5*time.Second).Should(Succeed())
	}
}
