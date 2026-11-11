package backupjob

import (
	"regexp"
	"sort"
	"time"
)

// RetryAfterFailure is how soon a failed run is retried, capped by the interval.
const RetryAfterFailure = 30 * time.Minute

// Upload timeout bounds.
const (
	minUploadTimeout       = 10 * time.Minute
	unlimitedUploadTimeout = 2 * time.Hour
)

// State is what the runner remembers between runs.
type State struct {
	LastSuccess         time.Time `json:"last_success"`
	LastAttempt         time.Time `json:"last_attempt"`
	LastFailed          bool      `json:"last_failed"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	LastSize            int64     `json:"last_size"`
	LastFile            string    `json:"last_file"`
}

// NextDue is when the next run should start. A zero time means now.
func NextDue(st State, interval time.Duration) time.Time {
	if st.LastFailed && !st.LastAttempt.IsZero() {
		return st.LastAttempt.Add(min(RetryAfterFailure, interval))
	}
	if st.LastSuccess.IsZero() {
		return time.Time{}
	}
	return st.LastSuccess.Add(interval)
}

var backupNameRE = regexp.MustCompile(`^keirouter-backup-\d{8}-\d{6}\.json\.gz$`)

// FileName is the backup file name for a run started at t.
func FileName(t time.Time) string {
	return "keirouter-backup-" + t.UTC().Format("20060102-150405") + ".json.gz"
}

// IsBackupName reports whether name is a file this feature created.
func IsBackupName(name string) bool { return backupNameRE.MatchString(name) }

// SelectForDeletion returns the backup files beyond the newest keep, oldest
// first. Names that are not backup files are never returned.
func SelectForDeletion(names []string, keep int) []string {
	var ours []string
	for _, n := range names {
		if IsBackupName(n) {
			ours = append(ours, n)
		}
	}
	if len(ours) <= keep {
		return nil
	}
	sort.Strings(ours)
	return ours[:len(ours)-keep]
}

// unlimitedFloorKBps is the slowest uplink an unlimited upload is given time
// for (128 KiB/s, about 1 Mbit/s).
const unlimitedFloorKBps = 128

// UploadTimeout gives an upload of size bytes at limitKBps 50% headroom, at
// least 10 minutes. An unlimited upload is timed as if it ran at the floor
// rate, with at least 2 hours, so a large file on a slow uplink can finish.
func UploadTimeout(size int64, limitKBps int) time.Duration {
	if limitKBps <= 0 {
		return max(atRate(size, unlimitedFloorKBps), unlimitedUploadTimeout)
	}
	return max(atRate(size, limitKBps), minUploadTimeout)
}

func atRate(size int64, kbps int) time.Duration {
	return time.Duration(float64(size) / float64(kbps*1024) * 1.5 * float64(time.Second))
}
