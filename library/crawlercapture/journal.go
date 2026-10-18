package crawlercapture

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/Laisky/errors/v2"
)

// Reference reports publication and durability, never payload contents.
type Reference struct {
	Status    string `json:"status"`
	CaptureID string `json:"capture_id,omitempty"`
	Path      string `json:"path,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	Bytes     int64  `json:"bytes"`
	Source    Source `json:"source"`
}

// Journal writes private complete log files and never evicts prior captures.
type Journal struct {
	directory     string
	source        Source
	retention     string
	syncFile      func(*os.File) error
	syncDirectory func(*os.File) error
}

var processIdentity struct {
	sync.Once
	id  string
	err error
}

// randomID creates a collision-resistant filename component, not a credential.
func randomID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", errors.Wrap(err, "create capture identity")
	}
	return hex.EncodeToString(data[:]), nil
}

// ProcessSource selects public build/process identity without inspecting configuration.
func ProcessSource(service string) (Source, error) {
	processIdentity.Do(func() { processIdentity.id, processIdentity.err = randomID() })
	if processIdentity.err != nil {
		return Source{}, processIdentity.err
	}
	host, err := os.Hostname()
	if err != nil {
		return Source{}, errors.Wrap(err, "identify capture host")
	}
	source := Source{Service: service, Hostname: host, ProcessRunID: processIdentity.id}
	if info, ok := debug.ReadBuildInfo(); ok {
		source.GoVersion = info.GoVersion
		source.ModuleVersion = info.Main.Version
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				source.Revision = setting.Value
			case "vcs.modified":
				source.Modified = setting.Value == "true"
			}
		}
	}
	return source, nil
}

// JournalFromEnvironment reads only the explicit capture directory setting.
// The default is private container-local storage with limited retention.
func JournalFromEnvironment(service string) (*Journal, error) {
	source, err := ProcessSource(service)
	if err != nil {
		return nil, err
	}
	directory := os.Getenv("CRAWLER_FAILURE_LOG_DIR")
	retention := "operator_configured_directory"
	if directory == "" {
		directory = filepath.Join(os.TempDir(), service+"-crawler-failure-logs")
		retention = "container_local_retention_limited"
	}
	journal, err := NewJournal(directory, source)
	if err != nil {
		return nil, err
	}
	journal.retention = retention
	return journal, nil
}

// NewJournal configures an explicit directory without creating files.
func NewJournal(directory string, source Source) (*Journal, error) {
	if directory == "" || source.Service == "" || strings.ContainsAny(source.Service, "/\\") || source.Hostname == "" || source.ProcessRunID == "" {
		return nil, errors.New("invalid failure journal identity")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, errors.Wrap(err, "resolve failure journal")
	}
	return &Journal{directory: absolute, source: source, retention: "operator_configured_directory", syncFile: (*os.File).Sync, syncDirectory: (*os.File).Sync}, nil
}

// Source returns the journal's immutable process identity.
func (j *Journal) Source() Source { return j.source }

// countingWriter measures actual bytes accepted by the file.
type countingWriter struct {
	writer io.Writer
	bytes  int64
}

// Write counts successful bytes and preserves the writer result.
func (w *countingWriter) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	w.bytes += int64(n)
	return n, err
}

// Write seals and synchronously publishes without overwriting any capture.
// A complete reference requires both file and directory fsync.
func (j *Journal) Write(record Record) (reference Reference, resultErr error) {
	reference = Reference{Status: "write_failed", Source: j.source}
	if record.Renderer != nil {
		snapshot := *record.Renderer
		record.Renderer = &snapshot
	}
	record.Version = 1
	record.Complete = true
	record.Source = j.source
	record.Retention = j.retention
	for _, b := range record.blobs() {
		if b.State == "" && b.Data == nil && b.SHA256 == "" && b.ByteLength == 0 {
			*b = NotReached()
		}
	}
	id, err := randomID()
	if err != nil {
		return reference, err
	}
	record.CaptureID = id
	reference.CaptureID = id
	record.CreatedAt = time.Now().UTC()
	record.ManifestSHA256, err = record.manifestDigest()
	if err != nil {
		return reference, err
	}
	if err := record.Validate(); err != nil {
		return reference, err
	}
	root, err := openPrivateDirectory(j.directory, true, j.syncDirectory)
	if err != nil {
		return reference, err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, closeErr)
			if reference.Status == "complete" {
				reference.Status = "durability_uncertain"
			}
		}
	}()
	temp := "." + id + ".pending"
	final := id + ".json"
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return reference, errors.Wrap(err, "create failure log")
	}
	hash := sha256.New()
	counter := &countingWriter{writer: io.MultiWriter(file, hash)}
	modeErr := file.Chmod(0600)
	encodeErr := modeErr
	if modeErr != nil {
		encodeErr = modeErr
	} else {
		encodeErr = streamRecord(counter, record)
	}
	syncErr := j.syncFile(file)
	closeErr := file.Close()
	// Incomplete pending files remain available for operator forensics, never as complete captures.
	if err := errors.Join(encodeErr, syncErr, closeErr); err != nil {
		return reference, errors.Wrap(err, "write failure log")
	}
	// Atomic hardlink publication requires same-filesystem support and fails on existing final.
	if err := root.Link(temp, final); err != nil {
		return reference, errors.Wrap(err, "publish failure log")
	}
	reference.Path = filepath.Join(j.directory, final)
	reference.Bytes = counter.bytes
	reference.SHA256 = hex.EncodeToString(hash.Sum(nil))
	reference.Status = "durability_uncertain"
	removeErr := root.Remove(temp)
	directory, openErr := root.Open(".")
	if openErr != nil {
		return reference, errors.Wrap(errors.Join(removeErr, openErr), "sync failure log directory")
	}
	syncErr = j.syncDirectory(directory)
	closeErr = directory.Close()
	if err := errors.Join(removeErr, syncErr, closeErr); err != nil {
		return reference, errors.Wrap(err, "sync failure log directory")
	}
	reference.Status = "complete"
	return reference, nil
}

// Read opens a private non-symlink capture and validates complete data.
func Read(path string) (result Record, resultErr error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Record{}, errors.Wrap(err, "resolve capture path")
	}
	if filepath.Ext(absolute) != ".json" {
		return Record{}, errors.New("capture is not a published journal file")
	}
	root, err := openPrivateDirectory(filepath.Dir(absolute), false, (*os.File).Sync)
	if err != nil {
		return Record{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()
	name := filepath.Base(absolute)
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 {
		return Record{}, errors.New("capture file is not private regular data")
	}
	if err := checkOwner(before, true); err != nil {
		return Record{}, err
	}
	file, err := root.Open(name)
	if err != nil {
		return Record{}, errors.Wrap(err, "open capture file")
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return Record{}, errors.New("capture file changed during open")
	}
	current, err := root.Lstat(name)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, current) {
		return Record{}, errors.New("capture file path changed")
	}
	return Decode(file)
}

// openPrivateDirectory holds every path component beneath os.Root handles.
func openPrivateDirectory(path string, create bool, syncDirectory func(*os.File) error) (*os.Root, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.Wrap(err, "resolve journal directory")
	}
	root, err := os.OpenRoot(string(filepath.Separator))
	if err != nil {
		return nil, errors.Wrap(err, "open journal filesystem")
	}
	components := strings.Split(strings.TrimPrefix(absolute, string(filepath.Separator)), string(filepath.Separator))
	if len(components) == 0 || components[0] == "" {
		return nil, closeJournalRoot(root, errors.New("journal cannot use filesystem root"))
	}
	for index, component := range components {
		before, statErr := root.Lstat(component)
		if errors.Is(statErr, os.ErrNotExist) && create {
			mkdirErr := root.Mkdir(component, 0700)
			if mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
				return nil, closeJournalRoot(root, errors.Wrap(mkdirErr, "create private journal directory"))
			}
			before, statErr = root.Lstat(component)
		}
		if statErr != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			return nil, closeJournalRoot(root, errors.New("journal directory unavailable or symlink"))
		}
		if create {
			parent, openErr := root.Open(".")
			if openErr != nil {
				return nil, closeJournalRoot(root, errors.Wrap(openErr, "open journal parent"))
			}
			syncErr := syncDirectory(parent)
			closeErr := parent.Close()
			if err := errors.Join(syncErr, closeErr); err != nil {
				return nil, closeJournalRoot(root, errors.Wrap(err, "sync journal parent"))
			}
		}
		leaf := index == len(components)-1
		if err := checkOwner(before, leaf); err != nil {
			return nil, closeJournalRoot(root, err)
		}
		if leaf && before.Mode().Perm() != 0700 {
			return nil, closeJournalRoot(root, errors.New("journal directory must have mode 0700"))
		}
		if !leaf && before.Mode().Perm()&0022 != 0 && before.Mode()&os.ModeSticky == 0 {
			return nil, closeJournalRoot(root, errors.Errorf("journal ancestor %d permits untrusted writes (mode %o)", index, before.Mode()))
		}
		child, openErr := root.OpenRoot(component)
		if openErr != nil {
			return nil, closeJournalRoot(root, errors.Wrap(openErr, "open private journal directory"))
		}
		after, afterErr := child.Stat(".")
		current, currentErr := root.Lstat(component)
		if afterErr != nil || currentErr != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, after) || !os.SameFile(before, current) {
			return nil, closeJournalRoot(root, closeJournalRoot(child, errors.New("journal directory changed during open")))
		}
		if closeErr := root.Close(); closeErr != nil {
			return nil, closeJournalRoot(child, errors.Wrap(closeErr, "close journal ancestor"))
		}
		root = child
	}
	return root, nil
}

// streamRecord writes fixed typed metadata and streams each base64 value.
// The small metadata JSON contains null payload slots in the exact Blob field order.
func streamRecord(writer io.Writer, record Record) error {
	metadata := record
	if metadata.Renderer != nil {
		cloned := *metadata.Renderer
		metadata.Renderer = &cloned
	}
	for _, blob := range metadata.blobs() {
		blob.Data = nil
		blob.owned = nil
		blob.text = nil
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return errors.Wrap(err, "encode capture metadata")
	}
	marker := []byte("\"base64\":null")
	values := record.blobs()
	if bytes.Count(encoded, marker) != len(values) {
		return errors.New("capture payload slot mismatch")
	}
	for _, blob := range values {
		at := bytes.Index(encoded, marker)
		if _, err := writer.Write(encoded[:at+len(marker)-4]); err != nil {
			return errors.Wrap(err, "write capture metadata")
		}
		if blob.State == "captured" {
			if _, err := io.WriteString(writer, "\""); err != nil {
				return err
			}
			encoder := base64.NewEncoder(base64.StdEncoding, writer)
			_, copyErr := io.Copy(encoder, readerOnly{blob.reader()})
			closeErr := encoder.Close()
			if err := errors.Join(copyErr, closeErr); err != nil {
				return errors.Wrap(err, "write capture payload")
			}
			if _, err := io.WriteString(writer, "\""); err != nil {
				return err
			}
		} else if _, err := io.WriteString(writer, "null"); err != nil {
			return err
		}
		encoded = encoded[at+len(marker):]
	}
	if _, err := writer.Write(encoded); err != nil {
		return errors.Wrap(err, "finish capture metadata")
	}
	_, err = io.WriteString(writer, "\n")
	return err
}

// closeJournalRoot retains cleanup failures alongside the original operation error.
func closeJournalRoot(root *os.Root, operationErr error) error {
	return errors.Join(operationErr, root.Close())
}
