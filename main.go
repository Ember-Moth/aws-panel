package main

import (
	"os"
	_ "time/tzdata"

	"github.com/herta-network/aws-panel/cmd"
)

// @title AWS Panel API
// @version 0.1.0
// @description AWS Panel foundation API. Cloud operations are not yet exposed.
// @BasePath /api/v1
// @schemes http https
func main() {
	os.Exit(cmd.Execute())
}
