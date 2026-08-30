package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

const downgradeCommand = "downgrade-db"

// resolveConfigPath returns the data folder to use, defaulting to ~/.decypharr
// and falling back to the working directory when there is no home directory.
func resolveConfigPath(configPath string) string {
	if configPath != "" {
		return configPath
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".decypharr")
}

// runDowngrade rewrites the databases in the data folder in an older log
// format, so a Decypharr release that predates the current format can read
// them. It exists for the move to the current format and goes away with it.
func runDowngrade(args []string) error {
	flags := flag.NewFlagSet(downgradeCommand, flag.ContinueOnError)
	configPath := flags.String("config", "", "path to the data folder")
	version := flags.Uint("to", 3, "target database format (3 for releases up to v2.4)")
	replaceBackup := flags.Bool("replace-backup", false, "overwrite a .pre-downgrade.bak left by an earlier run")
	flags.Usage = func() {
		out := flags.Output()
		fmt.Fprintf(out, "usage: %s %s [-config dir] [-to version]\n\n", filepath.Base(os.Args[0]), downgradeCommand)
		fmt.Fprint(out, "Rewrites the databases so an older Decypharr release can read them.\n"+
			"Stop Decypharr first. The current databases are kept as .pre-downgrade.bak,\n"+
			"and no data is lost in either direction.\n\n")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil // -h printed the usage; that is not a failure
		}
		return err
	}

	config.SetConfigPath(resolveConfigPath(*configPath))
	config.Get()

	dbPath := filepath.Join(config.GetMainPath(), "db")
	if err := storage.Downgrade(dbPath, uint32(*version), *replaceBackup); err != nil {
		return err
	}
	fmt.Printf("The databases in %s are now format version %d. Start the older Decypharr release.\n", dbPath, *version)
	return nil
}
