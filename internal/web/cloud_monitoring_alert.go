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
	handler := ginMw.FromStd(cloudMonitoringWebhook(resolver).ServeHTTP)
	for _, path := range ssoRoutePaths(prefix, cloudMonitoringAlertPath) {
		engine.POST(path, handler)
	}
}

// cloudMonitoringWebhook builds the webhook from the GraphQL alert mutation's
// resolver when there is one, so both entrypoints share one credential
// directory: one copy, one read per interval and one view of rotated tokens.
func cloudMonitoringWebhook(resolver *Resolver) *telegram.CloudMonitoringWebhook {
	if resolver == nil {
		return telegram.NewCloudMonitoringWebhook(nil)
	}
	if resolver.mutationResolver != nil {
		if mutation := resolver.mutationResolver.telegramMutation.MutationResolver; mutation != nil {
			return mutation.CloudMonitoringWebhook()
		}
	}
	var svc telegramSvc.Interface
	if resolver.args.TelegramSvc != nil {
		svc = resolver.args.TelegramSvc
	}
	return telegram.NewCloudMonitoringWebhook(svc)
}
