package dynamic53

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/cenkalti/backoff/v4"
	"github.com/rs/zerolog"
)

type Daemon struct {
	Config *Config
	DryRun bool
	client *route53.Client
}

func NewDaemon(config *Config, route53Client *route53.Client, dryRun bool) *Daemon {
	return &Daemon{
		Config: config,
		DryRun: dryRun,
		client: route53Client,
	}
}

func (d *Daemon) Start(ctx context.Context) {
	logger := zerolog.Ctx(ctx)
	logger.Info().Stringer("pollingInterval", d.Config.Polling.Interval).Msg("Starting dynamic53 daemon")

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
	logger := *zerolog.Ctx(ctx)
	logger.Debug().Stringer("maxJitter", d.Config.Polling.MaxJitter).Msg("Polling for current public address")

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

	logger = logger.With().IPAddr("ipv4", ipv4).Logger()
	ctx = logger.WithContext(ctx)

	logger.Info().Msg("Retrieved current public address")

	// TODO: Maybe set an upper bound for the number of goroutines
	var wg sync.WaitGroup
	wg.Add(len(d.Config.Zones))

	for _, zone := range d.Config.Zones {
		go func() {
			d.updateRecords(ctx, zone, ipv4)
			wg.Done()
		}()
	}

	wg.Wait()

	return nil
}

func (d *Daemon) updateRecords(ctx context.Context, zone ZoneConfig, ipv4 net.IP) {
	// Either zone.Id or zone.Name might be "", but set both fields on the
	// logger so that it's clear what values were (un)set in the config
	logger := zerolog.Ctx(ctx).With().Str("zoneId", zone.Id).Str("zoneName", zone.Name).Logger()
	logCtx := logger.WithContext(ctx)

	logger.Debug().Msg("Updating records in hosted zone")

	if d.DryRun {
		logger.Info().Msg("Skipping hosted zone update because of dry run")
		return
	}

	var hostedZone *types.HostedZone

	// Depending on what was supplied in the config, use either the ID or the
	// name of the hosted zone to check if it exists and get info about it. If
	// both ID and name were given in the config, then the ID is used to fetch
	// the zone info, and the name from the config is checked against the name
	// returned by the API
	if zone.Id != "" {
		input := route53.GetHostedZoneInput{Id: &zone.Id}

		// Double-check that the given ID points to a real hosted zone
		output, err := d.client.GetHostedZone(logCtx, &input)
		if err != nil {
			logger.Error().Err(fmt.Errorf("error getting hosted zone info: %w", err)).Send()
			return
		}

		hostedZone = output.HostedZone

		if zone.Name != "" && zone.Name != *hostedZone.Name {
			logger.Error().Err(fmt.Errorf("hosted zone name does not match config: %s", *hostedZone.Name)).Send()
			return
		}
	} else {
		maxItems := int32(1)
		input := route53.ListHostedZonesByNameInput{DNSName: &zone.Name, MaxItems: &maxItems}

		// Find the hosted zone with the given name, if it exists
		listOutput, err := d.client.ListHostedZonesByName(logCtx, &input)
		if err != nil {
			logger.Error().Err(fmt.Errorf("error listing hosted zones: %w", err)).Send()
			return
		}

		if len(listOutput.HostedZones) == 0 {
			logger.Error().Err(fmt.Errorf("cannot find hosted zone by name")).Send()
			return
		}

		hostedZone = &listOutput.HostedZones[0]
	}

	// The API returns these in their fully-qualified form, but for the sake of
	// readability in the logs, use the simplified forms
	id := strings.TrimPrefix(*hostedZone.Id, "/hostedzone/")
	name := strings.TrimSuffix(*hostedZone.Name, ".")

	// Now that we've used the config-supplied ID/name to fetch info about the
	// zone, use that data to fill in whatever field might've been missing
	logger = zerolog.Ctx(ctx).With().Str("zoneId", id).Str("zoneName", name).Logger()
	logCtx = logger.WithContext(ctx)

	logger.Debug().Msg("Retrieved info about hosted zone")

	// Create a batch change to update resource records
	ttl := int64(d.Config.Polling.Interval.Seconds())
	value := ipv4.String()
	changes := make([]types.Change, 0, len(zone.Records))
	for _, record := range zone.Records {
		change := types.Change{
			Action: types.ChangeActionUpsert,
			ResourceRecordSet: &types.ResourceRecordSet{
				Name:            &record,
				Type:            types.RRTypeA,
				TTL:             &ttl,
				ResourceRecords: []types.ResourceRecord{{Value: &value}},
			},
		}
		changes = append(changes, change)
	}

	input := route53.ChangeResourceRecordSetsInput{
		HostedZoneId: &id,
		ChangeBatch:  &types.ChangeBatch{Changes: changes},
	}

	changeOutput, err := d.client.ChangeResourceRecordSets(logCtx, &input)
	if err != nil {
		logger.Error().Err(fmt.Errorf("failed to update resource records: %w", err)).Send()
		return
	}

	changeId := strings.TrimPrefix(*changeOutput.ChangeInfo.Id, "/change/")
	logger.Info().Msg("Sent resource record change for Route 53 hosted zone")

	// The docs say propagation generally finishes within 60 seconds. We could
	// set a timeout here to enforce some upper limit of wait time, but the
	// parent context will do that for us eventually. Realistically, if we've
	// gotten this far, the AWS change is going to complete at some point, and
	// any failure to see that on the client side is more likely to be a network
	// issue
	err = d.waitForRRChange(logCtx, &changeId)

	if err != nil {
		logger.Error().Err(fmt.Errorf("error waiting for resource record change to propagate: %w", err)).Send()
		return
	}

	logger.Info().Msg("Route 53 resource record change has finished propagating")
}

func (d *Daemon) waitForRRChange(ctx context.Context, changeId *string) error {
	logger := zerolog.Ctx(ctx).With().Str("changeId", *changeId).Logger()
	ctx = logger.WithContext(ctx)

	logger.Debug().Msg("Waiting for Route 53 change to propagate")

	bo := backoff.WithContext(
		backoff.NewExponentialBackOff(
			backoff.WithInitialInterval(5*time.Second),
			backoff.WithMaxInterval(30*time.Second),
			backoff.WithRandomizationFactor(0.25),
		),
		ctx,
	)
	return backoff.Retry(
		func() error {
			return d.verifyChangeHasPropagated(ctx, changeId)
		},
		bo,
	)
}

func (d *Daemon) verifyChangeHasPropagated(ctx context.Context, changeId *string) error {
	logger := *zerolog.Ctx(ctx)

	output, err := d.client.GetChange(ctx, &route53.GetChangeInput{Id: changeId})
	if err != nil {
		wrapped := fmt.Errorf("failed to get change status: %w", err)

		logger.Warn().Err(wrapped).Send()
		return wrapped
	}

	if output.ChangeInfo.Status != types.ChangeStatusInsync {
		logger.Debug().Msg("change is still pending")
		return fmt.Errorf("change is still pending")
	}

	return nil
}
