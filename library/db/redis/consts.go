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
)
