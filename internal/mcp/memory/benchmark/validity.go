package benchmark

import (
	"strings"

	errors "github.com/Laisky/errors/v2"
)

const invalidReportWarning = "Benchmark run is INVALID because one or more query cases or lifecycle operations failed. Failed query cases are excluded from aggregate quality metrics, abstention credit is disabled, and this report must not be used as a baseline."

// FinalizeReport derives the durable validity status and warning text from query and lifecycle errors.
func FinalizeReport(report *Report) {
	if report == nil {
		return
	}
	if reportFailureCount(report) > 0 || report.Quality.ErrorRate > 0 || len(report.ExecutionErrors) > 0 {
		report.Status = ReportStatusInvalid
		if !containsWarning(report.Warnings, invalidReportWarning) {
			report.Warnings = append(report.Warnings, invalidReportWarning)
		}
		return
	}
	report.Status = ReportStatusValid
}

// ValidateReport returns an error when a report is nil or contains query or lifecycle failures.
func ValidateReport(report *Report) error {
	if report == nil {
		return errors.New("benchmark report is nil")
	}
	FinalizeReport(report)
	if report.Status != ReportStatusValid {
		total := report.Quality.Queries
		if total == 0 {
			total = len(report.Cases)
		}
		return errors.Errorf(
			"benchmark report is invalid: %d of %d queries failed and %d lifecycle operations failed",
			reportFailureCount(report),
			total,
			len(report.ExecutionErrors),
		)
	}
	return nil
}

func reportFailureCount(report *Report) int {
	if report == nil {
		return 0
	}
	failed := report.Quality.FailedQueries
	caseFailures := 0
	for _, result := range report.Cases {
		if strings.TrimSpace(result.Error) != "" {
			caseFailures++
		}
	}
	if caseFailures > failed {
		return caseFailures
	}
	return failed
}

func containsWarning(warnings []string, expected string) bool {
	for _, warning := range warnings {
		if strings.TrimSpace(warning) == expected {
			return true
		}
	}
	return false
}
