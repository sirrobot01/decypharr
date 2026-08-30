package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sirrobot01/appendstore"
	"github.com/sirrobot01/decypharr/internal/logger"
)

const (
	// downgradeWorkSuffix names the converted copy while the run is in
	// progress. It is replaced on every run, so an abandoned one cannot block a
	// retry.
	downgradeWorkSuffix = ".downgrade"

	// downgradeBackupSuffix names the current database, kept aside once the
	// older copy takes its place. It is version-agnostic on purpose: it is
	// simply whatever this build was reading before the downgrade.
	downgradeBackupSuffix = ".pre-downgrade.bak"
)

// Downgrade rewrites every database under dbPath in an older log format, so a
// Decypharr release that predates the current format can read them. Version 3
// is what releases up to v2.4 read.
//
// Unlike restoring a migration backup, this carries the current contents: no
// work is lost. The databases this build was reading are kept beside the new
// ones with a .pre-downgrade.bak suffix, so the downgrade itself is reversible.
//
// Every database is converted before any is replaced. A database holding data
// the older format cannot express stops the run with nothing changed.
//
// A .pre-downgrade.bak left by an earlier run stops this one, because replacing
// it would discard the only copy of what that run set aside. Pass replaceBackup
// to overwrite it, which is what a second trip back and forth needs: by then the
// earlier copy has been superseded by the databases in use since.
//
// Decypharr must not be running: each database is locked by the process that
// has it open, and Downgrade reports that rather than waiting.
func Downgrade(dbPath string, version uint32, replaceBackup bool) error {
	dbPath = filepath.Clean(dbPath)
	log := logger.New("downgrade")

	type conversion struct{ source, converted, backup string }
	var pending []conversion

	// Convert everything first. The risky step is the conversion, so nothing is
	// moved until every database has survived it.
	for _, name := range storeNames {
		source := filepath.Join(dbPath, name+".db")
		if _, err := os.Stat(source); err != nil {
			if os.IsNotExist(err) {
				continue // never created; nothing to downgrade
			}
			return err
		}
		c := conversion{
			source:    source,
			converted: source + downgradeWorkSuffix,
			backup:    source + downgradeBackupSuffix,
		}
		if _, err := os.Stat(c.backup); err == nil {
			if !replaceBackup {
				return fmt.Errorf("%s already exists: an earlier downgrade left it behind. "+
					"Move it aside, or pass -replace-backup to overwrite it", c.backup)
			}
			if err := os.Remove(c.backup); err != nil {
				return fmt.Errorf("replace %s: %w", c.backup, err)
			}
		} else if !os.IsNotExist(err) {
			return err
		}

		options := appendstore.DowngradeOptions{Version: version, Overwrite: true}
		if err := appendstore.Downgrade(c.source, c.converted, options); err != nil {
			for _, done := range pending {
				_ = os.Remove(done.converted)
			}
			if errors.Is(err, appendstore.ErrStoreLocked) {
				return fmt.Errorf("the %s database is in use: stop Decypharr and run this again", name)
			}
			return fmt.Errorf("convert the %s database to version %d: %w", name, version, err)
		}
		log.Info().Str("database", name).Msg("Converted")
		pending = append(pending, c)
	}

	if len(pending) == 0 {
		return fmt.Errorf("no databases found in %s", dbPath)
	}

	// Swap them in. Each database ends up either untouched or converted, and the
	// previous copy is always kept.
	for _, c := range pending {
		if err := os.Rename(c.source, c.backup); err != nil {
			return fmt.Errorf("set %s aside: %w", c.source, err)
		}
		if err := os.Rename(c.converted, c.source); err != nil {
			// Put the original back so the database is never missing.
			if restoreErr := os.Rename(c.backup, c.source); restoreErr != nil {
				return errors.Join(err, fmt.Errorf("restore %s: %w", c.source, restoreErr))
			}
			return fmt.Errorf("install the converted %s: %w", c.source, err)
		}
	}

	log.Info().
		Int("databases", len(pending)).
		Uint32("version", version).
		Str("dir", dbPath).
		Msg("Downgrade complete. The previous databases are kept as .pre-downgrade.bak")
	return nil
}
