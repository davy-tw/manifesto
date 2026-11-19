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

// helmRoutingValues put press in gateway mode on k3d, as docs/k3s.md does.
var helmRoutingValues = []string{
	"press.routing.mode=gateway",
	"press.routing.gatewayName=manifesto",
	"press.routing.gatewayNamespace=kube-system",
	"press.routing.gatewaySection=web",
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

// installWithHelm installs the chart with the images under test, in gateway
// routing mode, and waits for press.
func installWithHelm() {
	_, err := utils.Run(exec.Command("make", "helm"))
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	set := append([]string{"desk.publicURL=http://desk.e2e.test"}, helmRoutingValues...)
	for component, image := range map[string]string{
		"press": managerImage, "herald": heraldImage, "darkroom": darkroomImage, "desk": deskImage,
	} {
		repo, tag, _ := strings.Cut(image[strings.LastIndex(image, "/")+1:], ":")
		repo = image[:strings.LastIndex(image, "/")+1] + repo
		set = append(set, component+".image.repository="+repo, component+".image.tag="+tag)
	}
	args := make([]string, 0, 8+2*len(set))
	args = append(args, "install", "manifesto", "charts/manifesto", "-n", namespace, "--wait", "--timeout", "5m")
	for _, s := range set {
		args = append(args, "--set", s)
	}
	_, err = utils.Run(exec.Command("bin/helm", args...))
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to install the Helm chart")
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
