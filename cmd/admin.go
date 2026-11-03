package cmd

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/herta-network/aws-panel/internal/app"
	"github.com/herta-network/aws-panel/internal/config"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func adminCommand(path *string) *cobra.Command {
	root := &cobra.Command{Use: "admin", Short: "Local administrator bootstrap"}
	var username, name string
	var stdin bool
	initCommand := &cobra.Command{
		Use: "init", Short: "Create the first administrator once", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			cfg, err := config.Load(*path)
			if err != nil {
				return err
			}
			if name == "" {
				name = username
			}
			password, err := readAdminPassword(command.InOrStdin(), command.ErrOrStderr(), stdin)
			if err != nil {
				return err
			}
			member, err := app.InitializeAdmin(command.Context(), cfg, username, name, password)
			if err != nil {
				return err
			}
			fmt.Fprintf(command.OutOrStdout(), "Created administrator %s (id=%d).\n", member.Username, member.ID)
			return nil
		},
	}
	initCommand.Flags().StringVar(&username, "username", "", "Administrator username")
	initCommand.Flags().StringVar(&name, "display-name", "", "Administrator display name")
	initCommand.Flags().BoolVar(&stdin, "password-stdin", false, "Read a password from non-interactive stdin instead of prompting")
	_ = initCommand.MarkFlagRequired("username")
	root.AddCommand(initCommand)
	return root
}

func readAdminPassword(input io.Reader, output io.Writer, stdin bool) (string, error) {
	file, isFile := input.(*os.File)
	interactive := isFile && term.IsTerminal(int(file.Fd()))
	if stdin {
		if interactive {
			return "", errors.New("omit --password-stdin to use a hidden terminal prompt")
		}
		data, err := io.ReadAll(io.LimitReader(input, 513))
		if err != nil || len(data) > 512 {
			return "", errors.New("cannot read password from stdin")
		}
		value := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
		if strings.ContainsAny(value, "\r\n") {
			return "", errors.New("password stdin must contain one line")
		}
		return value, nil
	}
	if !interactive {
		return "", errors.New("use an interactive terminal or explicitly pass --password-stdin")
	}
	fmt.Fprint(output, "Administrator password (15–128 characters): ")
	first, err := term.ReadPassword(int(file.Fd()))
	fmt.Fprintln(output)
	if err != nil {
		return "", errors.New("cannot read password")
	}
	fmt.Fprint(output, "Repeat password: ")
	second, err := term.ReadPassword(int(file.Fd()))
	fmt.Fprintln(output)
	if err != nil {
		return "", errors.New("cannot read password")
	}
	if subtle.ConstantTimeCompare(first, second) != 1 {
		return "", errors.New("passwords do not match")
	}
	return string(first), nil
}
