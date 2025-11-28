package rely

import (
	"net"
	"net/http"
	"strings"
)

const DefaultIPv6Prefix = 64

// IP is a wrapper around the standard library [net.IP].
// It provides useful convenience methods such as [IP.Group] and [IP.GroupPrefix]
// for grouping/normalizing IP addresses for rate-limiting purposes.
type IP struct {
	Raw net.IP
}

// Group returns a stable value suitable for rate limiting or grouping.
//   - IPv4 addresses: the full /32 address is returned.
//   - IPv6 addresses: it uses the default /64 mask to return the network prefix,
//     grouping all traffic from the same standard subnet block.
func (ip IP) Group() string {
	return ip.GroupPrefix(DefaultIPv6Prefix)
}

// GroupPrefix returns a stable value suitable for rate limiting or grouping.
//   - IPv4 addresses: the full /32 address is returned.
//   - IPv6 addresses: it uses the mask with the specified `prefix` (typically 48 or 64),
//     treating all addresses within that network block as a single entity.
//
// It panics if prefix is outside the closed interval [0,128].
func (ip IP) GroupPrefix(prefix int) string {
	if prefix < 0 || prefix > 128 {
		panic("rely.IP.GroupPrefix: prefix must be between 0 and 128")
	}
	if len(ip.Raw) == 0 {
		return ""
	}
	if ip.IsV4() {
		return ip.Raw.String()
	}
	return ip.Raw.Mask(net.CIDRMask(prefix, 128)).String()
}

// IsV4 returns whether the IP is a valid IPv4 address.
func (ip IP) IsV4() bool { return ip.Raw.To4() != nil }

// IsV6 returns whether the IP is an IPv6 address.
func (ip IP) IsV6() bool { return len(ip.Raw) == net.IPv6len && ip.Raw.To4() == nil }

// String returns the string form of the raw [net.IP] address ip.
func (ip IP) String() string { return ip.Raw.String() }

// GetIP returns the IP address of the http request.
// It parses the extracted IP string into the custom rely.IP wrapper struct.
//
// IMPORTANT: This function assumes the relay is behind a trusted reverse proxy.
// If this is not the case, clients can easily spoof the IP headers (True-Client-IP, X-Real-IP).
func GetIP(r *http.Request) IP {
	ip := getIP(r)
	return IP{Raw: net.ParseIP(ip)}
}

func getIP(r *http.Request) string {
	if tIP := r.Header.Get("True-Client-IP"); tIP != "" {
		return tIP
	}
	if rIP := r.Header.Get("X-Real-IP"); rIP != "" {
		return rIP
	}
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		first := strings.Split(forwarded, ",")[0]
		return strings.TrimSpace(first)
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr // fallback: return as is
	}
	return host
}
