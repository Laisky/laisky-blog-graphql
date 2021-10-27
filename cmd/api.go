package cmd

import (
	"context"

	"laisky-blog-graphql/internal/web"

	gcmd "github.com/Laisky/go-utils/cmd"
	"github.com/spf13/cobra"
)

var apiCMD = &cobra.Command{
	Use:   "api",
	Short: "api",
	Long:  `graphql API service for laisky`,
	Args:  gcmd.NoExtraArgs,
	PreRun: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		initialize(ctx, cmd)
	},
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		c := web.NewControllor()
		c.Run(ctx)
	},
}

func init() {
	rootCMD.AddCommand(apiCMD)
}
