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

	// KeyTaskHTMLCrawlerPending is the key for HTML crawler pending tasks
	KeyTaskHTMLCrawlerPending = keyPrefixTask + "html_crawler/pending"
	// KeyPrefixTaskHTMLCrawlerResult is the key prefix for HTML crawler results
	//
	//  `KeyPrefixTaskHTMLCrawlerResult + <task_id>`
	KeyPrefixTaskHTMLCrawlerResult = keyPrefixTask + "html_crawler/result/"
)
