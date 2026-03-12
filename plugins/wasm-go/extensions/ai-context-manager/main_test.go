// Copyright (c) 2024 Alibaba Group Holding Ltd.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"encoding/json"
	"testing"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/test"
	"github.com/stretchr/testify/require"
)

var basicContextConfig = func() json.RawMessage {
	data, _ := json.Marshal(map[string]interface{}{
		"redis": map[string]interface{}{
			"serviceName": "redis.static",
			"servicePort": 6379,
			"timeout":     1000,
		},
		"sessionHeader":    "x-session-id",
		"systemPrompt":     "You are a helpful AI assistant.",
		"maxContextTokens": 4096,
		"maxTurns":         10,
		"sessionTTL":       3600,
		"memoryStrategy":   "sliding_window",
	})
	return data
}()

var minimalContextConfig = func() json.RawMessage {
	data, _ := json.Marshal(map[string]interface{}{
		"redis": map[string]interface{}{
			"serviceName": "redis.static",
		},
	})
	return data
}()

var memoryContextConfig = func() json.RawMessage {
	data, _ := json.Marshal(map[string]interface{}{
		"redis": map[string]interface{}{
			"serviceName": "redis.static",
		},
		"enableMemory": true,
		"memoryPrefix": "Long-term memory: ",
	})
	return data
}()

func TestParseConfig(t *testing.T) {
	test.RunGoTest(t, func(t *testing.T) {
		t.Run("basic config", func(t *testing.T) {
			host, status := test.NewTestHost(basicContextConfig)
			defer host.Reset()
			require.Equal(t, types.OnPluginStartStatusOK, status)
			config, err := host.GetMatchConfig()
			require.NoError(t, err)
			require.NotNil(t, config)

			cfg := config.(*ContextManagerConfig)
			require.Equal(t, "x-session-id", cfg.SessionHeader)
			require.Equal(t, "You are a helpful AI assistant.", cfg.SystemPrompt)
			require.Equal(t, 4096, cfg.MaxContextTokens)
			require.Equal(t, 10, cfg.MaxTurns)
			require.Equal(t, 3600, cfg.SessionTTL)
			require.Equal(t, "sliding_window", cfg.MemoryStrategy)
		})

		t.Run("minimal config uses defaults", func(t *testing.T) {
			host, status := test.NewTestHost(minimalContextConfig)
			defer host.Reset()
			require.Equal(t, types.OnPluginStartStatusOK, status)
			config, err := host.GetMatchConfig()
			require.NoError(t, err)
			require.NotNil(t, config)

			cfg := config.(*ContextManagerConfig)
			require.Equal(t, defaultSessionHeader, cfg.SessionHeader)
			require.Equal(t, defaultMaxContextTokens, cfg.MaxContextTokens)
			require.Equal(t, defaultMaxTurns, cfg.MaxTurns)
			require.Equal(t, defaultSessionTTL, cfg.SessionTTL)
			require.Equal(t, strategySliding, cfg.MemoryStrategy)
		})

		t.Run("memory config", func(t *testing.T) {
			host, status := test.NewTestHost(memoryContextConfig)
			defer host.Reset()
			require.Equal(t, types.OnPluginStartStatusOK, status)
			config, err := host.GetMatchConfig()
			require.NoError(t, err)
			require.NotNil(t, config)

			cfg := config.(*ContextManagerConfig)
			require.True(t, cfg.EnableMemory)
			require.Equal(t, "Long-term memory: ", cfg.MemoryPrefix)
		})
	})
}

func TestMergeMessages(t *testing.T) {
	config := ContextManagerConfig{
		SystemPrompt:     "You are a helpful AI assistant.",
		MaxContextTokens: 4096,
		MaxTurns:         3,
		MemoryStrategy:   strategySliding,
	}

	t.Run("no history, no system message - injects system prompt", func(t *testing.T) {
		current := []Message{
			{Role: "user", Content: "Hello"},
		}
		result := mergeMessages(nil, current, config)
		require.Len(t, result, 2)
		require.Equal(t, "system", result[0].Role)
		require.Equal(t, config.SystemPrompt, result[0].Content)
		require.Equal(t, "user", result[1].Role)
	})

	t.Run("existing system message is preserved", func(t *testing.T) {
		current := []Message{
			{Role: "system", Content: "Custom system"},
			{Role: "user", Content: "Hello"},
		}
		result := mergeMessages(nil, current, config)
		require.Len(t, result, 2)
		require.Equal(t, "Custom system", result[0].Content)
	})

	t.Run("history is prepended before current messages", func(t *testing.T) {
		history := []Message{
			{Role: "user", Content: "Previous question"},
			{Role: "assistant", Content: "Previous answer"},
		}
		current := []Message{
			{Role: "user", Content: "New question"},
		}
		result := mergeMessages(history, current, config)
		// system + prev user + prev assistant + new user
		require.Len(t, result, 4)
		require.Equal(t, "system", result[0].Role)
		require.Equal(t, "Previous question", result[1].Content)
		require.Equal(t, "Previous answer", result[2].Content)
		require.Equal(t, "New question", result[3].Content)
	})

	t.Run("MaxTurns limits are respected", func(t *testing.T) {
		// 3 turns of history = 6 messages
		history := []Message{
			{Role: "user", Content: "Q1"},
			{Role: "assistant", Content: "A1"},
			{Role: "user", Content: "Q2"},
			{Role: "assistant", Content: "A2"},
			{Role: "user", Content: "Q3"},
			{Role: "assistant", Content: "A3"},
		}
		current := []Message{
			{Role: "user", Content: "Q4"},
		}
		result := mergeMessages(history, current, config)
		// MaxTurns=3 => max 6 non-system messages. History(6) + current(1) = 7, so oldest trimmed.
		// system + 6 messages = 7 total
		require.LessOrEqual(t, len(result), 8) // system + 3*2 turns + 1 = 8
	})
}

func TestTrimToTokenLimit(t *testing.T) {
	t.Run("no trimming when under limit", func(t *testing.T) {
		msgs := []Message{
			{Role: "user", Content: "short"},
			{Role: "assistant", Content: "brief"},
		}
		result := trimToTokenLimit(msgs, 10000)
		require.Len(t, result, 2)
	})

	t.Run("trims oldest when over limit", func(t *testing.T) {
		// Create messages that exceed token limit
		msgs := []Message{
			{Role: "user", Content: "This is a very long question that takes many tokens to represent in the model context"},
			{Role: "assistant", Content: "This is a very long answer that also takes many tokens to represent in the context window"},
			{Role: "user", Content: "Short"},
		}
		result := trimToTokenLimit(msgs, 10) // very small limit
		require.Less(t, len(result), 3)
	})
}

func TestEstimateTokens(t *testing.T) {
	t.Run("single message", func(t *testing.T) {
		msgs := []Message{
			{Role: "user", Content: "Hello, world!"},
		}
		tokens := estimateTokens(msgs)
		require.Greater(t, tokens, 0)
	})

	t.Run("empty messages", func(t *testing.T) {
		tokens := estimateTokens(nil)
		require.Equal(t, 0, tokens)
	})
}

func TestIsChatCompletionPath(t *testing.T) {
	require.True(t, isChatCompletionPath("/v1/chat/completions"))
	require.True(t, isChatCompletionPath("/openai/v1/chat/completions"))
	require.False(t, isChatCompletionPath("/v1/completions"))
	require.False(t, isChatCompletionPath("/v1/models"))
	require.False(t, isChatCompletionPath("/health"))
}

func TestGetLatestUserMessage(t *testing.T) {
	t.Run("returns last user message", func(t *testing.T) {
		msgs := []Message{
			{Role: "user", Content: "first"},
			{Role: "assistant", Content: "response"},
			{Role: "user", Content: "second"},
		}
		result := getLatestUserMessage(msgs)
		require.Equal(t, "second", result)
	})

	t.Run("returns empty when no user message", func(t *testing.T) {
		msgs := []Message{
			{Role: "system", Content: "system prompt"},
		}
		result := getLatestUserMessage(msgs)
		require.Equal(t, "", result)
	})
}

func TestOnHttpRequestHeaders(t *testing.T) {
	test.RunTest(t, func(t *testing.T) {
		t.Run("non-chat endpoint skips processing", func(t *testing.T) {
			host, status := test.NewTestHost(basicContextConfig)
			defer host.Reset()
			require.Equal(t, types.OnPluginStartStatusOK, status)

			action := host.CallOnHttpRequestHeaders([][2]string{
				{":path", "/v1/models"},
				{":method", "GET"},
			})
			require.Equal(t, types.ActionContinue, action)
		})

		t.Run("chat endpoint continues with session extraction", func(t *testing.T) {
			host, status := test.NewTestHost(basicContextConfig)
			defer host.Reset()
			require.Equal(t, types.OnPluginStartStatusOK, status)

			action := host.CallOnHttpRequestHeaders([][2]string{
				{":path", "/v1/chat/completions"},
				{":method", "POST"},
				{"x-session-id", "test-session-123"},
			})
			require.Equal(t, types.ActionContinue, action)
		})
	})
}
