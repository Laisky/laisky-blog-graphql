package web

import (
	"context"

	"laisky-blog-graphql/internal/global"
	"laisky-blog-graphql/internal/web/general"

	gutils "github.com/Laisky/go-utils"
)

func setupSvcs(ctx context.Context) {
	general.Initialize()
}

type Controllor struct {
}

func NewControllor() *Controllor {
	return &Controllor{}
}

func (c *Controllor) Run(ctx context.Context) {
	global.SetupDB(ctx)
	setupSvcs(ctx)

	RunServer(gutils.Settings.GetString("listen"))
}
