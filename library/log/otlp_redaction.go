package log

import (
	"regexp"
	"strconv"
	"strings"
)

var otlpKeyVersion = regexp.MustCompile("v[0-9]+$")

// sensitiveOTLPKey follows the SDK's targeted credential/payload-key contract.
// The extra request/result and memory/file payload names occur in GraphQL source logs.
func sensitiveOTLPKey(key string) bool {
	compact := strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(strings.ToLower(key))
	compact = otlpKeyVersion.ReplaceAllString(compact, "")
	switch compact {
	case "query", "querytext", "querystring", "searchquery", "prompt", "content", "body", "payload", "headers",
		"url", "uri", "password", "passwd", "pwd", "secret", "token", "credential", "credentials", "cookie", "setcookie",
		"html", "markdown", "rawresponse", "receipt",
		"request", "response", "arguments", "currentinput", "currentinputtext", "conversationitems", "inputitems", "outputitems", "filesummary", "chunkcontent":
		return true
	}
	for _, suffix := range []string{
		"apikey", "apitoken", "accesskey", "accesstoken", "refreshtoken", "idtoken", "authtoken", "pushtoken",
		"clientsecret", "authorization", "authheader", "credential", "credentials", "cookieheader", "bearertoken",
		"authorizationtoken", "sessiontoken", "password", "passwd", "passwordhash", "privatekey", "secretkey", "secret",
		"tokenprefix", "apikeyprefix", "requestheaders", "responseheaders", "requestbody", "responsebody", "rawbody",
		"requestpayload", "responsepayload", "rawpayload", "targeturl", "requesturl", "responseurl",
		"rawhtml", "resulthtml", "rawmarkdown", "resultmarkdown", "egressreceipt",
	} {
		if strings.HasSuffix(compact, suffix) {
			return true
		}
	}
	return false
}

// formatOTLPUint retains unsigned values that exceed the OTLP signed integer range.
func formatOTLPUint(value uint64) string { return strconv.FormatUint(value, 10) }

// reservedOTLPKey protects canonical source identity, record metadata and bridge accounting from field spoofing.
func reservedOTLPKey(key string) bool {
	switch key {
	case "service.name", "service.namespace", "host.name", "deployment.environment.name", "logger.name",
		"app.log.attributes_dropped", "app.log.content_truncated", "_msg", "_time", "_stream":
		return true
	default:
		return false
	}
}
