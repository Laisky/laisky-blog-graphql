package web

import (
	"context"
	"fmt"
	"time"

	gutils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/form3tech-oss/jwt-go"
	"github.com/pkg/errors"

	"laisky-blog-graphql/internal/global"
	"laisky-blog-graphql/internal/web/blog"
	"laisky-blog-graphql/library"
	"laisky-blog-graphql/library/log"
)

func (r *Resolver) BlogPost() BlogPostResolver {
	return &blogPostResolver{r}
}
func (r *Resolver) BlogUser() BlogUserResolver {
	return &blogUserResolver{r}
}
func (r *Resolver) BlogPostSeries() BlogPostSeriesResolver {
	return &blogPostSeriesResolver{r}
}

type blogPostResolver struct{ *Resolver }
type blogPostSeriesResolver struct{ *Resolver }
type blogUserResolver struct{ *Resolver }

// =====================================
// query resolver
// =====================================

func (r *queryResolver) BlogPostInfo(ctx context.Context) (*blog.PostInfo, error) {
	return global.BlogSvc.LoadPostInfo()
}

func (r *queryResolver) GetBlogPostSeries(ctx context.Context,
	page *Pagination,
	key string,
) ([]*blog.PostSeries, error) {
	se, err := global.BlogSvc.LoadPostSeries("", key)
	if err != nil {
		return nil, err
	}

	return se, nil
}

func (r *queryResolver) BlogPosts(ctx context.Context,
	page *Pagination,
	tag string,
	categoryURL *string,
	length int,
	name string,
	regexp string,
) ([]*blog.Post, error) {
	cfg := &blog.PostCfg{
		Page:        page.Page,
		Size:        page.Size,
		Length:      length,
		Tag:         tag,
		Regexp:      regexp,
		CategoryURL: categoryURL,
		Name:        name,
	}
	results, err := global.BlogSvc.LoadPosts(cfg)
	if err != nil {
		return nil, err
	}

	return results, nil
}
func (r *queryResolver) BlogPostCategories(ctx context.Context) ([]*blog.Category, error) {
	return global.BlogSvc.LoadAllCategories()
}

// ----------------
// blog resolver
// ----------------

func (r *blogPostResolver) ID(ctx context.Context, obj *blog.Post) (string, error) {
	return obj.ID.Hex(), nil
}
func (r *blogPostResolver) CreatedAt(ctx context.Context, obj *blog.Post) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.CreatedAt), nil
}
func (r *blogPostResolver) ModifiedAt(ctx context.Context, obj *blog.Post) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.ModifiedAt), nil
}
func (r *blogPostResolver) Author(ctx context.Context, obj *blog.Post) (*blog.User, error) {
	return global.BlogSvc.LoadUserByID(obj.Author)
}
func (r *blogPostResolver) Category(ctx context.Context, obj *blog.Post) (*blog.Category, error) {
	return global.BlogSvc.LoadCategoryByID(obj.Category)
}
func (r *blogPostResolver) Type(ctx context.Context, obj *blog.Post) (BlogPostType, error) {
	switch obj.Type {
	case BlogPostTypeMarkdown.String():
		return BlogPostTypeMarkdown, nil
	case BlogPostTypeSlide.String():
		return BlogPostTypeSlide, nil
	case BlogPostTypeHTML.String():
		return BlogPostTypeHTML, nil
	}

	return "", fmt.Errorf("unknown blog post type: `%+v`", obj.Type)
}

func (r *blogPostSeriesResolver) Posts(ctx context.Context, obj *blog.PostSeries) (posts []*blog.Post, err error) {
	se, err := global.BlogSvc.LoadPostSeries(obj.ID, "")
	if err != nil {
		return nil, err
	}

	if len(se) == 0 {
		return nil, errors.Errorf("notfound")
	}

	for _, postID := range se[0].Posts {
		ps, err := global.BlogSvc.LoadPosts(&blog.PostCfg{ID: postID})
		if err != nil {
			log.Logger.Error("load posts", zap.Error(err), zap.String("id", postID.Hex()))
			continue
		}

		posts = append(posts, ps...)
	}

	return posts, nil
}
func (r *blogPostSeriesResolver) Children(ctx context.Context,
	obj *blog.PostSeries) ([]*blog.PostSeries, error) {
	var ss []*blog.PostSeries
	for _, sid := range obj.Chidlren {
		se, err := global.BlogSvc.LoadPostSeries(sid, "")
		if err != nil {
			return nil, errors.Wrapf(err, "load post series `%s`", sid.Hex())
		}

		ss = append(ss, se...)
	}

	return ss, nil
}

func (r *blogUserResolver) ID(ctx context.Context,
	obj *blog.User) (string, error) {
	return obj.ID.Hex(), nil
}

// =====================================
// mutations
// =====================================

// BlogCreatePost create new blog post
func (r *mutationResolver) BlogCreatePost(ctx context.Context,
	input NewBlogPost) (*blog.Post, error) {
	user, err := validateAndGetUser(ctx)
	if err != nil {
		log.Logger.Debug("user invalidate", zap.Error(err))
		return nil, err
	}

	if input.Title == nil ||
		input.Markdown == nil {
		return nil, fmt.Errorf("title & markdown must set")
	}

	return global.BlogSvc.NewPost(user.ID,
		*input.Title,
		input.Name,
		*input.Markdown,
		input.Type.String())
}

// BlogLogin login in blog page
func (r *mutationResolver) BlogLogin(ctx context.Context,
	account string,
	password string,
) (user *blog.User, err error) {
	if user, err = global.BlogSvc.ValidateLogin(account, password); err != nil {
		log.Logger.Debug("user invalidate", zap.Error(err))
		return nil, err
	}

	uc := &blog.UserClaims{
		StandardClaims: jwt.StandardClaims{
			Subject:   user.ID.Hex(),
			IssuedAt:  gutils.Clock2.GetUTCNow().Unix(),
			ExpiresAt: gutils.Clock.GetUTCNow().Add(7 * 24 * time.Hour).Unix(),
		},
		Username:    user.Account,
		DisplayName: user.Username,
	}

	if err = auth.SetLoginCookie(ctx, uc); err != nil {
		log.Logger.Error("try to set cookie got error", zap.Error(err))
		return nil, errors.Wrap(err, "try to set cookies got error")
	}

	return user, nil
}

func (r *mutationResolver) BlogAmendPost(ctx context.Context,
	post NewBlogPost) (*blog.Post, error) {
	user, err := validateAndGetUser(ctx)
	if err != nil {
		log.Logger.Debug("user invalidate", zap.Error(err))
		return nil, err
	}

	if post.Name == "" {
		return nil, fmt.Errorf("title & name cannot be empty")
	}

	// only update category
	if post.Category != nil {
		return global.BlogSvc.UpdatePostCategory(post.Name, *post.Category)
	}

	if post.Title == nil ||
		post.Markdown == nil ||
		post.Type == nil {
		return nil, fmt.Errorf("title & markdown & type must set")
	}

	// update post content
	return global.BlogSvc.UpdatePost(user,
		post.Name,
		*post.Title,
		*post.Markdown,
		post.Type.String())
}
