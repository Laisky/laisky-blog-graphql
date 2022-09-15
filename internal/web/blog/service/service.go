package service

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/dao"
	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/dto"
	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
	"github.com/Laisky/laisky-blog-graphql/library/auth"
	"github.com/Laisky/laisky-blog-graphql/library/jwt"
	"github.com/Laisky/laisky-blog-graphql/library/log"

	gconfig "github.com/Laisky/go-config"
	"github.com/Laisky/go-utils/v2/encrypt"
	"github.com/Laisky/zap"
	"github.com/pkg/errors"
	mgo "gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
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

func (s *Type) LoadPostSeries(id bson.ObjectId, key string) (se []*model.PostSeries, err error) {
	query := bson.M{}
	if id != "" {
		query["_id"] = id
	}

	if key != "" {
		query["key"] = key
	}

	se = []*model.PostSeries{}
	err = s.dao.GetPostSeriesCol().Find(query).All(&se)
	return
}

func (s *Type) LoadPosts(cfg *dto.PostCfg) (results []*model.Post, err error) {
	logger := log.Logger.With(
		zap.Int("page", cfg.Page), zap.Int("size", cfg.Size),
		zap.String("tag", cfg.Tag),
		zap.String("regexp", cfg.Regexp),
	)
	if cfg.Size > 200 || cfg.Size < 0 {
		return nil, fmt.Errorf("size shoule in [0~200]")
	}

	var query bson.M
	if query, err = s.makeQuery(cfg); err != nil {
		return nil, errors.Wrap(err, "try to make query got error")
	}

	// logger.Debug("load blog posts", zap.String("query", fmt.Sprint(query)))
	iter := s.dao.GetPostsCol().Find(query).
		Sort("-_id").
		Skip(cfg.Page * cfg.Size).
		Limit(cfg.Size).
		Iter()
	results = s.filterPosts(cfg, iter)
	logger.Debug("load posts done", zap.Int("n", len(results)))
	return results, nil
}

func (s *Type) LoadPostInfo() (*dto.PostInfo, error) {
	cnt, err := s.dao.GetPostsCol().Count()
	if err != nil {
		return nil, errors.Wrap(err, "try to count posts got error")
	}

	return &dto.PostInfo{
		Total: cnt,
	}, nil
}

func (s *Type) makeQuery(cfg *dto.PostCfg) (query bson.M, err error) {
	log.Logger.Debug("makeQuery",
		zap.String("name", cfg.Name),
		zap.String("tag", cfg.Tag),
		zap.String("regexp", cfg.Regexp),
	)
	query = bson.M{}
	if cfg.Name != "" {
		query["post_name"] = strings.ToLower(url.QueryEscape(cfg.Name))
	}

	if cfg.ID != "" {
		query["_id"] = cfg.ID
	}

	if cfg.Tag != "" {
		query["post_tags"] = cfg.Tag
	}

	if cfg.Regexp != "" {
		query["post_content"] = bson.M{"$regex": bson.RegEx{
			Pattern: cfg.Regexp,
			Options: "im",
		}}
	}

	// "" means empty, nil means ignore
	if cfg.CategoryURL != nil {
		log.Logger.Debug("post category", zap.String("category_url", *cfg.CategoryURL))
		if *cfg.CategoryURL == "" {
			query["category"] = nil
		} else {
			var cate *model.Category
			if cate, err = s.LoadCategoryByURL(*cfg.CategoryURL); err != nil {
				log.Logger.Error("try to load posts by category url got error",
					zap.Error(err),
					zap.String("category_url", *cfg.CategoryURL),
				)
			} else if cate != nil {
				log.Logger.Debug("set post filter", zap.String("category", cate.ID.Hex()))
				query["category"] = cate.ID
			}
		}
	}

	log.Logger.Debug("generate query", zap.String("query", fmt.Sprint(query)))
	return query, nil
}

func (s *Type) filterPosts(cfg *dto.PostCfg, iter *mgo.Iter) (results []*model.Post) {
	post := &model.Post{}
	isValidate := true
	for iter.Next(post) {
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
			post = &model.Post{}
		}
		isValidate = true
	}

	return results
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

func (s *Type) LoadUserByID(uid bson.ObjectId) (user *model.User, err error) {
	log.Logger.Debug("LoadUserByID", zap.String("user_id", uid.Hex()))
	if uid == "" {
		return nil, nil
	}

	user = &model.User{}
	if err = s.dao.GetUsersCol().FindId(uid).One(user); err != nil {
		return nil, err
	}
	return user, nil
}

func (s *Type) LoadCategoryByID(cateid bson.ObjectId) (cate *model.Category, err error) {
	log.Logger.Debug("LoadCategoryByID", zap.String("cate_id", cateid.Hex()))
	if cateid == "" {
		return nil, nil
	}

	cate = &model.Category{}
	if err = s.dao.GetCategoriesCol().FindId(cateid).One(cate); err != nil {
		return nil, err
	}
	return cate, nil
}

func (s *Type) LoadAllCategories() (cates []*model.Category, err error) {
	cates = []*model.Category{}
	if err = s.dao.GetCategoriesCol().Find(bson.M{}).All(&cates); err != nil {
		return nil, errors.Wrap(err, "try to load all categories got error")
	}

	return cates, nil
}

func (s *Type) LoadCategoryByName(name string) (cate *model.Category, err error) {
	if name == "" {
		return nil, nil
	}

	cate = &model.Category{}
	if err = s.dao.GetCategoriesCol().Find(bson.M{"name": name}).One(cate); err != nil && err != mgo.ErrNotFound {
		return nil, err
	}

	return cate, nil
}

func (s *Type) LoadCategoryByURL(url string) (cate *model.Category, err error) {
	if url == "" {
		return nil, nil
	}

	cate = &model.Category{}
	if err = s.dao.GetCategoriesCol().Find(bson.M{"url": url}).One(cate); err != nil && err != mgo.ErrNotFound {
		return nil, err
	}

	return cate, nil
}

func (s *Type) IsNameExists(name string) (bool, error) {
	n, err := s.dao.GetPostsCol().Find(bson.M{"post_name": name}).Count()
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
func (s *Type) NewPost(authorID bson.ObjectId, title, name, md, ptype string) (post *model.Post, err error) {
	if isExists, err := s.IsNameExists(name); err != nil {
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
		if err = s.dao.GetPostsCol().Insert(p); err != nil {
			return nil, errors.Wrap(err, "try to insert post got error")
		}
	}

	return p, nil
}

var ErrLogin = errors.New("Password Or Username Incorrect")

func (s *Type) ValidateLogin(account, password string) (u *model.User, err error) {
	log.Logger.Debug("ValidateLogin", zap.String("account", account))
	u = &model.User{}
	if err := s.dao.GetUsersCol().Find(bson.M{"account": account}).One(u); err != nil {
		if err == mgo.ErrNotFound {
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
func (s *Type) UpdatePostCategory(name, category string) (p *model.Post, err error) {
	c := new(model.Category)
	if err = s.dao.GetCategoriesCol().Find(bson.M{"name": category}).One(c); err != nil {
		return nil, errors.Wrapf(err, "load category `%s`", category)
	}

	p = new(model.Post)
	if err = s.dao.GetPostsCol().Find(bson.M{"post_name": name}).One(p); err != nil {
		return nil, errors.Wrapf(err, "load post by name `%s`", name)
	}

	if p.Category == c.ID {
		return p, nil
	}

	p.Category = c.ID
	if err = s.dao.GetPostsCol().UpdateId(p.ID, bson.M{
		"$set": bson.M{
			"category": c.ID,
		},
	}); err != nil {
		return nil, errors.Wrapf(err, "update post `%s` category", p.Name)
	}

	log.Logger.Info("updated post category", zap.String("post", p.Name), zap.String("category", c.Name))
	return p, nil
}

func (s *Type) UpdatePost(user *model.User,
	name string,
	title string,
	md string,
	typeArg string) (p *model.Post, err error) {
	p = &model.Post{}
	typeArg = strings.ToLower(typeArg)
	if _, ok := supporttedTypes[typeArg]; !ok {
		return nil, fmt.Errorf("type `%v` not supportted", typeArg)
	}
	if err = s.dao.GetPostsCol().Find(bson.M{"post_name": name}).One(p); err != nil {
		if err == mgo.ErrNotFound {
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

	if err = s.dao.GetPostsCol().UpdateId(p.ID, p); err != nil {
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

	uid := bson.ObjectIdHex(uc.Subject)
	if user, err = s.LoadUserByID(uid); err != nil {
		return nil, errors.Wrapf(err, "load user `%s`", uid)
	}

	return user, nil
}
