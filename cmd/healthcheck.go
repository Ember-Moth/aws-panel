package cmd

import (
	"errors"
	"net/http"
	"time"

	"github.com/spf13/cobra"
)

func healthcheckCommand() *cobra.Command {
	var address string
	command := &cobra.Command{
		Use: "healthcheck", Short: "Check local application readiness", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
			defer client.CloseIdleConnections()
			request, err := http.NewRequestWithContext(command.Context(), http.MethodGet, address+"/health/ready", nil)
			if err != nil {
				return errors.New("invalid healthcheck address")
			}
			response, err := client.Do(request)
			if err != nil {
				return errors.New("application is unavailable")
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				return errors.New("application is not ready")
			}
			return nil
		},
	}
	command.Flags().StringVar(&address, "address", "http://127.0.0.1:8080", "local HTTP origin")
	return command
}
