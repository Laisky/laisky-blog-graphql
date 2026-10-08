package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"

	"github.com/Laisky/laisky-blog-graphql/internal/library/models"
	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/dao"
	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
)

// historyTestTransport accepts a request and returns an entirely local gateway response.
type historyTestTransport func(*http.Request) (*http.Response, error)

// RoundTrip invokes the local transport function with the supplied request and returns its response.
func (f historyTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// TestPostHistoryRejectsUnregisteredArchive verifies an unknown public archive cannot become trusted article HTML.
func TestPostHistoryRejectsUnregisteredArchive(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("unknown file cannot cross history trust boundary", func(mt *mtest.T) {
		id := strings.Repeat("A", 43)
		mt.AddMockResponses(mtest.CreateCursorResponse(0, mt.DB.Name()+".posts", mtest.FirstBatch))
		service := &Blog{logger: glog.Shared, dao: dao.New(glog.Shared, queryContractDB{db: mt.DB}, nil)}
		originalClient := httpcli
		requests := 0
		httpcli = &http.Client{Transport: historyTestTransport(func(request *http.Request) (*http.Response, error) {
			requests++
			require.Equal(mt, "GET", request.Method)
			require.Equal(mt, "/"+id, request.URL.Path)
			body := `{"name":"local-canary","title":"Unregistered archive","content":"<iframe srcdoc=\"<script>parent.document.body.dataset.historyCanary=1</script>\"></iframe>","type":"slide"}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), ContentLength: int64(len(body))}, nil
		})}
		mt.Cleanup(func() { httpcli = originalClient })
		post, err := service.LoadPostHistory(context.Background(), id, models.LanguageZhCn)
		require.Error(mt, err, "arbitrary gateway JSON must not be trusted as a published blog archive")
		require.Nil(mt, post)
		require.Zero(mt, requests, "reject unknown IDs before any gateway request")
		event := mt.GetStartedEvent()
		require.NotNil(mt, event)
		var command bson.M
		require.NoError(mt, bson.Unmarshal(event.Command, &command))
		require.Equal(mt, bson.M{"arweave_id.id": id}, command["filter"])
	})
}

// TestPostHistoryRegisteredArchivesPreserveAuthoredHTML verifies registered current, legacy, compressed, and translated snapshots retain their exact HTML.
func TestPostHistoryRegisteredArchivesPreserveAuthoredHTML(t *testing.T) {
	const authored = `<section class="slides" style="color:blue"><iframe src="https://example.test/embed" allowfullscreen></iframe><video controls src="/local.mp4"></video><svg><path d="M0 0"></path></svg><math><mi>x</mi></math></section>`
	id := strings.Repeat("B", 43)
	post := model.Post{Name: "local-author-slide", Title: "Author slide", Type: "slide", Content: authored, Status: "publish"}
	post.I18N.EnUs = model.PostI18NLanguage{PostContent: authored + "<p>English</p>", PostTitle: "English slide", PostMenu: "<nav>Author menu</nav>", PostMarkdown: "Author source"}
	modern, err := json.Marshal(post)
	require.NoError(t, err)
	legacy, err := json.Marshal(map[string]any{
		"_id":         map[string]string{"$oid": "000000000000000000000001"},
		"post_author": map[string]string{"$oid": "000000000000000000000002"},
		"category":    map[string]string{"$oid": "000000000000000000000003"},
		"post_name":   post.Name, "post_title": post.Title, "post_type": post.Type, "post_content": authored,
	})
	require.NoError(t, err)
	var compressed bytes.Buffer
	compressed.WriteString("gz::")
	writer := gzip.NewWriter(&compressed)
	_, err = writer.Write(modern)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	for _, fixture := range []struct {
		name     string
		body     []byte
		language models.Language
		expected string
	}{
		{"current JSON", modern, models.LanguageZhCn, authored},
		{"legacy JSON", legacy, models.LanguageZhCn, authored},
		{"compressed JSON", compressed.Bytes(), models.LanguageZhCn, authored},
		{"English translation", modern, models.LanguageEnUs, post.I18N.EnUs.PostContent},
	} {
		mt.Run(fixture.name, func(mt *mtest.T) {
			mt.AddMockResponses(mtest.CreateCursorResponse(0, mt.DB.Name()+".posts", mtest.FirstBatch,
				bson.D{{Key: "_id", Value: primitive.NewObjectID()}, {Key: "post_status", Value: "publish"}, {Key: "arweave_id", Value: bson.A{bson.D{{Key: "id", Value: id}}}}},
			))
			service := &Blog{logger: glog.Shared, dao: dao.New(glog.Shared, queryContractDB{db: mt.DB}, nil)}
			originalClient := httpcli
			requests := 0
			httpcli = &http.Client{Transport: historyTestTransport(func(request *http.Request) (*http.Response, error) {
				requests++
				// Membership must already have succeeded before the intercepted gateway can return article HTML.
				assertHistoryMembershipQuery(mt, id)
				require.Equal(mt, "/"+id, request.URL.Path)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(fixture.body)), Header: make(http.Header), ContentLength: int64(len(fixture.body))}, nil
			})}
			mt.Cleanup(func() { httpcli = originalClient })
			loaded, err := service.LoadPostHistory(context.Background(), id, fixture.language)
			require.NoError(mt, err)
			require.Equal(mt, fixture.expected, loaded.Content)
			require.Equal(mt, "slide", loaded.Type)
			require.Equal(mt, post.Name, loaded.Name)
			require.Equal(mt, 1, requests)
		})
	}
}

// assertHistoryMembershipQuery verifies the actual Mongo wire command is limited to the requested registered archive ID.
func assertHistoryMembershipQuery(mt *mtest.T, id string) {
	mt.Helper()
	event := mt.GetStartedEvent()
	require.NotNil(mt, event, "archive authorization must precede any gateway request")
	var command bson.M
	require.NoError(mt, bson.Unmarshal(event.Command, &command))
	require.Equal(mt, bson.M{"arweave_id.id": id}, command["filter"])
	require.Equal(mt, bson.M{"_id": int32(0), "hidden": int32(1), "post_password": int32(1), "post_status": int32(1)}, command["projection"])
}

// TestPostHistoryLookupFailureStopsGateway verifies unrelated, unpublished, unknown, and unavailable metadata cannot fetch foreign content.
func TestPostHistoryLookupFailureStopsGateway(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	for _, name := range []string{"unrelated archive", "never published archive", "unknown archive", "database unavailable"} {
		mt.Run(name, func(mt *mtest.T) {
			id := strings.Repeat("C", 43)
			if name == "database unavailable" {
				mt.AddMockResponses(mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 2, Message: "local metadata lookup rejected"}))
			} else {
				mt.AddMockResponses(mtest.CreateCursorResponse(0, mt.DB.Name()+".posts", mtest.FirstBatch))
			}
			service := &Blog{logger: glog.Shared, dao: dao.New(glog.Shared, queryContractDB{db: mt.DB}, nil)}
			originalClient := httpcli
			httpcli = &http.Client{Transport: historyTestTransport(func(*http.Request) (*http.Response, error) {
				mt.Fatal("rejected archive must never reach gateway transport")
				return nil, nil
			})}
			mt.Cleanup(func() { httpcli = originalClient })
			loaded, err := service.LoadPostHistory(context.Background(), id, models.LanguageZhCn)
			require.Error(mt, err)
			require.Nil(mt, loaded)
			assertHistoryMembershipQuery(mt, id)
		})
	}
}

// TestPostHistoryInvalidIDStopsAllIO verifies an invalid route ID fails before database lookup or gateway transport.
func TestPostHistoryInvalidIDStopsAllIO(t *testing.T) {
	service := &Blog{}
	originalClient := httpcli
	httpcli = &http.Client{Transport: historyTestTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid archive must never reach gateway transport")
		return nil, nil
	})}
	t.Cleanup(func() { httpcli = originalClient })
	loaded, err := service.LoadPostHistory(context.Background(), "not-an-archive", models.LanguageZhCn)
	require.Error(t, err)
	require.ErrorContains(t, err, "validate file id")
	require.Nil(t, loaded)
}

// TestPostHistoryCurrentVisibilityStopsGateway verifies registered protected or explicitly unpublished posts cannot bypass current publication controls.
func TestPostHistoryCurrentVisibilityStopsGateway(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	for _, fixture := range []struct {
		name     string
		metadata bson.D
	}{
		{"hidden", bson.D{{Key: "hidden", Value: true}, {Key: "post_status", Value: "publish"}}},
		{"password protected", bson.D{{Key: "post_password", Value: "local-test-marker"}, {Key: "post_status", Value: "publish"}}},
		{"draft", bson.D{{Key: "post_status", Value: "draft"}}},
		{"future publication", bson.D{{Key: "post_status", Value: "future"}}},
	} {
		mt.Run(fixture.name, func(mt *mtest.T) {
			id := strings.Repeat("D", 43)
			mt.AddMockResponses(mtest.CreateCursorResponse(0, mt.DB.Name()+".posts", mtest.FirstBatch, fixture.metadata))
			service := &Blog{logger: glog.Shared, dao: dao.New(glog.Shared, queryContractDB{db: mt.DB}, nil)}
			originalClient := httpcli
			httpcli = &http.Client{Transport: historyTestTransport(func(*http.Request) (*http.Response, error) {
				mt.Fatal("protected archive must never reach gateway transport")
				return nil, nil
			})}
			mt.Cleanup(func() { httpcli = originalClient })
			loaded, err := service.LoadPostHistory(context.Background(), id, models.LanguageZhCn)
			require.Error(mt, err)
			require.Nil(mt, loaded)
			assertHistoryMembershipQuery(mt, id)
		})
	}
}

// TestPostHistoryLegacyRegistrationWithoutStatus preserves trusted legacy archive records because the existing public reader does not require a status field.
func TestPostHistoryLegacyRegistrationWithoutStatus(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("registered legacy metadata without status", func(mt *mtest.T) {
		id := strings.Repeat("E", 43)
		mt.AddMockResponses(mtest.CreateCursorResponse(0, mt.DB.Name()+".posts", mtest.FirstBatch, bson.D{{Key: "_id", Value: primitive.NewObjectID()}}))
		service := &Blog{logger: glog.Shared, dao: dao.New(glog.Shared, queryContractDB{db: mt.DB}, nil)}
		originalClient := httpcli
		httpcli = &http.Client{Transport: historyTestTransport(func(*http.Request) (*http.Response, error) {
			assertHistoryMembershipQuery(mt, id)
			body := `{"name":"legacy-public","title":"Legacy public article","content":"<iframe src=\"https://example.test/embed\"></iframe>","type":"slide"}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), ContentLength: int64(len(body))}, nil
		})}
		mt.Cleanup(func() { httpcli = originalClient })
		loaded, err := service.LoadPostHistory(context.Background(), id, models.LanguageZhCn)
		require.NoError(mt, err)
		require.Equal(mt, `<iframe src="https://example.test/embed"></iframe>`, loaded.Content)
		require.Equal(mt, "slide", loaded.Type)
	})
}
