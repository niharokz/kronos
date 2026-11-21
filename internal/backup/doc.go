// Package backup holds Kronos's backup job (module "backup").
//
//	backup.go   notes_backup – the notes vault (paths.vault) → paths.backups/<prefix>_YYYYMMDD_HHMMSS.tar.gz
//
// Pure Go (archive/tar + compress/gzip): no tar, gzip or tzdata needed in the image, and the
// file name uses the container's TZ. The archive is written as a hidden .tmp file, read back
// in full to prove it is a valid gzip + tar, and only then renamed into place. Old archives
// (same prefix) past keep_days are deleted after a good backup, never the newest one.
//
// The backups folder is a separate mount (.env KRONOS_BACKUP → /data/backups), so the archive
// never lands inside the vault it copies or gets synced to the phone.
package backup
