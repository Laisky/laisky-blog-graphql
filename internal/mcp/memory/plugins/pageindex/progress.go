package pageindex

import (
	logSDK "github.com/Laisky/go-utils/v6/log"
	glog "github.com/Laisky/zap"
)

// Progress is one in-process indexing event (§2.6.4.4).
type Progress struct {
	Phase     string
	Percent   int
	TokensIn  int
	TokensOut int
	LLMCalls  int
	// UpstreamRequests, Retries and ChargedTokens are set on the final event:
	// every request sent, the retries among them, and the tokens charged for them.
	UpstreamRequests int
	Retries          int
	ChargedTokens    int64
	Message          string
}

// Reporter forwards progress events to a buffered channel and a logger.
type Reporter struct {
	out chan<- Progress
	log logSDK.Logger
}

// NewReporter binds out (may be nil) and log (must be non-nil).
func NewReporter(out chan<- Progress, log logSDK.Logger) *Reporter {
	return &Reporter{out: out, log: log}
}

// Report emits a progress event. Channel sends are non-blocking.
func (r *Reporter) Report(p Progress) {
	if r == nil {
		return
	}
	if r.log != nil {
		r.log.Debug("pageindex.progress",
			glog.String("phase", p.Phase),
			glog.Int("percent", p.Percent),
			glog.Int("tokens_in", p.TokensIn),
			glog.Int("tokens_out", p.TokensOut),
			glog.Int("llm_calls", p.LLMCalls),
			glog.Int("upstream_requests", p.UpstreamRequests),
			glog.Int("retries", p.Retries),
			glog.Int64("charged_tokens", p.ChargedTokens),
			glog.String("message", p.Message),
		)
	}
	if r.out == nil {
		return
	}
	select {
	case r.out <- p:
	default:
	}
}
