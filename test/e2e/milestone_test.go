//go:build e2e

/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"go.pkgs.davy.tw/manifesto/test/utils"
)

// Milestone 1 end to end, on a real cluster: an author applies a Gazette and
// a scheduled Dispatch, press publishes it when publishAt passes, and herald
// serves it to visitors.
const (
	blogNamespace  = "blog-e2e"
	blogDomain     = "alice.e2e.test"
	heraldService  = "manifesto-herald"
	heraldCurlPod  = "curl-herald"
	dispatchName   = "hello-e2e"
	dispatchMarker = "Published by press, served by herald."
)

// registerMilestone1Specs adds the M1 specs to the enclosing Ordered container,
// after the controller has been deployed.
func registerMilestone1Specs() {
	It("should run herald", func() {
		cmd := exec.Command("kubectl", "rollout", "status", "deployment/"+heraldService,
			"-n", namespace, "--timeout=3m")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "herald Deployment did not become ready")
	})

	It("should publish a scheduled Dispatch and serve it", func() {
		By("starting a curl pod to reach herald from inside the cluster")
		startHeraldCurlPod()

		By("applying a Gazette and a Dispatch scheduled 30 seconds ahead")
		_, err := utils.Run(exec.Command("kubectl", "create", "ns", blogNamespace))
		Expect(err).NotTo(HaveOccurred())
		publishAt := time.Now().Add(30 * time.Second).UTC().Truncate(time.Second)
		kubectlApply(fmt.Sprintf(`
apiVersion: manifesto.davy.tw/v1alpha1
kind: Gazette
metadata:
  name: alice-notes
  namespace: %[1]s
spec:
  title: "Alice's Notes"
  domain: %[2]s
---
apiVersion: manifesto.davy.tw/v1alpha1
kind: Dispatch
metadata:
  name: %[3]s
  namespace: %[1]s
spec:
  gazetteRef: alice-notes
  title: Hello E2E
  state: Published
  publishAt: "%[4]s"
  content:
    body: |
      # Hello E2E

      %[5]s
`, blogNamespace, blogDomain, dispatchName, publishAt.Format(time.RFC3339), dispatchMarker))

		By("waiting for press to embargo it until publishAt")
		Eventually(func(g Gomega) {
			g.Expect(dispatchField(g, "{.status.phase}")).To(Or(Equal("Embargoed"), Equal("Published")))
			g.Expect(dispatchField(g, `{.status.conditions[?(@.type=="Ready")].status}`)).To(Equal("True"))
			g.Expect(dispatchField(g, `{.status.conditions[?(@.type=="Rendered")].status}`)).To(Equal("True"))
		}).Should(Succeed())
		if time.Until(publishAt) > 5*time.Second {
			Expect(dispatchField(Default, "{.status.phase}")).To(Equal("Embargoed"))
			code, _ := heraldGet(Default, "/posts/"+dispatchName)
			Expect(code).To(Equal("404"), "an Embargoed Dispatch must not be served")
		}

		By("waiting for press to publish it")
		Eventually(func(g Gomega) {
			g.Expect(dispatchField(g, "{.status.phase}")).To(Equal("Published"))
		}).Should(Succeed())
		Expect(time.Now()).To(BeTemporally(">=", publishAt), "published before publishAt")
		Expect(dispatchField(Default, "{.status.publishedAt}")).To(Equal(publishAt.Format(time.RFC3339)))

		By("reading it through herald")
		Eventually(func(g Gomega) {
			code, body := heraldGet(g, "/posts/"+dispatchName)
			g.Expect(code).To(Equal("200"))
			g.Expect(body).To(ContainSubstring(dispatchMarker))
		}).Should(Succeed())
		_, index := heraldGet(Default, "/")
		Expect(index).To(ContainSubstring(`href="/posts/` + dispatchName + `"`))

		if utils.Provider() == utils.ProviderK3d {
			expectServedThroughTraefik()
		}

		By("retracting it")
		_, err = utils.Run(exec.Command("kubectl", "patch", "dispatch", dispatchName, "-n", blogNamespace,
			"--type=merge", "-p", `{"spec":{"state":"Retracted"}}`))
		Expect(err).NotTo(HaveOccurred())
		Eventually(func(g Gomega) {
			code, _ := heraldGet(g, "/posts/"+dispatchName)
			g.Expect(code).To(Equal("410"))
		}).Should(Succeed())
	})
}

// cleanupMilestone1 removes what the M1 specs created.
func cleanupMilestone1() {
	_, _ = utils.Run(exec.Command("kubectl", "delete", "pod", heraldCurlPod, "-n", namespace, "--ignore-not-found"))
	_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", blogNamespace, "--ignore-not-found"))
}

// dumpMilestone1Debug prints herald logs and the test Dispatches on failure.
func dumpMilestone1Debug() {
	for _, args := range [][]string{
		{"logs", "deployment/" + heraldService, "-n", namespace, "--all-containers"},
		{"get", "manifesto", "-n", blogNamespace, "-o", "yaml"},
	} {
		out, err := utils.Run(exec.Command("kubectl", args...))
		_, _ = fmt.Fprintf(GinkgoWriter, "kubectl %s:\n%s\n(err: %v)\n", strings.Join(args, " "), out, err)
	}
}

func startHeraldCurlPod() {
	cmd := exec.Command("kubectl", "run", heraldCurlPod, "--restart=Never",
		"--namespace", namespace,
		"--image=curlimages/curl:latest",
		"--overrides", `{
			"spec": {
				"containers": [{
					"name": "curl",
					"image": "curlimages/curl:latest",
					"command": ["sleep", "3600"],
					"securityContext": {
						"readOnlyRootFilesystem": true,
						"allowPrivilegeEscalation": false,
						"capabilities": {"drop": ["ALL"]},
						"runAsNonRoot": true,
						"runAsUser": 1000,
						"seccompProfile": {"type": "RuntimeDefault"}
					}
				}]
			}
		}`)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to create the curl pod")
	_, err = utils.Run(exec.Command("kubectl", "wait", "pod/"+heraldCurlPod, "-n", namespace,
		"--for=condition=Ready", "--timeout=3m"))
	Expect(err).NotTo(HaveOccurred(), "curl pod did not become ready")
}

func kubectlApply(manifest string) {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	_, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "kubectl apply failed")
}

func dispatchField(g Gomega, jsonpath string) string {
	out, err := utils.Run(exec.Command("kubectl", "get", "dispatch", dispatchName, "-n", blogNamespace,
		"-o", "jsonpath="+jsonpath))
	g.ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return out
}

// heraldGet fetches path from herald as a visitor of blogDomain and returns
// the HTTP status code and body.
func heraldGet(g Gomega, path string) (code, body string) {
	return curlFrom(g, fmt.Sprintf("http://%s.%s.svc.cluster.local%s", heraldService, namespace, path))
}

// curlFrom fetches url from inside the cluster as a visitor of blogDomain.
func curlFrom(g Gomega, url string) (code, body string) {
	return curlFromHost(g, blogDomain, url)
}

// curlFromHost fetches url from inside the cluster with the given Host header.
func curlFromHost(g Gomega, host, url string) (code, body string) {
	const sep = "\n--status--"
	out, err := utils.Run(exec.Command("kubectl", "exec", heraldCurlPod, "-n", namespace, "--",
		"curl", "-s", "-H", "Host: "+host, "-w", sep+"%{http_code}", url))
	g.ExpectWithOffset(1, err).NotTo(HaveOccurred(), "curl from inside the cluster failed")
	i := strings.LastIndex(out, sep)
	g.ExpectWithOffset(1, i).To(BeNumerically(">=", 0), "unexpected curl output: %s", out)
	return strings.TrimSpace(out[i+len(sep):]), out[:i]
}
