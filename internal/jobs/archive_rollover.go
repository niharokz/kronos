package jobs

import (
	"os"
	"path/filepath"
	"strconv"

	"gitlab.com/niharokz/kronos/internal/job"
	"gitlab.com/niharokz/kronos/internal/store"
)

// archive_rollover moves loose files in the archive into a folder named
// after the previous year (archive/2025/). Scheduled for 1 January.
//
// This is the simplest job — a good template for a new one.

func init() { job.Register("archive_rollover", archiveRollover) }

func archiveRollover(c *job.Ctx) error {
	archive := c.Path("archive") // kronos.yml: paths.archive
	if !isDir(archive) {
		return missingDir(archive)
	}

	year := strconv.Itoa(c.Now.Year() - 1)
	target := filepath.Join(archive, year)
	if err := c.MkdirAll(target); err != nil {
		return err
	}

	entries, err := os.ReadDir(archive)
	if err != nil {
		return err
	}
	count := 0
	for _, entry := range entries {
		path := filepath.Join(archive, entry.Name())
		if !isFile(path) { // skip year folders
			continue
		}
		if err := c.Move(path, store.UniquePath(target, entry.Name())); err != nil {
			return err
		}
		count++
	}

	if count > 0 {
		c.Log.OK("Moved %d file(s) into %s", count, year)
	} else {
		c.Log.Skip("No files to roll over into %s", year)
	}
	return nil
}
