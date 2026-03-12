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
	"time"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/test"
	"github.com/stretchr/testify/require"
)

var basicBillingConfig = func() json.RawMessage {
	data, _ := json.Marshal(map[string]interface{}{
		"redis": map[string]interface{}{
			"serviceName": "redis.static",
			"servicePort": 6379,
			"timeout":     1000,
		},
		"blockStatusCode": 429,
		"blockMessage":    "Token quota exceeded",
		"rules": []map[string]interface{}{
			{
				"dimension":       "consumer",
				"dimensionValue":  "*",
				"model":           "*",
				"window":          "day",
				"limit":           100000,
				"softLimit":       80000,
				"blockOnExceeded": true,
			},
		},
		"enableResponseHeaders": true,
	})
	return data
}()

var multiRuleConfig = func() json.RawMessage {
	data, _ := json.Marshal(map[string]interface{}{
		"redis": map[string]interface{}{
			"serviceName": "redis.static",
		},
		"rules": []map[string]interface{}{
			{
				"dimension":       "consumer",
				"dimensionValue":  "premium-user",
				"model":           "gpt-4",
				"window":          "month",
				"limit":           1000000,
				"blockOnExceeded": true,
			},
			{
				"dimension":       "consumer",
				"dimensionValue":  "*",
				"model":           "*",
				"window":          "hour",
				"limit":           10000,
				"blockOnExceeded": false,
			},
			{
				"dimension":       "global",
				"dimensionValue":  "global",
				"model":           "*",
				"window":          "minute",
				"limit":           5000,
				"blockOnExceeded": false,
			},
		},
	})
	return data
}()

func TestParseConfig(t *testing.T) {
	test.RunGoTest(t, func(t *testing.T) {
		t.Run("basic billing config", func(t *testing.T) {
			host, status := test.NewTestHost(basicBillingConfig)
			defer host.Reset()
			require.Equal(t, types.OnPluginStartStatusOK, status)
			config, err := host.GetMatchConfig()
			require.NoError(t, err)
			require.NotNil(t, config)

			cfg := config.(*TokenBillingConfig)
			require.Equal(t, 429, cfg.BlockStatusCode)
			require.Equal(t, "Token quota exceeded", cfg.BlockMessage)
			require.True(t, cfg.EnableResponseHeaders)
			require.Len(t, cfg.Rules, 1)

			rule := cfg.Rules[0]
			require.Equal(t, dimensionConsumer, rule.Dimension)
			require.Equal(t, "*", rule.DimensionValue)
			require.Equal(t, "*", rule.Model)
			require.Equal(t, windowDay, rule.Window)
			require.Equal(t, int64(100000), rule.Limit)
			require.Equal(t, int64(80000), rule.SoftLimit)
			require.True(t, rule.BlockOnExceeded)
		})

		t.Run("multi rule config", func(t *testing.T) {
			host, status := test.NewTestHost(multiRuleConfig)
			defer host.Reset()
			require.Equal(t, types.OnPluginStartStatusOK, status)
			config, err := host.GetMatchConfig()
			require.NoError(t, err)
			require.NotNil(t, config)

			cfg := config.(*TokenBillingConfig)
			require.Len(t, cfg.Rules, 3)

			// First rule: premium-user with gpt-4 monthly quota
			require.Equal(t, "premium-user", cfg.Rules[0].DimensionValue)
			require.Equal(t, "gpt-4", cfg.Rules[0].Model)
			require.Equal(t, windowMonth, cfg.Rules[0].Window)
			require.True(t, cfg.Rules[0].BlockOnExceeded)

			// Second rule: all consumers hourly
			require.Equal(t, "*", cfg.Rules[1].DimensionValue)
			require.Equal(t, windowHour, cfg.Rules[1].Window)
			require.False(t, cfg.Rules[1].BlockOnExceeded)

			// Third rule: global per-minute
			require.Equal(t, dimensionGlobal, cfg.Rules[2].Dimension)
			require.Equal(t, windowMinute, cfg.Rules[2].Window)
		})
	})
}

func TestValidateRule(t *testing.T) {
	t.Run("valid rule passes validation", func(t *testing.T) {
		rule := &QuotaRule{
			Dimension: dimensionConsumer,
			Window:    windowDay,
			Limit:     1000,
		}
		err := validateRule(rule)
		require.NoError(t, err)
		require.Equal(t, "*", rule.DimensionValue)
		require.Equal(t, "*", rule.Model)
	})

	t.Run("defaults are applied", func(t *testing.T) {
		rule := &QuotaRule{
			Limit: 1000,
		}
		err := validateRule(rule)
		require.NoError(t, err)
		require.Equal(t, dimensionConsumer, rule.Dimension)
		require.Equal(t, windowDay, rule.Window)
		require.Equal(t, "*", rule.DimensionValue)
		require.Equal(t, "*", rule.Model)
	})

	t.Run("invalid dimension fails", func(t *testing.T) {
		rule := &QuotaRule{
			Dimension: "invalid",
			Window:    windowDay,
			Limit:     1000,
		}
		err := validateRule(rule)
		require.Error(t, err)
	})

	t.Run("invalid window fails", func(t *testing.T) {
		rule := &QuotaRule{
			Dimension: dimensionConsumer,
			Window:    "weekly",
			Limit:     1000,
		}
		err := validateRule(rule)
		require.Error(t, err)
	})

	t.Run("zero limit fails", func(t *testing.T) {
		rule := &QuotaRule{
			Dimension: dimensionConsumer,
			Window:    windowDay,
			Limit:     0,
		}
		err := validateRule(rule)
		require.Error(t, err)
	})
}

func TestBuildUsageKey(t *testing.T) {
	now := time.Date(2024, 6, 15, 10, 30, 0, 0, time.UTC)

	t.Run("minute window key", func(t *testing.T) {
		key := buildUsageKey(dimensionConsumer, "user1", "gpt-4", windowMinute, now)
		require.Contains(t, key, "billing:usage")
		require.Contains(t, key, "consumer")
		require.Contains(t, key, "user1")
		require.Contains(t, key, "gpt-4")
		require.Contains(t, key, "minute")
	})

	t.Run("day window key", func(t *testing.T) {
		key := buildUsageKey(dimensionGlobal, "global", "*", windowDay, now)
		require.Contains(t, key, "billing:usage")
		require.Contains(t, key, "global")
		require.Contains(t, key, "day")
	})

	t.Run("same window produces same key", func(t *testing.T) {
		t1 := time.Date(2024, 6, 15, 10, 0, 0, 0, time.UTC)
		t2 := time.Date(2024, 6, 15, 10, 45, 0, 0, time.UTC)
		key1 := buildUsageKey(dimensionConsumer, "user1", "*", windowHour, t1)
		key2 := buildUsageKey(dimensionConsumer, "user1", "*", windowHour, t2)
		require.Equal(t, key1, key2, "same hour should produce same key")
	})

	t.Run("different windows produce different keys", func(t *testing.T) {
		keyHour := buildUsageKey(dimensionConsumer, "user1", "*", windowHour, now)
		keyDay := buildUsageKey(dimensionConsumer, "user1", "*", windowDay, now)
		require.NotEqual(t, keyHour, keyDay)
	})
}

func TestWindowTimestamp(t *testing.T) {
	now := time.Date(2024, 6, 15, 10, 30, 45, 0, time.UTC)

	t.Run("minute truncation", func(t *testing.T) {
		ts := windowTimestamp(windowMinute, now)
		expected := time.Date(2024, 6, 15, 10, 30, 0, 0, time.UTC).Unix()
		require.Equal(t, expected, ts)
	})

	t.Run("hour truncation", func(t *testing.T) {
		ts := windowTimestamp(windowHour, now)
		expected := time.Date(2024, 6, 15, 10, 0, 0, 0, time.UTC).Unix()
		require.Equal(t, expected, ts)
	})

	t.Run("day truncation", func(t *testing.T) {
		ts := windowTimestamp(windowDay, now)
		expected := time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC).Unix()
		require.Equal(t, expected, ts)
	})

	t.Run("month truncation", func(t *testing.T) {
		ts := windowTimestamp(windowMonth, now)
		expected := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC).Unix()
		require.Equal(t, expected, ts)
	})
}

func TestWindowToTTL(t *testing.T) {
	t.Run("minute TTL", func(t *testing.T) {
		ttl := windowToTTL(windowMinute)
		require.Equal(t, 120, ttl)
	})

	t.Run("hour TTL", func(t *testing.T) {
		ttl := windowToTTL(windowHour)
		require.Equal(t, 3700, ttl)
	})

	t.Run("day TTL", func(t *testing.T) {
		ttl := windowToTTL(windowDay)
		require.Equal(t, 90000, ttl)
	})

	t.Run("month TTL", func(t *testing.T) {
		ttl := windowToTTL(windowMonth)
		require.Equal(t, 2700000, ttl)
	})
}

func TestGetDimensionValue(t *testing.T) {
	t.Run("consumer dimension", func(t *testing.T) {
		result := getDimensionValue(dimensionConsumer, "alice", "my-route")
		require.Equal(t, "alice", result)
	})

	t.Run("route dimension", func(t *testing.T) {
		result := getDimensionValue(dimensionRoute, "alice", "my-route")
		require.Equal(t, "my-route", result)
	})

	t.Run("global dimension", func(t *testing.T) {
		result := getDimensionValue(dimensionGlobal, "alice", "my-route")
		require.Equal(t, "global", result)
	})
}

func TestToInt64(t *testing.T) {
	require.Equal(t, int64(42), toInt64(int64(42)))
	require.Equal(t, int64(42), toInt64(int(42)))
	require.Equal(t, int64(42), toInt64(float64(42.0)))
	require.Equal(t, int64(0), toInt64("not a number"))
}

func TestOnHttpRequestHeaders(t *testing.T) {
	test.RunTest(t, func(t *testing.T) {
		t.Run("headers are processed", func(t *testing.T) {
			host, status := test.NewTestHost(basicBillingConfig)
			defer host.Reset()
			require.Equal(t, types.OnPluginStartStatusOK, status)

			action := host.CallOnHttpRequestHeaders([][2]string{
				{":path", "/v1/chat/completions"},
				{":method", "POST"},
				{"x-mse-consumer", "test-consumer"},
			})
			require.Equal(t, types.ActionContinue, action)
		})
	})
}

func TestWindowResetTime(t *testing.T) {
	now := time.Date(2024, 6, 15, 10, 30, 45, 0, time.UTC)

	t.Run("minute reset time", func(t *testing.T) {
		reset := windowResetTime(windowMinute, now)
		expected := time.Date(2024, 6, 15, 10, 31, 0, 0, time.UTC)
		require.Equal(t, expected, reset)
	})

	t.Run("hour reset time", func(t *testing.T) {
		reset := windowResetTime(windowHour, now)
		expected := time.Date(2024, 6, 15, 11, 0, 0, 0, time.UTC)
		require.Equal(t, expected, reset)
	})
}
