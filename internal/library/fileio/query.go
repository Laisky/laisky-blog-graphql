package fileio

import (
	"context"
	"encoding/base64"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/library/models"
	"github.com/Laisky/laisky-blog-graphql/internal/library/toolpolicy"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	"github.com/Laisky/laisky-blog-graphql/library"
)

// QueryResolver implements the read-only FileIO and memory GraphQL fields.
type QueryResolver struct{ *Resolver }

// FileStat returns live metadata, including the CAS token a mutation needs.
func (r *QueryResolver) FileStat(ctx context.Context, project, path string,
	plugin *models.MemoryPlugin,
) (*models.FileIOStatResult, error) {
	if r.plugin == nil {
		return nil, unavailable("file_stat")
	}
	auth, err := r.authorize(ctx)
	if err != nil {
		return nil, err
	}
	path = normalizePath(path)
	if err := validateTarget(project, path); err != nil {
		return nil, err
	}
	startAt := time.Now().UTC()
	log := logger(ctx, "file_stat").With(zap.String(fieldProject, project))
	result, statErr := r.plugin.Stat(withPlugin(ctx, plugin), auth, project, path)
	r.record(ctx, log, auth, "file_stat", map[string]any{fieldProject: project, fieldPath: path}, startAt, statErr)
	if statErr != nil {
		return nil, graphQLError(statErr, "stat file")
	}
	return &models.FileIOStatResult{
		Exists: result.Exists, Type: entryType(result.Type), Size: library.NewBigInt(result.Size),
		CreatedAt: optionalTime(result.CreatedAt), UpdatedAt: optionalTime(result.UpdatedAt),
		Version: optionalString(result.Version),
	}, nil
}

// FileRead returns content and its version from one snapshot. Passing
// expected_version pins the range read to a single content generation.
func (r *QueryResolver) FileRead(ctx context.Context, project, path string, offset, length *library.BigInt,
	expectedVersion *string, plugin *models.MemoryPlugin,
) (*models.FileIOReadResult, error) {
	if r.plugin == nil {
		return nil, unavailable("file_read")
	}
	auth, err := r.authorize(ctx)
	if err != nil {
		return nil, err
	}
	path = normalizePath(path)
	if err := validateTarget(project, path); err != nil {
		return nil, err
	}
	conditions, err := preconditions(expectedVersion, nil)
	if err != nil {
		return nil, err
	}
	readOffset, readLength := int64(0), int64(-1)
	if offset != nil {
		readOffset = offset.Int64()
	}
	if length != nil {
		readLength = length.Int64()
	}
	svc, readCtx, err := r.conditional(withPlugin(ctx, plugin), auth, project, path, "", files.FileOperationRead, conditions)
	if err != nil {
		return nil, err
	}
	startAt := time.Now().UTC()
	log := logger(ctx, "file_read").With(zap.String(fieldProject, project))
	result, readErr := svc.Read(readCtx, auth, project, path, readOffset, readLength)
	r.record(ctx, log, auth, "file_read", map[string]any{fieldProject: project, fieldPath: path,
		"offset": readOffset, "length": readLength}, startAt, readErr)
	if readErr != nil {
		return nil, graphQLError(readErr, "read file")
	}
	return &models.FileIOReadResult{Content: result.Content, ContentEncoding: result.ContentEncoding,
		Version: optionalString(result.Version)}, nil
}

// FileList returns one bounded page of entries under a path.
func (r *QueryResolver) FileList(ctx context.Context, project, path string, depth, limit *int,
	plugin *models.MemoryPlugin,
) (*models.FileIOListResult, error) {
	if r.plugin == nil {
		return nil, unavailable("file_list")
	}
	auth, err := r.authorize(ctx)
	if err != nil {
		return nil, err
	}
	path = normalizePath(path)
	if err := validateTarget(project, path); err != nil {
		return nil, err
	}
	// The service clamps depth and limit against configured maxima; reject only
	// values that cannot express an intent at all.
	listDepth, err := optionalInt(depth, 0, 0, 64)
	if err != nil {
		return nil, err
	}
	listLimit, err := optionalInt(limit, 0, 0, 10000)
	if err != nil {
		return nil, err
	}
	startAt := time.Now().UTC()
	log := logger(ctx, "file_list").With(zap.String(fieldProject, project))
	result, listErr := r.plugin.List(withPlugin(ctx, plugin), auth, project, path, listDepth, listLimit)
	r.record(ctx, log, auth, "file_list", map[string]any{fieldProject: project, fieldPath: path,
		"depth": listDepth, fieldLimit: listLimit}, startAt, listErr)
	if listErr != nil {
		return nil, graphQLError(listErr, "list files")
	}
	entries := make([]*models.FileIOEntry, 0, len(result.Entries))
	for _, entry := range result.Entries {
		entries = append(entries, &models.FileIOEntry{
			Name: entry.Name, Path: entry.Path, Type: entryType(entry.Type),
			Size:      library.NewBigInt(entry.Size),
			CreatedAt: *library.NewDatetimeFromTime(entry.CreatedAt.UTC()),
			UpdatedAt: *library.NewDatetimeFromTime(entry.UpdatedAt.UTC()),
		})
	}
	return &models.FileIOListResult{Entries: entries, HasMore: result.HasMore}, nil
}

// FileSearch returns hybrid retrieval chunks. An empty result is an empty
// array, never null, so a client cannot mistake it for a backend failure.
func (r *QueryResolver) FileSearch(ctx context.Context, project, query, pathPrefix string, limit *int,
	plugin *models.MemoryPlugin,
) (*models.FileIOSearchResult, error) {
	if r.plugin == nil {
		return nil, unavailable("file_search")
	}
	auth, err := r.authorize(ctx)
	if err != nil {
		return nil, err
	}
	normalizedQuery, err := toolpolicy.Query(query)
	if err != nil {
		return nil, invalidArgument("query must be trimmed, non-empty, valid UTF-8 within 16384 bytes")
	}
	pathPrefix = normalizePath(pathPrefix)
	if err := validateTarget(project, pathPrefix); err != nil {
		return nil, err
	}
	searchLimit, err := optionalInt(limit, 0, 0, 1000)
	if err != nil {
		return nil, err
	}
	startAt := time.Now().UTC()
	log := logger(ctx, "file_search").With(zap.String(fieldProject, project), zap.Int("query_len", len(normalizedQuery)))
	result, searchErr := r.plugin.Search(withPlugin(ctx, plugin), auth, project, normalizedQuery, pathPrefix, searchLimit)
	r.record(ctx, log, auth, "file_search", map[string]any{fieldProject: project, "query": normalizedQuery,
		"path_prefix": pathPrefix, fieldLimit: searchLimit}, startAt, searchErr)
	if searchErr != nil {
		return nil, graphQLError(searchErr, "search files")
	}
	chunks := make([]*models.FileIOChunk, 0, len(result.Chunks))
	for _, chunk := range result.Chunks {
		chunks = append(chunks, &models.FileIOChunk{
			Project: optionalString(chunk.Project), FilePath: chunk.FilePath,
			FileSeekStartBytes: library.NewBigInt(chunk.FileSeekStartBytes),
			FileSeekEndBytes:   library.NewBigInt(chunk.FileSeekEndBytes),
			IsFullFile:         chunk.IsFullFile, ChunkContent: chunk.ChunkContent,
			FileSummary: optionalString(chunk.FileSummary), Score: chunk.Score,
		})
	}
	return &models.FileIOSearchResult{Chunks: chunks}, nil
}

// FileListVersions returns one bounded, newest-first page of immutable history.
// IDs stay exact decimal strings and are never live version tokens.
func (r *QueryResolver) FileListVersions(ctx context.Context, project, path string, limit *int, beforeID *string,
) (*models.FileIOHistoryPage, error) {
	if r.history == nil {
		return nil, unavailable("file_list_versions")
	}
	auth, err := r.authorize(ctx)
	if err != nil {
		return nil, err
	}
	path = normalizePath(path)
	if err := validateTarget(project, path); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, graphQLError(files.NewError(files.ErrCodeInvalidPath,
			"history requires an exact file path", false), "validate history path")
	}
	pageLimit, err := optionalInt(limit, defaultHistoryLimit, 1, maxHistoryLimit)
	if err != nil {
		return nil, err
	}
	var before uint64
	if beforeID != nil {
		before, err = files.ParseHistoryID(*beforeID)
		if err != nil {
			return nil, graphQLError(err, "validate history cursor")
		}
	}
	startAt := time.Now().UTC()
	log := logger(ctx, "file_list_versions").With(zap.String(fieldProject, project))
	page, listErr := r.history.ListVersionPage(ctx, auth, project, path, before, pageLimit)
	r.record(ctx, log, auth, "file_list_versions", map[string]any{fieldProject: project, fieldPath: path,
		fieldLimit: pageLimit, "before_id": beforeID}, startAt, listErr)
	if listErr != nil {
		return nil, graphQLError(listErr, "list file versions")
	}
	versions := make([]*models.FileIOHistoryEntry, 0, len(page.Versions))
	for _, entry := range page.Versions {
		versions = append(versions, &models.FileIOHistoryEntry{
			ID: entry.ID, Size: library.NewBigInt(entry.Size),
			CreatedAt: *library.NewDatetimeFromTime(entry.CreatedAt.UTC()),
		})
	}
	return &models.FileIOHistoryPage{Versions: versions, HasMore: page.HasMore,
		NextCursor: optionalString(page.NextCursor)}, nil
}

// FileReadVersion returns one immutable snapshot. Retained non-UTF-8 bytes come
// back base64-encoded rather than being silently repaired.
func (r *QueryResolver) FileReadVersion(ctx context.Context, project, path, historyID string,
) (*models.FileIOHistoryContent, error) {
	if r.history == nil {
		return nil, unavailable("file_read_version")
	}
	auth, err := r.authorize(ctx)
	if err != nil {
		return nil, err
	}
	path = normalizePath(path)
	if err := validateTarget(project, path); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, graphQLError(files.NewError(files.ErrCodeInvalidPath,
			"history requires an exact file path", false), "validate history path")
	}
	id, err := files.ParseHistoryID(historyID)
	if err != nil {
		return nil, graphQLError(err, "validate history id")
	}
	startAt := time.Now().UTC()
	log := logger(ctx, "file_read_version").With(zap.String(fieldProject, project))
	snapshot, readErr := r.history.ReadVersion(ctx, auth, project, path, id)
	r.record(ctx, log, auth, "file_read_version", map[string]any{fieldProject: project, fieldPath: path,
		fieldHistoryID: historyID}, startAt, readErr)
	if readErr != nil {
		return nil, graphQLError(readErr, "read file version")
	}
	return historyContent(snapshot), nil
}

// MemoryListDirWithAbstract lists memory directories with their abstracts.
func (r *QueryResolver) MemoryListDirWithAbstract(ctx context.Context, project, sessionID *string, path string,
	depth, limit *int, plugin *models.MemoryPlugin,
) (*models.MemoryDirectoryListing, error) {
	if r.memory == nil {
		return nil, unavailable("memory_list_dir_with_abstract")
	}
	auth, err := r.authorize(ctx)
	if err != nil {
		return nil, err
	}
	listDepth, err := optionalInt(depth, defaultListDepth, 1, 64)
	if err != nil {
		return nil, err
	}
	listLimit, err := optionalInt(limit, defaultListLimit, 1, 1000)
	if err != nil {
		return nil, err
	}
	request := mcpmemoryListRequest(project, sessionID, path, listDepth, listLimit)
	startAt := time.Now().UTC()
	log := logger(ctx, "memory_list_dir_with_abstract").With(zap.String(fieldProject, request.Project))
	response, listErr := r.memory.ListDirWithAbstract(withPlugin(ctx, plugin), auth, request)
	r.record(ctx, log, auth, "memory_list_dir_with_abstract", map[string]any{fieldProject: request.Project,
		"session_id": request.SessionID, fieldPath: request.Path, "depth": listDepth, fieldLimit: listLimit}, startAt, listErr)
	if listErr != nil {
		return nil, graphQLError(listErr, "list memory directories")
	}
	summaries := make([]*models.MemoryDirectorySummary, 0, len(response.Summaries))
	for _, summary := range response.Summaries {
		summaries = append(summaries, &models.MemoryDirectorySummary{
			Path: summary.Path, Abstract: summary.Abstract,
			UpdatedAt: summary.UpdatedAt, HasOverview: summary.HasOverview,
		})
	}
	return &models.MemoryDirectoryListing{Summaries: summaries}, nil
}

// historyContent encodes a snapshot without treating legacy bytes as UTF-8.
func historyContent(snapshot files.FileVersion) *models.FileIOHistoryContent {
	content, encoding := string(snapshot.Content), "utf-8"
	if !utf8.Valid(snapshot.Content) {
		content, encoding = base64.StdEncoding.EncodeToString(snapshot.Content), "base64"
	}
	return &models.FileIOHistoryContent{
		HistoryID: strconv.FormatUint(snapshot.ID, 10), Content: content, ContentEncoding: encoding,
		Size:      library.NewBigInt(snapshot.Size),
		CreatedAt: *library.NewDatetimeFromTime(snapshot.CreatedAt.UTC()),
	}
}
