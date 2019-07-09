package laisky_blog_graphql

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Laisky/zap"

	"github.com/Laisky/go-utils"

	"github.com/Laisky/laisky-blog-graphql/blog"
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

// query
// ===========================

type queryResolver struct{ *Resolver }
type tweetResolver struct{ *Resolver }
type twitterUserResolver struct{ *Resolver }
type blogPostResolver struct{ *Resolver }
type blogUserResolver struct{ *Resolver }

func (t *twitterUserResolver) ID(ctx context.Context, obj *twitter.User) (string, error) {
	return strconv.FormatInt(int64(obj.ID), 10), nil
}
func (t *twitterUserResolver) Description(ctx context.Context, obj *twitter.User) (string, error) {
	return obj.Dscription, nil
}

func (q *queryResolver) Benchmark(ctx context.Context) (string, error) {
	return "hello, world", nil
}

func (q *queryResolver) Tweets(ctx context.Context, page *Pagination, username string, sort *Sort, topic string, regexp string) ([]*twitter.Tweet, error) {
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

func (q *queryResolver) Posts(ctx context.Context, page *Pagination, tag string, category string, length int, name string, regexp string) ([]*blog.Post, error) {
	cfg := &blog.BlogPostCfg{
		Page:     page.Page,
		Size:     page.Size,
		Length:   length,
		Tag:      tag,
		Regexp:   regexp,
		Category: category,
		Name:     name,
	}
	if results, err := blogDB.LoadPosts(cfg); err != nil {
		return nil, err
	} else {
		return results, nil
	}
}

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
	}

	return "", fmt.Errorf("unknown blog post type: `%+v`", obj.Type)
}

func (r *blogUserResolver) ID(ctx context.Context, obj *blog.User) (string, error) {
	return obj.ID.Hex(), nil
}

// mutations
// =========
type mutationResolver struct{ *Resolver }

func (r *mutationResolver) CreateBlogPost(ctx context.Context, input NewBlogPost) (*blog.Post, error) {
	user, err := validateAndGetUser(ctx)
	if err != nil {
		utils.Logger.Debug("user invalidate", zap.Error(err))
		return nil, err
	}

	return blogDB.NewPost(user.ID, string(input.Title), input.Name, string(input.Markdown))
}

func (r *mutationResolver) Login(ctx context.Context, account string, password string) (user *blog.User, err error) {
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

func (r *mutationResolver) AmendBlogPost(ctx context.Context, post NewBlogPost) (*blog.Post, error) {
	user, err := validateAndGetUser(ctx)
	if err != nil {
		utils.Logger.Debug("user invalidate", zap.Error(err))
		return nil, err
	}

	return blogDB.UpdatePost(user, post.Name, string(post.Title), string(post.Markdown), string(post.Type))
}
