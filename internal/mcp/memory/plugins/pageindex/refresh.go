package pageindex

import (
	"context"
	"sync"
	"time"

	glog "github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// Refresh policy for trees whose LLM work a rolling budget or the indexing
// deadline cut short. A "document_limit" tree is final for its content, because
// a refresh would hit the same ceiling.
const (
	// maxRefreshAttempts bounds the refreshes of one content generation.
	maxRefreshAttempts = 5
	// refreshBaseDelay is the wait before the first refresh; it doubles per attempt.
	refreshBaseDelay = 5 * time.Minute
	// refreshMaxDelay caps the wait between refreshes.
	refreshMaxDelay = 6 * time.Hour
	// refreshQueueSize bounds the refreshes waiting for the worker.
	refreshQueueSize = 32
)

// refreshable reports whether reason is a degradation a later run can overcome.
func refreshable(reason string) bool {
	return reason == degradedTimeout || reason == degradedBudget
}

// markRefresh records the degraded state and next refresh time of entry. refreshes
// is the number of refreshes already made for this content generation.
func (p *Plugin) markRefresh(entry *IndexEntry, reason string, refreshes int) {
	entry.Degraded = reason
	if !refreshable(reason) {
		return
	}
	delay := refreshBaseDelay << min(refreshes, 10)
	if delay > refreshMaxDelay || delay <= 0 {
		delay = refreshMaxDelay
	}
	entry.RefreshAttempts = refreshes
	entry.RefreshAfter = p.now().UTC().Add(delay).Format(time.RFC3339)
}

// refreshDue reports whether a degraded entry should be re-indexed at now.
func refreshDue(entry IndexEntry, now time.Time) bool {
	if !refreshable(entry.Degraded) || entry.RefreshAttempts >= maxRefreshAttempts {
		return false
	}
	after, err := time.Parse(time.RFC3339, entry.RefreshAfter)
	return err != nil || !now.Before(after)
}

// refreshJob re-indexes one degraded document generation.
type refreshJob struct {
	auth          files.AuthContext
	project, path string
	contentHash   string
	attempts      int
}

// key deduplicates jobs for the same document generation.
func (j refreshJob) key() string {
	return PageIndexStorageProject(j.auth, j.project) + "\x00" + j.path + "\x00" + j.contentHash
}

// degradedRefresher runs refresh jobs one at a time on a bounded, deduplicated
// queue. Searches that meet a due degraded entry enqueue it; a full queue drops
// the job, which a later search enqueues again.
type degradedRefresher struct {
	mu      sync.Mutex
	pending map[string]struct{}
	jobs    chan refreshJob
	cancel  context.CancelFunc
	done    chan struct{}
}

// startRefresher starts the plugin's refresh worker. The worker keeps ctx's
// values but not its cancellation; Stop ends it.
func (p *Plugin) startRefresher(ctx context.Context) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	r := &degradedRefresher{pending: make(map[string]struct{}), jobs: make(chan refreshJob, refreshQueueSize), cancel: cancel, done: make(chan struct{})}
	p.refresher = r
	go func() {
		defer close(r.done)
		for {
			select {
			case <-ctx.Done():
				return
			case job := <-r.jobs:
				p.runRefresh(ctx, job)
				r.mu.Lock()
				delete(r.pending, job.key())
				r.mu.Unlock()
			}
		}
	}()
}

// stop cancels the worker, including a refresh in flight, and waits for it.
func (r *degradedRefresher) stop() {
	if r == nil {
		return
	}
	r.cancel()
	<-r.done
}

// enqueue schedules job unless the same generation is already waiting or the
// queue is full. It never blocks the caller.
func (r *degradedRefresher) enqueue(job refreshJob) bool {
	if r == nil {
		return false
	}
	key := job.key()
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.pending[key]; ok {
		return false
	}
	select {
	case r.jobs <- job:
		r.pending[key] = struct{}{}
		return true
	default:
		return false
	}
}

// maybeRefresh enqueues a refresh of a degraded search candidate when it is due
// and the caller's rolling budget can pay for it.
func (p *Plugin) maybeRefresh(auth files.AuthContext, project, userPath string, entry IndexEntry) {
	if p.refresher == nil || p.indexer == nil || !refreshDue(entry, p.now()) || !p.indexer.gate.spend.hasBudget(pageIndexTenant(auth)) {
		return
	}
	if p.refresher.enqueue(refreshJob{auth: auth, project: project, path: userPath, contentHash: entry.SourceContentHash, attempts: entry.RefreshAttempts}) && p.log != nil {
		p.log.Debug("pageindex.refresh.enqueued", glog.String("path", userPath), glog.String("reason", entry.Degraded), glog.Int("attempts", entry.RefreshAttempts))
	}
}

// runRefresh re-indexes the job's document when it is still due and its content
// is unchanged. The catalog is re-read first, so a job queued before an earlier
// refresh (or a new write) published never repeats that work.
func (p *Plugin) runRefresh(ctx context.Context, job refreshJob) {
	if !p.indexer.gate.spend.hasBudget(pageIndexTenant(job.auth)) {
		return
	}
	index, err := p.store.GetIndex(ctx, PageIndexStorageProject(job.auth, job.project))
	if err != nil {
		return
	}
	entry, ok := index[job.path]
	if !ok || entry.SourceContentHash != job.contentHash || entry.RefreshAttempts != job.attempts || !refreshDue(entry, p.now()) {
		return
	}
	read, err := p.userFS.Read(ctx, job.auth, job.project, job.path, 0, -1)
	if err != nil || files.HashFileContent([]byte(read.Content)) != job.contentHash {
		return
	}
	if err := p.indexAndPublish(ctx, job.auth, job.project, job.path, read.Content, job.attempts+1); err != nil && p.log != nil {
		p.log.Warn("pageindex.refresh.failed", glog.String("path", job.path), glog.Error(err))
	}
}
