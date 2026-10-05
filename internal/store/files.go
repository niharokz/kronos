package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AtomicWrite writes data via a hidden temp file in the same folder, fsyncs
// it, then renames it over path, so readers never see a half-written file.
func AtomicWrite(path string, data []byte) error { return AtomicWritePerm(path, data, 0o644) }

// AtomicWritePerm is AtomicWrite with explicit permissions (0o600 for secrets).
func AtomicWritePerm(path string, data []byte, perm os.FileMode) error {
	dir, name := filepath.Split(path)
	tmp := filepath.Join(dir, "."+name+".tmp")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// UniquePath returns dir/name, or dir/stem_1.ext, dir/stem_2.ext … if taken,
// so a move never overwrites an existing file. Python: unique_path().
func UniquePath(dir, name string) string {
	candidate := filepath.Join(dir, name)
	stem, ext := splitExt(name)
	for i := 1; exists(candidate); i++ {
		candidate = filepath.Join(dir, fmt.Sprintf("%s_%d%s", stem, i, ext))
	}
	return candidate
}

// splitExt mirrors pathlib's stem/suffix: ".bashrc" has no suffix,
// "a.tar.gz" → ("a.tar", ".gz").
func splitExt(name string) (string, string) {
	i := strings.LastIndex(name, ".")
	if i <= 0 || i == len(name)-1 {
		return name, ""
	}
	return name[:i], name[i:]
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
