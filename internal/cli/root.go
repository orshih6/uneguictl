// Package cli implements uneguictl's command-line interface.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/orshih6/uneguictl/internal/output"
	"github.com/orshih6/uneguictl/internal/unegui"
)

const interruptExitCode = 130

// Exit statuses besides 0, 1 and interruptExitCode, for scripts and
// assistants that act on the outcome rather than parse the message.
const (
	// exitBlocked: Cloudflare refused the request.
	exitBlocked = 3
	// exitNotFound: the advert or category does not exist.
	exitNotFound = 4
)

const exitStatusHelp = `Exit status:
  0    success (also a search that found nothing)
  1    any other error
  3    unegui.mn's Cloudflare refused the request; wait and try again
  4    the advert or category does not exist (or the advert was removed)
  130  interrupted`

var flagJSON bool

// client returns a client for unegui.mn.
func client() (*unegui.Client, error) {
	return unegui.New()
}

// emit prints a command's result: the data as JSON under --json, otherwise
// whatever text does.
func emit(cmd *cobra.Command, resource any, text func(w io.Writer) error) error {
	if flagJSON {
		return output.JSON(cmd.OutOrStdout(), resource)
	}
	return text(cmd.OutOrStdout())
}

// version is set with -ldflags "-X ...cli.version=v1.2.3" for releases.
var version string

func buildVersion() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			if len(s.Value) > 12 {
				return s.Value[:12]
			}
			return s.Value
		}
	}
	return "dev"
}

func newRootCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uneguictl",
		Short: "Search unegui.mn listings from the command line",
		Long: "Search unegui.mn listings from the command line.\n\n" +
			"uneguictl reads the same public JSON the unegui.mn website loads, with no\n" +
			"sign-in. It only reads: it never posts, messages or changes anything.\n\n" +
			"Results go to standard output; counts, notes and errors to standard error.\n\n" +
			exitStatusHelp,
		Version:       buildVersion(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.PersistentFlags().BoolVar(&flagJSON, "json", false, "print the result as JSON")

	cmd.AddCommand(
		newSearchCommand(),
		newShowCommand(),
		newCategoriesCommand(),
		newFiltersCommand(),
		newWatchCommand(),
	)
	return cmd
}

func exitCode(err error, interrupted bool) int {
	switch {
	case errors.Is(err, unegui.ErrBlocked):
		return exitBlocked
	case errors.Is(err, unegui.ErrNotFound), errors.Is(err, unegui.ErrNoCategory):
		return exitNotFound
	case interrupted:
		return interruptExitCode
	}
	return 1
}

// Execute runs uneguictl, exiting with the status exitCode gives.
func Execute() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := newRootCommand().ExecuteContext(ctx)
	if err == nil {
		return
	}
	interrupted := ctx.Err() != nil
	if !interrupted || !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "uneguictl:", err)
	}
	if interrupted {
		fmt.Fprintln(os.Stderr, "interrupted")
	}
	os.Exit(exitCode(err, interrupted))
}
