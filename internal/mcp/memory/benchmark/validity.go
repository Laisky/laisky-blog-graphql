package benchmark

import (
	"strings"

	errors "github.com/Laisky/errors/v2"
)

// FinalizeReport derives the durable validity status and warning text from case errors.
func FinalizeReport(report *Report) {
	if report == nil {
		return
	}
	if report.Quality.Queries == 0 && len(report.Cases) > 0 {
		report.Quality = aggregateQuality(report.Cases)
	}
	if report.Quality.FailedQueries > 0 || report.Quality.ErrorRate > 0 || hasCaseErrors(report.Cases) {
		report.Status = ReportStatusInvalid
		warning := "Benchmark run is INVALID because one or more cases failed. Aggregate quality metrics exclude failed cases, abstention credit is disabled, and this report must not be used as a baseline."
		if !containsWarning(report.Warnings, warning) {
			report.Warnings = append(report.Warnings, warning)
		}
		return
	}
	report.Status = ReportStatusValid
}

// ValidateReport returns an error when a report is nil or contains execution failures.
func ValidateReport(report *Report) error {
	if report == nil {
		return errors.New("benchmark report is nil")
	}
	FinalizeReport(report)
	if report.Status != ReportStatusValid {
		return errors.Errorf(
			"benchmark report is invalid: %d of %d queries failed",
			report.Quality.FailedQueries,
			report.Quality.Queries,
		)
	}
	return nil
}

func containsWarning(warnings []string, expected string) bool {
	for _, warning := range warnings {
		if strings.TrimSpace(warning) == expected {
			return true
		}
	}
	return false
}
