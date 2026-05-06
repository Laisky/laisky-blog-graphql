package plugin

import (
	"context"
	"runtime"
	"sync"
	"time"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"golang.org/x/sync/semaphore"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// defaultShadowOpTimeout bounds each fire-and-forget shadow call.
const defaultShadowOpTimeout = 30 * time.Second

// defaultShadowDrainGrace caps how long Stop waits for in-flight shadow ops.
const defaultShadowDrainGrace = 5 * time.Second

// ShadowConfig wires a primary (live) plugin to a shadow plugin and a Recorder.
type ShadowConfig struct {
	Live        Plugin
	Shadow      Plugin
	Recorder    Recorder
	Name        string
	Logger      logSDK.Logger
	OpTimeout   time.Duration
	DrainGrace  time.Duration
	Concurrency int64
}

// ShadowPlugin dual-writes mutations and dual-reads queries for promotion-gate
// scoring per proposal §7.8. The live plugin's response is what the user sees;
// the shadow plugin's response is captured for offline scoring and never
// returned. A failure on the shadow side never blocks or alters the live path.
type ShadowPlugin struct {
	live       Plugin
	shadow     Plugin
	rec        Recorder
	name       string
	logger     logSDK.Logger
	opTimeout  time.Duration
	drainGrace time.Duration
	sem        *semaphore.Weighted

	shutdownOnce sync.Once
	shutdown     chan struct{}
	inflight     sync.WaitGroup
}

// NewShadowPlugin validates the config and returns a wrapper plugin.
func NewShadowPlugin(cfg ShadowConfig) (*ShadowPlugin, error) {
	if cfg.Live == nil {
		return nil, errors.New("live plugin is required")
	}
	if cfg.Shadow == nil {
		return nil, errors.New("shadow plugin is required")
	}
	if cfg.Recorder == nil {
		return nil, errors.New("recorder is required")
	}

	name := cfg.Name
	if name == "" {
		name = "shadow:" + cfg.Live.Name() + ":" + cfg.Shadow.Name()
	}

	logger := cfg.Logger
	if logger == nil {
		logger = log.Logger.Named("mcp_memory_shadow")
	}

	opTimeout := cfg.OpTimeout
	if opTimeout <= 0 {
		opTimeout = defaultShadowOpTimeout
	}

	drainGrace := cfg.DrainGrace
	if drainGrace <= 0 {
		drainGrace = defaultShadowDrainGrace
	}

	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = int64(runtime.NumCPU())
		if concurrency <= 0 {
			concurrency = 1
		}
	}

	return &ShadowPlugin{
		live:       cfg.Live,
		shadow:     cfg.Shadow,
		rec:        cfg.Recorder,
		name:       name,
		logger:     logger,
		opTimeout:  opTimeout,
		drainGrace: drainGrace,
		sem:        semaphore.NewWeighted(concurrency),
		shutdown:   make(chan struct{}),
	}, nil
}

// Name returns the configured wrapper name.
func (s *ShadowPlugin) Name() string { return s.name }

// Capabilities reports the live plugin's user-visible contract.
func (s *ShadowPlugin) Capabilities() Capabilities { return s.live.Capabilities() }

// Start brings up live then shadow; a shadow start failure logs but never fails.
func (s *ShadowPlugin) Start(ctx context.Context) error {
	if err := s.live.Start(ctx); err != nil {
		return errors.Wrap(err, "start live plugin")
	}
	if err := s.shadow.Start(ctx); err != nil {
		s.logger.Warn("shadow plugin start failed", zap.String("plugin", s.shadow.Name()), zap.Error(err))
	}
	return nil
}

// Stop signals shutdown, waits for in-flight shadow ops up to drainGrace
// (cancelling any still-running ops once the grace period expires), then
// stops shadow and live in reverse order.
func (s *ShadowPlugin) Stop(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		s.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(s.drainGrace):
		s.logger.Warn("shadow plugin drain grace exceeded", zap.Duration("grace", s.drainGrace))
		s.shutdownOnce.Do(func() { close(s.shutdown) })
		<-done
	}
	s.shutdownOnce.Do(func() { close(s.shutdown) })

	var firstErr error
	if err := s.shadow.Stop(ctx); err != nil {
		s.logger.Warn("shadow plugin stop failed", zap.String("plugin", s.shadow.Name()), zap.Error(err))
	}
	if err := s.live.Stop(ctx); err != nil {
		firstErr = errors.Wrap(err, "stop live plugin")
	}
	if err := s.rec.Close(); err != nil {
		if firstErr != nil {
			return errors.Wrapf(firstErr, "close recorder: %v", err)
		}
		firstErr = errors.Wrap(err, "close recorder")
	}
	return firstErr
}

// Stat forwards to live; reads are not part of §7.8 scoring.
func (s *ShadowPlugin) Stat(ctx context.Context, auth files.AuthContext, project, path string) (files.StatResult, error) {
	return s.live.Stat(ctx, auth, project, path)
}

// Read forwards to live; reads are not part of §7.8 scoring.
func (s *ShadowPlugin) Read(ctx context.Context, auth files.AuthContext, project, path string, offset, length int64) (files.ReadResult, error) {
	return s.live.Read(ctx, auth, project, path, offset, length)
}

// List forwards to live; reads are not part of §7.8 scoring.
func (s *ShadowPlugin) List(ctx context.Context, auth files.AuthContext, project, path string, depth, limit int) (files.ListResult, error) {
	return s.live.List(ctx, auth, project, path, depth, limit)
}

// Write applies the mutation to live first; on success it dual-writes to shadow.
func (s *ShadowPlugin) Write(ctx context.Context, auth files.AuthContext, project, path, content, contentEncoding string, offset int64, mode files.WriteMode) (files.WriteResult, error) {
	liveStart := time.Now()
	res, err := s.live.Write(ctx, auth, project, path, content, contentEncoding, offset, mode)
	liveDur := time.Since(liveStart)
	if err != nil {
		return res, err
	}

	s.fireMutation("write", project, path, liveDur, func(opCtx context.Context) error {
		_, e := s.shadow.Write(opCtx, auth, project, path, content, contentEncoding, offset, mode)
		return e
	})
	return res, nil
}

// Delete applies the mutation to live first; on success it dual-writes to shadow.
func (s *ShadowPlugin) Delete(ctx context.Context, auth files.AuthContext, project, path string, recursive bool) (files.DeleteResult, error) {
	liveStart := time.Now()
	res, err := s.live.Delete(ctx, auth, project, path, recursive)
	liveDur := time.Since(liveStart)
	if err != nil {
		return res, err
	}

	s.fireMutation("delete", project, path, liveDur, func(opCtx context.Context) error {
		_, e := s.shadow.Delete(opCtx, auth, project, path, recursive)
		return e
	})
	return res, nil
}

// Rename applies the mutation to live first; on success it dual-writes to shadow.
func (s *ShadowPlugin) Rename(ctx context.Context, auth files.AuthContext, project, fromPath, toPath string, overwrite bool) (files.RenameResult, error) {
	liveStart := time.Now()
	res, err := s.live.Rename(ctx, auth, project, fromPath, toPath, overwrite)
	liveDur := time.Since(liveStart)
	if err != nil {
		return res, err
	}

	s.fireMutation("rename", project, fromPath, liveDur, func(opCtx context.Context) error {
		_, e := s.shadow.Rename(opCtx, auth, project, fromPath, toPath, overwrite)
		return e
	})
	return res, nil
}

// Search returns the live result; the shadow Search is captured asynchronously.
func (s *ShadowPlugin) Search(ctx context.Context, auth files.AuthContext, project, query, pathPrefix string, limit int) (files.SearchResult, error) {
	liveStart := time.Now()
	liveRes, liveErr := s.live.Search(ctx, auth, project, query, pathPrefix, limit)
	liveDur := time.Since(liveStart)

	s.fireSearch(auth, project, query, pathPrefix, limit, liveRes, liveErr, liveDur)
	return liveRes, liveErr
}

// fireMutation runs a bounded, fire-and-forget shadow mutation.
func (s *ShadowPlugin) fireMutation(op, project, path string, liveDur time.Duration, run func(context.Context) error) {
	select {
	case <-s.shutdown:
		s.recordMutation(op, project, path, liveDur, 0, "", "shadow: shutdown")
		return
	default:
	}

	s.inflight.Add(1)
	go func() {
		defer s.inflight.Done()

		semCtx, semCancel := context.WithTimeout(context.Background(), s.opTimeout)
		defer semCancel()
		if err := s.sem.Acquire(semCtx, 1); err != nil {
			s.logger.Warn("shadow semaphore acquire failed",
				zap.String("op", op),
				zap.String("project", project),
				zap.String("path", path),
				zap.Error(err),
			)
			s.recordMutation(op, project, path, liveDur, 0, "", "shadow: "+err.Error())
			return
		}
		defer s.sem.Release(1)

		opCtx, cancel := context.WithTimeout(context.Background(), s.opTimeout)
		defer cancel()
		stop := s.watchShutdown(cancel)
		defer stop()

		shadowStart := time.Now()
		err := run(opCtx)
		shadowDur := time.Since(shadowStart)
		shadowErrMsg := ""
		if err != nil {
			shadowErrMsg = err.Error()
			s.logger.Warn("shadow mutation failed",
				zap.String("op", op),
				zap.String("project", project),
				zap.String("path", path),
				zap.Error(err),
			)
		}
		s.recordMutation(op, project, path, liveDur, shadowDur, "", shadowErrMsg)
	}()
}

// fireSearch runs a bounded, fire-and-forget shadow Search and records the pair.
func (s *ShadowPlugin) fireSearch(auth files.AuthContext, project, query, pathPrefix string, limit int, liveRes files.SearchResult, liveErr error, liveDur time.Duration) {
	select {
	case <-s.shutdown:
		s.recordSearch(project, query, pathPrefix, limit, liveRes, files.SearchResult{}, liveDur, 0, liveErr, errors.New("shadow: shutdown"))
		return
	default:
	}

	s.inflight.Add(1)
	go func() {
		defer s.inflight.Done()

		semCtx, semCancel := context.WithTimeout(context.Background(), s.opTimeout)
		defer semCancel()
		if err := s.sem.Acquire(semCtx, 1); err != nil {
			s.logger.Warn("shadow semaphore acquire failed",
				zap.String("op", "search"),
				zap.String("project", project),
				zap.Error(err),
			)
			s.recordSearch(project, query, pathPrefix, limit, liveRes, files.SearchResult{}, liveDur, 0, liveErr, err)
			return
		}
		defer s.sem.Release(1)

		opCtx, cancel := context.WithTimeout(context.Background(), s.opTimeout)
		defer cancel()
		stop := s.watchShutdown(cancel)
		defer stop()

		shadowStart := time.Now()
		shadowRes, shadowErr := s.shadow.Search(opCtx, auth, project, query, pathPrefix, limit)
		shadowDur := time.Since(shadowStart)
		if shadowErr != nil {
			s.logger.Warn("shadow search failed",
				zap.String("project", project),
				zap.Error(shadowErr),
			)
		}
		s.recordSearch(project, query, pathPrefix, limit, liveRes, shadowRes, liveDur, shadowDur, liveErr, shadowErr)
	}()
}

// watchShutdown cancels the per-op context if the wrapper begins shutdown
// before the op finishes. The returned stop function releases the watcher.
func (s *ShadowPlugin) watchShutdown(cancel context.CancelFunc) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-s.shutdown:
			cancel()
		case <-done:
		}
	}()
	return func() { close(done) }
}

func (s *ShadowPlugin) recordMutation(op, project, path string, liveDur, shadowDur time.Duration, liveErrMsg, shadowErrMsg string) {
	rec := MutationRecord{
		Timestamp:      time.Now().UTC(),
		Op:             op,
		Project:        project,
		Path:           path,
		LiveErr:        liveErrMsg,
		ShadowErr:      shadowErrMsg,
		LiveDuration:   liveDur,
		ShadowDuration: shadowDur,
	}
	if err := s.rec.RecordMutation(rec); err != nil {
		s.logger.Warn("recorder mutation failed", zap.Error(err))
	}
}

func (s *ShadowPlugin) recordSearch(project, query, pathPrefix string, limit int, liveRes, shadowRes files.SearchResult, liveDur, shadowDur time.Duration, liveErr, shadowErr error) {
	rec := SearchRecord{
		Timestamp:      time.Now().UTC(),
		Project:        project,
		Query:          query,
		PathPrefix:     pathPrefix,
		Limit:          limit,
		LiveResult:     liveRes,
		ShadowResult:   shadowRes,
		LiveDuration:   liveDur,
		ShadowDuration: shadowDur,
		LivePlugin:     s.live.Name(),
		ShadowPlugin:   s.shadow.Name(),
	}
	if liveErr != nil {
		rec.LiveErr = liveErr.Error()
	}
	if shadowErr != nil {
		rec.ShadowErr = shadowErr.Error()
	}
	if err := s.rec.RecordSearch(rec); err != nil {
		s.logger.Warn("recorder search failed", zap.Error(err))
	}
}
