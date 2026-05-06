package eval

import (
	"bytes"
	"encoding/json"
	"math"
	"sort"
	"text/template"

	errors "github.com/Laisky/errors/v2"
)

func renderPrompt(tmpl string, sample RAGASSample) (string, error) {
	t, err := template.New("ragas").Parse(tmpl)
	if err != nil {
		return "", errors.Wrap(err, "parse prompt template")
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, sample); err != nil {
		return "", errors.Wrap(err, "render prompt")
	}
	return buf.String(), nil
}

func extractFloat(out map[string]any, key string) (float64, error) {
	if out == nil {
		return 0, errors.Errorf("missing field %q in judge output", key)
	}
	raw, ok := out[key]
	if !ok {
		return 0, errors.Errorf("missing field %q in judge output", key)
	}
	switch v := raw.(type) {
	case float64:
		return v, nil
	case float32:
		return float64(v), nil
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0, errors.Wrapf(err, "decode %q as float", key)
		}
		return f, nil
	default:
		return 0, errors.Errorf("unexpected type %T for %q", raw, key)
	}
}

func extractStringSlice(out map[string]any, key string) ([]string, error) {
	raw, ok := out[key]
	if !ok {
		return nil, errors.Errorf("missing field %q", key)
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, errors.Errorf("field %q is not an array", key)
	}
	out2 := make([]string, 0, len(arr))
	for i, item := range arr {
		s, ok := item.(string)
		if !ok {
			return nil, errors.Errorf("element %d of %q is not a string", i, key)
		}
		out2 = append(out2, s)
	}
	return out2, nil
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func stats(values []float64, status string) RAGASMetricStats {
	if len(values) == 0 {
		return RAGASMetricStats{Status: "skipped"}
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	cp := append([]float64(nil), values...)
	sort.Float64s(cp)
	idx := int(math.Ceil(0.95*float64(len(cp)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	return RAGASMetricStats{
		N:      len(values),
		Mean:   sum / float64(len(values)),
		P95:    cp[idx],
		Status: status,
	}
}

func orDefault(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}
