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

var _ = Describe("Scaling", Ordered, func() {
	const (
		scheduleNamespace = "lightsout-system"
	)

	Context("Deployments", func() {
		const (
			testNamespace = "test-scaling-deployments"
			scheduleName  = "test-deployment-scaling"
		)

		BeforeAll(func() {
			By("creating test namespace")
			cmd := exec.Command("kubectl", "create", "ns", testNamespace)
			_, _ = utils.Run(cmd) // Ignore error if exists

			By("labeling the test namespace")
			cmd = exec.Command("kubectl", "label", "ns", testNamespace, "test-type=scaling", "--overwrite")
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("creating test deployments with varying replica counts")
			createDeployment(testNamespace, "deploy-1-replica", 1)
			createDeployment(testNamespace, "deploy-3-replicas", 3)
			createDeployment(testNamespace, "deploy-5-replicas", 5)
		})

		AfterAll(func() {
			By("cleaning up test resources")
			cmd := exec.Command("kubectl", "delete", "lightsoutschedule", scheduleName, "-n", scheduleNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "ns", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should scale down deployments during downscale period", func() {
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

			scheduleFile := "/tmp/test-deploy-schedule.yaml"
			err := os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			cmd := exec.Command("kubectl", "apply", "-f", scheduleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for deployments to be scaled down")
			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "deploy-1-replica")
				g.Expect(replicas).To(Equal("0"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "deploy-3-replicas")
				g.Expect(replicas).To(Equal("0"))
			}, 30*time.Second, 5*time.Second).Should(Succeed())

			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "deploy-5-replicas")
				g.Expect(replicas).To(Equal("0"))
			}, 30*time.Second, 5*time.Second).Should(Succeed())

			By("verifying annotations are set correctly")
			verifyDeploymentAnnotation(testNamespace, "deploy-1-replica", "lightsout.techsupport.mk/original-replicas", "1")
			verifyDeploymentAnnotation(testNamespace, "deploy-3-replicas", "lightsout.techsupport.mk/original-replicas", "3")
			verifyDeploymentAnnotation(testNamespace, "deploy-5-replicas", "lightsout.techsupport.mk/original-replicas", "5")

			verifyDeploymentAnnotation(testNamespace, "deploy-1-replica", "lightsout.techsupport.mk/managed-by", scheduleName)
			verifyDeploymentAnnotation(testNamespace, "deploy-3-replicas", "lightsout.techsupport.mk/managed-by", scheduleName)
			verifyDeploymentAnnotation(testNamespace, "deploy-5-replicas", "lightsout.techsupport.mk/managed-by", scheduleName)

			By("verifying managed-by labels are set correctly")
			verifyDeploymentLabel(testNamespace, "deploy-1-replica", "lightsout.techsupport.mk/managed-by", scheduleName)
			verifyDeploymentLabel(testNamespace, "deploy-3-replicas", "lightsout.techsupport.mk/managed-by", scheduleName)
			verifyDeploymentLabel(testNamespace, "deploy-5-replicas", "lightsout.techsupport.mk/managed-by", scheduleName)

			By("verifying schedule status")
			Eventually(func(g Gomega) {
				state := getScheduleField(scheduleNamespace, scheduleName, "{.status.state}")
				g.Expect(state).To(Equal("Down"))
			}, 30*time.Second, 5*time.Second).Should(Succeed())
		})

		It("should scale up deployments during upscale period", func() {
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

			scheduleFile := "/tmp/test-deploy-schedule.yaml"
			err := os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			cmd := exec.Command("kubectl", "apply", "-f", scheduleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for deployments to be scaled up")
			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "deploy-1-replica")
				g.Expect(replicas).To(Equal("1"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "deploy-3-replicas")
				g.Expect(replicas).To(Equal("3"))
			}, 30*time.Second, 5*time.Second).Should(Succeed())

			Eventually(func(g Gomega) {
				replicas := getDeploymentReplicas(testNamespace, "deploy-5-replicas")
				g.Expect(replicas).To(Equal("5"))
			}, 30*time.Second, 5*time.Second).Should(Succeed())

			By("verifying annotations are removed")
			verifyDeploymentAnnotationMissing(testNamespace, "deploy-1-replica", "lightsout.techsupport.mk/original-replicas")
			verifyDeploymentAnnotationMissing(testNamespace, "deploy-3-replicas", "lightsout.techsupport.mk/original-replicas")
			verifyDeploymentAnnotationMissing(testNamespace, "deploy-5-replicas", "lightsout.techsupport.mk/original-replicas")

			By("verifying managed-by labels are removed")
			verifyDeploymentLabelMissing(testNamespace, "deploy-1-replica", "lightsout.techsupport.mk/managed-by")
			verifyDeploymentLabelMissing(testNamespace, "deploy-3-replicas", "lightsout.techsupport.mk/managed-by")
			verifyDeploymentLabelMissing(testNamespace, "deploy-5-replicas", "lightsout.techsupport.mk/managed-by")

			By("verifying schedule status")
			Eventually(func(g Gomega) {
				state := getScheduleField(scheduleNamespace, scheduleName, "{.status.state}")
				g.Expect(state).To(Equal("Up"))
			}, 30*time.Second, 5*time.Second).Should(Succeed())
		})
	})

	Context("StatefulSets", func() {
		const (
			testNamespace = "test-scaling-statefulsets"
			scheduleName  = "test-statefulset-scaling"
		)

		BeforeAll(func() {
			By("creating test namespace")
			cmd := exec.Command("kubectl", "create", "ns", testNamespace)
			_, _ = utils.Run(cmd)

			By("creating test statefulset")
			createStatefulSet(testNamespace, "test-sts", 2)
		})

		AfterAll(func() {
			By("cleaning up test resources")
			cmd := exec.Command("kubectl", "delete", "lightsoutschedule", scheduleName, "-n", scheduleNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "ns", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should scale down and up statefulsets", func() {
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
    - StatefulSet
`, scheduleName, scheduleNamespace, testNamespace)

			scheduleFile := "/tmp/test-sts-schedule.yaml"
			err := os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			cmd := exec.Command("kubectl", "apply", "-f", scheduleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for statefulset to be scaled down")
			Eventually(func(g Gomega) {
				replicas := getStatefulSetReplicas(testNamespace, "test-sts")
				g.Expect(replicas).To(Equal("0"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying annotations are set")
			verifyStatefulSetAnnotation(testNamespace, "test-sts", "lightsout.techsupport.mk/original-replicas", "2")

			By("verifying managed-by label is set")
			verifyStatefulSetLabel(testNamespace, "test-sts", "lightsout.techsupport.mk/managed-by", scheduleName)

			By("updating schedule to upscale period")
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
  workloadTypes:
    - StatefulSet
`, scheduleName, scheduleNamespace, testNamespace)

			err = os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			cmd = exec.Command("kubectl", "apply", "-f", scheduleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for statefulset to be scaled up")
			Eventually(func(g Gomega) {
				replicas := getStatefulSetReplicas(testNamespace, "test-sts")
				g.Expect(replicas).To(Equal("2"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying annotations are removed")
			verifyStatefulSetAnnotationMissing(testNamespace, "test-sts", "lightsout.techsupport.mk/original-replicas")

			By("verifying managed-by label is removed")
			verifyStatefulSetLabelMissing(testNamespace, "test-sts", "lightsout.techsupport.mk/managed-by")
		})
	})

	Context("CronJobs", func() {
		const (
			testNamespace = "test-scaling-cronjobs"
			scheduleName  = "test-cronjob-scaling"
		)

		BeforeAll(func() {
			By("creating test namespace")
			cmd := exec.Command("kubectl", "create", "ns", testNamespace)
			_, _ = utils.Run(cmd)

			By("creating test cronjob")
			createCronJob(testNamespace, "test-cronjob", false)
		})

		AfterAll(func() {
			By("cleaning up test resources")
			cmd := exec.Command("kubectl", "delete", "lightsoutschedule", scheduleName, "-n", scheduleNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "ns", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should suspend and resume cronjobs", func() {
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

			scheduleFile := "/tmp/test-cj-schedule.yaml"
			err := os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			cmd := exec.Command("kubectl", "apply", "-f", scheduleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for cronjob to be suspended")
			Eventually(func(g Gomega) {
				suspended := getCronJobSuspended(testNamespace, "test-cronjob")
				g.Expect(suspended).To(Equal("true"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying annotation indicates lightsout suspended it")
			verifyCronJobAnnotation(testNamespace, "test-cronjob", "lightsout.techsupport.mk/original-suspend", "lightsout")

			By("verifying managed-by label is set")
			verifyCronJobLabel(testNamespace, "test-cronjob", "lightsout.techsupport.mk/managed-by", scheduleName)

			By("updating schedule to upscale period")
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
  workloadTypes:
    - CronJob
`, scheduleName, scheduleNamespace, testNamespace)

			err = os.WriteFile(scheduleFile, []byte(scheduleYAML), 0644)
			Expect(err).NotTo(HaveOccurred())

			cmd = exec.Command("kubectl", "apply", "-f", scheduleFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for cronjob to be resumed")
			Eventually(func(g Gomega) {
				suspended := getCronJobSuspended(testNamespace, "test-cronjob")
				g.Expect(suspended).To(Equal("false"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying annotations are removed")
			verifyCronJobAnnotationMissing(testNamespace, "test-cronjob", "lightsout.techsupport.mk/original-suspend")

			By("verifying managed-by label is removed")
			verifyCronJobLabelMissing(testNamespace, "test-cronjob", "lightsout.techsupport.mk/managed-by")
		})
	})
})

// Helper functions

func createDeployment(namespace, name string, replicas int) {
	yaml := fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
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
      containers:
      - name: nginx
        image: nginx:alpine
        resources:
          limits:
            memory: "64Mi"
            cpu: "50m"
`, name, namespace, replicas, name, name)

	file := fmt.Sprintf("/tmp/deploy-%s.yaml", name)
	err := os.WriteFile(file, []byte(yaml), 0644)
	Expect(err).NotTo(HaveOccurred())

	cmd := exec.Command("kubectl", "apply", "-f", file)
	_, err = utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())

	// Wait for deployment to be ready (don't wait for ready if replicas is 0)
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

func createStatefulSet(namespace, name string, replicas int) {
	yaml := fmt.Sprintf(`
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: %s
  namespace: %s
spec:
  serviceName: %s
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
`, name, namespace, name, replicas, name, name)

	file := fmt.Sprintf("/tmp/sts-%s.yaml", name)
	err := os.WriteFile(file, []byte(yaml), 0644)
	Expect(err).NotTo(HaveOccurred())

	cmd := exec.Command("kubectl", "apply", "-f", file)
	_, err = utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())

	// Wait for statefulset to be ready
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "statefulset", name, "-n", namespace,
			"-o", "jsonpath={.status.readyReplicas}")
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).To(Equal(fmt.Sprintf("%d", replicas)))
	}, 2*time.Minute, 5*time.Second).Should(Succeed())
}

func createCronJob(namespace, name string, suspended bool) {
	suspendStr := "false"
	if suspended {
		suspendStr = "true"
	}
	yaml := fmt.Sprintf(`
apiVersion: batch/v1
kind: CronJob
metadata:
  name: %s
  namespace: %s
spec:
  schedule: "*/5 * * * *"
  suspend: %s
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - name: hello
            image: busybox
            command: ["echo", "hello"]
            resources:
              limits:
                memory: "32Mi"
                cpu: "10m"
          restartPolicy: OnFailure
`, name, namespace, suspendStr)

	file := fmt.Sprintf("/tmp/cj-%s.yaml", name)
	err := os.WriteFile(file, []byte(yaml), 0644)
	Expect(err).NotTo(HaveOccurred())

	cmd := exec.Command("kubectl", "apply", "-f", file)
	_, err = utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
}

func getDeploymentReplicas(namespace, name string) string {
	cmd := exec.Command("kubectl", "get", "deployment", name, "-n", namespace,
		"-o", "jsonpath={.spec.replicas}")
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	return output
}

func getStatefulSetReplicas(namespace, name string) string {
	cmd := exec.Command("kubectl", "get", "statefulset", name, "-n", namespace,
		"-o", "jsonpath={.spec.replicas}")
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	return output
}

func getCronJobSuspended(namespace, name string) string {
	cmd := exec.Command("kubectl", "get", "cronjob", name, "-n", namespace,
		"-o", "jsonpath={.spec.suspend}")
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	return output
}

func getScheduleField(namespace, name, jsonpath string) string {
	cmd := exec.Command("kubectl", "get", "lightsoutschedule", name, "-n", namespace,
		"-o", fmt.Sprintf("jsonpath=%s", jsonpath))
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	return output
}

func verifyDeploymentAnnotation(namespace, name, annotation, expected string) {
	cmd := exec.Command("kubectl", "get", "deployment", name, "-n", namespace,
		"-o", fmt.Sprintf("jsonpath={.metadata.annotations['%s']}", escapeAnnotationKey(annotation)))
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(output).To(Equal(expected), "Annotation %s should be %s", annotation, expected)
}

func verifyDeploymentAnnotationMissing(namespace, name, annotation string) {
	cmd := exec.Command("kubectl", "get", "deployment", name, "-n", namespace,
		"-o", fmt.Sprintf("jsonpath={.metadata.annotations['%s']}", escapeAnnotationKey(annotation)))
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(output).To(BeEmpty(), "Annotation %s should not exist", annotation)
}

func verifyStatefulSetAnnotation(namespace, name, annotation, expected string) {
	cmd := exec.Command("kubectl", "get", "statefulset", name, "-n", namespace,
		"-o", fmt.Sprintf("jsonpath={.metadata.annotations['%s']}", escapeAnnotationKey(annotation)))
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(output).To(Equal(expected), "Annotation %s should be %s", annotation, expected)
}

func verifyStatefulSetAnnotationMissing(namespace, name, annotation string) {
	cmd := exec.Command("kubectl", "get", "statefulset", name, "-n", namespace,
		"-o", fmt.Sprintf("jsonpath={.metadata.annotations['%s']}", escapeAnnotationKey(annotation)))
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(output).To(BeEmpty(), "Annotation %s should not exist", annotation)
}

func verifyCronJobAnnotation(namespace, name, annotation, expected string) {
	cmd := exec.Command("kubectl", "get", "cronjob", name, "-n", namespace,
		"-o", fmt.Sprintf("jsonpath={.metadata.annotations['%s']}", escapeAnnotationKey(annotation)))
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(output).To(Equal(expected), "Annotation %s should be %s", annotation, expected)
}

func verifyCronJobAnnotationMissing(namespace, name, annotation string) {
	cmd := exec.Command("kubectl", "get", "cronjob", name, "-n", namespace,
		"-o", fmt.Sprintf("jsonpath={.metadata.annotations['%s']}", escapeAnnotationKey(annotation)))
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(output).To(BeEmpty(), "Annotation %s should not exist", annotation)
}

func verifyDeploymentLabel(namespace, name, label, expected string) {
	cmd := exec.Command("kubectl", "get", "deployment", name, "-n", namespace,
		"-o", fmt.Sprintf("jsonpath={.metadata.labels['%s']}", escapeAnnotationKey(label)))
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(output).To(Equal(expected), "Label %s should be %s", label, expected)
}

func verifyDeploymentLabelMissing(namespace, name, label string) {
	cmd := exec.Command("kubectl", "get", "deployment", name, "-n", namespace,
		"-o", fmt.Sprintf("jsonpath={.metadata.labels['%s']}", escapeAnnotationKey(label)))
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(output).To(BeEmpty(), "Label %s should not exist", label)
}

func verifyStatefulSetLabel(namespace, name, label, expected string) {
	cmd := exec.Command("kubectl", "get", "statefulset", name, "-n", namespace,
		"-o", fmt.Sprintf("jsonpath={.metadata.labels['%s']}", escapeAnnotationKey(label)))
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(output).To(Equal(expected), "Label %s should be %s", label, expected)
}

func verifyStatefulSetLabelMissing(namespace, name, label string) {
	cmd := exec.Command("kubectl", "get", "statefulset", name, "-n", namespace,
		"-o", fmt.Sprintf("jsonpath={.metadata.labels['%s']}", escapeAnnotationKey(label)))
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(output).To(BeEmpty(), "Label %s should not exist", label)
}

func verifyCronJobLabel(namespace, name, label, expected string) {
	cmd := exec.Command("kubectl", "get", "cronjob", name, "-n", namespace,
		"-o", fmt.Sprintf("jsonpath={.metadata.labels['%s']}", escapeAnnotationKey(label)))
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(output).To(Equal(expected), "Label %s should be %s", label, expected)
}

func verifyCronJobLabelMissing(namespace, name, label string) {
	cmd := exec.Command("kubectl", "get", "cronjob", name, "-n", namespace,
		"-o", fmt.Sprintf("jsonpath={.metadata.labels['%s']}", escapeAnnotationKey(label)))
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(output).To(BeEmpty(), "Label %s should not exist", label)
}

func escapeAnnotationKey(key string) string {
	// Replace dots with escaped dots for jsonpath
	result := ""
	for _, c := range key {
		if c == '.' {
			result += "\\."
		} else {
			result += string(c)
		}
	}
	return result
}
