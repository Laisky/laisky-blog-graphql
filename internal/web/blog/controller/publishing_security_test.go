package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	gconfig "github.com/Laisky/go-config/v2"
	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/gin-gonic/gin"
	jwtLib "github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"

	"github.com/Laisky/laisky-blog-graphql/internal/library/models"
	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/dao"
	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/service"
	"github.com/Laisky/laisky-blog-graphql/library/auth"
	"github.com/Laisky/laisky-blog-graphql/library/db/arweave"
	"github.com/Laisky/laisky-blog-graphql/library/jwt"
)

const publisherTestUID = "01999df0-0000-7000-8000-000000000001"
const publisherTestHTML = "<iframe src=\"https://example.test/embed\"></iframe>"

// publisherMockDB routes DAO requests exclusively through the Mongo driver's local wire mock.
type publisherMockDB struct{ db *mongo.Database }

// GetCol returns a mocked collection, without opening a network connection.
func (d publisherMockDB) GetCol(name string) *mongo.Collection { return d.db.Collection(name) }

// CurrentDB returns this test's mocked database.
func (d publisherMockDB) CurrentDB() *mongo.Database { return d.db }

// DB returns a database on this test's mock client.
func (d publisherMockDB) DB(name string) *mongo.Database { return d.db.Client().Database(name) }

// Close leaves cleanup to the owning test.
func (d publisherMockDB) Close(context.Context) error { return nil }

// publisherMockArchive captures archive publication payloads entirely in memory.
type publisherMockArchive struct{ posts []model.Post }

// Upload captures authored content and returns a dummy identifier without blockchain activity.
func (a *publisherMockArchive) Upload(_ context.Context, data []byte, _ ...arweave.UploadOption) (string, error) {
	var post model.Post
	if err := json.Unmarshal(data, &post); err != nil {
		return "", errors.Wrap(err, "decode local archive payload")
	}
	a.posts = append(a.posts, post)
	return strings.Repeat("A", 43), nil
}

// publisherFixture creates the actual service and resolver with local DAO/archive responses.
func publisherFixture(mt *mtest.T) (*MutationResolver, *publisherMockArchive) {
	mt.Helper()
	for range 6 {
		mt.AddMockResponses(mtest.CreateSuccessResponse())
	}
	archive := &publisherMockArchive{}
	svc, err := service.New(context.Background(), glog.Shared, dao.New(glog.Shared, publisherMockDB{mt.DB}, archive), nil)
	require.NoError(mt, err)
	mt.ClearEvents()
	return NewMutationResolver(svc), archive
}

// publisherContext signs only a dummy local bearer and attaches the actual Gin request context.
func publisherContext(t *testing.T, valid bool) context.Context {
	t.Helper()
	claims := &jwt.UserClaims{RegisteredClaims: jwtLib.RegisteredClaims{
		Subject: publisherTestUID, Issuer: jwt.SSOIssuer,
		ExpiresAt: jwtLib.NewNumericDate(time.Now().UTC().Add(time.Hour)),
	}, UID: publisherTestUID, Username: "ppcelery@gmail.com"}
	token, err := jwt.SignSSOToken(claims)
	require.NoError(t, err)
	if !valid {
		token = "invalid-local-bearer"
	}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "http://example.test/query", nil)
	ctx.Request.Header.Set("Authorization", "Bearer "+token)
	return context.WithValue(context.Background(), gmw.CtxKeyGin, ctx)
}

// publisherConfig isolates deterministic dummy signing configuration and disables dry-run shortcuts.
func publisherConfig(t *testing.T) {
	t.Helper()
	previousAuth := auth.Instance
	require.NoError(t, auth.Initialize([]byte("local-publisher-tests-only-0123456789")))
	t.Cleanup(func() { auth.Instance = previousAuth })
	for key, value := range map[string]any{"settings.secret": "local-publisher-tests-only-0123456789", "settings.web.sso_jwt.private_key": "", "dry": false} {
		previous := gconfig.Shared.Get(key)
		gconfig.Shared.Set(key, value)
		t.Cleanup(func() { gconfig.Shared.Set(key, previous) })
	}
}

// publisherMutation invokes one of the three actual public write branches.
func publisherMutation(resolver *MutationResolver, ctx context.Context, operation string) (*model.Post, error) {
	title, markdown, category := "Local authored article", publisherTestHTML, "target"
	ptype := models.BlogPostTypeMarkdown
	post := models.NewBlogPost{Name: "local-post", Title: &title, Markdown: &markdown, Type: &ptype}
	switch operation {
	case "create":
		return resolver.BlogCreatePost(ctx, post, models.LanguageZhCn)
	case "content":
		return resolver.BlogAmendPost(ctx, post, models.LanguageZhCn)
	default:
		post.Category = &category
		return resolver.BlogAmendPost(ctx, post, models.LanguageZhCn)
	}
}

// publisherUserResponse supplies persisted authority, independently of claims submitted by the caller.
func publisherUserResponse(mt *mtest.T, id primitive.ObjectID, account, status string) bson.D {
	return mtest.CreateCursorResponse(0, mt.DB.Name()+".users", mtest.FirstBatch, bson.D{
		{Key: "_id", Value: id}, {Key: "uid", Value: publisherTestUID}, {Key: "account", Value: account}, {Key: "status", Value: status},
	})
}

// publisherWriteResponses supplies all subsequent write results so a missing gate can complete publication.
func publisherWriteResponses(mt *mtest.T, operation string, author primitive.ObjectID) {
	mt.Helper()
	switch operation {
	case "create":
		mt.AddMockResponses(mtest.CreateCursorResponse(0, mt.DB.Name()+".posts", mtest.FirstBatch, bson.D{{Key: "n", Value: 0}}), mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}))
	case "content":
		mt.AddMockResponses(mtest.CreateCursorResponse(0, mt.DB.Name()+".posts", mtest.FirstBatch, bson.D{
			{Key: "_id", Value: primitive.NewObjectID()}, {Key: "post_author", Value: author}, {Key: "post_name", Value: "local-post"},
		}), mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}, bson.E{Key: "nModified", Value: 1}))
	default:
		mt.AddMockResponses(mtest.CreateCursorResponse(0, mt.DB.Name()+".categories", mtest.FirstBatch, bson.D{{Key: "_id", Value: primitive.NewObjectID()}}),
			mtest.CreateCursorResponse(0, mt.DB.Name()+".posts", mtest.FirstBatch, bson.D{
				{Key: "_id", Value: primitive.NewObjectID()}, {Key: "post_author", Value: author}, {Key: "post_name", Value: "local-post"},
			}), mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}, bson.E{Key: "nModified", Value: 1}))
	}
}

// TestPublicPublishingRejectsNonAdmin reproduces low-trust active/legacy authors publishing and archiving HTML.
func TestPublicPublishingRejectsNonAdmin(t *testing.T) {
	publisherConfig(t)
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	for _, status := range []string{"active", ""} {
		for _, operation := range []string{"create", "content", "category"} {
			mt.Run(operation+"/"+status, func(mt *mtest.T) {
				resolver, archive := publisherFixture(mt)
				id := primitive.NewObjectID()
				mt.AddMockResponses(publisherUserResponse(mt, id, "ordinary@example.test", status))
				publisherWriteResponses(mt, operation, id)
				post, err := publisherMutation(resolver, publisherContext(mt.T, true), operation)
				mt.Logf("operation=%s returned_post=%t archive_calls=%d mongo_commands=%d", operation, post != nil, len(archive.posts), len(mt.GetAllStartedEvents()))
				require.ErrorContains(mt, err, "administrator")
				require.Nil(mt, post)
				require.Empty(mt, archive.posts, "denied callers must not archive data")
				events := mt.GetAllStartedEvents()
				require.Len(mt, events, 1, "only the authoritative account lookup is permitted")
				require.Equal(mt, "find", events[0].CommandName)
				require.Equal(mt, "users", events[0].Command.Lookup("find").StringValue())
			})
		}
	}
}

// TestPublicPublishingAdminControls verifies the existing admin can publish authored embeds, with ownership retained.
func TestPublicPublishingAdminControls(t *testing.T) {
	publisherConfig(t)
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	for _, operation := range []string{"create", "content", "category"} {
		mt.Run(operation, func(mt *mtest.T) {
			resolver, archive := publisherFixture(mt)
			id := primitive.NewObjectID()
			mt.AddMockResponses(publisherUserResponse(mt, id, "ppcelery@gmail.com", "active"))
			publisherWriteResponses(mt, operation, id)
			post, err := publisherMutation(resolver, publisherContext(mt.T, true), operation)
			require.NoError(mt, err)
			require.Equal(mt, id, post.Author)
			if operation == "category" {
				require.Empty(mt, archive.posts)
				return
			}
			require.Len(mt, archive.posts, 1)
			require.Contains(mt, post.Content, publisherTestHTML)
			require.Equal(mt, post.Content, archive.posts[0].Content)
			if operation == "create" {
				require.Equal(mt, "publish", post.Status)
			}
		})
	}
}

// TestPublicPublishingAdminCannotAmendAnotherAuthor verifies administration does not grant other-author content write access.
func TestPublicPublishingAdminCannotAmendAnotherAuthor(t *testing.T) {
	publisherConfig(t)
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	for _, operation := range []string{"content"} {
		mt.Run(operation, func(mt *mtest.T) {
			resolver, archive := publisherFixture(mt)
			mt.AddMockResponses(publisherUserResponse(mt, primitive.NewObjectID(), "ppcelery@gmail.com", "active"))
			publisherWriteResponses(mt, operation, primitive.NewObjectID())
			post, err := publisherMutation(resolver, publisherContext(mt.T, true), operation)
			require.ErrorContains(mt, err, "belong")
			require.Nil(mt, post)
			require.Empty(mt, archive.posts)
			for _, event := range mt.GetAllStartedEvents() {
				require.Equal(mt, "find", event.CommandName)
			}
		})
	}
}

// TestPublicPublishingInactiveAdminDenied verifies active status remains required even for the existing admin identity.
func TestPublicPublishingInactiveAdminDenied(t *testing.T) {
	publisherConfig(t)
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	for _, operation := range []string{"create", "content", "category"} {
		mt.Run(operation, func(mt *mtest.T) {
			resolver, archive := publisherFixture(mt)
			mt.AddMockResponses(publisherUserResponse(mt, primitive.NewObjectID(), "ppcelery@gmail.com", "pending"))
			post, err := publisherMutation(resolver, publisherContext(mt.T, true), operation)
			require.ErrorIs(mt, err, model.ErrInvalidCredentials)
			require.Nil(mt, post)
			require.Empty(mt, archive.posts)
			require.Len(mt, mt.GetAllStartedEvents(), 1)
		})
	}
}

// TestPublicPublishingAccountLookupFailsClosed verifies missing and unavailable accounts cannot reach article I/O.
func TestPublicPublishingAccountLookupFailsClosed(t *testing.T) {
	publisherConfig(t)
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	for _, operation := range []string{"create", "content", "category"} {
		for _, failure := range []string{"missing", "unavailable"} {
			mt.Run(operation+"/"+failure, func(mt *mtest.T) {
				resolver, archive := publisherFixture(mt)
				if failure == "missing" {
					mt.AddMockResponses(mtest.CreateCursorResponse(0, mt.DB.Name()+".users", mtest.FirstBatch))
				} else {
					mt.AddMockResponses(mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 2, Message: "local account store unavailable"}))
				}
				post, err := publisherMutation(resolver, publisherContext(mt.T, true), operation)
				require.Error(mt, err)
				require.Nil(mt, post)
				require.Empty(mt, archive.posts)
				require.Len(mt, mt.GetAllStartedEvents(), 1)
			})
		}
	}
}

// TestPublicPublishingAdminCanCurateOtherAuthorCategory preserves the existing Admin category-curation capability.
func TestPublicPublishingAdminCanCurateOtherAuthorCategory(t *testing.T) {
	publisherConfig(t)
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("category remains editable by admin", func(mt *mtest.T) {
		resolver, archive := publisherFixture(mt)
		owner := primitive.NewObjectID()
		mt.AddMockResponses(publisherUserResponse(mt, primitive.NewObjectID(), "ppcelery@gmail.com", "active"))
		publisherWriteResponses(mt, "category", owner)
		post, err := publisherMutation(resolver, publisherContext(mt.T, true), "category")
		require.NoError(mt, err)
		require.Equal(mt, owner, post.Author)
		require.Empty(mt, archive.posts)
	})
}

// TestPublicPublishingInvalidBearerDenied verifies unauthenticated and invalid bearer requests never reach account or article storage.
func TestPublicPublishingInvalidBearerDenied(t *testing.T) {
	publisherConfig(t)
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	for _, operation := range []string{"create", "content", "category"} {
		for _, bearer := range []string{"absent", "invalid"} {
			mt.Run(operation+"/"+bearer, func(mt *mtest.T) {
				resolver, archive := publisherFixture(mt)
				ctx := publisherContext(mt.T, false)
				if bearer == "absent" {
					gctx, ok := gmw.GetGinCtxFromStdCtx(ctx)
					require.True(mt, ok)
					gctx.Request.Header.Del("Authorization")
				}
				post, err := publisherMutation(resolver, ctx, operation)
				require.Error(mt, err)
				require.Nil(mt, post)
				require.Empty(mt, archive.posts)
				require.Empty(mt, mt.GetAllStartedEvents())
			})
		}
	}
}

// TestPublicPublishingAdminRichContentTypes preserves the existing HTML and Slide authoring paths.
func TestPublicPublishingAdminRichContentTypes(t *testing.T) {
	publisherConfig(t)
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	for _, ptype := range []models.BlogPostType{models.BlogPostTypeHTML, models.BlogPostTypeSlide} {
		mt.Run(ptype.String(), func(mt *mtest.T) {
			resolver, archive := publisherFixture(mt)
			id := primitive.NewObjectID()
			mt.AddMockResponses(publisherUserResponse(mt, id, "ppcelery@gmail.com", ""))
			publisherWriteResponses(mt, "create", id)
			title, markdown := "Local authored rich content", publisherTestHTML
			post, err := resolver.BlogCreatePost(publisherContext(mt.T, true), models.NewBlogPost{
				Name: "local-post", Title: &title, Markdown: &markdown, Type: &ptype,
			}, models.LanguageZhCn)
			require.NoError(mt, err)
			require.Equal(mt, ptype.String(), post.Type)
			require.Contains(mt, post.Content, publisherTestHTML)
			require.Equal(mt, "publish", post.Status)
			require.Len(mt, archive.posts, 1)
			require.Equal(mt, post.Content, archive.posts[0].Content)
		})
	}
}

// TestPublicPublishingAdminTranslation preserves author-owned English amendments and archive snapshots.
func TestPublicPublishingAdminTranslation(t *testing.T) {
	publisherConfig(t)
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("English content amendment", func(mt *mtest.T) {
		resolver, archive := publisherFixture(mt)
		id := primitive.NewObjectID()
		mt.AddMockResponses(publisherUserResponse(mt, id, "ppcelery@gmail.com", "active"))
		publisherWriteResponses(mt, "content", id)
		title, markdown, ptype := "Local English article", publisherTestHTML, models.BlogPostTypeMarkdown
		post, err := resolver.BlogAmendPost(publisherContext(mt.T, true), models.NewBlogPost{
			Name: "local-post", Title: &title, Markdown: &markdown, Type: &ptype,
		}, models.LanguageEnUs)
		require.NoError(mt, err)
		require.Equal(mt, title, post.I18N.EnUs.PostTitle)
		require.Contains(mt, post.I18N.EnUs.PostContent, publisherTestHTML)
		require.Len(mt, archive.posts, 1)
		require.Equal(mt, post.I18N.EnUs, archive.posts[0].I18N.EnUs)
	})
}
