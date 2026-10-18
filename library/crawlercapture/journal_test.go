package crawlercapture

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// privateJournalTempDir applies the journal's exact permission contract to test-owned roots.
func privateJournalTempDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	require.NoError(t, os.Chmod(directory, 0700))
	return directory
}

// syntheticJournalSource never reads host identity, environment or build metadata.
func syntheticJournalSource() Source {
	return Source{Service: "synthetic-crawler", Hostname: "synthetic-host", ProcessRunID: "synthetic-process"}
}

// syntheticJournalRecord selects only the declared reproduction inputs.
func syntheticJournalRecord(input Blob) Record {
	return Record{
		Correlation: Correlation{TaskID: "synthetic-task", RequestID: "synthetic-request"},
		Failure:     Failure{Stage: "conversion", Code: "empty_markdown"},
		Request: Request{OriginalURL: CapturedString("https://example.invalid/original?q=unchanged#fragment"),
			SubmittedURL: CapturedString("https://example.invalid/submitted"), OutputMarkdown: true},
		Converter: Converter{Name: "synthetic-converter", Version: "synthetic-version", Input: input},
	}
}

// newSyntheticJournal keeps every fixture under a test-owned private directory.
func newSyntheticJournal(t *testing.T) *Journal {
	t.Helper()
	journal, err := NewJournal(filepath.Join(privateJournalTempDir(t), "private", "journal"), syntheticJournalSource())
	require.NoError(t, err)
	return journal
}

// readSyntheticJournal checks the reference against the exact published file bytes.
func readSyntheticJournal(t *testing.T, reference Reference) Record {
	t.Helper()
	require.Equal(t, "complete", reference.Status)
	require.NotEmpty(t, reference.Path)
	raw, err := os.ReadFile(reference.Path)
	require.NoError(t, err)
	require.EqualValues(t, len(raw), reference.Bytes)
	sum := sha256.Sum256(raw)
	require.Equal(t, hex.EncodeToString(sum[:]), reference.SHA256)
	record, err := Read(reference.Path)
	require.NoError(t, err)
	require.NoError(t, record.Validate())
	require.Equal(t, reference.Source, record.Source)
	return record
}

// requireJournalBlob compares the public value rather than its private source representation.
func requireJournalBlob(t *testing.T, expected, actual Blob) {
	t.Helper()
	require.Equal(t, expected.State, actual.State)
	require.Equal(t, expected.ByteLength, actual.ByteLength)
	require.Equal(t, expected.SHA256, actual.SHA256)
	require.Equal(t, expected.Bytes(), actual.Bytes())
}

// TestJournalRoundTripBytes distinguishes absent, empty, Unicode and invalid UTF-8.
func TestJournalRoundTripBytes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input Blob
	}{
		{name: "unicode and controls", input: CapturedString("<body>雪 😀 e\u0301\r\n\t\x00</body>")},
		{name: "invalid bytes", input: Captured([]byte{0xff, 0xfe, '<', 0, 0xc0, 0xaf, '>'})},
		{name: "captured empty", input: Captured(nil)},
		{name: "not reached", input: NotReached()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			journal := newSyntheticJournal(t)
			record := syntheticJournalRecord(tc.input)
			reference, err := journal.Write(record)
			require.NoError(t, err)
			got := readSyntheticJournal(t, reference)
			requireJournalBlob(t, tc.input, got.Converter.Input)
			if tc.input.State == "captured" {
				require.True(t, SameInput(tc.input, got.Converter.Input))
			}
			requireJournalBlob(t, record.Request.OriginalURL, got.Request.OriginalURL)
			require.Equal(t, record.Correlation, got.Correlation)
			require.Equal(t, record.Failure, got.Failure)
			require.Equal(t, NotReached(), got.RenderedHTML)
			require.Equal(t, NotReached(), got.Request.OriginalOutputMarkdown)
			require.Equal(t, "operator_configured_directory", got.Retention)
			if tc.input.State == "captured" {
				require.NotNil(t, got.Converter.Input.Data)
			} else {
				require.Nil(t, got.Converter.Input.Data)
			}
		})
	}
}

// TestJournalPreservesOriginalOption retains declared text independently of its parsed boolean.
func TestJournalPreservesOriginalOption(t *testing.T) {
	record := syntheticJournalRecord(CapturedString("<body></body>"))
	declared := false
	record.Request.OutputMarkdown = false
	record.Request.OutputMarkdownDeclared = true
	record.Request.OriginalOutputMarkdown = CapturedString(" false \n")
	record.Request.DeclaredOutputMarkdown = &declared
	reference, err := newSyntheticJournal(t).Write(record)
	require.NoError(t, err)
	got := readSyntheticJournal(t, reference)
	requireJournalBlob(t, record.Request.OriginalURL, got.Request.OriginalURL)
	requireJournalBlob(t, record.Request.SubmittedURL, got.Request.SubmittedURL)
	requireJournalBlob(t, record.Request.OriginalOutputMarkdown, got.Request.OriginalOutputMarkdown)
	require.Equal(t, record.Request.OutputMarkdown, got.Request.OutputMarkdown)
	require.Equal(t, record.Request.OutputMarkdownDeclared, got.Request.OutputMarkdownDeclared)
	require.False(t, *got.Request.DeclaredOutputMarkdown)
}

// TestJournalSnapshots prevents caller-owned bytes or later mutations changing the saved input.
func TestJournalSnapshots(t *testing.T) {
	data := []byte("original exact input")
	blob := Captured(data)
	data[0] = 'X'
	require.Equal(t, []byte("original exact input"), blob.Bytes())
	copyBytes := blob.Bytes()
	copyBytes[0] = 'Q'
	require.Equal(t, []byte("original exact input"), blob.Bytes(), "Bytes returns caller-owned data")
	record := syntheticJournalRecord(blob)
	record.Renderer = &Renderer{
		RequestID: "synthetic-exchange", Kind: "document", Method: "GET",
		URL:         CapturedString("https://example.invalid/failed"),
		DecodedBody: Captured([]byte{0xff, 0, 'x'}), DecodedBodyComplete: false, FailurePhase: "read",
	}
	reference, err := newSyntheticJournal(t).Write(record)
	require.NoError(t, err)
	before, err := os.ReadFile(reference.Path)
	require.NoError(t, err)
	record.Converter.Input = CapturedString("changed input")
	record.Request.OriginalURL = CapturedString("changed URL")
	record.Renderer.DecodedBody = Captured([]byte{1})
	record.Renderer.URL = CapturedString("changed exchange URL")
	record.Failure.Code = "changed"
	record.Correlation.TaskID = "changed"
	got := readSyntheticJournal(t, reference)
	require.Equal(t, []byte("original exact input"), got.Converter.Input.Data)
	require.Equal(t, []byte{0xff, 0, 'x'}, got.Renderer.DecodedBody.Data)
	require.Equal(t, "empty_markdown", got.Failure.Code)
	require.Equal(t, "synthetic-task", got.Correlation.TaskID)
	after, err := os.ReadFile(reference.Path)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

// TestJournalLargeInputs proves there is no alert-size or scanner-size truncation.
func TestJournalLargeInputs(t *testing.T) {
	for _, size := range []int{16*1024 + 37, 17*1024*1024 + 19} {
		t.Run(stringSizeName(size), func(t *testing.T) {
			data := make([]byte, size)
			for i := range data {
				data[i] = byte(i*31 + 17)
			}
			copy(data, []byte("<body>"))
			copy(data[len(data)-7:], []byte("</body>"))
			reference, err := newSyntheticJournal(t).Write(syntheticJournalRecord(Captured(data)))
			require.NoError(t, err)
			got := readSyntheticJournal(t, reference)
			require.EqualValues(t, size, got.Converter.Input.ByteLength)
			require.Equal(t, data, got.Converter.Input.Data)
			require.Equal(t, data[:32], got.Converter.Input.Data[:32])
			require.Equal(t, data[len(data)-32:], got.Converter.Input.Data[len(data)-32:])
		})
	}
}

// stringSizeName gives the two deliberate boundary fixtures stable readable names.
func stringSizeName(size int) string {
	if size > 16*1024*1024 {
		return "over 16 MiB"
	}
	return "over 16 KiB"
}

// TestJournalRejectsUnsealedOrChangedFiles rejects damaged data instead of returning a partial record.
func TestJournalRejectsUnsealedOrChangedFiles(t *testing.T) {
	journal := newSyntheticJournal(t)
	reference, err := journal.Write(syntheticJournalRecord(CapturedString("exact input")))
	require.NoError(t, err)
	original, err := os.ReadFile(reference.Path)
	require.NoError(t, err)
	change := func(field string, value any) []byte {
		var fields map[string]any
		require.NoError(t, json.Unmarshal(original, &fields))
		fields[field] = value
		raw, marshalErr := json.Marshal(fields)
		require.NoError(t, marshalErr)
		return raw
	}
	changeInput := func(field string, value any) []byte {
		var fields map[string]any
		require.NoError(t, json.Unmarshal(original, &fields))
		converter := fields["converter"].(map[string]any)
		input := converter["input"].(map[string]any)
		input[field] = value
		raw, marshalErr := json.Marshal(fields)
		require.NoError(t, marshalErr)
		return raw
	}
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{name: "truncated", raw: original[:len(original)/2]},
		{name: "trailing object", raw: append(bytes.Clone(original), []byte("{}")...)},
		{name: "trailing garbage", raw: append(bytes.Clone(original), []byte("garbage")...)},
		{name: "incomplete", raw: change("complete", false)},
		{name: "changed metadata", raw: change("correlation", map[string]any{"task_id": "other-task"})},
		{name: "unknown field", raw: change("unexpected_payload", "not permitted")},
		{name: "changed bytes", raw: changeInput("base64", base64.StdEncoding.EncodeToString([]byte("other input")))},
		{name: "changed length", raw: changeInput("byte_length", 1)},
		{name: "changed digest", raw: changeInput("sha256", hex.EncodeToString(make([]byte, 32)))},
		{name: "missing bytes", raw: changeInput("base64", nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(filepath.Dir(reference.Path), tc.name+".json")
			require.NoError(t, os.WriteFile(path, tc.raw, 0600))
			_, readErr := Read(path)
			require.Error(t, readErr)
		})
	}
	pending := filepath.Join(filepath.Dir(reference.Path), ".complete-but-pending.pending")
	require.NoError(t, os.WriteFile(pending, original, 0600))
	_, err = Read(pending)
	require.Error(t, err, "even complete bytes are not published under a pending name")
	got := readSyntheticJournal(t, reference)
	require.Equal(t, []byte("exact input"), got.Converter.Input.Data)
}

// TestJournalRejectsInvalidRecords never upgrades missing or corrupt inputs into complete captures.
func TestJournalRejectsInvalidRecords(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Record)
	}{
		{name: "missing failure stage", mutate: func(r *Record) { r.Failure.Stage = "" }},
		{name: "missing failure code", mutate: func(r *Record) { r.Failure.Code = "" }},
		{name: "corrupt input", mutate: func(r *Record) { r.Converter.Input.SHA256 = hex.EncodeToString(make([]byte, 32)) }},
		{name: "presence mismatch", mutate: func(r *Record) { r.Request.OutputMarkdownDeclared = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := syntheticJournalRecord(CapturedString("input"))
			tc.mutate(&record)
			reference, err := newSyntheticJournal(t).Write(record)
			require.Error(t, err)
			require.Equal(t, "write_failed", reference.Status)
			require.Empty(t, reference.Path)
		})
	}
}

// TestJournalPrivateModes checks both newly created directories and the published file.
func TestJournalPrivateModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission assertions")
	}
	journal := newSyntheticJournal(t)
	reference, err := journal.Write(syntheticJournalRecord(CapturedString("private input")))
	require.NoError(t, err)
	for _, path := range []string{filepath.Dir(reference.Path), filepath.Dir(filepath.Dir(reference.Path))} {
		info, statErr := os.Stat(path)
		require.NoError(t, statErr)
		require.Equal(t, os.FileMode(0700), info.Mode().Perm())
	}
	info, err := os.Stat(reference.Path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	require.NoError(t, os.Chmod(reference.Path, 0644))
	_, err = Read(reference.Path)
	require.Error(t, err)
}

// TestJournalRejectsUnsafePaths does not follow symlinks or repair unsafe existing directories.
func TestJournalRejectsUnsafePaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions and symlink fixtures")
	}
	base := privateJournalTempDir(t)
	real := filepath.Join(base, "real")
	require.NoError(t, os.Mkdir(real, 0700))
	alias := filepath.Join(base, "alias")
	require.NoError(t, os.Symlink(real, alias))
	file := filepath.Join(base, "existing-file")
	sentinel := []byte("existing file must remain unchanged")
	require.NoError(t, os.WriteFile(file, sentinel, 0600))
	unsafeLeaf := filepath.Join(base, "unsafe-leaf")
	require.NoError(t, os.Mkdir(unsafeLeaf, 0700))
	require.NoError(t, os.Chmod(unsafeLeaf, 0755))
	unsafeParent := filepath.Join(base, "unsafe-parent")
	require.NoError(t, os.Mkdir(unsafeParent, 0700))
	require.NoError(t, os.Chmod(unsafeParent, 0777))
	for _, tc := range []struct {
		name, path string
	}{
		{name: "existing regular file", path: file},
		{name: "symlink journal", path: alias},
		{name: "symlink ancestor", path: filepath.Join(alias, "child")},
		{name: "unsafe leaf permissions", path: unsafeLeaf},
		{name: "unsafe writable ancestor", path: filepath.Join(unsafeParent, "child")},
		{name: "filesystem root", path: string(filepath.Separator)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			journal, err := NewJournal(tc.path, syntheticJournalSource())
			require.NoError(t, err, "configuration is lazy; Write validates the path")
			reference, err := journal.Write(syntheticJournalRecord(CapturedString("input")))
			require.Error(t, err)
			require.Equal(t, "write_failed", reference.Status)
			require.Empty(t, reference.Path)
		})
	}
	after, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, sentinel, after)
	entries, err := os.ReadDir(real)
	require.NoError(t, err)
	require.Empty(t, entries)
}

// TestJournalRejectsCaptureSymlink cannot read a published record through another file name.
func TestJournalRejectsCaptureSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX symlink fixture")
	}
	reference, err := newSyntheticJournal(t).Write(syntheticJournalRecord(CapturedString("input")))
	require.NoError(t, err)
	alias := filepath.Join(filepath.Dir(reference.Path), "alias.json")
	require.NoError(t, os.Symlink(reference.Path, alias))
	_, err = Read(alias)
	require.Error(t, err)
	readSyntheticJournal(t, reference)
}

// TestJournalConcurrentWrites leaves every complete capture readable with a distinct identity.
func TestJournalConcurrentWrites(t *testing.T) {
	const count = 16
	journal := newSyntheticJournal(t)
	seed, err := journal.Write(syntheticJournalRecord(CapturedString("pre-existing capture")))
	require.NoError(t, err)
	seedBytes, err := os.ReadFile(seed.Path)
	require.NoError(t, err)
	type result struct {
		index     int
		reference Reference
		err       error
	}
	results := make(chan result, count)
	var writers sync.WaitGroup
	for i := range count {
		writers.Add(1)
		go func(index int) {
			defer writers.Done()
			record := syntheticJournalRecord(Captured([]byte{byte(index), 0xff, 0}))
			reference, writeErr := journal.Write(record)
			results <- result{index: index, reference: reference, err: writeErr}
		}(i)
	}
	writers.Wait()
	close(results)
	paths := map[string]bool{seed.Path: true}
	ids := map[string]bool{readSyntheticJournal(t, seed).CaptureID: true}
	for result := range results {
		require.NoError(t, result.err)
		require.False(t, paths[result.reference.Path], "a previous path was overwritten")
		paths[result.reference.Path] = true
		record := readSyntheticJournal(t, result.reference)
		require.False(t, ids[record.CaptureID], "capture identities must differ")
		ids[record.CaptureID] = true
		require.Equal(t, []byte{byte(result.index), 0xff, 0}, record.Converter.Input.Data)
	}
	require.Len(t, paths, count+1)
	require.Len(t, ids, count+1)
	entries, err := os.ReadDir(filepath.Dir(seed.Path))
	require.NoError(t, err)
	require.Len(t, entries, count+1, "successful publication leaves no pending files")
	after, err := os.ReadFile(seed.Path)
	require.NoError(t, err)
	require.Equal(t, seedBytes, after)
}

// TestJournalDeclaredInputSchema excludes task credentials while retaining requested URL content.
func TestJournalDeclaredInputSchema(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeOf(Record{}), reflect.TypeOf(Request{}), reflect.TypeOf(Converter{}), reflect.TypeOf(Renderer{})} {
		for _, forbidden := range []string{"APIKey", "Authorization", "Headers", "Task", "Arguments", "Config"} {
			_, exists := typ.FieldByName(forbidden)
			require.False(t, exists, "%s must not accept arbitrary credential containers", typ.Name())
		}
	}
	task := struct {
		URL            string
		OutputMarkdown bool
		APIKey         string
		Authorization  string
	}{
		URL:            "https://example.invalid/private?token=preserve-this-input",
		OutputMarkdown: true,
		APIKey:         "synthetic-task-api-key-excluded",
		Authorization:  "Bearer synthetic-auth-header-excluded",
	}
	record := syntheticJournalRecord(CapturedString("<body>unchanged</body>"))
	record.Request.OriginalURL = CapturedString(task.URL)
	record.Request.SubmittedURL = CapturedString(task.URL)
	record.Request.OutputMarkdown = task.OutputMarkdown
	reference, err := newSyntheticJournal(t).Write(record)
	require.NoError(t, err)
	got := readSyntheticJournal(t, reference)
	require.Equal(t, []byte(task.URL), got.Request.OriginalURL.Data, "declared URL content is not filtered")
	raw, err := os.ReadFile(reference.Path)
	require.NoError(t, err)
	for _, excluded := range []string{task.APIKey, task.Authorization} {
		require.NotContains(t, string(raw), excluded)
		require.NotContains(t, string(raw), base64.StdEncoding.EncodeToString([]byte(excluded)))
	}
}

// TestJournalFileSyncFailure retains only unpublished forensic data.
func TestJournalFileSyncFailure(t *testing.T) {
	journal := newSyntheticJournal(t)
	injected := errors.New("synthetic file sync failure")
	calls := 0
	journal.syncFile = func(*os.File) error {
		calls++
		return injected
	}
	reference, err := journal.Write(syntheticJournalRecord(CapturedString("complete input bytes")))
	require.ErrorIs(t, err, injected)
	require.Equal(t, 1, calls)
	require.Equal(t, "write_failed", reference.Status)
	require.Empty(t, reference.Path)
	entries, err := os.ReadDir(journal.directory)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, ".pending", filepath.Ext(entries[0].Name()))
	pending := filepath.Join(journal.directory, entries[0].Name())
	_, err = Read(pending)
	require.Error(t, err, "pending data must not be exposed as a complete capture")
}

// TestJournalDirectorySyncFailure distinguishes published data from confirmed durability.
func TestJournalDirectorySyncFailure(t *testing.T) {
	// Fail only the publication sync; every ancestor sync must still succeed.
	directory := privateJournalTempDir(t)
	leaf, err := os.Stat(directory)
	require.NoError(t, err)
	journal, err := NewJournal(directory, syntheticJournalSource())
	require.NoError(t, err)
	injected := errors.New("synthetic publication directory sync failure")
	calls := 0
	journal.syncDirectory = failSyntheticDirectorySync(leaf, injected, &calls)
	reference, err := journal.Write(syntheticJournalRecord(CapturedString("published exact bytes")))
	require.ErrorIs(t, err, injected)
	require.Equal(t, 1, calls)
	require.Equal(t, "durability_uncertain", reference.Status)
	require.NotEqual(t, "complete", reference.Status)
	require.NotEmpty(t, reference.Path)
	got, err := Read(reference.Path)
	require.NoError(t, err, "published bytes remain available despite uncertain crash durability")
	require.Equal(t, []byte("published exact bytes"), got.Converter.Input.Bytes())
	raw, err := os.ReadFile(reference.Path)
	require.NoError(t, err)
	require.EqualValues(t, len(raw), reference.Bytes)
	sum := sha256.Sum256(raw)
	require.Equal(t, hex.EncodeToString(sum[:]), reference.SHA256)
	entries, err := os.ReadDir(journal.directory)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, filepath.Base(reference.Path), entries[0].Name())
}

// TestJournalNewParentSyncFailure never reports completion before directory creation is durable.
func TestJournalNewParentSyncFailure(t *testing.T) {
	parent := privateJournalTempDir(t)
	directory := filepath.Join(parent, "new-journal")
	parentInfo, err := os.Stat(parent)
	require.NoError(t, err)
	journal, err := NewJournal(directory, syntheticJournalSource())
	require.NoError(t, err)
	injected := errors.New("synthetic new parent sync failure")
	calls := 0
	journal.syncDirectory = failSyntheticDirectorySync(parentInfo, injected, &calls)
	reference, err := journal.Write(syntheticJournalRecord(CapturedString("input")))
	require.ErrorIs(t, err, injected)
	require.Equal(t, 1, calls)
	require.Equal(t, "write_failed", reference.Status)
	require.Empty(t, reference.Path)
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	require.Empty(t, entries, "failure precedes creation of any pending or published log")
}

// failSyntheticDirectorySync identifies a directory by inode rather than path text.
func failSyntheticDirectorySync(target os.FileInfo, injected error, calls *int) func(*os.File) error {
	return func(file *os.File) error {
		actual, err := file.Stat()
		if err != nil {
			return err
		}
		if os.SameFile(target, actual) {
			*calls++
			return injected
		}
		return file.Sync()
	}
}

// TestJournalRetryRechecksParentDurability cannot skip a failed mkdir-parent sync on retry.
func TestJournalRetryRechecksParentDurability(t *testing.T) {
	parent := privateJournalTempDir(t)
	parentInfo, err := os.Stat(parent)
	require.NoError(t, err)
	directory := filepath.Join(parent, "retry-journal")
	journal, err := NewJournal(directory, syntheticJournalSource())
	require.NoError(t, err)
	injected := errors.New("synthetic persistent parent sync failure")
	calls := 0
	journal.syncDirectory = failSyntheticDirectorySync(parentInfo, injected, &calls)
	for attempt := 1; attempt <= 2; attempt++ {
		reference, writeErr := journal.Write(syntheticJournalRecord(CapturedString("retry exact input")))
		require.ErrorIs(t, writeErr, injected)
		require.Equal(t, attempt, calls)
		require.Equal(t, "write_failed", reference.Status)
		require.Empty(t, reference.Path)
		info, statErr := os.Stat(directory)
		require.NoError(t, statErr, "first attempt created the directory before its parent sync failed")
		require.True(t, info.IsDir())
		entries, readErr := os.ReadDir(directory)
		require.NoError(t, readErr)
		require.Empty(t, entries, "retry must not publish while parent durability remains unconfirmed")
	}
	journal.syncDirectory = (*os.File).Sync
	reference, err := journal.Write(syntheticJournalRecord(CapturedString("retry exact input")))
	require.NoError(t, err)
	got := readSyntheticJournal(t, reference)
	require.Equal(t, []byte("retry exact input"), got.Converter.Input.Bytes())
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}
