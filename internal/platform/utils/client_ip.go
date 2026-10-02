package utils

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

type ClientIPResolver struct {
	header  string
	trusted []netip.Prefix
}

type clientIPContextKey struct{}
type clientIPResult struct {
	address string
	err     error
}

func NewClientIPResolver(header string, trusted []string) (*ClientIPResolver, error) {
	header = strings.TrimSpace(header)
	for _, char := range header {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("!#$%&'*+-.^_\x60|~", char) {
			continue
		}
		return nil, fmt.Errorf("CLIENT_IP_HEADER must be a valid HTTP header name")
	}
	_, prefixes, err := NormalizeAllowedCIDRs(trusted)
	if err != nil {
		return nil, fmt.Errorf("TRUSTED_PROXIES must contain IP addresses or CIDRs")
	}
	return &ClientIPResolver{header: http.CanonicalHeaderKey(header), trusted: prefixes}, nil
}

func (resolver *ClientIPResolver) Apply(request *http.Request) *http.Request {
	if request == nil {
		return nil
	}
	address, err := resolver.resolve(request)
	return request.WithContext(context.WithValue(request.Context(), clientIPContextKey{}, clientIPResult{address: address, err: err}))
}

func ClientIP(request *http.Request) (string, error) {
	if request == nil {
		return "", fmt.Errorf("client IP request is missing")
	}
	if result, ok := request.Context().Value(clientIPContextKey{}).(clientIPResult); ok {
		return result.address, result.err
	}
	return NormalizePeerIP(request.RemoteAddr)
}

func (resolver *ClientIPResolver) resolve(request *http.Request) (string, error) {
	peer, peerErr := NormalizePeerIP(request.RemoteAddr)
	if resolver == nil || resolver.header == "" || len(resolver.trusted) > 0 && !AllowedCIDRsContain(resolver.trusted, peer) {
		return peer, peerErr
	}
	value := strings.Join(request.Header.Values(resolver.header), ",")
	if resolver.header != "X-Forwarded-For" {
		if address, err := NormalizeIP(value); err == nil {
			return address, nil
		}
		return peer, peerErr
	}
	items := strings.Split(value, ",")
	if len(resolver.trusted) == 0 {
		if address, err := NormalizeIP(items[0]); err == nil {
			return address, nil
		}
		return peer, peerErr
	}
	for index := len(items) - 1; index >= 0; index-- {
		address, err := NormalizeIP(items[index])
		if err != nil {
			break
		}
		if index == 0 || !AllowedCIDRsContain(resolver.trusted, address) {
			return address, nil
		}
	}
	return peer, peerErr
}

func NormalizeIP(value string) (string, error) {
	address, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil || address.Zone() != "" {
		return "", fmt.Errorf("invalid IP address")
	}
	return address.Unmap().String(), nil
}
