package awsapi

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	lt "github.com/aws/aws-sdk-go-v2/service/lightsail/types"
)

func TestRegionAvailable(t *testing.T) {
	available := []lt.AvailabilityZone{{ZoneName: aws.String("us-east-1a"), State: aws.String("available")}}
	impaired := []lt.AvailabilityZone{{ZoneName: aws.String("us-east-1a"), State: aws.String("impaired")}}
	cases := []struct {
		name  string
		state string
		zones []lt.AvailabilityZone
		want  bool
	}{
		{"zones of the queried region", "ENABLED_BY_DEFAULT", available, true},
		{"other regions arrive without zones", "ENABLED_BY_DEFAULT", nil, true},
		{"opted-in region without zones", "ENABLED", nil, true},
		{"listed zones are all unavailable", "ENABLED", impaired, false},
		{"region not enabled", "DISABLED", available, false},
		{"region still enabling", "ENABLING", nil, false},
		{"opt-in state unknown", "UNKNOWN", nil, false},
	}
	for _, test := range cases {
		if got := regionAvailable(test.state, test.zones); got != test.want {
			t.Errorf("%s: regionAvailable = %v, want %v", test.name, got, test.want)
		}
	}
}
