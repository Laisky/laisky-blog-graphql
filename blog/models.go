package blog

import (
	"fmt"
	"time"

	"github.com/Laisky/go-utils"
	"github.com/Laisky/laisky-blog-graphql/models"
	"github.com/Laisky/zap"
	mgo "gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
)

type BlogDB struct {
	*models.DB
	users, posts *mgo.Collection
}

type Post struct {
	ID         bson.ObjectId `bson:"_id" json:"mongo_id"`
	CreatedAt  time.Time     `bson:"post_created_at" json:"created_at"`
	ModifiedAt time.Time     `bson:"post_updated_gmt" json:"modified_at"`
	Title      string        `bson:"post_title" json:"title"`
	Content    string        `bson:"post_content" json:"content"`
	Author     bson.ObjectId `bson:"post_author" json:"author"`
}

type User struct {
	ID       bson.ObjectId `bson:"_id" json:"mongo_id"`
	Username string        `bson:"username" json:"username"`
}

const (
	DB_NAME       = "blog"
	POST_COL_NAME = "posts"
	USER_COL_NAME = "users"
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
	return db, nil
}

func (t *BlogDB) LoadPosts(page, size int, tag, regexp string) (results []*Post, err error) {
	utils.Logger.Debug("LoadPosts",
		zap.Int("page", page), zap.Int("size", size),
		zap.String("tag", tag),
		zap.String("regexp", regexp),
	)

	if size > 100 || size < 0 {
		return nil, fmt.Errorf("size shoule in [0~100]")
	}

	results = []*Post{}
	var query = bson.M{}
	if tag != "" {
		query["post_tags"] = tag
	}

	if regexp != "" {
		query["post_content"] = bson.M{"$regex": bson.RegEx{regexp, "im"}}
	}

	if err = t.posts.Find(query).Sort("-_id").Skip(page * size).Limit(size).All(&results); err != nil {
		return nil, err
	}

	return results, nil
}

func (t *BlogDB) LoadUserById(uid bson.ObjectId) (user *User, err error) {
	utils.Logger.Debug("LoadUserById", zap.String("user_id", uid.Hex()))
	user = &User{}
	if err = t.users.FindId(uid).One(user); err != nil {
		return nil, err
	}
	return user, nil
}
