//go:build e2e

/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package e2e

import (
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"go.pkgs.davy.tw/manifesto/test/utils"
)

// On k3d the suite reproduces docs/k3s.md: Gateway API CRDs, Traefik's
// Gateway provider and a shared Gateway, then runs press in gateway mode.
const (
	gatewayAPIInstall = "https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.6.3/standard-install.yaml"
	traefikService    = "traefik.kube-system.svc.cluster.local"
)

// pressRoutingArgs are the flags press runs with on k3d.
var pressRoutingArgs = []string{
	"--routing-mode=gateway",
	"--gateway-name=manifesto",
	"--gateway-namespace=kube-system",
	"--gateway-section=web",
}

func setupTraefikGateway() {
	By("making sure Gateway API v1 CRDs (HTTPRoute, ReferenceGrant) are installed")
	versions, _ := utils.Run(exec.Command("kubectl", "get", "crd", "referencegrants.gateway.networking.k8s.io",
		"-o", "jsonpath={.spec.versions[*].name}"))
	if !strings.Contains(" "+versions+" ", " v1 ") {
		_, err := utils.Run(exec.Command("kubectl", "apply", "--server-side", "--force-conflicts", "-f", gatewayAPIInstall))
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to install Gateway API CRDs")
	}

	By("enabling Traefik's Gateway API provider")
	_, err := utils.Run(exec.Command("kubectl", "apply", "-f", "test/e2e/k3d/traefik-config.yaml"))
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	EventuallyWithOffset(1, func(g Gomega) {
		out, err := utils.Run(exec.Command("kubectl", "get", "gatewayclass", "traefik",
			"-o", `jsonpath={.status.conditions[?(@.type=="Accepted")].status}`))
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(out).To(Equal("True"))
	}, 5*time.Minute, 5*time.Second).Should(Succeed(), "Traefik did not accept the GatewayClass")

	By("creating the shared Gateway")
	_, err = utils.Run(exec.Command("kubectl", "apply", "-f", "test/e2e/k3d/gateway.yaml"))
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	EventuallyWithOffset(1, func(g Gomega) {
		out, err := utils.Run(exec.Command("kubectl", "get", "gateway", "manifesto", "-n", "kube-system",
			"-o", `jsonpath={.status.conditions[?(@.type=="Programmed")].status}`))
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(out).To(Equal("True"))
	}, 3*time.Minute, 5*time.Second).Should(Succeed(), "Traefik did not program the Gateway")
}

// enablePressRouting adds the gateway-mode flags to press and waits for the
// rollout.
func enablePressRouting() {
	ops := make([]string, 0, len(pressRoutingArgs))
	for _, a := range pressRoutingArgs {
		ops = append(ops, `{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"`+a+`"}`)
	}
	_, err := utils.Run(exec.Command("kubectl", "patch", "deployment", "manifesto-controller-manager", "-n", namespace,
		"--type=json", "-p", "["+strings.Join(ops, ",")+"]"))
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to enable routing in press")
	_, err = utils.Run(exec.Command("kubectl", "rollout", "status", "deployment/manifesto-controller-manager",
		"-n", namespace, "--timeout=3m"))
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
}

// expectServedThroughTraefik checks press routed the Gazette and that a
// visitor reaching Traefik gets the Dispatch.
func expectServedThroughTraefik() {
	By("waiting for press to report the Gazette as Routed")
	Eventually(func(g Gomega) {
		out, err := utils.Run(exec.Command("kubectl", "get", "gazette", "alice-notes", "-n", blogNamespace,
			"-o", `jsonpath={.status.conditions[?(@.type=="Routed")].status}`))
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(out).To(Equal("True"))
	}).Should(Succeed())

	By("reading the Dispatch through Traefik")
	Eventually(func(g Gomega) {
		code, body := curlFrom(g, "http://"+traefikService+"/posts/"+dispatchName)
		g.Expect(code).To(Equal("200"))
		g.Expect(body).To(ContainSubstring(dispatchMarker))
	}).Should(Succeed())
}
