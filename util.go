package dynamic53

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
)

const ADDRESS_API_URL = "https://ipinfo.io/ip"

// GetPublicIPv4 attempts to determine the current public IPv4 address of the
// host by making a request to an external third-party API
func GetPublicIPv4(ctx context.Context) (net.IP, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, ADDRESS_API_URL, nil)
	if err != nil {
		return nil, fmt.Errorf("cannot create GET request: %w", err)
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("GET request failed: %w", err)
	}

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status on response: %s", response.Status)
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	ip := net.ParseIP(string(body))
	if ip == nil {
		return nil, fmt.Errorf("response body does not look like an IP address")
	}

	ipv4 := ip.To4()
	if ipv4 == nil {
		return nil, fmt.Errorf("response body does not look like an IPv4 address")
	}

	return ipv4, nil
}
