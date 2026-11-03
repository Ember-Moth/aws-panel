package awsapi

import (
	"context"
	"errors"

	"github.com/Ember-Moth/aws-panel/internal/module/accounts/internal/service"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

type STS struct{}

func (STS) Identity(ctx context.Context, cfg aws.Config) (service.IdentityResult, error) {
	output, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return service.IdentityResult{}, err
	}
	requestID, _ := middleware.GetRequestIDMetadata(output.ResultMetadata)
	return service.IdentityResult{AccountID: aws.ToString(output.Account), ARN: aws.ToString(output.Arn), RequestID: requestID}, nil
}
func (STS) Assume(ctx context.Context, cfg aws.Config, role, name, external string) (aws.Credentials, error) {
	input := &sts.AssumeRoleInput{RoleArn: aws.String(role), RoleSessionName: aws.String(name), DurationSeconds: aws.Int32(3600)}
	if external != "" {
		input.ExternalId = aws.String(external)
	}
	output, err := sts.NewFromConfig(cfg).AssumeRole(ctx, input)
	if err != nil {
		return aws.Credentials{}, err
	}
	value := output.Credentials
	if value == nil || value.Expiration == nil || aws.ToString(value.AccessKeyId) == "" || aws.ToString(value.SecretAccessKey) == "" || aws.ToString(value.SessionToken) == "" {
		return aws.Credentials{}, errors.New("invalid STS response")
	}
	return aws.Credentials{AccessKeyID: aws.ToString(value.AccessKeyId), SecretAccessKey: aws.ToString(value.SecretAccessKey), SessionToken: aws.ToString(value.SessionToken),
		CanExpire: true, Expires: *value.Expiration, Source: "AWS Panel AssumeRole"}, nil
}
