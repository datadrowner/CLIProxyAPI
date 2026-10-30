package util

import (
	"errors"
	"os"

	log "github.com/sirupsen/logrus"
)

// MigrateStateFile renames legacyPath to path when only the legacy file exists.
// State files used to be stored as *.json in the auth directory, where every
// *.json file is loaded as a credential; they now use a different extension.
func MigrateStateFile(legacyPath, path string) {
	if legacyPath == "" || legacyPath == path {
		return
	}
	if _, errStat := os.Stat(path); !errors.Is(errStat, os.ErrNotExist) {
		return
	}
	if _, errStat := os.Stat(legacyPath); errStat != nil {
		return
	}
	if errRename := os.Rename(legacyPath, path); errRename != nil {
		log.Warnf("failed to move state file %s to %s: %v", legacyPath, path, errRename)
		return
	}
	log.Infof("moved state file %s to %s so it is no longer loaded as a credential", legacyPath, path)
}
