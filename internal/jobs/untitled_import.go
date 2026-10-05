package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gitlab.com/niharokz/kronos/internal/job"
)

// untitled_import appends every inbox file with "untitled" in its name to
// note.md under a "## <file> | YYYYMMDD" heading, then deletes the original.
// It appends first and deletes after, so a crash can duplicate a note but
// never lose one.
//
// Options: note_file (default note.md, inside the inbox).

func init() { job.Register("untitled_import", untitledImport) }

func untitledImport(c *job.Ctx) error {
	inbox := c.Path("inbox") // kronos.yml: paths.inbox
	if !isDir(inbox) {
		return missingDir(inbox)
	}
	noteName := c.String("note_file", "note.md") // kronos.yml: jobs.untitled_import.options.note_file
	notePath := filepath.Join(inbox, noteName)
	today := c.Now.Format("20060102")

	entries, err := os.ReadDir(inbox)
	if err != nil {
		return err
	}
	count := 0
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(inbox, name)
		if !isFile(path) || strings.EqualFold(name, noteName) || !strings.Contains(strings.ToLower(name), "untitled") {
			continue
		}

		content, err := readText(path)
		if err != nil {
			return err
		}
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		if err := c.AppendFile(notePath, fmt.Sprintf("\n## %s | %s\n\n%s\n---\n", name, today, content)); err != nil {
			return err
		}
		if err := c.Remove(path); err != nil {
			return err
		}
		count++
	}

	if count > 0 {
		c.Log.OK("Merged %d file(s) into %s", count, noteName)
	} else {
		c.Log.Skip("No untitled files found")
	}
	return nil
}
