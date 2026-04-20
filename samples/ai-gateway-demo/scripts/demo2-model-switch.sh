#!/usr/bin/env bash
# 演示 2：多模型统一接入与无感切换脚本
#
# 用法：
#   bash demo2-model-switch.sh qwen       # 发送请求至通义千问
#   bash demo2-model-switch.sh deepseek   # 发送请求至 DeepSeek（网关侧切换）
#   bash demo2-model-switch.sh compare    # 对比演示（推荐演示用）
#
# 演示核心：业务代码（请求体）完全相同，只改网关配置，模型无感切换
#
# 前置条件：
#   1. Higress 已启动（localhost:8080）
#   2. ai-proxy 插件已配置对应模型提供商

set -euo pipefail

GATEWAY_URL="${GATEWAY_URL:-http://localhost:8080}"
API_PATH="${API_PATH:-/v1/chat/completions}"

# 颜色输出
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m'

# 演示用：业务代码发送的请求体（模型名是"虚拟的" gpt-3.5-turbo，
# 实际路由到哪个模型由网关配置决定）
DEMO_REQUEST_BODY='{
  "model": "gpt-3.5-turbo",
  "messages": [
    {
      "role": "system",
      "content": "你是一个专业的 AI 助手，请简洁回答问题。"
    },
    {
      "role": "user",
      "content": "请用一句话介绍你自己，包括你是哪个模型。"
    }
  ],
  "max_tokens": 100
}'

print_header() {
    echo -e "\n${BLUE}========================================${NC}"
    echo -e "${BLUE}  $1${NC}"
    echo -e "${BLUE}========================================${NC}\n"
}

send_and_show() {
    local label="$1"

    echo -e "${YELLOW}▶ 发送请求（注意：业务代码完全相同）${NC}"
    echo -e "${CYAN}请求体：${NC}"
    echo "$DEMO_REQUEST_BODY" | python3 -m json.tool 2>/dev/null || echo "$DEMO_REQUEST_BODY"
    echo ""

    local start_time
    start_time=$(date +%s%N)

    local response
    response=$(curl -s -w "\n__HTTP_STATUS__%{http_code}" \
        -X POST "${GATEWAY_URL}${API_PATH}" \
        -H "Content-Type: application/json" \
        -H "Accept: application/json" \
        -d "$DEMO_REQUEST_BODY")

    local end_time
    end_time=$(date +%s%N)
    local latency_ms=$(( (end_time - start_time) / 1000000 ))

    local http_status
    http_status=$(echo "$response" | grep '__HTTP_STATUS__' | sed 's/__HTTP_STATUS__//')
    local body
    body=$(echo "$response" | grep -v '__HTTP_STATUS__')

    echo -e "${CYAN}响应 [${label}] HTTP ${http_status} (${latency_ms}ms)：${NC}"
    echo "$body" | python3 -m json.tool 2>/dev/null || echo "$body"

    # 提取 Token 使用信息
    local usage
    usage=$(echo "$body" | python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    usage = data.get('usage', {})
    if usage:
        print(f'  prompt_tokens={usage.get(\"prompt_tokens\",\"N/A\")}')
        print(f'  completion_tokens={usage.get(\"completion_tokens\",\"N/A\")}')
        print(f'  total_tokens={usage.get(\"total_tokens\",\"N/A\")}')
except:
    pass
" 2>/dev/null)

    if [ -n "$usage" ]; then
        echo -e "\n${GREEN}Token 使用情况：${NC}"
        echo "$usage"
    fi
    echo ""
}

case "${1:-compare}" in
    qwen)
        print_header "演示 2a：请求路由至通义千问"
        echo -e "${YELLOW}当前网关配置：ai-proxy → 通义千问 (qwen-turbo)${NC}\n"
        echo -e "配置参考：configs/demo2-multi-model-qwen.yaml\n"
        send_and_show "通义千问"
        ;;
    deepseek)
        print_header "演示 2b：切换到 DeepSeek（业务代码不变！）"
        echo -e "${YELLOW}当前网关配置：ai-proxy → DeepSeek (deepseek-chat)${NC}\n"
        echo -e "配置参考：configs/demo2-multi-model-deepseek.yaml\n"
        send_and_show "DeepSeek"
        ;;
    compare)
        print_header "演示 2：多模型无感切换完整演示"

        echo -e "${YELLOW}【第一步】后端路由至通义千问${NC}"
        echo -e "  → 在控制台应用 configs/demo2-multi-model-qwen.yaml"
        echo -e "  → 等待约 1 秒配置生效\n"
        read -r -p "  请确认已应用通义千问配置，按 Enter 继续..." 2>/dev/null || true
        send_and_show "通义千问"

        echo -e "${YELLOW}【第二步】切换到 DeepSeek（仅改网关配置，业务代码不变）${NC}"
        echo -e "  → 在控制台应用 configs/demo2-multi-model-deepseek.yaml"
        echo -e "  → 等待约 1 秒配置生效\n"
        read -r -p "  请确认已切换到 DeepSeek 配置，按 Enter 继续..." 2>/dev/null || true
        send_and_show "DeepSeek"

        echo -e "${GREEN}✅ 演示完成！${NC}"
        echo -e "${BLUE}亮点：两次请求的代码完全相同，只改了网关配置，模型无感切换。${NC}"
        echo -e "${BLUE}支持模型：通义千问、DeepSeek、GPT-4、Claude、Gemini 等全部主流厂商。${NC}"
        ;;
    *)
        echo "用法：$0 [qwen|deepseek|compare]"
        exit 1
        ;;
esac
