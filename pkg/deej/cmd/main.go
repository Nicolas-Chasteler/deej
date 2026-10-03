package main

import (
	"flag"
	"fmt"
	"runtime/debug"

	"github.com/omriharel/deej/pkg/deej"
)

var (
	gitCommit  string
	versionTag string
	buildType  string

	verbose bool
)

func init() {
	flag.BoolVar(&verbose, "verbose", false, "show verbose logs (useful for debugging serial)")
	flag.BoolVar(&verbose, "v", false, "shorthand for --verbose")
	flag.Parse()
}

func main() {

	// a plain "go build" doesn't run the build scripts, so nothing set these.
	// the Go toolchain stamps the commit into the binary regardless
	if gitCommit == "" {
		gitCommit = commitFromBuildInfo()
	}

	// first we need a logger
	logger, err := deej.NewLogger(buildType, verbose)
	if err != nil {
		panic(fmt.Sprintf("Failed to create logger: %v", err))
	}

	named := logger.Named("main")
	named.Debug("Created logger")

	named.Infow("Version info",
		"gitCommit", gitCommit,
		"versionTag", versionTag,
		"buildType", buildType)

	// provide a fair warning if the user's running in verbose mode
	if verbose {
		named.Debug("Verbose flag provided, all log messages will be shown")
	}

	// create the deej instance
	d, err := deej.NewDeej(logger, verbose)
	if err != nil {
		named.Fatalw("Failed to create deej object", "error", err)
	}

	// if injected by build process, set version info to show up in the tray
	if buildType != "" && (versionTag != "" || gitCommit != "") {
		identifier := gitCommit
		if versionTag != "" {
			identifier = versionTag
		}

		versionString := fmt.Sprintf("Version %s-%s", buildType, identifier)
		d.SetVersion(versionString)
	}

	// onwards, to glory
	if err = d.Initialize(); err != nil {
		named.Fatalw("Failed to initialize deej", "error", err)
	}
}

// commitFromBuildInfo returns the short commit the binary was built from, with
// "-dirty" if the tree had uncommitted changes, or "" if it wasn't recorded
func commitFromBuildInfo() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}

	var revision string
	var modified bool

	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}

	if len(revision) > 7 {
		revision = revision[:7]
	}

	if revision != "" && modified {
		revision += "-dirty"
	}

	return revision
}
