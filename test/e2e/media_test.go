//go:build e2e

/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"go.pkgs.davy.tw/manifesto/test/utils"
)

// Media end to end: a Print is uploaded through darkroom, press marks it
// Ready, and herald shows it on a Dispatch.
const (
	mediaNamespace = "blog-media"
	mediaDomain    = "media-test.e2e.test"
	mediaArchive   = "e2e-local"
	mediaBody      = "Manifesto media end to end\n"
	darkroomURL    = "http://manifesto-darkroom.manifesto-system.svc.cluster.local"
)

func registerMediaSpecs() {
	It("should run darkroom", func() {
		_, err := utils.Run(exec.Command("kubectl", "rollout", "status", "deployment/manifesto-darkroom",
			"-n", namespace, "--timeout=3m"))
		Expect(err).NotTo(HaveOccurred(), "darkroom Deployment did not become ready")
	})

	It("should store an uploaded Print and show it on a Dispatch", func() {
		sum := sha256.Sum256([]byte(mediaBody))
		_, err := utils.Run(exec.Command("kubectl", "create", "ns", mediaNamespace))
		Expect(err).NotTo(HaveOccurred())
		kubectlApply(fmt.Sprintf(`
apiVersion: manifesto.davy.tw/v1alpha1
kind: ClusterArchive
metadata:
  name: %[1]s
spec:
  pvc:
    claimName: manifesto-media-data
    publicURL: %[2]s
---
apiVersion: manifesto.davy.tw/v1alpha1
kind: Print
metadata:
  name: notes
  namespace: %[3]s
spec:
  archiveRef: {kind: ClusterArchive, name: %[1]s}
  contentType: text/plain
  size: %[4]d
  sha256: "%[5]s"
---
apiVersion: manifesto.davy.tw/v1alpha1
kind: Gazette
metadata:
  name: media
  namespace: %[3]s
spec:
  title: Media
  domain: %[6]s
---
apiVersion: manifesto.davy.tw/v1alpha1
kind: Dispatch
metadata:
  name: with-attachment
  namespace: %[3]s
spec:
  gazetteRef: media
  title: With attachment
  state: Published
  content:
    body: "[the notes](print:notes)"
`, mediaArchive, darkroomURL, mediaNamespace, len(mediaBody), hex.EncodeToString(sum[:]), mediaDomain))

		By("waiting for the ClusterArchive to reach darkroom")
		Eventually(func(g Gomega) {
			ready := kubectlGet(g, "clusterarchive", mediaArchive, "", `{.status.conditions[?(@.type=="Ready")].status}`)
			g.Expect(ready).To(Equal("True"))
		}).Should(Succeed())

		By("uploading the file to the signed URL press hands out")
		var uploadURL string
		Eventually(func(g Gomega) {
			uploadURL = kubectlGet(g, "print", "notes", mediaNamespace, "{.status.uploadURL}")
			g.Expect(uploadURL).To(HavePrefix(darkroomURL + "/upload/"))
		}).Should(Succeed())
		put := exec.Command("kubectl", "exec", "-i", heraldCurlPod, "-n", namespace, "--",
			"curl", "-sf", "-X", "PUT", "--data-binary", "@-", uploadURL)
		put.Stdin = strings.NewReader(mediaBody)
		_, err = utils.Run(put)
		Expect(err).NotTo(HaveOccurred(), "upload to darkroom failed")

		By("seeing the Print Ready and the file served")
		var fileURL string
		Eventually(func(g Gomega) {
			g.Expect(kubectlGet(g, "print", "notes", mediaNamespace, "{.status.phase}")).To(Equal("Ready"))
			fileURL = kubectlGet(g, "print", "notes", mediaNamespace, "{.status.url}")
		}).Should(Succeed())
		Eventually(func(g Gomega) {
			code, body := curlFrom(g, fileURL)
			g.Expect(code).To(Equal("200"))
			g.Expect(body).To(Equal(mediaBody))
		}).Should(Succeed())

		By("seeing herald link the Dispatch to the file")
		Eventually(func(g Gomega) {
			g.Expect(kubectlGet(g, "dispatch", "with-attachment", mediaNamespace,
				`{.status.conditions[?(@.type=="Ready")].status}`)).To(Equal("True"))
			code, page := curlFromHost(g, mediaDomain,
				fmt.Sprintf("http://%s.%s.svc.cluster.local/posts/with-attachment", heraldService, namespace))
			g.Expect(code).To(Equal("200"))
			g.Expect(page).To(ContainSubstring(`href="` + fileURL + `"`))
		}).Should(Succeed())
	})
}

func cleanupMedia() {
	_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", mediaNamespace,
		"--ignore-not-found", "--wait=true", "--timeout=2m"))
	_, _ = utils.Run(exec.Command("kubectl", "delete", "clusterarchive", mediaArchive, "--ignore-not-found"))
}

func kubectlGet(g Gomega, kind, name, ns, jsonpath string) string {
	args := []string{"get", kind, name, "-o", "jsonpath=" + jsonpath}
	if ns != "" {
		args = append(args, "-n", ns)
	}
	out, err := utils.Run(exec.Command("kubectl", args...))
	g.ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return out
}
