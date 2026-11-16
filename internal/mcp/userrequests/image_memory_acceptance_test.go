package userrequests

import (
	"bufio"
	"bytes"
	"fmt"
	"image"
	"image/png"
	"math/rand/v2"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// imageMemoryAcceptanceEnv enables TestImageCreateMemoryEnvelope, a
// deliberate measurement (several GiB of RAM, about a minute):
//
//	MCP_IMAGE_MEMORY_ACCEPTANCE=1 go test -count=1 ./internal/mcp/userrequests -run TestImageCreateMemoryEnvelope -v
const imageMemoryAcceptanceEnv = "MCP_IMAGE_MEMORY_ACCEPTANCE"

// processRSS returns the resident set size of this process from
// /proc/self/status (Linux).
func processRSS(t *testing.T) int64 {
	t.Helper()
	raw, err := os.ReadFile("/proc/self/status")
	require.NoError(t, err)
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		if rest, ok := strings.CutPrefix(scanner.Text(), "VmRSS:"); ok {
			kb, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 10, 64)
			require.NoError(t, err)
			return kb << 10
		}
	}
	t.Fatal("VmRSS not found")
	return 0
}

// worstCaseAttachments returns a create's attachments that maximize what it
// holds: four 16-bit noise images at the normalized edge (each kept as an
// ~18 MiB PNG until its upload ends) and one 8192 x 8192 image (the largest
// decode working set the default budget admits).
func worstCaseAttachments(t *testing.T, seed uint64) [][]byte {
	t.Helper()
	out := make([][]byte, 0, 5)
	for i := range 4 {
		img := image.NewRGBA64(image.Rect(0, 0, 1536, 1536))
		r := rand.New(rand.NewPCG(seed, uint64(i))) //nolint:gosec // deterministic test noise
		for p := 0; p < len(img.Pix); p += 8 {
			v := r.Uint64()
			for b := range 8 {
				img.Pix[p+b] = byte(v >> (8 * b))
			}
		}
		var buf bytes.Buffer
		require.NoError(t, png.Encode(&buf, img))
		out = append(out, buf.Bytes())
	}
	big := image.NewRGBA(image.Rect(0, 0, 8192, 8192))
	for p := range big.Pix {
		big.Pix[p] = byte(seed) + byte(p&3)
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, big))
	return append(out, buf.Bytes())
}

// multipartImageBody encodes files as the images parts of a create request.
func multipartImageBody(t *testing.T, files [][]byte) ([]byte, string) {
	t.Helper()
	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	require.NoError(t, mw.WriteField("content", "memory acceptance"))
	for i, file := range files {
		hdr := make(textproto.MIMEHeader)
		hdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name="images"; filename="img%d.png"`, i))
		hdr.Set("Content-Type", "image/png")
		part, err := mw.CreatePart(hdr)
		require.NoError(t, err)
		_, err = part.Write(file)
		require.NoError(t, err)
	}
	require.NoError(t, mw.Close())
	return buf.Bytes(), mw.FormDataContentType()
}

// TestImageCreateMemoryEnvelope measures the peak RSS of the image create
// path under the shipped limits (maintainer review of PR #94, P2: "measure
// peak RSS, not only decoder allocation totals, under concurrent five-image
// requests"). Eight creates of four identities each attach five worst-case
// images. With GOMEMLIMIT set to the baseline plus the envelope the peak
// above the idle baseline stays close to the envelope; without a soft limit
// the collector lets the heap reach about twice the live set, which is why
// the server derives a soft limit from the container limit and sizes the
// envelope to half the process limit.
func TestImageCreateMemoryEnvelope(t *testing.T) {
	if os.Getenv(imageMemoryAcceptanceEnv) == "" {
		t.Skipf("%s not set", imageMemoryAcceptanceEnv)
	}
	prevTimeout := createRequestTimeout
	createRequestTimeout = 5 * time.Minute
	t.Cleanup(func() { createRequestTimeout = prevTimeout })

	settings := defaultImageSettings()
	settings.PerUserQuotaBytes = 1 << 30
	settings.MaxConcurrentCreates = DefaultImageMaxConcurrentCreates
	settings.MaxConcurrentCreatesPerUser = DefaultImageMaxConcurrentCreatesPerUser
	settings.DecodeMemoryBudgetBytes = DefaultImageDecodeMemoryBudgetBytes
	envelope := settings.MemoryEnvelope()

	// Request bodies are built before the baseline, so the measurement sees
	// what the server allocates, not the test client.
	bodies := make([][]byte, DefaultImageMaxConcurrentCreates)
	types := make([]string, len(bodies))
	for i := range bodies {
		bodies[i], types[i] = multipartImageBody(t, worstCaseAttachments(t, uint64(i+1)))
	}

	for _, softLimit := range []bool{false, true} {
		t.Run(fmt.Sprintf("gomemlimit=%v", softLimit), func(t *testing.T) {
			h := newOrphanHarness(t, settings, "")
			h.store.DiscardBodies = true // the uploaded objects live in the object store, not here
			runtime.GC()
			debug.FreeOSMemory()
			baseline := processRSS(t)
			if softLimit {
				prev := debug.SetMemoryLimit(baseline + envelope.Total())
				t.Cleanup(func() { debug.SetMemoryLimit(prev) })
			}

			var peak atomic.Int64
			stop := make(chan struct{})
			sampled := make(chan struct{})
			go func() {
				defer close(sampled)
				ticker := time.NewTicker(2 * time.Millisecond)
				defer ticker.Stop()
				for {
					if rss := processRSS(t); rss > peak.Load() {
						peak.Store(rss)
					}
					select {
					case <-stop:
						return
					case <-ticker.C:
					}
				}
			}()

			start := time.Now()
			var wg sync.WaitGroup
			codes := make([]int, len(bodies))
			for i := range bodies {
				wg.Go(func() {
					req := httptest.NewRequest(http.MethodPost, "/api/requests", bytes.NewReader(bodies[i]))
					req.Header.Set("Content-Type", types[i])
					req.Header.Set("Authorization", fmt.Sprintf("Bearer sk-memory-acceptance-%d-123456", i/2))
					rec := httptest.NewRecorder()
					h.handler.ServeHTTP(rec, req)
					codes[i] = rec.Code
					if rec.Code != http.StatusOK {
						t.Logf("create %d: %s", i, rec.Body.String())
					}
				})
			}
			wg.Wait()
			close(stop)
			<-sampled

			delta := peak.Load() - baseline
			t.Logf("gomemlimit=%v: peak RSS %d MiB above a %d MiB baseline; envelope %d MiB (decode %d, inputs %d, outputs %d); ratio %.2f; %s",
				softLimit, delta>>20, baseline>>20, envelope.Total()>>20, envelope.DecodeBudget>>20, envelope.InputBuffers>>20,
				envelope.PreparedOutputs>>20, float64(delta)/float64(envelope.Total()), time.Since(start).Round(time.Millisecond))
			for i, code := range codes {
				require.Equal(t, http.StatusOK, code, "create %d", i)
			}
			if softLimit {
				require.Less(t, delta, envelope.Total()*5/4, "with GOMEMLIMIT the peak stays near the envelope")
			} else {
				// Measured 2.01x and 2.11x (2026-11-15): the collector lets the heap
				// reach about twice the live set.
				require.Less(t, delta, envelope.Total()*9/4, "without GOMEMLIMIT the peak stays near twice the envelope")
			}
		})
	}
}
