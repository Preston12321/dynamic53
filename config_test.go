package dynamic53

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDaemonConfigValidate(t *testing.T) {
	type testCase struct {
		name      string
		config    DaemonConfig
		expectErr bool
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
			name:      "empty config",
			config:    DaemonConfig{},
			expectErr: true,
		},
		{
			name: "full config, skipUpdate false",
			config: DaemonConfig{
				SkipUpdate: false,
				Polling:    polling,
				Zones:      []ZoneConfig{zone1, zone2},
			},
			expectErr: false,
		},
		{
			name: "full config, skipUpdate true",
			config: DaemonConfig{
				SkipUpdate: true,
				Polling:    polling,
				Zones:      []ZoneConfig{zone1, zone2},
			},
			expectErr: false,
		},
		{
			name: "invalid polling config",
			config: DaemonConfig{
				Polling: PollingConfig{},
				Zones:   []ZoneConfig{zone1, zone2},
			},
			expectErr: true,
		},
		{
			name: "no configured zones",
			config: DaemonConfig{
				Polling: polling,
				Zones:   []ZoneConfig{},
			},
			expectErr: true,
		},
		{
			name: "invalid zone config",
			config: DaemonConfig{
				Polling: polling,
				Zones:   []ZoneConfig{zone1, ZoneConfig{}, zone2},
			},
			expectErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(tt *testing.T) {
			assertion := assert.New(tt)
			err := tc.config.Validate()

			if tc.expectErr {
				assertion.Error(err)
			} else {
				assertion.NoError(err)
			}
		})
	}
}

func TestZoneConfigValidate(t *testing.T) {
	tooManyRecords := make([]string, 301)
	for n := 0; n < 301; n++ {
		tooManyRecords[n] = fmt.Sprintf("%03d.foobar.com", n+1)
	}

	type testCase struct {
		name      string
		config    ZoneConfig
		expectErr bool
	}

	cases := []testCase{
		{
			name:      "empty config",
			config:    ZoneConfig{},
			expectErr: true,
		},
		{
			name: "only records",
			config: ZoneConfig{
				Records: []string{"foobar.com"},
			},
			expectErr: true,
		},
		{
			name: "only name and records",
			config: ZoneConfig{
				Name:    "foobar.com",
				Records: []string{"foobar.com"},
			},
			expectErr: false,
		},
		{
			name: "only id and records",
			config: ZoneConfig{
				Id:      "foobar",
				Records: []string{"foobar.com"},
			},
			expectErr: false,
		},
		{
			name: "full config",
			config: ZoneConfig{
				Name:    "foobar.com",
				Id:      "foobar",
				Records: []string{"foobar.com"},
			},
			expectErr: false,
		},
		{
			name: "full config but empty record",
			config: ZoneConfig{
				Name:    "foobar.com",
				Id:      "foobar",
				Records: []string{"foobar.com", ""},
			},
			expectErr: true,
		},
		{
			name: "full config but too many records",
			config: ZoneConfig{
				Name:    "foobar.com",
				Id:      "foobar",
				Records: tooManyRecords,
			},
			expectErr: true,
		},
		{
			name: "full config, max supported records",
			config: ZoneConfig{
				Name:    "foobar.com",
				Id:      "foobar",
				Records: tooManyRecords[0:300],
			},
			expectErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(tt *testing.T) {
			assertion := assert.New(tt)
			err := tc.config.Validate()

			if tc.expectErr {
				assertion.Error(err)
			} else {
				assertion.NoError(err)
			}
		})
	}
}

func TestPollingConfigValidate(t *testing.T) {
	type testCase struct {
		name      string
		config    PollingConfig
		expectErr bool
	}

	cases := []testCase{
		{
			name:      "empty config",
			config:    PollingConfig{},
			expectErr: true,
		},
		{
			name: "minimal config",
			config: PollingConfig{
				Interval: 60 * time.Second,
			},
			expectErr: false,
		},
		{
			name: "full config",
			config: PollingConfig{
				Interval:  60 * time.Second,
				MaxJitter: 10 * time.Second,
				Url:       "https://foobar.com/ip",
			},
			expectErr: false,
		},
		{
			name: "zero polling interval",
			config: PollingConfig{
				Interval:  0 * time.Second,
				MaxJitter: 10 * time.Second,
				Url:       "https://foobar.com/ip",
			},
			expectErr: true,
		},
		{
			name: "negative polling interval",
			config: PollingConfig{
				Interval:  -1 * time.Second,
				MaxJitter: 10 * time.Second,
				Url:       "https://foobar.com/ip",
			},
			expectErr: true,
		},
		{
			name: "negative maxJitter",
			config: PollingConfig{
				Interval:  60 * time.Second,
				MaxJitter: -1 * time.Second,
				Url:       "https://foobar.com/ip",
			},
			expectErr: true,
		},
		{
			name: "maxJitter equals interval",
			config: PollingConfig{
				Interval:  60 * time.Second,
				MaxJitter: 60 * time.Second,
				Url:       "https://foobar.com/ip",
			},
			expectErr: true,
		},
		{
			name: "maxJitter greater than interval",
			config: PollingConfig{
				Interval:  60 * time.Second,
				MaxJitter: 120 * time.Second,
				Url:       "https://foobar.com/ip",
			},
			expectErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(tt *testing.T) {
			assertion := assert.New(tt)
			err := tc.config.Validate()

			if tc.expectErr {
				assertion.Error(err)
			} else {
				assertion.NoError(err)
			}
		})
	}
}

const TestDaemonConfigYaml string = `
polling:
  interval: 5m
zones:
  - id: foobar
    records:
      - foobar.com
`

func TestLoadDaemonConfig(t *testing.T) {
	type testCase struct {
		name         string
		yaml         string
		expectConfig *DaemonConfig
		expectErr    bool
	}

	cases := []testCase{
		{
			name:         "invalid yaml",
			yaml:         "this ain't valid yaml",
			expectConfig: nil,
			expectErr:    true,
		},
		{
			name:         "empty config",
			yaml:         "",
			expectConfig: nil,
			expectErr:    true,
		},
		{
			name:         "valid yaml, invalid config",
			yaml:         "technicallyValid: true",
			expectConfig: nil,
			expectErr:    true,
		},
		{
			name: "valid yaml and minimal config",
			yaml: TestDaemonConfigYaml,
			expectConfig: &DaemonConfig{
				Polling: PollingConfig{
					Interval: 5 * time.Minute,
				},
				Zones: []ZoneConfig{
					{
						Id:      "foobar",
						Records: []string{"foobar.com"},
					},
				},
			},
			expectErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(tt *testing.T) {
			assertion := assert.New(tt)
			config, err := LoadDaemonConfig(strings.NewReader(tc.yaml))

			if tc.expectErr {
				assertion.Error(err)
				return
			}

			assertion.NotNil(config)
			assertion.Equal(tc.expectConfig, config)
		})
	}
}
