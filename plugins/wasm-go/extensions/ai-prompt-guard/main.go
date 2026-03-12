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

// Package main implements the ai-prompt-guard Wasm plugin.
// This plugin provides local, pattern-based detection of malicious prompts including:
//
//   - Prompt injection attacks (attempts to override system instructions)
//   - Jailbreak attempts (DAN, roleplay-based bypasses, etc.)
//   - Harmful content requests (violence, illegal activities, CSAM, etc.)
//   - Sensitive topic detection (configurable blocklist)
//
// Unlike ai-security-guard which relies on external content moderation APIs,
// this plugin works entirely locally without any external service dependency,
// making it suitable for air-gapped or latency-sensitive deployments.
//
// Detection operates in two modes:
//   - "block": reject the request with a 400 error (default)
//   - "log": allow the request but log the detection event
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
)

const (
	pluginName = "ai-prompt-guard"

	// Detection modes
	modeBlock = "block"
	modeLog   = "log"

	// Context keys
	ctxDetected     = "apg_detected"
	ctxDetectedRule = "apg_rule"
)

func main() {}

func init() {
	wrapper.SetCtx(
		pluginName,
		wrapper.ParseConfig(parseConfig),
		wrapper.ProcessRequestHeaders(onHttpRequestHeaders),
		wrapper.ProcessRequestBody(onHttpRequestBody),
	)
}

// DetectionRule is a single named detection rule.
type DetectionRule struct {
	// Name is the human-readable rule identifier
	Name string `json:"name"`
	// Pattern is the regular expression to match against prompt content
	Pattern string `json:"pattern"`
	// Description explains what the rule detects (used in log messages)
	Description string `json:"description"`
	// compiled is the pre-compiled regex (not serialized)
	compiled *regexp.Regexp
}

// PromptGuardConfig holds the plugin configuration.
type PromptGuardConfig struct {
	// Mode controls what happens when a malicious prompt is detected:
	//   "block" (default): reject the request with an error response
	//   "log": allow the request but log the detection
	Mode string `json:"mode"`

	// BlockStatusCode is the HTTP status code returned in block mode (default: 400)
	BlockStatusCode int `json:"blockStatusCode"`

	// BlockMessage is the error message returned in block mode
	BlockMessage string `json:"blockMessage"`

	// EnableBuiltinRules enables the built-in detection rules (default: true)
	EnableBuiltinRules bool `json:"enableBuiltinRules"`

	// EnabledCategories restricts which built-in rule categories are active.
	// If empty, all categories are enabled.
	// Valid values: "injection", "jailbreak", "harmful"
	EnabledCategories []string `json:"enabledCategories"`

	// CustomRules are user-defined detection rules appended to the active set.
	CustomRules []DetectionRule `json:"customRules"`

	// DenyList is a simple list of forbidden keywords/phrases (case-insensitive).
	// These are checked without regex, for performance.
	DenyList []string `json:"denyList"`

	// AllowList contains text patterns that should NOT be flagged even if they
	// match other rules. Useful for allowlisting specific legitimate use cases.
	AllowList []string `json:"allowList"`

	// CheckAllMessages controls whether all messages (including system/assistant) are
	// scanned, or only the latest user message (default: false = only user messages).
	CheckAllMessages bool `json:"checkAllMessages"`

	// rules is the compiled active rule set (not serialized)
	rules []DetectionRule
	// allowListCompiled is the pre-compiled allow-list regexes
	allowListCompiled []*regexp.Regexp
	// denyListLower is the lowercase version of deny-list items
	denyListLower []string
}

// Category prefix constants for built-in rules
const (
	categoryInjection = "injection"
	categoryJailbreak = "jailbreak"
	categoryHarmful   = "harmful"
)

// builtinRules contains production-ready detection patterns.
// These rules are based on common prompt injection and jailbreak research.
var builtinRules = []struct {
	Category string
	Rule     DetectionRule
}{
	// --- Prompt Injection ---
	{
		Category: categoryInjection,
		Rule: DetectionRule{
			Name:        "ignore_previous_instructions",
			Pattern:     `(?i)ignore\s+(?:all\s+)?(?:previous|prior|above|earlier)\s+instructions?`,
			Description: "Attempt to override system instructions",
		},
	},
	{
		Category: categoryInjection,
		Rule: DetectionRule{
			Name:        "disregard_system_prompt",
			Pattern:     `(?i)(?:disregard|forget|override|bypass|skip|ignore)\s+(?:(?:the|your)\s+)?(?:system\s+prompt|system\s+message|instructions?|guidelines?|training)`,
			Description: "Attempt to bypass system prompt",
		},
	},
	{
		Category: categoryInjection,
		Rule: DetectionRule{
			Name:        "new_instructions_injection",
			Pattern:     `(?i)(?:new\s+instructions?|your\s+new\s+(?:task|role|instructions?|guidelines?)|from\s+now\s+on\s+you\s+(?:are|will|must|should))`,
			Description: "Injection of new instructions",
		},
	},
	{
		Category: categoryInjection,
		Rule: DetectionRule{
			Name:        "prompt_leak_attempt",
			Pattern:     `(?i)(?:repeat|print|show|reveal|display|output|tell\s+me)\s+(?:the\s+)?(?:system\s+prompt|initial\s+instructions?|your\s+(?:full\s+)?prompt|everything\s+(?:before|above))`,
			Description: "Attempt to extract system prompt",
		},
	},
	{
		Category: categoryInjection,
		Rule: DetectionRule{
			Name:        "developer_mode_injection",
			Pattern:     `(?i)(?:developer\s+mode|dev\s+mode|god\s+mode|admin\s+mode|unrestricted\s+mode|jailbreak\s+mode)\s*(?:enabled?|activated?|on)`,
			Description: "Attempt to activate a fictional 'developer mode'",
		},
	},
	// --- Jailbreak ---
	{
		Category: categoryJailbreak,
		Rule: DetectionRule{
			Name:        "dan_jailbreak",
			Pattern:     `(?i)(?:do\s+anything\s+now|DAN\s+mode|you\s+are\s+DAN|act\s+as\s+DAN|pretend\s+(?:you\s+are|to\s+be)\s+(?:DAN|an?\s+AI\s+without\s+restrictions?))`,
			Description: "DAN (Do Anything Now) jailbreak attempt",
		},
	},
	{
		Category: categoryJailbreak,
		Rule: DetectionRule{
			Name:        "fictional_frame_bypass",
			Pattern:     `(?i)(?:pretend|imagine|roleplay|let's\s+say|hypothetically|in\s+a\s+story|for\s+a\s+novel|as\s+a\s+character)\s+(?:that\s+)?(?:you\s+(?:are|have\s+no|don't\s+have)\s+(?:rules?|restrictions?|guidelines?|ethics?|limits?|boundaries?)|there\s+are\s+no\s+(?:rules?|restrictions?|limits?))`,
			Description: "Fictional framing to bypass restrictions",
		},
	},
	{
		Category: categoryJailbreak,
		Rule: DetectionRule{
			Name:        "jailbreak_prefix",
			Pattern:     `(?i)\[(?:JAILBREAK(?:ED)?|STAN|DUDE|AIM|EVIL|ANTI-GPT|BETTERDANGPT|BasedGPT|UCAR|PersonGPT)\]`,
			Description: "Known jailbreak persona prefix",
		},
	},
	{
		Category: categoryJailbreak,
		Rule: DetectionRule{
			Name:        "token_manipulation",
			Pattern:     `(?i)(?:token\s+(?:smuggling|manipulation|injection)|base64\s+(?:encode|decode)\s+(?:to\s+)?(?:bypass|avoid|evade|circumvent))|(?:use\s+(?:leetspeak|pig\s+latin|rot13|caesar\s+cipher)\s+(?:to\s+)?(?:bypass|avoid|evade|circumvent))`,
			Description: "Token manipulation or encoding-based bypass",
		},
	},
	// --- Harmful content ---
	{
		Category: categoryHarmful,
		Rule: DetectionRule{
			Name:        "weapons_instructions",
			Pattern:     `(?i)(?:how\s+to\s+(?:make|build|create|manufacture|synthesize)\s+(?:a\s+)?(?:bomb|explosive|weapon|gun|bioweapon|chemical\s+weapon|nerve\s+agent|poison\s+gas))|(?:instructions?\s+(?:for|to)\s+(?:make|build|create)\s+(?:a\s+)?(?:bomb|explosive|weapon))`,
			Description: "Request for weapons manufacturing instructions",
		},
	},
	{
		Category: categoryHarmful,
		Rule: DetectionRule{
			Name:        "cyberattack_instructions",
			Pattern:     `(?i)(?:how\s+to\s+(?:hack|crack|exploit|attack|compromise|pwn)\s+(?:a\s+)?(?:website|server|database|network|system|computer))|(?:write\s+(?:a\s+)?(?:malware|ransomware|trojan|keylogger|rootkit|spyware|exploit))`,
			Description: "Request for cyberattack instructions",
		},
	},
	{
		Category: categoryHarmful,
		Rule: DetectionRule{
			Name:        "drug_synthesis",
			Pattern:     `(?i)(?:how\s+to\s+(?:make|synthesize|produce|manufacture)\s+(?:meth(?:amphetamine)?|cocaine|heroin|fentanyl|MDMA|ecstasy|LSD|crack))`,
			Description: "Request for illegal drug synthesis",
		},
	},
}

func parseConfig(jsonConfig gjson.Result, config *PromptGuardConfig) error {
	// Mode
	config.Mode = jsonConfig.Get("mode").String()
	if config.Mode == "" {
		config.Mode = modeBlock
	}
	if config.Mode != modeBlock && config.Mode != modeLog {
		return fmt.Errorf("invalid mode '%s': must be 'block' or 'log'", config.Mode)
	}

	// Block settings
	config.BlockStatusCode = int(jsonConfig.Get("blockStatusCode").Int())
	if config.BlockStatusCode == 0 {
		config.BlockStatusCode = 400
	}
	config.BlockMessage = jsonConfig.Get("blockMessage").String()
	if config.BlockMessage == "" {
		config.BlockMessage = "Request was rejected due to policy violations"
	}

	// Built-in rules
	enableBuiltinRules := jsonConfig.Get("enableBuiltinRules")
	if enableBuiltinRules.Exists() {
		config.EnableBuiltinRules = enableBuiltinRules.Bool()
	} else {
		config.EnableBuiltinRules = true
	}

	// Enabled categories
	for _, cat := range jsonConfig.Get("enabledCategories").Array() {
		config.EnabledCategories = append(config.EnabledCategories, cat.String())
	}
	enabledCatSet := make(map[string]bool)
	for _, cat := range config.EnabledCategories {
		enabledCatSet[cat] = true
	}

	// CheckAllMessages
	config.CheckAllMessages = jsonConfig.Get("checkAllMessages").Bool()

	// Compile built-in rules
	if config.EnableBuiltinRules {
		for _, entry := range builtinRules {
			if len(enabledCatSet) > 0 && !enabledCatSet[entry.Category] {
				continue
			}
			rule := entry.Rule
			compiled, err := regexp.Compile(rule.Pattern)
			if err != nil {
				log.Warnf("[ai-prompt-guard] failed to compile rule '%s': %v", rule.Name, err)
				continue
			}
			rule.compiled = compiled
			config.rules = append(config.rules, rule)
		}
	}

	// Parse custom rules
	for _, cr := range jsonConfig.Get("customRules").Array() {
		var rule DetectionRule
		if err := json.Unmarshal([]byte(cr.Raw), &rule); err != nil {
			log.Warnf("[ai-prompt-guard] failed to parse custom rule: %v", err)
			continue
		}
		if rule.Pattern == "" {
			continue
		}
		compiled, err := regexp.Compile(rule.Pattern)
		if err != nil {
			log.Warnf("[ai-prompt-guard] failed to compile custom rule '%s': %v", rule.Name, err)
			continue
		}
		rule.compiled = compiled
		config.rules = append(config.rules, rule)
		config.CustomRules = append(config.CustomRules, rule)
	}

	// Parse deny list
	for _, item := range jsonConfig.Get("denyList").Array() {
		config.DenyList = append(config.DenyList, item.String())
		config.denyListLower = append(config.denyListLower, strings.ToLower(item.String()))
	}

	// Parse allow list
	for _, item := range jsonConfig.Get("allowList").Array() {
		config.AllowList = append(config.AllowList, item.String())
		compiled, err := regexp.Compile(item.String())
		if err != nil {
			log.Warnf("[ai-prompt-guard] failed to compile allow-list pattern '%s': %v", item.String(), err)
			continue
		}
		config.allowListCompiled = append(config.allowListCompiled, compiled)
	}

	return nil
}

func onHttpRequestHeaders(ctx wrapper.HttpContext, config PromptGuardConfig) types.Action {
	ctx.DisableReroute()
	return types.ActionContinue
}

func onHttpRequestBody(ctx wrapper.HttpContext, config PromptGuardConfig, body []byte) types.Action {
	messagesResult := gjson.GetBytes(body, "messages")
	if !messagesResult.Exists() {
		return types.ActionContinue
	}

	messages := messagesResult.Array()
	for _, msg := range messages {
		role := msg.Get("role").String()

		// By default, only scan user messages
		if !config.CheckAllMessages && role != "user" {
			continue
		}

		content := msg.Get("content").String()
		if content == "" {
			continue
		}

		// Check allow list first
		if isAllowed(content, config.allowListCompiled) {
			continue
		}

		// Check deny list
		if matched, item := checkDenyList(content, config.denyListLower, config.DenyList); matched {
			log.Infof("[ai-prompt-guard] deny-list match '%s' in %s message", item, role)
			return handleDetection(ctx, config, "deny_list:"+item)
		}

		// Check regex rules
		if detected, ruleName := checkRules(content, config.rules); detected {
			log.Infof("[ai-prompt-guard] rule '%s' matched in %s message", ruleName, role)
			return handleDetection(ctx, config, ruleName)
		}
	}

	return types.ActionContinue
}

// --- Helper functions ---

// isAllowed returns true if the text matches any allow-list pattern.
func isAllowed(text string, allowList []*regexp.Regexp) bool {
	for _, re := range allowList {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// checkDenyList checks if the text contains any denied keyword/phrase.
func checkDenyList(text string, denyListLower, denyList []string) (bool, string) {
	textLower := strings.ToLower(text)
	for i, item := range denyListLower {
		if strings.Contains(textLower, item) {
			return true, denyList[i]
		}
	}
	return false, ""
}

// checkRules checks if the text matches any detection rule.
func checkRules(text string, rules []DetectionRule) (bool, string) {
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

// handleDetection either blocks the request or logs it depending on the mode.
func handleDetection(ctx wrapper.HttpContext, config PromptGuardConfig, ruleName string) types.Action {
	ctx.SetContext(ctxDetected, true)
	ctx.SetContext(ctxDetectedRule, ruleName)

	if config.Mode == modeBlock {
		return sendBlockedResponse(config, ruleName)
	}

	// Log mode: allow but emit a log entry
	log.Warnf("[ai-prompt-guard] DETECTED malicious prompt (rule=%s), allowing in log mode", ruleName)
	return types.ActionContinue
}

// sendBlockedResponse sends an HTTP error response and stops the request.
func sendBlockedResponse(config PromptGuardConfig, ruleName string) types.Action {
	body := fmt.Sprintf(`{"error":{"message":"%s","type":"policy_violation","code":"prompt_rejected","rule":"%s"}}`,
		escapeJSON(config.BlockMessage), ruleName)
	headers := [][2]string{
		{":status", fmt.Sprintf("%d", config.BlockStatusCode)},
		{"content-type", "application/json; charset=utf-8"},
		{"content-length", fmt.Sprintf("%d", len(body))},
	}
	if err := proxywasm.SendHttpResponse(uint32(config.BlockStatusCode), headers, []byte(body), -1); err != nil {
		log.Errorf("[ai-prompt-guard] failed to send block response: %v", err)
	}
	return types.ActionContinue
}

// escapeJSON escapes special characters in a string for JSON embedding.
func escapeJSON(s string) string {
	b, _ := json.Marshal(s)
	if len(b) >= 2 {
		return string(b[1 : len(b)-1])
	}
	return s
}
