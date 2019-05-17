package blog

import (
	"fmt"
	"time"

	"github.com/Laisky/go-utils"
	"github.com/Laisky/laisky-blog-graphql/models"
	"github.com/Laisky/zap"
	"github.com/pkg/errors"
	mgo "gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
)

type BlogDB struct {
	*models.DB
	users, posts, categories *mgo.Collection
}

type Post struct {
	ID         bson.ObjectId `bson:"_id" json:"mongo_id"`
	CreatedAt  time.Time     `bson:"post_created_at" json:"created_at"`
	ModifiedAt time.Time     `bson:"post_modified_gmt" json:"modified_at"`
	Title      string        `bson:"post_title" json:"title"`
	Content    string        `bson:"post_content" json:"content"`
	Markdown   string        `bson:"post_markdown" json:"markdown"`
	Author     bson.ObjectId `bson:"post_author" json:"author"`
	Password   string        `bson:"post_password", json:"password"`
	Category   bson.ObjectId `bson:"category", json:"category"`
	Tags       []string      `bson:"post_tags", json:"tags"`
}

type User struct {
	ID       bson.ObjectId `bson:"_id" json:"mongo_id"`
	Username string        `bson:"username" json:"username"`
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

func NewBlogDB(addr string) (db *BlogDB, err error) {
	db = &BlogDB{
		DB: &models.DB{},
	}
	if err = db.Dial(addr, DB_NAME); err != nil {
		return nil, err
	}

	db.posts = db.GetCol(POST_COL_NAME)
	db.users = db.GetCol(USER_COL_NAME)
	db.categories = db.GetCol(CATEGORY_COL_NAME)
	return db, nil
}

type BlogPostCfg struct {
	Page, Size, Length    int
	Tag, Regexp, Category string
}

func (t *BlogDB) LoadPosts(cfg *BlogPostCfg) (results []*Post, err error) {
	utils.Logger.Debug("LoadPosts",
		zap.Int("page", cfg.Page), zap.Int("size", cfg.Size),
		zap.String("tag", cfg.Tag),
		zap.String("regexp", cfg.Regexp),
	)

	if cfg.Size > 100 || cfg.Size < 0 {
		return nil, fmt.Errorf("size shoule in [0~100]")
	}

	var query bson.M
	if query, err = t.makeQuery(cfg); err != nil {
		return nil, errors.Wrap(err, "try to make query got error")
	}

	iter := t.posts.Find(query).Sort("-_id").Skip(cfg.Page * cfg.Size).Limit(cfg.Size).Iter()
	results = t.filtersPipeline(cfg, iter)

	return results, nil
}

func (t *BlogDB) makeQuery(cfg *BlogPostCfg) (query bson.M, err error) {
	utils.Logger.Debug("makeQuery",
		zap.String("category", cfg.Category),
	)
	query = bson.M{}

	// query tags
	if cfg.Tag != "" {
		query["post_tags"] = cfg.Tag
	}

	// query regexp
	if cfg.Regexp != "" {
		query["post_content"] = bson.M{"$regex": bson.RegEx{cfg.Regexp, "im"}}
	}

	// query category
	var cate *Category
	if cate, err = t.LoadCategoryByName(cfg.Category); err != nil {
		return nil, errors.Wrap(err, "category error")
	} else if cate != nil {
		query["category"] = cate.ID
	}

	return query, nil
}

func (t *BlogDB) filtersPipeline(cfg *BlogPostCfg, iter *mgo.Iter) (results []*Post) {
	result := &Post{}
	for iter.Next(result) {
		for _, f := range []func(*Post) bool{
			// filters pipeline
			passwordFilter,
			getContentLengthFilter(cfg.Length),
		} {
			if !f(result) {
				break
			}
		}

		results = append(results, result)
		result = &Post{}
	}

	return results
}

func passwordFilter(docu *Post) bool {
	if docu.Password != "" {
		return false
	}

	return true
}

func getContentLengthFilter(length int) func(*Post) bool {
	return func(docu *Post) bool {
		if length > 0 && len(docu.Content) > length {
			docu.Content = docu.Content[:length]
		}
		return true
	}
}

func (t *BlogDB) LoadUserById(uid bson.ObjectId) (user *User, err error) {
	utils.Logger.Debug("LoadUserById", zap.String("user_id", uid.Hex()))
	if uid == "" {
		return nil, nil
	}

	user = &User{}
	if err = t.users.FindId(uid).One(user); err != nil {
		return nil, err
	}
	return user, nil
}

func (t *BlogDB) LoadCategoryById(cateid bson.ObjectId) (cate *Category, err error) {
	utils.Logger.Debug("LoadCategoryById", zap.String("cate_id", cateid.Hex()))
	if cateid == "" {
		return nil, nil
	}

	cate = &Category{}
	if err = t.categories.FindId(cateid).One(cate); err != nil {
		return nil, err
	}
	return cate, nil
}

func (t *BlogDB) LoadCategoryByName(name string) (cate *Category, err error) {
	if name == "" {
		return nil, nil
	}

	cate = &Category{}
	if err = t.categories.Find(bson.M{"name": name}).One(cate); err != nil && err != mgo.ErrNotFound {
		return nil, err
	}

	return cate, nil
}
