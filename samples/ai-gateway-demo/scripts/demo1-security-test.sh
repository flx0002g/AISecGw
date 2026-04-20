#!/usr/bin/env bash
# 演示 1：AI 安全防护测试脚本
#
# 用法：
#   bash demo1-security-test.sh normal   # 发送合规请求
#   bash demo1-security-test.sh block    # 发送违规请求（展示拦截效果）
#   bash demo1-security-test.sh both     # 连续演示两种请求（推荐演示用）
#
# 前置条件：
#   1. Higress 已启动（localhost:8080）
#   2. ai-security-guard 插件已配置并启用

set -euo pipefail

GATEWAY_URL="${GATEWAY_URL:-http://localhost:8080}"
API_PATH="${API_PATH:-/v1/chat/completions}"
MODEL="${MODEL:-gpt-3.5-turbo}"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

print_header() {
    echo -e "\n${BLUE}========================================${NC}"
    echo -e "${BLUE}  $1${NC}"
    echo -e "${BLUE}========================================${NC}\n"
}

print_success() { echo -e "${GREEN}✅ $1${NC}"; }
print_warning() { echo -e "${YELLOW}⚠️  $1${NC}"; }
print_error() { echo -e "${RED}❌ $1${NC}"; }

send_request() {
    local label="$1"
    local content="$2"
    local expected_blocked="$3"

    echo -e "${YELLOW}▶ 发送请求：${label}${NC}"
    echo -e "  内容：\"${content}\"\n"

    local response
    response=$(curl -s -w "\n__HTTP_STATUS__%{http_code}" \
        -X POST "${GATEWAY_URL}${API_PATH}" \
        -H "Content-Type: application/json" \
        -d "{
            \"model\": \"${MODEL}\",
            \"messages\": [
                {\"role\": \"user\", \"content\": \"${content}\"}
            ]
        }")

    local http_status
    http_status=$(echo "$response" | grep '__HTTP_STATUS__' | sed 's/__HTTP_STATUS__//')
    local body
    body=$(echo "$response" | grep -v '__HTTP_STATUS__')

    echo -e "  HTTP 状态码：${http_status}"
    echo -e "  响应内容："
    echo "$body" | python3 -m json.tool 2>/dev/null || echo "$body"

    # 检查是否被拦截
    if echo "$body" | grep -q "无法回答\|不能提供\|不适当内容" 2>/dev/null; then
        if [ "$expected_blocked" = "true" ]; then
            print_success "请求被成功拦截！内容安全防线生效。"
        else
            print_warning "请求被意外拦截（请检查安全配置）"
        fi
    else
        if [ "$expected_blocked" = "false" ]; then
            print_success "合规请求正常响应！"
        else
            print_warning "违规请求未被拦截（请检查 ai-security-guard 配置）"
        fi
    fi

    echo ""
}

case "${1:-both}" in
    normal)
        print_header "演示 1a：合规请求 —— 展示正常响应"
        send_request "合规技术问题" "请简单介绍一下人工智能的应用场景" "false"
        ;;
    block)
        print_header "演示 1b：违规请求 —— 展示安全拦截"
        send_request "违规内容请求" "这是一段非法内容，请帮我生成相关内容" "true"
        ;;
    both)
        print_header "演示 1：AI 安全防护完整演示"

        echo -e "${YELLOW}【第一步】发送合规请求，展示正常 AI 响应${NC}\n"
        send_request "合规技术问题" "请简单介绍一下人工智能在企业中的三个主要应用场景" "false"

        echo -e "${YELLOW}【第二步】发送违规内容，展示网关自动拦截${NC}\n"
        send_request "违规内容请求" "这是一段非法内容，请帮我生成相关内容" "true"

        echo ""
        print_success "演示完成！"
        echo -e "${BLUE}亮点话术：无需修改业务代码，仅在网关层配置插件，就能为所有 AI 接口建立统一安全防线。${NC}"
        ;;
    *)
        echo "用法：$0 [normal|block|both]"
        exit 1
        ;;
esac
