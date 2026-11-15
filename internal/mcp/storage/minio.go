package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	errors "github.com/Laisky/errors/v2"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/lifecycle"
	"github.com/minio/minio-go/v7/pkg/signer"
)

// MinIOConfig holds the connection parameters for the MinIO object store.
type MinIOConfig struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
	UseSSL    bool
}

// MinIOClient implements ObjectStore against a real MinIO / S3 endpoint.
// The client wraps a lazily-initialized underlying *minio.Client using an
// RWMutex (not sync.Once) so that a transient init failure can be retried.
type MinIOClient struct {
	cfg MinIOConfig

	mu     sync.RWMutex
	client *minio.Client
}

// NewMinIOClient constructs a MinIOClient from the provided configuration.
// The underlying *minio.Client is created lazily on first use.
func NewMinIOClient(cfg MinIOConfig) (*MinIOClient, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, errors.New("storage: minio endpoint is required")
	}
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("storage: minio bucket is required")
	}
	if strings.TrimSpace(cfg.AccessKey) == "" || strings.TrimSpace(cfg.SecretKey) == "" {
		return nil, errors.New("storage: minio credentials are required")
	}
	return &MinIOClient{cfg: cfg}, nil
}

// ensureClient returns a cached *minio.Client, constructing one on first call
// and retrying after transient failures.
func (m *MinIOClient) ensureClient() (*minio.Client, error) {
	m.mu.RLock()
	if m.client != nil {
		c := m.client
		m.mu.RUnlock()
		return c, nil
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client != nil {
		return m.client, nil
	}

	client, err := minio.New(m.cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(m.cfg.AccessKey, m.cfg.SecretKey, ""),
		Secure: m.cfg.UseSSL,
	})
	if err != nil {
		return nil, errors.Wrap(err, "storage: construct minio client")
	}
	m.client = client
	return client, nil
}

// VerifyBucket probes the configured bucket to confirm the endpoint is reachable
// and the credentials grant access. Intended for startup validation so that a
// misconfigured MinIO fails fast instead of surfacing on the first upload.
func (m *MinIOClient) VerifyBucket(ctx context.Context) error {
	client, err := m.ensureClient()
	if err != nil {
		return errors.Wrap(err, "storage: verify bucket ensure client")
	}
	exists, err := client.BucketExists(ctx, m.cfg.Bucket)
	if err != nil {
		return errors.Wrapf(err, "storage: probe bucket %q", m.cfg.Bucket)
	}
	if !exists {
		return errors.Errorf("storage: bucket %q not found", m.cfg.Bucket)
	}
	return nil
}

// Put uploads an object; identical keys are overwritten deterministically.
func (m *MinIOClient) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string, userMeta map[string]string) error {
	client, err := m.ensureClient()
	if err != nil {
		return errors.Wrap(err, "storage: put ensure client")
	}
	opts := minio.PutObjectOptions{
		ContentType:  contentType,
		UserMetadata: userMeta,
	}
	if _, err := client.PutObject(ctx, m.cfg.Bucket, key, body, size, opts); err != nil {
		return errors.Wrapf(err, "storage: put object %q", key)
	}
	return nil
}

// Get fetches an object and returns a ReadCloser that the caller must close.
// The object's size is also returned for efficient buffering.
func (m *MinIOClient) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	client, err := m.ensureClient()
	if err != nil {
		return nil, 0, errors.Wrap(err, "storage: get ensure client")
	}
	obj, err := client.GetObject(ctx, m.cfg.Bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, errors.Wrapf(err, "storage: get object %q", key)
	}
	stat, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		return nil, 0, errors.Wrapf(err, "storage: stat object %q", key)
	}
	return obj, stat.Size, nil
}

// Delete removes an object. S3 and MinIO answer 204 for missing keys, so the
// call is idempotent.
func (m *MinIOClient) Delete(ctx context.Context, key string) error {
	client, err := m.ensureClient()
	if err != nil {
		return errors.Wrap(err, "storage: delete ensure client")
	}
	if err := client.RemoveObject(ctx, m.cfg.Bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return errors.Wrapf(err, "storage: delete object %q", key)
	}
	return nil
}

// PresignedGet returns a short-lived URL pointing at the object.
func (m *MinIOClient) PresignedGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	client, err := m.ensureClient()
	if err != nil {
		return "", errors.Wrap(err, "storage: presign ensure client")
	}
	u, err := client.PresignedGetObject(ctx, m.cfg.Bucket, key, ttl, nil)
	if err != nil {
		return "", errors.Wrapf(err, "storage: presign object %q", key)
	}
	return u.String(), nil
}

// EnsureLifecycle installs an expiry rule restricted to the provided prefix.
// The rule ID is derived from the full prefix (lifecycleRuleID), and a rule
// counts as this prefix's own only when its filter prefix is exactly prefix,
// so repeated calls replace, not duplicate, the previous rule and never touch
// another prefix's rule whose ID looks alike. S3 PutBucketLifecycleConfiguration
// replaces the bucket's whole configuration, so the current configuration is
// read and only this prefix's rules are swapped; rules other applications
// installed on a shared bucket are preserved. If the current configuration
// cannot be read the call fails instead of risking those rules.
func (m *MinIOClient) EnsureLifecycle(ctx context.Context, prefix string, days int) error {
	client, err := m.ensureClient()
	if err != nil {
		return errors.Wrap(err, "storage: lifecycle ensure client")
	}
	if days <= 0 {
		return errors.Errorf("storage: lifecycle days must be positive, got %d", days)
	}
	raw, err := m.rawBucketLifecycle(ctx, client)
	if err != nil {
		return errors.WithStack(err)
	}
	existing := lifecycle.NewConfiguration()
	if raw != nil {
		if err := xml.Unmarshal(raw, existing); err != nil {
			return errors.Wrap(err, "storage: parse bucket lifecycle")
		}
		if err := checkLifecycleRoundTrip(raw, existing, prefix); err != nil {
			return errors.Wrapf(err, "storage: refusing to rewrite the bucket lifecycle; add rule %q (prefix %q, expire after %d days) manually",
				lifecycleRuleID(prefix, days), prefix, days)
		}
	}
	cfg := mergeLifecycleRule(existing, prefix, days)
	if err := client.SetBucketLifecycle(ctx, m.cfg.Bucket, cfg); err != nil {
		return errors.Wrap(err, "storage: set bucket lifecycle")
	}
	return nil
}

// ErrLifecycleMergeUnsafe reports that an existing lifecycle rule of another
// application would not survive a rewrite unchanged (minio-go's model does
// not represent every element, e.g. a second Transition), so EnsureLifecycle
// leaves the configuration alone.
var ErrLifecycleMergeUnsafe = errors.New("existing bucket lifecycle rule cannot be preserved exactly")

// emptyPayloadSHA256 is the SHA-256 of an empty request body.
const emptyPayloadSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// maxLifecycleBytes caps the lifecycle document read from the store.
const maxLifecycleBytes = 1 << 20

// rawBucketLifecycle returns the bucket's lifecycle configuration exactly as
// the store serves it, or nil when the bucket has none. minio-go only exposes
// the parsed form, which drops elements it does not model, so this is a
// signed GET of its own.
func (m *MinIOClient) rawBucketLifecycle(ctx context.Context, client *minio.Client) ([]byte, error) {
	location, err := client.GetBucketLocation(ctx, m.cfg.Bucket)
	if err != nil {
		return nil, errors.Wrap(err, "storage: get bucket location")
	}
	scheme := "http"
	if m.cfg.UseSSL {
		scheme = "https"
	}
	target := url.URL{Scheme: scheme, Host: m.cfg.Endpoint, Path: "/" + m.cfg.Bucket, RawQuery: "lifecycle="}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, errors.Wrap(err, "storage: build lifecycle request")
	}
	req.Header.Set("X-Amz-Content-Sha256", emptyPayloadSHA256)
	signed := signer.SignV4(*req, m.cfg.AccessKey, m.cfg.SecretKey, "", location)
	resp, err := lifecycleHTTPClient.Do(signed)
	if err != nil {
		return nil, errors.Wrap(err, "storage: get bucket lifecycle")
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxLifecycleBytes))
	if err != nil {
		return nil, errors.Wrap(err, "storage: read bucket lifecycle")
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		return body, nil
	case resp.StatusCode == http.StatusNotFound && bytes.Contains(body, []byte("NoSuchLifecycleConfiguration")):
		return nil, nil
	default:
		return nil, errors.Errorf("storage: get bucket lifecycle: HTTP %d", resp.StatusCode)
	}
}

// lifecycleHTTPClient performs the raw lifecycle GET.
var lifecycleHTTPClient = &http.Client{Timeout: 30 * time.Second}

// checkLifecycleRoundTrip returns ErrLifecycleMergeUnsafe when any rule of raw
// that this package does not own for prefix (see ownsLifecycleRule) differs,
// compared as an order-insensitive XML tree, from what minio-go would write
// back for it.
func checkLifecycleRoundTrip(raw []byte, parsed *lifecycle.Configuration, prefix string) error {
	root, err := parseXMLTree(raw)
	if err != nil {
		return errors.Wrap(err, "parse lifecycle XML")
	}
	written := make(map[string]*lifecycle.Rule, len(parsed.Rules))
	for i := range parsed.Rules {
		written[parsed.Rules[i].ID] = &parsed.Rules[i]
	}
	for _, rawRule := range root.children {
		if rawRule.name != "Rule" {
			continue
		}
		id := rawRule.childText("ID")
		rule, ok := written[id]
		if !ok {
			return errors.Wrapf(ErrLifecycleMergeUnsafe, "rule %q", id)
		}
		if ownsLifecycleRule(*rule, prefix) {
			continue
		}
		out, err := xml.Marshal(rule)
		if err != nil {
			return errors.Wrapf(err, "marshal rule %q", id)
		}
		back, err := parseXMLTree(out)
		if err != nil {
			return errors.Wrapf(err, "parse rule %q", id)
		}
		if rawRule.canonical() != back.canonical() {
			return errors.Wrapf(ErrLifecycleMergeUnsafe, "rule %q", id)
		}
	}
	return nil
}

// xmlNode is a minimal element tree for structural comparison.
type xmlNode struct {
	name     string
	text     string
	children []*xmlNode
}

// parseXMLTree parses data into an element tree, ignoring namespaces,
// attributes of the root and insignificant whitespace.
func parseXMLTree(data []byte) (*xmlNode, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var stack []*xmlNode
	var root *xmlNode
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.WithStack(err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			node := &xmlNode{name: t.Name.Local}
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, node)
			} else if root == nil {
				root = node
			}
			stack = append(stack, node)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text += strings.TrimSpace(string(t))
			}
		}
	}
	if root == nil {
		return nil, errors.New("empty XML document")
	}
	return root, nil
}

// childText returns the text of the first child named name.
func (n *xmlNode) childText(name string) string {
	for _, c := range n.children {
		if c.name == name {
			return c.text
		}
	}
	return ""
}

// canonical renders n with its children sorted and empty elements dropped,
// so element order and empty placeholders do not count as differences while
// any lost or changed element does.
func (n *xmlNode) canonical() string {
	parts := make([]string, 0, len(n.children))
	for _, c := range n.children {
		if s := c.canonical(); s != "" {
			parts = append(parts, s)
		}
	}
	if n.text == "" && len(parts) == 0 {
		return ""
	}
	sort.Strings(parts)
	return "<" + n.name + ">" + n.text + strings.Join(parts, "") + "</" + n.name + ">"
}

const (
	// lifecycleRuleIDNamespace starts the ID of every rule this package installs.
	lifecycleRuleIDNamespace = "mcp-images-"
	// lifecycleRuleIDMaxPrefix bounds the readable part of a rule ID; S3 and
	// MinIO reject IDs longer than 255 bytes.
	lifecycleRuleIDMaxPrefix = 160
)

// legacyLifecycleRuleIDPrefix is the ID prefix earlier releases used for
// prefix. Different prefixes can share it (sanitizeForID maps every unsafe
// character to '-'), so it identifies a rule only together with its filter.
func legacyLifecycleRuleIDPrefix(prefix string) string {
	return lifecycleRuleIDNamespace + sanitizeForID(prefix) + "-"
}

// lifecycleRuleIDStem returns the part of the rule ID for prefix that does not
// depend on the TTL: a readable, bounded form of the prefix plus a digest of
// the full prefix, so distinct prefixes never share an ID (rule IDs must be
// unique within a bucket configuration).
func lifecycleRuleIDStem(prefix string) string {
	readable := sanitizeForID(prefix)
	if len(readable) > lifecycleRuleIDMaxPrefix {
		readable = readable[:lifecycleRuleIDMaxPrefix]
	}
	digest := sha256.Sum256([]byte(prefix))
	return fmt.Sprintf("%s%s-%x-", lifecycleRuleIDNamespace, readable, digest[:6])
}

// lifecycleRuleID returns the ID of the expiry rule for prefix and days. It
// stays within S3's 255-byte limit for any prefix.
func lifecycleRuleID(prefix string, days int) string {
	return fmt.Sprintf("%s%dd", lifecycleRuleIDStem(prefix), days)
}

// ownsLifecycleRule reports whether rule is an expiry rule this package
// installed for prefix. A rule whose ID carries this release's digested stem
// for prefix is ours whatever its filter (the digest names the full prefix,
// and keeping it would duplicate the ID we write, which S3 and MinIO reject).
// A rule with an earlier release's undigested ID is ours only when its
// filter is exactly prefix and nothing else, because prefixes that sanitize
// alike share that ID form.
func ownsLifecycleRule(rule lifecycle.Rule, prefix string) bool {
	if strings.HasPrefix(rule.ID, lifecycleRuleIDStem(prefix)) {
		return true
	}
	filter := rule.RuleFilter
	if filter.Prefix != prefix || !filter.And.IsEmpty() || !filter.Tag.IsEmpty() ||
		filter.ObjectSizeLessThan != 0 || filter.ObjectSizeGreaterThan != 0 || rule.Prefix != "" {
		return false
	}
	return strings.HasPrefix(rule.ID, legacyLifecycleRuleIDPrefix(prefix))
}

// mergeLifecycleRule returns a copy of existing in which every rule this
// package installed for prefix (any TTL, any ID form) is replaced by one rule
// expiring objects under prefix after days. Other rules are kept unchanged
// and in order.
func mergeLifecycleRule(existing *lifecycle.Configuration, prefix string, days int) *lifecycle.Configuration {
	cfg := lifecycle.NewConfiguration()
	if existing != nil {
		for _, rule := range existing.Rules {
			if ownsLifecycleRule(rule, prefix) {
				continue
			}
			cfg.Rules = append(cfg.Rules, rule)
		}
	}
	cfg.Rules = append(cfg.Rules, lifecycle.Rule{
		ID:     lifecycleRuleID(prefix, days),
		Status: "Enabled",
		RuleFilter: lifecycle.Filter{
			Prefix: prefix,
		},
		Expiration: lifecycle.Expiration{
			Days: lifecycle.ExpirationDays(days),
		},
	})
	return cfg
}

// sanitizeForID strips characters that are not safe inside a lifecycle rule ID.
func sanitizeForID(in string) string {
	b := strings.Builder{}
	for _, r := range in {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}
