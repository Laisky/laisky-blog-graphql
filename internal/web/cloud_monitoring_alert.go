package web

import (
	ginMw "github.com/Laisky/gin-middlewares/v7"
	"github.com/gin-gonic/gin"

	telegram "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/controller"
	telegramSvc "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/service"
)

// cloudMonitoringAlertPath is the webhook path configured on Cloud Monitoring
// channels. Inbound third-party webhooks live under /webhook/<provider>/.
const cloudMonitoringAlertPath = "/webhook/gcp/alerts/cloud-monitoring"

// registerCloudMonitoringAlertRoutes mounts the Cloud Monitoring webhook. It is
// mounted even when the Telegram service did not start, and then answers 503,
// like the GraphQL alert mutation, so the sender sees a retryable outage rather
// than a missing route.
func registerCloudMonitoringAlertRoutes(engine *gin.Engine, prefix urlPrefixConfig, resolver *Resolver) {
	// A nil *Telegram must stay a nil interface, or the handler would call it.
	var svc telegramSvc.Interface
	if resolver != nil && resolver.args.TelegramSvc != nil {
		svc = resolver.args.TelegramSvc
	}
	handler := ginMw.FromStd(telegram.NewCloudMonitoringWebhook(svc).ServeHTTP)
	for _, path := range ssoRoutePaths(prefix, cloudMonitoringAlertPath) {
		engine.POST(path, handler)
	}
}
