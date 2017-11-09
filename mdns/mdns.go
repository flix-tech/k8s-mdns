package mdns

// Advertise network services via multicast DNS

import (
	"log"
	"net"

	"github.com/miekg/dns"
	"reflect"
)

var (
	ipv4mcastaddr = &net.UDPAddr{
		IP:   net.ParseIP("224.0.0.251"),
		Port: 5353,
	}

	ipv6mcastaddr = &net.UDPAddr{
		IP:   net.ParseIP("ff02::fb"),
		Port: 5353,
	}
	local *zone // the local mdns zone
)

func init() {
	local = &zone{
		entries: make(map[string]entries),
		op:      make(chan operation),
		queries: make(chan *query, 16),
	}
	go local.mainloop()
	if err := local.listen(ipv4mcastaddr); err != nil {
		log.Fatalf("Failed to listen %s: %s", ipv4mcastaddr, err)
	}
	// TODO re-enable IPV6 with better error handling
	//if err := local.listen(ipv6mcastaddr); err != nil {
	//	log.Printf("Failed to listen %s: %s", ipv6mcastaddr, err)
	//}
}

// Publish adds a record, describewrite tod in RFC XXX
func Publish(r string) error {
	rr, err := dns.NewRR(r)
	if err != nil {
		return err
	}
	local.op <- operation{"add",&entry{rr}}
	return nil
}

func UnPublish(r string) error {
	rr, err := dns.NewRR(r)
	if err != nil {
		return err
	}
	local.op <- operation{"del",&entry{rr}}
	return nil
}

func Clear() {
	local.op <- operation{"clr",nil}
}

type entry struct {
	dns.RR
}

func (e *entry) fqdn() string {
	return e.Header().Name
}

type query struct {
	dns.Question
	result chan *entry
}

type entries []*entry

func (e entries) contains(entry *entry) int {
	for i, ee := range e {
		if reflect.DeepEqual(entry, ee) {
			return i
		}
	}
	return -1
}

type operation struct {
	op string // one of add, del, clr
	*entry
}

type zone struct {
	entries map[string]entries
	op      chan operation
	queries chan *query // query exsting entries in zone
}

func (z *zone) mainloop() {
	for {
		select {
		case op := <-z.op:
			entry := op.entry
			switch op.op {
			case "add":
				if z.entries[entry.fqdn()].contains(entry) == -1 {
					z.entries[entry.fqdn()] = append(z.entries[entry.fqdn()], entry)
				}
			case "del":
				entries := z.entries[entry.fqdn()]
				idx := z.entries[entry.fqdn()].contains(entry)
				if idx != -1 {
					entries[idx] = entries[len(entries)-1]
					entries[len(entries)-1] = nil
					z.entries[entry.fqdn()] = entries[:len(entries)-1]
				}
			case "clr":
				z.entries = make(map[string]entries)
			}
		case q := <-z.queries:
			for _, entry := range z.entries[q.Question.Name] {
				if q.matches(entry) {
					q.result <- entry
				}
			}
			close(q.result)
		}
	}
}

func (z *zone) query(q dns.Question) (entries []*entry) {
	res := make(chan *entry, 16)
	z.queries <- &query{q, res}
	for e := range res {
		entries = append(entries, e)
	}
	return
}

func (q *query) matches(entry *entry) bool {
	return q.Question.Qtype == dns.TypeANY || q.Question.Qtype == entry.RR.Header().Rrtype
}

type connector struct {
	*net.UDPAddr
	*net.UDPConn
	*zone
}

func (z *zone) listen(addr *net.UDPAddr) error {
	conn, err := openSocket(addr)
	if err != nil {
		return err
	}
	c := &connector{
		UDPAddr: addr,
		UDPConn: conn,
		zone:    z,
	}
	go c.mainloop()

	return nil
}

func openSocket(addr *net.UDPAddr) (*net.UDPConn, error) {
	switch addr.IP.To4() {
	case nil:
		return net.ListenMulticastUDP("udp6", nil, ipv6mcastaddr)
	default:
		return net.ListenMulticastUDP("udp4", nil, ipv4mcastaddr)
	}
	panic("unreachable")
}

type pkt struct {
	*dns.Msg
	*net.UDPAddr
}

func (c *connector) readloop(in chan pkt) {
	for {
		msg, addr, err := c.readMessage()
		if err != nil {
			// log dud packets
			log.Printf("Could not read from %s: %s", c.UDPConn, err)
			continue
		}
		if len(msg.Question) > 0 {
			in <- pkt{msg, addr}
		}
	}
}

func (c *connector) mainloop() {
	in := make(chan pkt, 32)
	go c.readloop(in)
	for {
		msg := <-in
		msg.MsgHdr.Response = true     // convert question to response
		msg.Answer = make([]dns.RR, 0) // some queries already have an answer, we should not answer them
		for _, result := range c.query(msg.Question) {
			msg.Answer = append(msg.Answer, result.RR)
		}
		msg.Extra = append(msg.Extra, c.findExtra(msg.Answer...)...)

		if len(msg.Answer) > 0 {
			log.Println(msg)

			addr := ipv4mcastaddr
			// check unicast-response bit https://tools.ietf.org/html/rfc6762#section-5.4
			if msg.Question[0].Qclass & 32768 > 0 {
				log.Println("using unicast")
				addr = msg.UDPAddr
			}

			// nuke questions
			msg.Question = nil

			msg.UDPAddr = addr

			if err := c.writeMessage(msg.Msg, addr); err != nil {
				log.Fatalf("Cannot send: %s", err)
			}
		}
	}
}

func (c *connector) query(qs []dns.Question) (results []*entry) {
	for _, q := range qs {
		results = append(results, c.zone.query(q)...)
	}

	return
}

// recursively probe for related records
func (c *connector) findExtra(r ...dns.RR) (extra []dns.RR) {
	for _, rr := range r {
		var q dns.Question
		switch rr := rr.(type) {
		case *dns.PTR:
			q = dns.Question{
				Name:   rr.Ptr,
				Qtype:  dns.TypeANY,
				Qclass: dns.ClassINET,
			}
		case *dns.SRV:
			q = dns.Question{
				Name:   rr.Target,
				Qtype:  dns.TypeA,
				Qclass: dns.ClassINET,
			}
		default:
			continue
		}
		res := c.zone.query(q)
		if len(res) > 0 {
			for _, entry := range res {
				extra = append(append(extra, entry.RR), c.findExtra(entry.RR)...)
			}
		}
	}
	return
}

// encode an mdns msg and broadcast it on the wire
func (c *connector) writeMessage(msg *dns.Msg, addr *net.UDPAddr) error {
	buf, err := msg.Pack()
	if err != nil {
		return err
	}
	_, err = c.WriteToUDP(buf, addr)
	return err
}

// consume an mdns packet from the wire and decode it
func (c *connector) readMessage() (*dns.Msg, *net.UDPAddr, error) {
	buf := make([]byte, 4096)
	read, addr, err := c.ReadFromUDP(buf)
	if err != nil {
		return nil, nil, err
	}

	var msg dns.Msg
	if err := msg.Unpack(buf[:read]); err != nil {
		return nil, nil, err
	}

	return &msg, addr, nil
}
