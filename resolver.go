package laisky_blog_graphql

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Laisky/zap"

	"github.com/Laisky/go-utils"

	"github.com/Laisky/laisky-blog-graphql/blog"
	"github.com/Laisky/laisky-blog-graphql/telegram"
	"github.com/Laisky/laisky-blog-graphql/twitter"
	"github.com/Laisky/laisky-blog-graphql/types"
	"github.com/pkg/errors"
)

type Resolver struct{}

func (r *Resolver) Query() QueryResolver {
	return &queryResolver{r}
}

func (r *Resolver) Mutation() MutationResolver {
	return &mutationResolver{r}
}

func (r *Resolver) Tweet() TweetResolver {
	return &tweetResolver{r}
}
func (r *Resolver) TwitterUser() TwitterUserResolver {
	return &twitterUserResolver{r}
}
func (r *Resolver) BlogPost() BlogPostResolver {
	return &blogPostResolver{r}
}
func (r *Resolver) BlogUser() BlogUserResolver {
	return &blogUserResolver{r}
}
func (r *Resolver) TelegramAlertType() TelegramAlertTypeResolver {
	return &telegramAlertTypeResolver{r}
}
func (r *Resolver) TelegramUser() TelegramUserResolver {
	return &telegramUserResolver{r}
}

// ===========================
// query
// ===========================

type queryResolver struct{ *Resolver }
type tweetResolver struct{ *Resolver }
type twitterUserResolver struct{ *Resolver }
type blogPostResolver struct{ *Resolver }
type blogUserResolver struct{ *Resolver }
type telegramAlertTypeResolver struct{ *Resolver }
type telegramUserResolver struct{ *Resolver }

// query resolver
// --------------

func (t *twitterUserResolver) ID(ctx context.Context, obj *twitter.User) (string, error) {
	return strconv.FormatInt(int64(obj.ID), 10), nil
}
func (t *twitterUserResolver) Description(ctx context.Context, obj *twitter.User) (string, error) {
	return obj.Dscription, nil
}

func (q *queryResolver) Hello(ctx context.Context) (string, error) {
	return "hello, world", nil
}
func (q *queryResolver) BlogPostInfo(ctx context.Context) (*blog.PostInfo, error) {
	return blogDB.LoadPostInfo()
}
func (q *queryResolver) TwitterStatues(ctx context.Context, page *Pagination, username string, sort *Sort, topic string, regexp string) ([]*twitter.Tweet, error) {
	if results, err := twitterDB.LoadTweets(&twitter.TweetLoadCfg{
		Page:      page.Page,
		Regexp:    regexp,
		Size:      page.Size,
		Username:  username,
		SortBy:    sort.SortBy,
		SortOrder: string(sort.Order),
	}); err != nil {
		return nil, err
	} else {
		return results, nil
	}
}
func (q *queryResolver) BlogPosts(ctx context.Context, page *Pagination, tag string, categoryURL *string, length int, name string, regexp string) ([]*blog.Post, error) {
	cfg := &blog.BlogPostCfg{
		Page:        page.Page,
		Size:        page.Size,
		Length:      length,
		Tag:         tag,
		Regexp:      regexp,
		CategoryURL: categoryURL,
		Name:        name,
	}
	if results, err := blogDB.LoadPosts(cfg); err != nil {
		return nil, err
	} else {
		return results, nil
	}
}
func (q *queryResolver) BlogPostCategories(ctx context.Context) ([]*blog.Category, error) {
	return blogDB.LoadAllCategories()
}
func (q *queryResolver) TelegramMonitorUsers(ctx context.Context, page *Pagination, name string) ([]*telegram.Users, error) {
	cfg := &telegram.TelegramQueryCfg{
		Page: page.Page,
		Size: page.Size,
		Name: name,
	}
	return monitorDB.LoadUsers(cfg)
}
func (q *queryResolver) TelegramAlertTypes(ctx context.Context, page *Pagination, name string) ([]*telegram.AlertTypes, error) {
	cfg := &telegram.TelegramQueryCfg{
		Page: page.Page,
		Size: page.Size,
		Name: name,
	}
	return monitorDB.LoadAlertTypes(cfg)
}

// ----------------
// telegram monitor resolver
// ----------------
func (t *telegramUserResolver) ID(ctx context.Context, obj *telegram.Users) (string, error) {
	return obj.ID.Hex(), nil
}
func (t *telegramUserResolver) CreatedAt(ctx context.Context, obj *telegram.Users) (*types.Datetime, error) {
	return types.NewDatetimeFromTime(obj.CreatedAt), nil
}
func (t *telegramUserResolver) ModifiedAt(ctx context.Context, obj *telegram.Users) (*types.Datetime, error) {
	return types.NewDatetimeFromTime(obj.ModifiedAt), nil
}
func (t *telegramUserResolver) TelegramID(ctx context.Context, obj *telegram.Users) (string, error) {
	return strconv.FormatInt(int64(obj.UID), 10), nil
}
func (t *telegramUserResolver) SubAlerts(ctx context.Context, obj *telegram.Users) ([]*telegram.AlertTypes, error) {
	return monitorDB.LoadAlertTypesByUser(obj)
}

func (t *telegramAlertTypeResolver) ID(ctx context.Context, obj *telegram.AlertTypes) (string, error) {
	return obj.ID.Hex(), nil
}
func (t *telegramAlertTypeResolver) CreatedAt(ctx context.Context, obj *telegram.AlertTypes) (*types.Datetime, error) {
	return types.NewDatetimeFromTime(obj.CreatedAt), nil
}
func (t *telegramAlertTypeResolver) ModifiedAt(ctx context.Context, obj *telegram.AlertTypes) (*types.Datetime, error) {
	return types.NewDatetimeFromTime(obj.ModifiedAt), nil
}
func (t *telegramAlertTypeResolver) SubUsers(ctx context.Context, obj *telegram.AlertTypes) ([]*telegram.Users, error) {
	return monitorDB.LoadUsersByAlertType(obj)
}

// ----------------
// twitter resolver
// ----------------

func (t *tweetResolver) ID(ctx context.Context, obj *twitter.Tweet) (string, error) {
	return strconv.FormatInt(obj.ID, 10), nil
}
func (t *tweetResolver) IsQuoteStatus(ctx context.Context, obj *twitter.Tweet) (bool, error) {
	return obj.IsQuoted, nil
}
func (t *tweetResolver) QuotedStatus(ctx context.Context, obj *twitter.Tweet) (*twitter.Tweet, error) {
	return obj.QuotedTweet, nil
}
func (t *tweetResolver) MongoID(ctx context.Context, obj *twitter.Tweet) (string, error) {
	return obj.MongoID.Hex(), nil
}
func (t *tweetResolver) TweetID(ctx context.Context, obj *twitter.Tweet) (int, error) {
	return int(obj.ID), nil
}
func (t *tweetResolver) CreatedAt(ctx context.Context, obj *twitter.Tweet) (*types.Datetime, error) {
	if obj.CreatedAt == nil {
		return nil, nil
	}

	return types.NewDatetimeFromTime(*obj.CreatedAt), nil
}
func (t *tweetResolver) URL(ctx context.Context, obj *twitter.Tweet) (string, error) {
	if obj.User == nil {
		return "", nil
	}
	return "https://twitter.com/" + obj.User.ScreenName + "/status/" + strconv.FormatInt(obj.ID, 10), nil
}
func (t *tweetResolver) ReplyTo(ctx context.Context, obj *twitter.Tweet) (tweet *twitter.Tweet, err error) {
	if obj.ReplyToStatusID == 0 {
		return nil, nil
	}

	if tweet, err = twitterDB.LoadTweetByTwitterID(obj.ReplyToStatusID); err != nil {
		utils.Logger.Warn("try to load tweet by id got error",
			zap.Int64("tid", obj.ReplyToStatusID),
			zap.Error(err))
		return nil, fmt.Errorf("can not load tweet by tid: %v", obj.ReplyToStatusID)
	}

	return tweet, nil
}

// ----------------
// blog resolver
// ----------------

func (r *blogPostResolver) MongoID(ctx context.Context, obj *blog.Post) (string, error) {
	return obj.ID.Hex(), nil
}
func (r *blogPostResolver) CreatedAt(ctx context.Context, obj *blog.Post) (*types.Datetime, error) {
	return types.NewDatetimeFromTime(obj.CreatedAt), nil
}
func (r *blogPostResolver) ModifiedAt(ctx context.Context, obj *blog.Post) (*types.Datetime, error) {
	return types.NewDatetimeFromTime(obj.ModifiedAt), nil
}
func (r *blogPostResolver) Author(ctx context.Context, obj *blog.Post) (*blog.User, error) {
	return blogDB.LoadUserById(obj.Author)
}
func (r *blogPostResolver) Category(ctx context.Context, obj *blog.Post) (*blog.Category, error) {
	return blogDB.LoadCategoryById(obj.Category)
}
func (r *blogPostResolver) Type(ctx context.Context, obj *blog.Post) (BlogPostType, error) {
	switch obj.Type {
	case "markdown":
		return BlogPostTypeMarkdown, nil
	case "slide":
		return BlogPostTypeSlide, nil
	case "html":
		return BlogPostTypeHTML, nil
	}

	return "", fmt.Errorf("unknown blog post type: `%+v`", obj.Type)
}

func (r *blogUserResolver) ID(ctx context.Context, obj *blog.User) (string, error) {
	return obj.ID.Hex(), nil
}

// =========
// mutations
// =========
type mutationResolver struct{ *Resolver }

func (r *mutationResolver) BlogCreatePost(ctx context.Context, input NewBlogPost) (*blog.Post, error) {
	user, err := validateAndGetUser(ctx)
	if err != nil {
		utils.Logger.Debug("user invalidate", zap.Error(err))
		return nil, err
	}

	return blogDB.NewPost(user.ID, string(input.Title), input.Name, string(input.Markdown), input.Type.String())
}
func (r *mutationResolver) BlogLogin(ctx context.Context, account string, password string) (user *blog.User, err error) {
	if user, err = blogDB.ValidateLogin(account, password); err != nil {
		utils.Logger.Debug("user invalidate", zap.Error(err))
		return nil, err
	}

	if err = auth.SetLoginCookie(ctx, user, nil); err != nil {
		utils.Logger.Error("try to set cookie got error", zap.Error(err))
		return nil, errors.Wrap(err, "try to set cookies got error")
	}

	return user, nil
}
func (r *mutationResolver) BlogAmendPost(ctx context.Context, post NewBlogPost) (*blog.Post, error) {
	user, err := validateAndGetUser(ctx)
	if err != nil {
		utils.Logger.Debug("user invalidate", zap.Error(err))
		return nil, err
	}

	return blogDB.UpdatePost(user, post.Name, string(post.Title), string(post.Markdown), string(post.Type))
}
func (r *mutationResolver) TelegramMonitorAlert(ctx context.Context, typeArg string, token string, msg string) (*telegram.AlertTypes, error) {
	alert, err := monitorDB.ValidateTokenForAlertType(token, typeArg)
	if err != nil {
		return nil, err
	}
	users, err := monitorDB.LoadUsersByAlertType(alert)
	if err != nil {
		return nil, err
	}

	errMsg := ""
	msg = typeArg + " >>>>>>>>>>>>>>>>>> " + "\n" + msg
	for _, user := range users {
		if err = telegramCli.SendMsgToUser(user.UID, msg); err != nil {
			utils.Logger.Error("send msg to user", zap.Error(err), zap.Int("uid", user.UID), zap.String("msg", msg))
			errMsg += err.Error()
		}
	}

	if errMsg != "" {
		err = fmt.Errorf(errMsg)
	}

	return alert, err
}
