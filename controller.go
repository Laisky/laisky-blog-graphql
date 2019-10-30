package laisky_blog_graphql

import (
	"context"

	"github.com/Laisky/laisky-blog-graphql/telegram"
	"github.com/Laisky/zap"

	utils "github.com/Laisky/go-utils"
)

var (
	telegramCli *telegram.Telegram
)

func setupTasks(ctx context.Context) {
	var err error
	for _, task := range utils.Settings.GetStringSlice("tasks") {
		switch task {
		case "telegram":
			utils.Logger.Info("enable telegram")
			if telegramCli, err = telegram.NewTelegram(
				ctx,
				monitorDB,
				utils.Settings.GetString("settings.telegram.token"),
				utils.Settings.GetString("settings.telegram.api"),
			); err != nil {
				utils.Logger.Panic("new telegram", zap.Error(err))
			}
		default:
			utils.Logger.Panic("unknown task", zap.String("task", task))
		}
	}
}

type Controllor struct {
}

func NewControllor() *Controllor {
	return &Controllor{}
}

func (c *Controllor) Run(ctx context.Context) {
	setupDB(ctx)
	setupTasks(ctx)
	RunServer(utils.Settings.GetString("addr"))
}
