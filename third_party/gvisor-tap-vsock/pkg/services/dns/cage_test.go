// SPDX-License-Identifier: Apache-2.0
package dns

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/miekg/dns"
)

type cageWriter struct {
	dns.ResponseWriter
	msg *dns.Msg
}

func (w *cageWriter) WriteMsg(m *dns.Msg) error { w.msg = m; return nil }

func TestCageDNSFailureDoesNotUseHostResolver(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	w := &cageWriter{}
	// A nil upstream would panic if the ordinary resolver path were used.
	h := &dnsHandler{exchange: func(context.Context, *dns.Msg) (*dns.Msg, error) { return nil, errors.New("proxy down") }}
	h.handle(w, q, dns.MaxMsgSize)
	if w.msg == nil || w.msg.Rcode != dns.RcodeServerFailure || !w.msg.RecursionAvailable {
		t.Fatalf("expected fail-closed SERVFAIL with RA=true, got %+v", w.msg)
	}
}

func TestCageDNSNilAnswerFailsClosed(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	w := &cageWriter{}
	h := &dnsHandler{exchange: func(context.Context, *dns.Msg) (*dns.Msg, error) { return nil, nil }}
	h.handle(w, q, dns.MinMsgSize)
	if w.msg == nil || w.msg.Rcode != dns.RcodeServerFailure || !w.msg.RecursionAvailable {
		t.Fatalf("expected nil exchange reply to fail closed with SERVFAIL, got %+v", w.msg)
	}
}

func TestCageDNSEDNSZeroUDPSizeDoesNotTruncateAnswers(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("deb.debian.org.", dns.TypeA)
	q.SetEdns0(0, false)
	w := &cageWriter{}
	h := &dnsHandler{exchange: func(_ context.Context, req *dns.Msg) (*dns.Msg, error) {
		reply := new(dns.Msg)
		reply.SetReply(req)
		reply.RecursionAvailable = true
		reply.Answer = []dns.RR{&dns.A{
			Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
			A:   net.ParseIP("151.101.2.132").To4(),
		}}
		return reply, nil
	}}
	h.handleUDP(w, q)
	if w.msg == nil || w.msg.Rcode != dns.RcodeSuccess || len(w.msg.Answer) != 1 || w.msg.Truncated {
		t.Fatalf("EDNS0 UDPSize=0 truncated answer to empty: %+v", w.msg)
	}
}

func TestCageDNSTCPPreservesMaxMsgSizeWithEDNS(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("large.example.", dns.TypeA)
	q.SetEdns0(512, false)
	w := &cageWriter{}
	h := &dnsHandler{exchange: func(_ context.Context, req *dns.Msg) (*dns.Msg, error) {
		reply := new(dns.Msg)
		reply.SetReply(req)
		reply.RecursionAvailable = true
		for i := 1; i <= 40; i++ {
			reply.Answer = append(reply.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
				A:   net.IPv4(198, 51, 100, byte(i)),
			})
		}
		return reply, nil
	}}
	h.handleTCP(w, q)
	if w.msg == nil || len(w.msg.Answer) != 40 || w.msg.Truncated {
		t.Fatalf("TCP response was shrunk to EDNS UDPSize: answers=%d truncated=%v", len(w.msg.Answer), w.msg.Truncated)
	}
}
