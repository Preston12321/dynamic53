package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
	"gopkg.in/yaml.v3"
)

const ADDRESS_API_URL = "https://ipinfo.io/ip"

// Config holds the top-level configuration data for the dynamic53 daemon
type Config struct {
	// Polling contains the configuration for IP address polling
	Polling PollingConfig `yaml:"polling"`

	// Zones is a slice containing the configuration for each Route 53 hosted
	// zone that should be managed by dynamic53
	Zones []ZoneConfig `yaml:"zones"`
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

// PollingConfig holds the configuration date for the dynamic53 daemon's IP
// address polling behavior
type PollingConfig struct {
	// Interval is the interval at which the daemon should poll for changes to
	// the host's public IP address
	Interval time.Duration `yaml:"interval"`

	// MaxJitter is the maximum amount of time the daemon should randomly choose
	// to wait before polling on a given iteration
	MaxJitter time.Duration `yaml:"maxJitter"`
}

func main() {
	configPath := flag.String("config", "", "File path of the config")
	flag.Parse()

	cfg, err := ReadConfig(*configPath)
	if err != nil {
		panic(fmt.Errorf("cannot read configuration: %w", err))
	}

	err = ValidateConfig(cfg)
	if err != nil {
		panic(fmt.Errorf("invalid configuration: %w", err))
	}

	ctx := context.Background()

	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		panic(fmt.Errorf("cannot load AWS configuration: %w", err))
	}

	go StartDaemon(context.Background(), cfg, route53.NewFromConfig(awsCfg))
}

func StartDaemon(ctx context.Context, cfg *Config, route53Client *route53.Client) {
	ticker := time.NewTicker(cfg.Polling.Interval)
	defer ticker.Stop()

	for {
		timeoutCtx, cancel := context.WithTimeout(ctx, cfg.Polling.Interval)

		// TODO: Log this error instead of ignoring it
		_ = DoUpdate(timeoutCtx, cfg, route53Client)

		cancel()

		select {
		case <-ticker.C:
			continue
		case <-ctx.Done():
			return
		}
	}
}

func DoUpdate(ctx context.Context, cfg *Config, route53Client *route53.Client) error {
	if cfg.Polling.MaxJitter != 0 {
		jitter := rand.Int64N(int64(cfg.Polling.MaxJitter))
		time.Sleep(time.Duration(jitter))
	}

	if ctx.Err() != nil {
		return fmt.Errorf("context cancelled: %w", ctx.Err())
	}

	address, err := GetPublicAddress(ctx)
	if err != nil {
		return fmt.Errorf("failed to get public IPv4 address: %w", err)
	}

	errChan := make(chan error, len(cfg.Zones))

	// TODO: Maybe set an upper bound for the number of goroutines
	var wg sync.WaitGroup
	wg.Add(len(cfg.Zones))

	for _, zone := range cfg.Zones {
		go func() {
			errChan <- UpdateRecords(ctx, route53Client, zone, address)
			wg.Done()
		}()
	}

	wg.Wait()

	// Collect all the errors together in a slice
	errs := make([]error, len(cfg.Zones))
	for err := range errChan {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

func GetPublicAddress(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, ADDRESS_API_URL, nil)
	if err != nil {
		return "", fmt.Errorf("cannot create GET request: %w", err)
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("GET request failed: %w", err)
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	return string(body), nil
}

func UpdateRecords(ctx context.Context, route53Client *route53.Client, zone ZoneConfig, address string) error {
	zoneId := zone.Id

	// Check if hosted zone exists and get its ID if we don't already have it
	if zoneId != "" {
		input := route53.GetHostedZoneInput{Id: &zoneId}

		// Double-check that the given ID points to a real hosted zone
		_, err := route53Client.GetHostedZone(ctx, &input)
		if err != nil {
			return fmt.Errorf("error getting hosted zone info: %w", err)
		}
	} else {
		maxItems := int32(1)
		input := route53.ListHostedZonesByNameInput{DNSName: &zone.Name, MaxItems: &maxItems}

		// Find the hosted zone with the given name, if it exists
		listOutput, err := route53Client.ListHostedZonesByName(ctx, &input)
		if err != nil {
			return fmt.Errorf("error listing hosted zones: %w", err)
		}

		if len(listOutput.HostedZones) == 0 {
			return fmt.Errorf("cannot find hosted zone by name '%s'", zone.Name)
		}

		zoneId = *listOutput.HostedZones[0].Id
	}

	// TODO: Split these into meta-batches if there are more than 1000 RRs
	// TODO: Test to see how leaving TTL and other options blank affects them
	// Create a batch change to update resource records
	changes := make([]types.Change, 0, len(zone.Records))
	for _, record := range zone.Records {
		change := types.Change{
			Action: types.ChangeActionUpsert,
			ResourceRecordSet: &types.ResourceRecordSet{
				Name: &record,
				Type: types.RRTypeA,
			},
		}
		changes = append(changes, change)
	}

	input := route53.ChangeResourceRecordSetsInput{
		HostedZoneId: &zoneId,
		ChangeBatch:  &types.ChangeBatch{Changes: changes},
	}

	changeOutput, err := route53Client.ChangeResourceRecordSets(ctx, &input)
	if err != nil {
		return fmt.Errorf("failed to update resource records: %w", err)
	}

	// The docs say this generally happens within 60 seconds, so let's go 90
	timeoutCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	err = WaitForRRChange(timeoutCtx, route53Client, changeOutput.ChangeInfo.Id)
	cancel()

	if err != nil {
		return fmt.Errorf("error waiting for resource record change to propagate: %w", err)
	}

	return nil
}

func WaitForRRChange(ctx context.Context, route53Client *route53.Client, changeId *string) error {
	for i := 1; i <= 5; i++ {
		if ctx.Err() != nil {
			return fmt.Errorf("context cancelled: %w", ctx.Err())
		}

		output, err := route53Client.GetChange(ctx, &route53.GetChangeInput{Id: changeId})
		if err != nil {
			continue
		}

		if output.ChangeInfo.Status == types.ChangeStatusInsync {
			return nil
		}

		time.Sleep(time.Duration(i^2) * time.Second)
	}

	return fmt.Errorf("maximum tries exceeded")
}

func ReadConfig(configPath string) (*Config, error) {
	if configPath == "" {
		return nil, fmt.Errorf("no config file specified")
	}

	file, err := os.Open(configPath)
	if err != nil {
		return nil, fmt.Errorf("cannot open file: %w", err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)

	var cfg Config
	err = decoder.Decode(&cfg)
	if err != nil {
		return nil, fmt.Errorf("cannot parse yaml: %w", err)
	}

	return &cfg, nil
}

func ValidateConfig(cfg *Config) error {
	if cfg.Polling.Interval <= 0 {
		return fmt.Errorf("invalid polling config: interval must be positive and non-zero")
	}

	if cfg.Polling.MaxJitter < 0 {
		return fmt.Errorf("invalid polling config: maxJitter must be positive")
	}

	if cfg.Polling.MaxJitter >= cfg.Polling.Interval {
		return fmt.Errorf("invalid polling config: maxJitter must be less than polling interval")
	}

	if len(cfg.Zones) == 0 {
		return fmt.Errorf("must specify at least one zone")
	}

	for _, zone := range cfg.Zones {
		if zone.Id == "" && zone.Name == "" {
			return fmt.Errorf("invalid zone: missing id or name")
		}

		if len(zone.Records) == 0 {
			return fmt.Errorf("invalid zone: must specify at least one record")
		}

		for _, record := range zone.Records {
			if record == "" {
				return fmt.Errorf("invalid record: must not be an empty string")
			}
		}
	}

	return nil
}
