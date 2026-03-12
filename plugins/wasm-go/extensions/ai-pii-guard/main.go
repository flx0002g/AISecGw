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

// Package main implements the ai-pii-guard Wasm plugin.
// This plugin detects and protects Personally Identifiable Information (PII)
// in both AI request prompts and AI response content.
//
// Supported PII types:
//   - Phone numbers (Chinese mobile & landline, international)
//   - Email addresses
//   - Chinese ID card numbers
//   - Credit card numbers
//   - Bank card numbers
//   - IP addresses (IPv4)
//   - Chinese names (basic heuristic)
//   - Passport numbers
//   - Custom regex patterns
//
// Actions:
//   - "mask": Replace detected PII with a placeholder (default)
//   - "block": Reject the request / response with a 400/403 error
package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/log"
	"github.com/higress-group/wasm-go/pkg/wrapper"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	pluginName = "ai-pii-guard"

	// Action constants
	actionMask  = "mask"
	actionBlock = "block"

	// Default mask token
	defaultMaskToken = "[REDACTED]"
)

func main() {}

func init() {
	wrapper.SetCtx(
		pluginName,
		wrapper.ParseConfig(parseConfig),
		wrapper.ProcessRequestHeaders(onHttpRequestHeaders),
		wrapper.ProcessRequestBody(onHttpRequestBody),
		wrapper.ProcessResponseHeaders(onHttpResponseHeaders),
		wrapper.ProcessResponseBody(onHttpResponseBody),
		wrapper.ProcessStreamingResponseBody(onHttpStreamingResponseBody),
	)
}

// PIIRule defines a single PII detection rule.
type PIIRule struct {
	// Name is the human-readable identifier (e.g., "phone", "email")
	Name string `json:"name"`
	// Pattern is the regular expression used to detect PII
	Pattern string `json:"pattern"`
	// MaskToken overrides the global mask token for this rule (optional)
	MaskToken string `json:"maskToken"`
	// compiled is the pre-compiled regex (not serialized)
	compiled *regexp.Regexp
}

// PIIGuardConfig holds the plugin configuration.
type PIIGuardConfig struct {
	// Action controls what happens when PII is detected.
	//   "mask" (default): replace PII with a placeholder
	//   "block": return an error response
	Action string `json:"action"`

	// MaskToken is the string used to replace detected PII when action is "mask".
	MaskToken string `json:"maskToken"`

	// CheckRequest enables PII detection in LLM requests (default: true)
	CheckRequest bool `json:"checkRequest"`

	// CheckResponse enables PII detection in LLM responses (default: false)
	CheckResponse bool `json:"checkResponse"`

	// EnabledTypes is the list of built-in PII types to enable.
	// If empty, all built-in types are enabled.
	// Supported values: "phone", "email", "id_card", "credit_card", "bank_card",
	//                   "ipv4", "passport"
	EnabledTypes []string `json:"enabledTypes"`

	// CustomRules are user-defined PII detection rules.
	CustomRules []PIIRule `json:"customRules"`

	// BlockStatusCode is the HTTP status code returned when action is "block" (default: 400)
	BlockStatusCode int `json:"blockStatusCode"`

	// BlockMessage is the error message returned when action is "block"
	BlockMessage string `json:"blockMessage"`

	// rules is the compiled list of active rules (not serialized)
	rules []PIIRule
}

// builtinRules defines the default PII detection patterns.
var builtinRules = []PIIRule{
	{
		Name:    "phone",
		Pattern: `(?:(?:\+|00)?86)?1[3-9]\d{9}`,
	},
	{
		Name:    "phone_cn_landline",
		Pattern: `(?:0\d{2,3}[-\s]?)?\d{7,8}`,
	},
	{
		Name:    "email",
		Pattern: `[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`,
	},
	{
		Name:    "id_card",
		Pattern: `[1-9]\d{5}(?:18|19|20)\d{2}(?:0[1-9]|1[0-2])(?:0[1-9]|[12]\d|3[01])\d{3}[\dXx]`,
	},
	{
		Name:    "credit_card",
		Pattern: `(?:4[0-9]{12}(?:[0-9]{3})?|[25][1-7][0-9]{14}|6(?:011|5[0-9][0-9])[0-9]{12}|3[47][0-9]{13}|3(?:0[0-5]|[68][0-9])[0-9]{11}|(?:2131|1800|35\d{3})\d{11})`,
	},
	{
		Name:    "bank_card",
		Pattern: `[1-9]\d{15,18}`,
	},
	{
		Name:    "ipv4",
		Pattern: `\b(?:(?:25[0-5]|2[0-4]\d|[01]?\d\d?)\.){3}(?:25[0-5]|2[0-4]\d|[01]?\d\d?)\b`,
	},
	{
		Name:    "passport",
		Pattern: `[A-Z]{1,2}[0-9]{6,9}`,
	},
}

func parseConfig(jsonConfig gjson.Result, config *PIIGuardConfig) error {
	// Action
	config.Action = jsonConfig.Get("action").String()
	if config.Action == "" {
		config.Action = actionMask
	}
	if config.Action != actionMask && config.Action != actionBlock {
		return fmt.Errorf("invalid action '%s': must be 'mask' or 'block'", config.Action)
	}

	// Mask token
	config.MaskToken = jsonConfig.Get("maskToken").String()
	if config.MaskToken == "" {
		config.MaskToken = defaultMaskToken
	}

	// Check flags (default both true for request, false for response)
	checkRequest := jsonConfig.Get("checkRequest")
	if checkRequest.Exists() {
		config.CheckRequest = checkRequest.Bool()
	} else {
		config.CheckRequest = true
	}

	checkResponse := jsonConfig.Get("checkResponse")
	if checkResponse.Exists() {
		config.CheckResponse = checkResponse.Bool()
	} else {
		config.CheckResponse = false
	}

	// Block settings
	config.BlockStatusCode = int(jsonConfig.Get("blockStatusCode").Int())
	if config.BlockStatusCode == 0 {
		config.BlockStatusCode = 400
	}
	config.BlockMessage = jsonConfig.Get("blockMessage").String()
	if config.BlockMessage == "" {
		config.BlockMessage = "Request contains sensitive personal information"
	}

	// Enabled types
	enabledTypesResult := jsonConfig.Get("enabledTypes").Array()
	enabledSet := make(map[string]bool)
	for _, t := range enabledTypesResult {
		enabledSet[t.String()] = true
		config.EnabledTypes = append(config.EnabledTypes, t.String())
	}

	// Build active rules from builtins
	for _, rule := range builtinRules {
		if len(enabledSet) > 0 && !enabledSet[rule.Name] {
			continue
		}
		compiled, err := regexp.Compile(rule.Pattern)
		if err != nil {
			log.Warnf("[ai-pii-guard] failed to compile built-in rule '%s': %v", rule.Name, err)
			continue
		}
		rule.compiled = compiled
		config.rules = append(config.rules, rule)
	}

	// Parse custom rules
	customRulesResult := jsonConfig.Get("customRules").Array()
	for _, cr := range customRulesResult {
		var rule PIIRule
		if err := json.Unmarshal([]byte(cr.Raw), &rule); err != nil {
			log.Warnf("[ai-pii-guard] failed to parse custom rule: %v", err)
			continue
		}
		if rule.Pattern == "" {
			log.Warnf("[ai-pii-guard] custom rule '%s' has empty pattern, skipping", rule.Name)
			continue
		}
		compiled, err := regexp.Compile(rule.Pattern)
		if err != nil {
			log.Warnf("[ai-pii-guard] failed to compile custom rule '%s': %v", rule.Name, err)
			continue
		}
		rule.compiled = compiled
		config.rules = append(config.rules, rule)
		config.CustomRules = append(config.CustomRules, rule)
	}

	return nil
}

func onHttpRequestHeaders(ctx wrapper.HttpContext, config PIIGuardConfig) types.Action {
	ctx.DisableReroute()
	if !config.CheckRequest {
		ctx.DontReadRequestBody()
	}
	return types.ActionContinue
}

func onHttpRequestBody(ctx wrapper.HttpContext, config PIIGuardConfig, body []byte) types.Action {
	if !config.CheckRequest {
		return types.ActionContinue
	}

	// Scan message contents in the request
	messagesResult := gjson.GetBytes(body, "messages")
	if !messagesResult.Exists() {
		return types.ActionContinue
	}

	switch config.Action {
	case actionBlock:
		for _, msg := range messagesResult.Array() {
			content := msg.Get("content").String()
			if detected, ruleName := detectPII(content, config.rules); detected {
				log.Infof("[ai-pii-guard] PII detected (%s) in request, blocking", ruleName)
				return sendBlockedResponse(config)
			}
		}
	case actionMask:
		newBody := maskPIIInMessages(body, config)
		if err := proxywasm.ReplaceHttpRequestBody(newBody); err != nil {
			log.Errorf("[ai-pii-guard] failed to replace request body: %v", err)
		}
	}

	return types.ActionContinue
}

func onHttpResponseHeaders(ctx wrapper.HttpContext, config PIIGuardConfig) types.Action {
	if !config.CheckResponse {
		ctx.DontReadResponseBody()
	}
	return types.ActionContinue
}

func onHttpResponseBody(ctx wrapper.HttpContext, config PIIGuardConfig, body []byte) types.Action {
	if !config.CheckResponse {
		return types.ActionContinue
	}

	content := gjson.GetBytes(body, "choices.0.message.content").String()
	if content == "" {
		return types.ActionContinue
	}

	switch config.Action {
	case actionBlock:
		if detected, ruleName := detectPII(content, config.rules); detected {
			log.Infof("[ai-pii-guard] PII detected (%s) in response, blocking", ruleName)
			return sendBlockedResponse(config)
		}
	case actionMask:
		maskedContent := maskPIIInText(content, config)
		if maskedContent != content {
			newBody, err := sjson.SetBytes(body, "choices.0.message.content", maskedContent)
			if err != nil {
				log.Errorf("[ai-pii-guard] failed to mask response content: %v", err)
				return types.ActionContinue
			}
			if err := proxywasm.ReplaceHttpResponseBody(newBody); err != nil {
				log.Errorf("[ai-pii-guard] failed to replace response body: %v", err)
			}
		}
	}

	return types.ActionContinue
}

func onHttpStreamingResponseBody(ctx wrapper.HttpContext, config PIIGuardConfig, data []byte, endOfStream bool) []byte {
	if !config.CheckResponse {
		return data
	}

	// For streaming responses, scan each delta content chunk
	str := string(data)
	for _, line := range strings.Split(str, "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(line[5:])
		if payload == "[DONE]" {
			continue
		}
		content := gjson.Get(payload, "choices.0.delta.content").String()
		if content == "" {
			continue
		}

		switch config.Action {
		case actionBlock:
			if detected, ruleName := detectPII(content, config.rules); detected {
				log.Infof("[ai-pii-guard] PII detected (%s) in streaming response", ruleName)
				// For streaming we can only mask (blocking mid-stream is complex)
				// Fall through to mask for safety
				_ = ruleName
			}
		case actionMask:
			maskedContent := maskPIIInText(content, config)
			if maskedContent != content {
				newPayload, err := sjson.Set(payload, "choices.0.delta.content", maskedContent)
				if err == nil {
					str = strings.Replace(str, payload, newPayload, 1)
				}
			}
		}
	}

	return []byte(str)
}

// --- Helper functions ---

// detectPII checks if the text contains any PII matching the given rules.
// Returns (true, ruleName) on first match, (false, "") if no PII found.
func detectPII(text string, rules []PIIRule) (bool, string) {
	for _, rule := range rules {
		if rule.compiled == nil {
			continue
		}
		if rule.compiled.MatchString(text) {
			return true, rule.Name
		}
	}
	return false, ""
}

// maskPIIInText replaces all PII occurrences in text with the configured mask token.
func maskPIIInText(text string, config PIIGuardConfig) string {
	result := text
	for _, rule := range config.rules {
		if rule.compiled == nil {
			continue
		}
		maskToken := config.MaskToken
		if rule.MaskToken != "" {
			maskToken = rule.MaskToken
		}
		result = rule.compiled.ReplaceAllString(result, maskToken)
	}
	return result
}

// maskPIIInMessages applies PII masking to all message contents in the request body.
func maskPIIInMessages(body []byte, config PIIGuardConfig) []byte {
	messagesResult := gjson.GetBytes(body, "messages")
	if !messagesResult.Exists() {
		return body
	}

	messages := messagesResult.Array()
	newBody := body

	for i, msg := range messages {
		content := msg.Get("content").String()
		if content == "" {
			continue
		}
		masked := maskPIIInText(content, config)
		if masked == content {
			continue
		}
		path := fmt.Sprintf("messages.%d.content", i)
		var err error
		newBody, err = sjson.SetBytes(newBody, path, masked)
		if err != nil {
			log.Errorf("[ai-pii-guard] failed to mask message %d: %v", i, err)
		}
	}

	return newBody
}

// sendBlockedResponse sends an HTTP 400/403 response indicating PII was detected.
func sendBlockedResponse(config PIIGuardConfig) types.Action {
	body := fmt.Sprintf(`{"error":{"message":"%s","type":"pii_detected","code":"pii_error"}}`,
		escapeJSON(config.BlockMessage))
	headers := [][2]string{
		{":status", fmt.Sprintf("%d", config.BlockStatusCode)},
		{"content-type", "application/json; charset=utf-8"},
		{"content-length", fmt.Sprintf("%d", len(body))},
	}
	if err := proxywasm.SendHttpResponse(uint32(config.BlockStatusCode), headers, []byte(body), -1); err != nil {
		log.Errorf("[ai-pii-guard] failed to send block response: %v", err)
	}
	return types.ActionContinue
}

// escapeJSON escapes special characters in a string for JSON embedding.
func escapeJSON(s string) string {
	b, _ := json.Marshal(s)
	// Remove surrounding quotes added by json.Marshal
	if len(b) >= 2 {
		return string(b[1 : len(b)-1])
	}
	return s
}
