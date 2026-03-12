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

// Package main implements the ai-context-manager Wasm plugin.
// This plugin provides context engineering and memory management for LLM conversations,
// inspired by Google ADK's session and memory management approach.
//
// Features:
//   - Session-based conversation context management
//   - Token-aware context window management (slides old messages out when nearing limit)
//   - Redis-backed persistent memory storage
//   - System prompt injection based on session context
//   - Support for multiple memory strategies: sliding window, summarize (future), RAG (future)
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/log"
	"github.com/higress-group/wasm-go/pkg/wrapper"
	"github.com/tidwall/gjson"
	"github.com/tidwall/resp"
	"github.com/tidwall/sjson"
)

const (
	pluginName = "ai-context-manager"

	// Context keys
	ctxSessionID     = "acm_session_id"
	ctxMessages      = "acm_messages"
	ctxSystemPrompt  = "acm_system_prompt"
	ctxIsStream      = "acm_is_stream"
	ctxPartialMsg    = "acm_partial_msg"
	ctxAssistantResp = "acm_assistant_resp"

	// Redis key patterns
	redisKeySession  = "acm:session:%s"       // session messages
	redisKeyMeta     = "acm:meta:%s"          // session metadata
	redisKeyMemory   = "acm:memory:%s"        // long-term memory
	redisKeyToolCall = "acm:toolcall:%s"      // tool call tracking

	// Default values
	defaultMaxContextTokens = 4096
	defaultMaxTurns         = 20
	defaultSessionTTL       = 3600 // 1 hour in seconds
	defaultCacheKeyPrefix   = "acm:"
	defaultSessionHeader    = "x-session-id"

	// Memory strategy constants
	strategySliding = "sliding_window"
	strategyTrimOld = "trim_oldest"
)

func main() {}

func init() {
	wrapper.SetCtx(
		pluginName,
		wrapper.ParseConfig(parseConfig),
		wrapper.ProcessRequestHeaders(onHttpRequestHeaders),
		wrapper.ProcessRequestBody(onHttpRequestBody),
		wrapper.ProcessResponseHeaders(onHttpResponseHeaders),
		wrapper.ProcessStreamingResponseBody(onHttpStreamingResponseBody),
		wrapper.ProcessResponseBody(onHttpResponseBody),
	)
}

// RedisInfo holds Redis connection configuration.
type RedisInfo struct {
	ServiceName string `json:"serviceName" yaml:"serviceName"`
	ServicePort int    `json:"servicePort" yaml:"servicePort"`
	Username    string `json:"username" yaml:"username"`
	Password    string `json:"password" yaml:"password"`
	Timeout     int    `json:"timeout" yaml:"timeout"`
	Database    int    `json:"database" yaml:"database"`
}

// ContextManagerConfig holds the plugin configuration.
type ContextManagerConfig struct {
	// Redis connection info for persistent storage
	Redis RedisInfo `json:"redis" yaml:"redis"`

	// SessionHeader is the HTTP header used to identify the session.
	// Defaults to "x-session-id".
	SessionHeader string `json:"sessionHeader" yaml:"sessionHeader"`

	// SystemPrompt is injected as the first system message in every request
	// when no system message is present.
	SystemPrompt string `json:"systemPrompt" yaml:"systemPrompt"`

	// MaxContextTokens limits the total token count in the context window.
	// When exceeded, oldest non-system messages are removed.
	// A rough estimate of 4 chars per token is used for counting.
	MaxContextTokens int `json:"maxContextTokens" yaml:"maxContextTokens"`

	// MaxTurns limits the number of conversation turns (user+assistant pairs) kept in context.
	// 0 means no limit (rely on MaxContextTokens only).
	MaxTurns int `json:"maxTurns" yaml:"maxTurns"`

	// SessionTTL is the time-to-live for session data in Redis (seconds).
	SessionTTL int `json:"sessionTTL" yaml:"sessionTTL"`

	// MemoryStrategy controls how context is trimmed:
	//   "sliding_window" (default): removes oldest turns first
	//   "trim_oldest": same as sliding_window
	MemoryStrategy string `json:"memoryStrategy" yaml:"memoryStrategy"`

	// EnableMemory enables long-term memory (stored separately from session context).
	// When enabled, a summary of past conversations is injected as a system message.
	EnableMemory bool `json:"enableMemory" yaml:"enableMemory"`

	// MemoryPrefix is prepended to system message when injecting long-term memory.
	MemoryPrefix string `json:"memoryPrefix" yaml:"memoryPrefix"`

	// Internal fields
	redisClient wrapper.RedisClient
}

// Message represents a single LLM conversation message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func parseConfig(jsonConfig gjson.Result, config *ContextManagerConfig) error {
	// Session header
	config.SessionHeader = jsonConfig.Get("sessionHeader").String()
	if config.SessionHeader == "" {
		config.SessionHeader = defaultSessionHeader
	}

	// System prompt
	config.SystemPrompt = jsonConfig.Get("systemPrompt").String()

	// Token limit
	config.MaxContextTokens = int(jsonConfig.Get("maxContextTokens").Int())
	if config.MaxContextTokens <= 0 {
		config.MaxContextTokens = defaultMaxContextTokens
	}

	// Max turns
	config.MaxTurns = int(jsonConfig.Get("maxTurns").Int())
	if config.MaxTurns <= 0 {
		config.MaxTurns = defaultMaxTurns
	}

	// Session TTL
	config.SessionTTL = int(jsonConfig.Get("sessionTTL").Int())
	if config.SessionTTL <= 0 {
		config.SessionTTL = defaultSessionTTL
	}

	// Memory strategy
	config.MemoryStrategy = jsonConfig.Get("memoryStrategy").String()
	if config.MemoryStrategy == "" {
		config.MemoryStrategy = strategySliding
	}

	// Long-term memory
	config.EnableMemory = jsonConfig.Get("enableMemory").Bool()
	config.MemoryPrefix = jsonConfig.Get("memoryPrefix").String()
	if config.MemoryPrefix == "" {
		config.MemoryPrefix = "Previous conversation summary: "
	}

	// Redis configuration
	redisConfig := jsonConfig.Get("redis")
	if !redisConfig.Exists() {
		return errors.New("missing redis configuration")
	}
	serviceName := redisConfig.Get("serviceName").String()
	if serviceName == "" {
		return errors.New("redis serviceName must not be empty")
	}

	servicePort := int(redisConfig.Get("servicePort").Int())
	if servicePort == 0 {
		if strings.HasSuffix(serviceName, ".static") {
			servicePort = 80
		} else {
			servicePort = 6379
		}
	}

	config.Redis.ServiceName = serviceName
	config.Redis.ServicePort = servicePort
	config.Redis.Username = redisConfig.Get("username").String()
	config.Redis.Password = redisConfig.Get("password").String()
	config.Redis.Timeout = int(redisConfig.Get("timeout").Int())
	if config.Redis.Timeout == 0 {
		config.Redis.Timeout = 1000
	}
	config.Redis.Database = int(redisConfig.Get("database").Int())

	config.redisClient = wrapper.NewRedisClusterClient(wrapper.FQDNCluster{
		FQDN: serviceName,
		Port: int64(servicePort),
	})

	return config.redisClient.Init(
		config.Redis.Username,
		config.Redis.Password,
		int64(config.Redis.Timeout),
		wrapper.WithDataBase(config.Redis.Database),
	)
}

func onHttpRequestHeaders(ctx wrapper.HttpContext, config ContextManagerConfig) types.Action {
	ctx.DisableReroute()

	// Only process chat completion endpoints
	path := ctx.Path()
	if !isChatCompletionPath(path) {
		ctx.DontReadRequestBody()
		return types.ActionContinue
	}

	// Extract or generate session ID
	sessionID, _ := proxywasm.GetHttpRequestHeader(config.SessionHeader)
	if sessionID == "" {
		sessionID = generateSessionID(ctx)
	}
	ctx.SetContext(ctxSessionID, sessionID)

	// Remove content-length so we can modify the body
	proxywasm.RemoveHttpRequestHeader("content-length")

	return types.ActionContinue
}

func onHttpRequestBody(ctx wrapper.HttpContext, config ContextManagerConfig, body []byte) types.Action {
	sessionIDI := ctx.GetContext(ctxSessionID)
	if sessionIDI == nil {
		return types.ActionContinue
	}
	sessionID := sessionIDI.(string)

	// Parse current messages from the request
	messagesResult := gjson.GetBytes(body, "messages")
	if !messagesResult.Exists() {
		return types.ActionContinue
	}

	var currentMessages []Message
	if err := json.Unmarshal([]byte(messagesResult.Raw), &currentMessages); err != nil {
		log.Errorf("[ai-context-manager] failed to parse messages: %v", err)
		return types.ActionContinue
	}

	// Extract the latest user message and remember if stream mode
	stream := gjson.GetBytes(body, "stream").Bool()
	ctx.SetContext(ctxIsStream, stream)

	// Fetch history from Redis and merge with current messages
	redisKey := fmt.Sprintf(redisKeySession, sessionID)
	err := config.redisClient.Get(redisKey, func(response resp.Value) {
		var history []Message
		if !response.IsNull() {
			if err := json.Unmarshal([]byte(response.String()), &history); err != nil {
				log.Errorf("[ai-context-manager] failed to unmarshal history: %v", err)
			}
		}

		// Merge history with current request
		mergedMessages := mergeMessages(history, currentMessages, config)

		// Optionally inject long-term memory as system message
		if config.EnableMemory {
			mergedMessages = injectMemory(ctx, config, sessionID, mergedMessages)
		}

		// Encode user question for later storage
		if latestUserMsg := getLatestUserMessage(currentMessages); latestUserMsg != "" {
			ctx.SetContext("acm_user_msg", latestUserMsg)
		}

		// Re-encode the messages into the request body
		newMessagesJSON, err := json.Marshal(mergedMessages)
		if err != nil {
			log.Errorf("[ai-context-manager] failed to marshal merged messages: %v", err)
			proxywasm.ResumeHttpRequest()
			return
		}

		newBody, err := sjson.SetRawBytes(body, "messages", newMessagesJSON)
		if err != nil {
			log.Errorf("[ai-context-manager] failed to set messages in body: %v", err)
			proxywasm.ResumeHttpRequest()
			return
		}

		if err := proxywasm.ReplaceHttpRequestBody(newBody); err != nil {
			log.Errorf("[ai-context-manager] failed to replace request body: %v", err)
		}

		proxywasm.ResumeHttpRequest()
	})

	if err != nil {
		log.Errorf("[ai-context-manager] redis GET failed: %v", err)
		return types.ActionContinue
	}

	return types.HeaderStopAllIterationAndWatermark
}

func onHttpResponseHeaders(ctx wrapper.HttpContext, config ContextManagerConfig) types.Action {
	if ctx.GetContext(ctxSessionID) == nil {
		ctx.DontReadResponseBody()
		return types.ActionContinue
	}

	contentType, _ := proxywasm.GetHttpResponseHeader("content-type")
	if strings.Contains(contentType, "text/event-stream") {
		ctx.SetContext(ctxIsStream, true)
	}

	return types.ActionContinue
}

func onHttpStreamingResponseBody(ctx wrapper.HttpContext, config ContextManagerConfig, data []byte, endOfStream bool) []byte {
	if ctx.GetContext(ctxSessionID) == nil {
		return data
	}

	// Accumulate partial messages
	var buf []byte
	if partial := ctx.GetContext(ctxPartialMsg); partial != nil {
		buf = append(partial.([]byte), data...)
	} else {
		buf = data
	}

	// Process complete SSE events
	lines := strings.Split(string(buf), "\n\n")
	for i, line := range lines {
		if i == len(lines)-1 {
			// Potentially incomplete, buffer it
			if line != "" {
				ctx.SetContext(ctxPartialMsg, []byte(line))
			} else {
				ctx.SetContext(ctxPartialMsg, nil)
			}
			break
		}
		// Extract content from SSE data field
		extractStreamContent(ctx, line)
	}

	if endOfStream {
		saveConversationToRedis(ctx, config)
	}

	return data
}

func onHttpResponseBody(ctx wrapper.HttpContext, config ContextManagerConfig, body []byte) types.Action {
	if ctx.GetContext(ctxSessionID) == nil {
		return types.ActionContinue
	}

	// Non-streaming response: extract assistant message
	content := gjson.GetBytes(body, "choices.0.message.content").String()
	if content != "" {
		ctx.SetContext(ctxAssistantResp, content)
	}

	saveConversationToRedis(ctx, config)
	return types.ActionContinue
}

// --- Helper functions ---

// isChatCompletionPath returns true if the path is an OpenAI-compatible chat completion endpoint.
func isChatCompletionPath(path string) bool {
	parsed, err := url.Parse(path)
	if err != nil {
		return false
	}
	p := parsed.Path
	return strings.HasSuffix(p, "/chat/completions") || strings.Contains(p, "/chat/completions")
}

// generateSessionID creates a unique session ID from request metadata.
func generateSessionID(ctx wrapper.HttpContext) string {
	// Use a combination of request ID and timestamp as fallback
	reqID, _ := proxywasm.GetHttpRequestHeader("x-request-id")
	if reqID != "" {
		return "auto-" + reqID
	}
	return fmt.Sprintf("auto-%d-%d", ctx.ID(), time.Now().UnixNano())
}

// mergeMessages merges session history with the current request messages, applying
// context window limits.
func mergeMessages(history, current []Message, config ContextManagerConfig) []Message {
	// Separate system messages from the current request
	var systemMsgs []Message
	var userMsgs []Message
	for _, m := range current {
		if m.Role == "system" {
			systemMsgs = append(systemMsgs, m)
		} else {
			userMsgs = append(userMsgs, m)
		}
	}

	// If there is no system message and a default system prompt is configured, inject it
	if len(systemMsgs) == 0 && config.SystemPrompt != "" {
		systemMsgs = append(systemMsgs, Message{Role: "system", Content: config.SystemPrompt})
	}

	// Append history (skip any system messages in history to avoid duplication)
	var historyUserMsgs []Message
	for _, m := range history {
		if m.Role != "system" {
			historyUserMsgs = append(historyUserMsgs, m)
		}
	}

	// Combine history + new user messages
	allMsgs := append(historyUserMsgs, userMsgs...)

	// Apply MaxTurns limit (each turn = user message + assistant message = 2 entries)
	maxMsgs := config.MaxTurns * 2
	if maxMsgs > 0 && len(allMsgs) > maxMsgs {
		allMsgs = allMsgs[len(allMsgs)-maxMsgs:]
	}

	// Apply token-based limit (rough estimate: 4 chars per token)
	allMsgs = trimToTokenLimit(allMsgs, config.MaxContextTokens)

	// Assemble final message list: system + trimmed context
	return append(systemMsgs, allMsgs...)
}

// trimToTokenLimit removes oldest messages until the total token estimate fits within limit.
func trimToTokenLimit(messages []Message, maxTokens int) []Message {
	if maxTokens <= 0 {
		return messages
	}

	// Estimate total tokens (4 chars ≈ 1 token is a conservative approximation)
	total := estimateTokens(messages)
	if total <= maxTokens {
		return messages
	}

	// Remove from front (oldest messages) until we fit
	for total > maxTokens && len(messages) > 1 {
		removed := messages[0]
		messages = messages[1:]
		total -= estimateMessageTokens(removed)
	}

	return messages
}

// estimateTokens returns a rough token count for a slice of messages.
func estimateTokens(messages []Message) int {
	total := 0
	for _, m := range messages {
		total += estimateMessageTokens(m)
	}
	return total
}

// estimateMessageTokens returns a rough token count for a single message.
func estimateMessageTokens(m Message) int {
	// 4 chars ≈ 1 token, plus ~4 tokens overhead per message
	return (len(m.Role)+len(m.Content))/4 + 4
}

// getLatestUserMessage returns the content of the last user message in the list.
func getLatestUserMessage(messages []Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return messages[i].Content
		}
	}
	return ""
}

// injectMemory prepends long-term memory as an additional system message.
func injectMemory(ctx wrapper.HttpContext, config ContextManagerConfig, sessionID string, messages []Message) []Message {
	memKey := fmt.Sprintf(redisKeyMemory, sessionID)

	// Synchronous Redis GET is not directly available in Wasm, so we read from
	// request context where memory was pre-fetched during headers phase.
	memI := ctx.GetContext("acm_memory")
	if memI == nil {
		// No memory available yet, try to fetch in background (best effort)
		_ = config.redisClient.Get(memKey, func(response resp.Value) {
			if !response.IsNull() && response.String() != "" {
				ctx.SetContext("acm_memory", response.String())
			}
		})
		return messages
	}

	memContent := memI.(string)
	if memContent == "" {
		return messages
	}

	memMsg := Message{
		Role:    "system",
		Content: config.MemoryPrefix + memContent,
	}

	// Insert after existing system messages
	insertAt := 0
	for i, m := range messages {
		if m.Role == "system" {
			insertAt = i + 1
		}
	}

	result := make([]Message, 0, len(messages)+1)
	result = append(result, messages[:insertAt]...)
	result = append(result, memMsg)
	result = append(result, messages[insertAt:]...)
	return result
}

// extractStreamContent parses an SSE event and accumulates the assistant response.
func extractStreamContent(ctx wrapper.HttpContext, sseEvent string) {
	for _, line := range strings.Split(sseEvent, "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[5:])
		if data == "[DONE]" {
			return
		}
		content := gjson.Get(data, "choices.0.delta.content").String()
		if content == "" {
			continue
		}
		existing := ""
		if prev := ctx.GetContext(ctxAssistantResp); prev != nil {
			existing = prev.(string)
		}
		ctx.SetContext(ctxAssistantResp, existing+content)
	}
}

// saveConversationToRedis persists the latest user+assistant turn to Redis.
func saveConversationToRedis(ctx wrapper.HttpContext, config ContextManagerConfig) {
	sessionIDI := ctx.GetContext(ctxSessionID)
	if sessionIDI == nil {
		return
	}
	sessionID := sessionIDI.(string)

	userMsgI := ctx.GetContext("acm_user_msg")
	assistantRespI := ctx.GetContext(ctxAssistantResp)
	if userMsgI == nil || assistantRespI == nil {
		return
	}

	userMsg := userMsgI.(string)
	assistantResp := assistantRespI.(string)
	if userMsg == "" || assistantResp == "" {
		return
	}

	redisKey := fmt.Sprintf(redisKeySession, sessionID)

	// Fetch existing history and append new turn
	_ = config.redisClient.Get(redisKey, func(response resp.Value) {
		var history []Message
		if !response.IsNull() {
			if err := json.Unmarshal([]byte(response.String()), &history); err != nil {
				log.Warnf("[ai-context-manager] could not parse existing history: %v", err)
			}
		}

		history = append(history, Message{Role: "user", Content: userMsg})
		history = append(history, Message{Role: "assistant", Content: assistantResp})

		// Trim to MaxTurns before storing
		maxMsgs := config.MaxTurns * 2
		if maxMsgs > 0 && len(history) > maxMsgs {
			history = history[len(history)-maxMsgs:]
		}

		encoded, err := json.Marshal(history)
		if err != nil {
			log.Errorf("[ai-context-manager] failed to marshal history: %v", err)
			return
		}

		_ = config.redisClient.Set(redisKey, string(encoded), nil)
		if config.SessionTTL > 0 {
			_ = config.redisClient.Expire(redisKey, config.SessionTTL, nil)
		}
	})
}

// intToStr converts an integer to string.
func intToStr(n int) string {
	return strconv.Itoa(n)
}

var _ = intToStr // suppress unused warning
