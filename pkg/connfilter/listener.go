package connfilter

import (
	"net"
	"net/netip"
	"sync"
	"time"
)

type HitResult struct {
	Blocked  bool
	Extended bool
	Reason   string
	IP       string
}

type IPBlacklist interface {
	Check(ip net.IP, now time.Time) HitResult
	CheckAndRecordHit(ip net.IP, now time.Time) HitResult
}

type FilteringListener struct {
	Inner     net.Listener
	Blacklist IPBlacklist

	connectionsMu sync.Mutex
	connections   map[string]map[*trackedConn]struct{}
}

func New(inner net.Listener, blacklist IPBlacklist) *FilteringListener {
	return &FilteringListener{
		Inner:       inner,
		Blacklist:   blacklist,
		connections: make(map[string]map[*trackedConn]struct{}),
	}
}

func (l *FilteringListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Inner.Accept()
		if err != nil {
			return nil, err
		}
		ip := remoteIP(conn.RemoteAddr())
		if ip == nil || l.Blacklist == nil {
			return conn, nil
		}
		result := l.Blacklist.CheckAndRecordHit(ip, time.Now())
		if result.Blocked {
			_ = conn.Close()
			continue
		}

		tracked := &trackedConn{Conn: conn, owner: l, ip: normalizedIP(ip)}
		l.register(tracked)
		// A ban can be published between the first check and registration. The
		// second, non-counting check closes that race without double-counting a hit.
		if result = l.Blacklist.Check(ip, time.Now()); result.Blocked {
			_ = tracked.Close()
			continue
		}
		return tracked, nil
	}
}

func (l *FilteringListener) CloseIP(ip string) int {
	key := normalizedIPString(ip)
	if key == "" {
		return 0
	}
	l.connectionsMu.Lock()
	set := l.connections[key]
	connections := make([]*trackedConn, 0, len(set))
	for conn := range set {
		connections = append(connections, conn)
	}
	l.connectionsMu.Unlock()
	for _, conn := range connections {
		_ = conn.Close()
	}
	return len(connections)
}

func (l *FilteringListener) CloseBlocked() int {
	if l.Blacklist == nil {
		return 0
	}
	l.connectionsMu.Lock()
	byIP := make(map[string]net.IP, len(l.connections))
	for ip := range l.connections {
		byIP[ip] = net.ParseIP(ip)
	}
	l.connectionsMu.Unlock()

	closed := 0
	now := time.Now()
	for ip, parsed := range byIP {
		if parsed != nil && l.Blacklist.Check(parsed, now).Blocked {
			closed += l.CloseIP(ip)
		}
	}
	return closed
}

func (l *FilteringListener) Close() error {
	err := l.Inner.Close()
	l.connectionsMu.Lock()
	connections := make([]*trackedConn, 0)
	for _, set := range l.connections {
		for conn := range set {
			connections = append(connections, conn)
		}
	}
	l.connectionsMu.Unlock()
	for _, conn := range connections {
		_ = conn.Close()
	}
	return err
}

func (l *FilteringListener) Addr() net.Addr { return l.Inner.Addr() }

func (l *FilteringListener) register(conn *trackedConn) {
	l.connectionsMu.Lock()
	set := l.connections[conn.ip]
	if set == nil {
		set = make(map[*trackedConn]struct{})
		l.connections[conn.ip] = set
	}
	set[conn] = struct{}{}
	l.connectionsMu.Unlock()
}

func (l *FilteringListener) unregister(conn *trackedConn) {
	l.connectionsMu.Lock()
	if set := l.connections[conn.ip]; set != nil {
		delete(set, conn)
		if len(set) == 0 {
			delete(l.connections, conn.ip)
		}
	}
	l.connectionsMu.Unlock()
}

type trackedConn struct {
	net.Conn
	owner     *FilteringListener
	ip        string
	closeOnce sync.Once
	closeErr  error
}

func (c *trackedConn) Close() error {
	c.closeOnce.Do(func() {
		c.owner.unregister(c)
		c.closeErr = c.Conn.Close()
	})
	return c.closeErr
}

func remoteIP(addr net.Addr) net.IP {
	if tcpAddr, ok := addr.(*net.TCPAddr); ok {
		return tcpAddr.IP
	}
	return nil
}

func normalizedIP(ip net.IP) string {
	if addr, ok := netip.AddrFromSlice(ip); ok {
		return addr.Unmap().String()
	}
	return ip.String()
}

func normalizedIPString(value string) string {
	if addr, err := netip.ParseAddr(value); err == nil {
		return addr.Unmap().String()
	}
	return ""
}
