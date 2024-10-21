package dynamic53

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"

	_ "embed"
)

//go:embed templates/iam-policy.tmpl
var IAM_POLICY_TEMPLATE string

// doer wraps *http.Client.
type doer interface {
	Do(*http.Request) (*http.Response, error)
}

// AddressClient retrieves information about the host's IP address.
type AddressClient struct {
	Url string

	httpClient doer
}

func NewAddressClient(url string) AddressClient {
	return AddressClient{
		Url:        url,
		httpClient: &http.Client{},
	}
}

// GetPublicIPv4 attempts to determine the current public IPv4 address of the
// host by making a request to an external third-party API.
func (c AddressClient) GetPublicIPv4(ctx context.Context) (net.IP, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Url, nil)
	if err != nil {
		return nil, fmt.Errorf("cannot create GET request: %w", err)
	}

	response, err := c.httpClient.Do(request)
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
