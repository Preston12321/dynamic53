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
	"github.com/rs/zerolog"
)

type Daemon struct {
	Config DaemonConfig

	route53Api Route53Api
}

func NewDaemon(config DaemonConfig, route53Client *route53.Client) *Daemon {
	return &Daemon{
		Config:     config,
		route53Api: Route53Api{Manager: route53Client},
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

// TODO: Ensure daemon can handle the 5 requests-per-second limit on Route 53 APIs
// https://docs.aws.amazon.com/Route53/latest/DeveloperGuide/DNSLimitations.html#limits-api-requests-route-53

func (d *Daemon) updateRecords(ctx context.Context, zone ZoneConfig, ipv4 net.IP) {
	// Either zone.Id or zone.Name might be "", but set both fields on the
	// logger so that it's clear what values were (un)set in the config
	logger := zerolog.Ctx(ctx).With().Str("zoneId", zone.Id).Str("zoneName", zone.Name).Logger()
	logCtx := logger.WithContext(ctx)

	logger.Debug().Msg("Beginning update pass for hosted zone")

	hostedZone, err := d.route53Api.HostedZoneFromConfig(logCtx, zone)
	if err != nil {
		logger.Error().Err(err).Send()
		return
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

	ttl := int64(d.Config.Polling.Interval.Seconds())
	batch, err := d.route53Api.GetChangesForZone(logCtx, hostedZone, zone.Records, ttl, ipv4)
	if err != nil {
		logger.Error().Err(err).Send()
		return
	}

	if len(batch.Changes) == 0 {
		logger.Info().Msg("Hosted zone already has the desired records")
		return
	}

	logger.Info().Msg(fmt.Sprintf("Hosted zone requires %d updates", len(batch.Changes)))

	if d.Config.SkipUpdate {
		logger.Info().Msg("Skipping hosted zone update")
		return
	}

	d.route53Api.ApplyChangeBatch(logCtx, hostedZone, batch)
}
