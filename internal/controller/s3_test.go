/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

func s3Credentials(ns, name string) *corev1.Secret {
	return &corev1.Secret{
		Namespace: ns, Name: name,
		Data: map[string][]byte{"accessKeyID": []byte("test"), "secretAccessKey": []byte("test")},
	}
}

func s3Source(secret string) *v1.S3ArchiveSource {
	return &v1.S3ArchiveSource{
		Endpoint: fakeS3.URL, Bucket: testBucket, PathStyle: new(true),
		CredentialsSecretRef: v1.SecretReference{Name: secret},
	}
}

func putWithType(url, contentType, body string) int {
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(body))
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	_ = resp.Body.Close()
	return resp.StatusCode
}

var _ = Describe("S3 media", func() {
	const timeout, interval = 10 * time.Second, 100 * time.Millisecond

	uploadURL := func(p *v1.Print) string { return p.Status.UploadURL }

	It("stores files in a bucket through a namespaced Archive", func() {
		ns := newNamespace()
		Expect(k8sClient.Create(ctx, s3Credentials(ns, "s3-creds"))).To(Succeed())
		archive := &v1.Archive{Namespace: ns, Name: "bucket", Spec: v1.ArchiveSpec{S3: s3Source("s3-creds")}}
		Expect(k8sClient.Create(ctx, archive)).To(Succeed())
		Eventually(fetch(archive), timeout, interval).Should(
			WithTransform(func(a *v1.Archive) string { return readyReason(a.Status.Conditions) }, Equal("True/"+v1.ReasonBackendReady)))

		const body = "bytes in a bucket"
		p := newPrint(ns, "doc", v1.ArchiveRef{Kind: v1.KindArchive, Name: "bucket"}, body)
		Expect(k8sClient.Create(ctx, p)).To(Succeed())
		Eventually(fetch(p), timeout, interval).Should(WithTransform(uploadURL, ContainSubstring("X-Amz-Signature=")))
		Expect(condition(p.Status.Conditions, v1.ConditionReady).Message).To(ContainSubstring("Content-Type text/plain"))

		By("rejecting and deleting the wrong file")
		Expect(putWithType(p.Status.UploadURL, "text/plain", strings.Repeat("x", len(body)))).To(Equal(http.StatusOK))
		Eventually(fetch(p), timeout, interval).Should(
			WithTransform(func(p *v1.Print) string { return readyReason(p.Status.Conditions) }, Equal("False/"+reasonUploadRejected)))
		Expect(p.Status.Phase).To(Equal(v1.PrintPending))

		By("accepting the right file at the same URL")
		Expect(putWithType(p.Status.UploadURL, "text/plain", body)).To(Equal(http.StatusOK))
		Eventually(fetch(p), timeout, interval).Should(WithTransform(phaseOfPrint, Equal(v1.PrintReady)))
		Expect(p.Status.URL).To(Equal(fakeS3.URL + "/" + testBucket + "/" + p.Status.ObjectKey))
		code, got := httpDo(http.MethodGet, p.Status.URL, "")
		Expect(code).To(Equal(http.StatusOK))
		Expect(got).To(Equal(body))

		By("deleting the object with the Print")
		Expect(k8sClient.Delete(ctx, p)).To(Succeed())
		Eventually(func() int { c, _ := httpDo(http.MethodGet, p.Status.URL, ""); return c }, timeout, interval).
			Should(Equal(http.StatusNotFound))
	})

	It("reads a ClusterArchive's credentials from press's namespace by default", func() {
		name := fmt.Sprintf("s3-%d", nsCounter.Add(1))
		Expect(k8sClient.Create(ctx, s3Credentials(testRouting.Herald.Namespace, name))).To(Succeed())
		ok := &v1.ClusterArchive{Name: name, Spec: v1.ArchiveSpec{S3: s3Source(name)}}
		Expect(k8sClient.Create(ctx, ok)).To(Succeed())
		Eventually(fetch(ok), timeout, interval).Should(
			WithTransform(func(a *v1.ClusterArchive) string { return readyReason(a.Status.Conditions) }, Equal("True/"+v1.ReasonBackendReady)))

		By("reporting missing credentials")
		missing := &v1.ClusterArchive{Name: name + "-nocreds", Spec: v1.ArchiveSpec{S3: s3Source("nowhere")}}
		Expect(k8sClient.Create(ctx, missing)).To(Succeed())
		Eventually(fetch(missing), timeout, interval).Should(
			WithTransform(func(a *v1.ClusterArchive) string { return readyReason(a.Status.Conditions) }, Equal("False/"+v1.ReasonBackendError)))
		Expect(condition(missing.Status.Conditions, v1.ConditionReady).Message).To(ContainSubstring("not found"))

		By("reporting a missing bucket")
		src := s3Source(name)
		src.Bucket = "no-such-bucket"
		noBucket := &v1.ClusterArchive{Name: name + "-nobucket", Spec: v1.ArchiveSpec{S3: src}}
		Expect(k8sClient.Create(ctx, noBucket)).To(Succeed())
		Eventually(fetch(noBucket), timeout, interval).Should(
			WithTransform(func(a *v1.ClusterArchive) string { return condition(a.Status.Conditions, v1.ConditionReady).Message },
				ContainSubstring("no-such-bucket")))
	})
})
