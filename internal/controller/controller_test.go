/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"fmt"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

var nsCounter atomic.Int32

// newNamespace creates a fresh namespace for one test.
func newNamespace() string {
	name := fmt.Sprintf("press-test-%d", nsCounter.Add(1))
	Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})).To(Succeed())
	return name
}

func newGazette(ns, name, dom string) *v1.Gazette {
	return &v1.Gazette{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       v1.GazetteSpec{Title: "Test Gazette", Domain: dom},
	}
}

func newDispatch(ns, name string, state v1.DispatchState) *v1.Dispatch {
	return &v1.Dispatch{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: v1.DispatchSpec{
			GazetteRef: "notes",
			Title:      "Hello",
			State:      state,
			Content:    v1.DispatchContent{Body: "# Hello\n\nworld"},
		},
	}
}

// fetch returns a func that reads the latest version of obj, for Eventually.
func fetch[T client.Object](obj T) func() T {
	return func() T {
		ExpectWithOffset(1, k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), obj)).To(Succeed())
		return obj
	}
}

func phaseOf(d *v1.Dispatch) v1.DispatchPhase { return d.Status.Phase }

func condition(conds []metav1.Condition, t string) metav1.Condition {
	if c := meta.FindStatusCondition(conds, t); c != nil {
		return *c
	}
	return metav1.Condition{}
}

func dispatchCondition(t string) func(*v1.Dispatch) string {
	return func(d *v1.Dispatch) string {
		c := condition(d.Status.Conditions, t)
		return string(c.Status) + "/" + c.Reason
	}
}

var _ = Describe("Dispatch controller", func() {
	const timeout, interval = 10 * time.Second, 100 * time.Millisecond

	var ns string
	BeforeEach(func() { ns = newNamespace() })

	It("keeps a Draft as Draft and reports the missing Gazette", func() {
		d := newDispatch(ns, "draft", v1.StateDraft)
		Expect(k8sClient.Create(ctx, d)).To(Succeed())

		Eventually(fetch(d), timeout, interval).Should(WithTransform(phaseOf, Equal(v1.PhaseDraft)))
		Expect(d.Status.PublishedAt).To(BeNil())
		Expect(d.Status.URL).To(BeEmpty())
		Expect(d.Status.ObservedGeneration).To(Equal(d.Generation))
		Expect(dispatchCondition(v1.ConditionReady)(d)).To(Equal("False/" + v1.ReasonGazetteNotFound))
		Expect(dispatchCondition(v1.ConditionRendered)(d)).To(Equal("True/" + v1.ReasonRenderSucceeded))
	})

	It("publishes immediately and becomes Ready once its Gazette appears", func() {
		d := newDispatch(ns, "hello", v1.StatePublished)
		Expect(k8sClient.Create(ctx, d)).To(Succeed())
		Eventually(fetch(d), timeout, interval).Should(WithTransform(phaseOf, Equal(v1.PhasePublished)))
		Expect(d.Status.PublishedAt).NotTo(BeNil())
		Expect(dispatchCondition(v1.ConditionReady)(d)).To(Equal("False/" + v1.ReasonGazetteNotFound))

		Expect(k8sClient.Create(ctx, newGazette(ns, "notes", ns+".example.io"))).To(Succeed())
		Eventually(fetch(d), timeout, interval).Should(
			WithTransform(dispatchCondition(v1.ConditionReady), Equal("True/"+v1.ReasonResolved)))
		Expect(d.Status.URL).To(Equal("https://" + ns + ".example.io/posts/hello"))
	})

	It("holds a scheduled Dispatch as Embargoed and publishes it at publishAt", func() {
		Expect(k8sClient.Create(ctx, newGazette(ns, "notes", ns+".example.io"))).To(Succeed())
		publishAt := metav1.NewTime(time.Now().Add(3 * time.Second).Truncate(time.Second))
		d := newDispatch(ns, "scheduled", v1.StateUnlisted)
		d.Spec.PublishAt = &publishAt
		Expect(k8sClient.Create(ctx, d)).To(Succeed())

		Eventually(fetch(d), timeout, interval).Should(WithTransform(phaseOf, Equal(v1.PhaseEmbargoed)))
		Expect(d.Status.PublishedAt).To(BeNil())

		Eventually(fetch(d), timeout, interval).Should(WithTransform(phaseOf, Equal(v1.PhaseUnlisted)))
		Expect(d.Status.PublishedAt.Time).To(BeTemporally("==", publishAt.Time))
		Expect(time.Now()).To(BeTemporally(">=", publishAt.Time))
	})

	It("keeps the first publishedAt when a Dispatch goes back to Draft and is republished", func() {
		d := newDispatch(ns, "roundtrip", v1.StatePublished)
		Expect(k8sClient.Create(ctx, d)).To(Succeed())
		Eventually(fetch(d), timeout, interval).Should(WithTransform(phaseOf, Equal(v1.PhasePublished)))
		first := d.Status.PublishedAt.DeepCopy()

		d.Spec.State = v1.StateDraft
		Expect(k8sClient.Update(ctx, d)).To(Succeed())
		Eventually(fetch(d), timeout, interval).Should(WithTransform(phaseOf, Equal(v1.PhaseDraft)))
		Expect(d.Status.PublishedAt.Equal(first)).To(BeTrue())

		// A future publishAt no longer embargoes a Dispatch that has been visible.
		future := metav1.NewTime(time.Now().Add(time.Hour))
		d.Spec.State = v1.StatePublished
		d.Spec.PublishAt = &future
		Expect(k8sClient.Update(ctx, d)).To(Succeed())
		Eventually(fetch(d), timeout, interval).Should(WithTransform(phaseOf, Equal(v1.PhasePublished)))
		Expect(d.Status.PublishedAt.Equal(first)).To(BeTrue())
		Expect(d.Status.ObservedGeneration).To(Equal(d.Generation))
	})

	It("retracts a Dispatch", func() {
		d := newDispatch(ns, "gone", v1.StateRetracted)
		Expect(k8sClient.Create(ctx, d)).To(Succeed())
		Eventually(fetch(d), timeout, interval).Should(WithTransform(phaseOf, Equal(v1.PhaseRetracted)))
	})

	It("reports a scribeRef that does not resolve", func() {
		Expect(k8sClient.Create(ctx, newGazette(ns, "notes", ns+".example.io"))).To(Succeed())
		d := newDispatch(ns, "by-alice", v1.StateDraft)
		d.Spec.ScribeRef = "alice"
		Expect(k8sClient.Create(ctx, d)).To(Succeed())
		Eventually(fetch(d), timeout, interval).Should(
			WithTransform(dispatchCondition(v1.ConditionReady), Equal("False/"+v1.ReasonScribeNotFound)))
	})

	It("writes status as the manifesto-press field manager", func() {
		d := newDispatch(ns, "owned", v1.StateDraft)
		Expect(k8sClient.Create(ctx, d)).To(Succeed())
		Eventually(fetch(d), timeout, interval).Should(WithTransform(phaseOf, Equal(v1.PhaseDraft)))
		var managers []string
		for _, f := range d.ManagedFields {
			if f.Subresource == "status" {
				managers = append(managers, f.Manager+"/"+string(f.Operation))
			}
		}
		Expect(managers).To(ConsistOf(v1.FieldManagerPress + "/Apply"))
	})
})

var _ = Describe("Gazette controller", func() {
	const timeout, interval = 10 * time.Second, 100 * time.Millisecond

	gazetteReady := func(g *v1.Gazette) string {
		c := condition(g.Status.Conditions, v1.ConditionReady)
		return string(c.Status) + "/" + c.Reason
	}

	It("marks a Gazette with its own domain Ready", func() {
		ns := newNamespace()
		g := newGazette(ns, "notes", ns+".example.io")
		Expect(k8sClient.Create(ctx, g)).To(Succeed())
		Eventually(fetch(g), timeout, interval).Should(WithTransform(gazetteReady, Equal("True/"+v1.ReasonResolved)))
		Expect(g.Status.URL).To(Equal("https://" + ns + ".example.io"))
		Expect(g.Status.ObservedGeneration).To(Equal(g.Generation))
	})

	It("lets the oldest Gazette keep a contested domain and hands it over on deletion", func() {
		dom := fmt.Sprintf("contested-%d.example.io", nsCounter.Add(1))
		first := newGazette(newNamespace(), "first", dom)
		Expect(k8sClient.Create(ctx, first)).To(Succeed())
		Eventually(fetch(first), timeout, interval).Should(WithTransform(gazetteReady, Equal("True/"+v1.ReasonResolved)))

		// creationTimestamp has second precision; make sure the second one is newer.
		time.Sleep(1100 * time.Millisecond)
		second := newGazette(newNamespace(), "second", dom)
		Expect(k8sClient.Create(ctx, second)).To(Succeed())
		Eventually(fetch(second), timeout, interval).Should(WithTransform(gazetteReady, Equal("False/"+v1.ReasonDomainConflict)))
		Consistently(fetch(first), time.Second, interval).Should(WithTransform(gazetteReady, Equal("True/"+v1.ReasonResolved)))

		Expect(k8sClient.Delete(ctx, first)).To(Succeed())
		Eventually(fetch(second), timeout, interval).Should(WithTransform(gazetteReady, Equal("True/"+v1.ReasonResolved)))
	})
})
