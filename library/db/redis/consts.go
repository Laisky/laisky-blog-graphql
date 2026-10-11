package redis

const (
	keyPrefix     = "laisky/"
	keyPrefixTask = keyPrefix + "tasks/"

	// KeyTaskLLMStormPending is the key for LLM storm pending tasks
	KeyTaskLLMStormPending = keyPrefixTask + "llm_storm/pending"
	// KeyPrefixTaskLLMStormResult is the key prefix for LLM storm results
	//
	//  `KeyPrefixTaskLLMStormResult + <task_id>`
	KeyPrefixTaskLLMStormResult = keyPrefixTask + "llm_storm/result/"

	// KeyTaskHTMLCrawlerLegacyPending is the queue compiled into legacy workers.
	// Verified producers and consumers must never migrate, drain or fall back to it.
	KeyTaskHTMLCrawlerLegacyPending = keyPrefixTask + "html_crawler/pending"

	// KeyTaskHTMLCrawlerPending is the isolated queue for connection-receipt v1 tasks.
	// Its generation prevents legacy workers from competing for verified tasks.
	KeyTaskHTMLCrawlerPending = keyPrefixTask + "html_crawler/verified/v1/pending"
	// KeyPrefixTaskHTMLCrawlerResult is the key prefix for HTML crawler results
	//
	//  `KeyPrefixTaskHTMLCrawlerResult + <task_id>`
	KeyPrefixTaskHTMLCrawlerResult = keyPrefixTask + "html_crawler/result/"
)
