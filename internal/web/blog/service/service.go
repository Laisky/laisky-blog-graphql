package service

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Laisky/errors"
	gconfig "github.com/Laisky/go-config/v2"
	"github.com/Laisky/go-utils/v2/encrypt"
	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/dao"
	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/dto"
	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
	"github.com/Laisky/laisky-blog-graphql/library/auth"
	mongoSDK "github.com/Laisky/laisky-blog-graphql/library/db/mongo"
	"github.com/Laisky/laisky-blog-graphql/library/jwt"
	"github.com/Laisky/laisky-blog-graphql/library/log"
	"github.com/Laisky/zap"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var Instance *Type

func Initialize(ctx context.Context) {
	dao.Initialize(ctx)
	Instance = New(dao.Instance)
}

type Type struct {
	dao *dao.Type
}

func New(dao *dao.Type) *Type {
	return &Type{dao: dao}
}

func (s *Type) LoadPostSeries(ctx context.Context, id primitive.ObjectID, key string) (se []*model.PostSeries, err error) {
	query := bson.D{}
	if id.IsZero() {
		query = append(query, bson.E{Key: "_id", Value: id})
	}

	if key != "" {
		query = append(query, bson.E{Key: "key", Value: key})
	}

	se = []*model.PostSeries{}
	cur, err := s.dao.GetPostSeriesCol().Find(ctx, query)
	if err != nil {
		return nil, errors.Wrap(err, "find series")
	}
	defer cur.Close(ctx)

	if err = cur.All(ctx, &se); err != nil {
		return nil, errors.Wrap(err, "load series")
	}

	return
}

func (s *Type) LoadPosts(ctx context.Context, cfg *dto.PostCfg) (results []*model.Post, err error) {
	logger := log.Logger.With(
		zap.Int("page", cfg.Page), zap.Int("size", cfg.Size),
		zap.String("tag", cfg.Tag),
		zap.String("regexp", cfg.Regexp),
	)
	if cfg.Size > 200 || cfg.Size < 0 {
		return nil, fmt.Errorf("size shoule in [0~200]")
	}

	var query bson.D
	if query, err = s.makeQuery(ctx, cfg); err != nil {
		return nil, errors.Wrap(err, "try to make query got error")
	}

	// logger.Debug("load blog posts", zap.String("query", fmt.Sprint(query)))
	iter, err := s.dao.GetPostsCol().Find(ctx, query,
		options.Find().SetSort(bson.D{{Key: "_id", Value: -1}}),
		options.Find().SetSkip(int64(cfg.Page*cfg.Size)),
		options.Find().SetLimit(int64(cfg.Size)),
	)
	if err != nil {
		return nil, errors.Wrap(err, "find posts")
	}

	results, err = s.filterPosts(ctx, cfg, iter)
	if err != nil {
		return nil, errors.Wrap(err, "filter")
	}

	logger.Debug("load posts done", zap.Int("n", len(results)))
	return results, nil
}

func (s *Type) LoadPostInfo(ctx context.Context) (*dto.PostInfo, error) {
	cnt, err := s.dao.GetPostsCol().CountDocuments(ctx, nil)
	if err != nil {
		return nil, errors.Wrap(err, "try to count posts got error")
	}

	return &dto.PostInfo{
		Total: int(cnt),
	}, nil
}

func (s *Type) makeQuery(ctx context.Context, cfg *dto.PostCfg) (query bson.D, err error) {
	log.Logger.Debug("makeQuery",
		zap.String("name", cfg.Name),
		zap.String("tag", cfg.Tag),
		zap.String("regexp", cfg.Regexp),
	)
	query = bson.D{}
	if cfg.Name != "" {
		query = append(query, bson.E{Key: "post_name", Value: strings.ToLower(url.QueryEscape(cfg.Name))})
	}

	if !cfg.ID.IsZero() {
		query = append(query, bson.E{Key: "_id", Value: cfg.ID})
	}

	if cfg.Tag != "" {
		query = append(query, bson.E{Key: "post_tags", Value: cfg.Tag})
	}

	if cfg.Regexp != "" {
		query = append(query, bson.E{Key: "post_content", Value: primitive.Regex{
			Pattern: cfg.Regexp,
			Options: "im",
		}})
	}

	// "" means empty, nil means ignore
	if cfg.CategoryURL != nil {
		log.Logger.Debug("post category", zap.String("category_url", *cfg.CategoryURL))
		if *cfg.CategoryURL == "" {
			query = append(query, bson.E{Key: "category", Value: nil})
		} else {
			var cate *model.Category
			if cate, err = s.LoadCategoryByURL(ctx, *cfg.CategoryURL); err != nil {
				log.Logger.Error("try to load posts by category url got error",
					zap.Error(err),
					zap.String("category_url", *cfg.CategoryURL),
				)
			} else if cate != nil {
				log.Logger.Debug("set post filter", zap.String("category", cate.ID.Hex()))
				query = append(query, bson.E{Key: "category", Value: cate.ID})
			}
		}
	}

	log.Logger.Debug("generate query", zap.String("query", fmt.Sprint(query)))
	return query, nil
}

func (s *Type) filterPosts(ctx context.Context, cfg *dto.PostCfg, iter *mongo.Cursor) (results []*model.Post, err error) {
	isValidate := true
	for iter.Next(ctx) {
		post := &model.Post{}
		if err = iter.Decode(post); err != nil {
			return nil, errors.Wrap(err, "iter posts")
		}

		// log.Logger.Debug("filter post", zap.String("post", fmt.Sprintf("%+v", result)))
		for _, f := range [...]func(*model.Post) bool{
			// filters pipeline
			passwordFilter,
			getContentLengthFilter(cfg.Length),
			defaultTypeFilter,
			hiddenFilter,
		} {
			if !f(post) {
				isValidate = false
				break
			}
		}

		if isValidate {
			results = append(results, post)
		}
		isValidate = true
	}

	return results, err
}

const defaultPostType = "html"

func defaultTypeFilter(docu *model.Post) bool {
	if docu.Type == "" {
		docu.Type = defaultPostType
	}

	return true
}

func hiddenFilter(docu *model.Post) bool {
	if docu.Hidden {
		docu.Markdown = "本文已被设置为隐藏"
		docu.Content = "本文已被设置为隐藏"
	}

	return true
}

func passwordFilter(docu *model.Post) bool {
	if docu.Password != "" {
		docu.Content = "🔒本文已设置为加密"
		docu.Markdown = "🔒本文已设置为加密"
	}

	return true
}

func getContentLengthFilter(length int) func(*model.Post) bool {
	return func(docu *model.Post) bool {
		if length > 0 { // 0 means full
			if len([]rune(docu.Content)) > length {
				docu.Content = string([]rune(docu.Content)[:length])
			}
			if len([]rune(docu.Markdown)) > length {
				docu.Markdown = string([]rune(docu.Markdown)[:length])
			}
		}
		return true
	}
}

func (s *Type) LoadUserByID(ctx context.Context, uid primitive.ObjectID) (user *model.User, err error) {
	log.Logger.Debug("LoadUserByID", zap.String("user_id", uid.Hex()))
	if uid.IsZero() {
		return nil, errors.Errorf("uid is empty")
	}

	user = &model.User{}
	result := s.dao.GetUsersCol().FindOne(ctx, bson.D{{Key: "_id", Value: uid}})
	if err = result.Decode(user); err != nil {
		return nil, errors.Wrap(err, "decode user")
	}

	return user, nil
}

func (s *Type) LoadCategoryByID(ctx context.Context, cateid primitive.ObjectID) (cate *model.Category, err error) {
	log.Logger.Debug("LoadCategoryByID", zap.String("cate_id", cateid.Hex()))
	if cateid.IsZero() {
		return nil, errors.Errorf("cateid is empty")
	}

	cate = &model.Category{}
	if err = s.dao.GetCategoriesCol().
		FindOne(ctx, bson.D{{Key: "_id", Value: cateid}}).
		Decode(cate); err != nil {
		return nil, errors.Wrapf(err, "get category by id %s", cateid.Hex())
	}

	return cate, nil
}

func (s *Type) LoadAllCategories(ctx context.Context) (cates []*model.Category, err error) {
	cates = []*model.Category{}
	cur, err := s.dao.GetCategoriesCol().Find(ctx, nil)
	if err != nil {
		return nil, errors.Wrap(err, "find all categories")
	}

	if err = cur.All(ctx, &cates); err != nil {
		return nil, errors.Wrap(err, "load all categories")
	}

	return cates, nil
}

func (s *Type) LoadCategoryByName(ctx context.Context, name string) (cate *model.Category, err error) {
	if name == "" {
		return nil, errors.Errorf("name is empty")
	}

	cate = &model.Category{}
	if err := s.dao.GetCategoriesCol().
		FindOne(ctx, bson.D{{Key: "name", Value: name}}).
		Decode(cate); err != nil && err != mongo.ErrNoDocuments {
		return nil, err
	}

	return cate, nil
}

func (s *Type) LoadCategoryByURL(ctx context.Context, url string) (cate *model.Category, err error) {
	if url == "" {
		return nil, nil
	}

	cate = &model.Category{}
	if err = s.dao.GetCategoriesCol().
		FindOne(ctx, bson.D{{Key: "url", Value: url}}).
		Decode(cate); err != nil && err != mongo.ErrNoDocuments {
		return nil, err
	}

	return cate, nil
}

func (s *Type) IsNameExists(ctx context.Context, name string) (bool, error) {
	n, err := s.dao.GetPostsCol().CountDocuments(ctx, bson.D{{Key: "post_name", Value: name}})
	if err != nil {
		log.Logger.Error("try to count post_name got error", zap.Error(err))
		return false, err
	}

	return n != 0, nil
}

// NewPost insert new post
//   - title: post title
//   - name: post url
//   - md: post markdown content
//   - ptype: post type, markdown/slide
func (s *Type) NewPost(ctx context.Context, authorID primitive.ObjectID, title, name, md, ptype string) (post *model.Post, err error) {
	if isExists, err := s.IsNameExists(ctx, name); err != nil {
		return nil, err
	} else if isExists {
		return nil, fmt.Errorf("post name `%v` already exists", name)
	}

	ts := time.Now()
	p := &model.Post{
		Type:       strings.ToLower(ptype),
		Markdown:   md,
		Content:    string(ParseMarkdown2HTML([]byte(md))),
		ModifiedAt: ts,
		CreatedAt:  ts,
		Title:      title,
		Name:       strings.ToLower(url.QueryEscape(name)),
		Status:     "publish",
		Author:     authorID,
	}
	p.Menu = ExtractMenu(p.Content)

	if gconfig.Shared.GetBool("dry") {
		log.Logger.Info("insert post",
			zap.String("title", p.Title),
			zap.String("name", p.Name),
			// zap.String("markdown", p.Markdown),
			// zap.String("content", p.Content),
		)
	} else {
		if _, err = s.dao.GetPostsCol().InsertOne(ctx, p); err != nil {
			return nil, errors.Wrap(err, "try to insert post got error")
		}
	}

	return p, nil
}

var ErrLogin = errors.New("Password Or Username Incorrect")

func (s *Type) ValidateLogin(ctx context.Context, account, password string) (u *model.User, err error) {
	log.Logger.Debug("ValidateLogin", zap.String("account", account))
	u = &model.User{}
	if err := s.dao.GetUsersCol().
		FindOne(ctx, bson.D{{Key: "account", Value: account}}).
		Decode(u); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, fmt.Errorf("user notfound")
		}

		return nil, err
	}

	if encrypt.ValidatePasswordHash([]byte(u.Password), []byte(password)) {
		log.Logger.Debug("user login", zap.String("user", u.Account))
		return u, nil
	}

	return nil, ErrLogin
}

var supporttedTypes = map[string]struct{}{
	"markdown": {},
}

// UpdatePostCategory change blog post's category
func (s *Type) UpdatePostCategory(ctx context.Context, name, category string) (p *model.Post, err error) {
	c := new(model.Category)
	if err = s.dao.GetCategoriesCol().FindOne(ctx, bson.M{"name": category}).Decode(c); err != nil {
		return nil, errors.Wrapf(err, "load category `%s`", category)
	}

	p = new(model.Post)
	if err = s.dao.GetPostsCol().FindOne(ctx, bson.M{"post_name": name}).Decode(p); err != nil {
		return nil, errors.Wrapf(err, "load post by name `%s`", name)
	}

	if p.Category == c.ID {
		return p, nil
	}

	p.Category = c.ID
	if _, err = s.dao.GetPostsCol().UpdateByID(ctx, p.ID, bson.M{
		"$set": bson.M{
			"category": c.ID,
		},
	}); err != nil {
		return nil, errors.Wrapf(err, "update post `%s` category", p.Name)
	}

	log.Logger.Info("updated post category", zap.String("post", p.Name), zap.String("category", c.Name))
	return p, nil
}

func (s *Type) UpdatePost(ctx context.Context, user *model.User,
	name string,
	title string,
	md string,
	typeArg string) (p *model.Post, err error) {
	p = &model.Post{}
	typeArg = strings.ToLower(typeArg)
	if _, ok := supporttedTypes[typeArg]; !ok {
		return nil, fmt.Errorf("type `%v` not supportted", typeArg)
	}
	if err = s.dao.GetPostsCol().FindOne(ctx, bson.M{"post_name": name}).Decode(p); err != nil {
		if mongoSDK.NotFound(err) {
			return nil, errors.Wrap(err, "post not exists")
		}

		return nil, err
	}

	if p.Author != user.ID {
		return nil, fmt.Errorf("post do not belong to this user")
	}

	p.Title = title
	p.Markdown = md
	p.Content = string(ParseMarkdown2HTML([]byte(md)))
	p.Menu = ExtractMenu(p.Content)
	p.ModifiedAt = time.Now()
	p.Type = typeArg

	if _, err = s.dao.GetPostsCol().UpdateByID(ctx, p.ID, p); err != nil {
		return nil, errors.Wrap(err, "try to update post got error")
	}

	log.Logger.Info("updated post", zap.String("post", p.Name), zap.String("user", user.Account))
	return p, nil
}

func (s *Type) ValidateAndGetUser(ctx context.Context) (user *model.User, err error) {
	uc := &jwt.UserClaims{}
	if err = auth.Instance.GetUserClaims(ctx, uc); err != nil {
		return nil, errors.Wrap(err, "get user from token")
	}

	uid := primitive.NewObjectID()
	if user, err = s.LoadUserByID(ctx, uid); err != nil {
		return nil, errors.Wrapf(err, "load user `%s`", uid)
	}

	return user, nil
}
