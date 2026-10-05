package main

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"slices"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mandloideep/reclaim/internal/scanners"
)

// version is set at build time with -ldflags "-X main.version=v1.2.3". When it
// is empty the module version from the build info is used.
var version string

func newRootCmd(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:   "reclaim",
		Short: "Find reclaimable disk space and remove only what you select",
		Long: "reclaim finds build artifacts, dependency folders, package manager caches, Docker leftovers,\n" +
			"app caches and forgotten downloads, shows them with sizes and removes only what you select\n" +
			"and confirm. Settings live in ~/.config/reclaim/config.toml.\n\n" +
			"  reclaim scan              report what can be reclaimed, read only\n" +
			"  reclaim here              what takes space in this folder, and what can go\n" +
			"  reclaim select            pick findings from the last report and write a plan\n" +
			"  reclaim apply plan.json   remove exactly what the plan lists, after confirmation",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetIn(a.stdin)
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	root.PersistentFlags().BoolVarP(&a.verbose, "verbose", "v", false, "print debug logs to stderr")
	root.PersistentFlags().StringVar(&a.configPath, "config", a.configPath, "configuration file to read; a missing file means defaults")
	root.AddCommand(
		newScanCmd(a),
		newHereCmd(a),
		newSelectCmd(a),
		newApplyCmd(a),
		newScannersCmd(a),
		newVersionCmd(a),
	)
	return root
}

func newScannersCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "scanners",
		Short: "List the registered scanners and what they look for",
		Args:  cobra.NoArgs,
		Long: "scanners lists every registered scanner with its category, its ecosystem and what it looks for.\n" +
			"Scanners turned off by [scanners] disable in the config file are marked disabled.",
		RunE: func(*cobra.Command, []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			all := scanners.New().All()
			enabled, err := enabledScanners(all, nil, cfg.Disable)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "NAME\tSTATUS\tCATEGORY\tECOSYSTEM\tLOOKS FOR")
			for _, s := range all {
				status := "disabled"
				if slices.Contains(enabled, s) {
					status = "enabled"
				}
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.Name(), status, s.Category(), s.Ecosystem(), s.Description())
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			if cfg.Path != "" && len(cfg.Disable) > 0 {
				a.sayf("\nDisabled in %s.\n", cfg.Path)
			}
			return nil
		},
	}
}

func newVersionCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			_, err := fmt.Fprintf(a.stdout, "reclaim %s %s %s/%s\n", buildVersion(), runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return err
		},
	}
}

func buildVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}
