package dynamic53

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"

	"gopkg.in/yaml.v3"
)

// DaemonConfig holds the top-level configuration data for the dynamic53 daemon
type DaemonConfig struct {
	// SkipUpdate specifies that a daemon should skip sending Route 53 updates
	// to the AWS API, printing a log message for each configured zone instead
	SkipUpdate bool `yaml:"skipUpdate"`

	// Polling contains the configuration for IP address polling
	Polling PollingConfig `yaml:"polling"`

	// Zones is a slice containing the configuration for each Route 53 hosted
	// zone that should be managed by dynamic53
	Zones []ZoneConfig `yaml:"zones"`
}

func (c DaemonConfig) Validate() error {
	errs := []error{c.Polling.Validate()}

	if len(c.Zones) == 0 {
		errs = append(errs, fmt.Errorf("must specify at least one zone"))
	}

	for _, zone := range c.Zones {
		errs = append(errs, zone.Validate())
	}

	return errors.Join(errs...)
}

// ZoneConfig holds the configuration data for a single Route 53 hosted zone
type ZoneConfig struct {
	// Name is the name given to the Route53 hosted zone. Either Name or Id is
	// required. If Id is specified, this field is ignored
	Name string `yaml:"name"`

	// Id is the AWS-assigned ID of the Route53 hosted zone. Either Name or Id
	// is required. Overrides the Name field if present
	Id string `yaml:"id"`

	// Records is a slice containing the DNS A records that dynamic53 should
	// manage in this hosted zone
	Records []string `yaml:"records"`
}

func (c ZoneConfig) Validate() error {
	if c.Id == "" && c.Name == "" {
		return fmt.Errorf("invalid zone: missing id or name")
	}

	if len(c.Records) == 0 {
		return fmt.Errorf("invalid zone: must specify at least one record")
	}

	// TODO: Maybe account for duplicates?
	if len(c.Records) > int(MaxRecordsPerZone) {
		return fmt.Errorf("invalid zone: managing more than 300 records in a zone is not supported")
	}

	for _, record := range c.Records {
		if record == "" {
			return fmt.Errorf("invalid record: must not be an empty string")
		}
	}

	return nil
}

// PollingConfig holds the configuration date for the dynamic53 daemon's IP
// address polling behavior
type PollingConfig struct {
	// Interval is the interval at which the daemon should poll for changes to
	// the host's public IP address
	Interval time.Duration `yaml:"interval"`

	// MaxJitter is the maximum amount of time the daemon should randomly choose
	// to wait before polling on a given iteration. Set to zero to disable
	// jitter (this is bad practice, so don't do it unless you have good reason)
	MaxJitter time.Duration `yaml:"maxJitter"`

	// Url is the resource that dynamic53 should poll to determine the host's
	// IPv4 address. If empty, dynamic53 defaults to the ipinfo.org API
	Url string `yaml:"url"`
}

func (c PollingConfig) Validate() error {
	if c.Interval <= 0 {
		return fmt.Errorf("invalid polling config: interval must be positive and non-zero")
	}

	if c.MaxJitter < 0 {
		return fmt.Errorf("invalid polling config: maxJitter must be positive")
	}

	if c.MaxJitter >= c.Interval {
		return fmt.Errorf("invalid polling config: maxJitter must be less than polling interval")
	}

	if c.Url != "" {
		_, err := url.Parse(c.Url)
		if err != nil {
			return fmt.Errorf("invalid polling config: invalid url: %w", err)
		}
	}

	return nil
}

// LoadDaemonConfig reads and parses a configuration from the given io.Reader
func LoadDaemonConfig(from io.Reader) (*DaemonConfig, error) {
	var cfg DaemonConfig

	err := yaml.NewDecoder(from).Decode(&cfg)
	if err != nil {
		return nil, fmt.Errorf("cannot parse yaml: %w", err)
	}

	err = cfg.Validate()
	if err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}
