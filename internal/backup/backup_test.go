package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"gitlab.com/niharokz/kronos/internal/config"
	"gitlab.com/niharokz/kronos/internal/job"
)

// testCtx: a vault and a backups folder in a temp dir, a fixed clock.
func testCtx(t *testing.T, options map[string]any, dry bool) (*job.Ctx, string, string, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	vault, backups := filepath.Join(dir, "vault"), filepath.Join(dir, "backups")
	for _, d := range []string{vault, backups} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Parse([]byte("paths:\n  vault: "+vault+"\n  backups: "+backups+"\n  logs: ''\n"), "test.yml")
	if err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	c := job.NewCtx(context.Background(), cfg, config.Job{Name: "notes_backup", Options: options}, dry, out)
	c.Now = time.Date(2026, 11, 21, 3, 55, 0, 0, time.Local)
	return c, vault, backups, out
}

func put(t *testing.T, path, body string) {
	t.Helper()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// entries reads an archive: name → content ("" for folders).
func entries(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("not gzip: %v", err)
	}
	tr := tar.NewReader(gz)
	got := map[string]string{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return got
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Uname != "" || h.Gname != "" {
			t.Errorf("%s: account names stored (%q/%q)", h.Name, h.Uname, h.Gname)
		}
		b, _ := io.ReadAll(tr)
		got[h.Name] = string(b)
		if h.Typeflag == tar.TypeSymlink {
			got[h.Name] = "-> " + h.Linkname
		}
	}
}

func list(t *testing.T, dir string) []string {
	t.Helper()
	des, _ := os.ReadDir(dir)
	var names []string
	for _, d := range des {
		names = append(names, d.Name())
	}
	sort.Strings(names)
	return names
}

// A real gzip'd tar named with the local time, owner-only, with every note and no Syncthing temp
// or excluded files; no .tmp left behind.
func TestBackupWritesVerifiedArchive(t *testing.T) {
	c, vault, backups, out := testCtx(t, map[string]any{"exclude": []any{".stversions", "*.bak"}}, false)
	put(t, filepath.Join(vault, "inbox", "note.md"), "# hello\n")
	put(t, filepath.Join(vault, "data", "event.md"), "events: []\n")
	put(t, filepath.Join(vault, "data", ".syncthing.event.md.tmp"), "partial")
	put(t, filepath.Join(vault, "data", "~syncthing~x.md.tmp"), "partial")
	put(t, filepath.Join(vault, ".stversions", "old.md"), "old")
	put(t, filepath.Join(vault, "draft.bak"), "bak")
	if err := os.Symlink("inbox/note.md", filepath.Join(vault, "link.md")); err != nil {
		t.Fatal(err)
	}

	if err := notesBackup(c); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	name := "notes_20261121_035500.tar.gz"
	if got := list(t, backups); len(got) != 1 || got[0] != name {
		t.Fatalf("backups = %v", got)
	}
	file := filepath.Join(backups, name)
	if info, _ := os.Stat(file); info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", info.Mode().Perm())
	}
	got := entries(t, file)
	want := map[string]string{
		"data/": "", "data/event.md": "events: []\n", "inbox/": "", "inbox/note.md": "# hello\n", "link.md": "-> inbox/note.md",
	}
	if len(got) != len(want) {
		t.Errorf("entries = %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if !strings.Contains(out.String(), name+": 2 files") {
		t.Errorf("log:\n%s", out)
	}
}

// Old archives of this prefix past keep_days go (after a good backup); others and recent ones stay;
// a leftover .tmp from a crash is removed.
func TestBackupRetention(t *testing.T) {
	c, vault, backups, _ := testCtx(t, map[string]any{"keep_days": 30}, false)
	put(t, filepath.Join(vault, "a.md"), "a")
	old, recent := filepath.Join(backups, "notes_20261001_035500.tar.gz"), filepath.Join(backups, "notes_20261110_035500.tar.gz")
	other := filepath.Join(backups, "photos_20260101_000000.tar.gz")
	stale := filepath.Join(backups, ".notes_20261120_035500.tar.gz.tmp")
	for _, p := range []string{old, recent, other, stale} {
		put(t, p, "x")
	}
	long := c.Now.Add(-40 * 24 * time.Hour)
	for _, p := range []string{old, other} {
		_ = os.Chtimes(p, long, long)
	}
	_ = os.Chtimes(recent, c.Now.Add(-11*24*time.Hour), c.Now.Add(-11*24*time.Hour))

	if err := notesBackup(c); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(list(t, backups), " ")
	if got != "notes_20261110_035500.tar.gz notes_20261121_035500.tar.gz photos_20260101_000000.tar.gz" {
		t.Errorf("backups = %s", got)
	}
}

// Dry run: counts and says what it would do; writes and deletes nothing.
func TestBackupDryRun(t *testing.T) {
	c, vault, backups, out := testCtx(t, nil, true)
	put(t, filepath.Join(vault, "a.md"), "hello")
	old := filepath.Join(backups, "notes_20260101_000000.tar.gz")
	put(t, old, "x")
	_ = os.Chtimes(old, c.Now.Add(-90*24*time.Hour), c.Now.Add(-90*24*time.Hour))
	if err := notesBackup(c); err != nil {
		t.Fatal(err)
	}
	if got := list(t, backups); len(got) != 1 {
		t.Errorf("dry run changed the folder: %v", got)
	}
	s := out.String()
	if !strings.Contains(s, "~ would write") || !strings.Contains(s, "1 files") || !strings.Contains(s, "~ would delete") {
		t.Errorf("output:\n%s", s)
	}
}

// Backups inside the vault are not archived into themselves; the vault itself is refused;
// a missing backups folder and a bad prefix fail clearly.
func TestBackupFolders(t *testing.T) {
	c, vault, _, out := testCtx(t, nil, false)
	inner := filepath.Join(vault, "backups")
	put(t, filepath.Join(vault, "a.md"), "a")
	put(t, filepath.Join(inner, "notes_20260101_000000.tar.gz"), "old")
	c.Cfg.Paths["backups"] = inner
	if err := notesBackup(c); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	got := entries(t, filepath.Join(inner, "notes_20261121_035500.tar.gz"))
	if _, ok := got["backups/"]; ok || len(got) != 1 {
		t.Errorf("entries = %v", got)
	}

	c.Cfg.Paths["backups"] = vault
	if err := notesBackup(c); err == nil || !strings.Contains(err.Error(), "the vault itself") {
		t.Errorf("vault as backups: %v", err)
	}
	c.Cfg.Paths["backups"] = filepath.Join(vault, "nope")
	if err := notesBackup(c); err == nil || !strings.Contains(err.Error(), "KRONOS_BACKUP") {
		t.Errorf("missing folder: %v", err)
	}
	c.Cfg.Paths["backups"] = inner
	c.Job.Options = map[string]any{"prefix": "../x"}
	if err := notesBackup(c); err == nil {
		t.Error("prefix with a slash should fail")
	}
}

// An unreadable file fails the run and leaves no partial archive.
func TestBackupUnreadableFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	c, vault, backups, _ := testCtx(t, nil, false)
	secret := filepath.Join(vault, "locked.md")
	put(t, secret, "x")
	_ = os.Chmod(secret, 0)
	t.Cleanup(func() { _ = os.Chmod(secret, 0o644) })
	if err := notesBackup(c); err == nil || !strings.Contains(err.Error(), "locked.md") {
		t.Errorf("want failure naming the file, got %v", err)
	}
	if got := list(t, backups); len(got) != 0 {
		t.Errorf("left behind: %v", got)
	}
}

// addFile writes a regular file whole; excluded() matches a path or a base name; Syncthing temps are known.
func TestHelpers(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	p := filepath.Join(t.TempDir(), "log.md")
	put(t, p, "0123456789")
	st := &stats{}
	if ok, err := addFile(tw, p, "log.md", st); err != nil || !ok || st.raw != 10 {
		t.Fatalf("addFile: %v %v %+v", ok, err, st)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&buf)
	if h, err := tr.Next(); err != nil || h.Name != "log.md" || h.Size != 10 {
		t.Fatalf("header: %+v %v", h, err)
	}
	if b, _ := io.ReadAll(tr); string(b) != "0123456789" {
		t.Errorf("content %q", b)
	}
	if n, _ := io.ReadFull(zeros{}, make([]byte, 4)); n != 4 {
		t.Error("zeros")
	}
	if !excluded("a/.stversions", []string{".stversions"}) || !excluded("x/y.bak", []string{"*.bak"}) ||
		!excluded("logs/kronos", []string{"/logs/kronos/"}) || excluded("a/b.md", []string{"c.md"}) {
		t.Error("excluded() patterns")
	}
	if !syncthingTemp(".syncthing.a.md.tmp") || !syncthingTemp("~syncthing~a.md.tmp") || syncthingTemp("a.tmp") {
		t.Error("syncthingTemp()")
	}
	if missing, _ := addFile(nil, filepath.Join(t.TempDir(), "gone.md"), "gone.md", st); missing || len(st.vanished) != 1 {
		t.Errorf("vanished file: %v %+v", missing, st)
	}
}
