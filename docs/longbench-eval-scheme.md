# 使用 LongBench 验证 AISecGw 上下文压缩功能的评估方案

## 1. 背景与目标

### 1.1 评估目标

`ai-context-manager` 插件为 AISecGw 提供多轮对话上下文压缩能力，支持三种策略：截断（truncate）、滑动窗口（sliding_window）和紧凑化压缩（compaction）。本方案旨在通过 [LongBench](https://github.com/THUDM/LongBench) 基准测试，量化以下核心问题：

1. **压缩损失**：各压缩策略下，模型回答准确率相比无压缩基线的下降幅度是多少？
2. **Token 节省**：各策略能节省多少 Token 用量？
3. **压缩质量权衡**：Token 节省率与准确率损失之间的帕累托前沿在哪里？
4. **策略对比**：三种压缩策略在不同任务类型上各有什么优劣？

### 1.2 LongBench 简介

LongBench 是清华大学知识工程组（KEG）发布的长文本理解多任务基准，分为两个版本：

| 版本 | 任务数 | 上下文长度 | 评估格式 | 主要特点 |
|------|-------|-----------|--------|---------|
| LongBench v1 | 21 任务（英文 14 + 中文 5 + 代码 2） | 平均 5k–15k tokens | 开放式生成 | 双语、多任务、自动评估 |
| LongBench v2 | 503 题 | 8k–2M words | 多选题（A/B/C/D） | 更难、更长、可靠评估 |

**推荐优先使用 LongBench v1**，因为：
- 上下文长度（5k–15k tokens）与 ai-context-manager 的压缩触发阈值（数百到数千 tokens）更为匹配
- 多任务覆盖可分析压缩策略在不同场景下的表现差异
- 中文任务适合验证中文对话场景

---

## 2. 技术挑战与适配方案

### 2.1 关键挑战：单轮 vs. 多轮的格式差异

`ai-context-manager` 的压缩逻辑作用于 **messages 数组的历史轮次**，而 LongBench 的大多数任务是**单轮**格式（长文档 + 问题 → 答案）。

**解决方案：将 LongBench 任务转换为多轮对话格式**

针对不同任务类型，采用以下转换策略：

#### 策略 A：直接多轮（适用于对话类任务）

QMSUM / VCSUM（会议摘要）等任务本身含有多轮对话记录，可直接逐句/逐段拆分为多个消息轮次：

```
[会议片段 1] → user turn 1
[会议片段 2] → user turn 2
...
[会议片段 N] → user turn N
最终问题     → user turn N+1（触发压缩后的查询）
```

#### 策略 B：文档分块多轮（适用于文档 QA 类任务）

NarrativeQA / Qasper / HotpotQA 等任务含有长文档，按段落或固定 token 数分块，模拟用户逐段输入的交互过程：

```python
def doc_to_multi_turn(document: str, question: str, chunk_size: int = 512) -> list[dict]:
    """将单轮长文档 QA 转换为多轮对话格式"""
    chunks = split_document(document, chunk_size)
    messages = []
    for i, chunk in enumerate(chunks):
        messages.append({
            "role": "user",
            "content": f"[文档片段 {i+1}/{len(chunks)}]\n{chunk}"
        })
        if i < len(chunks) - 1:
            messages.append({
                "role": "assistant",
                "content": "好的，我已记录这段内容。"  # 占位助手回复
            })
    messages.append({
        "role": "user",
        "content": f"基于以上内容，请回答：{question}"
    })
    return messages
```

#### 策略 C：Token 阈值触发（适用于验证 token_threshold 参数）

保留单轮格式，通过设置 `token_threshold` 使插件在单个超长消息上触发压缩：

```yaml
summarize_strategy: compaction
token_threshold: 2000  # 低于文档长度，强制触发压缩
overlap_size: 1
```

这种方式最直接地测试了 `token_threshold` 参数在真实长文档场景中的效果。

---

## 3. 推荐测试任务集

根据与 ai-context-manager 压缩场景的相关性，选取以下 LongBench v1 任务：

| 任务名 | 类别 | 语言 | 平均长度 | 适配策略 | 优先级 |
|--------|------|------|---------|---------|------|
| `qmsum` | 会议摘要 | 英文 | ~10k | 策略 A | ⭐⭐⭐ 最高 |
| `vcsum` | 会议摘要 | 中文 | ~15k | 策略 A | ⭐⭐⭐ 最高 |
| `narrativeqa` | 单文档 QA | 英文 | ~18k | 策略 B | ⭐⭐⭐ 最高 |
| `multifieldqa_zh` | 单文档 QA | 中文 | ~6k | 策略 B+C | ⭐⭐⭐ 最高 |
| `hotpotqa` | 多文档 QA | 英文 | ~9k | 策略 B | ⭐⭐ 高 |
| `2wikimqa` | 多文档 QA | 英文 | ~5k | 策略 B | ⭐⭐ 高 |
| `gov_report` | 摘要 | 英文 | ~8k | 策略 C | ⭐⭐ 高 |
| `lsht` | 少样本学习 | 中文 | ~22k | 策略 C | ⭐ 中 |
| `passage_retrieval_en` | 合成任务 | 英文 | ~9k | 策略 C | ⭐ 中 |

---

## 4. 评估架构

### 4.1 整体流程

```
                    ┌─────────────────────────────────────────┐
                    │           评估流程                        │
                    └─────────────────────────────────────────┘
                                      │
               ┌──────────────────────▼───────────────────────┐
               │  1. 加载 LongBench 数据集（HuggingFace）         │
               │     - load_dataset('THUDM/LongBench')          │
               └──────────────────────┬───────────────────────┘
                                      │
               ┌──────────────────────▼───────────────────────┐
               │  2. 数据预处理                                  │
               │     - 选取任务子集（qmsum/vcsum/narrativeqa...）  │
               │     - 按策略 A/B/C 转换为多轮对话格式             │
               └──────────────────────┬───────────────────────┘
                                      │
               ┌──────────────────────▼───────────────────────┐
               │  3. 对每种压缩配置运行推理                        │
               │                                               │
               │  ┌─────────────────────────────────────┐      │
               │  │  测试客户端（Python）                  │      │
               │  │  POST /v1/chat/completions            │      │
               │  └────────────────┬────────────────────┘      │
               │                   │                            │
               │  ┌────────────────▼────────────────────┐      │
               │  │  AISecGw（Higress Gateway）            │      │
               │  │  + ai-context-manager 插件             │      │
               │  │  （不同配置：baseline/truncate/         │      │
               │  │    sliding_window/compaction）          │      │
               │  └────────────────┬────────────────────┘      │
               │                   │                            │
               │  ┌────────────────▼────────────────────┐      │
               │  │  LLM 后端（OpenAI / vLLM / 本地模型）  │      │
               │  └────────────────────────────────────┘      │
               └──────────────────────┬───────────────────────┘
                                      │
               ┌──────────────────────▼───────────────────────┐
               │  4. 记录评估数据                                │
               │     - 模型输出                                  │
               │     - x-context-prompt-tokens 响应头           │
               │     - 请求的消息数量/token 估算值                 │
               └──────────────────────┬───────────────────────┘
                                      │
               ┌──────────────────────▼───────────────────────┐
               │  5. 使用 LongBench 官方评估脚本计算指标           │
               │     - F1 / ROUGE-L / Accuracy                 │
               └──────────────────────┬───────────────────────┘
                                      │
               ┌──────────────────────▼───────────────────────┐
               │  6. 汇总生成评估报告                             │
               │     - 准确率对比表                               │
               │     - Token 节省率对比表                         │
               │     - 压缩质量权衡曲线                           │
               └─────────────────────────────────────────────┘
```

### 4.2 部署要求

| 组件 | 要求 | 说明 |
|------|------|------|
| AISecGw | 已部署并运行 | 需启用 ai-context-manager 插件 |
| LLM 后端 | OpenAI API 兼容接口 | 可用 vLLM + 开源模型（如 Qwen2.5-7B-Instruct）或直连 OpenAI |
| 评估环境 | Python 3.8+, pip | 用于运行 LongBench 官方脚本 |
| 网络 | 可访问 HuggingFace 或已本地缓存数据集 | - |

---

## 5. 压缩配置矩阵

测试以下 6 种配置，覆盖 ai-context-manager 的核心参数组合：

| 配置编号 | 配置名 | 关键参数 | 说明 |
|---------|--------|---------|------|
| C0 | **Baseline（无压缩）** | 不启用插件 或 `max_messages: 0, max_tokens: 0` | 基准对照 |
| C1 | **截断-4k** | `summarize_strategy: truncate, max_tokens: 4096` | 最激进压缩，直接丢弃早期内容 |
| C2 | **滑动窗口-4k** | `summarize_strategy: sliding_window, max_tokens: 4096, preserve_last_n: 2` | 保留最新内容的滑动窗口 |
| C3 | **压缩-间隔触发** | `summarize_strategy: compaction, compaction_interval: 3, overlap_size: 2` | 每 3 轮触发一次压缩 |
| C4 | **压缩-Token 阈值** | `summarize_strategy: compaction, token_threshold: 2000, overlap_size: 2` | 超过 2000 tokens 时触发 |
| C5 | **压缩-轮次完整性** | `summarize_strategy: compaction, compaction_interval: 3, overlap_size: 2, preserve_turn_pairs: true` | 额外保护对话轮次完整性 |

---

## 6. 评估脚本设计

### 6.1 目录结构

```
eval/
├── longbench/
│   ├── README.md               # 本方案（使用说明）
│   ├── requirements.txt        # Python 依赖
│   ├── config/
│   │   ├── gateway.yaml        # AISecGw 网关地址和认证配置
│   │   └── tasks.yaml          # 任务列表和每任务的转换策略配置
│   ├── scripts/
│   │   ├── preprocess.py       # 数据预处理（LongBench → 多轮格式）
│   │   ├── predict.py          # 调用网关并收集预测结果
│   │   ├── evaluate.py         # 调用 LongBench 官方评估逻辑计算分数
│   │   └── report.py           # 汇总生成对比报告
│   └── results/                # 评估结果输出目录（git-ignored）
│       ├── C0_baseline/
│       ├── C1_truncate_4k/
│       └── ...
```

### 6.2 核心脚本逻辑

#### `preprocess.py` — 数据预处理

```python
"""
将 LongBench 数据集转换为 AISecGw 兼容的多轮对话格式。

输入:  HuggingFace LongBench 数据集
输出:  JSONL 文件，每行一个样本，包含 messages 数组和 ground_truth 答案
"""
from datasets import load_dataset

TASKS = ["qmsum", "vcsum", "narrativeqa", "multifieldqa_zh", "hotpotqa"]
CHUNK_SIZE = 512  # 文档分块大小（tokens 估算）

def chunk_document(text: str, chunk_size: int = CHUNK_SIZE) -> list[str]:
    """按段落边界分块，目标 chunk_size 字符"""
    paragraphs = text.split('\n\n')
    chunks, current = [], ""
    for para in paragraphs:
        if len(current) + len(para) > chunk_size * 4 and current:
            chunks.append(current.strip())
            current = para
        else:
            current += "\n\n" + para
    if current:
        chunks.append(current.strip())
    return chunks

def convert_to_multi_turn(sample: dict, task: str) -> dict:
    """
    将 LongBench 样本转换为多轮对话格式。
    返回 {"messages": [...], "answer": "...", "task": task, "id": sample["_id"]}
    """
    context = sample.get("context", "")
    question = sample.get("input", sample.get("question", ""))
    answer = sample.get("answers", sample.get("answer", ""))

    if task in ["qmsum", "vcsum"]:
        # 策略 A：对话/会议记录直接按段拆分
        turns = chunk_document(context, chunk_size=256)
        messages = []
        for i, turn in enumerate(turns):
            messages.append({"role": "user", "content": f"[段落 {i+1}]\n{turn}"})
            if i < len(turns) - 1:
                messages.append({"role": "assistant", "content": "已收到，请继续。"})
        messages.append({"role": "user", "content": question})
    else:
        # 策略 B：长文档分块为多轮
        chunks = chunk_document(context)
        messages = []
        for i, chunk in enumerate(chunks):
            messages.append({"role": "user", "content": f"[文档片段 {i+1}/{len(chunks)}]\n{chunk}"})
            if i < len(chunks) - 1:
                messages.append({"role": "assistant", "content": "好的，继续。"})
        messages.append({"role": "user", "content": question})

    return {
        "id": sample.get("_id", ""),
        "task": task,
        "messages": messages,
        "answer": answer,
        "original_length": len(context)
    }
```

#### `predict.py` — 网关调用与结果收集

```python
"""
调用 AISecGw 收集模型预测，同时记录 Token 使用情况。

使用方式:
  python predict.py --config C3_compaction_interval --tasks qmsum vcsum narrativeqa
"""
import argparse, json, time
import requests

def predict(sample: dict, gateway_url: str, api_key: str, model: str) -> dict:
    """发送请求到网关，收集响应和 Token 统计"""
    start = time.time()
    resp = requests.post(
        f"{gateway_url}/v1/chat/completions",
        headers={"Authorization": f"Bearer {api_key}", "Content-Type": "application/json"},
        json={"model": model, "messages": sample["messages"], "max_tokens": 256}
    )
    latency_ms = (time.time() - start) * 1000
    resp.raise_for_status()

    # 提取 ai-context-manager 添加的响应头（需 track_token_usage: true）
    prompt_tokens  = int(resp.headers.get("x-context-prompt-tokens", 0))
    total_tokens   = int(resp.headers.get("x-context-total-tokens", 0))

    data = resp.json()
    prediction = data["choices"][0]["message"]["content"]

    return {
        "id": sample["id"],
        "task": sample["task"],
        "prediction": prediction,
        "answer": sample["answer"],
        "prompt_tokens_actual": prompt_tokens,
        "total_tokens_actual": total_tokens,
        "latency_ms": latency_ms,
        "original_length": sample["original_length"],
        "message_count": len(sample["messages"]),
    }
```

#### `evaluate.py` — 指标计算

```python
"""
计算 LongBench 官方评估指标（F1/ROUGE-L/Accuracy），
并在配置间进行比较，输出对比表格。
"""
import json
from longbench_utils import (  # 从 LongBench 官方 repo 复制
    qa_f1_score, rouge_score, classification_score
)

TASK_METRICS = {
    "narrativeqa":     qa_f1_score,
    "qasper":          qa_f1_score,
    "multifieldqa_zh": qa_f1_score,
    "hotpotqa":        qa_f1_score,
    "2wikimqa":        qa_f1_score,
    "qmsum":           rouge_score,
    "vcsum":           rouge_score,
    "gov_report":      rouge_score,
    "lsht":            classification_score,
}

def compute_task_score(results: list[dict], task: str) -> float:
    metric_fn = TASK_METRICS[task]
    scores = [metric_fn(r["prediction"], r["answer"]) for r in results]
    return sum(scores) / len(scores) * 100

def token_reduction_ratio(results: list[dict], baseline_results: list[dict]) -> float:
    """计算相对于 baseline 的 Token 节省率"""
    baseline_avg = sum(r["prompt_tokens_actual"] for r in baseline_results) / len(baseline_results)
    compressed_avg = sum(r["prompt_tokens_actual"] for r in results) / len(results)
    return (baseline_avg - compressed_avg) / baseline_avg * 100
```

### 6.3 快速启动命令

```bash
# 1. 安装依赖
cd eval/longbench
pip install -r requirements.txt

# 2. 预处理数据（下载 LongBench 并转换格式）
python scripts/preprocess.py --tasks qmsum vcsum narrativeqa multifieldqa_zh hotpotqa

# 3. 运行基准评估（无压缩）
python scripts/predict.py \
  --config C0_baseline \
  --gateway-url http://your-gateway:8080 \
  --api-key YOUR_KEY \
  --model qwen2.5-7b-instruct

# 4. 运行压缩配置评估
for config in C1_truncate_4k C2_sliding_window_4k C3_compaction_interval C4_compaction_token C5_compaction_turnpair; do
  python scripts/predict.py --config $config ...
done

# 5. 计算并输出对比报告
python scripts/report.py --configs C0 C1 C2 C3 C4 C5
```

---

## 7. 评估指标体系

### 7.1 核心指标

| 指标 | 计算方法 | 解读 |
|------|---------|------|
| **任务准确率** | 各任务官方指标（F1/ROUGE-L/Acc） | 越高越好，反映压缩后模型理解能力 |
| **质量保留率** | `accuracy_compressed / accuracy_baseline × 100%` | 越高越好，>90% 表示压缩损失可接受 |
| **Token 节省率** | `(baseline_tokens - compressed_tokens) / baseline_tokens × 100%` | 越高越好，反映成本优化幅度 |
| **压缩效率分** | `质量保留率 × Token节省率 / 100` | 综合权衡指标 |
| **延迟变化** | `latency_compressed / latency_baseline` | 反映插件处理开销（应 <1.05） |

### 7.2 预期结果表（示例）

| 配置 | 平均质量保留率 | 平均 Token 节省率 | 压缩效率分 | 推荐场景 |
|------|-------------|-----------------|-----------|---------|
| C0 基准 | 100% | 0% | 0 | 对照组 |
| C1 截断-4k | ~60-70% | ~50-60% | ~35 | 不推荐（信息丢失大）|
| C2 滑动窗口-4k | ~75-85% | ~45-55% | ~40 | 简单场景 |
| C3 压缩-间隔 | ~80-90% | ~40-50% | ~40 | 长对话场景 |
| C4 压缩-Token 阈值 | ~82-92% | ~35-45% | ~38 | Token 成本控制 |
| C5 压缩-轮次完整性 | ~85-95% | ~35-45% | ~40 | 高质量要求场景 |

> **注**：以上为估算值，实际结果取决于模型能力、任务类型和参数调整。

### 7.3 按任务类型的预期表现

| 任务类型 | 截断 | 滑动窗口 | 压缩 | 原因分析 |
|---------|------|---------|------|---------|
| 会议摘要（QMSUM/VCSUM） | ❌ 差 | ⚠️ 一般 | ✅ 好 | 需要整体理解，压缩摘要保留关键信息 |
| 单文档 QA（NarrativeQA） | ❌ 差 | ⚠️ 一般 | ⚠️ 一般 | 答案可能在被截断的早期文档段落中 |
| 多文档 QA（HotpotQA） | ❌ 差 | ⚠️ 一般 | ⚠️ 一般 | 跨文档推理受压缩影响较大 |
| 少样本学习（LSHT） | ⚠️ 一般 | ✅ 好 | ✅ 好 | 最近示例更重要，滑动窗口保留效果好 |

---

## 8. 插件配置示例（Higress WasmPlugin）

以下为评估中使用的各配置对应的 Higress WasmPlugin 配置文件。

### C0：基准（不启用插件）

无需 WasmPlugin 配置，直接透传。

### C1：截断策略

```yaml
apiVersion: extensions.higress.io/v1alpha1
kind: WasmPlugin
metadata:
  name: ai-context-manager-c1
  namespace: higress-system
spec:
  matchRules:
  - config:
      max_tokens: 4096
      summarize_strategy: truncate
      preserve_system_message: true
      preserve_last_n: 1
      track_token_usage: true
    ingress:
    - longbench-eval
  url: oci://higress-registry.cn-hangzhou.cr.aliyuncs.com/plugins/ai-context-manager:1.0.0
```

### C2：滑动窗口策略

```yaml
apiVersion: extensions.higress.io/v1alpha1
kind: WasmPlugin
metadata:
  name: ai-context-manager-c2
  namespace: higress-system
spec:
  matchRules:
  - config:
      max_tokens: 4096
      summarize_strategy: sliding_window
      preserve_system_message: true
      preserve_last_n: 2
      track_token_usage: true
    ingress:
    - longbench-eval
  url: oci://higress-registry.cn-hangzhou.cr.aliyuncs.com/plugins/ai-context-manager:1.0.0
```

### C3：压缩策略（间隔触发）

```yaml
apiVersion: extensions.higress.io/v1alpha1
kind: WasmPlugin
metadata:
  name: ai-context-manager-c3
  namespace: higress-system
spec:
  matchRules:
  - config:
      summarize_strategy: compaction
      compaction_interval: 3
      overlap_size: 2
      preserve_system_message: true
      preserve_last_n: 2
      track_token_usage: true
    ingress:
    - longbench-eval
  url: oci://higress-registry.cn-hangzhou.cr.aliyuncs.com/plugins/ai-context-manager:1.0.0
```

### C4：压缩策略（Token 阈值触发）

```yaml
apiVersion: extensions.higress.io/v1alpha1
kind: WasmPlugin
metadata:
  name: ai-context-manager-c4
  namespace: higress-system
spec:
  matchRules:
  - config:
      summarize_strategy: compaction
      token_threshold: 2000
      overlap_size: 2
      preserve_system_message: true
      preserve_last_n: 2
      token_estimate_ratio: 4.0
      track_token_usage: true
    ingress:
    - longbench-eval
  url: oci://higress-registry.cn-hangzhou.cr.aliyuncs.com/plugins/ai-context-manager:1.0.0
```

### C5：压缩策略（轮次完整性保护）

```yaml
apiVersion: extensions.higress.io/v1alpha1
kind: WasmPlugin
metadata:
  name: ai-context-manager-c5
  namespace: higress-system
spec:
  matchRules:
  - config:
      summarize_strategy: compaction
      compaction_interval: 3
      overlap_size: 2
      preserve_turn_pairs: true
      preserve_system_message: true
      preserve_last_n: 2
      track_token_usage: true
    ingress:
    - longbench-eval
  url: oci://higress-registry.cn-hangzhou.cr.aliyuncs.com/plugins/ai-context-manager:1.0.0
```

---

## 9. 重要注意事项

### 9.1 压缩机制的本质限制

`ai-context-manager` 使用**提取式摘要**（extractive summarization），即将旧消息内容原文拼接为摘要，而非 LLM 生成的抽象摘要（abstractive summarization）。这意味着：

- 对于**需要综合理解**的任务（如 QMSUM 摘要）：提取式摘要效果有限，会损失较多语义
- 对于**事实检索类**任务（如单文档 QA）：摘要保留了原文内容，损失相对较小
- **Token 节省的根本来源**：通过压缩旧轮次减少发送给 LLM 的 tokens，但摘要本身也占用一定 tokens

### 9.2 LongBench 评估的局限性

1. **格式差异**：LongBench 设计为测试单轮长文档理解，我们的多轮转换引入了额外的格式噪声
2. **分块影响**：文档分块边界的选择会影响评估结果，应统一使用相同的分块策略
3. **模型依赖**：压缩效果与所用 LLM 的能力强相关，建议使用支持 8k+ context 的模型作为基准

### 9.3 推荐最小可行评估集

如时间和资源有限，建议优先运行以下最小集：

| 任务 | 样本数 | 理由 |
|------|-------|------|
| `vcsum`（中文） | 50 条 | 验证中文多轮会议场景 |
| `narrativeqa`（英文） | 50 条 | 验证英文长文档 QA |
| `hotpotqa`（英文） | 50 条 | 验证多文档推理场景 |

总计约 150 次 LLM API 调用 × 6 种配置 = **900 次调用**，成本可控。

---

## 10. 后续改进方向

基于评估结果，可进一步探索以下优化方向：

1. **接入 LLM 生成摘要**：在 compaction 策略中，通过插件组合（ai-context-manager + ai-proxy 两阶段处理）实现 LLM 生成的抽象摘要，预期大幅提升质量保留率
2. **自适应分块大小**：根据任务类型动态调整 `chunk_size`，优化多轮转换质量
3. **与 ai-history 插件联动**：结合 ai-history 实现跨会话的长期记忆注入（`inject_memory: true`），测试记忆增强对准确率的提升
4. **RAG 对比**：与 LongBench 官方的 `--rag N` 评估模式对比，了解压缩 vs 检索增强的效果差异

---

## 附录：参考资料

- [LongBench GitHub](https://github.com/THUDM/LongBench)
- [LongBench v2 论文](https://arxiv.org/abs/2412.15204)
- [LongBench 数据集（HuggingFace）](https://huggingface.co/datasets/THUDM/LongBench)
- [ai-context-manager 插件文档](../plugins/wasm-go/extensions/ai-context-manager/README.md)
- [Google ADK EventsCompactionConfig](https://google.github.io/adk-docs/context/)
