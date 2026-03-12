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
	"regexp"
	"testing"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/test"
	"github.com/stretchr/testify/require"
)

var maskConfig = func() json.RawMessage {
	data, _ := json.Marshal(map[string]interface{}{
		"action":        "mask",
		"maskToken":     "[REDACTED]",
		"checkRequest":  true,
		"checkResponse": true,
		"enabledTypes":  []string{"phone", "email", "id_card", "credit_card"},
	})
	return data
}()

var blockConfig = func() json.RawMessage {
	data, _ := json.Marshal(map[string]interface{}{
		"action":          "block",
		"checkRequest":    true,
		"checkResponse":   false,
		"blockStatusCode": 400,
		"blockMessage":    "PII detected in request",
	})
	return data
}()

var customRulesConfig = func() json.RawMessage {
	data, _ := json.Marshal(map[string]interface{}{
		"action":       "mask",
		"checkRequest": true,
		"enabledTypes": []string{},
		"customRules": []map[string]interface{}{
			{
				"name":      "internal_id",
				"pattern":   `EMP-\d{6}`,
				"maskToken": "[EMPLOYEE_ID]",
			},
			{
				"name":    "project_code",
				"pattern": `PROJ-[A-Z]{3}-\d{4}`,
			},
		},
	})
	return data
}()

func TestParseConfig(t *testing.T) {
	test.RunGoTest(t, func(t *testing.T) {
		t.Run("mask config", func(t *testing.T) {
			host, status := test.NewTestHost(maskConfig)
			defer host.Reset()
			require.Equal(t, types.OnPluginStartStatusOK, status)
			config, err := host.GetMatchConfig()
			require.NoError(t, err)
			require.NotNil(t, config)

			cfg := config.(*PIIGuardConfig)
			require.Equal(t, actionMask, cfg.Action)
			require.Equal(t, "[REDACTED]", cfg.MaskToken)
			require.True(t, cfg.CheckRequest)
			require.True(t, cfg.CheckResponse)
			require.Len(t, cfg.EnabledTypes, 4)
			require.Greater(t, len(cfg.rules), 0)
		})

		t.Run("block config", func(t *testing.T) {
			host, status := test.NewTestHost(blockConfig)
			defer host.Reset()
			require.Equal(t, types.OnPluginStartStatusOK, status)
			config, err := host.GetMatchConfig()
			require.NoError(t, err)
			require.NotNil(t, config)

			cfg := config.(*PIIGuardConfig)
			require.Equal(t, actionBlock, cfg.Action)
			require.True(t, cfg.CheckRequest)
			require.False(t, cfg.CheckResponse)
			require.Equal(t, 400, cfg.BlockStatusCode)
			require.Equal(t, "PII detected in request", cfg.BlockMessage)
		})

		t.Run("custom rules config", func(t *testing.T) {
			host, status := test.NewTestHost(customRulesConfig)
			defer host.Reset()
			require.Equal(t, types.OnPluginStartStatusOK, status)
			config, err := host.GetMatchConfig()
			require.NoError(t, err)
			require.NotNil(t, config)

			cfg := config.(*PIIGuardConfig)
			require.Equal(t, actionMask, cfg.Action)
			// No builtin rules since enabledTypes is empty and custom rules have 2
			require.Len(t, cfg.rules, 2)
		})
	})
}

func TestDetectPII(t *testing.T) {
	// Compile test rules
	phoneRegex := regexp.MustCompile(`(?:(?:\+|00)?86)?1[3-9]\d{9}`)
	emailRegex := regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)
	idCardRegex := regexp.MustCompile(`[1-9]\d{5}(?:18|19|20)\d{2}(?:0[1-9]|1[0-2])(?:0[1-9]|[12]\d|3[01])\d{3}[\dXx]`)

	rules := []PIIRule{
		{Name: "phone", compiled: phoneRegex},
		{Name: "email", compiled: emailRegex},
		{Name: "id_card", compiled: idCardRegex},
	}

	t.Run("detects Chinese phone number", func(t *testing.T) {
		detected, ruleName := detectPII("Please call me at 13812345678", rules)
		require.True(t, detected)
		require.Equal(t, "phone", ruleName)
	})

	t.Run("detects email", func(t *testing.T) {
		detected, ruleName := detectPII("My email is user@example.com", rules)
		require.True(t, detected)
		require.Equal(t, "email", ruleName)
	})

	t.Run("detects Chinese ID card", func(t *testing.T) {
		detected, ruleName := detectPII("My ID is 110101199001011234", rules)
		require.True(t, detected)
		require.Equal(t, "id_card", ruleName)
	})

	t.Run("no PII in clean text", func(t *testing.T) {
		detected, _ := detectPII("What is the weather today?", rules)
		require.False(t, detected)
	})
}

func TestMaskPIIInText(t *testing.T) {
	phoneRegex := regexp.MustCompile(`(?:(?:\+|00)?86)?1[3-9]\d{9}`)
	emailRegex := regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)

	config := PIIGuardConfig{
		Action:    actionMask,
		MaskToken: "[REDACTED]",
		rules: []PIIRule{
			{Name: "phone", compiled: phoneRegex},
			{Name: "email", compiled: emailRegex},
		},
	}

	t.Run("masks phone number", func(t *testing.T) {
		result := maskPIIInText("Call me at 13812345678 please", config)
		require.Contains(t, result, "[REDACTED]")
		require.NotContains(t, result, "13812345678")
	})

	t.Run("masks email", func(t *testing.T) {
		result := maskPIIInText("Email me at test@example.com", config)
		require.Contains(t, result, "[REDACTED]")
		require.NotContains(t, result, "test@example.com")
	})

	t.Run("masks multiple PII in same text", func(t *testing.T) {
		result := maskPIIInText("Phone: 13812345678, Email: test@example.com", config)
		require.Contains(t, result, "[REDACTED]")
		require.NotContains(t, result, "13812345678")
		require.NotContains(t, result, "test@example.com")
	})

	t.Run("no change for clean text", func(t *testing.T) {
		text := "What is the weather today?"
		result := maskPIIInText(text, config)
		require.Equal(t, text, result)
	})

	t.Run("custom mask token per rule", func(t *testing.T) {
		configWithCustomToken := PIIGuardConfig{
			Action:    actionMask,
			MaskToken: "[REDACTED]",
			rules: []PIIRule{
				{Name: "phone", compiled: phoneRegex, MaskToken: "[PHONE]"},
			},
		}
		result := maskPIIInText("Call me at 13812345678", configWithCustomToken)
		require.Contains(t, result, "[PHONE]")
		require.NotContains(t, result, "[REDACTED]")
	})
}

func TestMaskPIIInMessages(t *testing.T) {
	phoneRegex := regexp.MustCompile(`(?:(?:\+|00)?86)?1[3-9]\d{9}`)

	config := PIIGuardConfig{
		Action:    actionMask,
		MaskToken: "[REDACTED]",
		rules: []PIIRule{
			{Name: "phone", compiled: phoneRegex},
		},
	}

	t.Run("masks PII in message content", func(t *testing.T) {
		body := []byte(`{"messages":[{"role":"user","content":"Call me at 13812345678"}]}`)
		result := maskPIIInMessages(body, config)
		require.NotContains(t, string(result), "13812345678")
		require.Contains(t, string(result), "[REDACTED]")
	})

	t.Run("preserves message structure", func(t *testing.T) {
		body := []byte(`{"model":"gpt-4","messages":[{"role":"system","content":"You are helpful"},{"role":"user","content":"Call 13812345678"}]}`)
		result := maskPIIInMessages(body, config)
		require.Contains(t, string(result), `"role":"system"`)
		require.Contains(t, string(result), `"role":"user"`)
		require.Contains(t, string(result), "You are helpful")
	})

	t.Run("no changes when no PII", func(t *testing.T) {
		body := []byte(`{"messages":[{"role":"user","content":"What is the weather?"}]}`)
		result := maskPIIInMessages(body, config)
		require.Equal(t, string(body), string(result))
	})
}

func TestOnHttpRequestHeaders(t *testing.T) {
	test.RunTest(t, func(t *testing.T) {
		t.Run("disable request body read when checkRequest is false", func(t *testing.T) {
			cfg, _ := json.Marshal(map[string]interface{}{
				"action":       "mask",
				"checkRequest": false,
			})
			host, status := test.NewTestHost(cfg)
			defer host.Reset()
			require.Equal(t, types.OnPluginStartStatusOK, status)

			action := host.CallOnHttpRequestHeaders([][2]string{
				{":path", "/v1/chat/completions"},
				{":method", "POST"},
			})
			require.Equal(t, types.ActionContinue, action)
		})
	})
}

func TestOnHttpRequestBody(t *testing.T) {
	test.RunTest(t, func(t *testing.T) {
		t.Run("masks PII in request body", func(t *testing.T) {
			host, status := test.NewTestHost(maskConfig)
			defer host.Reset()
			require.Equal(t, types.OnPluginStartStatusOK, status)

			host.CallOnHttpRequestHeaders([][2]string{
				{":path", "/v1/chat/completions"},
				{":method", "POST"},
			})

			body := `{"messages":[{"role":"user","content":"My phone is 13812345678"}]}`
			action := host.CallOnHttpRequestBody([]byte(body))
			require.Equal(t, types.ActionContinue, action)

			modifiedBody := host.GetRequestBody()
			require.NotContains(t, string(modifiedBody), "13812345678")
			require.Contains(t, string(modifiedBody), "[REDACTED]")
			host.CompleteHttp()
		})

		t.Run("passes clean request body unchanged", func(t *testing.T) {
			host, status := test.NewTestHost(maskConfig)
			defer host.Reset()
			require.Equal(t, types.OnPluginStartStatusOK, status)

			host.CallOnHttpRequestHeaders([][2]string{
				{":path", "/v1/chat/completions"},
				{":method", "POST"},
			})

			body := `{"messages":[{"role":"user","content":"What is the weather?"}]}`
			action := host.CallOnHttpRequestBody([]byte(body))
			require.Equal(t, types.ActionContinue, action)
			host.CompleteHttp()
		})
	})
}

func TestEscapeJSON(t *testing.T) {
	t.Run("escapes quotes", func(t *testing.T) {
		result := escapeJSON(`He said "hello"`)
		require.Contains(t, result, `\"`)
	})

	t.Run("handles normal text", func(t *testing.T) {
		result := escapeJSON("PII detected")
		require.Equal(t, "PII detected", result)
	})
}

func TestBuiltinRulesCompile(t *testing.T) {
	t.Run("all builtin rules compile without error", func(t *testing.T) {
		for _, rule := range builtinRules {
			_, err := regexp.Compile(rule.Pattern)
			require.NoError(t, err, "rule '%s' pattern should compile", rule.Name)
		}
	})
}
