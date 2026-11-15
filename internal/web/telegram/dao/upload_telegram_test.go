package dao

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Laisky/errors/v2"
	"github.com/Laisky/zap/zapcore"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	mongoLib "go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"

	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	"github.com/Laisky/laisky-blog-graphql/library/db/mongo"
)

type uploadMongoDB = mongo.DB

type uploadTestDB struct {
	uploadMongoDB
	database *mongoLib.Database
}

func (d uploadTestDB) GetCol(name string) *mongoLib.Collection { return d.database.Collection(name) }

// TestUploadFileWithTelegramUIDBackgroundMetadata exercises the complete caller
// path through Mongo decoding, billing, Arweave, caching and metadata insertion.
func TestUploadFileWithTelegramUIDBackgroundMetadata(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	for _, tc := range []struct {
		name                                                       string
		insertRefused, arweaveRefused, billingRefused, missingUser bool
	}{
		{name: "success"},
		{name: "metadata refused", insertRefused: true},
		{name: "arweave refused", arweaveRefused: true},
		{name: "billing refused", billingRefused: true},
		{name: "missing user", missingUser: true},
	} {
		mt.Run(tc.name, func(mt *mtest.T) {
			synctest.Test(mt.T, func(t *testing.T) {
				const uid int64 = 12345
				user := bson.D{{Key: "telegram_uid", Value: uid}, {Key: "oneapi_key", Value: "fixture-key"}}
				if tc.missingUser {
					mt.AddMockResponses(mtest.CreateCursorResponse(0, mt.DB.Name()+"."+colUploadUsers, mtest.FirstBatch))
				} else {
					mt.AddMockResponses(mtest.CreateCursorResponse(0, mt.DB.Name()+"."+colUploadUsers, mtest.FirstBatch, user))
				}
				if tc.insertRefused {
					mt.AddMockResponses(mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 121, Message: "metadata refused"}))
				} else {
					mt.AddMockResponses(mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}))
				}
				client, requests := uploadCacheFixture(t, false)
				ctx, observed := uploadLogger(t)
				ctx, cancel := context.WithCancel(ctx)
				defer cancel()

				var billRequests []*http.Request
				var billPayloads []map[string]any
				previousClient := http.DefaultClient
				http.DefaultClient = &http.Client{Transport: uploadRoundTripper(func(r *http.Request) (*http.Response, error) {
					var payload map[string]any
					if decodeErr := json.NewDecoder(r.Body).Decode(&payload); decodeErr != nil {
						return nil, decodeErr
					}
					billRequests = append(billRequests, r)
					billPayloads = append(billPayloads, payload)
					status := http.StatusOK
					if tc.billingRefused {
						status = http.StatusForbidden
					}
					return &http.Response{StatusCode: status, Header: make(http.Header),
						Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
				})}
				t.Cleanup(func() { http.DefaultClient = previousClient })

				ar := &fakeArweaveUploader{fileID: "tx-telegram"}
				if tc.arweaveRefused {
					ar.fileID, ar.err = "", errors.New("arweave refused")
				}
				upload := &Upload{db: uploadTestDB{database: mt.DB}, ar: ar, minio: client}
				fileID, err := upload.UploadFileWithTelegramUID(ctx, uid, []byte("telegram bytes"), "text/plain")
				cancel()
				failed := tc.missingUser || tc.billingRefused || tc.arweaveRefused
				if failed {
					require.Error(t, err)
					require.Empty(t, fileID)
				} else {
					require.NoError(t, err, "metadata persistence is best effort")
					require.Equal(t, "tx-telegram", fileID)
				}

				// All fixture operations are synchronous: draining runnable goroutines
				// completes both workers, rather than merely observing their first event.
				synctest.Wait()
				billCount := 1
				if tc.missingUser {
					billCount = 0
				}
				require.Len(t, billRequests, billCount, "accepted billing is single-phase, with no refund")
				if billCount != 0 {
					require.Equal(t, http.MethodPost, billRequests[0].Method)
					require.Equal(t, "/api/token/consume", billRequests[0].URL.Path)
					require.Equal(t, "Bearer fixture-key", billRequests[0].Header.Get("Authorization"))
					require.Equal(t, "single", billPayloads[0]["phase"])
					require.EqualValues(t, oneapi.PriceUploadFileMinimal.Int(), billPayloads[0]["add_used_quota"])
				}
				arCalls := 1
				if tc.missingUser || tc.billingRefused {
					arCalls = 0
				}
				require.Equal(t, arCalls, ar.calls)
				var inserts []bson.Raw
				for _, event := range mt.GetAllStartedEvents() {
					if event.CommandName == "insert" {
						require.Equal(t, colUploadFiles, event.Command.Lookup("insert").StringValue())
						inserts = append(inserts, event.Command.Lookup("documents").Array().Index(0).Value().Document())
					}
				}
				if failed {
					require.Empty(t, inserts)
					require.Empty(t, *requests)
					return
				}
				require.Len(t, inserts, 1)
				require.Equal(t, "tx-telegram", inserts[0].Lookup("file_id").StringValue())
				require.EqualValues(t, len("telegram bytes"), inserts[0].Lookup("file_size").Int32())
				require.Equal(t, uid, inserts[0].Lookup(fieldTelegramUid).Int64())
				require.WithinDuration(t, time.Now(), inserts[0].Lookup(fieldCreatedAt).Time(), time.Second)
				require.Len(t, *requests, 1)
				entries := observed.FilterMessage("save uploaded arweave file info").All()
				if tc.insertRefused {
					require.Len(t, entries, 1)
					require.Equal(t, zapcore.ErrorLevel, entries[0].Level)
				} else {
					require.Empty(t, entries)
				}
				require.NoError(t, err)
				require.Equal(t, "tx-telegram", fileID)
			})
		})
	}
}
