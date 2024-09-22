package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
)

const ADDRESS_API_URL = "https://ipinfo.io/ip"

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

	daemon := NewDaemon(cfg, route53.NewFromConfig(awsCfg))
	go daemon.Start(context.Background())
}

// GetPublicAddress attempts to determine the current public IPv4 address of the
// host by making a request to an external third-party API
func GetPublicAddress(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, ADDRESS_API_URL, nil)
	if err != nil {
		return "", fmt.Errorf("cannot create GET request: %w", err)
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("GET request failed: %w", err)
	}

	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status on response: %s", response.Status)
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	return string(body), nil
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
	ticker := time.NewTicker(d.Config.Polling.Interval)
	defer ticker.Stop()

	for {
		timeoutCtx, cancel := context.WithTimeout(ctx, d.Config.Polling.Interval)

		// TODO: Log this error instead of ignoring it
		_ = d.doUpdate(timeoutCtx)

		cancel()

		select {
		case <-ticker.C:
			continue
		case <-ctx.Done():
			return
		}
	}
}

func (d *Daemon) doUpdate(ctx context.Context) error {
	if d.Config.Polling.MaxJitter != 0 {
		jitter := rand.Int64N(int64(d.Config.Polling.MaxJitter))
		time.Sleep(time.Duration(jitter))
	}

	if ctx.Err() != nil {
		return fmt.Errorf("context cancelled: %w", ctx.Err())
	}

	address, err := GetPublicAddress(ctx)
	if err != nil {
		return fmt.Errorf("failed to get public IPv4 address: %w", err)
	}

	errChan := make(chan error, len(d.Config.Zones))

	// TODO: Maybe set an upper bound for the number of goroutines
	var wg sync.WaitGroup
	wg.Add(len(d.Config.Zones))

	for _, zone := range d.Config.Zones {
		go func() {
			errChan <- d.updateRecords(ctx, zone, address)
			wg.Done()
		}()
	}

	wg.Wait()

	// Collect all the errors together in a slice
	errs := make([]error, len(d.Config.Zones))
	for err := range errChan {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

func (d *Daemon) updateRecords(ctx context.Context, zone ZoneConfig, address string) error {
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
	changes := make([]types.Change, 0, len(zone.Records))
	for _, record := range zone.Records {
		change := types.Change{
			Action: types.ChangeActionUpsert,
			ResourceRecordSet: &types.ResourceRecordSet{
				Name:            &record,
				Type:            types.RRTypeA,
				ResourceRecords: []types.ResourceRecord{{Value: &address}},
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
	err = d.waitForRRChange(timeoutCtx, changeOutput.ChangeInfo.Id)
	cancel()

	if err != nil {
		return fmt.Errorf("error waiting for resource record change to propagate: %w", err)
	}

	return nil
}

func (d *Daemon) waitForRRChange(ctx context.Context, changeId *string) error {
	for i := 1; i <= 5; i++ {
		if ctx.Err() != nil {
			return fmt.Errorf("context cancelled: %w", ctx.Err())
		}

		output, err := d.client.GetChange(ctx, &route53.GetChangeInput{Id: changeId})
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
