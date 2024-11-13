// Package service is the service layer of blog.
package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v5"
	gconfig "github.com/Laisky/go-config/v2"
	gutils "github.com/Laisky/go-utils/v4"
	glog "github.com/Laisky/go-utils/v4/log"
	"github.com/Laisky/zap"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/Laisky/laisky-blog-graphql/internal/library/models"
	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/dao"
	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/dto"
	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
	"github.com/Laisky/laisky-blog-graphql/library/auth"
	mongoSDK "github.com/Laisky/laisky-blog-graphql/library/db/mongo"
	"github.com/Laisky/laisky-blog-graphql/library/jwt"
)

// Blog blog service
type Blog struct {
	logger glog.Logger
	dao    *dao.Blog
}

// New new blog service
func New(ctx context.Context,
	logger glog.Logger,
	dao *dao.Blog) (*Blog, error) {
	b := &Blog{
		logger: logger,
		dao:    dao,
	}

	if err := b.setupUserCols(ctx); err != nil {
		return nil, errors.Wrap(err, "setup user cols")
	}

	return b, nil
}

// LoadPostTags load post tags
func (s *Blog) LoadPostTags(ctx context.Context) (tags []string, err error) {
	// get latest document
	docu := new(model.PostTags)
	if err = s.dao.PostTagsCol().
		FindOne(ctx, bson.D{},
			options.FindOne().SetSort(bson.D{{Key: "_id", Value: -1}})).
		Decode(docu); err != nil {
		return nil, errors.Wrap(err, "get latest post tags")
	}

	return docu.Keywords, nil
}

// LoadPostSeries load post series
func (s *Blog) LoadPostSeries(ctx context.Context,
	id primitive.ObjectID, key string) (se []*model.PostSeries, err error) {
	query := bson.D{}
	if !id.IsZero() {
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
	defer cur.Close(ctx) //nolint:errcheck

	if err = cur.All(ctx, &se); err != nil {
		return nil, errors.Wrap(err, "load series")
	}

	return
}

// LoadPosts load posts
func (s *Blog) LoadPosts(ctx context.Context,
	cfg *dto.PostCfg) (results []*model.Post, err error) {
	logger := s.logger.With(
		zap.Int("page", cfg.Page), zap.Int("size", cfg.Size),
		zap.String("tag", cfg.Tag),
		zap.String("regexp", cfg.Regexp),
	)
	if cfg.Size > 200 || cfg.Size < 0 {
		return nil, errors.Errorf("size shoule in [0~200]")
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

// LoadPostInfo load post info
func (s *Blog) LoadPostInfo(ctx context.Context) (*dto.PostInfo, error) {
	cnt, err := s.dao.GetPostsCol().CountDocuments(ctx, bson.D{})
	if err != nil {
		return nil, errors.Wrap(err, "try to count posts got error")
	}

	return &dto.PostInfo{
		Total: int(cnt),
	}, nil
}

// LoadPostHistory load post history by arweave file id
func (s *Blog) LoadPostHistory(ctx context.Context, fileID string, language models.Language) (*model.Post, error) {
	logger := gmw.GetLogger(ctx)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://ario.laisky.com/"+fileID, nil)
	if err != nil {
		return nil, errors.Wrap(err, "new request")
	}

	resp, err := httpcli.Do(req)
	if err != nil {
		return nil, errors.Wrapf(err, "do request `%s`", req.URL.String())
	}
	defer gutils.CloseWithLog(resp.Body, logger)

	if resp.StatusCode != http.StatusOK {
		return nil, errors.Errorf("got status code `%d`", resp.StatusCode)
	}

	if resp.ContentLength > 1024*1024*10 { // 10M
		return nil, errors.Errorf("content too large")
	}

	cnt, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Wrap(err, "read all")
	}

	var reader io.Reader
	if bytes.HasPrefix(cnt, []byte("gz::")) {
		cnt = cnt[4:]

		reader, err = gzip.NewReader(bytes.NewReader(cnt))
		if err != nil {
			return nil, errors.Wrap(err, "new gzip reader")
		}
	} else {
		reader = bytes.NewReader(cnt)
	}

	post := new(model.Post)
	if err = json.NewDecoder(reader).Decode(post); err != nil {
		return nil, errors.Wrap(err, "decode post")
	}

	if language != models.LanguageZhCn && post.I18N.EnUs.PostContent != "" {
		post.Content = post.I18N.EnUs.PostContent
		post.Title = post.I18N.EnUs.PostTitle
		post.Menu = post.I18N.EnUs.PostMenu
		post.Markdown = post.I18N.EnUs.PostMarkdown
	}

	return post, nil
}

func (s *Blog) makeQuery(ctx context.Context,
	cfg *dto.PostCfg) (query bson.D, err error) {
	s.logger.Debug("makeQuery",
		zap.String("name", cfg.Name),
		zap.String("tag", cfg.Tag),
		zap.String("regexp", cfg.Regexp),
	)
	query = bson.D{}
	if cfg.Name != "" {
		query = append(query, bson.E{
			Key:   "post_name",
			Value: strings.ToLower(url.QueryEscape(cfg.Name))})
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
		s.logger.Debug("post category", zap.String("category_url", *cfg.CategoryURL))
		if *cfg.CategoryURL == "" {
			query = append(query, bson.E{Key: "category", Value: nil})
		} else {
			var cate *model.Category
			if cate, err = s.LoadCategoryByURL(ctx, *cfg.CategoryURL); err != nil {
				s.logger.Error("try to load posts by category url got error",
					zap.Error(err),
					zap.String("category_url", *cfg.CategoryURL),
				)
			} else if cate != nil {
				s.logger.Debug("set post filter", zap.String("category", cate.ID.Hex()))
				query = append(query, bson.E{Key: "category", Value: cate.ID})
			}
		}
	}

	s.logger.Debug("generate query", zap.String("query", fmt.Sprint(query)))
	return query, nil
}

func (s *Blog) filterPosts(ctx context.Context,
	cfg *dto.PostCfg, iter *mongo.Cursor) (results []*model.Post, err error) {
	isValidate := true
	for iter.Next(ctx) {
		post := &model.Post{}
		if err = iter.Decode(post); err != nil {
			return nil, errors.Wrap(err, "iter posts")
		}

		//s.logger.Debug("filter post", zap.String("post", fmt.Sprintf("%+v", result)))
		for _, f := range [...]func(*model.Post) bool{
			// filters pipeline
			passwordFilter,
			hiddenFilter,
			s.getI18NFilter(ctx, cfg.Language),
			getContentLengthFilter(cfg.Length),
			defaultTypeFilter,
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

// getI18NFilter replace content with specified language
func (s *Blog) getI18NFilter(ctx context.Context,
	language models.Language) func(*model.Post) bool {
	return func(p *model.Post) bool {
		logger := gmw.GetLogger(ctx).
			With(zap.String("language", language.String()),
				zap.String("post", p.ID.String()))

		// upgrade to new implementation
		// be noticed that p.Markdown could be empty!
		if p.Markdown != "" &&
			p.ModifiedAt.Before(time.Date(2024, 9, 23, 0, 0, 0, 0, time.UTC)) {
			p.Content = ParseMarkdown2HTML([]byte(p.Markdown))
			p.Menu = ExtractMenu(p.Content)

			if p.I18N.EnUs.PostMarkdown != "" {
				p.I18N.EnUs.PostContent = ParseMarkdown2HTML([]byte(p.I18N.EnUs.PostMarkdown))
				p.I18N.EnUs.PostMenu = ExtractMenu(p.I18N.EnUs.PostContent)
			}

			// save
			if _, err := s.dao.GetPostsCol().UpdateByID(ctx, p.ID, bson.M{
				"$set": bson.M{
					"modified_at":             gutils.Clock.GetUTCNow(),
					"content":                 p.Content,
					"menu":                    p.Menu,
					"i18n.en_us.post_content": p.I18N.EnUs.PostContent,
					"i18n.en_us.post_menu":    p.I18N.EnUs.PostMenu,
				},
			}); err != nil {
				logger.Error("try to update post got error", zap.Error(err))
			}
		}

		switch language {
		case models.LanguageZhCn:
			p.Language = models.LanguageZhCn.String()
		default: // en-us as default
			if p.I18N.EnUs.PostMarkdown != "" {
				p.Language = models.LanguageEnUs.String()
				p.Markdown = p.I18N.EnUs.PostMarkdown
				p.Title = p.I18N.EnUs.PostTitle

				if p.I18N.EnUs.PostContent == "" || p.I18N.EnUs.PostMenu == "" {
					p.I18N.EnUs.PostContent = ParseMarkdown2HTML([]byte(p.Markdown))
					p.I18N.EnUs.PostMenu = ExtractMenu(p.I18N.EnUs.PostContent)

					// update post i18n content
					if _, err := s.dao.GetPostsCol().UpdateByID(ctx, p.ID, bson.M{
						"$set": bson.M{
							"i18n.en_us.post_content": p.I18N.EnUs.PostContent,
							"i18n.en_us.post_menu":    p.I18N.EnUs.PostMenu,
						},
					}); err != nil {
						logger.Error("try to update post got error", zap.Error(err))
					}
				}

				p.Content = p.I18N.EnUs.PostContent
				p.Menu = p.I18N.EnUs.PostMenu
			}
		}

		return true
	}
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

// LoadUserByID load user by id
func (s *Blog) LoadUserByID(ctx context.Context, uid primitive.ObjectID) (user *model.User, err error) {
	s.logger.Debug("LoadUserByID", zap.String("user_id", uid.Hex()))
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

// LoadCategoryByID load category by id
func (s *Blog) LoadCategoryByID(ctx context.Context, cateid primitive.ObjectID) (cate *model.Category, err error) {
	s.logger.Debug("LoadCategoryByID", zap.String("cate_id", cateid.Hex()))
	if cateid.IsZero() {
		return cate, nil
	}

	cate = &model.Category{}
	if err = s.dao.GetCategoriesCol().
		FindOne(ctx, bson.D{{Key: "_id", Value: cateid}}).
		Decode(cate); err != nil {
		return nil, errors.Wrapf(err, "get category by id %s", cateid.Hex())
	}

	return cate, nil
}

// LoadAllCategories load all categories
func (s *Blog) LoadAllCategories(ctx context.Context) (cates []*model.Category, err error) {
	cates = []*model.Category{}
	cur, err := s.dao.GetCategoriesCol().Find(ctx, bson.D{})
	if err != nil {
		return nil, errors.Wrap(err, "find all categories")
	}

	if err = cur.All(ctx, &cates); err != nil {
		return nil, errors.Wrap(err, "load all categories")
	}

	return cates, nil
}

// LoadCategoryByName load category by name
func (s *Blog) LoadCategoryByName(ctx context.Context, name string) (cate *model.Category, err error) {
	if name == "" {
		return nil, errors.Errorf("name is empty")
	}

	cate = &model.Category{}
	if err := s.dao.GetCategoriesCol().
		FindOne(ctx, bson.D{{Key: "name", Value: name}}).
		Decode(cate); err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, errors.Wrapf(err, "decode category %q", name)
	}

	return cate, nil
}

// LoadCategoryByURL load category by url
func (s *Blog) LoadCategoryByURL(ctx context.Context, url string) (cate *model.Category, err error) {
	if url == "" {
		return cate, nil
	}

	cate = &model.Category{}
	if err = s.dao.GetCategoriesCol().
		FindOne(ctx, bson.D{{Key: "url", Value: url}}).
		Decode(cate); err != nil {
		if !errors.Is(err, mongo.ErrNoDocuments) {
			return nil, errors.Wrapf(err, "decode category %q", url)
		}
	}

	return cate, nil
}

// IsNameExists check if name exists
func (s *Blog) IsNameExists(ctx context.Context, name string) (bool, error) {
	n, err := s.dao.GetPostsCol().CountDocuments(ctx, bson.D{{Key: "post_name", Value: name}})
	if err != nil {
		s.logger.Error("try to count post_name got error", zap.Error(err))
		return false, errors.Wrapf(err, "try to count post_name `%s` got error", name)
	}

	return n != 0, nil
}

// NewPost insert new post
//   - title: post title
//   - name: post url
//   - md: post markdown content
//   - ptype: post type, markdown/slide
func (s *Blog) NewPost(ctx context.Context,
	authorID primitive.ObjectID, title, name, md, ptype string) (post *model.Post, err error) {
	if isExists, err := s.IsNameExists(ctx, name); err != nil {
		return nil, errors.Wrapf(err, "check post name `%s` exists", name)
	} else if isExists {
		return nil, errors.Errorf("post name `%v` already exists", name)
	}

	ts := gutils.Clock.GetUTCNow()
	p := &model.Post{
		Type:       strings.ToLower(ptype),
		Markdown:   md,
		Content:    ParseMarkdown2HTML([]byte(md)),
		ModifiedAt: ts,
		CreatedAt:  ts,
		Title:      title,
		Name:       strings.ToLower(url.QueryEscape(name)),
		Status:     "publish",
		Author:     authorID,
	}
	p.Menu = ExtractMenu(p.Content)

	switch models.Language(p.Language) {
	case models.LanguageEnUs:
		p.I18N.EnUs.PostContent = p.Content
		p.I18N.EnUs.PostTitle = p.Title
		p.I18N.EnUs.PostMarkdown = p.Markdown
		p.I18N.EnUs.PostMenu = p.Menu
	default:
	}

	if gconfig.Shared.GetBool("dry") {
		s.logger.Info("insert post",
			zap.String("title", p.Title),
			zap.String("name", p.Name),
			// zap.String("markdown", p.Markdown),
			// zap.String("content", p.Content),
		)
	} else {
		// save to arweave
		if arFileId, err := s.dao.SaveToArweave(ctx, p); err != nil {
			s.logger.Error("try to save post to arweave got error", zap.Error(err))
		} else {
			p.ArweaveId = slices.Insert(p.ArweaveId, 0, model.ArweaveHistoryItem{
				Time: p.ModifiedAt,
				Id:   arFileId,
			})
		}

		if _, err = s.dao.GetPostsCol().InsertOne(ctx, p); err != nil {
			return nil, errors.Wrap(err, "try to insert post got error")
		}
	}

	return p, nil
}

var supporttedTypes = map[string]struct{}{
	"markdown": {},
}

// UpdatePostCategory change blog post's category
func (s *Blog) UpdatePostCategory(ctx context.Context, name, category string) (p *model.Post, err error) {
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

	s.logger.Info("updated post category", zap.String("post", p.Name), zap.String("category", c.Name))
	return p, nil
}

func (s *Blog) UpdatePost(ctx context.Context, user *model.User,
	name string,
	title string,
	md string,
	typeArg string,
	language models.Language,
) (p *model.Post, err error) {
	p = &model.Post{}
	typeArg = strings.ToLower(typeArg)
	if _, ok := supporttedTypes[typeArg]; !ok {
		return nil, errors.Errorf("type `%v` not supportted", typeArg)
	}
	if err = s.dao.GetPostsCol().FindOne(ctx, bson.M{"post_name": name}).Decode(p); err != nil {
		if mongoSDK.NotFound(err) {
			return nil, errors.Wrap(err, "post not exists")
		}

		return nil, errors.Wrapf(err, "load post by name `%s`", name)
	}

	if p.Author != user.ID {
		return nil, errors.Errorf("post do not belong to this user")
	}

	p.ModifiedAt = gutils.Clock.GetUTCNow()
	p.Type = typeArg
	parsedMd := ParseMarkdown2HTML([]byte(md))
	switch language {
	case models.LanguageZhCn:
		p.Title = title
		p.Markdown = md
		p.Content = parsedMd
		p.Menu = ExtractMenu(p.Content)
	case models.LanguageEnUs:
		p.I18N.UpdateAt = time.Now().UTC()
		p.I18N.EnUs.PostTitle = title
		p.I18N.EnUs.PostMarkdown = md
		p.I18N.EnUs.PostContent = parsedMd
		p.I18N.EnUs.PostMenu = ExtractMenu(p.I18N.EnUs.PostContent)
	default:
		return nil, errors.Errorf("language `%s` not supportted", language)
	}

	// save to arweave
	if arFileId, err := s.dao.SaveToArweave(ctx, p); err != nil {
		s.logger.Error("try to save post to arweave got error", zap.Error(err))
	} else {
		p.ArweaveId = slices.Insert(p.ArweaveId, 0, model.ArweaveHistoryItem{
			Time: p.ModifiedAt,
			Id:   arFileId,
		})
	}

	if _, err = s.dao.GetPostsCol().ReplaceOne(ctx, bson.M{"_id": p.ID}, p); err != nil {
		return nil, errors.Wrap(err, "try to update post got error")
	}

	s.logger.Info("updated post", zap.String("post", p.Name), zap.String("user", user.Account))
	return p, nil
}

func (s *Blog) ValidateAndGetUser(ctx context.Context) (user *model.User, err error) {
	uc := &jwt.UserClaims{}
	if err = auth.Instance.GetUserClaims(ctx, uc); err != nil {
		return nil, errors.Wrap(err, "get user from token")
	}

	uid, err := primitive.ObjectIDFromHex(uc.Subject)
	if err != nil {
		return nil, errors.Wrap(err, "parse user id in hex")
	}

	if user, err = s.LoadUserByID(ctx, uid); err != nil {
		return nil, errors.Wrapf(err, "load user `%s`", uid)
	}

	return user, nil
}
