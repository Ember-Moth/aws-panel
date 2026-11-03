package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/herta-network/aws-panel/internal/app"
	"github.com/herta-network/aws-panel/internal/auth/vault"
	"github.com/herta-network/aws-panel/internal/config"
	"github.com/spf13/cobra"
)

func vaultCommand(path *string) *cobra.Command {
	root := &cobra.Command{Use: "vault", Short: "Manage the external credential keyring"}
	var output string
	initialize := &cobra.Command{Use: "init", Short: "Create a private keyring without overwriting a file", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		cfg, err := config.Load(*path)
		if err != nil {
			return err
		}
		if output == "" {
			output = cfg.Secrets.KeyringFile
		}
		if output == "" {
			return errors.New("set --out or secrets.keyring_file")
		}
		if err := os.MkdirAll(filepath.Dir(output), 0700); err != nil {
			return errors.New("cannot create keyring directory")
		}
		ring, err := vault.Generate()
		if err != nil {
			return errors.New("cannot generate keyring")
		}
		if err := ring.Write(output, true); err != nil {
			return err
		}
		fmt.Fprintf(command.OutOrStdout(), "Keyring initialized; active key ID: %s. Back up the private file separately from the database.\n", ring.Active)
		return nil
	}}
	initialize.Flags().StringVar(&output, "out", "", "Private keyring file")
	root.AddCommand(initialize)
	for _, operation := range []string{"status", "rotate", "reencrypt", "retire"} {
		op := operation
		var keyID string
		child := &cobra.Command{Use: op, Short: "Vault " + op + " using the database lock", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
			cfg, err := config.Load(*path)
			if err != nil {
				return err
			}
			result, err := app.MaintainVault(command.Context(), cfg, op, keyID)
			if err != nil {
				return err
			}
			fmt.Fprintf(command.OutOrStdout(), "active=%s reencrypted=%d\n", result.ActiveKey, result.Reencrypted)
			keys := make([]string, 0, len(result.KeyUsage))
			for key := range result.KeyUsage {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				fmt.Fprintf(command.OutOrStdout(), "%s: %d records\n", key, result.KeyUsage[key])
			}
			return nil
		}}
		if op == "retire" {
			child.Flags().StringVar(&keyID, "key-id", "", "Unused key ID to remove")
			_ = child.MarkFlagRequired("key-id")
		}
		root.AddCommand(child)
	}
	return root
}
