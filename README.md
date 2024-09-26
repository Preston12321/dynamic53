# dynamic53 - Dynamic DNS for AWS Route 53

[![Go Reference](https://pkg.go.dev/badge/github.com/Preston12321/dynamic53.svg)](https://pkg.go.dev/github.com/Preston12321/dynamic53)

A DNS update client that runs as a daemon and outputs structured logs. Comes
with a tool to generate a restrictive identity-based IAM policy based on its
configuration.

## Installation

Install the dynamic53 IAM policy generator:

```
go install github.com/Preston12321/dynamic53/cmd/d53policy@latest
```

Install the dynamic53 daemon:

```
go install github.com/Preston12321/dynamic53/cmd/dynamic53@latest
```

## Configuration

The config file format looks like the below example:

```yaml
# Tells dynamic53 to skip all API calls to AWS and simply print a log line
# instead. Only really useful for development testing
skipUpdate: true # Optional, defaults to false

# Configures the frequency at which dynamic53 polls for changes to its public
# IPv4 address and sends those updates to Route 53. You can disable jitter by
# setting it to 0s, but that's bad manners, so you probably shouldn't
polling:
  interval: 5m
  maxJitter: 1s

zones:
    # The name field is optional when id is set, but will be sanity checked
    # against the name returned by AWS before making changes to the zone
  - name: example.com
    id: Z0123456789ABCDEFGHIJ
    # You can list up to 1000 records to manage per zone. Any A records that
    # don't exist will be created. The TTL on each record is set to the polling
    # interval configured above
    records:
      - example.com
      - test.example.com
    # Zones can be identified by either their ID or their name, but it's
    # recommended to always use the ID. If a zone is missing an ID, the
    # d53policy tool can't generate an IAM policy for it
  - id: Z012345GHIJ6789ABCDEF
    records:
      - foo.example.net
```

## How to use

Just pass the path to a config file and you're off to the races!

Generate a strictly-scoped identity-based IAM policy to grant an AWS user or
role permissions to manage the DNS zones and records specified in the given
config:

```bash
>> d53policy -config my-config.yaml > policy.json
>> cat policy.json
{
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
                "route53:GetHostedZone"
            ],
            "Resource": [
                "arn:aws:route53:::hostedzone/Z0123456789ABCDEFGHIJ",
                "arn:aws:route53:::hostedzone/Z012345GHIJ6789ABCDEF",
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
                "arn:aws:route53:::hostedzone/Z0123456789ABCDEFGHIJ"
            ],
            "Condition": {
                "ForAllValues:StringEquals": {
                    "route53:ChangeResourceRecordSetsNormalizedRecordNames": [
                        "example.com",
                        "test.example.com"
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
        {
            "Sid": "AllowEditingRecords1",
            "Effect": "Allow",
            "Action": [
                "route53:ChangeResourceRecordSets"
            ],
            "Resource": [
                "arn:aws:route53:::hostedzone/Z012345GHIJ6789ABCDEF"
            ],
            "Condition": {
                "ForAllValues:StringEquals": {
                    "route53:ChangeResourceRecordSetsNormalizedRecordNames": [
                        "foo.example.net"
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
}
```

Attach the policy generated by d53policy to the AWS user or role that will be
used by the dynamic53 daemon.

Start the dynamic53 daemon:

```bash
>> dynamic53 -config my-config.yaml
{"level":"info","pollingInterval":"5m0s","time":"2024-09-24T22:37:40-05:00","message":"Starting dynamic53 daemon"}
{"level":"info","ipv4":"52.94.76.112","time":"2024-09-24T22:37:41-05:00","message":"Retrieved current public address"}
{"level":"info","ipv4":"52.94.76.112","zoneId":"Z012345GHIJ6789ABCDEF","zoneName":"","time":"2024-09-24T22:37:41-05:00","message":"Skipping hosted zone update"}
{"level":"info","ipv4":"52.94.76.112","zoneId":"Z0123456789ABCDEFGHIJ","zoneName":"example.com","time":"2024-09-24T22:37:41-05:00","message":"Skipping hosted zone update"}
...
```

## Using as a library

The functionality of this module is available as a Go library:

```
go get github.com/Preston12321/dynamic53
```
