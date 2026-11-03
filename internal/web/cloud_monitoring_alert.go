package web

import (
	ginMw "github.com/Laisky/gin-middlewares/v7"
	"github.com/gin-gonic/gin"

	telegram "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/controller"
)

// cloudMonitoringAlertPath is the webhook path configured on Cloud Monitoring channels.
const cloudMonitoringAlertPath = "/alerts/cloud-monitoring"

// registerCloudMonitoringAlertRoutes mounts the Cloud Monitoring webhook when the
// Telegram service exists. A deployment without it exposes no route, so a
// misrouted notification fails visibly instead of being accepted and dropped.
func registerCloudMonitoringAlertRoutes(engine *gin.Engine, prefix urlPrefixConfig, resolver *Resolver) {
	if resolver == nil || resolver.args.TelegramSvc == nil {
		return
	}
	handler := ginMw.FromStd(telegram.NewCloudMonitoringWebhook(resolver.args.TelegramSvc).ServeHTTP)
	for _, path := range ssoRoutePaths(prefix, cloudMonitoringAlertPath) {
		engine.POST(path, handler)
	}
}
