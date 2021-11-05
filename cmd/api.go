package cmd

import (
	"context"

	"laisky-blog-graphql/internal/web"
	"laisky-blog-graphql/library/log"

	gutils "github.com/Laisky/go-utils"
	gcmd "github.com/Laisky/go-utils/cmd"
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
		web.RunServer(gutils.Settings.GetString("listen"))
	},
}

func init() {
	rootCMD.AddCommand(apiCMD)
}
