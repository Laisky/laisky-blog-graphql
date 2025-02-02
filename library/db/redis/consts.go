package redis

const (
	keyPrefix     = "laisky/"
	keyPrefixTask = keyPrefix + "tasks/"

	// KeyPrefixTaskLLMStorm is the key prefix for LLM storm tasks
	KeyPrefixTaskLLMStorm = keyPrefixTask + "llm_storm/"
)
