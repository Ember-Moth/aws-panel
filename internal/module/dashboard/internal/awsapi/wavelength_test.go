package awsapi

import (
	"context"
	"github.com/Ember-Moth/aws-panel/internal/module/dashboard/contract"
	"github.com/aws/aws-sdk-go-v2/aws"
	"io"
	"net/http"
	"strings"
	"testing"
)

type carrierHTTP struct{}

func (carrierHTTP) Do(r *http.Request) (*http.Response, error) {
	body := `<DescribeAddressesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><addressesSet><item><allocationId>eipalloc-11111111</allocationId><publicIp>127.0.3.1</publicIp><domain>vpc</domain></item><item><allocationId>eipalloc-22222222</allocationId><carrierIp>127.0.3.2</carrierIp><domain>vpc</domain></item></addressesSet></DescribeAddressesResponse>`
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/xml"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
}
func TestCarrierMetricSeparateFromPublicIPv4(t *testing.T) {
	cfg := aws.Config{Region: "us-east-1", Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, nil
	}), HTTPClient: carrierHTTP{}}
	data, err := (Cloud{}).resources(context.Background(), cfg, "111111111111", contract.EC2IPs)
	if err != nil || data.Count != 1 || data.CarrierCount != 1 || !data.CarrierKnown || len(data.IPv4) != 1 || data.IPv4[0] != "127.0.3.1" {
		t.Fatal(data, err)
	}
}
