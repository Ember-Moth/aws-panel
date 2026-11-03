package aws

import (
	"errors"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// Transient identifies recoverable transport/service failures, not permission
// to repeat a cloud write. Write executors must reconcile ambiguous outcomes.
type Failure struct {
	Code, RequestID string
	Transient       bool
}

func ClassifyError(err error) Failure {
	result := Failure{Code: "NETWORK_UNAVAILABLE", Transient: true}
	var response *awshttp.ResponseError
	if errors.As(err, &response) {
		id := response.ServiceRequestID()
		if len(id) <= 256 {
			result.RequestID = id
		}
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		result.Transient = false
		switch api.ErrorCode() {
		case "AccessDenied", "AccessDeniedException", "UnauthorizedOperation", "UnauthenticatedException", "InvalidClientTokenId", "SignatureDoesNotMatch", "ExpiredToken", "ExpiredTokenException":
			result.Code = "AWS_ACCESS_DENIED"
		case "RegionSetupInProgressException", "OptInRequired", "AccountSetupInProgressException":
			result.Code = "AWS_REGION_UNAVAILABLE"
		case "InvalidInputException", "ValidationException", "NotFoundException":
			result.Code = "AWS_INVALID_REQUEST"
		case "Throttling", "ThrottlingException", "TooManyRequestsException", "RequestLimitExceeded":
			result.Code = "AWS_THROTTLED"
			result.Transient = true
		default:
			result.Code = "AWS_ERROR"
			if api.ErrorFault() == smithy.FaultServer {
				result.Code = "AWS_SERVICE_UNAVAILABLE"
				result.Transient = true
			}
		}
		return result
	}
	var httpError *smithyhttp.ResponseError
	if errors.As(err, &httpError) {
		code := httpError.HTTPStatusCode()
		if code == 401 || code == 403 {
			result.Code = "AWS_ACCESS_DENIED"
			result.Transient = false
		} else if code == 429 {
			result.Code = "AWS_THROTTLED"
		} else if code >= 500 {
			result.Code = "AWS_SERVICE_UNAVAILABLE"
		}
	}
	return result
}
