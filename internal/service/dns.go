package service

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"time"
)

// DNSProbe is what one test query to a resolver address found.
type DNSProbe struct {
	Address netip.Addr
	// Answered means a resolver replied with a usable answer (a name, or a clean "no such
	// name"), so clients pointed at Address can look names up.
	Answered bool
	// Detail says what happened, in words for the admin.
	Detail string
}

// dnsProbeTimeout bounds one test query. A resolver on the host answers in milliseconds;
// this leaves room for one that has to ask its upstream.
const dnsProbeTimeout = 2 * time.Second

// ProbeDNS asks each of the server's VPN addresses, IPv4 and IPv6, whether a DNS resolver
// answers there. The default client DNS is the server itself only when one does (D12): a
// resolver such as AdGuard Home, Pi-hole, Unbound, or dnsmasq that listens on those
// addresses. The results come back IPv4 first.
func (s *Service) ProbeDNS(ctx context.Context) ([]DNSProbe, error) {
	st, err := s.Store.Settings(ctx)
	if err != nil {
		return nil, err
	}
	srv, err := st.ServerAddrs()
	if err != nil {
		return nil, err
	}
	addrs := []netip.Addr{srv.IPv4}
	if srv.IPv6.IsValid() {
		addrs = append(addrs, srv.IPv6)
	}
	probe := s.DNSProbe
	if probe == nil {
		probe = func(ctx context.Context, a netip.Addr) DNSProbe { return ProbeResolver(ctx, netip.AddrPortFrom(a, 53)) }
	}
	out := make([]DNSProbe, len(addrs))
	var wg sync.WaitGroup
	for i, a := range addrs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = probe(ctx, a)
			out[i].Address = a
		}()
	}
	wg.Wait()
	return out, nil
}

// ProbeResolver sends one recursive query for example.com to a resolver and reports
// whether it answered. It sends nothing but that query, and nothing it learns is stored.
func ProbeResolver(ctx context.Context, to netip.AddrPort) DNSProbe {
	res := DNSProbe{Address: to.Addr()}
	ctx, cancel := context.WithTimeout(ctx, dnsProbeTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "udp", to.String())
	if err != nil {
		res.Detail = probeError(err)
		return res
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	const id = 0xdb53
	query := []byte{id >> 8, id & 0xff, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0}
	query = append(query, "\x07example\x03com\x00"...)
	query = append(query, 0, 1, 0, 1) // type A, class IN
	if _, err := conn.Write(query); err != nil {
		res.Detail = probeError(err)
		return res
	}
	// A datagram from anyone else, or with another ID, isn't the reply.
	buf := make([]byte, 512)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			res.Detail = probeError(err)
			return res
		}
		if n < 12 || binary.BigEndian.Uint16(buf) != id || buf[2]&0x80 == 0 {
			continue
		}
		switch rcode := buf[3] & 0x0f; rcode {
		case 0, 3: // NOERROR, NXDOMAIN
			res.Answered, res.Detail = true, "A resolver answered."
		case 5:
			res.Detail = "A resolver is listening but refused the query. Its access settings may not allow this address."
		default:
			res.Detail = "A resolver is listening but couldn't look up a name. Its own upstream servers may be down."
		}
		return res
	}
}

func probeError(err error) string {
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "Nothing is listening for DNS on port 53."
	case errors.Is(err, context.DeadlineExceeded), isTimeout(err):
		return "Nothing answered within two seconds."
	default:
		return "The address couldn't be reached. The tunnel may be down."
	}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
