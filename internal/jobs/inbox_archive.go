package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"gitlab.com/niharokz/kronos/internal/job"
	"gitlab.com/niharokz/kronos/internal/store"
)

// inbox_archive moves inbox files not modified for archive_after_days
// (default 30) into the archive, never overwriting an archived file of the
// same name (todo.md → todo_1.md). Files listed in exclude (default
// note.md, guide.md) and dotfiles stay.

func init() { job.Register("inbox_archive", inboxArchive) }

func inboxArchive(c *job.Ctx) error {
	inbox, archive := c.Path("inbox"), c.Path("archive") // kronos.yml: paths.inbox, paths.archive
	if !isDir(inbox) {
		return missingDir(inbox)
	}
	if err := c.MkdirAll(archive); err != nil {
		return err
	}

	excluded := map[string]bool{}
	// kronos.yml: jobs.inbox_archive.options.exclude
	for _, name := range c.Strings("exclude", []string{"note.md", "guide.md"}) {
		excluded[name] = true
	}
	cutoff := c.Now.Add(-time.Duration(c.Int("archive_after_days", 30)) * 24 * time.Hour) // kronos.yml: jobs.inbox_archive.options.archive_after_days

	entries, err := os.ReadDir(inbox)
	if err != nil {
		return err
	}
	count := 0
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(inbox, name)
		if excluded[name] || strings.HasPrefix(name, ".") || !isFile(path) {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if err := c.Move(path, store.UniquePath(archive, name)); err != nil {
			return err
		}
		count++
	}

	if count > 0 {
		c.Log.OK("Archived %d file(s)", count)
	} else {
		c.Log.Skip("Nothing to archive")
	}
	return nil
}
