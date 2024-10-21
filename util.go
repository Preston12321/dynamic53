package dynamic53

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"text/template"

	_ "embed"
)

const DefaultAddressApiUrl string = "https://ipinfo.io/ip"

//go:embed templates/iam-policy.tmpl
var IAM_POLICY_TEMPLATE string

// doer wraps *http.Client
type doer interface {
	Do(*http.Request) (*http.Response, error)
}

// AddressClient retrieves information about the host's IP address
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
// host by making a request to an external third-party API
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

// listSeparator provides a clean way to write templates that involve lists
// needing separators that can't be repeated after the final item, e.g. JSON
// lists. Given the length of the list, and the current index, listSeparator
// will either return the given separator or an empty string.
func listSeparator(length int, index int, separator string) string {
	if index == length-1 {
		return ""
	}
	return separator
}

// GenerateIAMPolicy returns a JSON string representing an identity-based AWS
// IAM policy granting the necessary permissions for a dynamic53 client to
// manage the zones and records specified in the given configuration
func GenerateIAMPolicy(cfg DaemonConfig) (string, error) {
	err := cfg.Validate()
	if err != nil {
		return "", err
	}

	funcs := template.FuncMap{"listSeparator": listSeparator}
	tmpl, err := template.New("iam-policy").Funcs(funcs).Parse(IAM_POLICY_TEMPLATE)
	if err != nil {
		return "", fmt.Errorf("unable to parse policy template: %w", err)
	}

	var buffer bytes.Buffer
	err = tmpl.Execute(&buffer, cfg.Zones)
	if err != nil {
		return "", fmt.Errorf("unable to template policy json: %w", err)
	}

	return buffer.String(), nil
}

// ShortenZoneId returns the given hosted zone ID without its optional prefix
func ShortenZoneId(id string) string {
	return strings.TrimPrefix(id, "/hostedzone/")
}

// ShortenDNSName returns the given DNS name without a trailing dot
func ShortenDNSName(name string) string {
	return strings.TrimSuffix(name, ".")
}
