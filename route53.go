package dynamic53

import (
	"context"
	"errors"
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

// ErrTooManyRecords signifies that a hosted zone contains more resource records
// than the supported limit, MaxRecordsPerZone.
var ErrTooManyRecords error = fmt.Errorf("hosted zones with more than %d resource records are unsupported", MaxRecordsPerZone)

// zoneManager wraps the functionality of a route53.Client that is specifically
// necessary to manage a hosted zone.
type zoneManager interface {
	GetHostedZone(
		ctx context.Context,
		params *route53.GetHostedZoneInput,
		optFns ...func(*route53.Options),
	) (*route53.GetHostedZoneOutput, error)
	ListHostedZonesByName(
		ctx context.Context,
		params *route53.ListHostedZonesByNameInput,
		optFns ...func(*route53.Options),
	) (*route53.ListHostedZonesByNameOutput, error)
	ListResourceRecordSets(
		ctx context.Context,
		params *route53.ListResourceRecordSetsInput,
		optFns ...func(*route53.Options),
	) (*route53.ListResourceRecordSetsOutput, error)
	ChangeResourceRecordSets(
		ctx context.Context,
		input *route53.ChangeResourceRecordSetsInput,
		optFns ...func(*route53.Options),
	) (*route53.ChangeResourceRecordSetsOutput, error)
	GetChange(
		ctx context.Context,
		params *route53.GetChangeInput,
		optFns ...func(*route53.Options),
	) (*route53.GetChangeOutput, error)
}

func createDefaultBackoff() backoff.BackOff {
	return backoff.NewExponentialBackOff(
		backoff.WithInitialInterval(5*time.Second),
		backoff.WithMaxInterval(30*time.Second),
		backoff.WithRandomizationFactor(0.25),
	)
}

// ShortenZoneId returns the given hosted zone ID without its optional prefix.
func ShortenZoneId(id string) string {
	return strings.TrimPrefix(id, "/hostedzone/")
}

// ShortenDNSName returns the given DNS name without a trailing dot.
func ShortenDNSName(name string) string {
	return strings.TrimSuffix(name, ".")
}

// ZoneUtility provides a high-level set of convenience functions to manage a
// hosted zone.
type ZoneUtility struct {
	manager       zoneManager
	createBackoff func() backoff.BackOff
}

func NewZoneUtility(route53Client *route53.Client) ZoneUtility {
	return ZoneUtility{
		manager:       route53Client,
		createBackoff: createDefaultBackoff,
	}
}

// HostedZoneFromConfig retrieves informaton on the hosted zone specified by the
// given configuration. If the Id is defined, the lookup is done based on that,
// with a cross-check of the configured Name if it is also defined. Otherwise,
// the Name is used for the lookup.
func (u ZoneUtility) HostedZoneFromConfig(ctx context.Context, zone ZoneConfig) (*types.HostedZone, error) {
	if zone.Id == "" {
		if zone.Name == "" {
			return nil, errors.New("hosted zone config has neither id nor name set")
		}

		return u.HostedZoneFromName(ctx, zone.Name)
	}

	hostedZone, err := u.HostedZoneFromId(ctx, zone.Id)
	if err != nil {
		return nil, err
	}

	// If the zone's name was supplied in the config, do a sanity check to
	// make sure it matches what was returned by the API
	actualName := ShortenDNSName(*hostedZone.Name)
	if zone.Name != "" && ShortenDNSName(zone.Name) != actualName {
		return nil, fmt.Errorf("hosted zone name does not match config: %s", actualName)
	}

	return hostedZone, nil
}

// HostedZoneFromId retrieves informaton on the hosted zone with the given id.
func (u ZoneUtility) HostedZoneFromId(ctx context.Context, id string) (*types.HostedZone, error) {
	input := route53.GetHostedZoneInput{Id: &id}

	// Double-check that the given ID points to a real hosted zone
	output, err := u.manager.GetHostedZone(ctx, &input)
	if err != nil {
		return nil, fmt.Errorf("error getting hosted zone info: %w", err)
	}

	return output.HostedZone, nil
}

// HostedZoneFromName retrieves informaton on the hosted zone with the given
// name.
func (u ZoneUtility) HostedZoneFromName(ctx context.Context, dnsName string) (*types.HostedZone, error) {
	maxItems := int32(1)
	input := route53.ListHostedZonesByNameInput{DNSName: &dnsName, MaxItems: &maxItems}

	// Find the hosted zone with the given name, if it exists
	listOutput, err := u.manager.ListHostedZonesByName(ctx, &input)
	if err != nil {
		return nil, fmt.Errorf("error listing hosted zones: %w", err)
	}

	if len(listOutput.HostedZones) == 0 {
		return nil, fmt.Errorf("cannot find hosted zone by name")
	}

	return &listOutput.HostedZones[0], nil
}

// GetChangesForZone computes the changes necessary for all specified A records
// in the given hosted zone to reflect the specified IPv4 address with the given
// TTL.
func (u ZoneUtility) GetChangesForZone(ctx context.Context, zone *types.HostedZone, records []string, ttl int64, ipv4 net.IP) (*types.ChangeBatch, error) {
	if zone == nil {
		return nil, errors.New("nil zone")
	}

	if len(records) == 0 {
		return &types.ChangeBatch{Changes: []types.Change{}}, nil
	}

	logger := zerolog.Ctx(ctx)
	value := ipv4.String()

	// Throw the records list into a map to get quicker lookups for matching
	desiredRecords := make(map[string]*types.ResourceRecordSet, len(records))
	for _, record := range records {
		stripped := ShortenDNSName(record)
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

	output, err := u.manager.ListResourceRecordSets(ctx, &input)
	if err != nil {
		return nil, fmt.Errorf("failed to list resource records: %w", err)
	}

	// Pagination is a pain. If there's more than one page of results, give up
	// and cry
	if output.IsTruncated {
		return nil, ErrTooManyRecords
	}

	for _, awsRecord := range output.ResourceRecordSets {
		stripped := ShortenDNSName(*awsRecord.Name)

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

		// Remote record already has the desired state. Delete this key from the
		// map of desired records
		logger.Debug().Msg(fmt.Sprintf("Desired record already exists: name=%s ttl=%d value=%s", stripped, *awsRecord.TTL, *awsRecord.ResourceRecords[0].Value))
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

// ApplyChangeBatch applies the given changes to the hosted zone, blocking until
// those changes have completed.
func (u ZoneUtility) ApplyChangeBatch(ctx context.Context, zone *types.HostedZone, batch *types.ChangeBatch) error {
	if zone == nil {
		return errors.New("nil zone")
	}

	if batch == nil {
		return errors.New("nil change batch")
	}

	logger := zerolog.Ctx(ctx)

	input := route53.ChangeResourceRecordSetsInput{
		HostedZoneId: zone.Id,
		ChangeBatch:  batch,
	}

	changeOutput, err := u.manager.ChangeResourceRecordSets(ctx, &input)
	if err != nil {
		return fmt.Errorf("failed to update resource records: %w", err)
	}

	changeId := strings.TrimPrefix(*changeOutput.ChangeInfo.Id, "/change/")
	logger.Info().Msg("Sent resource record updates for hosted zone")

	// The docs say propagation generally finishes within 60 seconds. We could
	// set a timeout here to enforce some upper limit of wait time, but the
	// parent context will do that for us eventually. Realistically, if we've
	// gotten this far, the AWS change is going to complete at some point, and
	// any failure to see that on the client side is more likely to be a network
	// issue
	err = u.WaitForChange(ctx, changeId)
	if err != nil {
		return fmt.Errorf("error waiting for resource record change to propagate: %w", err)
	}

	logger.Info().Msg("Hosted zone change has finished propagating")
	return nil
}

// WaitForChange blocks until the hosted zone change corresponding to the given
// changeId has completed.
func (u ZoneUtility) WaitForChange(ctx context.Context, changeId string) error {
	logger := zerolog.Ctx(ctx).With().Str("changeId", changeId).Logger()
	ctx = logger.WithContext(ctx)

	logger.Debug().Msg("Waiting for Route 53 change to propagate")

	var bo backoff.BackOff

	if u.createBackoff != nil {
		bo = u.createBackoff()
	} else {
		bo = createDefaultBackoff()
	}

	return backoff.Retry(
		func() error {
			done, err := u.VerifyChangeHasPropagated(ctx, changeId)
			if err != nil {
				logger.Warn().Err(err).Send()
				return err
			}

			if !done {
				return fmt.Errorf("change is still pending")
			}

			return nil
		},
		backoff.WithContext(bo, ctx),
	)
}

// VerifyChangeHasPropagated returns a boolean describing whether the hosted
// zone change corresponding to the given changeId has completed propagation.
func (u ZoneUtility) VerifyChangeHasPropagated(ctx context.Context, changeId string) (bool, error) {
	logger := *zerolog.Ctx(ctx)

	output, err := u.manager.GetChange(ctx, &route53.GetChangeInput{Id: &changeId})
	if err != nil {
		wrapped := fmt.Errorf("failed to get change status: %w", err)

		logger.Warn().Err(wrapped).Send()
		return false, wrapped
	}

	return output.ChangeInfo.Status == types.ChangeStatusInsync, nil
}
