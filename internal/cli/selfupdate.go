package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/hajime-ch/deploy-senpai/internal/selfupdate"
)

// Repo is where self-update looks for releases.
const Repo = "hajime-ch/deploy-senpai"

// NewSelfUpdateCmd creates the `self-update` subcommand. version is the running
// build's version, stamped at release time.
func NewSelfUpdateCmd(version string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "self-update",
		Short: "Replace this binary with a release from GitHub",
		Long: "Replace this binary with a release from GitHub.\n\n" +
			"The running server keeps using the old binary until it is restarted, " +
			"which this command deliberately does not do for you.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			checkOnly, _ := cmd.Flags().GetBool("check")
			wantTag, _ := cmd.Flags().GetString("version")
			force, _ := cmd.Flags().GetBool("force")

			updater, err := selfupdate.New(Repo, version)
			if err != nil {
				return err
			}
			updater.Force = force

			// --check reports without touching anything, so it must not be
			// blocked by the reasons an install would be.
			if !checkOnly {
				if err := updater.Preflight(); err != nil {
					return err
				}
			}

			update, err := updater.Check(cmd.Context(), wantTag)
			if err != nil {
				return err
			}

			if checkOnly {
				if IsJSON(cmd) {
					return PrintJSON(map[string]string{
						"installed": version,
						"available": update.Version(),
						"asset":     update.AssetName,
					})
				}
				fmt.Printf("installed: %s\n", version)
				fmt.Printf("available: %s (%s)\n", update.Version(), update.AssetName)
				return nil
			}

			if update.Version() == version {
				fmt.Printf("Already on %s — nothing to do.\n", version)
				return nil
			}

			fmt.Printf("Downloading %s (%s)...\n", update.Tag, update.AssetName)
			installed, err := updater.Apply(cmd.Context(), update)
			if err != nil {
				return err
			}

			fmt.Printf("Updated: %s -> %s\n", version, installed)
			fmt.Fprintf(os.Stderr, "The previous binary is at %s.old\n", updater.ExecPath)
			fmt.Fprintln(os.Stderr, "A running server keeps the old build until it is restarted "+
				"(e.g. sudo systemctl restart deploy-senpai).")
			return nil
		},
	}
	cmd.Flags().Bool("check", false, "Report the available release without installing it")
	cmd.Flags().String("version", "", "Install a specific release tag (e.g. v0.4.0)")
	cmd.Flags().Bool("force", false, "Replace a dev build")
	return cmd
}
