package blog

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Laisky/go-utils"
	"github.com/Laisky/laisky-blog-graphql/libs"
	"github.com/Laisky/laisky-blog-graphql/models"
	"github.com/Laisky/zap"
	"github.com/dgrijalva/jwt-go"
	"github.com/pkg/errors"
	mgo "gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
)

type BlogDB struct {
	dbcli *models.DB
}

type Post struct {
	ID         bson.ObjectId `bson:"_id,omitempty" json:"mongo_id"`
	CreatedAt  time.Time     `bson:"post_created_at" json:"created_at"`
	ModifiedAt time.Time     `bson:"post_modified_gmt" json:"modified_at"`
	Title      string        `bson:"post_title" json:"title"`
	Type       string        `bson:"post_type" json:"type"`
	Status     string        `bson:"post_status" json:"status"`
	Name       string        `bson:"post_name" json:"name"`
	Content    string        `bson:"post_content" json:"content"`
	Markdown   string        `bson:"post_markdown" json:"markdown"`
	Author     bson.ObjectId `bson:"post_author" json:"author"`
	Menu       string        `bson:"post_menu" json:"menu"`
	Password   string        `bson:"post_password" json:"password"`
	Category   bson.ObjectId `bson:"category,omitempty" json:"category"`
	Tags       []string      `bson:"post_tags" json:"tags"`
}

type PostInfo struct {
	Total int `json:"total"`
}

type UserClaims struct {
	jwt.StandardClaims
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

func (uc *UserClaims) Valid() error {
	now := utils.Clock.GetUTCNow().Unix()
	if !uc.StandardClaims.VerifyExpiresAt(now, true) {
		return fmt.Errorf("token expired")
	}

	if !uc.StandardClaims.VerifyIssuedAt(now, true) {
		return fmt.Errorf("token issueAt invalid")
	}

	return nil
}

type User struct {
	ID       bson.ObjectId `bson:"_id" json:"id"`
	Username string        `bson:"username" json:"username"`
	Account  string        `bson:"account" json:"account"`
	Password string        `bson:"password" json:"password"`
}

type Category struct {
	ID   bson.ObjectId `bson:"_id" json:"mongo_id"`
	Name string        `bson:"name" json:"name"`
	URL  string        `bson:"url" json:"url"`
}

const (
	DB_NAME           = "blog"
	POST_COL_NAME     = "posts"
	USER_COL_NAME     = "users"
	CATEGORY_COL_NAME = "categories"
)

func (u *User) GetID() string {
	return u.ID.Hex()
}

func (u *User) GetPayload() map[string]interface{} {
	return map[string]interface{}{
		"display_name": u.Username,
		"account":      u.Account,
	}
}

func NewBlogDB(dbcli *models.DB) *BlogDB {
	return &BlogDB{
		dbcli: dbcli,
	}
}

func (db *BlogDB) GetPostsCol() *mgo.Collection {
	return db.dbcli.GetCol(POST_COL_NAME)
}
func (db *BlogDB) GetUsersCol() *mgo.Collection {
	return db.dbcli.GetCol(USER_COL_NAME)
}
func (db *BlogDB) GetCategoriesCol() *mgo.Collection {
	return db.dbcli.GetCol(CATEGORY_COL_NAME)
}

type BlogPostCfg struct {
	Page, Size, Length int
	Name, Tag, Regexp  string
	CategoryURL        *string
}

func (db *BlogDB) LoadPosts(cfg *BlogPostCfg) (results []*Post, err error) {
	logger := libs.Logger.With(
		zap.Int("page", cfg.Page), zap.Int("size", cfg.Size),
		zap.String("tag", cfg.Tag),
		zap.String("regexp", cfg.Regexp),
	)
	if cfg.Size > 200 || cfg.Size < 0 {
		return nil, fmt.Errorf("size shoule in [0~200]")
	}

	var query bson.M
	if query, err = db.makeQuery(cfg); err != nil {
		return nil, errors.Wrap(err, "try to make query got error")
	}

	// logger.Debug("load blog posts", zap.String("query", fmt.Sprint(query)))
	iter := db.dbcli.GetCol(POST_COL_NAME).Find(query).
		Sort("-_id").
		Skip(cfg.Page * cfg.Size).
		Limit(cfg.Size).
		Iter()
	results = db.filterPosts(cfg, iter)
	logger.Debug("load posts done", zap.Int("n", len(results)))
	return results, nil
}

func (db *BlogDB) LoadPostInfo() (*PostInfo, error) {
	cnt, err := db.dbcli.GetCol(POST_COL_NAME).Count()
	if err != nil {
		return nil, errors.Wrap(err, "try to count posts got error")
	}

	return &PostInfo{
		Total: cnt,
	}, nil
}

func (db *BlogDB) makeQuery(cfg *BlogPostCfg) (query bson.M, err error) {
	libs.Logger.Debug("makeQuery",
		zap.String("name", cfg.Name),
		zap.String("tag", cfg.Tag),
		zap.String("regexp", cfg.Regexp),
	)
	query = bson.M{}
	if cfg.Name != "" {
		query["post_name"] = strings.ToLower(url.QueryEscape(cfg.Name))
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
		libs.Logger.Debug("post category", zap.String("category_url", *cfg.CategoryURL))
		if *cfg.CategoryURL == "" {
			query["category"] = nil
		} else {
			var cate *Category
			if cate, err = db.LoadCategoryByURL(*cfg.CategoryURL); err != nil {
				libs.Logger.Error("try to load posts by category url got error",
					zap.Error(err),
					zap.String("category_url", *cfg.CategoryURL),
				)
			} else if cate != nil {
				libs.Logger.Debug("set post filter", zap.String("category", cate.ID.Hex()))
				query["category"] = cate.ID
			}
		}
	}

	libs.Logger.Debug("generate query", zap.String("query", fmt.Sprint(query)))
	return query, nil
}

func (db *BlogDB) filterPosts(cfg *BlogPostCfg, iter *mgo.Iter) (results []*Post) {
	result := &Post{}
	isValidate := true
	for iter.Next(result) {
		// libs.Logger.Debug("filter post", zap.String("post", fmt.Sprintf("%+v", result)))
		for _, f := range [...]func(*Post) bool{
			// filters pipeline
			passwordFilter,
			getContentLengthFilter(cfg.Length),
			defaultTypeFilter,
		} {
			if !f(result) {
				isValidate = false
				break
			}
		}

		if isValidate {
			results = append(results, result)
			result = &Post{}
		}
		isValidate = true
	}

	return results
}

const defaultPostType = "html"

func defaultTypeFilter(docu *Post) bool {
	if docu.Type == "" {
		docu.Type = defaultPostType
	}

	return true
}

func passwordFilter(docu *Post) bool {
	return docu.Password == ""
}

func getContentLengthFilter(length int) func(*Post) bool {
	return func(docu *Post) bool {
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

func (db *BlogDB) LoadUserById(uid bson.ObjectId) (user *User, err error) {
	libs.Logger.Debug("LoadUserById", zap.String("user_id", uid.Hex()))
	if uid == "" {
		return nil, nil
	}

	user = &User{}
	if err = db.dbcli.GetCol(USER_COL_NAME).FindId(uid).One(user); err != nil {
		return nil, err
	}
	return user, nil
}

func (db *BlogDB) LoadCategoryByID(cateid bson.ObjectId) (cate *Category, err error) {
	libs.Logger.Debug("LoadCategoryByID", zap.String("cate_id", cateid.Hex()))
	if cateid == "" {
		return nil, nil
	}

	cate = &Category{}
	if err = db.dbcli.GetCol(CATEGORY_COL_NAME).FindId(cateid).One(cate); err != nil {
		return nil, err
	}
	return cate, nil
}

func (db *BlogDB) LoadAllCategories() (cates []*Category, err error) {
	cates = []*Category{}
	if err = db.dbcli.GetCol(CATEGORY_COL_NAME).Find(bson.M{}).All(&cates); err != nil {
		return nil, errors.Wrap(err, "try to load all categories got error")
	}

	return cates, nil
}

func (db *BlogDB) LoadCategoryByName(name string) (cate *Category, err error) {
	if name == "" {
		return nil, nil
	}

	cate = &Category{}
	if err = db.dbcli.GetCol(CATEGORY_COL_NAME).Find(bson.M{"name": name}).One(cate); err != nil && err != mgo.ErrNotFound {
		return nil, err
	}

	return cate, nil
}

func (db *BlogDB) LoadCategoryByURL(url string) (cate *Category, err error) {
	if url == "" {
		return nil, nil
	}

	cate = &Category{}
	if err = db.dbcli.GetCol(CATEGORY_COL_NAME).Find(bson.M{"url": url}).One(cate); err != nil && err != mgo.ErrNotFound {
		return nil, err
	}

	return cate, nil
}

func (db *BlogDB) IsNameExists(name string) (bool, error) {
	n, err := db.dbcli.GetCol(POST_COL_NAME).Find(bson.M{"post_name": name}).Count()
	if err != nil {
		libs.Logger.Error("try to count post_name got error", zap.Error(err))
		return false, err
	}

	return n != 0, nil
}

// NewPost insert new post
//   * title: post title
//   * name: post url
//   * md: post markdown content
//   * ptype: post type, markdown/slide
func (db *BlogDB) NewPost(authorID bson.ObjectId, title, name, md, ptype string) (post *Post, err error) {
	if isExists, err := db.IsNameExists(name); err != nil {
		return nil, err
	} else if isExists {
		return nil, fmt.Errorf("post name `%v` already exists", name)
	}

	ts := time.Now()
	p := &Post{
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

	if utils.Settings.GetBool("dry") {
		libs.Logger.Info("insert post",
			zap.String("title", p.Title),
			zap.String("name", p.Name),
			// zap.String("markdown", p.Markdown),
			// zap.String("content", p.Content),
		)
	} else {
		if err = db.dbcli.GetCol(POST_COL_NAME).Insert(p); err != nil {
			return nil, errors.Wrap(err, "try to insert post got error")
		}
	}

	return p, nil
}

var ErrLogin = errors.New("Password Or Username Incorrect")

func (db *BlogDB) ValidateLogin(account, password string) (u *User, err error) {
	libs.Logger.Debug("ValidateLogin", zap.String("account", account))
	u = &User{}
	if err := db.dbcli.GetCol(USER_COL_NAME).Find(bson.M{"account": account}).One(u); err != nil {
		if err == mgo.ErrNotFound {
			return nil, fmt.Errorf("user notfound")
		}

		return nil, err
	}

	if utils.ValidatePasswordHash([]byte(u.Password), []byte(password)) {
		libs.Logger.Debug("user login", zap.String("user", u.Account))
		return u, nil
	}

	return nil, ErrLogin
}

var supporttedTypes = map[string]struct{}{
	"markdown": {},
}

// UpdatePostCategory change blog post's category
func (db *BlogDB) UpdatePostCategory(name, category string) (p *Post, err error) {
	c := new(Category)
	if err = db.GetCategoriesCol().Find(bson.M{"name": category}).One(c); err != nil {
		return nil, errors.Wrapf(err, "load category `%s`", category)
	}

	p = new(Post)
	if err = db.GetPostsCol().Find(bson.M{"post_name": name}).One(p); err != nil {
		return nil, errors.Wrapf(err, "load post by name `%s`", name)
	}

	if p.Category == c.ID {
		return p, nil
	}

	p.Category = c.ID
	if err = db.GetPostsCol().UpdateId(p.ID, bson.M{
		"$set": bson.M{
			"category": c.ID,
		},
	}); err != nil {
		return nil, errors.Wrapf(err, "update post `%s` category", p.Name)
	}

	libs.Logger.Info("updated post category", zap.String("post", p.Name), zap.String("category", c.Name))
	return p, nil
}

func (db *BlogDB) UpdatePost(user *User, name string, title string, md string, typeArg string) (p *Post, err error) {
	p = &Post{}
	typeArg = strings.ToLower(typeArg)
	if _, ok := supporttedTypes[typeArg]; !ok {
		return nil, fmt.Errorf("type `%v` not supportted", typeArg)
	}
	if err = db.GetPostsCol().Find(bson.M{"post_name": name}).One(p); err != nil {
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

	if err = db.dbcli.GetCol(POST_COL_NAME).UpdateId(p.ID, p); err != nil {
		return nil, errors.Wrap(err, "try to update post got error")
	}

	libs.Logger.Info("updated post", zap.String("post", p.Name), zap.String("user", user.Account))
	return p, nil
}
