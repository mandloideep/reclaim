package main

import (
	"fmt"
	"runtime"
	"runtime/debug"
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
		Long: "reclaim finds build artifacts, dependency folders, package manager caches and Docker leftovers,\n" +
			"shows them with sizes and removes only what you select and confirm.\n\n" +
			"  reclaim scan              report what can be reclaimed, read only\n" +
			"  reclaim here              what takes space in this folder, and what can go\n" +
			"  reclaim select --preset   write a plan from the last report\n" +
			"  reclaim apply plan.json   remove exactly what the plan lists, after confirmation",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetIn(a.stdin)
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	root.PersistentFlags().BoolVarP(&a.verbose, "verbose", "v", false, "print debug logs to stderr")
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
		RunE: func(*cobra.Command, []string) error {
			tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "NAME\tCATEGORY\tECOSYSTEM\tLOOKS FOR")
			for _, s := range scanners.New().All() {
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.Name(), s.Category(), s.Ecosystem(), s.Description())
			}
			return tw.Flush()
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
