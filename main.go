package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/cenkalti/backoff/v4"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const ADDRESS_API_URL = "https://ipinfo.io/ip"

func main() {
	configPath := flag.String("config", "", "File path of the config")
	genPolicy := flag.Bool("genpolicy", false, "Generate an AWS identity-based IAM policy for dynamic53 using the given config")
	dryRun := flag.Bool("dryrun", false, "Don't send any API requests to AWS")
	logLevel := flag.String("loglevel", "info", "Logging level")
	flag.Parse()

	// Use the global default logger to report if we're unable to parse the
	// user-supplied log level string
	level, err := zerolog.ParseLevel(*logLevel)
	if err != nil {
		log.Fatal().Err(fmt.Errorf("unable to parse log level string: %w", err)).Send()
	}

	logger := zerolog.New(os.Stdout).With().Timestamp().Logger().Level(level)

	cfg, err := ReadConfig(*configPath)
	if err != nil {
		logger.Fatal().Err(fmt.Errorf("cannot read configuration: %w", err)).Send()
	}

	err = ValidateConfig(cfg, *genPolicy)
	if err != nil {
		logger.Fatal().Err(fmt.Errorf("invalid configuration: %w", err)).Send()
	}

	if *genPolicy {
		policy, err := GenerateIAMPolicy(cfg)
		if err != nil {
			logger.Fatal().Err(fmt.Errorf("invalid configuration: %w", err)).Send()
		}

		fmt.Println(policy)
		return
	}

	ctx := logger.WithContext(context.Background())
	ctx, cancel := context.WithCancel(ctx)

	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		logger.Fatal().Err(fmt.Errorf("cannot load AWS configuration: %w", err)).Send()
	}

	// Request SIGINT signals be sent to a channel for handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)

	daemon := NewDaemon(cfg, route53.NewFromConfig(awsCfg), *dryRun)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		daemon.Start(ctx)
		wg.Done()
	}()

	// Stop the running daemon if SIGINT is sent
	<-sigChan
	cancel()
	wg.Wait()
}

func GenerateIAMPolicy(cfg *Config) (string, error) {
	tmpl, err := template.New("iam-policy.tmpl").ParseFiles("iam-policy.tmpl")
	if err != nil {
		return "", fmt.Errorf("unable to parse template file: %w", err)
	}

	var buffer bytes.Buffer
	err = tmpl.Execute(&buffer, cfg.Zones)
	if err != nil {
		return "", fmt.Errorf("unable to execute template: %w", err)
	}

	return buffer.String(), nil
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
