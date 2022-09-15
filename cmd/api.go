package cmd

import (
	"context"

	"github.com/Laisky/laisky-blog-graphql/internal/web"
	"github.com/Laisky/laisky-blog-graphql/library/log"

	gconfig "github.com/Laisky/go-config"
	gcmd "github.com/Laisky/go-utils/v2/cmd"
	"github.com/Laisky/zap"
	"github.com/spf13/cobra"
)

var apiCMD = &cobra.Command{
	Use:   "api",
	Short: "api",
	Long:  `graphql API service for laisky`,
	Args:  gcmd.NoExtraArgs,
	PreRun: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		if err := initialize(ctx, cmd); err != nil {
			log.Logger.Panic("init", zap.Error(err))
		}
	},
	Run: func(cmd *cobra.Command, args []string) {
		web.RunServer(gconfig.Shared.GetString("listen"))
	},
}

func init() {
	rootCMD.AddCommand(apiCMD)
}
