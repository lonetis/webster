package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// DNS events are sent to the dashboard independently of HTTP completions.
type dnsEvent struct {
	Status string
	Reply  bool          `json:",omitempty"`
	Policy bool          `json:",omitempty"`
	RTT    time.Duration `json:",omitempty"`
}

type dnsObserverKey struct{}
type dnsObserver struct {
	mu   sync.Mutex
	ctx  context.Context
	emit func(dnsEvent)
	open map[*dnsAttempt]bool
}
type dnsAttempt struct {
	owner *dnsObserver
	start time.Time
}

func (o *dnsObserver) begin() *dnsAttempt {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.open == nil || o.ctx.Err() != nil {
		return nil
	}
	a := &dnsAttempt{o, time.Now()}
	o.open[a] = true
	o.emit(dnsEvent{Status: "start"})
	return a
}

func (a *dnsAttempt) finish(e dnsEvent) {
	o := a.owner
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.open[a] {
		delete(o.open, a)
		e.RTT = time.Since(a.start)
		o.emit(e)
	}
}

// Transport dials and DNS retries can outlive a timed-out request. Finalize the
// outstanding exchanges before the worker writes its result or takes another job.
func (o *dnsObserver) close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	status := "canceled"
	if errors.Is(o.ctx.Err(), context.DeadlineExceeded) {
		status = "timeout"
	}
	for a := range o.open {
		o.emit(dnsEvent{Status: status, RTT: time.Since(a.start)})
	}
	o.open = nil
}

func dnsFailure(err error) dnsEvent {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return dnsEvent{Status: "timeout"}
	}
	if errors.Is(err, context.Canceled) {
		return dnsEvent{Status: "canceled"}
	}
	return dnsEvent{Status: "net error"}
}

// Preserve net.PacketConn on UDP: net.Resolver uses this interface to choose
// datagrams vs length-prefixed DNS over TCP.
type packetMethods interface {
	ReadFrom([]byte) (int, net.Addr, error)
	WriteTo([]byte, net.Addr) (int, error)
}
type dnsPacketConn struct {
	*dnsWireConn
	packetMethods
}
type dnsWireConn struct {
	net.Conn
	attempt  *dnsAttempt
	tcp      bool
	frame    []byte
	id       uint16
	question dnsmessage.Question
}

func dialObservedDNS(ctx context.Context, network, server string) (net.Conn, error) {
	var d net.Dialer
	o, _ := ctx.Value(dnsObserverKey{}).(*dnsObserver)
	if o == nil {
		return d.DialContext(ctx, network, server)
	}
	a := o.begin()
	if a == nil {
		return nil, context.Canceled
	}
	if deadline, ok := o.ctx.Deadline(); ok {
		d.Deadline = deadline
	}
	conn, err := d.DialContext(ctx, network, server)
	if err != nil {
		a.finish(dnsFailure(err))
		return nil, err
	}
	wire := &dnsWireConn{Conn: conn, attempt: a}
	if packet, ok := conn.(net.PacketConn); ok {
		return &dnsPacketConn{wire, packet}, nil
	}
	wire.tcp = true
	return wire, nil
}

func (c *dnsWireConn) SetDeadline(t time.Time) error {
	if limit, ok := c.attempt.owner.ctx.Deadline(); ok && (t.IsZero() || limit.Before(t)) {
		t = limit
	}
	return c.Conn.SetDeadline(t)
}

func (c *dnsWireConn) Write(p []byte) (int, error) {
	msg := p
	if c.tcp && len(msg) >= 2 {
		msg = msg[2:]
	}
	var parser dnsmessage.Parser
	if h, err := parser.Start(msg); err == nil {
		c.id = h.ID
		c.question, _ = parser.Question()
	}
	n, err := c.Conn.Write(p)
	if err != nil {
		c.attempt.finish(dnsFailure(err))
	}
	return n, err
}
func (c *dnsWireConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	msg := p[:n]
	if c.tcp {
		c.frame = append(c.frame, msg...)
		msg = nil
		if len(c.frame) >= 2 && len(c.frame) == 2+int(binary.BigEndian.Uint16(c.frame)) {
			msg = c.frame[2:]
		}
	}
	if len(msg) > 0 {
		if event, ok := c.reply(msg); ok {
			c.attempt.finish(event)
		}
	}
	if err != nil {
		c.attempt.finish(dnsFailure(err))
	}
	return n, err
}
func (c *dnsWireConn) Close() error {
	c.attempt.finish(dnsEvent{Status: "net error"})
	return c.Conn.Close()
}

func (c *dnsWireConn) reply(msg []byte) (dnsEvent, bool) {
	var p dnsmessage.Parser
	h, err := p.Start(msg)
	if err != nil || !h.Response || h.ID != c.id {
		return dnsEvent{}, false
	}
	q, err := p.Question()
	if err != nil || q.Type != c.question.Type || q.Class != c.question.Class || !strings.EqualFold(q.Name.String(), c.question.Name.String()) {
		return dnsEvent{}, false
	}
	rcode := h.RCode
	policy := false
	if p.SkipAllQuestions() == nil && p.SkipAllAnswers() == nil && p.SkipAllAuthorities() == nil {
		rcode, policy = dnsReplyOptions(&p, rcode)
	}
	status := ""
	switch rcode {
	case dnsmessage.RCodeSuccess:
		status = "NOERROR"
	case dnsmessage.RCodeServerFailure:
		status = "SERVFAIL"
	case dnsmessage.RCodeNameError:
		status = "NXDOMAIN"
	case dnsmessage.RCodeRefused:
		status = "REFUSED"
	default:
		status = fmt.Sprintf("RCODE%d", rcode)
	}
	return dnsEvent{Status: status, Reply: true, Policy: policy}, true
}

func dnsReplyOptions(p *dnsmessage.Parser, rcode dnsmessage.RCode) (dnsmessage.RCode, bool) {
	for {
		hdr, err := p.AdditionalHeader()
		if err != nil {
			return rcode, false
		}
		if hdr.Type != dnsmessage.TypeOPT {
			if p.SkipAdditional() != nil {
				return rcode, false
			}
			continue
		}
		opt, err := p.OPTResource()
		if err != nil {
			return rcode, false
		}
		rcode = hdr.ExtendedRCode(rcode)
		for _, o := range opt.Options {
			if o.Code == 15 && len(o.Data) >= 2 {
				code := binary.BigEndian.Uint16(o.Data)
				if code >= 15 && code <= 18 {
					return rcode, true
				} // RFC 8914 policy signals
			}
		}
		return rcode, false
	}
}
