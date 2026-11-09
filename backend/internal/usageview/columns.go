package usageview

// Source Store usageColumns order, including legacy and partial accounting.
var columns = [][2]string{
	{"reported_input_tokens", "reportedInputTokens"},
	{"reported_output_tokens", "reportedOutputTokens"},
	{"reported_usage_calls", "reportedUsageCalls"},
	{"cache_rate_input_tokens", "cacheRateInputTokens"},
	{"cache_rate_read_tokens", "cacheRateReadTokens"},
	{"cache_rate_known_calls", "cacheRateKnownCalls"},
	{"ordinary_input_tokens", "ordinaryInputTokens"},
	{"cache_read_input_tokens", "cacheReadInputTokens"},
	{"cache_write_input_tokens", "cacheWriteInputTokens"},
	{"cache_write_5m_input_tokens", "cacheWrite5mInputTokens"},
	{"cache_write_1h_input_tokens", "cacheWrite1hInputTokens"},
	{"cache_write_30m_input_tokens", "cacheWrite30mInputTokens"},
	{"cache_write_12h_input_tokens", "cacheWrite12hInputTokens"},
	{"cache_known_calls", "cacheKnownCalls"},
	{"cache_read_known_calls", "cacheReadKnownCalls"},
	{"cache_write_known_calls", "cacheWriteKnownCalls"},
	{"partial_priced_calls", "partialPricedCalls"},
	{"legacy_calls", "legacyCalls"},
	{"unknown_usage_calls", "unknownUsageCalls"},
	{"ordinary_input_usd", "ordinaryInputUsd"},
	{"output_usd", "outputUsd"},
	{"cache_read_usd", "cacheReadUsd"},
	{"cache_write_usd", "cacheWriteUsd"},
	{"legacy_estimated_usd", "legacyEstimatedUsd"},
}
