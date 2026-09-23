package onboarding

import "testing"

func TestExternalProfileValidationAndScope(t *testing.T) {
	raw := `{"mode":"external","namespace":"payments","workloads":[{"name":"checkout","container":"checkout","podLabelKey":"app.kubernetes.io/name","podLabelValue":"checkout"}],"alerts":[{"name":"CheckoutErrors","workload":"checkout"}],"verifiers":["kubernetes_oom","image_pull"]}`
	p, err := Parse(raw)
	if err != nil || !p.AllowsIncident("payments", "checkout", "CheckoutErrors") || p.AllowsIncident("default", "checkout", "CheckoutErrors") || p.AllowsIncident("payments", "other", "CheckoutErrors") || p.AllowsIncident("payments", "checkout", "OtherAlert") {
		t.Fatalf("unexpected scope: %+v err=%v", p, err)
	}
	if selector, ok := p.PodSelector("checkout"); !ok || selector != "app.kubernetes.io/name=checkout" {
		t.Fatalf("unexpected selector %q", selector)
	}
	for _, invalid := range []string{
		`{"mode":"external","namespace":"kube-system","workloads":[{"name":"checkout","container":"checkout","podLabelKey":"app","podLabelValue":"checkout"}],"alerts":[{"name":"CheckoutErrors","workload":"checkout"}]}`,
		`{"mode":"external","namespace":"payments","workloads":[{"name":"checkout","container":"checkout","podLabelKey":"app","podLabelValue":"checkout"}],"alerts":[{"name":"CheckoutErrors","workload":"other"}]}`,
		`{"mode":"external","namespace":"payments","workloads":[{"name":"../secrets","container":"checkout","podLabelKey":"app","podLabelValue":"checkout"}],"alerts":[{"name":"CheckoutErrors","workload":"../secrets"}]}`,
		`{"mode":"external","namespace":"payments","workloads":[{"name":"checkout","container":"checkout","podLabelKey":"app","podLabelValue":"checkout"}],"alerts":[{"name":"CheckoutErrors","workload":"checkout"}],"extra":true}`,
	} {
		if _, err := Parse(invalid); err == nil {
			t.Fatalf("accepted unsafe profile: %s", invalid)
		}
	}
}
