package pageindex

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestPDFParserCanceledContext protects cancellation across the pdfcpu API upgrade.
func TestPDFParserCanceledContext(t *testing.T) {
	data := loadSamplePDF(t)
	parser, err := NewPDFParser("pdfcpu", "pdfcpu")
	require.NoError(t, err)
	for _, expired := range []bool{false, true} {
		name := "canceled"
		if expired {
			name = "deadline exceeded"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			if expired {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Unix(0, 0))
			}
			defer cancel()
			if !expired {
				cancel()
			}
			count, countErr := parser.PageCount(ctx, data)
			require.ErrorIs(t, countErr, ctx.Err())
			require.Zero(t, count)
			outline, outlineErr := parser.Outline(ctx, data)
			require.ErrorIs(t, outlineErr, ctx.Err())
			require.Empty(t, outline)
		})
	}
}

// TestPDFParserOutlineCompatibility keeps ordinary PDFs usable after adding contexts.
func TestPDFParserOutlineCompatibility(t *testing.T) {
	parser, err := NewPDFParser("pdfcpu", "pdfcpu")
	require.NoError(t, err)
	_, err = parser.Outline(context.Background(), loadSamplePDF(t))
	require.NoError(t, err)
}
