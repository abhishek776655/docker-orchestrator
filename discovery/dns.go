// Package discovery implements the M7 service-name DNS resolver: workloads
// reach each other by Compose-style logical names (e.g.
// http://metaapi:5000/) resolved against the same live registry
// controlplane.ListServiceInstances already serves over HTTP (SRS 8.3
// "platform DNS mechanism"), instead of injecting endpoint env vars.
package discovery

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"

	"github.com/miekg/dns"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
)

// Registry is the lookup the resolver needs; controlplane.Service
// satisfies it via its existing ListServiceInstances method.
type Registry interface {
	ListServiceInstances(ctx context.Context, project, service string) ([]model.ServiceInstance, error)
}

// Server answers A-record queries for "<service>.<project>.<suffix>."
// from the live registry. Unknown names return NXDOMAIN; a workload with
// no ready instance simply has no records, so it also resolves as
// NXDOMAIN until it becomes ready (SRS review focus: "A failed or waiting
// workload must disappear from DNS answers immediately").
type Server struct {
	registry Registry
	suffix   string // e.g. "haco", without leading/trailing dots
	dns      *dns.Server
}

func NewServer(registry Registry, suffix string) *Server {
	suffix = strings.Trim(suffix, ".")
	s := &Server{registry: registry, suffix: suffix}

	mux := dns.NewServeMux()
	mux.HandleFunc(suffix+".", s.handle)
	s.dns = &dns.Server{Handler: mux}
	return s
}

// ListenAndServe runs the UDP DNS server on addr until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	s.dns.Addr = addr
	s.dns.Net = "udp"

	errCh := make(chan error, 1)
	go func() { errCh <- s.dns.ListenAndServe() }()

	select {
	case <-ctx.Done():
		return s.dns.ShutdownContext(context.Background())
	case err := <-errCh:
		return err
	}
}

func (s *Server) handle(w dns.ResponseWriter, r *dns.Msg) {
	if err := w.WriteMsg(s.Answer(r)); err != nil {
		log.Printf("dns: write response failed: %v", err)
	}
}

// Answer builds the reply for one query against the live registry,
// separated from handle so it can be exercised directly in tests without
// binding a real UDP socket.
func (s *Server) Answer(r *dns.Msg) *dns.Msg {
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true

	for _, q := range r.Question {
		if q.Qtype != dns.TypeA {
			continue
		}
		s.answerA(m, q)
	}

	if len(m.Answer) == 0 && m.Rcode == dns.RcodeSuccess {
		m.Rcode = dns.RcodeNameError // NXDOMAIN: unknown or not-yet-ready service
	}
	return m
}

func (s *Server) answerA(m *dns.Msg, q dns.Question) {
	service, project, ok := s.parseName(q.Name)
	if !ok {
		return
	}

	instances, err := s.registry.ListServiceInstances(context.Background(), project, service)
	if err != nil {
		log.Printf("dns: lookup %s.%s failed: %v", service, project, err)
		m.Rcode = dns.RcodeServerFailure
		return
	}

	// Deterministic answer order for multiple ready replicas (review
	// focus: "must return each ready address deterministically").
	for _, inst := range instances {
		ip, err := resolveToIP(inst.Address)
		if err != nil {
			log.Printf("dns: resolve node address %q: %v", inst.Address, err)
			continue
		}
		rr, err := dns.NewRR(fmt.Sprintf("%s 5 IN A %s", q.Name, ip))
		if err != nil {
			log.Printf("dns: build record for %s: %v", ip, err)
			continue
		}
		m.Answer = append(m.Answer, rr)
	}
}

// resolveToIP accepts a node's Address as either a literal IP (the normal,
// production case) or a hostname. A hostname needs resolving because an A
// record's RDATA must be a literal IP — it can't itself name another host.
// This also covers this project's own local-simulation addresses like
// "host.docker.internal" (SRS M3/M4/M5/M6 docs: one shared Docker daemon
// standing in for two real hosts).
func resolveToIP(address string) (string, error) {
	if ip := net.ParseIP(address); ip != nil {
		return ip.String(), nil
	}
	ips, err := net.LookupIP(address)
	if err != nil {
		return "", err
	}
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			return v4.String(), nil
		}
	}
	return "", fmt.Errorf("no IPv4 address found for %q", address)
}

// parseName splits "<service>.<project>.<suffix>." into (service, project).
// Any other shape — wrong suffix, wrong label count, a name from another
// project's suffix — is rejected so one project's services can never
// resolve by short name in another's namespace.
func (s *Server) parseName(name string) (service, project string, ok bool) {
	labels := dns.SplitDomainName(name)
	if len(labels) != 3 {
		return "", "", false
	}
	if !strings.EqualFold(labels[2], s.suffix) {
		return "", "", false
	}
	return labels[0], labels[1], true
}
