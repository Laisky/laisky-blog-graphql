package service

import (
	"context"

	"github.com/Laisky/errors/v2"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// historyPublicationMetadata contains only the trusted current publication fields needed for public archive access.
type historyPublicationMetadata struct {
	Hidden   bool   `bson:"hidden"`
	Password string `bson:"post_password"`
	Status   string `bson:"post_status"`
}

// requireRegisteredPostHistory accepts an archive ID and rejects it unless trusted post metadata authorizes public history access.
// It returns an error before any gateway request; registered public archives retain their authored HTML unchanged.
func (s *Blog) requireRegisteredPostHistory(ctx context.Context, fileID string) error {
	if s == nil || s.dao == nil {
		return errors.New("blog archive provenance is unavailable")
	}
	var publication historyPublicationMetadata
	err := s.dao.GetPostsCol().FindOne(ctx,
		bson.D{{Key: "arweave_id.id", Value: fileID}},
		options.FindOne().SetProjection(bson.D{
			{Key: "_id", Value: 0}, {Key: "hidden", Value: 1},
			{Key: "post_password", Value: 1}, {Key: "post_status", Value: 1},
		}),
	).Decode(&publication)
	if err != nil {
		return errors.Wrap(err, "post history archive is not registered")
	}
	if publication.Hidden || publication.Password != "" {
		return errors.New("post history is not publicly available")
	}
	// Legacy registered records without status remain readable, matching the existing public post reader.
	if publication.Status != "" && publication.Status != "publish" {
		return errors.New("post history is not published")
	}
	return nil
}
