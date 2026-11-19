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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"go.pkgs.davy.tw/manifesto/test/utils"
)

// desk end to end: the deployed desk forwards requests with the caller's own
// token. A ServiceAccount token stands in for a user's ID token (browser
// sign-in needs an OIDC provider; it is covered by TestUI).
const deskURL = "http://manifesto-desk.manifesto-system.svc.cluster.local"

func registerDeskSpecs() {
	It("should serve desk with the caller's own permissions", func() {
		_, err := utils.Run(exec.Command("kubectl", "rollout", "status", "deployment/manifesto-desk",
			"-n", namespace, "--timeout=3m"))
		Expect(err).NotTo(HaveOccurred(), "desk Deployment did not become ready")

		By("giving a ServiceAccount the editor role in the publication")
		_, err = utils.Run(exec.Command("kubectl", "create", "serviceaccount", "e2e-editor", "-n", blogNamespace))
		Expect(err).NotTo(HaveOccurred())
		_, err = utils.Run(exec.Command("kubectl", "create", "rolebinding", "e2e-editor", "-n", blogNamespace,
			"--clusterrole=manifesto-editor", "--serviceaccount="+blogNamespace+":e2e-editor"))
		Expect(err).NotTo(HaveOccurred())
		token, err := utils.Run(exec.Command("kubectl", "create", "token", "e2e-editor", "-n", blogNamespace))
		Expect(err).NotTo(HaveOccurred())
		token = strings.TrimSpace(token)

		By("asking desk who the caller is")
		Eventually(func(g Gomega) {
			code, body := deskCall(g, token, "GET", "/api/session", "", "")
			g.Expect(code).To(Equal("200"))
			g.Expect(body).To(ContainSubstring(`"username":"system:serviceaccount:` + blogNamespace + `:e2e-editor"`))
		}).Should(Succeed())

		dispatches := "/k8s/apis/manifesto.davy.tw/v1alpha1/namespaces/" + blogNamespace + "/dispatches"
		By("writing a Dispatch through desk with server-side apply")
		apply := fmt.Sprintf(`{"apiVersion":"manifesto.davy.tw/v1alpha1","kind":"Dispatch",`+
			`"metadata":{"name":"from-desk","namespace":"%s"},`+
			`"spec":{"gazetteRef":"alice-notes","title":"From desk"}}`, blogNamespace)
		code, body := deskCall(Default, token, "PATCH", dispatches+"/from-desk?fieldManager=manifesto-desk",
			"application/apply-patch+yaml", apply)
		Expect(code).To(Equal("201"), body)
		manager, err := utils.Run(exec.Command("kubectl", "get", "dispatch", "from-desk", "-n", blogNamespace,
			"-o", "jsonpath={.metadata.managedFields[0].manager}"))
		Expect(err).NotTo(HaveOccurred())
		Expect(manager).To(Equal("manifesto-desk"))

		By("listing and previewing through desk")
		code, body = deskCall(Default, token, "GET", dispatches, "", "")
		Expect(code).To(Equal("200"))
		Expect(body).To(MatchRegexp(`"name":\s*"from-desk"`))
		preview := fmt.Sprintf(`{"dispatch":{"metadata":{"name":"draft","namespace":"%s"},`+
			`"spec":{"gazetteRef":"alice-notes","title":"Previewed in desk","content":{"body":"Hello"}}}}`, blogNamespace)
		code, body = deskCall(Default, token, "POST", "/api/preview", "application/json", preview)
		Expect(code).To(Equal("200"), body)
		Expect(body).To(ContainSubstring("Previewed in desk"))

		By("refusing what RBAC refuses, and what desk does not relay")
		gazettes := "/k8s/apis/manifesto.davy.tw/v1alpha1/namespaces/" + blogNamespace + "/gazettes/taken"
		code, _ = deskCall(Default, token, "PATCH", gazettes+"?fieldManager=manifesto-desk", "application/apply-patch+yaml",
			`{"apiVersion":"manifesto.davy.tw/v1alpha1","kind":"Gazette","metadata":{"name":"taken"},`+
				`"spec":{"title":"x","domain":"x.e2e.test"}}`)
		Expect(code).To(Equal("403"), "an editor must not create Gazettes")
		code, _ = deskCall(Default, token, "GET", "/k8s/api/v1/namespaces/"+blogNamespace+"/secrets", "", "")
		Expect(code).To(Equal("404"))
	})
}

// deskCall sends a request to desk from inside the cluster as the holder of token.
func deskCall(g Gomega, token, method, path, contentType, body string) (code, out string) {
	args := []string{"exec", "-i", heraldCurlPod, "-n", namespace, "--", "curl", "-s", "-X", method,
		"-H", "Authorization: Bearer " + token, "-w", "\n--status--%{http_code}"}
	if contentType != "" {
		args = append(args, "-H", "Content-Type: "+contentType, "--data-binary", "@-")
	}
	cmd := exec.Command("kubectl", append(args, deskURL+path)...)
	cmd.Stdin = strings.NewReader(body)
	res, err := utils.Run(cmd)
	g.ExpectWithOffset(1, err).NotTo(HaveOccurred())
	i := strings.LastIndex(res, "\n--status--")
	g.ExpectWithOffset(1, i).To(BeNumerically(">=", 0), "unexpected curl output: %s", res)
	return strings.TrimSpace(res[i+len("\n--status--"):]), res[:i]
}
