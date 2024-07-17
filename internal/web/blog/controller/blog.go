// Package controller is the resolver of graphql.
package controller

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	ginMw "github.com/Laisky/gin-middlewares/v5"
	gutils "github.com/Laisky/go-utils/v4"
	"github.com/Laisky/zap"
	jwtLib "github.com/golang-jwt/jwt/v4"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/Laisky/laisky-blog-graphql/internal/library/models"
	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/dto"
	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/service"
	"github.com/Laisky/laisky-blog-graphql/library"
	"github.com/Laisky/laisky-blog-graphql/library/auth"
	"github.com/Laisky/laisky-blog-graphql/library/jwt"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// PostResolver post resolver
type PostResolver struct {
	svc *service.Blog
}

type ArweaveItemResolver struct {
}

// PostSeriesResolver post series resolver
type PostSeriesResolver struct {
	svc *service.Blog
}

// UserResolver user resolver
type UserResolver struct {
	svc *service.Blog
}

// QueryResolver query resolver
type QueryResolver struct {
	svc *service.Blog
}

// MutationResolver mutation resolver
type MutationResolver struct {
	svc *service.Blog
}

// NewQueryResolver new query resolver
func NewQueryResolver(svc *service.Blog) QueryResolver {
	return QueryResolver{
		svc: svc,
	}
}

// NewMutationResolver new mutation resolver
func NewMutationResolver(
	svc *service.Blog,
) MutationResolver {
	return MutationResolver{
		svc: svc,
	}
}

// Blog blog resolver
type Blog struct {
	PostResolver        *PostResolver
	PostSeriesResolver  *PostSeriesResolver
	UserResolver        *UserResolver
	ArweaveItemResolver *ArweaveItemResolver
}

func New(svc *service.Blog) *Blog {
	return &Blog{
		PostResolver: &PostResolver{
			svc: svc,
		},
		PostSeriesResolver: &PostSeriesResolver{
			svc: svc,
		},
		UserResolver: &UserResolver{
			svc: svc,
		},
	}
}

// =====================================
// query resolver
// =====================================

func (r *QueryResolver) BlogPostInfo(ctx context.Context) (*dto.PostInfo, error) {
	return r.svc.LoadPostInfo(ctx)
}

func (r *QueryResolver) WhoAmI(ctx context.Context) (*model.User, error) {
	user, err := r.svc.ValidateAndGetUser(ctx)
	if err != nil {
		log.Logger.Debug("user invalidate", zap.Error(err))
		return nil, err
	}

	return user, nil
}

func (r *QueryResolver) GetBlogPostSeries(ctx context.Context,
	page *models.Pagination,
	key string,
) ([]*model.PostSeries, error) {
	se, err := r.svc.LoadPostSeries(ctx, primitive.NilObjectID, key)
	if err != nil {
		return nil, err
	}

	return se, nil
}

func (r *QueryResolver) BlogTags(ctx context.Context) ([]string, error) {
	return r.svc.LoadPostTags(ctx)
}

func (r *QueryResolver) BlogPosts(ctx context.Context,
	page *models.Pagination,
	tag string,
	categoryURL *string,
	length int,
	name string,
	regexp string,
	language models.Language,
) ([]*model.Post, error) {
	cfg := &dto.PostCfg{
		Page:        page.Page,
		Size:        page.Size,
		Length:      length,
		Tag:         tag,
		Regexp:      regexp,
		CategoryURL: categoryURL,
		Name:        name,
		Language:    language,
	}
	results, err := r.svc.LoadPosts(ctx, cfg)
	if err != nil {
		return nil, err
	}

	return results, nil
}
func (r *QueryResolver) BlogPostCategories(ctx context.Context) ([]*model.Category, error) {
	return r.svc.LoadAllCategories(ctx)
}

var (
	markdownImgRe = regexp.MustCompile(`!\[[^\]]*\]\((.*)\)`)
)

func (r *QueryResolver) BlogTwitterCard(ctx context.Context,
	name string, language models.Language) (string, error) {
	name = strings.Trim(name, " /")
	posts, err := r.svc.LoadPosts(ctx, &dto.PostCfg{
		Name:     name,
		Language: language,
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

func (r *PostResolver) ID(ctx context.Context,
	obj *model.Post) (string, error) {
	return obj.ID.Hex(), nil
}
func (r *PostResolver) CreatedAt(ctx context.Context,
	obj *model.Post) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.CreatedAt), nil
}
func (r *PostResolver) ModifiedAt(ctx context.Context,
	obj *model.Post) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.ModifiedAt), nil
}
func (r *PostResolver) Author(ctx context.Context,
	obj *model.Post) (*model.User, error) {
	return r.svc.LoadUserByID(ctx, obj.Author)
}
func (r *PostResolver) Category(ctx context.Context,
	obj *model.Post) (*model.Category, error) {
	return r.svc.LoadCategoryByID(ctx, obj.Category)
}
func (r *PostResolver) Type(ctx context.Context,
	obj *model.Post) (models.BlogPostType, error) {
	switch obj.Type {
	case models.BlogPostTypeMarkdown.String():
		return models.BlogPostTypeMarkdown, nil
	case models.BlogPostTypeSlide.String():
		return models.BlogPostTypeSlide, nil
	case models.BlogPostTypeHTML.String():
		return models.BlogPostTypeHTML, nil
	}

	return "", errors.Errorf("unknown blog post type: `%+v`", obj.Type)
}
func (r *PostResolver) Language(ctx context.Context,
	obj *model.Post) (models.Language, error) {
	if gutils.Contains(models.AllLanguage, models.Language(obj.Language)) {
		return models.Language(obj.Language), nil
	}

	return "", errors.Errorf("unknown language: %q", obj.Language)
}
func (r *PostResolver) AllLanguages(ctx context.Context,
	obj *model.Post) (allLangs []models.Language, err error) {
	allLangs = append(allLangs, models.LanguageZhCn)
	if obj.I18N.EnUs.PostMarkdown != "" {
		allLangs = append(allLangs, models.LanguageEnUs)
	}

	return allLangs, nil
}
func (r *ArweaveItemResolver) Time(ctx context.Context, obj *model.ArweaveHistoryItem) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.Time), nil
}

func (r *PostSeriesResolver) Posts(ctx context.Context,
	obj *model.PostSeries) (posts []*model.Post, err error) {
	se, err := r.svc.LoadPostSeries(ctx, obj.ID, "")
	if err != nil {
		return nil, err
	}

	if len(se) == 0 {
		return nil, errors.Errorf("notfound")
	}

	for _, postID := range se[0].Posts {
		ps, err := r.svc.LoadPosts(ctx, &dto.PostCfg{ID: postID})
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
		se, err := r.svc.LoadPostSeries(ctx, sid, "")
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

// UserLogin login user
func (r *MutationResolver) UserLogin(ctx context.Context,
	account string, password string) (*models.BlogLoginResponse, error) {
	return r.BlogLogin(ctx, account, password)
}

// UserRegister register user
func (r *MutationResolver) UserRegister(ctx context.Context,
	account string, password string, displayName string, captcha string) (
	*models.UserRegisterResponse, error) {
	_, err := r.svc.UserRegister(ctx, account, password, displayName)
	if err != nil {
		return nil, errors.Wrap(err, "register user")
	}

	return &models.UserRegisterResponse{
		Msg: fmt.Sprintf("check your email `%s` to active your account", account),
	}, nil
}

func (r *MutationResolver) UserActive(ctx context.Context, token string) (*models.UserActiveResponse, error) {
	// FIXME
	return nil, errors.Errorf("notimplement")
}

func (r *MutationResolver) UserResendActiveEmail(ctx context.Context,
	account string) (*models.UserResendActiveEmailResponse, error) {
	// FIXME
	return nil, errors.Errorf("notimplement")
}

// BlogCreatePost create new blog post
func (r *MutationResolver) BlogCreatePost(ctx context.Context,
	newpost models.NewBlogPost,
	language models.Language,
) (*model.Post, error) {
	user, err := r.svc.ValidateAndGetUser(ctx)
	if err != nil {
		log.Logger.Debug("user invalidate", zap.Error(err))
		return nil, err
	}

	newpost.Language = language
	if newpost.Title == nil ||
		newpost.Markdown == nil {
		return nil, errors.Errorf("title & markdown must set")
	}

	return r.svc.NewPost(ctx,
		user.ID,
		*newpost.Title,
		newpost.Name,
		*newpost.Markdown,
		newpost.Type.String())
}

// BlogLogin login in blog page
func (r *MutationResolver) BlogLogin(ctx context.Context,
	account string,
	password string,
) (resp *models.BlogLoginResponse, err error) {
	var user *model.User
	if user, err = r.svc.ValidateLogin(ctx, account, password); err != nil {
		return nil, errors.Wrapf(err, "user `%v` invalidate", account)
	}

	uc := &jwt.UserClaims{
		RegisteredClaims: jwtLib.RegisteredClaims{
			ID:        gutils.UUID7(),
			Subject:   user.ID.Hex(),
			Issuer:    "laisky-sso",
			IssuedAt:  jwtLib.NewNumericDate(gutils.Clock.GetUTCNow()),
			ExpiresAt: jwtLib.NewNumericDate(gutils.Clock.GetUTCNow().Add(3 * 30 * 24 * time.Hour)),
		},
		Username:    user.Account,
		DisplayName: user.Username,
	}

	var token string
	if token, err = auth.Instance.SetAuthHeader(ctx,
		ginMw.WithSetAuthHeaderClaim(uc),
	); err != nil {
		log.Logger.Error("try to set cookie got error", zap.Error(err))
		return nil, errors.Wrap(err, "try to set cookies got error")
	}

	return &models.BlogLoginResponse{
		User:  user,
		Token: token,
	}, nil
}

func (r *MutationResolver) BlogAmendPost(ctx context.Context,
	post models.NewBlogPost,
	language models.Language,
) (*model.Post, error) {
	user, err := r.svc.ValidateAndGetUser(ctx)
	if err != nil {
		log.Logger.Debug("user invalidate", zap.Error(err))
		return nil, err
	}

	post.Language = language
	if post.Name == "" {
		return nil, errors.Errorf("title & name cannot be empty")
	}

	// only update category
	if post.Category != nil {
		return r.svc.UpdatePostCategory(ctx, post.Name, *post.Category)
	}

	if post.Title == nil ||
		post.Markdown == nil ||
		post.Type == nil {
		return nil, errors.Errorf("title & markdown & type must set")
	}

	// update post content
	return r.svc.UpdatePost(ctx,
		user,
		post.Name,
		*post.Title,
		*post.Markdown,
		post.Type.String(),
		post.Language,
	)
}
