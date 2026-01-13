// Package cmd command line
package cmd

import (
	"context"
	"encoding/xml"
	"io"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	gcmd "github.com/Laisky/go-utils/v6/cmd"
	"github.com/Laisky/zap"
	"github.com/spf13/cobra"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	blogModel "github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// DisqusXML represents the root element of Disqus export XML
type DisqusXML struct {
	XMLName    xml.Name        `xml:"disqus"`
	Categories []DisqusCategory `xml:"category"`
	Threads    []DisqusThread   `xml:"thread"`
	Posts      []DisqusPost     `xml:"post"`
}

// DisqusCategory represents a Disqus category element
type DisqusCategory struct {
	ID        string `xml:"id,attr"`
	Forum     string `xml:"forum"`
	Title     string `xml:"title"`
	IsDefault bool   `xml:"isDefault"`
}

// DisqusThread represents a Disqus thread element (corresponds to a blog post)
type DisqusThread struct {
	// DsqID is the Disqus internal thread ID
	DsqID     string       `xml:"id,attr"`
	ID        string       `xml:"id"`
	Forum     string       `xml:"forum"`
	Link      string       `xml:"link"`
	Title     string       `xml:"title"`
	Message   string       `xml:"message"`
	CreatedAt string       `xml:"createdAt"`
	Author    DisqusAuthor `xml:"author"`
	IsClosed  bool         `xml:"isClosed"`
	IsDeleted bool         `xml:"isDeleted"`
}

// DisqusPost represents a Disqus post element (a comment)
type DisqusPost struct {
	// DsqID is the Disqus internal post ID
	DsqID     string         `xml:"id,attr"`
	ID        string         `xml:"id"`
	Message   string         `xml:"message"`
	CreatedAt string         `xml:"createdAt"`
	IsDeleted bool           `xml:"isDeleted"`
	IsSpam    bool           `xml:"isSpam"`
	Author    DisqusAuthor   `xml:"author"`
	Thread    DisqusThreadRef `xml:"thread"`
	Parent    *DisqusParentRef `xml:"parent"`
}

// DisqusAuthor represents the author of a thread or post
type DisqusAuthor struct {
	Name        string `xml:"name"`
	IsAnonymous bool   `xml:"isAnonymous"`
	Username    string `xml:"username"`
}

// DisqusThreadRef references a thread by its Disqus ID
type DisqusThreadRef struct {
	DsqID string `xml:"id,attr"`
}

// DisqusParentRef references a parent post by its Disqus ID
type DisqusParentRef struct {
	DsqID string `xml:"id,attr"`
}

// importConfig holds the configuration for the import command
type importConfig struct {
	DisqusFile string
	DBURI      string
	DryRun     bool
}

var importCMD = &cobra.Command{
	Use:   "import",
	Short: "import data from external sources",
	Long:  `Import data from external sources into the database`,
	Args:  gcmd.NoExtraArgs,
}

var importCommentsCMD = &cobra.Command{
	Use:   "comments",
	Short: "import comments from Disqus export",
	Long: `Import comments from a Disqus XML export file into the MongoDB database.

This command reads the Disqus export XML file, parses threads (blog posts) and posts (comments),
and imports them into the blog database. It matches threads to existing blog posts using the
post_name field extracted from the Disqus thread link.

Example usage:
  go run main.go import comments --disqus_file=disqus_exported_data.xml --db_uri=mongodb://user:pwd@addr:port/dbname

The command will:
1. Parse the Disqus XML export file
2. Build a mapping of Disqus thread IDs to blog post names
3. Look up corresponding blog posts in the database
4. Import comments with proper parent-child relationships
5. Create comment users as needed`,
	Args: gcmd.NoExtraArgs,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		cfg := importConfig{
			DisqusFile: cmd.Flag("disqus_file").Value.String(),
			DBURI:      cmd.Flag("db_uri").Value.String(),
			DryRun:     cmd.Flag("dry").Value.String() == "true",
		}

		if err := runImportComments(ctx, cfg); err != nil {
			log.Logger.Panic("import comments", zap.Error(err))
		}
	},
}

func init() {
	rootCMD.AddCommand(importCMD)
	importCMD.AddCommand(importCommentsCMD)

	importCommentsCMD.Flags().String("disqus_file", "", "path to the Disqus XML export file (required)")
	importCommentsCMD.Flags().String("db_uri", "", "MongoDB connection URI (format: mongodb://user:pwd@addr:port/dbname) (required)")
	if err := importCommentsCMD.MarkFlagRequired("disqus_file"); err != nil {
		log.Logger.Panic("mark flag required", zap.Error(err))
	}
	if err := importCommentsCMD.MarkFlagRequired("db_uri"); err != nil {
		log.Logger.Panic("mark flag required", zap.Error(err))
	}
}

// runImportComments orchestrates the import of Disqus comments into MongoDB
func runImportComments(ctx context.Context, cfg importConfig) error {
	logger := log.Logger.Named("import-comments")
	logger.Info("starting Disqus comments import",
		zap.String("disqus_file", cfg.DisqusFile),
		zap.Bool("dry_run", cfg.DryRun),
	)

	// Parse the Disqus XML file
	disqusData, err := parseDisqusXML(cfg.DisqusFile)
	if err != nil {
		return errors.Wrap(err, "parse Disqus XML")
	}

	logger.Info("parsed Disqus XML",
		zap.Int("threads", len(disqusData.Threads)),
		zap.Int("posts", len(disqusData.Posts)),
		zap.Int("categories", len(disqusData.Categories)),
	)

	// Connect to MongoDB
	mongoClient, err := connectMongoDB(ctx, cfg.DBURI)
	if err != nil {
		return errors.Wrap(err, "connect to MongoDB")
	}
	defer func() {
		if err := mongoClient.Disconnect(ctx); err != nil {
			logger.Error("disconnect MongoDB", zap.Error(err))
		}
	}()

	// Extract database name from URI
	dbName, err := extractDBName(cfg.DBURI)
	if err != nil {
		return errors.Wrap(err, "extract database name")
	}
	db := mongoClient.Database(dbName)

	// Build thread ID to post name mapping
	threadToPostName := buildThreadToPostNameMap(disqusData.Threads)
	logger.Info("built thread to post name mapping",
		zap.Int("mappings", len(threadToPostName)),
	)

	// Look up blog posts and build post name to ObjectID mapping
	postNameToID, err := lookupBlogPosts(ctx, db, threadToPostName)
	if err != nil {
		return errors.Wrap(err, "lookup blog posts")
	}
	logger.Info("found matching blog posts",
		zap.Int("matched", len(postNameToID)),
	)

	// Import comments
	stats, err := importComments(ctx, db, disqusData, threadToPostName, postNameToID, cfg.DryRun)
	if err != nil {
		return errors.Wrap(err, "import comments")
	}

	logger.Info("import completed",
		zap.Int("imported", stats.Imported),
		zap.Int("skipped_deleted", stats.SkippedDeleted),
		zap.Int("skipped_spam", stats.SkippedSpam),
		zap.Int("skipped_no_post", stats.SkippedNoPost),
		zap.Int("users_created", stats.UsersCreated),
	)

	return nil
}

// parseDisqusXML reads and parses a Disqus XML export file
func parseDisqusXML(filePath string) (*DisqusXML, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, errors.Wrapf(err, "open file %s", filePath)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, errors.Wrap(err, "read file")
	}

	var disqus DisqusXML
	if err := xml.Unmarshal(data, &disqus); err != nil {
		return nil, errors.Wrap(err, "unmarshal XML")
	}

	return &disqus, nil
}

// connectMongoDB establishes a connection to MongoDB
func connectMongoDB(ctx context.Context, uri string) (*mongo.Client, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		return nil, errors.Wrap(err, "connect to MongoDB")
	}

	// Verify connection
	if err := client.Ping(ctx, nil); err != nil {
		return nil, errors.Wrap(err, "ping MongoDB")
	}

	return client, nil
}

// extractDBName extracts the database name from a MongoDB URI
func extractDBName(uri string) (string, error) {
	// Parse the URI
	parsed, err := url.Parse(uri)
	if err != nil {
		return "", errors.Wrap(err, "parse URI")
	}

	// The database name is the path without the leading slash
	dbName := strings.TrimPrefix(parsed.Path, "/")
	if dbName == "" {
		return "", errors.New("database name not found in URI")
	}

	// Remove query parameters if present
	if idx := strings.Index(dbName, "?"); idx != -1 {
		dbName = dbName[:idx]
	}

	return dbName, nil
}

// buildThreadToPostNameMap builds a mapping from Disqus thread ID to blog post name
func buildThreadToPostNameMap(threads []DisqusThread) map[string]string {
	result := make(map[string]string)

	for _, thread := range threads {
		if thread.IsDeleted {
			continue
		}

		postName := extractPostNameFromLink(thread.Link)
		if postName == "" {
			continue
		}

		result[thread.DsqID] = postName
	}

	return result
}

// extractPostNameFromLink extracts the post_name from a Disqus thread link
// Expected format: http://blog.laisky.com/p/{post_name}
func extractPostNameFromLink(link string) string {
	parsed, err := url.Parse(link)
	if err != nil {
		return ""
	}

	// The path should be like /p/{post_name}
	pathParts := strings.Split(parsed.Path, "/")
	for i, part := range pathParts {
		if part == "p" && i+1 < len(pathParts) {
			// URL decode the post name
			postName, err := url.PathUnescape(pathParts[i+1])
			if err != nil {
				return pathParts[i+1]
			}
			return postName
		}
	}

	// Try to get the last path segment as fallback
	baseName := path.Base(parsed.Path)
	if baseName != "." && baseName != "/" {
		postName, err := url.PathUnescape(baseName)
		if err != nil {
			return baseName
		}
		return postName
	}

	return ""
}

// lookupBlogPosts looks up blog posts in the database and returns a mapping of post_name to ObjectID
func lookupBlogPosts(ctx context.Context, db *mongo.Database, threadToPostName map[string]string) (map[string]primitive.ObjectID, error) {
	// Collect unique post names
	postNames := make(map[string]struct{})
	for _, name := range threadToPostName {
		postNames[name] = struct{}{}
	}

	// Convert to slice for query
	names := make([]string, 0, len(postNames))
	for name := range postNames {
		names = append(names, name)
	}

	// Query posts collection
	col := db.Collection("posts")
	cursor, err := col.Find(ctx, bson.M{"post_name": bson.M{"$in": names}})
	if err != nil {
		return nil, errors.Wrap(err, "find posts")
	}
	defer cursor.Close(ctx)

	result := make(map[string]primitive.ObjectID)
	for cursor.Next(ctx) {
		var post blogModel.Post
		if err := cursor.Decode(&post); err != nil {
			return nil, errors.Wrap(err, "decode post")
		}
		result[post.Name] = post.ID
	}

	if err := cursor.Err(); err != nil {
		return nil, errors.Wrap(err, "cursor error")
	}

	return result, nil
}

// importStats tracks statistics for the import process
type importStats struct {
	Imported       int
	SkippedDeleted int
	SkippedSpam    int
	SkippedNoPost  int
	UsersCreated   int
}

// importComments imports Disqus posts as comments into MongoDB
func importComments(
	ctx context.Context,
	db *mongo.Database,
	disqusData *DisqusXML,
	threadToPostName map[string]string,
	postNameToID map[string]primitive.ObjectID,
	dryRun bool,
) (*importStats, error) {
	logger := log.Logger.Named("import-comments")
	stats := &importStats{}

	commentsColl := db.Collection(blogModel.Comment{}.Collection())
	usersColl := db.Collection(blogModel.CommentUser{}.Collection())

	// Build a mapping from Disqus post ID to MongoDB comment ID for parent references
	disqusIDToCommentID := make(map[string]primitive.ObjectID)

	// Track created users to avoid duplicates
	usernameToUser := make(map[string]blogModel.CommentUser)

	// First pass: create all comments without parent references
	for _, post := range disqusData.Posts {
		// Skip deleted comments
		if post.IsDeleted {
			stats.SkippedDeleted++
			continue
		}

		// Skip spam comments
		if post.IsSpam {
			stats.SkippedSpam++
			continue
		}

		// Get the post name from thread ID
		postName, ok := threadToPostName[post.Thread.DsqID]
		if !ok {
			stats.SkippedNoPost++
			logger.Debug("no post name mapping for thread",
				zap.String("thread_id", post.Thread.DsqID),
			)
			continue
		}

		// Get the blog post ID
		postID, ok := postNameToID[postName]
		if !ok {
			stats.SkippedNoPost++
			logger.Debug("no blog post found for post name",
				zap.String("post_name", postName),
			)
			continue
		}

		// Get or create user
		user, isNew, err := getOrCreateUser(ctx, usersColl, post.Author, usernameToUser, dryRun)
		if err != nil {
			return nil, errors.Wrap(err, "get or create user")
		}
		if isNew {
			stats.UsersCreated++
		}

		// Parse created time
		createdAt, err := parseDisqusTime(post.CreatedAt)
		if err != nil {
			logger.Warn("failed to parse created time, using current time",
				zap.String("time", post.CreatedAt),
				zap.Error(err),
			)
			createdAt = time.Now()
		}

		// Create comment
		commentID := primitive.NewObjectID()
		comment := blogModel.Comment{
			ID:         commentID,
			CreatedAt:  createdAt,
			UpdatedAt:  createdAt,
			Author:     user,
			PostID:     postID,
			Content:    cleanHTMLContent(post.Message),
			IsApproved: true, // Disqus comments are already moderated
		}

		// Store mapping for parent references
		disqusIDToCommentID[post.DsqID] = commentID

		if !dryRun {
			_, err := commentsColl.InsertOne(ctx, comment)
			if err != nil {
				return nil, errors.Wrapf(err, "insert comment %s", post.DsqID)
			}
		}

		stats.Imported++
		logger.Debug("imported comment",
			zap.String("disqus_id", post.DsqID),
			zap.String("post_name", postName),
			zap.String("author", user.Name),
		)
	}

	// Second pass: update parent references
	for _, post := range disqusData.Posts {
		if post.IsDeleted || post.IsSpam || post.Parent == nil {
			continue
		}

		commentID, ok := disqusIDToCommentID[post.DsqID]
		if !ok {
			continue
		}

		parentID, ok := disqusIDToCommentID[post.Parent.DsqID]
		if !ok {
			logger.Debug("parent comment not found",
				zap.String("post_id", post.DsqID),
				zap.String("parent_id", post.Parent.DsqID),
			)
			continue
		}

		if !dryRun {
			_, err := commentsColl.UpdateOne(
				ctx,
				bson.M{"_id": commentID},
				bson.M{"$set": bson.M{"parent_id": parentID}},
			)
			if err != nil {
				return nil, errors.Wrapf(err, "update parent reference for comment %s", post.DsqID)
			}
		}

		logger.Debug("updated parent reference",
			zap.String("comment_id", commentID.Hex()),
			zap.String("parent_id", parentID.Hex()),
		)
	}

	return stats, nil
}

// getOrCreateUser retrieves an existing user or creates a new one
func getOrCreateUser(
	ctx context.Context,
	coll *mongo.Collection,
	author DisqusAuthor,
	cache map[string]blogModel.CommentUser,
	dryRun bool,
) (blogModel.CommentUser, bool, error) {
	// Use username as the key, fallback to name if anonymous
	key := author.Username
	if key == "" || author.IsAnonymous {
		key = author.Name
	}

	// Check cache first
	if user, ok := cache[key]; ok {
		return user, false, nil
	}

	// Check if user exists in database
	var existingUser blogModel.CommentUser
	err := coll.FindOne(ctx, bson.M{"name": author.Name}).Decode(&existingUser)
	if err == nil {
		cache[key] = existingUser
		return existingUser, false, nil
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return blogModel.CommentUser{}, false, errors.Wrap(err, "find user")
	}

	// Create new user
	now := time.Now()
	user := blogModel.CommentUser{
		ID:        primitive.NewObjectID(),
		CreatedAt: now,
		Name:      author.Name,
		Email:     "", // Disqus export doesn't include email
	}

	if !dryRun {
		_, err := coll.InsertOne(ctx, user)
		if err != nil {
			return blogModel.CommentUser{}, false, errors.Wrap(err, "insert user")
		}
	}

	cache[key] = user
	return user, true, nil
}

// parseDisqusTime parses a Disqus timestamp in ISO 8601 format
func parseDisqusTime(s string) (time.Time, error) {
	// Disqus uses format: 2015-03-25T14:10:41Z
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		// Try without timezone
		t, err = time.Parse("2006-01-02T15:04:05", s)
		if err != nil {
			return time.Time{}, errors.Wrapf(err, "parse time %s", s)
		}
	}
	return t.UTC(), nil
}

// cleanHTMLContent strips HTML tags and CDATA wrappers from content
func cleanHTMLContent(content string) string {
	// The content is typically wrapped in CDATA and contains HTML
	// Remove common HTML tags and clean up whitespace
	content = strings.TrimSpace(content)

	// Remove basic HTML paragraph tags
	content = strings.ReplaceAll(content, "<p>", "")
	content = strings.ReplaceAll(content, "</p>", "\n")
	content = strings.ReplaceAll(content, "<br>", "\n")
	content = strings.ReplaceAll(content, "<br/>", "\n")
	content = strings.ReplaceAll(content, "<br />", "\n")

	// Trim extra whitespace
	content = strings.TrimSpace(content)

	return content
}
