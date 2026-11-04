package crawleregress

const workerFailureStageRender = "render"

// WorkerFailureDiagnostic validates content-free worker stage/category pairs.
// These values describe a failed result; they are not receipt or identity proof.
func WorkerFailureDiagnostic(stage, category string) (string, string) {
	switch stage {
	case "admission", workerFailureStageRender, "verification", "conversion":
	default:
		return "", ""
	}
	switch category {
	case "canceled", "timeout", "worker_failed":
		return stage, category
	}
	var approved bool
	switch stage {
	case "admission":
		approved = category == "policy_missing" || category == "policy_invalid" || category == "unsupported_proxy" || category == "task_missing"
	case workerFailureStageRender:
		approved = category == "render_failed" || category == "challenge" || category == "empty_body" || category == "result_too_large"
	case "verification":
		approved = category == "evidence_rejected"
	case "conversion":
		approved = category == "conversion_failed" || category == "empty_body" || category == "empty_markdown" || category == "result_too_large"
	}
	if !approved {
		return "", ""
	}
	return stage, category
}

// WorkerChallengeSignal validates finite reasons for a render/challenge result.
// Callers must independently bind the failed result to the submitted task.
// These diagnostic reasons contain no page content and cannot release a body.
func WorkerChallengeSignal(stage, category, signal string) string {
	if stage != workerFailureStageRender || category != "challenge" {
		return ""
	}
	switch signal {
	case "widget_element", "error_element", "challenge_platform_script", "challenge_options_assignment":
		return signal
	default:
		return ""
	}
}
