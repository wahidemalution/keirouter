package backupjob

import "testing"

// Review Focus 5: folder input variants map to exactly one slash.
func TestFTPPathJoinsFolder(t *testing.T) {
	for folder, want := range map[string]string{
		"":          "f.json.gz",
		"/":         "f.json.gz",
		"backups":   "backups/f.json.gz",
		"backups//": "backups/f.json.gz",
		"/srv/kr/":  "/srv/kr/f.json.gz",
		" a/b ":     "a/b/f.json.gz",
	} {
		if got := NewFTP(FTPSettings{Folder: folder}).(*ftpTarget).path("f.json.gz"); got != want {
			t.Errorf("folder %q: path = %q, want %q", folder, got, want)
		}
	}
}
