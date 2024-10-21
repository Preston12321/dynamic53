package dynamic53

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type mockHttpClient struct {
	mockDo func(request *http.Request) (*http.Response, error)
}

func (m mockHttpClient) Do(request *http.Request) (*http.Response, error) {
	return m.mockDo(request)
}

func TestGetPublicIPv4(t *testing.T) {
	type testCase struct {
		name               string
		mockResponseStatus int
		mockResponseBody   string
		mockErrMsg         string
		expectIPv4Str      string
		expectErr          bool
	}

	cases := []testCase{
		{
			name:       "failed request",
			mockErrMsg: "test error",
			expectErr:  true,
		},
		{
			name:               "non-200 response code",
			mockResponseStatus: http.StatusForbidden,
			expectErr:          true,
		},
		{
			name:               "successful request, no address in body",
			mockResponseStatus: http.StatusOK,
			mockResponseBody:   "this ain't an IP address",
			expectErr:          true,
		},
		{
			name:               "successful request, IPv6 address in body",
			mockResponseStatus: http.StatusOK,
			mockResponseBody:   "::1",
			expectErr:          true,
		},
		{
			name:               "successful request, IPv4 address in body",
			mockResponseStatus: http.StatusOK,
			mockResponseBody:   "192.168.0.1",
			expectIPv4Str:      "192.168.0.1",
			expectErr:          false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(tt *testing.T) {
			assertion := assert.New(tt)
			ctx := context.WithValue(context.Background(), "foo", "bar")

			client := AddressClient{
				Url: "http://foobar:123/test",
				httpClient: mockHttpClient{
					mockDo: func(request *http.Request) (*http.Response, error) {
						assertion.Nil(request.Context().Err(), "context should not be canceled")
						assertion.Equal("bar", request.Context().Value("foo"), "context should be the same as passed in")
						assertion.Equal("GET", request.Method)
						assertion.Equal("http://foobar:123/test", request.URL.String())

						if tc.mockErrMsg != "" {
							return nil, errors.New(tc.mockErrMsg)
						}

						response := &http.Response{
							StatusCode: tc.mockResponseStatus,
							Body:       io.NopCloser(bytes.NewBufferString(tc.mockResponseBody)),
						}
						return response, nil
					},
				},
			}
			ipv4, err := client.GetPublicIPv4(ctx)

			if tc.expectErr {
				assertion.Error(err)

				if tc.mockErrMsg != "" {
					assertion.ErrorContains(err, tc.mockErrMsg)
				}
				return
			}

			assertion.NoError(err)
			assertion.Equal(tc.expectIPv4Str, ipv4.String())
		})
	}
}

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
