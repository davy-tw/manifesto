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

const (
	s3Endpoint = "http://seaweedfs.storage-e2e.svc.cluster.local:8333"
	s3Body     = "Stored in SeaweedFS through a presigned URL\n"
)

func registerS3Specs() {
	It("should store a Print in an S3 bucket", func() {
		By("running SeaweedFS with S3 authentication")
		_, err := utils.Run(exec.Command("kubectl", "apply", "-f", "test/e2e/s3/seaweedfs.yaml"))
		Expect(err).NotTo(HaveOccurred())
		_, err = utils.Run(exec.Command("kubectl", "rollout", "status", "deployment/seaweedfs",
			"-n", "storage-e2e", "--timeout=5m"))
		Expect(err).NotTo(HaveOccurred())
		Eventually(func(g Gomega) {
			out, err := utils.Run(exec.Command("kubectl", "exec", "deploy/seaweedfs", "-n", "storage-e2e", "--",
				"sh", "-c", "echo 's3.bucket.create -name media' | weed shell"))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(out).NotTo(ContainSubstring("rror"))
		}).Should(Succeed())

		sum := sha256.Sum256([]byte(s3Body))
		kubectlApply(fmt.Sprintf(`
apiVersion: v1
kind: Secret
metadata:
  name: s3-creds
  namespace: %[1]s
stringData:
  accessKeyID: manifesto
  secretAccessKey: manifesto-e2e-secret
---
apiVersion: manifesto.davy.tw/v1alpha1
kind: Archive
metadata:
  name: bucket
  namespace: %[1]s
spec:
  s3:
    endpoint: %[2]s
    bucket: media
    credentialsSecretRef: {name: s3-creds}
---
apiVersion: manifesto.davy.tw/v1alpha1
kind: Print
metadata:
  name: s3doc
  namespace: %[1]s
spec:
  archiveRef: {kind: Archive, name: bucket}
  contentType: text/plain
  size: %[3]d
  sha256: "%[4]s"
`, mediaNamespace, s3Endpoint, len(s3Body), hex.EncodeToString(sum[:])))

		By("waiting for the Archive to reach the bucket")
		Eventually(func(g Gomega) {
			ready := kubectlGet(g, "archive", "bucket", mediaNamespace, `{.status.conditions[?(@.type=="Ready")].status}`)
			g.Expect(ready).To(Equal("True"))
		}).Should(Succeed())

		By("uploading to the presigned URL press hands out")
		var uploadURL string
		Eventually(func(g Gomega) {
			uploadURL = kubectlGet(g, "print", "s3doc", mediaNamespace, "{.status.uploadURL}")
			g.Expect(uploadURL).To(ContainSubstring("X-Amz-Signature="))
		}).Should(Succeed())
		By("being refused without the signed Content-Type")
		unsigned := exec.Command("kubectl", "exec", "-i", heraldCurlPod, "-n", namespace, "--",
			"curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "-X", "PUT",
			"-H", "Content-Type: application/octet-stream", "--data-binary", "@-", uploadURL)
		unsigned.Stdin = strings.NewReader(s3Body)
		code, err := utils.Run(unsigned)
		Expect(err).NotTo(HaveOccurred())
		Expect(code).To(Equal("403"), "SeaweedFS must reject a PUT that does not match the signature")

		put := exec.Command("kubectl", "exec", "-i", heraldCurlPod, "-n", namespace, "--",
			"curl", "-sf", "-X", "PUT", "-H", "Content-Type: text/plain", "--data-binary", "@-", uploadURL)
		put.Stdin = strings.NewReader(s3Body)
		_, err = utils.Run(put)
		Expect(err).NotTo(HaveOccurred(), "presigned upload to SeaweedFS failed")

		By("seeing press verify the object and visitors read it")
		var fileURL string
		Eventually(func(g Gomega) {
			g.Expect(kubectlGet(g, "print", "s3doc", mediaNamespace, "{.status.phase}")).To(Equal("Ready"))
			fileURL = kubectlGet(g, "print", "s3doc", mediaNamespace, "{.status.url}")
		}).Should(Succeed())
		Expect(fileURL).To(HavePrefix(s3Endpoint + "/media/"))
		Eventually(func(g Gomega) {
			code, body := curlFrom(g, fileURL)
			g.Expect(code).To(Equal("200"))
			g.Expect(body).To(Equal(s3Body))
		}).Should(Succeed())
	})
}

// cleanupMedia deletes the Prints (whose finalizers need the backends) before
// the backends themselves.
func cleanupMedia() {
	_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", mediaNamespace,
		"--ignore-not-found", "--wait=true", "--timeout=2m"))
	_, _ = utils.Run(exec.Command("kubectl", "delete", "clusterarchive", mediaArchive, "--ignore-not-found"))
	_, _ = utils.Run(exec.Command("kubectl", "delete", "-f", "test/e2e/s3/seaweedfs.yaml", "--ignore-not-found"))
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
