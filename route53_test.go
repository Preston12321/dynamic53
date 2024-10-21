package dynamic53

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/cenkalti/backoff/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

type mockRoute53Client struct {
	mockGetHostedZone            func(context.Context, *route53.GetHostedZoneInput) (*route53.GetHostedZoneOutput, error)
	mockListHostedZonesByName    func(context.Context, *route53.ListHostedZonesByNameInput) (*route53.ListHostedZonesByNameOutput, error)
	mockListResourceRecordSets   func(context.Context, *route53.ListResourceRecordSetsInput) (*route53.ListResourceRecordSetsOutput, error)
	mockChangeResourceRecordSets func(context.Context, *route53.ChangeResourceRecordSetsInput) (*route53.ChangeResourceRecordSetsOutput, error)
	mockGetChange                func(context.Context, *route53.GetChangeInput) (*route53.GetChangeOutput, error)
}

func (u mockRoute53Client) GetHostedZone(
	ctx context.Context,
	params *route53.GetHostedZoneInput,
	_ ...func(*route53.Options),
) (*route53.GetHostedZoneOutput, error) {
	return u.mockGetHostedZone(ctx, params)
}

func (u mockRoute53Client) ListHostedZonesByName(
	ctx context.Context,
	params *route53.ListHostedZonesByNameInput,
	_ ...func(*route53.Options),
) (*route53.ListHostedZonesByNameOutput, error) {
	return u.mockListHostedZonesByName(ctx, params)
}

func (u mockRoute53Client) ListResourceRecordSets(
	ctx context.Context,
	params *route53.ListResourceRecordSetsInput,
	_ ...func(*route53.Options),
) (*route53.ListResourceRecordSetsOutput, error) {
	return u.mockListResourceRecordSets(ctx, params)
}

func (u mockRoute53Client) ChangeResourceRecordSets(
	ctx context.Context,
	input *route53.ChangeResourceRecordSetsInput,
	_ ...func(*route53.Options),
) (*route53.ChangeResourceRecordSetsOutput, error) {
	return u.mockChangeResourceRecordSets(ctx, input)
}

func (u mockRoute53Client) GetChange(
	ctx context.Context,
	params *route53.GetChangeInput,
	_ ...func(*route53.Options),
) (*route53.GetChangeOutput, error) {
	return u.mockGetChange(ctx, params)
}

func createZeroBackoff() backoff.BackOff {
	return &backoff.ZeroBackOff{}
}

func TestShortenZoneId(t *testing.T) {
	type testCase struct {
		name           string
		input          string
		expectedOutput string
	}

	cases := []testCase{
		{"already shortened", "foobar", "foobar"},
		{"needs to be shortened", "/hostedzone/foobar", "foobar"},
		{"don't remove from middle of the string", "foo/hostedzone/bar", "foo/hostedzone/bar"},
		{"don't remove from the end of the string", "foobar/hostedzone/", "foobar/hostedzone/"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(tt *testing.T) {
			assertion := assert.New(tt)
			assertion.Equal(tc.expectedOutput, ShortenZoneId(tc.input))
		})
	}
}

func TestShortenDNSName(t *testing.T) {
	type testCase struct {
		name           string
		input          string
		expectedOutput string
	}

	cases := []testCase{
		{"shortened", "foobar.com", "foobar.com"},
		{"unshortened", "foobar.com.", "foobar.com"},
		{"don't remove non-trailing (shortened)", "example.foobar.com", "example.foobar.com"},
		{"don't remove non-trailing (unshortened)", "example.foobar.com.", "example.foobar.com"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(tt *testing.T) {
			assertion := assert.New(tt)
			assertion.Equal(tc.expectedOutput, ShortenDNSName(tc.input))
		})
	}
}

func TestHostedZoneFromConfig(t *testing.T) {
	type testCase struct {
		name                            string
		inputId                         string
		inputName                       string
		outputName                      string
		expectListHostedZonesByNameCall bool
		expectGetHostedZoneCall         bool
		expectErr                       bool
	}

	cases := []testCase{
		{
			name:      "id is empty, name is empty",
			expectErr: true,
		},
		{
			name:                            "id is empty, name is given",
			inputName:                       "foobar.com",
			expectListHostedZonesByNameCall: true,
			expectErr:                       false,
		},
		{
			name:                    "id is given, name is empty",
			inputId:                 "foobar",
			expectGetHostedZoneCall: true,
			expectErr:               false,
		},
		{
			name:                    "id is given, name is given",
			inputId:                 "foobar",
			inputName:               "foobar.com",
			expectGetHostedZoneCall: true,
			expectErr:               false,
		},
		{
			name:                    "id is given, name is given but mismatched",
			inputId:                 "foobar",
			inputName:               "foobar.com",
			outputName:              "not.foobar.com",
			expectGetHostedZoneCall: true,
			expectErr:               true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(tt *testing.T) {
			assertion := assert.New(tt)
			ctx := context.Background()

			hostedZone := &types.HostedZone{
				Id:   &tc.inputId,
				Name: &tc.inputName,
			}
			if tc.outputName != "" {
				hostedZone.Name = &tc.outputName
			}

			client := mockRoute53Client{
				mockListHostedZonesByName: func(ctx context.Context, input *route53.ListHostedZonesByNameInput) (*route53.ListHostedZonesByNameOutput, error) {
					if !tc.expectListHostedZonesByNameCall {
						assertion.Fail("unexpected call to ListHostedZonesByName")
					}
					output := &route53.ListHostedZonesByNameOutput{
						HostedZones: []types.HostedZone{*hostedZone},
					}
					return output, nil
				},
				mockGetHostedZone: func(ctx context.Context, input *route53.GetHostedZoneInput) (*route53.GetHostedZoneOutput, error) {
					if !tc.expectGetHostedZoneCall {
						assertion.Fail("unexpected call to GetHostedZone")
					}
					output := &route53.GetHostedZoneOutput{
						HostedZone: hostedZone,
					}
					return output, nil
				},
			}
			utility := ZoneUtility{manager: client}

			config := ZoneConfig{
				Id:   tc.inputId,
				Name: tc.inputName,
			}
			zone, err := utility.HostedZoneFromConfig(ctx, config)

			if tc.expectErr {
				assertion.Error(err)
				return
			}

			assertion.NoError(err)
			assertion.Equal(tc.inputId, *zone.Id)
			assertion.Equal(tc.inputName, *zone.Name)
		})
	}
}

func TestHostedZoneFromId(t *testing.T) {
	type testCase struct {
		name         string
		inputId      string
		mockErr      error
		expectErr    bool
		expectErrMsg string
	}

	id1 := "foobar"
	id2 := "foobarbaz"
	cases := []testCase{
		{
			name:         "failed request",
			inputId:      id1,
			mockErr:      errors.New("test error"),
			expectErr:    true,
			expectErrMsg: "test error",
		},
		{
			name:      "successful request",
			inputId:   id1,
			mockErr:   nil,
			expectErr: false,
		},
		{
			name:      "successful request #2",
			inputId:   id2,
			mockErr:   nil,
			expectErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(tt *testing.T) {
			assertion := assert.New(tt)

			type key string
			ctx := context.WithValue(context.Background(), key("foo"), "bar")

			client := mockRoute53Client{
				mockGetHostedZone: func(ctx context.Context, input *route53.GetHostedZoneInput) (*route53.GetHostedZoneOutput, error) {
					assertion.Nil(ctx.Err(), "context should not be canceled")
					assertion.Equal("bar", ctx.Value(key("foo")), "context should be the same as passed in")
					assertion.Equal(tc.inputId, *input.Id)

					if tc.mockErr != nil {
						return nil, tc.mockErr
					}

					output := &route53.GetHostedZoneOutput{
						HostedZone: &types.HostedZone{Id: &tc.inputId},
					}
					return output, nil
				},
			}
			utility := ZoneUtility{manager: client}

			zone, err := utility.HostedZoneFromId(ctx, tc.inputId)

			if tc.expectErr {
				assertion.Error(err)

				if tc.expectErrMsg != "" {
					assertion.ErrorContains(err, tc.expectErrMsg)
				}

				return
			}

			assertion.NoError(err)
			assertion.Equal(tc.inputId, *zone.Id)
		})
	}
}

func TestHostedZoneFromName(t *testing.T) {
	type testCase struct {
		name         string
		inputName    string
		mockOutput   *route53.ListHostedZonesByNameOutput
		mockErr      error
		expectErr    bool
		expectErrMsg string
	}

	name1 := "foobar.com"
	name2 := "foobar.baz.com"
	cases := []testCase{
		{
			name:         "failed request",
			inputName:    name1,
			mockOutput:   nil,
			mockErr:      errors.New("test error"),
			expectErr:    true,
			expectErrMsg: "test error",
		},
		{
			name:       "successful request, nil zones list",
			inputName:  name1,
			mockOutput: &route53.ListHostedZonesByNameOutput{},
			mockErr:    nil,
			expectErr:  true,
		},
		{
			name:       "successful request, empty zones list",
			inputName:  name1,
			mockOutput: &route53.ListHostedZonesByNameOutput{HostedZones: []types.HostedZone{}},
			mockErr:    nil,
			expectErr:  true,
		},
		{
			name:       "successful request",
			inputName:  name1,
			mockOutput: &route53.ListHostedZonesByNameOutput{HostedZones: []types.HostedZone{{Name: &name1}}},
			mockErr:    nil,
			expectErr:  false,
		},
		{
			name:       "successful request #2",
			inputName:  name2,
			mockOutput: &route53.ListHostedZonesByNameOutput{HostedZones: []types.HostedZone{{Name: &name2}}},
			mockErr:    nil,
			expectErr:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(tt *testing.T) {
			assertion := assert.New(tt)

			type key string
			ctx := context.WithValue(context.Background(), key("foo"), "bar")

			client := mockRoute53Client{
				mockListHostedZonesByName: func(ctx context.Context, input *route53.ListHostedZonesByNameInput) (*route53.ListHostedZonesByNameOutput, error) {
					assertion.Nil(ctx.Err(), "context should not be canceled")
					assertion.Equal("bar", ctx.Value(key("foo")), "context should be the same as passed in")
					assertion.Equal((*string)(nil), input.HostedZoneId)
					assertion.Equal(int32(1), *input.MaxItems)
					assertion.Equal(tc.inputName, *input.DNSName)
					return tc.mockOutput, tc.mockErr
				},
			}
			utility := ZoneUtility{manager: client}

			zone, err := utility.HostedZoneFromName(ctx, tc.inputName)

			if tc.expectErr {
				assertion.Error(err)

				if tc.expectErrMsg != "" {
					assertion.ErrorContains(err, tc.expectErrMsg)
				}

				return
			}

			assertion.NoError(err)
			assertion.Equal(tc.inputName, *zone.Name)
		})
	}
}

func TestGetChangesForZone(t *testing.T) {
	type testCase struct {
		name          string
		inputZone     *types.HostedZone
		inputRecords  []string
		inputTTL      int64
		inputIPv4     net.IP
		mockOutput    *route53.ListResourceRecordSetsOutput
		mockErr       string
		expectChanges []types.Change
		expectErr     bool
	}

	name1 := "example1.foobar.com"
	name2 := "example2.foobar.com"
	name3 := "example3.foobar.com"
	name4 := "example4.foobar.com"
	name5 := "example5.foobar.com"
	name6 := "example6.foobar.com"
	name7 := "example7.foobar.com"
	text := "example TXT record"
	ttl1 := int64(300)
	ttl2 := int64(150)
	setId := "test id"
	ip1 := "192.168.0.1"
	ip2 := "10.0.0.1"
	ipv4 := net.ParseIP(ip1)
	id := "foobar"
	zone := &types.HostedZone{Id: &id}

	cases := []testCase{
		{
			name:      "nil zone",
			inputZone: nil,
			expectErr: true,
		},
		{
			name:          "zero records to change",
			inputZone:     zone,
			inputRecords:  []string{},
			inputTTL:      300,
			inputIPv4:     ipv4,
			expectChanges: []types.Change{},
			expectErr:     false,
		},
		{
			name:         "failed request",
			inputZone:    zone,
			inputRecords: []string{"foobar.com"},
			inputTTL:     300,
			inputIPv4:    ipv4,
			mockErr:      "test error",
			expectErr:    true,
		},
		{
			name:         "truncated response",
			inputZone:    zone,
			inputRecords: []string{"foobar.com"},
			inputTTL:     300,
			inputIPv4:    ipv4,
			mockOutput:   &route53.ListResourceRecordSetsOutput{IsTruncated: true},
			expectErr:    true,
		},
		{
			name:         "complete diff",
			inputZone:    zone,
			inputRecords: []string{name1, name2, name3, name4, name5, name6},
			inputTTL:     300,
			inputIPv4:    ipv4,
			mockOutput: &route53.ListResourceRecordSetsOutput{
				ResourceRecordSets: []types.ResourceRecordSet{
					// Already exists. Won't be in change batch
					{
						Name:            &name1,
						Type:            types.RRTypeA,
						TTL:             &ttl1,
						ResourceRecords: []types.ResourceRecord{{Value: &ip1}},
					},
					// Not an A record. Will be ignored
					{
						Name:            &name2,
						Type:            types.RRTypeTxt,
						TTL:             &ttl1,
						ResourceRecords: []types.ResourceRecord{{Value: &text}},
					},
					// Has the wrong value. Will be included in the change batch
					{
						Name:            &name2,
						Type:            types.RRTypeA,
						TTL:             &ttl1,
						ResourceRecords: []types.ResourceRecord{{Value: &ip2}},
					},
					// Has the wrong TTL. Will be included in the change batch
					{
						Name:            &name3,
						Type:            types.RRTypeA,
						TTL:             &ttl2,
						ResourceRecords: []types.ResourceRecord{{Value: &ip1}},
					},
					// Has SetIdentifier. Will be ignored, but overriden
					{
						Name:            &name4,
						Type:            types.RRTypeA,
						TTL:             &ttl1,
						SetIdentifier:   &setId,
						ResourceRecords: []types.ResourceRecord{{Value: &ip1}},
					},
					// Has multiple values. Will be included in the change batch
					{
						Name: &name5,
						Type: types.RRTypeA,
						TTL:  &ttl1,
						ResourceRecords: []types.ResourceRecord{
							{Value: &ip1},
							{Value: &ip2},
						},
					},
					// Doesn't match a desired record. Will be ignored
					{
						Name:            &name7,
						Type:            types.RRTypeA,
						TTL:             &ttl1,
						ResourceRecords: []types.ResourceRecord{{Value: &ip2}},
					},
				},
			},
			expectChanges: []types.Change{
				// Included because record had the wrong value
				{
					Action: types.ChangeActionUpsert,
					ResourceRecordSet: &types.ResourceRecordSet{
						Name:            &name2,
						Type:            types.RRTypeA,
						TTL:             &ttl1,
						ResourceRecords: []types.ResourceRecord{{Value: &ip1}},
					},
				},
				// Included because record had the wrong TTL
				{
					Action: types.ChangeActionUpsert,
					ResourceRecordSet: &types.ResourceRecordSet{
						Name:            &name3,
						Type:            types.RRTypeA,
						TTL:             &ttl1,
						ResourceRecords: []types.ResourceRecord{{Value: &ip1}},
					},
				},
				// Included because record was part of a set
				{
					Action: types.ChangeActionUpsert,
					ResourceRecordSet: &types.ResourceRecordSet{
						Name:            &name4,
						Type:            types.RRTypeA,
						TTL:             &ttl1,
						ResourceRecords: []types.ResourceRecord{{Value: &ip1}},
					},
				},
				// Included because the record had more than one value
				{
					Action: types.ChangeActionUpsert,
					ResourceRecordSet: &types.ResourceRecordSet{
						Name:            &name5,
						Type:            types.RRTypeA,
						TTL:             &ttl1,
						ResourceRecords: []types.ResourceRecord{{Value: &ip1}},
					},
				},
				// Included because it didn't already exist
				{
					Action: types.ChangeActionUpsert,
					ResourceRecordSet: &types.ResourceRecordSet{
						Name:            &name6,
						Type:            types.RRTypeA,
						TTL:             &ttl1,
						ResourceRecords: []types.ResourceRecord{{Value: &ip1}},
					},
				},
			},
			expectErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(tt *testing.T) {
			assertion := assert.New(tt)

			type key string
			logger := zerolog.Nop()
			ctx := logger.WithContext(context.Background())
			ctx = context.WithValue(ctx, key("foo"), "bar")

			client := mockRoute53Client{
				mockListResourceRecordSets: func(ctx context.Context, input *route53.ListResourceRecordSetsInput) (*route53.ListResourceRecordSetsOutput, error) {
					assertion.Nil(ctx.Err(), "context should not be canceled")
					assertion.Equal("bar", ctx.Value(key("foo")), "context should be the same as passed in")
					assertion.NotNil(tc.inputZone)
					assertion.Equal(*tc.inputZone.Id, *input.HostedZoneId)
					assertion.NotNil(input.MaxItems)
					assertion.LessOrEqual(*input.MaxItems, MaxRecordsPerZone)

					if tc.mockErr != "" {
						return nil, errors.New(tc.mockErr)
					}

					return tc.mockOutput, nil
				},
			}
			utility := ZoneUtility{manager: client, createBackoff: createZeroBackoff}

			batch, err := utility.GetChangesForZone(ctx, tc.inputZone, tc.inputRecords, tc.inputTTL, tc.inputIPv4)

			if tc.expectErr {
				assertion.Error(err)

				if tc.mockErr != "" {
					assertion.ErrorContains(err, tc.mockErr)
				}

				return
			}

			assertion.NoError(err)
			assertion.NotNil(batch)
			assertion.ElementsMatch(tc.expectChanges, batch.Changes)
		})
	}
}

func TestApplyChangeBatch(t *testing.T) {
	type testCase struct {
		name                               string
		inputZone                          *types.HostedZone
		inputBatch                         *types.ChangeBatch
		mockChangeId                       string
		mockChangeResourceRecordSetsErrMsg string
		cancelCtx                          bool
		expectErr                          bool
	}

	name := "foobar.com"
	zoneId := "foobar"
	zone := &types.HostedZone{Id: &zoneId}
	ttl := int64(300)
	rrSet := types.ResourceRecordSet{
		Name: &name,
		Type: types.RRTypeA,
		TTL:  &ttl,
	}
	batch := &types.ChangeBatch{
		Changes: []types.Change{
			{
				Action:            types.ChangeActionUpsert,
				ResourceRecordSet: &rrSet,
			},
		},
	}
	cases := []testCase{
		{
			name:       "nil zone",
			inputZone:  nil,
			inputBatch: batch,
			expectErr:  true,
		},
		{
			name:       "nil batch",
			inputZone:  zone,
			inputBatch: nil,
			expectErr:  true,
		},
		{
			name:                               "failed ChangeResourceRecordSets request",
			inputZone:                          zone,
			inputBatch:                         batch,
			mockChangeId:                       "test123",
			mockChangeResourceRecordSetsErrMsg: "test error ChangeResourceRecordSets",
			expectErr:                          true,
		},
		{
			name:         "canceled context",
			inputZone:    zone,
			inputBatch:   batch,
			mockChangeId: "test123",
			cancelCtx:    true,
			expectErr:    true,
		},
		{
			name:         "successful requests",
			inputZone:    zone,
			inputBatch:   batch,
			mockChangeId: "test123",
			expectErr:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(tt *testing.T) {
			assertion := assert.New(tt)

			logger := zerolog.Nop()
			ctx := logger.WithContext(context.Background())
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()

			callCount := 0
			statusSequence := []types.ChangeStatus{types.ChangeStatusPending, types.ChangeStatusInsync}
			client := mockRoute53Client{
				mockChangeResourceRecordSets: func(ctx context.Context, input *route53.ChangeResourceRecordSetsInput) (*route53.ChangeResourceRecordSetsOutput, error) {
					if tc.mockChangeResourceRecordSetsErrMsg != "" {
						return nil, errors.New(tc.mockChangeResourceRecordSetsErrMsg)
					}
					output := &route53.ChangeResourceRecordSetsOutput{
						ChangeInfo: &types.ChangeInfo{
							Id:     &tc.mockChangeId,
							Status: types.ChangeStatusPending,
						},
					}
					return output, nil
				},
				mockGetChange: func(ctx context.Context, input *route53.GetChangeInput) (*route53.GetChangeOutput, error) {
					output := &route53.GetChangeOutput{
						ChangeInfo: &types.ChangeInfo{
							Id:     &tc.mockChangeId,
							Status: statusSequence[callCount],
						},
					}
					callCount += 1
					return output, nil
				},
			}
			utility := ZoneUtility{manager: client, createBackoff: createZeroBackoff}

			// Call cancel early if the test case calls for it
			if tc.cancelCtx {
				cancel()
			}

			err := utility.ApplyChangeBatch(ctx, tc.inputZone, tc.inputBatch)

			if tc.expectErr {
				assertion.Error(err)

				if tc.mockChangeResourceRecordSetsErrMsg != "" {
					assertion.ErrorContains(err, tc.mockChangeResourceRecordSetsErrMsg)
				}

				return
			}

			assertion.NoError(err)
		})
	}
}

func TestWaitForChange(t *testing.T) {
	type mockOutput struct {
		status types.ChangeStatus
		errMsg string
	}
	type testCase struct {
		name            string
		inputId         string
		mockOutputs     []mockOutput
		cancelCtx       bool
		expectCallCount int
		expectErr       bool
	}

	cases := []testCase{
		{
			name:    "canceled context",
			inputId: "foobar",
			mockOutputs: []mockOutput{
				{types.ChangeStatusPending, ""},
			},
			cancelCtx:       true,
			expectCallCount: 1,
			expectErr:       true,
		},
		{
			name:    "immediate sync",
			inputId: "foobar",
			mockOutputs: []mockOutput{
				{types.ChangeStatusInsync, ""},
			},
			expectCallCount: 1,
			expectErr:       false,
		},
		{
			name:    "sync after one try (pending)",
			inputId: "foobar",
			mockOutputs: []mockOutput{
				{types.ChangeStatusPending, ""},
				{types.ChangeStatusInsync, ""},
			},
			expectCallCount: 2,
			expectErr:       false,
		},
		{
			name:    "sync after one try (error)",
			inputId: "foobar",
			mockOutputs: []mockOutput{
				{"", "test error"},
				{types.ChangeStatusInsync, ""},
			},
			expectCallCount: 2,
			expectErr:       false,
		},
		{
			name:    "sync after five tries (mix of pending and error)",
			inputId: "foobar",
			mockOutputs: []mockOutput{
				{"", "test error"},
				{types.ChangeStatusPending, ""},
				{"", "test error"},
				{"", "test error"},
				{types.ChangeStatusPending, ""},
				{types.ChangeStatusInsync, ""},
			},
			expectCallCount: 6,
			expectErr:       false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(tt *testing.T) {
			assertion := assert.New(tt)

			logger := zerolog.Nop()
			ctx := logger.WithContext(context.Background())
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()

			callCount := 0
			client := mockRoute53Client{
				mockGetChange: func(ctx context.Context, input *route53.GetChangeInput) (*route53.GetChangeOutput, error) {
					assertion.Greater(len(tc.mockOutputs), callCount)
					if len(tc.mockOutputs) <= callCount {
						tt.FailNow()
					}

					mock := tc.mockOutputs[callCount]

					// Keep track of how many times this API has been called
					callCount += 1

					if mock.errMsg != "" {
						return nil, errors.New(mock.errMsg)
					}

					output := &route53.GetChangeOutput{
						ChangeInfo: &types.ChangeInfo{
							Id:     &tc.inputId,
							Status: mock.status,
						},
					}

					return output, nil
				},
			}
			utility := ZoneUtility{manager: client, createBackoff: createZeroBackoff}

			// Call cancel early if the test case calls for it
			if tc.cancelCtx {
				cancel()
			}

			err := utility.WaitForChange(ctx, tc.inputId)

			if tc.expectErr {
				assertion.Error(err)
				return
			}

			assertion.NoError(err)
			assertion.Equal(tc.expectCallCount, callCount)
		})
	}
}

func TestVerifyChangeHasPropagated(t *testing.T) {
	type testCase struct {
		name         string
		inputId      string
		mockStatus   types.ChangeStatus
		mockErrMsg   string
		expectResult bool
	}

	cases := []testCase{
		{"failed request", "foobar", "", "test error", false},
		{"change is pending", "foobar", types.ChangeStatusPending, "", false},
		{"change is complete", "foobar", types.ChangeStatusInsync, "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(tt *testing.T) {
			assertion := assert.New(tt)

			type key string
			logger := zerolog.Nop()
			ctx := logger.WithContext(context.Background())
			ctx = context.WithValue(ctx, key("foo"), "bar")

			client := mockRoute53Client{
				mockGetChange: func(ctx context.Context, input *route53.GetChangeInput) (*route53.GetChangeOutput, error) {
					assertion.Nil(ctx.Err(), "context should not be canceled")
					assertion.Equal("bar", ctx.Value(key("foo")), "context should be the same as passed in")
					assertion.Equal(tc.inputId, *input.Id)

					if tc.mockErrMsg != "" {
						return nil, errors.New(tc.mockErrMsg)
					}

					output := &route53.GetChangeOutput{
						ChangeInfo: &types.ChangeInfo{
							Id:     &tc.inputId,
							Status: tc.mockStatus,
						},
					}
					return output, nil
				},
			}
			utility := ZoneUtility{manager: client}

			result, err := utility.VerifyChangeHasPropagated(ctx, tc.inputId)

			if tc.mockErrMsg != "" {
				assertion.Error(err)
				assertion.ErrorContains(err, tc.mockErrMsg)
				return
			}

			assertion.NoError(err)
			assertion.Equal(tc.expectResult, result)
		})
	}
}
