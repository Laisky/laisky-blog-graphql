package service

// -------------------------------------
// A comment service for blog according to replace disqus
// -------------------------------------

import (
	"context"
	"time"

	"github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v5"
	"github.com/Laisky/zap"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/Laisky/laisky-blog-graphql/internal/library/models"
	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
	"github.com/Laisky/laisky-blog-graphql/library"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

const (
	collComments    = "comments"
	collCommentLike = "comment_likes"
)

// mapDBCommentToAPIComment converts a model.Comment to models.Comment for API response
func (s *Blog) mapDBCommentToAPIComment(comment *model.Comment) *models.Comment {
	result := &models.Comment{
		ID:            comment.ID.Hex(),
		Content:       comment.Content,
		AuthorName:    comment.Author.Name,
		AuthorWebsite: comment.Author.Website,
		PostID:        comment.PostID.Hex(),
		CreatedAt:     *library.NewDatetimeFromTime(comment.CreatedAt),
	}

	if comment.ParentID != nil {
		parentIDStr := comment.ParentID.Hex()
		result.ParentID = &parentIDStr
	}

	return result
}

// buildCommentTree organizes comments into a tree structure
func (s *Blog) buildCommentTree(comments []*model.Comment) []*model.Comment {
	// Map to store comments by ID
	commentMap := make(map[string]*model.Comment)

	// First pass: add all comments to map
	for _, comment := range comments {
		commentMap[comment.ID.Hex()] = comment
	}

	// Second pass: organize into tree
	rootComments := []*model.Comment{}
	for _, comment := range comments {
		if comment.ParentID == nil {
			// This is a root comment
			rootComments = append(rootComments, comment)
		} else {
			// This is a child comment
			parentID := comment.ParentID.Hex()
			if parent, ok := commentMap[parentID]; ok {
				parent.Replies = append(parent.Replies, comment)
			}
		}
	}

	return rootComments
}

// BlogComments retrieves comments for a specific post
func (s *Blog) BlogComments(ctx context.Context,
	postName string,
	page *models.Pagination,
	sort *models.Sort) ([]*models.Comment, error) {

	// Build filter for approved comments on this post
	filter := bson.M{
		"post_name":   postName,
		"is_approved": true,
	}

	// Configure sorting
	sortOpts := bson.D{{Key: "created_at", Value: -1}} // Default: newest first
	if sort != nil {
		sortDir := -1 // DESC by default
		if sort.Order == models.SortOrderAsc {
			sortDir = 1
		}
		sortOpts = bson.D{{Key: sort.SortBy, Value: sortDir}}
	}

	// Configure pagination - Page starts at 0, not 1
	findOpts := options.Find().SetSort(sortOpts)

	if page != nil {
		// Fixed: use page * size instead of (page - 1) * size
		findOpts.SetSkip(int64(page.Page * page.Size))
		findOpts.SetLimit(int64(page.Size))
	}

	// Execute query - use the correct collection
	cursor, err := s.dao.GetPostCommentCol().Find(ctx, filter, findOpts)
	if err != nil {
		return nil, errors.Wrap(err, "failed to find comments")
	}
	defer cursor.Close(ctx)

	// Decode results
	var dbComments []*model.Comment
	if err = cursor.All(ctx, &dbComments); err != nil {
		return nil, errors.Wrap(err, "failed to decode comments")
	}

	// Organize comments into a tree structure
	rootComments := s.buildCommentTree(dbComments)

	// Convert to API format
	apiComments := make([]*models.Comment, 0, len(rootComments))
	for _, comment := range rootComments {
		apiComments = append(apiComments, s.mapDBCommentToAPIComment(comment))
	}

	return apiComments, nil
}

// BlogCommentCount counts comments for a specific post
func (s *Blog) BlogCommentCount(ctx context.Context, postName string) (int, error) {
	// Count approved comments - use the correct collection
	count, err := s.dao.GetPostCommentCol().CountDocuments(ctx, bson.M{
		"post_name":   postName,
		"is_approved": true,
	})
	if err != nil {
		return 0, errors.Wrap(err, "failed to count comments")
	}

	return int(count), nil
}

// BlogCreateComment creates a new comment for a post
func (s *Blog) BlogCreateComment(ctx context.Context,
	postName string,
	content string,
	authorName string,
	authorEmail string,
	authorWebsite *string,
	parentID *string) (*models.Comment, error) {

	// Input validation
	if content == "" {
		return nil, errors.New("comment content cannot be empty")
	}

	if authorName == "" {
		return nil, errors.New("author name cannot be empty")
	}

	if authorEmail == "" {
		return nil, errors.New("author email cannot be empty")
	}

	// Verify that post exists
	post := new(model.Post)
	err := s.dao.GetPostsCol().FindOne(ctx, bson.M{"post_name": postName}).Decode(post)
	if err != nil {
		return nil, errors.Wrap(err, "failed to check post existence")
	}

	// Create comment user
	commentUser := model.CommentUser{
		Name:  authorName,
		Email: authorEmail,
	}
	if authorWebsite != nil {
		commentUser.Website = authorWebsite
	}

	// Create new comment
	now := time.Now()
	comment := &model.Comment{
		ID:         primitive.NewObjectID(),
		CreatedAt:  now,
		UpdatedAt:  now,
		Author:     commentUser,
		PostID:     post.ID,
		Content:    content,
		IsApproved: false, // Require approval by default
	}

	// Handle parent comment if provided
	if parentID != nil {
		parentObjID, err := primitive.ObjectIDFromHex(*parentID)
		if err != nil {
			return nil, errors.Wrapf(err, "invalid parent comment ID: %s", *parentID)
		}

		// Verify that parent comment exists and belongs to the same post
		var parentComment model.Comment
		err = s.dao.GetPostCommentCol().FindOne(ctx, bson.M{
			"_id":     parentObjID,
			"post_id": post.ID,
		}).Decode(&parentComment)

		if err != nil {
			if err == mongo.ErrNoDocuments {
				return nil, errors.New("parent comment not found or doesn't belong to the specified post")
			}
			return nil, errors.Wrap(err, "failed to fetch parent comment")
		}

		comment.ParentID = &parentObjID
	}

	// Insert the comment into the database
	_, err = s.dao.GetPostCommentCol().InsertOne(ctx, comment)
	if err != nil {
		return nil, errors.Wrap(err, "failed to insert comment")
	}

	// Log the new comment
	log.Logger.Info("new comment created",
		zap.String("post_id", post.ID.Hex()),
		zap.String("comment_id", comment.ID.Hex()),
		zap.String("author", authorName))

	// Return the mapped comment
	return s.mapDBCommentToAPIComment(comment), nil
}

// BlogToggleCommentLike toggles a like for a comment
func (s *Blog) BlogToggleCommentLike(ctx context.Context, commentID string) (*models.Comment, error) {
	// Get the user from context (assuming you have auth middleware)
	user, err := s.ValidateAndGetUser(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "authentication required to like comments")
	}

	// Convert string ID to ObjectID
	commentObjID, err := primitive.ObjectIDFromHex(commentID)
	if err != nil {
		return nil, errors.Wrapf(err, "invalid comment ID: %s", commentID)
	}

	// Check if comment exists
	var comment model.Comment
	err = s.dao.GetPostCommentCol().FindOne(ctx, bson.M{"_id": commentObjID}).Decode(&comment)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, errors.New("comment not found")
		}
		return nil, errors.Wrap(err, "failed to fetch comment")
	}

	// Check if user has already liked this comment
	filter := bson.M{
		"comment_id": commentObjID,
		"user_id":    user.ID,
	}

	// Start a session for transaction
	session, err := s.dao.StartSession()
	if err != nil {
		return nil, errors.Wrap(err, "failed to start MongoDB session")
	}
	defer session.EndSession(ctx)

	var updatedComment model.Comment
	err = mongo.WithSession(ctx, session, func(sc mongo.SessionContext) error {
		if err := session.StartTransaction(); err != nil {
			return errors.Wrap(err, "failed to start transaction")
		}

		// Try to find existing like
		var like model.CommentLike
		err = s.dao.GetPostCommentLike().FindOne(sc, filter).Decode(&like)

		if err == mongo.ErrNoDocuments {
			// User has not liked this comment yet - add a like
			like = model.CommentLike{
				ID:        primitive.NewObjectID(),
				CreatedAt: time.Now(),
				CommentID: commentObjID,
				UserID:    user.ID,
			}

			_, err = s.dao.GetPostCommentLike().InsertOne(sc, like)
			if err != nil {
				return errors.Wrap(err, "failed to insert like")
			}

			// Increment likes count
			update := bson.M{"$inc": bson.M{"likes": 1}}
			err = s.dao.GetPostCommentCol().FindOneAndUpdate(
				sc,
				bson.M{"_id": commentObjID},
				update,
				options.FindOneAndUpdate().SetReturnDocument(options.After),
			).Decode(&updatedComment)

			if err != nil {
				return errors.Wrap(err, "failed to update comment likes count")
			}
		} else if err != nil {
			return errors.Wrap(err, "failed to check existing like")
		} else {
			// User has already liked this comment - remove the like
			_, err = s.dao.GetPostCommentLike().DeleteOne(sc, bson.M{"_id": like.ID})
			if err != nil {
				return errors.Wrap(err, "failed to delete like")
			}

			// Decrement likes count
			update := bson.M{"$inc": bson.M{"likes": -1}}
			err = s.dao.GetPostCommentCol().FindOneAndUpdate(
				sc,
				bson.M{"_id": commentObjID},
				update,
				options.FindOneAndUpdate().SetReturnDocument(options.After),
			).Decode(&updatedComment)

			if err != nil {
				return errors.Wrap(err, "failed to update comment likes count")
			}
		}

		return session.CommitTransaction(sc)
	})

	if err != nil {
		return nil, errors.Wrap(err, "transaction failed")
	}

	return s.mapDBCommentToAPIComment(&updatedComment), nil
}

// BlogApproveComment approves a comment
func (s *Blog) BlogApproveComment(ctx context.Context, commentID string) (*models.Comment, error) {
	// Get the user from context and check if admin
	user, err := s.ValidateAndGetUser(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "authentication required to approve comments")
	}

	if !user.IsAdmin() {
		return nil, errors.New("only admins can approve comments")
	}

	// Convert string ID to ObjectID
	commentObjID, err := primitive.ObjectIDFromHex(commentID)
	if err != nil {
		return nil, errors.Wrapf(err, "invalid comment ID: %s", commentID)
	}

	// Update the comment approval status
	var updatedComment model.Comment
	err = s.dao.GetPostCommentCol().FindOneAndUpdate(
		ctx,
		bson.M{"_id": commentObjID},
		bson.M{"$set": bson.M{"is_approved": true, "updated_at": gutils.Clock.GetUTCNow()}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&updatedComment)

	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, errors.New("comment not found")
		}
		return nil, errors.Wrap(err, "failed to approve comment")
	}

	log.Logger.Info("comment approved",
		zap.String("comment_id", commentID),
		zap.String("admin", user.Username))

	return s.mapDBCommentToAPIComment(&updatedComment), nil
}

// BlogDeleteComment deletes a comment
func (s *Blog) BlogDeleteComment(ctx context.Context, commentID string) (*models.Comment, error) {
	// Get the user from context and check if admin
	user, err := s.ValidateAndGetUser(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "authentication required to delete comments")
	}

	if !user.IsAdmin() {
		return nil, errors.New("only admins can delete comments")
	}

	// Convert string ID to ObjectID
	commentObjID, err := primitive.ObjectIDFromHex(commentID)
	if err != nil {
		return nil, errors.Wrapf(err, "invalid comment ID: %s", commentID)
	}

	// Get the comment before deletion
	var comment model.Comment
	err = s.dao.GetPostCommentCol().FindOne(ctx, bson.M{"_id": commentObjID}).Decode(&comment)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, errors.New("comment not found")
		}
		return nil, errors.Wrap(err, "failed to fetch comment")
	}

	// Start a session for transaction
	session, err := s.dao.StartSession()
	if err != nil {
		return nil, errors.Wrap(err, "failed to start MongoDB session")
	}
	defer session.EndSession(ctx)

	err = mongo.WithSession(ctx, session, func(sc mongo.SessionContext) error {
		if err := session.StartTransaction(); err != nil {
			return errors.Wrap(err, "failed to start transaction")
		}

		// Delete the comment
		_, err = s.dao.GetPostCommentCol().DeleteOne(sc, bson.M{"_id": commentObjID})
		if err != nil {
			return errors.Wrap(err, "failed to delete comment")
		}

		// Delete all child comments
		_, err = s.dao.GetPostCommentCol().DeleteMany(sc, bson.M{"parent_id": commentObjID})
		if err != nil {
			return errors.Wrap(err, "failed to delete child comments")
		}

		// Delete all likes for this comment
		_, err = s.dao.GetPostCommentLike().DeleteMany(sc, bson.M{"comment_id": commentObjID})
		if err != nil {
			return errors.Wrap(err, "failed to delete comment likes")
		}

		return session.CommitTransaction(sc)
	})

	if err != nil {
		return nil, errors.Wrap(err, "transaction failed")
	}

	log.Logger.Info("comment deleted",
		zap.String("comment_id", commentID),
		zap.String("admin", user.Username))

	return s.mapDBCommentToAPIComment(&comment), nil
}
