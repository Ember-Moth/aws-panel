package httpx

import "github.com/cloudwego/hertz/pkg/app"

const RequestIDKey = "aws_panel_request_id"

type Envelope struct {
	Data      any    `json:"data,omitempty"`
	Error     *Error `json:"error,omitempty"`
	RequestID string `json:"request_id"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func requestID(c *app.RequestContext) string {
	value, _ := c.Get(RequestIDKey)
	id, _ := value.(string)
	return id
}

func Success(c *app.RequestContext, status int, data any) {
	c.JSON(status, Envelope{Data: data, RequestID: requestID(c)})
}

func Failure(c *app.RequestContext, status int, code, message string) {
	c.JSON(status, Envelope{Error: &Error{Code: code, Message: message}, RequestID: requestID(c)})
}
