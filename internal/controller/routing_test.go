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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

var _ = Describe("Gazette routing", func() {
	const timeout, interval = 10 * time.Second, 100 * time.Millisecond

	routed := func(g *v1.Gazette) string {
		c := condition(g.Status.Conditions, v1.ConditionRouted)
		return string(c.Status) + "/" + c.Reason
	}
	listenerSet := func(ns, gazette string) *gatewayv1.ListenerSet {
		return &gatewayv1.ListenerSet{Namespace: ns, Name: "manifesto-" + gazette}
	}
	grantFor := func(ns string) *gatewayv1.ReferenceGrant {
		return &gatewayv1.ReferenceGrant{Namespace: testRouting.Herald.Namespace, Name: "manifesto-" + ns}
	}
	exists := func(obj client.Object) func() error {
		return func() error { return k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), obj) }
	}

	It("exposes a Gazette's domain through a ListenerSet, HTTPRoutes and a Certificate", func() {
		ns := newNamespace()
		dom := ns + ".example.io"
		g := newGazette(ns, "notes", dom)
		Expect(k8sClient.Create(ctx, g)).To(Succeed())

		By("creating the ListenerSet on the shared Gateway")
		ls := listenerSet(ns, "notes")
		Eventually(exists(ls), timeout, interval).Should(Succeed())
		Expect(string(ls.Spec.ParentRef.Name)).To(Equal("public"))
		Expect(string(*ls.Spec.ParentRef.Namespace)).To(Equal("infra"))
		Expect(ls.Spec.Listeners).To(HaveLen(2))
		https := ls.Spec.Listeners[0]
		Expect(string(https.Name)).To(Equal("https"))
		Expect(string(*https.Hostname)).To(Equal(dom))
		Expect(https.Port).To(BeEquivalentTo(443))
		Expect(string(https.TLS.CertificateRefs[0].Name)).To(Equal("manifesto-notes-tls"))
		Expect(ls.OwnerReferences).To(ConsistOf(HaveField("UID", g.UID)))

		By("routing HTTPS to herald and redirecting HTTP")
		route := &gatewayv1.HTTPRoute{Namespace: ns, Name: "manifesto-notes"}
		Eventually(exists(route), timeout, interval).Should(Succeed())
		Expect(route.Spec.Hostnames).To(ConsistOf(gatewayv1.Hostname(dom)))
		Expect(string(*route.Spec.ParentRefs[0].SectionName)).To(Equal("https"))
		backend := route.Spec.Rules[0].BackendRefs[0]
		Expect(string(backend.Name)).To(Equal("manifesto-herald"))
		Expect(string(*backend.Namespace)).To(Equal("manifesto-system"))
		redirect := &gatewayv1.HTTPRoute{Namespace: ns, Name: "manifesto-notes-redirect"}
		Eventually(exists(redirect), timeout, interval).Should(Succeed())
		Expect(*redirect.Spec.Rules[0].Filters[0].RequestRedirect.Scheme).To(Equal("https"))

		By("asking cert-manager for the certificate")
		cert := &unstructured.Unstructured{}
		cert.SetGroupVersionKind(certificateGVK)
		cert.SetNamespace(ns)
		cert.SetName("manifesto-notes")
		Eventually(exists(cert), timeout, interval).Should(Succeed())
		Expect(cert.Object["spec"]).To(Equal(map[string]any{
			"secretName": "manifesto-notes-tls",
			"dnsNames":   []any{dom},
			"issuerRef":  map[string]any{"group": "cert-manager.io", "kind": "ClusterIssuer", "name": "letsencrypt"},
		}))

		By("letting the namespace's routes reach herald")
		grant := grantFor(ns)
		Eventually(exists(grant), timeout, interval).Should(Succeed())
		Expect(string(grant.Spec.From[0].Namespace)).To(Equal(ns))
		Expect(string(*grant.Spec.To[0].Name)).To(Equal("manifesto-herald"))

		By("reporting Routed once the Gateway implementation accepts them")
		Eventually(fetch(g), timeout, interval).Should(WithTransform(routed, Equal("False/"+v1.ReasonRoutePending)))
		Expect(exists(ls)()).To(Succeed())
		meta.SetStatusCondition(&ls.Status.Conditions, metav1.Condition{
			Type: "Programmed", Status: metav1.ConditionTrue, Reason: "Programmed", ObservedGeneration: ls.Generation,
		})
		Expect(k8sClient.Status().Update(ctx, ls)).To(Succeed())
		Expect(exists(route)()).To(Succeed())
		route.Status.Parents = []gatewayv1.RouteParentStatus{{
			ParentRef:      route.Spec.ParentRefs[0],
			ControllerName: "example.io/gateway",
			Conditions: []metav1.Condition{{
				Type: "Accepted", Status: metav1.ConditionTrue, Reason: "Accepted",
				ObservedGeneration: route.Generation, LastTransitionTime: metav1.Now(),
			}},
		}}
		Expect(k8sClient.Status().Update(ctx, route)).To(Succeed())
		Eventually(fetch(g), timeout, interval).Should(WithTransform(routed, Equal("True/"+v1.ReasonProgrammed)))

		By("following a domain change")
		update(g, func(g *v1.Gazette) { g.Spec.Domain = "new-" + dom })
		Eventually(func() string {
			Expect(exists(ls)()).To(Succeed())
			return string(*ls.Spec.Listeners[0].Hostname)
		}, timeout, interval).Should(Equal("new-" + dom))

		By("removing the ReferenceGrant with the namespace's last Gazette")
		Expect(k8sClient.Delete(ctx, g)).To(Succeed())
		Eventually(func() bool { return apierrors.IsNotFound(exists(grant)()) }, timeout, interval).Should(BeTrue())
	})

	It("does not route a Gazette that claims a reserved domain", func() {
		g := newGazette(newNamespace(), "squatter", "desk.reserved.test")
		Expect(k8sClient.Create(ctx, g)).To(Succeed())
		Eventually(fetch(g), timeout, interval).Should(WithTransform(routed, Equal("False/"+v1.ReasonDomainReserved)))
		Expect(condition(g.Status.Conditions, v1.ConditionReady).Reason).To(Equal(v1.ReasonDomainReserved))
		Consistently(func() bool {
			return apierrors.IsNotFound(exists(listenerSet(g.Namespace, "squatter"))())
		}, time.Second, interval).Should(BeTrue())
	})

	It("does not route a Gazette that lost its domain", func() {
		dom := fmt.Sprintf("taken-%d.example.io", nsCounter.Add(1))
		first := newGazette(newNamespace(), "first", dom)
		Expect(k8sClient.Create(ctx, first)).To(Succeed())
		Eventually(exists(listenerSet(first.Namespace, "first")), timeout, interval).Should(Succeed())

		time.Sleep(1100 * time.Millisecond) // creationTimestamp has second precision
		second := newGazette(newNamespace(), "second", dom)
		Expect(k8sClient.Create(ctx, second)).To(Succeed())
		Eventually(fetch(second), timeout, interval).Should(WithTransform(routed, Equal("False/"+v1.ReasonDomainConflict)))
		Consistently(func() bool {
			return apierrors.IsNotFound(exists(listenerSet(second.Namespace, "second"))())
		}, time.Second, interval).Should(BeTrue())

		By("routing the second one once the first is gone")
		Expect(k8sClient.Delete(ctx, first)).To(Succeed())
		Eventually(exists(listenerSet(second.Namespace, "second")), timeout, interval).Should(Succeed())
		Eventually(fetch(second), timeout, interval).Should(WithTransform(routed, Equal("False/"+v1.ReasonRoutePending)))
	})
})
