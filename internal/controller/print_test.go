/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

func newPrint(ns, name string, ref v1.ArchiveRef, body string) *v1.Print {
	sum := sha256.Sum256([]byte(body))
	return &v1.Print{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: v1.PrintSpec{
			ArchiveRef: ref, ContentType: "text/plain", Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:]),
		},
	}
}

func phaseOfPrint(p *v1.Print) v1.PrintPhase { return p.Status.Phase }

func readyReason(conds []metav1.Condition) string {
	c := condition(conds, v1.ConditionReady)
	return string(c.Status) + "/" + c.Reason
}

func httpDo(method, url, body string) (int, string) {
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

var _ = Describe("Media", func() {
	const timeout, interval = 10 * time.Second, 100 * time.Millisecond

	var local v1.ArchiveRef
	BeforeEach(func() {
		name := fmt.Sprintf("local-%d", nsCounter.Add(1))
		Expect(k8sClient.Create(ctx, &v1.ClusterArchive{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: v1.ArchiveSpec{PVC: &v1.PVCArchiveSource{
				ClaimName: "manifesto-media-data", PublicURL: darkroomPublic.URL,
			}},
		})).To(Succeed())
		local = v1.ArchiveRef{Kind: v1.KindClusterArchive, Name: name}
	})

	It("reports whether Archives are usable", func() {
		archiveReady := func(a *v1.ClusterArchive) string { return readyReason(a.Status.Conditions) }
		ok := &v1.ClusterArchive{ObjectMeta: metav1.ObjectMeta{Name: local.Name}}
		Eventually(fetch(ok), timeout, interval).Should(WithTransform(archiveReady, Equal("True/"+v1.ReasonBackendReady)))

		other := &v1.ClusterArchive{
			ObjectMeta: metav1.ObjectMeta{Name: local.Name + "-other-claim"},
			Spec:       v1.ArchiveSpec{PVC: &v1.PVCArchiveSource{ClaimName: "elsewhere", PublicURL: "https://media.example.io"}},
		}
		Expect(k8sClient.Create(ctx, other)).To(Succeed())
		Eventually(fetch(other), timeout, interval).Should(WithTransform(archiveReady, Equal("False/"+v1.ReasonBackendError)))
		Expect(condition(other.Status.Conditions, v1.ConditionReady).Message).To(ContainSubstring(`not "elsewhere"`))
	})

	It("stores an uploaded file, shares it within the namespace and deletes it with the last Print", func() {
		ns := newNamespace()
		var nsObj corev1.Namespace
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: ns}, &nsObj)).To(Succeed())
		const body = "hello, darkroom"

		By("handing out an upload URL")
		first := newPrint(ns, "first", local, body)
		Expect(k8sClient.Create(ctx, first)).To(Succeed())
		Eventually(fetch(first), timeout, interval).Should(WithTransform(func(p *v1.Print) string { return p.Status.UploadURL }, Not(BeEmpty())))
		Expect(first.Status.Phase).To(Equal(v1.PrintPending))
		Expect(first.Status.ObjectKey).To(Equal(string(nsObj.UID) + "/sha256/" + first.Spec.SHA256 + ".txt"))
		Expect(first.Status.UploadExpiresAt).NotTo(BeNil())
		Expect(first.Finalizers).To(ContainElement(v1.PrintFinalizer))
		Expect(readyReason(first.Status.Conditions)).To(Equal("False/" + reasonAwaitingUpload))

		By("rejecting a different file at that URL")
		code, _ := httpDo(http.MethodPut, first.Status.UploadURL, strings.Repeat("x", len(body)))
		Expect(code).To(Equal(http.StatusBadRequest))

		By("noticing the upload")
		code, _ = httpDo(http.MethodPut, first.Status.UploadURL, body)
		Expect(code).To(Equal(http.StatusCreated))
		Eventually(fetch(first), timeout, interval).Should(WithTransform(phaseOfPrint, Equal(v1.PrintReady)))
		Expect(first.Status.URL).To(Equal(darkroomPublic.URL + "/" + first.Status.ObjectKey))
		Expect(first.Status.UploadURL).To(BeEmpty())
		code, got := httpDo(http.MethodGet, first.Status.URL, "")
		Expect(code).To(Equal(http.StatusOK))
		Expect(got).To(Equal(body))

		By("reusing the stored file for an identical Print")
		second := newPrint(ns, "second", local, body)
		Expect(k8sClient.Create(ctx, second)).To(Succeed())
		Eventually(fetch(second), timeout, interval).Should(WithTransform(phaseOfPrint, Equal(v1.PrintReady)))
		Expect(second.Status.URL).To(Equal(first.Status.URL))

		By("keeping the file while another Print uses it")
		Expect(k8sClient.Delete(ctx, first)).To(Succeed())
		Eventually(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(first), first)) },
			timeout, interval).Should(BeTrue())
		code, _ = httpDo(http.MethodGet, second.Status.URL, "")
		Expect(code).To(Equal(http.StatusOK))

		By("deleting the file with the last Print")
		Expect(k8sClient.Delete(ctx, second)).To(Succeed())
		Eventually(func() int { c, _ := httpDo(http.MethodGet, second.Status.URL, ""); return c }, timeout, interval).
			Should(Equal(http.StatusNotFound))
	})

	It("keeps namespaces apart", func() {
		const body = "same bytes, different publishers"
		a := newPrint(newNamespace(), "shared", local, body)
		b := newPrint(newNamespace(), "shared", local, body)
		Expect(k8sClient.Create(ctx, a)).To(Succeed())
		Eventually(fetch(a), timeout, interval).Should(WithTransform(func(p *v1.Print) string { return p.Status.UploadURL }, Not(BeEmpty())))
		code, _ := httpDo(http.MethodPut, a.Status.UploadURL, body)
		Expect(code).To(Equal(http.StatusCreated))
		Eventually(fetch(a), timeout, interval).Should(WithTransform(phaseOfPrint, Equal(v1.PrintReady)))

		Expect(k8sClient.Create(ctx, b)).To(Succeed())
		Consistently(fetch(b), 2*time.Second, interval).Should(WithTransform(phaseOfPrint, Not(Equal(v1.PrintReady))))
		Expect(b.Status.ObjectKey).NotTo(Equal(a.Status.ObjectKey))
	})

	It("fails a Print whose file never arrives", func() {
		p := newPrint(newNamespace(), "late", local, "never sent")
		Expect(k8sClient.Create(ctx, p)).To(Succeed())
		Eventually(fetch(p), testUploadTTL+timeout, interval).Should(WithTransform(phaseOfPrint, Equal(v1.PrintFailed)))
		Expect(readyReason(p.Status.Conditions)).To(Equal("False/" + v1.ReasonUploadExpired))
		Expect(p.Status.UploadURL).To(BeEmpty())
	})

	It("waits for a missing Archive", func() {
		ns := newNamespace()
		p := newPrint(ns, "orphan", v1.ArchiveRef{Kind: v1.KindArchive, Name: "media"}, "x")
		Expect(k8sClient.Create(ctx, p)).To(Succeed())
		Eventually(fetch(p), timeout, interval).Should(
			WithTransform(func(p *v1.Print) string { return readyReason(p.Status.Conditions) }, Equal("False/"+v1.ReasonArchiveNotFound)))

		Expect(k8sClient.Create(ctx, &v1.Archive{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "media"},
			Spec:       v1.ArchiveSpec{PVC: &v1.PVCArchiveSource{ClaimName: "manifesto-media-data", PublicURL: darkroomPublic.URL}},
		})).To(Succeed())
		Eventually(fetch(p), timeout, interval).Should(WithTransform(func(p *v1.Print) string { return p.Status.UploadURL }, Not(BeEmpty())))

		By("releasing a Print whose Archive is gone")
		Expect(k8sClient.Delete(ctx, &v1.Archive{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "media"}})).To(Succeed())
		Expect(k8sClient.Delete(ctx, p)).To(Succeed())
		Eventually(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), p)) },
			timeout, interval).Should(BeTrue())
	})

	It("keeps a Dispatch that uses a Print not Ready until the file is stored", func() {
		ns := newNamespace()
		const body = "cover bytes"
		p := newPrint(ns, "cover", local, body)
		Expect(k8sClient.Create(ctx, p)).To(Succeed())
		Expect(k8sClient.Create(ctx, newGazette(ns, "notes", ns+".example.io"))).To(Succeed())
		d := newDispatch(ns, "with-cover", v1.StateDraft)
		d.Spec.Cover = "print:cover"
		d.Spec.Content.Body = "![inline](print:cover) and ![missing](print:nowhere)"
		Expect(k8sClient.Create(ctx, d)).To(Succeed())

		Eventually(fetch(d), timeout, interval).Should(
			WithTransform(dispatchCondition(v1.ConditionReady), Equal("False/"+v1.ReasonPrintNotReady)))
		Expect(condition(d.Status.Conditions, v1.ConditionReady).Message).To(ContainSubstring("cover, nowhere"))

		By("uploading the cover")
		Eventually(fetch(p), timeout, interval).Should(WithTransform(func(p *v1.Print) string { return p.Status.UploadURL }, Not(BeEmpty())))
		code, _ := httpDo(http.MethodPut, p.Status.UploadURL, body)
		Expect(code).To(Equal(http.StatusCreated))
		Eventually(func() string {
			fetch(d)()
			return condition(d.Status.Conditions, v1.ConditionReady).Message
		}, timeout, interval).Should(Equal("Prints not found or not Ready: nowhere"))

		By("dropping the missing reference")
		update(d, func(d *v1.Dispatch) { d.Spec.Content.Body = "![inline](print:cover)" })
		Eventually(fetch(d), timeout, interval).Should(
			WithTransform(dispatchCondition(v1.ConditionReady), Equal("True/"+v1.ReasonResolved)))
	})
})
