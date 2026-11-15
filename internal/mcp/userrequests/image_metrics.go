package userrequests

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Object delete paths and results of mcp_user_image_object_deletes_total.
const (
	deletePathCompensation = "compensation"
	deletePathReconciler   = "reconciler"
	deleteResultDeleted    = "deleted"
	deleteResultFailed     = "failed"
)

// imageObjectMetrics are the Prometheus collectors of the image object
// lifecycle (maintainer review of PR #94). They are process-wide, registered
// once on the default registry, which the API server serves at /metrics. An
// alert on "no cleanup path" is
//
//	max(mcp_user_image_lifecycle_rule_present) + max(mcp_user_image_reconciler_enabled) == 0
//
// and one on a stuck backlog is a rising mcp_user_image_claims_oldest_due_age_seconds
// or a stale mcp_user_image_reconcile_last_success_timestamp_seconds.
type imageObjectMetrics struct {
	// dueClaims is how many claims were due when the last reconciler tick ended.
	dueClaims prometheus.Gauge
	// oldestDueClaimAge is how long the oldest of those claims has been due.
	oldestDueClaimAge prometheus.Gauge
	// objectDeletes counts object deletes by path and result.
	objectDeletes *prometheus.CounterVec
	// reconcilePasses counts reconciler passes by result.
	reconcilePasses *prometheus.CounterVec
	// lastReconcileSuccess is the Unix time of the last pass without errors.
	lastReconcileSuccess prometheus.Gauge
	// lifecycleRulePresent is 1 while the last lifecycle check found the
	// bucket rule that expires the image prefix, else 0.
	lifecycleRulePresent prometheus.Gauge
	// reconcilerEnabled is 1 when the reconciler runs in this process.
	reconcilerEnabled prometheus.Gauge
}

// imageMetrics is the process-wide instance.
var imageMetrics = newImageObjectMetrics(prometheus.DefaultRegisterer)

// newImageObjectMetrics creates the collectors and registers them on reg.
func newImageObjectMetrics(reg prometheus.Registerer) *imageObjectMetrics {
	m := &imageObjectMetrics{
		dueClaims: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mcp_user_image_claims_due",
			Help: "Image object claims that were due when the last reconciler tick ended.",
		}),
		oldestDueClaimAge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mcp_user_image_claims_oldest_due_age_seconds",
			Help: "How long the oldest due image object claim had been due when the last reconciler tick ended.",
		}),
		objectDeletes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mcp_user_image_object_deletes_total",
			Help: "Image object deletes by path (compensation, reconciler) and result (deleted, failed).",
		}, []string{"path", "result"}),
		reconcilePasses: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mcp_user_image_reconcile_passes_total",
			Help: "Image object reconciler passes by result (ok, error).",
		}, []string{"result"}),
		lastReconcileSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mcp_user_image_reconcile_last_success_timestamp_seconds",
			Help: "Unix time of the last image object reconciler pass that finished without errors.",
		}),
		lifecycleRulePresent: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mcp_user_image_lifecycle_rule_present",
			Help: "1 while the last check found the bucket lifecycle rule that expires image objects, else 0.",
		}),
		reconcilerEnabled: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mcp_user_image_reconciler_enabled",
			Help: "1 when the image object reconciler runs in this process, else 0.",
		}),
	}
	m.objectDeletes.WithLabelValues(deletePathCompensation, deleteResultDeleted)
	m.objectDeletes.WithLabelValues(deletePathCompensation, deleteResultFailed)
	m.objectDeletes.WithLabelValues(deletePathReconciler, deleteResultDeleted)
	m.objectDeletes.WithLabelValues(deletePathReconciler, deleteResultFailed)
	m.reconcilePasses.WithLabelValues("ok")
	m.reconcilePasses.WithLabelValues("error")
	if reg != nil {
		reg.MustRegister(m.dueClaims, m.oldestDueClaimAge, m.objectDeletes, m.reconcilePasses,
			m.lastReconcileSuccess, m.lifecycleRulePresent, m.reconcilerEnabled)
	}
	return m
}

// boolGauge returns 1 for true and 0 for false.
func boolGauge(v bool) float64 {
	if v {
		return 1
	}
	return 0
}
