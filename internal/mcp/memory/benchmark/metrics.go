package benchmark

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

func scoreCase(query Query, hits []SearchHit, answer *AnswerResult, topK int) CaseMetrics {
	metrics := retrievalMetrics(query, hits, topK)
	if query.Unanswerable {
		ok := len(hits) == 0
		if answer != nil {
			ok = isInsufficientEvidence(answer.Text)
		}
		metrics.AbstentionOK = boolPointer(ok)
	}
	if answer != nil && !query.Unanswerable && strings.TrimSpace(query.Answer) != "" {
		exact := normalizeAnswer(answer.Text) == normalizeAnswer(query.Answer)
		precision, recall, f1 := tokenPRF(answer.Text, query.Answer)
		metrics.ExactMatch = boolPointer(exact)
		metrics.TokenPrecision = floatPointer(precision)
		metrics.TokenRecall = floatPointer(recall)
		metrics.TokenF1 = floatPointer(f1)
	}
	if answer != nil && len(query.Rubric) > 0 {
		coverage := rubricCoverage(answer.Text, query.Rubric)
		metrics.RubricCoverage = floatPointer(coverage)
	}
	return metrics
}

func retrievalMetrics(query Query, hits []SearchHit, topK int) CaseMetrics {
	if topK <= 0 {
		topK = len(hits)
	}
	ranked := deduplicateHitPaths(hits, topK)
	gold := make(map[string]struct{}, len(query.GoldPaths))
	for _, path := range query.GoldPaths {
		gold[path] = struct{}{}
	}

	hitsCount := 0
	firstRelevant := -1
	dcg := 0.0
	for index, path := range ranked {
		if _, relevant := gold[path]; !relevant {
			continue
		}
		hitsCount++
		if firstRelevant < 0 {
			firstRelevant = index
		}
		dcg += 1 / math.Log2(float64(index+2))
	}

	recall := 0.0
	if len(gold) > 0 {
		recall = float64(hitsCount) / float64(len(gold))
	}
	precision := 0.0
	if topK > 0 {
		precision = float64(hitsCount) / float64(topK)
	}
	mrr := 0.0
	if firstRelevant >= 0 {
		mrr = 1 / float64(firstRelevant+1)
	}
	idealHits := min(len(gold), topK)
	idcg := 0.0
	for index := 0; index < idealHits; index++ {
		idcg += 1 / math.Log2(float64(index+2))
	}
	ndcg := 0.0
	if idcg > 0 {
		ndcg = dcg / idcg
	}

	return CaseMetrics{
		RecallAtK: recall, PrecisionAtK: precision, NDCGAtK: ndcg, MRR: mrr,
		HitAtK: hitsCount > 0, EvidenceRecall: evidenceRecall(query.GoldEvidence, hits),
	}
}

func deduplicateHitPaths(hits []SearchHit, topK int) []string {
	seen := make(map[string]struct{}, len(hits))
	paths := make([]string, 0, min(len(hits), topK))
	for _, hit := range hits {
		path := strings.TrimSpace(hit.FilePath)
		if path == "" {
			continue
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
		if topK > 0 && len(paths) >= topK {
			break
		}
	}
	return paths
}

func evidenceRecall(gold []string, hits []SearchHit) float64 {
	if len(gold) == 0 {
		return 0
	}
	var context strings.Builder
	for _, hit := range hits {
		context.WriteString("\n")
		context.WriteString(hit.Content)
	}
	normalizedContext := normalizeLoose(context.String())
	matched := 0
	for _, evidence := range gold {
		normalizedEvidence := normalizeLoose(evidence)
		if normalizedEvidence != "" && strings.Contains(normalizedContext, normalizedEvidence) {
			matched++
		}
	}
	return float64(matched) / float64(len(gold))
}

func tokenPRF(prediction, reference string) (float64, float64, float64) {
	predictionTokens := answerTokens(prediction)
	referenceTokens := answerTokens(reference)
	if len(predictionTokens) == 0 && len(referenceTokens) == 0 {
		return 1, 1, 1
	}
	if len(predictionTokens) == 0 || len(referenceTokens) == 0 {
		return 0, 0, 0
	}
	predictionCounts := tokenCounts(predictionTokens)
	referenceCounts := tokenCounts(referenceTokens)
	common := 0
	for token, predictionCount := range predictionCounts {
		common += min(predictionCount, referenceCounts[token])
	}
	precision := float64(common) / float64(len(predictionTokens))
	recall := float64(common) / float64(len(referenceTokens))
	if precision+recall == 0 {
		return precision, recall, 0
	}
	return precision, recall, 2 * precision * recall / (precision + recall)
}

func rubricCoverage(answer string, rubric []string) float64 {
	if len(rubric) == 0 {
		return 0
	}
	normalizedAnswer := normalizeLoose(answer)
	covered := 0
	for _, item := range rubric {
		normalizedItem := normalizeLoose(item)
		if normalizedItem != "" && strings.Contains(normalizedAnswer, normalizedItem) {
			covered++
		}
	}
	return float64(covered) / float64(len(rubric))
}

func aggregateQuality(cases []CaseResult) QualitySummary {
	summary := QualitySummary{Queries: len(cases)}
	answerable := 0
	unanswerable := 0
	errorsCount := 0
	hitsCount := 0
	abstentionCount := 0
	falseAnswers := 0
	exactCount := 0
	exactTotal := 0
	tokenTotal := 0
	rubricTotal := 0
	for _, result := range cases {
		if result.Error != "" {
			errorsCount++
		}
		if result.Unanswerable {
			unanswerable++
			if result.Metrics.AbstentionOK != nil {
				if *result.Metrics.AbstentionOK {
					abstentionCount++
				} else {
					falseAnswers++
				}
			}
			continue
		}
		answerable++
		summary.RecallAtK += result.Metrics.RecallAtK
		summary.PrecisionAtK += result.Metrics.PrecisionAtK
		summary.NDCGAtK += result.Metrics.NDCGAtK
		summary.MRR += result.Metrics.MRR
		summary.EvidenceRecall += result.Metrics.EvidenceRecall
		if result.Metrics.HitAtK {
			hitsCount++
		}
		if result.Metrics.ExactMatch != nil {
			exactTotal++
			if *result.Metrics.ExactMatch {
				exactCount++
			}
		}
		if result.Metrics.TokenF1 != nil {
			tokenTotal++
			summary.TokenPrecision += pointerValue(result.Metrics.TokenPrecision)
			summary.TokenRecall += pointerValue(result.Metrics.TokenRecall)
			summary.TokenF1 += pointerValue(result.Metrics.TokenF1)
		}
		if result.Metrics.RubricCoverage != nil {
			rubricTotal++
			summary.RubricCoverage += *result.Metrics.RubricCoverage
		}
	}
	summary.AnswerableQueries = answerable
	summary.UnanswerableQueries = unanswerable
	if answerable > 0 {
		denominator := float64(answerable)
		summary.RecallAtK /= denominator
		summary.PrecisionAtK /= denominator
		summary.NDCGAtK /= denominator
		summary.MRR /= denominator
		summary.HitRateAtK = float64(hitsCount) / denominator
		summary.EvidenceRecall /= denominator
	}
	if unanswerable > 0 {
		summary.AbstentionAccuracy = float64(abstentionCount) / float64(unanswerable)
		summary.FalseAnswerRate = float64(falseAnswers) / float64(unanswerable)
	}
	if exactTotal > 0 {
		summary.ExactMatch = float64(exactCount) / float64(exactTotal)
	}
	if tokenTotal > 0 {
		denominator := float64(tokenTotal)
		summary.TokenPrecision /= denominator
		summary.TokenRecall /= denominator
		summary.TokenF1 /= denominator
	}
	if rubricTotal > 0 {
		summary.RubricCoverage /= float64(rubricTotal)
	}
	if len(cases) > 0 {
		summary.ErrorRate = float64(errorsCount) / float64(len(cases))
	}
	return summary
}

func aggregateCategories(cases []CaseResult) []CategorySummary {
	grouped := make(map[string][]CaseResult)
	for _, result := range cases {
		category := strings.TrimSpace(result.Category)
		if category == "" {
			category = "uncategorized"
		}
		grouped[category] = append(grouped[category], result)
	}
	categories := make([]string, 0, len(grouped))
	for category := range grouped {
		categories = append(categories, category)
	}
	sort.Strings(categories)
	result := make([]CategorySummary, 0, len(categories))
	for _, category := range categories {
		result = append(result, CategorySummary{Category: category, Quality: aggregateQuality(grouped[category])})
	}
	return result
}

func latencyStats(values []float64) LatencyStats {
	if len(values) == 0 {
		return LatencyStats{}
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	total := 0.0
	for _, value := range sorted {
		total += value
	}
	return LatencyStats{
		Count: len(sorted), Mean: total / float64(len(sorted)), P50: percentile(sorted, 0.50),
		P95: percentile(sorted, 0.95), P99: percentile(sorted, 0.99), Max: sorted[len(sorted)-1],
	}
}

func percentile(sorted []float64, quantile float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if quantile <= 0 {
		return sorted[0]
	}
	if quantile >= 1 {
		return sorted[len(sorted)-1]
	}
	position := quantile * float64(len(sorted)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return sorted[lower]
	}
	weight := position - float64(lower)
	return sorted[lower]*(1-weight) + sorted[upper]*weight
}

func estimatedTokens(hits []SearchHit) int {
	total := 0
	for _, hit := range hits {
		total += len(answerTokens(hit.Content))
	}
	return total
}

func normalizeAnswer(value string) string {
	return strings.Join(answerTokens(value), " ")
}

func normalizeLoose(value string) string {
	return strings.Join(answerTokens(value), " ")
}

func answerTokens(value string) []string {
	value = strings.ToLower(value)
	return strings.FieldsFunc(value, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func tokenCounts(tokens []string) map[string]int {
	counts := make(map[string]int, len(tokens))
	for _, token := range tokens {
		counts[token]++
	}
	return counts
}

func isInsufficientEvidence(value string) bool {
	normalized := strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(value)), " ", "_")
	return strings.Contains(normalized, "INSUFFICIENT_EVIDENCE") || strings.Contains(normalized, "NOT_ENOUGH_INFORMATION")
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	total := 0.0
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

func boolPointer(value bool) *bool { return &value }
func floatPointer(value float64) *float64 { return &value }

func pointerValue(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}
