package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/rs/zerolog"
)

const ADDRESS_API_URL = "https://ipinfo.io/ip"

func main() {
	configPath := flag.String("config", "", "File path of the config")
	flag.Parse()

	zerolog.DurationFieldUnit = time.Second
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()

	cfg, err := ReadConfig(*configPath)
	if err != nil {
		logger.Fatal().Err(fmt.Errorf("cannot read configuration: %w", err)).Send()
	}

	err = ValidateConfig(cfg)
	if err != nil {
		logger.Fatal().Err(fmt.Errorf("invalid configuration: %w", err)).Send()
	}

	ctx := logger.WithContext(context.Background())

	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		logger.Fatal().Err(fmt.Errorf("cannot load AWS configuration: %w", err)).Send()
	}

	daemon := NewDaemon(cfg, route53.NewFromConfig(awsCfg))
	daemon.Start(ctx)
}

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

type Daemon struct {
	Config *Config
	client *route53.Client
}

func NewDaemon(config *Config, route53Client *route53.Client) *Daemon {
	return &Daemon{
		Config: config,
		client: route53Client,
	}
}

func (d *Daemon) Start(ctx context.Context) {
	logger := zerolog.Ctx(ctx)
	logger.Info().Stringer("polling interval", d.Config.Polling.Interval).Msg("Starting dynamic53 daemon")

	ticker := time.NewTicker(d.Config.Polling.Interval)
	defer ticker.Stop()

	for {
		timeoutCtx, cancel := context.WithTimeout(ctx, d.Config.Polling.Interval)

		err := d.doUpdate(timeoutCtx)
		if err != nil {
			logger.Error().Err(fmt.Errorf("update failed: %w", err)).Send()
		}

		cancel()

		select {
		case <-ticker.C:
			continue
		case <-ctx.Done():
			logger.Info().Msg("Stopping dynamic53 daemon")
			return
		}
	}
}

func (d *Daemon) doUpdate(ctx context.Context) error {
	logger := zerolog.Ctx(ctx)
	logger.Debug().Stringer("max jitter", d.Config.Polling.MaxJitter).Msg("Polling for current public address")

	if d.Config.Polling.MaxJitter != 0 {
		jitter := time.Duration(rand.Int64N(int64(d.Config.Polling.MaxJitter)))

		logger.Debug().Stringer("jitter", jitter).Msg("Going to sleep")
		time.Sleep(jitter)
		logger.Debug().Stringer("jitter", jitter).Msg("Finished sleeping")
	}

	if ctx.Err() != nil {
		return fmt.Errorf("context cancelled: %w", ctx.Err())
	}

	ipv4, err := GetPublicIPv4(ctx)
	if err != nil {
		return fmt.Errorf("failed to get public IPv4 address: %w", err)
	}

	logger.Info().IPAddr("ipv4", ipv4).Msg("Retrieved current public address")

	// TODO: Maybe set an upper bound for the number of goroutines
	var wg sync.WaitGroup
	wg.Add(len(d.Config.Zones))

	for _, zone := range d.Config.Zones {
		go func() {
			err := d.updateRecords(ctx, zone, ipv4)
			if err != nil {
				logger.Error().Err(fmt.Errorf("error updating route53 zone: %w", err)).Send()
			}

			wg.Done()
		}()
	}

	wg.Wait()

	return nil
}

func (d *Daemon) updateRecords(ctx context.Context, zone ZoneConfig, ipv4 net.IP) error {
	logger := zerolog.Ctx(ctx)
	logger.Debug().Str("zone id", zone.Id).Str("zone name", zone.Name).Msg("Updating records in zone")

	zoneId := zone.Id

	// Check if hosted zone exists and get its ID if we don't already have it
	if zoneId != "" {
		input := route53.GetHostedZoneInput{Id: &zoneId}

		// Double-check that the given ID points to a real hosted zone
		_, err := d.client.GetHostedZone(ctx, &input)
		if err != nil {
			return fmt.Errorf("error getting hosted zone info: %w", err)
		}
	} else {
		maxItems := int32(1)
		input := route53.ListHostedZonesByNameInput{DNSName: &zone.Name, MaxItems: &maxItems}

		// Find the hosted zone with the given name, if it exists
		listOutput, err := d.client.ListHostedZonesByName(ctx, &input)
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
	value := ipv4.String()
	changes := make([]types.Change, 0, len(zone.Records))
	for _, record := range zone.Records {
		change := types.Change{
			Action: types.ChangeActionUpsert,
			ResourceRecordSet: &types.ResourceRecordSet{
				Name:            &record,
				Type:            types.RRTypeA,
				ResourceRecords: []types.ResourceRecord{{Value: &value}},
			},
		}
		changes = append(changes, change)
	}

	input := route53.ChangeResourceRecordSetsInput{
		HostedZoneId: &zoneId,
		ChangeBatch:  &types.ChangeBatch{Changes: changes},
	}

	changeOutput, err := d.client.ChangeResourceRecordSets(ctx, &input)
	if err != nil {
		return fmt.Errorf("failed to update resource records: %w", err)
	}

	// The docs say this generally happens within 60 seconds, so let's go 90
	timeoutCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	err = d.waitForRRChange(timeoutCtx, changeOutput.ChangeInfo.Id, 5)
	cancel()

	if err != nil {
		return fmt.Errorf("error waiting for resource record change to propagate: %w", err)
	}

	return nil
}

func (d *Daemon) waitForRRChange(ctx context.Context, changeId *string, backoffLimit int) error {
	logger := zerolog.Ctx(ctx)
	logger.Debug().Int("backoff limit", backoffLimit).Str("change id", *changeId).Msg("Waiting for Route 53 change to propagate")

	if backoffLimit < 1 {
		return fmt.Errorf("backoffLimit must be greater than or equal to 1")
	}

	for i := 1; i <= backoffLimit; i++ {
		if ctx.Err() != nil {
			return fmt.Errorf("context cancelled: %w", ctx.Err())
		}

		output, err := d.client.GetChange(ctx, &route53.GetChangeInput{Id: changeId})
		if err != nil {
			logger.Warn().Str("change id", *changeId).Err(fmt.Errorf("failed to get change status: %w", err)).Send()
			continue
		}

		if output.ChangeInfo.Status == types.ChangeStatusInsync {
			return nil
		}

		sleep := time.Duration(i^2) * time.Second

		logger.Debug().Str("change id", *changeId).Stringer("sleep duration", sleep).Msg("Going to sleep")
		time.Sleep(sleep)
		logger.Debug().Str("change id", *changeId).Stringer("sleep duration", sleep).Msg("Finished sleeping")
	}

	return fmt.Errorf("maximum tries exceeded")
}
