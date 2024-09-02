package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v5"
	gutils "github.com/Laisky/go-utils/v4"
	"github.com/Laisky/zap"
	tb "gopkg.in/telebot.v3"
)

func (s *Type) notesSearchHandler() {
	s.bot.Handle("/notes_search", func(c tb.Context) error {
		m := c.Message()
		s.userStats.Store(m.Sender.ID, &userStat{
			user:  m.Sender,
			state: userWaitNotesSearchCmd,
			lastT: gutils.Clock.GetUTCNow(),
		})

		if _, err := s.bot.Send(m.Sender, gutils.Dedent(`
			Reply keyword to search notes, do not contain any blank space, regex is supported.

			For more info, check this doc: https://t.me/laiskynotes/298
			`)); err != nil {
			return errors.Wrap(err, "send msg")
		}

		return nil
	})
}

func (s *Type) notesSearchDispatcher(ctx context.Context, us *userStat, msg *tb.Message) {
	logger := gmw.GetLogger(ctx).With(
		zap.String("user", us.user.Username),
		zap.String("msg", msg.Text),
	)
	logger.Debug("choose notes_search cmd")
	defer s.userStats.Delete(us.user.ID)

	keyword := strings.TrimSpace(msg.Text)
	if keyword == "" {
		s.PleaseRetry(us.user, msg.Text)
		return
	}

	if err := s.notesSearchByKeyword(ctx, us, keyword); err != nil {
		logger.Error("notes search by keyword", zap.Error(err))
	}
}

const noteSummaryLen = 140

func (s *Type) notesSearchByKeyword(ctx context.Context, us *userStat, msg string) error {
	keyword := strings.TrimSpace(msg)
	if keyword == "" {
		return errors.New("keyword is empty")
	}

	notes, err := s.telegramDao.Search(ctx, keyword)
	if err != nil {
		return errors.Wrap(err, "search notes")
	}

	if len(notes) == 0 {
		if _, err = s.bot.Send(us.user, "No notes found"); err != nil {
			return errors.Wrap(err, "send msg")
		}
		return nil
	}

	var resp string
	for _, note := range notes {
		summary := strings.ReplaceAll(note.Content, "\n", " ")
		if len([]rune(summary)) > noteSummaryLen {
			summary = string([]rune(summary)[:noteSummaryLen]) + "..."
		}

		resp += fmt.Sprintf(gutils.Dedent(`
		-------------------------------------
		url: https://t.me/laiskynotes/%d
		content: %s`), note.PostID, summary)
		resp += "\n"
	}

	resp += "-------------------------------------"

	if _, err = s.bot.Send(us.user, resp); err != nil {
		return errors.Wrap(err, "send msg")
	}

	return nil
}
