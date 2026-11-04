package crawleregress

import (
	"encoding/json"
	"mime"
	"net"
	"net/url"
	"strings"
)

// FailureContext contains optional worker-observed diagnostics, never page
// content or connection proof. Pointer counts distinguish absent values from zero.
type FailureContext struct {
	TargetScheme                 string `json:"target_scheme,omitempty"`
	TargetHostname               string `json:"target_hostname,omitempty"`
	FinalScheme                  string `json:"final_scheme,omitempty"`
	FinalHostname                string `json:"final_hostname,omitempty"`
	UpstreamStatus               *int64 `json:"upstream_status,omitempty"`
	ContentType                  string `json:"content_type,omitempty"`
	RedirectCount                *int64 `json:"redirect_count,omitempty"`
	RenderedHTMLBytes            *int64 `json:"rendered_html_bytes,omitempty"`
	ConversionInputBytes         *int64 `json:"conversion_input_bytes,omitempty"`
	ConversionOutputBytes        *int64 `json:"conversion_output_bytes,omitempty"`
	ConversionOutputTrimmedBytes *int64 `json:"conversion_output_trimmed_bytes,omitempty"`
	BodyTextRunes                *int64 `json:"body_text_runes,omitempty"`
	BodyTextNodes                *int64 `json:"body_text_nodes,omitempty"`
	Converter                    string `json:"converter,omitempty"`
}

// TargetComponents selects only an admitted URL's scheme and hostname. Paths,
// query, userinfo, fragments, ports and full URL strings are never diagnostic fields.
func TargetComponents(raw string) (scheme, hostname string) {
	parsed, err := url.Parse(RequestURL(raw))
	if err != nil || parsed.Hostname() == "" {
		return "", ""
	}
	hostname = strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if !diagnosticHostname(hostname) {
		return "", ""
	}
	return parsed.Scheme, hostname
}

// ReviewedFailureContext copies a complete valid diagnostic envelope. Invalid
// optional metadata is not attributed to a task; it never changes result handling.
// The 2048-byte wire ceiling accommodates all fixed fields at their valid maxima.
func ReviewedFailureContext(value *FailureContext) *FailureContext {
	if value == nil {
		return nil
	}
	cloned := *value
	if !diagnosticComponents(cloned.TargetScheme, cloned.TargetHostname) || !diagnosticComponents(cloned.FinalScheme, cloned.FinalHostname) {
		return nil
	}
	if cloned.ContentType != "" {
		if len(cloned.ContentType) > 127 {
			return nil
		}
		media, _, err := mime.ParseMediaType(cloned.ContentType)
		if err != nil || !strings.Contains(media, "/") || media != cloned.ContentType {
			return nil
		}
	}
	if cloned.Converter != "" && cloned.Converter != "html-to-markdown/v2" {
		return nil
	}
	if cloned.UpstreamStatus != nil && (*cloned.UpstreamStatus < 100 || *cloned.UpstreamStatus > 599) {
		return nil
	}
	if cloned.RedirectCount != nil && (*cloned.RedirectCount < 0 || *cloned.RedirectCount > MaxRedirects) {
		return nil
	}
	pointers := []**int64{&cloned.UpstreamStatus, &cloned.RedirectCount, &cloned.RenderedHTMLBytes, &cloned.ConversionInputBytes, &cloned.ConversionOutputBytes, &cloned.ConversionOutputTrimmedBytes, &cloned.BodyTextRunes, &cloned.BodyTextNodes}
	for _, pointer := range pointers {
		if *pointer == nil {
			continue
		}
		count := **pointer
		if count < 0 {
			return nil
		}
		*pointer = &count
	}
	encoded, err := json.Marshal(cloned)
	if err != nil || len(encoded) > 2048 {
		return nil
	}
	return &cloned
}

// diagnosticComponents validates deliberately selected scheme/hostname pairs.
func diagnosticComponents(scheme, hostname string) bool {
	if scheme == "" && hostname == "" {
		return true
	}
	return RequestURL(scheme+"://"+net.JoinHostPort(hostname, "80")) != "" && diagnosticHostname(hostname)
}

// diagnosticHostname checks a scalar hostname without resolving it or admitting
// a new connection. Existing task/policy binding remains the authority.
func diagnosticHostname(hostname string) bool {
	if hostname == "" || len(hostname) > 253 || strings.ContainsAny(hostname, "/?#@ ") {
		return false
	}
	parsed, err := url.Parse("http://" + net.JoinHostPort(hostname, "80"))
	return err == nil && parsed.User == nil && parsed.Hostname() == hostname && parsed.Port() == "80" && parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == ""
}
