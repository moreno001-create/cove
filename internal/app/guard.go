package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Requests Cove makes to addresses people type in (calendar subscriptions, ntfy, push services) must not reach
// this machine or the home network: otherwise anyone signed in could make the server knock on the router,
// Homeport, or other apps' private ports. Addresses under Cove's own main domain (ntfy.<main domain>, which
// Homeport serves through its own Caddy) are the one exception.

var trustedSuffix string // ".home.example.com", from COVE_URL

func setTrusted(coveURL string) {
	u, err := url.Parse(coveURL)
	if err != nil {
		return
	}
	host := u.Hostname()
	if _, parent, ok := strings.Cut(host, "."); ok && strings.Contains(parent, ".") && net.ParseIP(host) == nil {
		trustedSuffix = "." + parent
	}
}

var cgnat = &net.IPNet{IP: net.IP{100, 64, 0, 0}, Mask: net.CIDRMask(10, 32)}

func publicIP(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() ||
		ip.IsUnspecified() || cgnat.Contains(ip) || ip.Equal(net.IPv4bcast))
}

func guardedDial(trust bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 10 * time.Second}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if trust && trustedSuffix != "" && strings.HasSuffix(strings.ToLower(host), trustedSuffix) {
			return d.DialContext(ctx, network, addr)
		}
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			if publicIP(ip) {
				return d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			}
		}
		return nil, errors.New("that address is on the home network or this machine, which Cove doesn't fetch from")
	}
}

// outsideClient reaches the internet (and Cove's own main domain); pushClient reaches only the internet.
var (
	outsideClient = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: guardedDial(true), TLSHandshakeTimeout: 10 * time.Second}}
	pushClient    = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: guardedDial(false), TLSHandshakeTimeout: 10 * time.Second}}
)
