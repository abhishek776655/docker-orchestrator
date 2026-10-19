package discovery_test

import (
	"context"
	"testing"

	"github.com/miekg/dns"

	"github.com/abhishek/hardware-aware-container-orchestrator/discovery"
	"github.com/abhishek/hardware-aware-container-orchestrator/model"
)

// fakeRegistry is an in-memory stand-in for controlplane.Service, keyed by
// "project/service", so the resolver can be tested without a database.
type fakeRegistry map[string][]model.ServiceInstance

func (f fakeRegistry) ListServiceInstances(_ context.Context, project, service string) ([]model.ServiceInstance, error) {
	return f[project+"/"+service], nil
}

func answer(srv *discovery.Server, name string) *dns.Msg {
	q := new(dns.Msg)
	q.SetQuestion(dns.Fqdn(name), dns.TypeA)
	return srv.Answer(q)
}

func TestResolverReturnsReadyInstance(t *testing.T) {
	reg := fakeRegistry{
		"demo/metaapi": {{Project: "demo", Service: "metaapi", NodeID: "node-a", Address: "10.0.0.5", ContainerPort: 5000, Status: model.StatusReady}},
	}
	srv := discovery.NewServer(reg, "haco")

	resp := answer(srv, "metaapi.demo.haco.")
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("rcode = %d, want success", resp.Rcode)
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("expected 1 answer, got %d", len(resp.Answer))
	}
	a, ok := resp.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("expected an A record, got %T", resp.Answer[0])
	}
	if a.A.String() != "10.0.0.5" {
		t.Fatalf("address = %s, want 10.0.0.5", a.A.String())
	}
}

func TestResolverNXDOMAINForUnknownService(t *testing.T) {
	srv := discovery.NewServer(fakeRegistry{}, "haco")
	resp := answer(srv, "doesnotexist.demo.haco.")
	if resp.Rcode != dns.RcodeNameError {
		t.Fatalf("rcode = %d, want NXDOMAIN (%d)", resp.Rcode, dns.RcodeNameError)
	}
}

func TestResolverNXDOMAINWhenNotReady(t *testing.T) {
	// A service with no ready instance (e.g. still "waiting_dependencies")
	// must resolve as absent, not as a stale address.
	reg := fakeRegistry{"demo/metaapi": nil}
	srv := discovery.NewServer(reg, "haco")
	resp := answer(srv, "metaapi.demo.haco.")
	if resp.Rcode != dns.RcodeNameError {
		t.Fatalf("rcode = %d, want NXDOMAIN", resp.Rcode)
	}
}

func TestResolverIsProjectScoped(t *testing.T) {
	reg := fakeRegistry{
		"demo/metaapi":  {{Address: "10.0.0.5", ContainerPort: 5000, Status: model.StatusReady}},
		"other/metaapi": {{Address: "10.0.0.9", ContainerPort: 5000, Status: model.StatusReady}},
	}
	srv := discovery.NewServer(reg, "haco")

	resp := answer(srv, "metaapi.demo.haco.")
	if len(resp.Answer) != 1 {
		t.Fatalf("expected 1 answer, got %d", len(resp.Answer))
	}
	a := resp.Answer[0].(*dns.A)
	if a.A.String() != "10.0.0.5" {
		t.Fatalf("demo project resolved to %s, want its own instance 10.0.0.5, not other project's", a.A.String())
	}
}

func TestResolverReturnsMultipleReplicasDeterministically(t *testing.T) {
	reg := fakeRegistry{
		"demo/metaapi": {
			{Address: "10.0.0.5", ContainerPort: 5000, Status: model.StatusReady},
			{Address: "10.0.0.6", ContainerPort: 5000, Status: model.StatusReady},
		},
	}
	srv := discovery.NewServer(reg, "haco")

	var first []string
	for i := 0; i < 5; i++ {
		resp := answer(srv, "metaapi.demo.haco.")
		var got []string
		for _, rr := range resp.Answer {
			got = append(got, rr.(*dns.A).A.String())
		}
		if first == nil {
			first = got
			continue
		}
		if len(got) != len(first) {
			t.Fatalf("answer count changed across queries: %v vs %v", got, first)
		}
		for i := range got {
			if got[i] != first[i] {
				t.Fatalf("non-deterministic answer order: %v vs %v", got, first)
			}
		}
	}
}

func TestResolverIgnoresWrongSuffix(t *testing.T) {
	reg := fakeRegistry{"demo/metaapi": {{Address: "10.0.0.5", ContainerPort: 5000, Status: model.StatusReady}}}
	srv := discovery.NewServer(reg, "haco")

	resp := answer(srv, "metaapi.demo.example.com.")
	if resp.Rcode != dns.RcodeNameError {
		t.Fatalf("rcode = %d, want NXDOMAIN for a name outside the haco suffix", resp.Rcode)
	}
}
