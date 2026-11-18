/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

func inlineTypeset(dispatchTemplate string) v1.TypesetSpec {
	return v1.TypesetSpec{Source: v1.TypesetSource{Inline: &v1.InlineTypesetSource{Templates: map[string]string{
		"layout.html":   `{{ block "content" . }}{{ end }}`,
		"dispatch.html": dispatchTemplate,
		"index.html":    `{{ define "content" }}index{{ end }}`,
	}}}}
}

// update re-reads obj, applies mutate and updates it, retrying on conflicts
// with press's own status writes.
func update[T client.Object](obj T, mutate func(T)) {
	ExpectWithOffset(1, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
			return err
		}
		mutate(obj)
		return k8sClient.Update(ctx, obj)
	})).To(Succeed())
}

func conditionOf(conds []metav1.Condition) string {
	c := condition(conds, v1.ConditionReady)
	return string(c.Status) + "/" + c.Reason
}

var _ = Describe("M2 references", func() {
	const timeout, interval = 10 * time.Second, 100 * time.Millisecond

	var ns string
	BeforeEach(func() {
		ns = newNamespace()
		Expect(k8sClient.Create(ctx, newGazette(ns, "notes", ns+".example.io"))).To(Succeed())
	})

	It("resolves scribeRef once the Scribe exists", func() {
		d := newDispatch(ns, "by-bob", v1.StateDraft)
		d.Spec.ScribeRef = "bob"
		Expect(k8sClient.Create(ctx, d)).To(Succeed())
		Eventually(fetch(d), timeout, interval).Should(
			WithTransform(dispatchCondition(v1.ConditionReady), Equal("False/"+v1.ReasonScribeNotFound)))

		Expect(k8sClient.Create(ctx, &v1.Scribe{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "bob"},
			Spec:       v1.ScribeSpec{DisplayName: "Bob"},
		})).To(Succeed())
		Eventually(fetch(d), timeout, interval).Should(
			WithTransform(dispatchCondition(v1.ConditionReady), Equal("True/"+v1.ReasonResolved)))
	})

	It("follows the Gazette's theme through errors and fixes", func() {
		name := fmt.Sprintf("theme-%d", nsCounter.Add(1))
		ts := &v1.ClusterTypeset{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: inlineTypeset(`{{ define "content" }}{{ .Dispatch.Title }`)}
		Expect(k8sClient.Create(ctx, ts)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, ts) })
		Eventually(fetch(ts), timeout, interval).Should(
			WithTransform(func(t *v1.ClusterTypeset) string { return conditionOf(t.Status.Conditions) }, Equal("False/"+v1.ReasonTemplateError)))

		g := &v1.Gazette{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "notes"}}
		update(g, func(g *v1.Gazette) {
			g.Spec.TypesetRef = &v1.TypesetRef{Kind: v1.KindClusterTypeset, Name: name}
		})
		d := newDispatch(ns, "themed", v1.StateDraft)
		Expect(k8sClient.Create(ctx, d)).To(Succeed())

		gazetteReady := func(g *v1.Gazette) string { return conditionOf(g.Status.Conditions) }
		Eventually(fetch(g), timeout, interval).Should(WithTransform(gazetteReady, Equal("False/"+v1.ReasonTemplateError)))
		Eventually(fetch(d), timeout, interval).Should(
			WithTransform(dispatchCondition(v1.ConditionRendered), Equal("False/"+v1.ReasonTemplateError)))

		By("fixing the theme")
		update(ts, func(ts *v1.ClusterTypeset) {
			ts.Spec = inlineTypeset(`{{ define "content" }}{{ .Dispatch.Title }}{{ end }}`)
		})
		Eventually(fetch(g), timeout, interval).Should(WithTransform(gazetteReady, Equal("True/"+v1.ReasonResolved)))
		Eventually(fetch(d), timeout, interval).Should(
			WithTransform(dispatchCondition(v1.ConditionRendered), Equal("True/"+v1.ReasonRenderSucceeded)))

		By("using a field the Dispatch does not have")
		update(ts, func(ts *v1.ClusterTypeset) {
			ts.Spec = inlineTypeset(`{{ define "content" }}{{ .Dispatch.Section.DisplayName }}{{ end }}`)
		})
		Eventually(fetch(d), timeout, interval).Should(
			WithTransform(dispatchCondition(v1.ConditionRendered), Equal("False/"+v1.ReasonTemplateError)))

		By("labelling the Dispatch with a Section")
		update(d, func(d *v1.Dispatch) {
			d.Labels = map[string]string{v1.SectionLabel: "tech"}
		})
		Eventually(fetch(d), timeout, interval).Should(
			WithTransform(dispatchCondition(v1.ConditionRendered), Equal("True/"+v1.ReasonRenderSucceeded)))
	})

	It("falls back to the built-in theme when the Typeset is missing", func() {
		g := &v1.Gazette{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "notes"}}
		update(g, func(g *v1.Gazette) {
			g.Spec.TypesetRef = &v1.TypesetRef{Kind: v1.KindTypeset, Name: "nowhere"}
		})
		d := newDispatch(ns, "fallback", v1.StateDraft)
		Expect(k8sClient.Create(ctx, d)).To(Succeed())

		Eventually(fetch(g), timeout, interval).Should(
			WithTransform(func(g *v1.Gazette) string { return conditionOf(g.Status.Conditions) }, Equal("False/"+v1.ReasonTypesetNotFound)))
		Eventually(fetch(d), timeout, interval).Should(
			WithTransform(dispatchCondition(v1.ConditionRendered), Equal("True/"+v1.ReasonRenderSucceeded)))

		By("creating the namespaced Typeset")
		Expect(k8sClient.Create(ctx, &v1.Typeset{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "nowhere"},
			Spec:       inlineTypeset(`{{ define "content" }}ok{{ end }}`),
		})).To(Succeed())
		Eventually(fetch(g), timeout, interval).Should(
			WithTransform(func(g *v1.Gazette) string { return conditionOf(g.Status.Conditions) }, Equal("True/"+v1.ReasonResolved)))
	})

	It("checks Section parents", func() {
		sectionReady := func(s *v1.Section) string { return conditionOf(s.Status.Conditions) }
		child := &v1.Section{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "go"}, Spec: v1.SectionSpec{Parent: "tech"}}
		Expect(k8sClient.Create(ctx, child)).To(Succeed())
		Eventually(fetch(child), timeout, interval).Should(WithTransform(sectionReady, Equal("False/"+v1.ReasonParentNotFound)))

		parent := &v1.Section{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "tech"}}
		Expect(k8sClient.Create(ctx, parent)).To(Succeed())
		Eventually(fetch(child), timeout, interval).Should(WithTransform(sectionReady, Equal("True/"+v1.ReasonResolved)))

		By("making the parent point back at the child")
		update(parent, func(parent *v1.Section) {
			parent.Spec.Parent = "go"
		})
		Eventually(fetch(child), timeout, interval).Should(WithTransform(sectionReady, Equal("False/"+v1.ReasonParentCycle)))
		Eventually(fetch(parent), timeout, interval).Should(WithTransform(sectionReady, Equal("False/"+v1.ReasonParentCycle)))
	})
})
