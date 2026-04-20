#!/usr/bin/env bash
# 演示 4：MCP Server 托管与 AI Agent 调用脚本
#
# 用法：
#   bash demo4-mcp-agent.sh list     # 列出可用的 MCP 工具
#   bash demo4-mcp-agent.sh call     # 演示 AI Agent 调用工具
#   bash demo4-mcp-agent.sh demo     # 完整演示（推荐）
#
# 演示核心：
#   - AI Agent 通过标准 MCP 协议调用工具
#   - 网关层透明提供：认证/速率限制/审计日志/可观测
#   - 动态更新无损：热更新时不断开 SSE 连接
#
# 前置条件：
#   1. Higress 已启动（localhost:8080）
#   2. MCP Server 路由和插件已配置（configs/demo4-mcp-server.yaml）
#   OR 访问在线演示：https://mcp.higress.ai/

set -euo pipefail

GATEWAY_URL="${GATEWAY_URL:-http://localhost:8080}"
MCP_ENDPOINT="${MCP_ENDPOINT:-/mcp}"
# 演示用 API Key
DEMO_API_KEY="${DEMO_API_KEY:-agent-api-key-001}"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m'

print_header() {
    echo -e "\n${BLUE}========================================${NC}"
    echo -e "${BLUE}  $1${NC}"
    echo -e "${BLUE}========================================${NC}\n"
}

# 列出可用的 MCP 工具
list_tools() {
    print_header "MCP Server 可用工具列表"

    echo -e "${CYAN}发送 MCP list_tools 请求...${NC}\n"

    local response
    response=$(curl -s -w "\n__HTTP_STATUS__%{http_code}" \
        -X POST "${GATEWAY_URL}${MCP_ENDPOINT}" \
        -H "Content-Type: application/json" \
        -H "x-api-key: ${DEMO_API_KEY}" \
        -d '{
            "jsonrpc": "2.0",
            "id": 1,
            "method": "tools/list",
            "params": {}
        }' 2>/dev/null || echo '{"result":{"tools":[]}}
__HTTP_STATUS__000')

    local http_status
    http_status=$(echo "$response" | grep '__HTTP_STATUS__' | sed 's/__HTTP_STATUS__//')
    local body
    body=$(echo "$response" | grep -v '__HTTP_STATUS__')

    echo -e "HTTP 状态：${http_status}"
    echo -e "响应内容："
    echo "$body" | python3 -m json.tool 2>/dev/null || echo "$body"

    if [ "$http_status" = "000" ] || [ "$http_status" = "" ]; then
        echo -e "\n${YELLOW}（本地 MCP Server 未运行，以下展示在线平台演示）${NC}"
        show_online_demo
    fi
}

# 调用 MCP 工具
call_tool() {
    print_header "AI Agent 调用 MCP 工具"

    echo -e "${CYAN}演示场景：AI Agent 通过网关调用「搜索工具」${NC}\n"
    echo -e "${YELLOW}请求（携带 API Key 认证）：${NC}"

    local request_body='{
    "jsonrpc": "2.0",
    "id": 2,
    "method": "tools/call",
    "params": {
        "name": "web_search",
        "arguments": {
            "query": "Higress AI 网关最新特性",
            "max_results": 3
        }
    }
}'
    echo "$request_body" | python3 -m json.tool 2>/dev/null || echo "$request_body"
    echo ""

    local response
    response=$(curl -s -w "\n__HTTP_STATUS__%{http_code}" \
        -X POST "${GATEWAY_URL}${MCP_ENDPOINT}" \
        -H "Content-Type: application/json" \
        -H "x-api-key: ${DEMO_API_KEY}" \
        -d "$request_body" 2>/dev/null || echo '{}
__HTTP_STATUS__000')

    local http_status
    http_status=$(echo "$response" | grep '__HTTP_STATUS__' | sed 's/__HTTP_STATUS__//')
    local body
    body=$(echo "$response" | grep -v '__HTTP_STATUS__')

    echo -e "${YELLOW}响应 (HTTP ${http_status})：${NC}"
    echo "$body" | python3 -m json.tool 2>/dev/null || echo "$body"

    if [ "$http_status" = "000" ] || [ "$http_status" = "" ]; then
        echo -e "\n${YELLOW}（本地 MCP Server 未运行）${NC}"
    fi
}

# 展示在线平台和网关能力
show_online_demo() {
    print_header "mcp.higress.ai 在线平台演示"

    echo -e "${GREEN}已上线的 MCP Server 托管平台：https://mcp.higress.ai/${NC}\n"

    echo -e "网关层透明提供的能力：\n"

    cat << 'EOF'
  ┌─────────────────────────────────────────────────────┐
  │                   AI Agent 请求                      │
  │                        ↓                            │
  │         ┌─────────────────────────────┐             │
  │         │      Higress AI 网关         │             │
  │         │                             │             │
  │         │  ┌──────────┐  ┌─────────┐  │             │
  │         │  │ 认证鉴权  │  │ 速率限制 │  │             │
  │         │  │ API Key  │  │ 防滥用  │  │             │
  │         │  └──────────┘  └─────────┘  │             │
  │         │                             │             │
  │         │  ┌──────────┐  ┌─────────┐  │             │
  │         │  │ 审计日志  │  │ 可观测  │  │             │
  │         │  │ 完整记录  │  │ Grafana │  │             │
  │         │  └──────────┘  └─────────┘  │             │
  │         │                             │             │
  │         │  ┌────────────────────────┐  │             │
  │         │  │   动态更新无损         │  │             │
  │         │  │   Wasm 热更新          │  │             │
  │         │  │   SSE 连接不断开       │  │             │
  │         │  └────────────────────────┘  │             │
  │         └─────────────────────────────┘             │
  │                        ↓                            │
  │               MCP Server（工具服务）                   │
  └─────────────────────────────────────────────────────┘
EOF

    echo -e "\n${CYAN}核心能力说明：${NC}"
    echo -e "  ${GREEN}✅ 统一认证${NC}：Bearer Token / API Key，支持多消费者差异化配置"
    echo -e "  ${GREEN}✅ 速率限制${NC}：按 API Key 限流，防止工具调用被滥用"
    echo -e "  ${GREEN}✅ 完整审计${NC}：记录所有工具调用请求/响应，满足合规要求"
    echo -e "  ${GREEN}✅ 实时监控${NC}：调用延迟、成功率、错误率 Grafana 大盘"
    echo -e "  ${GREEN}✅ 动态更新${NC}：MCP Server 逻辑实时更新，不断开任何 SSE 连接"
}

# 演示速率限制效果
demo_rate_limit() {
    print_header "演示：速率限制保护（防滥用）"

    echo -e "${CYAN}快速发送 5 个请求，观察速率限制触发...${NC}\n"

    for i in $(seq 1 5); do
        local response_code
        response_code=$(curl -s -o /dev/null -w "%{http_code}" \
            -X POST "${GATEWAY_URL}${MCP_ENDPOINT}" \
            -H "Content-Type: application/json" \
            -H "x-api-key: ${DEMO_API_KEY}" \
            -d '{"jsonrpc":"2.0","id":'"$i"',"method":"ping","params":{}}' \
            2>/dev/null || echo "000")

        if [ "$response_code" = "429" ]; then
            echo -e "  请求 $i: ${RED}HTTP 429 Too Many Requests — 速率限制触发！${NC}"
        elif [ "$response_code" = "000" ]; then
            echo -e "  请求 $i: ${YELLOW}连接失败（本地服务未运行）${NC}"
        else
            echo -e "  请求 $i: ${GREEN}HTTP ${response_code} 正常${NC}"
        fi
    done
    echo ""
}

# 完整演示
run_demo() {
    print_header "演示 4：MCP Server 托管与 AI Agent 调用"

    echo -e "${YELLOW}【第一步】访问在线平台（已上线）${NC}"
    echo -e "  → 打开浏览器：https://mcp.higress.ai/"
    echo -e "  → 展示基于 Higress 托管的远程 MCP 服务器列表\n"
    read -r -p "  按 Enter 继续..." 2>/dev/null || true
    echo ""

    echo -e "${YELLOW}【第二步】展示 AI Agent 通过网关调用工具${NC}\n"
    show_online_demo

    echo -e "\n${YELLOW}【第三步】演示网关层能力（如本地环境已配置）${NC}"
    echo -e "  → 配置参考：configs/demo4-mcp-server.yaml\n"

    echo -e "${GREEN}✅ 演示完成！${NC}"
    echo -e "${BLUE}亮点话术：AI Agent 工具调用天生需要安全管控。通过 Higress 托管 MCP Server，${NC}"
    echo -e "${BLUE}          认证/限流/审计/监控能力一次性获得，且对 Agent 完全透明。${NC}"
    echo -e "${BLUE}          动态更新无损——Wasm 插件热更新时，不会断开任何现有的 SSE 连接。${NC}"
}

case "${1:-demo}" in
    list)       list_tools ;;
    call)       call_tool ;;
    rate-limit) demo_rate_limit ;;
    online)     show_online_demo ;;
    demo)       run_demo ;;
    *)
        echo "用法：$0 [list|call|rate-limit|online|demo]"
        exit 1
        ;;
esac
