package controller

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"laisky-blog-graphql/internal/global"
	"laisky-blog-graphql/internal/web/blog/dto"
	"laisky-blog-graphql/internal/web/blog/model"
	"laisky-blog-graphql/internal/web/blog/service"
	"laisky-blog-graphql/library"
	"laisky-blog-graphql/library/auth"
	"laisky-blog-graphql/library/jwt"
	"laisky-blog-graphql/library/log"

	ginMw "github.com/Laisky/gin-middlewares"
	gutils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	jwtLib "github.com/golang-jwt/jwt/v4"
	"github.com/pkg/errors"
)

type PostResolver struct{}
type PostSeriesResolver struct{}
type UserResolver struct{}

type QueryResolver struct{}
type MutationResolver struct{}

type Type struct {
	PostResolver       *PostResolver
	PostSeriesResolver *PostSeriesResolver
	UserResolver       *UserResolver
}

func New() *Type {
	return &Type{
		PostResolver:       new(PostResolver),
		PostSeriesResolver: new(PostSeriesResolver),
		UserResolver:       new(UserResolver),
	}
}

var Instance *Type

func Initialize(ctx context.Context) {
	service.Initialize(ctx)
	Instance = New()
}

// =====================================
// query resolver
// =====================================

func (r *QueryResolver) BlogPostInfo(ctx context.Context) (*dto.PostInfo, error) {
	return service.Instance.LoadPostInfo()
}

func (r *QueryResolver) GetBlogPostSeries(ctx context.Context,
	page *global.Pagination,
	key string,
) ([]*model.PostSeries, error) {
	se, err := service.Instance.LoadPostSeries("", key)
	if err != nil {
		return nil, err
	}

	return se, nil
}

func (r *QueryResolver) BlogPosts(ctx context.Context,
	page *global.Pagination,
	tag string,
	categoryURL *string,
	length int,
	name string,
	regexp string,
) ([]*model.Post, error) {
	cfg := &dto.PostCfg{
		Page:        page.Page,
		Size:        page.Size,
		Length:      length,
		Tag:         tag,
		Regexp:      regexp,
		CategoryURL: categoryURL,
		Name:        name,
	}
	results, err := service.Instance.LoadPosts(cfg)
	if err != nil {
		return nil, err
	}

	return results, nil
}
func (r *QueryResolver) BlogPostCategories(ctx context.Context) ([]*model.Category, error) {
	return service.Instance.LoadAllCategories()
}

var (
	markdownImgRe = regexp.MustCompile(`!\[[^\]]*\]\((.*)\)`)
)

func (r *QueryResolver) BlogTwitterCard(ctx context.Context, name string) (string, error) {
	posts, err := service.Instance.LoadPosts(&dto.PostCfg{
		Name: name,
	})
	if err != nil {
		return "", errors.Wrapf(err, "load posts `%s`", name)
	}
	if len(posts) == 0 {
		return "", errors.Errorf("notfound `%s`", name)
	}

	p := posts[0]

	// find image
	var imgURL string
	{
		matched := markdownImgRe.FindStringSubmatch(p.Markdown)
		if len(matched) == 2 {
			imgURL = matched[1]
		}
	}

	// get description
	// var desc string
	// if len(p.Markdown) > 100 {
	// 	desc = p.Markdown[:100]
	// } else {
	// 	desc = p.Markdown
	// }

	return fmt.Sprintf(`
<meta name="twitter:card" content="summary_large_image">
<meta name="twitter:title" content="%s">
<meta name="twitter:image" content="%s">
<meta name="twitter:site" content="https://blog.laisky.com/p/%s/">
`, p.Title, imgURL, name), nil
}

// ----------------
// blog resolver
// ----------------

func (r *PostResolver) ID(ctx context.Context, obj *model.Post) (string, error) {
	return obj.ID.Hex(), nil
}
func (r *PostResolver) CreatedAt(ctx context.Context, obj *model.Post) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.CreatedAt), nil
}
func (r *PostResolver) ModifiedAt(ctx context.Context, obj *model.Post) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.ModifiedAt), nil
}
func (r *PostResolver) Author(ctx context.Context, obj *model.Post) (*model.User, error) {
	return service.Instance.LoadUserByID(obj.Author)
}
func (r *PostResolver) Category(ctx context.Context, obj *model.Post) (*model.Category, error) {
	return service.Instance.LoadCategoryByID(obj.Category)
}
func (r *PostResolver) Type(ctx context.Context, obj *model.Post) (global.BlogPostType, error) {
	switch obj.Type {
	case global.BlogPostTypeMarkdown.String():
		return global.BlogPostTypeMarkdown, nil
	case global.BlogPostTypeSlide.String():
		return global.BlogPostTypeSlide, nil
	case global.BlogPostTypeHTML.String():
		return global.BlogPostTypeHTML, nil
	}

	return "", fmt.Errorf("unknown blog post type: `%+v`", obj.Type)
}

func (r *PostSeriesResolver) Posts(ctx context.Context, obj *model.PostSeries) (posts []*model.Post, err error) {
	se, err := service.Instance.LoadPostSeries(obj.ID, "")
	if err != nil {
		return nil, err
	}

	if len(se) == 0 {
		return nil, errors.Errorf("notfound")
	}

	for _, postID := range se[0].Posts {
		ps, err := service.Instance.LoadPosts(&dto.PostCfg{ID: postID})
		if err != nil {
			log.Logger.Error("load posts", zap.Error(err), zap.String("id", postID.Hex()))
			continue
		}

		posts = append(posts, ps...)
	}

	return posts, nil
}
func (r *PostSeriesResolver) Children(ctx context.Context,
	obj *model.PostSeries) ([]*model.PostSeries, error) {
	var ss []*model.PostSeries
	for _, sid := range obj.Chidlren {
		se, err := service.Instance.LoadPostSeries(sid, "")
		if err != nil {
			return nil, errors.Wrapf(err, "load post series `%s`", sid.Hex())
		}

		ss = append(ss, se...)
	}

	return ss, nil
}

func (r *UserResolver) ID(ctx context.Context,
	obj *model.User) (string, error) {
	return obj.ID.Hex(), nil
}

// =====================================
// mutations
// =====================================

// BlogCreatePost create new blog post
func (r *MutationResolver) BlogCreatePost(ctx context.Context,
	input global.NewBlogPost) (*model.Post, error) {
	user, err := service.Instance.ValidateAndGetUser(ctx)
	if err != nil {
		log.Logger.Debug("user invalidate", zap.Error(err))
		return nil, err
	}

	if input.Title == nil ||
		input.Markdown == nil {
		return nil, fmt.Errorf("title & markdown must set")
	}

	return service.Instance.NewPost(user.ID,
		*input.Title,
		input.Name,
		*input.Markdown,
		input.Type.String())
}

// BlogLogin login in blog page
func (r *MutationResolver) BlogLogin(ctx context.Context,
	account string,
	password string,
) (resp *global.BlogLoginResponse, err error) {
	var user *model.User
	if user, err = service.Instance.ValidateLogin(account, password); err != nil {
		log.Logger.Debug("user invalidate", zap.Error(err))
		return nil, err
	}

	uc := &jwt.UserClaims{
		RegisteredClaims: jwtLib.RegisteredClaims{
			Subject: user.ID.Hex(),
			IssuedAt: &jwtLib.NumericDate{
				Time: gutils.Clock.GetUTCNow(),
			},
			ExpiresAt: &jwtLib.NumericDate{
				Time: gutils.Clock.GetUTCNow().Add(7 * 24 * time.Hour),
			},
		},
		Username:    user.Account,
		DisplayName: user.Username,
	}

	var token string
	if token, err = auth.Instance.SetLoginCookiev2(ctx, ginMw.WithAuthClaims(uc)); err != nil {
		log.Logger.Error("try to set cookie got error", zap.Error(err))
		return nil, errors.Wrap(err, "try to set cookies got error")
	}

	return &global.BlogLoginResponse{
		User:  user,
		Token: token,
	}, nil
}

func (r *MutationResolver) BlogAmendPost(ctx context.Context,
	post global.NewBlogPost) (*model.Post, error) {
	user, err := service.Instance.ValidateAndGetUser(ctx)
	if err != nil {
		log.Logger.Debug("user invalidate", zap.Error(err))
		return nil, err
	}

	if post.Name == "" {
		return nil, fmt.Errorf("title & name cannot be empty")
	}

	// only update category
	if post.Category != nil {
		return service.Instance.UpdatePostCategory(post.Name, *post.Category)
	}

	if post.Title == nil ||
		post.Markdown == nil ||
		post.Type == nil {
		return nil, fmt.Errorf("title & markdown & type must set")
	}

	// update post content
	return service.Instance.UpdatePost(user,
		post.Name,
		*post.Title,
		*post.Markdown,
		post.Type.String())
}
