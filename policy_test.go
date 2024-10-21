package dynamic53

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

const testPolicy1 string = `{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Sid": "AllowListingZones",
            "Effect": "Allow",
            "Action": "route53:ListHostedZonesByName",
            "Resource": "*"
        },
        {
            "Sid": "AllowGettingZonesAndChanges",
            "Effect": "Allow",
            "Action": [
                "route53:GetChange",
                "route53:GetHostedZone",
                "route53:ListResourceRecordSets"
            ],
            "Resource": [
                "arn:aws:route53:::hostedzone/1234567890",
                "arn:aws:route53:::change/*"
            ]
        },
        {
            "Sid": "AllowEditingRecords0",
            "Effect": "Allow",
            "Action": [
                "route53:ChangeResourceRecordSets"
            ],
            "Resource": [
                "arn:aws:route53:::hostedzone/1234567890"
            ],
            "Condition": {
                "ForAllValues:StringEquals": {
                    "route53:ChangeResourceRecordSetsNormalizedRecordNames": [
                        "example.com"
                    ],
                    "route53:ChangeResourceRecordSetsRecordTypes": [
                        "A"
                    ],
                    "route53:ChangeResourceRecordSetsActions": [
                        "CREATE",
                        "UPSERT"
                    ]
                }
            }
        }
    ]
}`

const testPolicy2 string = `{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Sid": "AllowListingZones",
            "Effect": "Allow",
            "Action": "route53:ListHostedZonesByName",
            "Resource": "*"
        },
        {
            "Sid": "AllowGettingZonesAndChanges",
            "Effect": "Allow",
            "Action": [
                "route53:GetChange",
                "route53:GetHostedZone",
                "route53:ListResourceRecordSets"
            ],
            "Resource": [
                "arn:aws:route53:::hostedzone/1234567890",
                "arn:aws:route53:::hostedzone/0987654321",
                "arn:aws:route53:::change/*"
            ]
        },
        {
            "Sid": "AllowEditingRecords0",
            "Effect": "Allow",
            "Action": [
                "route53:ChangeResourceRecordSets"
            ],
            "Resource": [
                "arn:aws:route53:::hostedzone/1234567890"
            ],
            "Condition": {
                "ForAllValues:StringEquals": {
                    "route53:ChangeResourceRecordSetsNormalizedRecordNames": [
                        "example.com"
                    ],
                    "route53:ChangeResourceRecordSetsRecordTypes": [
                        "A"
                    ],
                    "route53:ChangeResourceRecordSetsActions": [
                        "CREATE",
                        "UPSERT"
                    ]
                }
            }
        },
        {
            "Sid": "AllowEditingRecords1",
            "Effect": "Allow",
            "Action": [
                "route53:ChangeResourceRecordSets"
            ],
            "Resource": [
                "arn:aws:route53:::hostedzone/0987654321"
            ],
            "Condition": {
                "ForAllValues:StringEquals": {
                    "route53:ChangeResourceRecordSetsNormalizedRecordNames": [
                        "foobar.com",
                        "example.foobar.com"
                    ],
                    "route53:ChangeResourceRecordSetsRecordTypes": [
                        "A"
                    ],
                    "route53:ChangeResourceRecordSetsActions": [
                        "CREATE",
                        "UPSERT"
                    ]
                }
            }
        }
    ]
}`

func TestGenerateIAMPolicy(t *testing.T) {
	type testCase struct {
		name         string
		config       DaemonConfig
		expectPolicy string
		expectErr    bool
	}

	polling := PollingConfig{
		Interval:  5 * time.Minute,
		MaxJitter: 5 * time.Second,
		Url:       "https://example.com/foo",
	}
	zone1 := ZoneConfig{
		Name: "example.com",
		Id:   "1234567890",
		Records: []string{
			"example.com",
		},
	}
	zone2 := ZoneConfig{
		Name: "foobar.com",
		Id:   "0987654321",
		Records: []string{
			"foobar.com",
			"example.foobar.com",
		},
	}

	cases := []testCase{
		{
			name:      "invalid config",
			config:    DaemonConfig{},
			expectErr: true,
		},
		{
			name: "valid config #1",
			config: DaemonConfig{
				Zones:   []ZoneConfig{zone1},
				Polling: polling,
			},
			expectPolicy: testPolicy1,
			expectErr:    false,
		},
		{
			name: "valid config #2",
			config: DaemonConfig{
				Zones:   []ZoneConfig{zone1, zone2},
				Polling: polling,
			},
			expectPolicy: testPolicy2,
			expectErr:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(tt *testing.T) {
			assertion := assert.New(tt)
			policy, err := GenerateIAMPolicy(tc.config)

			if tc.expectErr {
				assertion.Error(err)
				return
			}

			assertion.NoError(err)
			assertion.Equal(tc.expectPolicy, policy)
		})
	}
}
