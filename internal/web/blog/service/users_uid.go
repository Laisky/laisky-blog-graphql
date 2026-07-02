package service

import (
	"context"
	"strings"

	"github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
)

// LoadUserByUID loads a user by the external stable UID.
// It accepts a context and UUID string, returning the matching user.
func (s *Blog) LoadUserByUID(ctx context.Context, uid string) (user *model.User, err error) {
	uid = strings.TrimSpace(uid)
	if _, err = uuid.Parse(uid); err != nil {
		return nil, errors.Wrap(err, "parse user uid")
	}

	user = &model.User{}
	result := s.dao.GetUsersCol().FindOne(ctx, bson.D{{Key: "uid", Value: uid}})
	if err = result.Decode(user); err != nil {
		return nil, errors.Wrap(err, "decode user by uid")
	}

	if user, err = s.EnsureUserUID(ctx, user); err != nil {
		return nil, errors.Wrap(err, "ensure user uid")
	}

	return user, nil
}

// EnsureUserUID guarantees an existing user has an external stable UID.
// It accepts a context and user model, returning the updated user.
func (s *Blog) EnsureUserUID(ctx context.Context, user *model.User) (*model.User, error) {
	if user == nil {
		return nil, errors.New("user is nil")
	}
	if strings.TrimSpace(user.UID) != "" {
		return user, nil
	}

	uid := gutils.UUID7()
	now := gutils.Clock.GetUTCNow()
	result, err := s.dao.GetUsersCol().UpdateOne(ctx, bson.M{
		"_id": user.ID,
		"$or": []bson.M{
			{"uid": bson.M{"$exists": false}},
			{"uid": ""},
		},
	}, bson.M{
		"$set": bson.M{
			"uid":               uid,
			"post_modified_gmt": now,
		},
	})
	if err != nil {
		return nil, errors.Wrapf(err, "set uid for user %s", user.ID.Hex())
	}
	if result.ModifiedCount > 0 {
		user.UID = uid
		user.ModifiedAt = now
		return user, nil
	}

	reloaded := &model.User{}
	if err = s.dao.GetUsersCol().FindOne(ctx, bson.M{"_id": user.ID}).Decode(reloaded); err != nil {
		return nil, errors.Wrapf(err, "reload user %s after uid race", user.ID.Hex())
	}
	if strings.TrimSpace(reloaded.UID) == "" {
		return nil, errors.New("user uid is still empty")
	}

	return reloaded, nil
}
