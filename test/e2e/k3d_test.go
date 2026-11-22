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
	gatewayAPIInstall = "https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.7.0/standard-install.yaml"
	traefikService    = "traefik.kube-system.svc.cluster.local"
	// deskDomain and chartMediaDomain are the hostnames the chart routes to
	// desk and darkroom, under the Gateway's wildcard.
	deskDomain       = "desk.e2e.test"
	chartMediaDomain = "files.e2e.test"
)

// helmRoutingValues put press in gateway mode on k3d, as docs/k3s.md does.
var helmRoutingValues = []string{
	"press.routing.mode=gateway",
	"press.routing.gatewayName=manifesto",
	"press.routing.gatewayNamespace=kube-system",
	"press.routing.gatewaySection=websecure",
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

	By("issuing the wildcard certificate from a self-signed CA with cert-manager")
	_, err = utils.Run(exec.Command("kubectl", "apply", "-f", "test/e2e/k3d/tls.yaml"))
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	_, err = utils.Run(exec.Command("kubectl", "wait", "certificate/e2e-wildcard", "-n", "kube-system",
		"--for=condition=Ready", "--timeout=3m"))
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "cert-manager did not issue the wildcard certificate")

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

	By("reading the Dispatch through Traefik over HTTPS, trusting only the test CA")
	ip, err := utils.Run(exec.Command("kubectl", "get", "service", "traefik", "-n", "kube-system",
		"-o", "jsonpath={.spec.clusterIP}"))
	Expect(err).NotTo(HaveOccurred())
	startTLSCurlPod()
	url := "https://" + blogDomain + "/posts/" + dispatchName
	Eventually(func(g Gomega) {
		code, body := curlTLS(g, ip, "--cacert", "/tls/ca.crt", url)
		g.Expect(code).To(Equal("200"))
		g.Expect(body).To(ContainSubstring(dispatchMarker))
	}).Should(Succeed())

	By("presenting a certificate for the Gazette's domain, which curl refuses without the CA")
	// curl exits with 60 when it cannot authenticate the peer's certificate.
	out, err := utils.Run(exec.Command("kubectl", "exec", tlsCurlPod, "-n", "kube-system", "--",
		"curl", "-s", "-o", "/dev/null", "--resolve", blogDomain+":443:"+ip, url))
	Expect(err).To(HaveOccurred(), "an untrusted certificate must fail verification")
	Expect(out).To(ContainSubstring("exit code 60"), "curl must fail on the certificate, not something else")

	By("not serving the Gazette on the plain HTTP listener, which press does not attach to")
	code, _ := curlFrom(Default, "http://"+traefikService+"/posts/"+dispatchName)
	Expect(code).To(Equal("404"))
}

// expectChartRoutes checks the routes the chart made for desk and darkroom:
// accepted by Traefik, and served over HTTPS with the wildcard certificate.
func expectChartRoutes() {
	ip, err := utils.Run(exec.Command("kubectl", "get", "service", "traefik", "-n", "kube-system",
		"-o", "jsonpath={.spec.clusterIP}"))
	Expect(err).NotTo(HaveOccurred())
	for _, c := range []struct{ component, host string }{{"desk", deskDomain}, {"darkroom", chartMediaDomain}} {
		By("reaching " + c.component + " through the chart's route at " + c.host)
		Eventually(func(g Gomega) {
			out, err := utils.Run(exec.Command("kubectl", "get", "httproute", "manifesto-"+c.component, "-n", namespace,
				"-o", `jsonpath={.status.parents[0].conditions[?(@.type=="Accepted")].status}`))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(out).To(Equal("True"))
			code, body := curlTLS(g, ip, "--cacert", "/tls/ca.crt", "https://"+c.host+"/healthz")
			g.Expect(code).To(Equal("200"))
			g.Expect(body).To(Equal("ok"))
		}).Should(Succeed())
	}
}

const tlsCurlPod = "curl-tls"

// startTLSCurlPod runs curl next to the Gateway with the certificate's
// Secret mounted, for its ca.crt.
func startTLSCurlPod() {
	_, err := utils.Run(exec.Command("kubectl", "run", tlsCurlPod, "--restart=Never", "-n", "kube-system",
		"--image=curlimages/curl:latest", "--overrides", `{"spec": {
			"containers": [{
				"name": "curl", "image": "curlimages/curl:latest", "command": ["sleep", "3600"],
				"volumeMounts": [{"name": "tls", "mountPath": "/tls", "readOnly": true}],
				"securityContext": {"readOnlyRootFilesystem": true, "allowPrivilegeEscalation": false,
					"capabilities": {"drop": ["ALL"]}, "runAsNonRoot": true, "runAsUser": 1000,
					"seccompProfile": {"type": "RuntimeDefault"}}
			}],
			"volumes": [{"name": "tls", "secret": {"secretName": "e2e-wildcard-tls"}}]
		}}`))
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	_, err = utils.Run(exec.Command("kubectl", "wait", "pod/"+tlsCurlPod, "-n", "kube-system",
		"--for=condition=Ready", "--timeout=3m"))
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
}

// curlTLS fetches an https URL through Traefik's Service at ip, resolving
// its host there, as a visitor's browser would after DNS.
func curlTLS(g Gomega, ip string, args ...string) (code, body string) {
	const sep = "\n--status--"
	url := args[len(args)-1]
	host := strings.TrimPrefix(url, "https://")
	host = host[:strings.Index(host, "/")]
	cmdArgs := append([]string{"exec", tlsCurlPod, "-n", "kube-system", "--", "curl", "-s",
		"--resolve", host + ":443:" + ip, "-w", sep + "%{http_code}"}, args...)
	out, err := utils.Run(exec.Command("kubectl", cmdArgs...))
	g.ExpectWithOffset(1, err).NotTo(HaveOccurred(), "curl over TLS failed")
	i := strings.LastIndex(out, sep)
	g.ExpectWithOffset(1, i).To(BeNumerically(">=", 0), "unexpected curl output: %s", out)
	return strings.TrimSpace(out[i+len(sep):]), out[:i]
}
