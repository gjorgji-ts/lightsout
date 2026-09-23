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
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gjorgji-ts/lightsout/test/utils"
)

// Stuck-termination detection is covered here only for the negative case: a
// healthy downscale must never report a stuck pod.
//
// The positive case cannot be built in a test cluster. The controller counts a
// pod that is terminating, past its grace period, and NOT in Succeeded or Failed,
// because only a pod whose containers are still alive still holds its node. Every
// route to that state was tried against a live cluster, and each one fails:
//
//   - A finalizer holds the API object, but the kubelet still kills the
//     containers and the pod ends in Failed.
//   - A zero grace period makes the kubelet send SIGKILL at once, so the
//     container exits 137 and the pod ends in Failed.
//   - An unschedulable pod runs no container, but Kubernetes moves it to Failed
//     when it is deleted, and puts it back there after a status patch.
//   - SIGKILL cannot be ignored, so no container image can stay alive.
//
// A live pod that refuses to die needs a broken kubelet or container runtime,
// which no test can arrange. TestReconcile_FindsPodStuckAfterTheDownscale covers
// the positive path instead, driving the real reconcile loop through the same
// sequence.
//
// The case guarded here is the one that would hurt in production. An alert that
// fires on every nightly downscale is worse than no alert at all.
var _ = Describe("Stuck Termination", Ordered, func() {
	const (
		scheduleNamespace = "lightsout-system"
		testNamespace     = "test-stuck-termination"
		scheduleName      = "test-stuck-termination"
		deploymentName    = "draining"
	)

	BeforeAll(func() {
		By("clearing anything left by an interrupted run")
		clearStaleNamespace(testNamespace)

		By("creating the test namespace")
		cmd := exec.Command("kubectl", "create", "ns", testNamespace)
		_, _ = utils.Run(cmd)

		By("creating a deployment whose pods terminate normally")
		createDrainingDeployment(testNamespace, deploymentName)
	})

	AfterEach(func() {
		if CurrentSpecReport().Failed() {
			By("collecting controller logs and namespace state")
			dumpTerminationDiagnostics(testNamespace, scheduleNamespace, scheduleName)
		}
	})

	AfterAll(func() {
		By("cleaning up the schedule and namespace")
		cmd := exec.Command("kubectl", "delete", "lightsoutschedule", scheduleName,
			"-n", scheduleNamespace, "--ignore-not-found")
		_, _ = utils.Run(cmd)
		// --wait=false, so a slow namespace can never hang the rest of the suite.
		cmd = exec.Command("kubectl", "delete", "ns", testNamespace,
			"--ignore-not-found", "--wait=false")
		_, _ = utils.Run(cmd)
	})

	It("should not report a stuck pod when the pods terminate normally", func() {
		By("creating a schedule in the downscale period")
		applyStuckTerminationSchedule(scheduleNamespace, scheduleName, testNamespace)

		By("waiting for the deployment to be scaled down")
		Eventually(func(g Gomega) {
			g.Expect(getDeploymentReplicas(testNamespace, deploymentName)).To(Equal("0"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("waiting for the pods to go away")
		Eventually(func(g Gomega) {
			g.Expect(livePodCount(testNamespace, deploymentName)).To(Equal(0))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("checking the schedule reports Down")
		Eventually(func(g Gomega) {
			state := getScheduleField(scheduleNamespace, scheduleName, "{.status.state}")
			g.Expect(state).To(Equal("Down"))
		}, 30*time.Second, 5*time.Second).Should(Succeed())

		// The controller checks again every TerminationCheckInterval while anything
		// is terminating. Watching across one full interval covers the polls that
		// follow a downscale, which is where a false positive would appear.
		By("checking the count stays at zero across a full check interval")
		Consistently(func(g Gomega) {
			g.Expect(stuckTerminatingPods(scheduleNamespace, scheduleName)).To(Equal(0))
		}, 6*time.Minute, 30*time.Second).Should(Succeed())
	})

	It("should emit no stuck-termination event for a clean downscale", func() {
		cmd := exec.Command("kubectl", "get", "events.events.k8s.io", "--all-namespaces",
			"--field-selector", "reason=PodsStuckTerminating",
			"-o", "jsonpath={range .items[*]}{.note}{\"\\n\"}{end}")
		output, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(output)).To(BeEmpty(),
			"a clean downscale must not warn, or the alert fires every night")
	})
})

// createDrainingDeployment creates a two-replica deployment whose pods shut down
// normally, which is what every healthy downscale looks like.
func createDrainingDeployment(namespace, name string) {
	yaml := fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
spec:
  replicas: 2
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
`, name, namespace, name, name)

	file := fmt.Sprintf("/tmp/deploy-%s.yaml", name)
	err := os.WriteFile(file, []byte(yaml), 0644)
	Expect(err).NotTo(HaveOccurred())

	cmd := exec.Command("kubectl", "apply", "-f", file)
	_, err = utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())

	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "deployment", name, "-n", namespace,
			"-o", "jsonpath={.status.availableReplicas}")
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).To(Equal("2"))
	}, 2*time.Minute, 5*time.Second).Should(Succeed())
}

// applyStuckTerminationSchedule creates a schedule whose cron windows put it
// permanently in the downscale period.
func applyStuckTerminationSchedule(scheduleNamespace, name, targetNamespace string) {
	yaml := fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutSchedule
metadata:
  name: %s
  namespace: %s
spec:
  upscale: "0 0 29 12 *"
  downscale: "0 0 2 1 *"
  timezone: "UTC"
  namespaces:
    - %s
`, name, scheduleNamespace, targetNamespace)

	file := "/tmp/test-stuck-termination-schedule.yaml"
	err := os.WriteFile(file, []byte(yaml), 0644)
	Expect(err).NotTo(HaveOccurred())

	cmd := exec.Command("kubectl", "apply", "-f", file)
	_, err = utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
}

// livePodCount counts the pods a label still selects. The marker form avoids
// jsonpath filters, which cannot express negation.
func livePodCount(namespace, app string) int {
	cmd := exec.Command("kubectl", "get", "pods", "-n", namespace,
		"-l", "app="+app, "-o", "jsonpath={range .items[*]}pod{\"\\n\"}{end}")
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())

	count := 0
	for _, line := range strings.Fields(output) {
		if line == "pod" {
			count++
		}
	}
	return count
}

// clearStaleNamespace removes pod finalizers left by an interrupted run and waits
// for the namespace to go. A namespace stuck in Terminating accepts no new
// objects, so without this a killed run poisons the next one.
func clearStaleNamespace(namespace string) {
	cmd := exec.Command("kubectl", "get", "pods", "-n", namespace,
		"-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}")
	output, err := utils.Run(cmd)
	if err != nil {
		return // The namespace does not exist, which is the normal case.
	}
	for _, name := range strings.Fields(output) {
		_, _ = utils.Run(exec.Command("kubectl", "patch", "pod", name, "-n", namespace,
			"--type", "merge", "-p", `{"metadata":{"finalizers":null}}`))
	}

	_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", namespace,
		"--ignore-not-found", "--wait=false"))
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "ns", namespace, "--ignore-not-found", "-o", "name")
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(strings.TrimSpace(output)).To(BeEmpty())
	}, 2*time.Minute, 5*time.Second).Should(Succeed())
}

// stuckTerminatingPods reads the count the controller published on the schedule.
// An absent field means zero, which is how the CRD omits it.
func stuckTerminatingPods(scheduleNamespace, name string) int {
	value := strings.TrimSpace(getScheduleField(scheduleNamespace, name, "{.status.stuckTerminatingPods}"))
	if value == "" {
		return 0
	}
	count, err := strconv.Atoi(value)
	Expect(err).NotTo(HaveOccurred(), "status.stuckTerminatingPods was %q", value)
	return count
}

// dumpTerminationDiagnostics prints what the cluster holds when a spec fails.
func dumpTerminationDiagnostics(testNamespace, scheduleNamespace, scheduleName string) {
	cmds := [][]string{
		{"kubectl", "get", "pods", "-n", testNamespace, "-o", "wide"},
		{"kubectl", "get", "lightsoutschedule", scheduleName, "-n", scheduleNamespace, "-o", "yaml"},
		{"kubectl", "get", "events.events.k8s.io", "--all-namespaces",
			"--field-selector", "reason=PodsStuckTerminating"},
		{"kubectl", "logs", "-n", scheduleNamespace, "deploy/lightsout-controller-manager", "--tail=120"},
	}
	for _, argv := range cmds {
		_, _ = fmt.Fprintf(GinkgoWriter, "\n$ %s\n", strings.Join(argv, " "))
		output, err := utils.Run(exec.Command(argv[0], argv[1:]...))
		if err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "command failed: %v\n", err)
			continue
		}
		_, _ = fmt.Fprintln(GinkgoWriter, output)
	}
}
