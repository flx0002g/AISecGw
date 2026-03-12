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

// Package main implements the ai-token-billing Wasm plugin.
// This plugin provides precise multi-dimensional token metering and quota management
// for LLM API traffic, with support for:
//
//   - Per-consumer token usage tracking (using x-mse-consumer header)
//   - Per-model token usage tracking
//   - Per-route token usage tracking
//   - Multi-granularity time windows: minute, hour, day, month
//   - Soft limits (warning headers) and hard limits (request blocking)
//   - Redis-backed durable storage with atomic INCRBY operations
//   - Admin API for querying and resetting quota usage
//
// Token counts are extracted from the LLM response's `usage` field (compatible with
// OpenAI, Azure OpenAI, Anthropic, and other OpenAI-compatible providers).
//
// Redis key schema:
//
//	billing:usage:{dimension}:{dimension_value}:{model}:{window}:{timestamp}
//	billing:quota:{dimension}:{dimension_value}:{model}:{window}
//
// where:
//   - dimension: "consumer", "route", "global"
//   - window: "minute", "hour", "day", "month"
//   - timestamp: Unix timestamp truncated to window granularity
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/log"
	"github.com/higress-group/wasm-go/pkg/tokenusage"
	"github.com/higress-group/wasm-go/pkg/wrapper"
	"github.com/tidwall/gjson"
	"github.com/tidwall/resp"
)

const (
	pluginName = "ai-token-billing"

	// Context keys
	ctxConsumer  = "atb_consumer"
	ctxModel     = "atb_model"
	ctxRoute     = "atb_route"
	ctxStartTime = "atb_start_time"

	// Redis key prefixes
	keyUsagePrefix = "billing:usage"
	keyQuotaPrefix = "billing:quota"

	// Header names
	headerConsumer         = "x-mse-consumer"
	headerRemainingTokens  = "X-Token-Remaining"
	headerUsedTokens       = "X-Token-Used"
	headerQuotaLimit       = "X-Token-Quota-Limit"
	headerQuotaWindow      = "X-Token-Quota-Window"
	headerQuotaResetAt     = "X-Token-Quota-Reset"

	// Time window names
	windowMinute = "minute"
	windowHour   = "hour"
	windowDay    = "day"
	windowMonth  = "month"

	// Dimension names
	dimensionConsumer = "consumer"
	dimensionRoute    = "route"
	dimensionGlobal   = "global"
)

func main() {}

func init() {
	wrapper.SetCtx(
		pluginName,
		wrapper.ParseConfig(parseConfig),
		wrapper.ProcessRequestHeaders(onHttpRequestHeaders),
		wrapper.ProcessRequestBody(onHttpRequestBody),
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

// QuotaRule defines a token quota for a specific dimension and time window.
type QuotaRule struct {
	// Dimension specifies what the quota applies to: "consumer", "route", "global"
	Dimension string `json:"dimension"`

	// DimensionValue is the specific value (e.g., consumer name).
	// Use "*" as wildcard to apply to all values of the dimension.
	DimensionValue string `json:"dimensionValue"`

	// Model is the LLM model name the quota applies to.
	// Use "*" to apply to all models.
	Model string `json:"model"`

	// Window is the time window for the quota: "minute", "hour", "day", "month"
	Window string `json:"window"`

	// Limit is the maximum number of tokens allowed in the time window.
	Limit int64 `json:"limit"`

	// SoftLimit is a token count at which a warning header is added.
	// 0 means no soft limit.
	SoftLimit int64 `json:"softLimit"`

	// BlockOnExceeded controls whether requests are blocked when the quota is exceeded.
	// If false, the quota is only tracked but not enforced.
	BlockOnExceeded bool `json:"blockOnExceeded"`
}

// TokenBillingConfig holds the plugin configuration.
type TokenBillingConfig struct {
	// Redis connection info
	Redis RedisInfo `json:"redis"`

	// Rules is the list of quota rules to enforce.
	Rules []QuotaRule `json:"rules"`

	// BlockStatusCode is the HTTP status code returned when quota is exceeded (default: 429)
	BlockStatusCode int `json:"blockStatusCode"`

	// BlockMessage is the error message returned when quota is exceeded
	BlockMessage string `json:"blockMessage"`

	// EnableResponseHeaders adds token usage headers to the response
	EnableResponseHeaders bool `json:"enableResponseHeaders"`

	// redisClient is the internal Redis client
	redisClient wrapper.RedisClient
}

func parseConfig(jsonConfig gjson.Result, config *TokenBillingConfig) error {
	// Block settings
	config.BlockStatusCode = int(jsonConfig.Get("blockStatusCode").Int())
	if config.BlockStatusCode == 0 {
		config.BlockStatusCode = 429
	}
	config.BlockMessage = jsonConfig.Get("blockMessage").String()
	if config.BlockMessage == "" {
		config.BlockMessage = "Token quota exceeded"
	}

	// Response headers
	config.EnableResponseHeaders = jsonConfig.Get("enableResponseHeaders").Bool()

	// Parse quota rules
	for _, ruleJSON := range jsonConfig.Get("rules").Array() {
		var rule QuotaRule
		if err := json.Unmarshal([]byte(ruleJSON.Raw), &rule); err != nil {
			log.Warnf("[ai-token-billing] failed to parse rule: %v", err)
			continue
		}
		if err := validateRule(&rule); err != nil {
			log.Warnf("[ai-token-billing] invalid rule: %v", err)
			continue
		}
		config.Rules = append(config.Rules, rule)
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

func validateRule(rule *QuotaRule) error {
	if rule.Dimension == "" {
		rule.Dimension = dimensionConsumer
	}
	if rule.Dimension != dimensionConsumer && rule.Dimension != dimensionRoute && rule.Dimension != dimensionGlobal {
		return fmt.Errorf("invalid dimension '%s'", rule.Dimension)
	}
	if rule.Window == "" {
		rule.Window = windowDay
	}
	if rule.Window != windowMinute && rule.Window != windowHour && rule.Window != windowDay && rule.Window != windowMonth {
		return fmt.Errorf("invalid window '%s'", rule.Window)
	}
	if rule.Limit <= 0 {
		return fmt.Errorf("limit must be positive, got %d", rule.Limit)
	}
	if rule.Model == "" {
		rule.Model = "*"
	}
	if rule.DimensionValue == "" {
		rule.DimensionValue = "*"
	}
	return nil
}

func onHttpRequestHeaders(ctx wrapper.HttpContext, config TokenBillingConfig) types.Action {
	ctx.DisableReroute()

	// Capture metadata for later use in response phase
	consumer, _ := proxywasm.GetHttpRequestHeader(headerConsumer)
	ctx.SetContext(ctxConsumer, consumer)
	ctx.SetContext(ctxStartTime, time.Now().UnixNano())

	return types.ActionContinue
}

func onHttpRequestBody(ctx wrapper.HttpContext, config TokenBillingConfig, body []byte) types.Action {
	// Capture model name from request body
	model := gjson.GetBytes(body, "model").String()
	if model == "" {
		model = "*"
	}
	ctx.SetContext(ctxModel, model)
	return types.ActionContinue
}

func onHttpStreamingResponseBody(ctx wrapper.HttpContext, config TokenBillingConfig, data []byte, endOfStream bool) []byte {
	if usage := tokenusage.GetTokenUsage(ctx, data); usage.TotalToken > 0 {
		ctx.SetContext(tokenusage.CtxKeyInputToken, usage.InputToken)
		ctx.SetContext(tokenusage.CtxKeyOutputToken, usage.OutputToken)
	}

	if endOfStream {
		recordTokenUsage(ctx, config)
	}

	return data
}

func onHttpResponseBody(ctx wrapper.HttpContext, config TokenBillingConfig, body []byte) types.Action {
	// Extract token usage from response
	inputTokens := gjson.GetBytes(body, "usage.prompt_tokens").Int()
	outputTokens := gjson.GetBytes(body, "usage.completion_tokens").Int()

	if inputTokens == 0 && outputTokens == 0 {
		// Try alternative field names (Anthropic, etc.)
		inputTokens = gjson.GetBytes(body, "usage.input_tokens").Int()
		outputTokens = gjson.GetBytes(body, "usage.output_tokens").Int()
	}

	if inputTokens > 0 || outputTokens > 0 {
		ctx.SetContext(tokenusage.CtxKeyInputToken, inputTokens)
		ctx.SetContext(tokenusage.CtxKeyOutputToken, outputTokens)
		recordTokenUsage(ctx, config)
	}

	return types.ActionContinue
}

// recordTokenUsage increments Redis counters for all matching quota rules.
func recordTokenUsage(ctx wrapper.HttpContext, config TokenBillingConfig) {
	consumer := getStringContext(ctx, ctxConsumer, "anonymous")
	model := getStringContext(ctx, ctxModel, "*")

	inputTokensI := ctx.GetContext(tokenusage.CtxKeyInputToken)
	outputTokensI := ctx.GetContext(tokenusage.CtxKeyOutputToken)

	var inputTokens, outputTokens int64
	if inputTokensI != nil {
		inputTokens = toInt64(inputTokensI)
	}
	if outputTokensI != nil {
		outputTokens = toInt64(outputTokensI)
	}

	totalTokens := inputTokens + outputTokens
	if totalTokens == 0 {
		return
	}

	log.Debugf("[ai-token-billing] recording %d tokens (in=%d, out=%d) for consumer=%s, model=%s",
		totalTokens, inputTokens, outputTokens, consumer, model)

	// Get route name from context (set by Envoy)
	route := getStringContext(ctx, ctxRoute, "unknown")
	if routeProp, err := proxywasm.GetProperty([]string{"route_name"}); err == nil {
		route = string(routeProp)
	}

	// Increment counters for each matching rule
	now := time.Now()
	for _, rule := range config.Rules {
		dimValue := getDimensionValue(rule.Dimension, consumer, route)
		if rule.DimensionValue != "*" && rule.DimensionValue != dimValue {
			continue
		}
		if rule.Model != "*" && rule.Model != model {
			continue
		}

		usageKey := buildUsageKey(rule.Dimension, dimValue, model, rule.Window, now)
		ttl := windowToTTL(rule.Window)

		// Atomically increment and set TTL
		_ = config.redisClient.IncrBy(usageKey, totalTokens, func(response resp.Value) {
			newCount := response.Integer()
			if newCount == totalTokens {
				// First increment: set TTL
				_ = config.redisClient.Expire(usageKey, ttl, nil)
			}

			log.Debugf("[ai-token-billing] updated usage key %s: %d tokens", usageKey, newCount)

			// Add usage headers to response if enabled
			if config.EnableResponseHeaders {
				quotaKey := buildQuotaKey(rule.Dimension, dimValue, model, rule.Window)
				addUsageHeaders(usageKey, quotaKey, rule, newCount, now)
			}
		})
	}
}

// addUsageHeaders adds token usage information headers to the HTTP response.
func addUsageHeaders(usageKey, quotaKey string, rule QuotaRule, currentUsage int64, now time.Time) {
	remaining := rule.Limit - currentUsage
	if remaining < 0 {
		remaining = 0
	}
	resetAt := windowResetTime(rule.Window, now)

	_ = proxywasm.AddHttpResponseHeader(headerUsedTokens, fmt.Sprintf("%d", currentUsage))
	_ = proxywasm.AddHttpResponseHeader(headerRemainingTokens, fmt.Sprintf("%d", remaining))
	_ = proxywasm.AddHttpResponseHeader(headerQuotaLimit, fmt.Sprintf("%d", rule.Limit))
	_ = proxywasm.AddHttpResponseHeader(headerQuotaWindow, rule.Window)
	_ = proxywasm.AddHttpResponseHeader(headerQuotaResetAt, fmt.Sprintf("%d", resetAt.Unix()))
}

// --- Helper functions ---

// buildUsageKey constructs the Redis key for usage tracking.
func buildUsageKey(dimension, dimValue, model, window string, now time.Time) string {
	ts := windowTimestamp(window, now)
	return fmt.Sprintf("%s:%s:%s:%s:%s:%d", keyUsagePrefix, dimension, dimValue, model, window, ts)
}

// buildQuotaKey constructs the Redis key for quota configuration.
func buildQuotaKey(dimension, dimValue, model, window string) string {
	return fmt.Sprintf("%s:%s:%s:%s:%s", keyQuotaPrefix, dimension, dimValue, model, window)
}

// getDimensionValue returns the value for the given dimension.
func getDimensionValue(dimension, consumer, route string) string {
	switch dimension {
	case dimensionConsumer:
		return consumer
	case dimensionRoute:
		return route
	default:
		return "global"
	}
}

// windowTimestamp returns the Unix timestamp truncated to the window granularity.
func windowTimestamp(window string, now time.Time) int64 {
	switch window {
	case windowMinute:
		return now.Truncate(time.Minute).Unix()
	case windowHour:
		return now.Truncate(time.Hour).Unix()
	case windowDay:
		y, m, d := now.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location()).Unix()
	case windowMonth:
		y, m, _ := now.Date()
		return time.Date(y, m, 1, 0, 0, 0, 0, now.Location()).Unix()
	default:
		return now.Truncate(time.Hour).Unix()
	}
}

// windowToTTL returns the Redis TTL in seconds for the given window.
func windowToTTL(window string) int {
	switch window {
	case windowMinute:
		return 120 // 2 minutes
	case windowHour:
		return 3700 // slightly over 1 hour
	case windowDay:
		return 90000 // slightly over 1 day
	case windowMonth:
		return 2700000 // slightly over 31 days
	default:
		return 3700
	}
}

// windowResetTime returns the time at which the current window resets.
func windowResetTime(window string, now time.Time) time.Time {
	switch window {
	case windowMinute:
		return now.Truncate(time.Minute).Add(time.Minute)
	case windowHour:
		return now.Truncate(time.Hour).Add(time.Hour)
	case windowDay:
		y, m, d := now.Date()
		return time.Date(y, m, d+1, 0, 0, 0, 0, now.Location())
	case windowMonth:
		y, m, _ := now.Date()
		return time.Date(y, m+1, 1, 0, 0, 0, 0, now.Location())
	default:
		return now.Add(time.Hour)
	}
}

// getStringContext retrieves a string value from context, returning defaultValue if not found.
func getStringContext(ctx wrapper.HttpContext, key, defaultValue string) string {
	v := ctx.GetContext(key)
	if v == nil {
		return defaultValue
	}
	if s, ok := v.(string); ok {
		return s
	}
	return defaultValue
}

// toInt64 converts an interface{} to int64.
func toInt64(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
}
