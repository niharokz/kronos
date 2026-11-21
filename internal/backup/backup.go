package backup

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gitlab.com/niharokz/kronos/internal/job"
)

// notes_backup packs the notes vault into one .tar.gz in the backups folder.
//
// Options:
//
//	prefix      notes   file name: <prefix>_YYYYMMDD_HHMMSS.tar.gz
//	keep_days   30      delete this prefix's archives older than this (0 = keep all)
//	exclude     []      names or paths relative to the vault to leave out (glob, e.g. .stversions, "*.tmp")
//
// Always left out: Syncthing's partial files (.syncthing.*.tmp, ~syncthing~*.tmp) and the
// backups folder itself if it lies inside the vault. A file that vanishes while being read
// (Syncthing, Obsidian) is skipped with a warning; an unreadable file fails the run, so a
// backup is never silently incomplete.
//
// Restore: tar -xzf <file> -C <empty folder>

func init() { job.Register("notes_backup", notesBackup) }

const tmpSuffix = ".tmp"

type stats struct {
	files, dirs, links int
	raw                int64
	vanished, changed  []string
}

func notesBackup(c *job.Ctx) error {
	src := c.Path("vault")    // kronos.yml: paths.vault
	dest := c.Path("backups") // kronos.yml: paths.backups (.env KRONOS_BACKUP)
	prefix := c.String("prefix", "notes")
	if prefix == "" || strings.ContainsAny(prefix, `/\`) {
		return fmt.Errorf("options.prefix %q: want a plain name", prefix)
	}
	keep := c.Int("keep_days", 30)
	exclude := c.Strings("exclude", nil)

	if info, err := os.Stat(src); err != nil || !info.IsDir() {
		return fmt.Errorf("vault %s is not a folder: %v", src, err)
	}
	if info, err := os.Stat(dest); err != nil || !info.IsDir() {
		return fmt.Errorf("backups folder %s missing – set KRONOS_BACKUP in .env and mount it: %v", dest, err)
	}
	skipDir := insideDir(src, dest) // backups inside the vault: never archive the archives
	if skipDir != "" && filepath.Clean(skipDir) == filepath.Clean(mustAbs(src)) {
		return fmt.Errorf("backups folder %s is the vault itself – point KRONOS_BACKUP at another folder", dest)
	}

	name := fmt.Sprintf("%s_%s.tar.gz", prefix, c.Now.Format("20060102_150405"))
	final := filepath.Join(dest, name)

	if c.DryRun {
		st, err := walk(c, src, skipDir, exclude, nil)
		if err != nil {
			return err
		}
		fmt.Fprintf(c.Out, "~ would write %s: %d files, %d folders, %s before compression\n", final, st.files, st.dirs, size(st.raw))
		for _, old := range expired(dest, prefix, keep, c.Now, "") {
			fmt.Fprintf(c.Out, "~ would delete %s (older than %d days)\n", old, keep)
		}
		return nil
	}

	removeStaleTmp(c, dest, prefix)
	started := time.Now()
	tmp := filepath.Join(dest, "."+name+tmpSuffix)
	st, err := writeArchive(c, tmp, src, skipDir, exclude)
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := verify(tmp, st.files+st.dirs+st.links); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("archive check failed, nothing kept: %v", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return err
	}
	info, err := os.Stat(final)
	if err != nil {
		return err
	}

	for _, p := range st.vanished {
		c.Log.Warn("Skipped (deleted while reading): %s", p)
	}
	for _, p := range st.changed {
		c.Log.Warn("Changed while reading, saved as far as it was: %s", p)
	}
	removed := 0
	for _, old := range expired(dest, prefix, keep, c.Now, final) {
		if err := os.Remove(old); err != nil {
			c.Log.Warn("Old backup not deleted: %v", err)
			continue
		}
		removed++
	}
	c.Log.OK("%s: %d files, %s (%s before compression) in %.1fs; %d old removed",
		name, st.files, size(info.Size()), size(st.raw), time.Since(started).Seconds(), removed)
	return nil
}

// writeArchive streams the vault into path (gzip over tar), fsyncs and closes it.
func writeArchive(c *job.Ctx, path, src, skipDir string, exclude []string) (stats, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // the whole vault: owner only
	if err != nil {
		return stats{}, err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	st, err := walk(c, src, skipDir, exclude, tw)
	if err != nil {
		return st, err
	}
	if err := tw.Close(); err != nil {
		return st, err
	}
	if err := gz.Close(); err != nil {
		return st, err
	}
	if err := f.Sync(); err != nil {
		return st, err
	}
	return st, f.Close()
}

// walk visits the vault in a stable order; with tw nil it only counts (dry run).
func walk(c *job.Ctx, src, skipDir string, exclude []string, tw *tar.Writer) (stats, error) {
	var st stats
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if cerr := c.Err(); cerr != nil {
			return cerr // timed out: stop instead of running on in the background
		}
		if path == src {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		rel = filepath.ToSlash(rel)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				st.vanished = append(st.vanished, rel)
				return nil
			}
			return fmt.Errorf("%s: %w", rel, err)
		}
		if d.IsDir() && (path == skipDir || excluded(rel, exclude)) {
			return filepath.SkipDir
		}
		if excluded(rel, exclude) || syncthingTemp(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			st.vanished = append(st.vanished, rel)
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		switch {
		case info.IsDir():
			st.dirs++
			return addHeader(tw, info, rel+"/", "")
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
			st.links++
			return addHeader(tw, info, rel, target)
		case info.Mode().IsRegular():
			ok, err := addFile(tw, path, rel, &st)
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
			if ok {
				st.files++
			}
			return nil
		}
		return nil // sockets, pipes, devices: not notes
	})
	return st, err
}

func addHeader(tw *tar.Writer, info fs.FileInfo, name, link string) error {
	if tw == nil {
		return nil
	}
	h, err := tar.FileInfoHeader(info, link)
	if err != nil {
		return err
	}
	h.Name = name
	h.Uname, h.Gname = "", "" // no account names from the host in the archive
	return tw.WriteHeader(h)
}

// addFile writes one regular file. The size is taken from the open file, and exactly that many
// bytes are written: a file that grows meanwhile is cut at that size, one that shrinks is padded
// with zeros (and reported), so the archive itself is always valid.
func addFile(tw *tar.Writer, path, rel string, st *stats) (bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		st.vanished = append(st.vanished, rel)
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	st.raw += info.Size()
	if tw == nil {
		return true, nil
	}
	h, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return false, err
	}
	h.Name, h.Uname, h.Gname = rel, "", ""
	if err := tw.WriteHeader(h); err != nil {
		return false, err
	}
	n, err := io.CopyN(tw, f, info.Size())
	if errors.Is(err, io.EOF) { // shrank while reading
		st.changed = append(st.changed, rel)
		_, err = io.CopyN(tw, zeros{}, info.Size()-n)
	}
	return err == nil, err
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// verify reads the whole archive back: gzip checksum and every tar entry.
func verify(path string, want int) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	n := 0
	for {
		_, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if _, err := io.Copy(io.Discard, tr); err != nil {
			return err
		}
		n++
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if n != want {
		return fmt.Errorf("%d entries, want %d", n, want)
	}
	return nil
}

// expired lists this prefix's archives older than keepDays (by mtime), oldest first, never keep.
func expired(dir, prefix string, keepDays int, now time.Time, keep string) []string {
	if keepDays <= 0 {
		return nil
	}
	cutoff := now.Add(-time.Duration(keepDays) * 24 * time.Hour)
	matches, _ := filepath.Glob(filepath.Join(dir, prefix+"_*.tar.gz"))
	sort.Strings(matches)
	var old []string
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil || !info.Mode().IsRegular() || m == keep {
			continue
		}
		if info.ModTime().Before(cutoff) {
			old = append(old, m)
		}
	}
	return old
}

// removeStaleTmp deletes half-written archives left by a crash or a killed container.
func removeStaleTmp(c *job.Ctx, dir, prefix string) {
	matches, _ := filepath.Glob(filepath.Join(dir, "."+prefix+"_*.tar.gz"+tmpSuffix))
	for _, m := range matches {
		if err := os.Remove(m); err == nil {
			c.Log.Info("Removed unfinished %s", filepath.Base(m))
		}
	}
}

// excluded: rel (slash-separated) or its base name matches one of the patterns.
func excluded(rel string, patterns []string) bool {
	base := rel[strings.LastIndex(rel, "/")+1:]
	for _, p := range patterns {
		p = strings.Trim(filepath.ToSlash(p), "/")
		if ok, _ := filepath.Match(p, rel); ok {
			return true
		}
		if ok, _ := filepath.Match(p, base); ok {
			return true
		}
	}
	return false
}

func syncthingTemp(name string) bool {
	return (strings.HasPrefix(name, ".syncthing.") || strings.HasPrefix(name, "~syncthing~")) && strings.HasSuffix(name, ".tmp")
}

// insideDir returns dir when it is src or below it ("" otherwise).
func insideDir(src, dir string) string {
	a, err1 := filepath.Abs(src)
	b, err2 := filepath.Abs(dir)
	if err1 != nil || err2 != nil {
		return ""
	}
	if rel, err := filepath.Rel(a, b); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
		return b
	}
	return ""
}

func mustAbs(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

func size(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
