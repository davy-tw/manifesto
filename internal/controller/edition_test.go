/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"slices"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

var _ = Describe("Editions", func() {
	const timeout, interval = 10 * time.Second, 100 * time.Millisecond

	var ns string
	BeforeEach(func() {
		ns = newNamespace()
	})

	// editions returns "<number>:<title>" for each Edition of name, by number.
	editions := func(name string) func() []string {
		return func() []string {
			var list v1.EditionList
			Expect(k8sClient.List(ctx, &list, client.InNamespace(ns),
				client.MatchingLabels{v1.EditionDispatchLabel: name})).To(Succeed())
			slices.SortFunc(list.Items, func(a, b v1.Edition) int { return int(a.Spec.Number - b.Spec.Number) })
			out := make([]string, 0, len(list.Items))
			for _, e := range list.Items {
				out = append(out, e.Name+"="+e.Spec.Snapshot.Title)
			}
			return out
		}
	}

	It("records a change of content, not of publishing, and keeps the latest", func() {
		d := newDispatch(ns, "story", v1.StateDraft)
		d.Spec.Title = "v1"
		Expect(k8sClient.Create(ctx, d)).To(Succeed())
		Eventually(editions("story"), timeout, interval).Should(Equal([]string{"story-1=v1"}))

		var first v1.Edition
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: "story-1"}, &first)).To(Succeed())
		Expect(first.Spec.DispatchRef).To(Equal("story"))
		Expect(first.Spec.Digest).To(Equal(Digest(SnapshotOf(&d.Spec))))
		Expect(metav1.GetControllerOf(&first).UID).To(Equal(d.UID), "deleting the Dispatch deletes its Editions")

		By("publishing, which is not a change of content")
		update(d, func(d *v1.Dispatch) { d.Spec.State = v1.StatePublished })
		Eventually(fetch(d), timeout, interval).Should(WithTransform(
			func(d *v1.Dispatch) v1.DispatchPhase { return d.Status.Phase }, Equal(v1.PhasePublished)))
		Consistently(editions("story"), time.Second, interval).Should(Equal([]string{"story-1=v1"}))

		By("editing three more times, beyond the limit of three")
		for _, title := range []string{"v2", "v3", "v4"} {
			update(d, func(d *v1.Dispatch) { d.Spec.Title = title })
			Eventually(editions("story"), timeout, interval).Should(ContainElement(HaveSuffix("=" + title)))
		}
		Eventually(editions("story"), timeout, interval).Should(Equal([]string{"story-2=v2", "story-3=v3", "story-4=v4"}))

		By("restoring an old Edition, which becomes the newest")
		update(d, func(d *v1.Dispatch) { d.Spec.Title = "v2" })
		Eventually(editions("story"), timeout, interval).Should(Equal([]string{"story-3=v3", "story-4=v4", "story-5=v2"}))
	})

	It("refuses changes to an Edition", func() {
		d := newDispatch(ns, "fixed", v1.StateDraft)
		Expect(k8sClient.Create(ctx, d)).To(Succeed())
		Eventually(editions("fixed"), timeout, interval).Should(HaveLen(1))
		var e v1.Edition
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: "fixed-1"}, &e)).To(Succeed())
		e.Spec.Snapshot.Title = "rewritten history"
		err := k8sClient.Update(ctx, &e)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("cannot be changed"))
	})
})
