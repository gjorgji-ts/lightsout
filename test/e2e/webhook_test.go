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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gjorgji-ts/lightsout/test/utils"
)

var _ = Describe("Webhook Validation", func() {
	const scheduleNamespace = "lightsout-system"

	// applySchedule writes yaml to a temp file and runs kubectl apply, returning the combined output and error.
	applySchedule := func(tmpName, yaml string) (string, error) {
		path := fmt.Sprintf("/tmp/wh-test-%s.yaml", tmpName)
		Expect(os.WriteFile(path, []byte(yaml), 0644)).To(Succeed())
		cmd := exec.Command("kubectl", "apply", "-f", path)
		return utils.Run(cmd)
	}

	deleteSchedule := func(name string) {
		cmd := exec.Command("kubectl", "delete", "lightsoutschedule", name,
			"-n", scheduleNamespace, "--ignore-not-found")
		_, _ = utils.Run(cmd)
	}

	Context("Rejected Schedules", func() {
		It("should reject an invalid upscale cron expression", func() {
			output, err := applySchedule("bad-upscale", fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutSchedule
metadata:
  name: wh-bad-upscale
  namespace: %s
spec:
  upscale: "not-a-cron"
  downscale: "0 18 * * 1-5"
  timezone: "UTC"
  namespaces:
    - default
`, scheduleNamespace))
			Expect(err).To(HaveOccurred(), "expected webhook to reject invalid upscale cron")
			Expect(output).To(ContainSubstring("invalid cron expression"))
		})

		It("should reject an invalid downscale cron expression", func() {
			output, err := applySchedule("bad-downscale", fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutSchedule
metadata:
  name: wh-bad-downscale
  namespace: %s
spec:
  upscale: "0 6 * * 1-5"
  downscale: "not-a-cron"
  timezone: "UTC"
  namespaces:
    - default
`, scheduleNamespace))
			Expect(err).To(HaveOccurred(), "expected webhook to reject invalid downscale cron")
			Expect(output).To(ContainSubstring("invalid cron expression"))
		})

		It("should reject an invalid timezone", func() {
			output, err := applySchedule("bad-tz", fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutSchedule
metadata:
  name: wh-bad-tz
  namespace: %s
spec:
  upscale: "0 6 * * 1-5"
  downscale: "0 18 * * 1-5"
  timezone: "America/NotACity"
  namespaces:
    - default
`, scheduleNamespace))
			Expect(err).To(HaveOccurred(), "expected webhook to reject invalid IANA timezone")
			Expect(output).To(ContainSubstring("invalid IANA timezone"))
		})

		It("should reject a schedule with neither namespaceSelector nor namespaces", func() {
			output, err := applySchedule("no-ns", fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutSchedule
metadata:
  name: wh-no-ns
  namespace: %s
spec:
  upscale: "0 6 * * 1-5"
  downscale: "0 18 * * 1-5"
  timezone: "UTC"
`, scheduleNamespace))
			Expect(err).To(HaveOccurred(), "expected webhook to require namespace selection")
			Expect(output).To(ContainSubstring("at least one of namespaceSelector or namespaces must be specified"))
		})

		It("should reject an upscale rate limit with batch size of 0", func() {
			output, err := applySchedule("zero-batch", fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutSchedule
metadata:
  name: wh-zero-batch
  namespace: %s
spec:
  upscale: "0 6 * * 1-5"
  downscale: "0 18 * * 1-5"
  timezone: "UTC"
  namespaces:
    - default
  upscaleRateLimit:
    batchSize: 0
    delayBetweenBatches: "5s"
`, scheduleNamespace))
			Expect(err).To(HaveOccurred(), "expected webhook to reject batchSize=0")
			Expect(output).To(ContainSubstring("should be greater than or equal to 1"))
		})

		It("should reject an invalid ArgoCD namespace name", func() {
			output, err := applySchedule("bad-argocd-ns", fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutSchedule
metadata:
  name: wh-bad-argocd-ns
  namespace: %s
spec:
  upscale: "0 6 * * 1-5"
  downscale: "0 18 * * 1-5"
  timezone: "UTC"
  namespaces:
    - default
  argoCD:
    namespace: "InvalidUpperCase"
`, scheduleNamespace))
			Expect(err).To(HaveOccurred(), "expected webhook to reject non-DNS-1123 ArgoCD namespace")
			Expect(output).To(ContainSubstring("invalid namespace name"))
		})
	})

	Context("Accepted Schedules", Ordered, func() {
		AfterAll(func() {
			deleteSchedule("wh-valid-full")
		})

		It("should accept a fully-specified valid schedule", func() {
			_, err := applySchedule("valid-full", fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutSchedule
metadata:
  name: wh-valid-full
  namespace: %s
spec:
  upscale: "0 6 * * 1-5"
  downscale: "0 18 * * 1-5"
  timezone: "Europe/London"
  namespaces:
    - default
  upscaleRateLimit:
    batchSize: 5
    delayBetweenBatches: "10s"
  downscaleRateLimit:
    batchSize: 10
    delayBetweenBatches: "5s"
  argoCD:
    namespace: "argocd"
`, scheduleNamespace))
			Expect(err).NotTo(HaveOccurred(), "expected a fully-specified valid schedule to be accepted")
		})
	})

	Context("Overlap Warning", Ordered, func() {
		AfterAll(func() {
			deleteSchedule("wh-overlap-a")
			deleteSchedule("wh-overlap-b")
		})

		It("should warn but accept a schedule that overlaps with an existing one", func() {
			By("creating the first schedule targeting the default namespace")
			_, err := applySchedule("overlap-a", fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutSchedule
metadata:
  name: wh-overlap-a
  namespace: %s
spec:
  upscale: "0 6 * * 1-5"
  downscale: "0 18 * * 1-5"
  timezone: "UTC"
  namespaces:
    - default
`, scheduleNamespace))
			Expect(err).NotTo(HaveOccurred())

			By("creating a second schedule targeting the same namespace")
			output, err := applySchedule("overlap-b", fmt.Sprintf(`
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutSchedule
metadata:
  name: wh-overlap-b
  namespace: %s
spec:
  upscale: "0 7 * * 1-5"
  downscale: "0 19 * * 1-5"
  timezone: "UTC"
  namespaces:
    - default
`, scheduleNamespace))
			Expect(err).NotTo(HaveOccurred(), "overlapping schedule should be accepted (warning, not error)")
			Expect(output).To(ContainSubstring("may conflict with existing schedule"), "expected an overlap warning in the output")
		})
	})
})
