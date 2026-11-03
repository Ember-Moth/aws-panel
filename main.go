package main

import (
	"os"
	_ "time/tzdata"

	"github.com/Ember-Moth/aws-panel/cmd"
)

// @title AWS Panel API
// @version 0.1.0
// @description AWS Panel foundation API. Cloud operations are not yet exposed.
// @BasePath /api/v1
// @schemes http https
// @securityDefinitions.apikey SessionCookie
// @in header
// @name Cookie
// @description Send the HttpOnly session cookie issued by login.
// @securityDefinitions.apikey CSRFToken
// @in header
// @name X-CSRF-Token
// @description Send the session-bound CSRF token for authenticated writes.
func main() {
	os.Exit(cmd.Execute())
}
