package dynamic53

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/cenkalti/backoff/v4"
	"github.com/rs/zerolog"
)

// MaxRecordsPerZone is the maximum number of records per zone supported by
// dynamic53. Neither the configuration passed to dynamic53 nor the actual
// hosted zone in Route 53 may contain more records than this limit. Its value
// comes from the maximum number of records that the Route 53 API is willing to
// return for a single ListResourceRecordSets request.
//
// Determining the set of records that need to be sent on a given update pass
// requires pulling down the entire list of records in that zone in order to
// diff it with the desired state. If dynamic53 were to support pagination of
// the ListResourceRecordSets API during this operation, it could result in up
// to 34 requests, given the maximum number of records allowed by AWS in a
// hosted zone is 10,000. To keep this simple and performant, pagination is not
// supported and the MaxRecordsPerZone limit is enforced.
var MaxRecordsPerZone int32 = 300

var ErrTooManyRecords error = fmt.Errorf("hosted zones with more than %d resource records are unsupported", MaxRecordsPerZone)

// ZoneManager wraps the functionality of a route53.Client that is specifically
// necessary to manage a hosted zone
type ZoneManager interface {
	GetHostedZone(ctx context.Context, params *route53.GetHostedZoneInput, optFns ...func(*route53.Options)) (*route53.GetHostedZoneOutput, error)
	ListHostedZonesByName(ctx context.Context, params *route53.ListHostedZonesByNameInput, optFns ...func(*route53.Options)) (*route53.ListHostedZonesByNameOutput, error)
	ListResourceRecordSets(ctx context.Context, params *route53.ListResourceRecordSetsInput, optFns ...func(*route53.Options)) (*route53.ListResourceRecordSetsOutput, error)
	ChangeResourceRecordSets(ctx context.Context, input *route53.ChangeResourceRecordSetsInput, optFns ...func(*route53.Options)) (*route53.ChangeResourceRecordSetsOutput, error)
	GetChange(ctx context.Context, params *route53.GetChangeInput, optFns ...func(*route53.Options)) (*route53.GetChangeOutput, error)
}

// ZoneUtility provides a high-level set of convenience functions to manage a
// hosted zone
type ZoneUtility struct {
	Manager ZoneManager
}

func (a ZoneUtility) HostedZoneFromConfig(ctx context.Context, zone ZoneConfig) (*types.HostedZone, error) {
	var hostedZone *types.HostedZone

	// Depending on what was supplied in the config, use either the ID or the
	// name of the hosted zone to check if it exists and get info about it. If
	// both ID and name were given in the config, then the ID is used to fetch
	// the zone info, and the name from the config is checked against the name
	// returned by the API
	if zone.Id != "" {
		input := route53.GetHostedZoneInput{Id: &zone.Id}

		// Double-check that the given ID points to a real hosted zone
		output, err := a.Manager.GetHostedZone(ctx, &input)
		if err != nil {
			return nil, fmt.Errorf("error getting hosted zone info: %w", err)
		}

		hostedZone = output.HostedZone

		trimmedConfig := strings.TrimSuffix(zone.Name, ".")
		trimmedActual := strings.TrimSuffix(*hostedZone.Name, ".")

		if zone.Name != "" && trimmedConfig != trimmedActual {
			return nil, fmt.Errorf("hosted zone name does not match config: %s", trimmedActual)
		}
	} else {
		maxItems := int32(1)
		input := route53.ListHostedZonesByNameInput{DNSName: &zone.Name, MaxItems: &maxItems}

		// Find the hosted zone with the given name, if it exists
		listOutput, err := a.Manager.ListHostedZonesByName(ctx, &input)
		if err != nil {
			return nil, fmt.Errorf("error listing hosted zones: %w", err)
		}

		if len(listOutput.HostedZones) == 0 {
			return nil, fmt.Errorf("cannot find hosted zone by name")
		}

		hostedZone = &listOutput.HostedZones[0]
	}

	if *hostedZone.ResourceRecordSetCount > int64(MaxRecordsPerZone) {
		return nil, ErrTooManyRecords
	}

	return hostedZone, nil
}

func (a ZoneUtility) GetChangesForZone(ctx context.Context, zone *types.HostedZone, records []string, ttl int64, ipv4 net.IP) (*types.ChangeBatch, error) {
	logger := zerolog.Ctx(ctx)
	value := ipv4.String()

	// Throw the records list into a map to get quicker lookups for matching
	desiredRecords := make(map[string]*types.ResourceRecordSet, len(records))
	for _, record := range records {
		stripped := strings.TrimSuffix(record, ".")
		desiredRecords[stripped] = &types.ResourceRecordSet{
			Name:            &stripped,
			Type:            types.RRTypeA,
			TTL:             &ttl,
			ResourceRecords: []types.ResourceRecord{{Value: &value}},
		}
	}

	recordsToSend := make([]*types.ResourceRecordSet, 0, len(records))

	input := route53.ListResourceRecordSetsInput{
		HostedZoneId: zone.Id,
		MaxItems:     &MaxRecordsPerZone,
	}

	output, err := a.Manager.ListResourceRecordSets(ctx, &input)
	if err != nil {
		return nil, fmt.Errorf("failed to list resource records: %w", err)
	}

	// Pagination is a pain. If there's more than one page of results, give up
	// and cry
	if output.IsTruncated {
		return nil, ErrTooManyRecords
	}

	for _, awsRecord := range output.ResourceRecordSets {
		stripped := strings.TrimSuffix(*awsRecord.Name, ".")

		// Ignore records of unsupported type
		if awsRecord.Type != types.RRTypeA || awsRecord.SetIdentifier != nil {
			logger.Trace().Msg(fmt.Sprintf("Ignoring non-A remote record: name=%s type=%s", stripped, string(awsRecord.Type)))
			continue
		}

		// Match the remote record name to a desired record state, if one
		// exists. If not, ignore it
		definedRecord, ok := desiredRecords[stripped]
		if !ok {
			logger.Trace().Msg(fmt.Sprintf("Ignoring unmatched remote record: name=%s", stripped))
			continue
		}

		// If the remote record differs from the desired state, add it to the
		// list of records to send
		if *awsRecord.TTL != *definedRecord.TTL || len(awsRecord.ResourceRecords) != 1 || *awsRecord.ResourceRecords[0].Value != *definedRecord.ResourceRecords[0].Value {
			logger.Debug().Msg(fmt.Sprintf("Remote record differs from desired state: name=%s ttl=%d len=%d value=%s", stripped, *awsRecord.TTL, len(awsRecord.ResourceRecords), *awsRecord.ResourceRecords[0].Value))
			recordsToSend = append(recordsToSend, definedRecord)
		}

		// Delete this key from the map of desired records
		delete(desiredRecords, stripped)
	}

	// Because all desired records that matched a pre-existing record were
	// deleted from the map, what's left over are all the records that don't yet
	// exist
	for stripped, record := range desiredRecords {
		logger.Debug().Msg(fmt.Sprintf("Desired record must be created: name=%s ttl=%d value=%s", stripped, ttl, value))
		recordsToSend = append(recordsToSend, record)
	}

	// Create a batch change to update resource records
	changes := make([]types.Change, 0, len(recordsToSend))
	for _, record := range recordsToSend {
		change := types.Change{
			Action:            types.ChangeActionUpsert,
			ResourceRecordSet: record,
		}
		changes = append(changes, change)
	}

	return &types.ChangeBatch{Changes: changes}, nil
}

func (a ZoneUtility) ApplyChangeBatch(ctx context.Context, zone *types.HostedZone, batch *types.ChangeBatch) {
	logger := zerolog.Ctx(ctx)

	input := route53.ChangeResourceRecordSetsInput{
		HostedZoneId: zone.Id,
		ChangeBatch:  batch,
	}

	changeOutput, err := a.Manager.ChangeResourceRecordSets(ctx, &input)
	if err != nil {
		logger.Error().Err(fmt.Errorf("failed to update resource records: %w", err)).Send()
		return
	}

	changeId := strings.TrimPrefix(*changeOutput.ChangeInfo.Id, "/change/")
	logger.Info().Msg("Sent resource record updates for hosted zone")

	// The docs say propagation generally finishes within 60 seconds. We could
	// set a timeout here to enforce some upper limit of wait time, but the
	// parent context will do that for us eventually. Realistically, if we've
	// gotten this far, the AWS change is going to complete at some point, and
	// any failure to see that on the client side is more likely to be a network
	// issue
	err = a.WaitForChange(ctx, &changeId)

	if err != nil {
		logger.Error().Err(fmt.Errorf("error waiting for resource record change to propagate: %w", err)).Send()
		return
	}

	logger.Info().Msg("Hosted zone change has finished propagating")
}

func (a ZoneUtility) WaitForChange(ctx context.Context, changeId *string) error {
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
			done, err := a.VerifyChangeHasPropagated(ctx, changeId)
			if err != nil {
				logger.Warn().Err(err).Send()
				return err
			}

			if !done {
				return fmt.Errorf("change is still pending")
			}

			return nil
		},
		bo,
	)
}

func (a ZoneUtility) VerifyChangeHasPropagated(ctx context.Context, changeId *string) (bool, error) {
	logger := *zerolog.Ctx(ctx)

	output, err := a.Manager.GetChange(ctx, &route53.GetChangeInput{Id: changeId})
	if err != nil {
		wrapped := fmt.Errorf("failed to get change status: %w", err)

		logger.Warn().Err(wrapped).Send()
		return false, wrapped
	}

	return output.ChangeInfo.Status == types.ChangeStatusInsync, nil
}
