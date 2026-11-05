package telegram

import (
	"time"

	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
)

// alertCredentialSummaryLogInterval spaces the credential directory's read
// summary, so reads leave a steady record without one entry per read.
const alertCredentialSummaryLogInterval = time.Minute

// alertCredentialReadReason names why a directory read started.
type alertCredentialReadReason string

const (
	// readReasonInitial is the directory's first read.
	readReasonInitial alertCredentialReadReason = "initial"
	// readReasonInterval rereads a healthy copy after the refresh interval.
	readReasonInterval alertCredentialReadReason = "interval"
	// readReasonLocalChange follows an alert type this process created or re-keyed.
	readReasonLocalChange alertCredentialReadReason = "local_change"
	// readReasonRetry follows a failed read.
	readReasonRetry alertCredentialReadReason = "retry"
	// readReasonStorageRefusal follows storage refusing a pair the copy held.
	readReasonStorageRefusal alertCredentialReadReason = "storage_refusal"
)

// alertCredentialReadStats counts the credential directory's reads since the
// process started, so operators can see how often storage is scanned, how much
// each read copies and how long it takes. The directory's mu guards it.
type alertCredentialReadStats struct {
	// Reads counts started reads, and the By* fields split them by reason.
	Reads, ByInitial, ByInterval, ByLocalChange, ByRetry, ByStorageRefusal uint64
	// Failed counts reads that kept the previous copy, TimedOut the part of them
	// that hit the read deadline, and Late the loaders that returned after their
	// deadline had already ended the read.
	Failed, TimedOut, Late uint64
	// AlertTypes sums the alert types copied by successful reads; LastAlertTypes
	// is the last successful read's count.
	AlertTypes, LastAlertTypes int
	// LastDuration and MaxDuration time successful reads.
	LastDuration, MaxDuration time.Duration
	// Remembered counts pairs added after the fallback lookup accepted them.
	Remembered uint64
}

// started counts one read started for reason.
func (s *alertCredentialReadStats) started(reason alertCredentialReadReason) {
	s.Reads++
	switch reason {
	case readReasonInitial:
		s.ByInitial++
	case readReasonInterval:
		s.ByInterval++
	case readReasonLocalChange:
		s.ByLocalChange++
	case readReasonRetry:
		s.ByRetry++
	case readReasonStorageRefusal:
		s.ByStorageRefusal++
	}
}

// copied records a successful read that copied alerts alert types in elapsed.
func (s *alertCredentialReadStats) copied(alerts int, elapsed time.Duration) {
	s.AlertTypes += alerts
	s.LastAlertTypes = alerts
	s.LastDuration = elapsed
	s.MaxDuration = max(s.MaxDuration, elapsed)
}

// logSummary writes the read counters at most once per
// alertCredentialSummaryLogInterval. It runs after reads, so an idle directory
// writes nothing.
func (d *alertCredentialDirectory) logSummary(logger glog.Logger) {
	d.summaryLog.Do(func() {
		d.mu.Lock()
		stats := d.stats
		d.mu.Unlock()
		logger.Info("alert credential directory reads",
			zap.Uint64("reads_total", stats.Reads),
			zap.Uint64("reads_initial", stats.ByInitial),
			zap.Uint64("reads_interval", stats.ByInterval),
			zap.Uint64("reads_local_change", stats.ByLocalChange),
			zap.Uint64("reads_retry", stats.ByRetry),
			zap.Uint64("reads_storage_refusal", stats.ByStorageRefusal),
			zap.Uint64("failed_total", stats.Failed),
			zap.Uint64("timed_out_total", stats.TimedOut),
			zap.Uint64("late_loaders_total", stats.Late),
			zap.Int("alert_types_copied_total", stats.AlertTypes),
			zap.Int("last_alert_types", stats.LastAlertTypes),
			zap.Duration("last_read_duration", stats.LastDuration),
			zap.Duration("max_read_duration", stats.MaxDuration),
			zap.Uint64("remembered_total", stats.Remembered),
			zap.Uint64("unwaited_misses_total", d.overflows.Load()),
			zap.Duration("refresh_interval", d.interval),
		)
	})
}
