# LongBench 数据集选择分析：ai-context-manager 上下文管理能力测试

## 背景

LongBench 是清华大学发布的一个双语、多任务、长文本理解基准测试集（[论文](https://arxiv.org/abs/2308.14508) / [GitHub](https://github.com/THUDM/LongBench)），包含 21 个数据集，涵盖 6 大任务类别，单条样本长度通常在 5k–15k token 范围内，部分超过 30k token。

`ai-context-manager` 插件的核心职责是在 AI 网关层对 LLM 请求的上下文窗口进行管理，主要能力包括：

| 能力 | 配置参数 |
|------|---------|
| 消息数量限制 | `max_messages` |
| Token 数量限制 | `max_tokens` |
| 滑动窗口策略 | `summarize_strategy: sliding_window` |
| 截断策略 | `summarize_strategy: truncate` |
| 上下文压缩/紧凑化 | `summarize_strategy: compaction` |
| 对话轮次触发压缩 | `compaction_interval` |
| Token 阈值触发压缩 | `token_threshold` |
| 压缩窗口重叠 | `overlap_size` |
| 对话轮次完整性 | `preserve_turn_pairs` |
| 关键消息固定 | `pinned_message_roles` |
| 会话记忆注入 | `inject_memory` / `memory_key` |
| 系统提示词保护 | `preserve_system_message` |
| Token 使用追踪 | `track_token_usage` |

---

## LongBench 全部数据集概览

LongBench v1 共 21 个数据集，分为 6 大类：

### 类别一：单文档问答（Single-Document QA）

| 数据集 | 语言 | 平均长度 | 描述 |
|--------|------|---------|------|
| NarrativeQA | 英文 | ~18,000 token | 基于完整书籍/电影剧本的阅读理解问答，文档极长 |
| Qasper | 英文 | ~3,600 token | 科学论文问答，包含摘要/章节/表格等结构化内容 |
| MultiFieldQA-en | 英文 | ~4,600 token | 多领域混合长文档问答（法律、政府、学术） |
| MultiFieldQA-zh | 中文 | ~6,700 token | 多领域混合长文档中文问答 |

### 类别二：多文档问答（Multi-Document QA）

| 数据集 | 语言 | 平均长度 | 描述 |
|--------|------|---------|------|
| HotpotQA | 英文 | ~9,100 token | 多跳推理问答，答案散布在多个文档片段中 |
| 2WikiMultihopQA | 英文 | ~4,900 token | 基于 Wikipedia 的多跳链式推理问答 |
| MuSiQue | 英文 | ~11,200 token | 需要 2-4 步推理链的多跳问答，干扰段落多 |
| DuReader | 中文 | ~15,800 token | 百度搜索场景多文档问答，中文，上下文极长 |

### 类别三：文本摘要（Summarization）

| 数据集 | 语言 | 平均长度 | 描述 |
|--------|------|---------|------|
| GovReport | 英文 | ~8,700 token | 美国政府调查报告摘要，文档结构化且极长 |
| QMSum | 英文 | ~14,000 token | 会议记录查询式摘要，多轮多参与者对话形式 |
| MultiNews | 英文 | ~2,100 token | 多篇新闻摘要，需跨文档信息整合 |
| VCSUM | 中文 | ~15,300 token | 中文会议记录摘要，多轮对话形式 |

### 类别四：少样本学习（Few-Shot Learning）

| 数据集 | 语言 | 平均长度 | 描述 |
|--------|------|---------|------|
| TREC | 英文 | ~5,200 token | 长上下文中包含大量 few-shot 样例的问题分类 |
| TriviaQA | 英文 | ~8,100 token | 长文档中嵌入大量 few-shot 样例的问答 |
| SAMSum | 英文 | ~6,300 token | 基于大量对话摘要样例的 few-shot 摘要 |
| LSHT | 中文 | ~24,100 token | 中文新闻分类（few-shot），上下文极长 |

### 类别五：合成任务（Synthetic Tasks）

| 数据集 | 语言 | 平均长度 | 描述 |
|--------|------|---------|------|
| PassageCount | 英文 | ~11,100 token | 在大量段落中计数特定内容出现次数 |
| PassageRetrieval-en | 英文 | ~9,300 token | 从 30 个段落中精确检索指定段落（英文） |
| PassageRetrieval-zh | 中文 | ~6,300 token | 从多段落中精确检索指定段落（中文） |

### 类别六：代码补全（Code Completion）

| 数据集 | 语言 | 平均长度 | 描述 |
|--------|------|---------|------|
| LCC | 代码 | ~1,200 token | 基于上下文的长代码文件补全任务 |
| RepoBench-P | 代码 | ~4,200 token | 跨文件仓库级代码补全，需要跨上下文推理 |

---

## 适合测试 ai-context-manager 的数据集

评选标准：

1. **长上下文压力**：数据集样本长度足够长，能够触发 `max_tokens` 或 `token_threshold` 等机制
2. **信息保留验证**：答案依赖于上下文中的特定信息，可验证压缩后关键信息是否被保留
3. **对话形式适配**：数据集具有多轮对话结构，贴近实际使用场景
4. **中文支持**：覆盖中英双语，验证 token 估算对中文文本的准确性

### ✅ 强烈推荐（直接测试上下文管理核心能力）

#### 1. PassageRetrieval-en / PassageRetrieval-zh（合成检索）

**推荐理由：**
- 明确的「指针型」问题：模型必须在长上下文中精确定位特定段落
- 最适合验证 `compaction`（压缩）策略是否保留了目标段落的关键信息
- 可以系统地测试不同 `overlap_size` 和 `token_threshold` 配置下的信息留存率
- 英/中双语版本可同时验证中英文 token 估算精度（`token_estimate_ratio`）

**测试映射：**
- `summarize_strategy: compaction` + `token_threshold` → 验证压缩后信息是否可检索
- `summarize_strategy: sliding_window` → 验证关键段落是否在窗口内
- `token_estimate_ratio` 中英文差异验证

#### 2. NarrativeQA（单文档长叙事问答）

**推荐理由：**
- 平均 ~18,000 token，远超大多数模型默认上下文窗口，必然触发上下文管理
- 答案需要对书籍/剧本全局理解，测试压缩后是否保留了叙事性关键信息
- 最能体现 `compaction` 策略的真实价值：压缩早期内容但保留核心情节

**测试映射：**
- `max_tokens: 4096` + `summarize_strategy: compaction` → 验证关键信息是否在压缩摘要中保留
- `preserve_last_n` 配置效果验证

#### 3. QMSum / VCSUM（会议记录摘要）

**推荐理由：**
- 会议记录天然具有**多轮对话结构**（多参与者、多轮交替发言），与 AI 对话的 messages 格式高度相似
- QMSum 平均 ~14,000 token，VCSUM 平均 ~15,300 token，能触发所有上下文管理策略
- 非常适合测试 `preserve_turn_pairs: true`（对话轮次完整性），验证截断时 user-assistant 对不被拆散
- VCSUM（中文）可测试中文场景下 token 估算与压缩效果

**测试映射：**
- `preserve_turn_pairs: true` + 会议多轮对话格式 → 验证轮次完整性保护
- `compaction_interval` 按轮次触发 → 模拟多轮对话压缩
- 中英双语覆盖

#### 4. HotpotQA / MuSiQue（多跳多文档问答）

**推荐理由：**
- 答案需要跨多个文档片段进行链式推理（2-4 跳），测试**压缩策略是否破坏了推理链**
- MuSiQue 平均 ~11,200 token，干扰段落多，是检验信息过滤能力的极佳场景
- HotpotQA 是 LongBench 中最广泛使用的多文档推理基准，结果具有可比性

**测试映射：**
- `summarize_strategy: compaction` + `overlap_size` → 验证推理链在压缩边界处的连贯性
- `pinned_message_roles` → 模拟固定关键推理步骤不被淘汰
- `max_tokens` 触发后的推理准确率下降曲线

#### 5. GovReport（长文档摘要）

**推荐理由：**
- 极长的政府报告（平均 ~8,700 token），结构化内容（章节、数据、结论）
- 专门测试**摘要质量**：即压缩策略提取的信息是否覆盖文档核心内容
- `compaction_summary_template` 自定义模板效果可以通过此数据集量化

**测试映射：**
- `summarize_strategy: compaction` + `compaction_summary_template` → 压缩摘要质量
- `token_threshold` 触发时机对最终摘要质量的影响

### 🔶 次要推荐（可测试特定能力）

#### 6. MultiFieldQA-zh（中文长文档问答）

**推荐理由：**
- 专门针对中文场景，平均 ~6,700 token
- 可精确测试 `token_estimate_ratio` 参数对中文的适配性（中文字符通常每字 0.6–0.8 token）
- 多领域（法律/政府/学术）覆盖真实业务场景

**测试映射：**
- `token_estimate_ratio` 中文调优验证（建议测试 1.5 vs 4.0 vs 实际值）

#### 7. DuReader（中文超长多文档问答）

**推荐理由：**
- 平均 ~15,800 token，是 LongBench 中**最长的中文数据集**
- 百度搜索场景，贴近实际中文 AI 应用
- 能触发所有基于 token 的阈值配置

**测试映射：**
- 极端长上下文下的 `token_threshold` 压缩效果
- `max_tokens` 硬截断 vs `compaction` 软压缩的效果对比

#### 8. LSHT（中文超长 few-shot 分类）

**推荐理由：**
- 平均 ~24,100 token，是 LongBench 中**最长的数据集**
- 大量 few-shot 样例在上下文中排列，测试**早期样例是否被滑动窗口淘汰**
- 适合验证 `preserve_last_n` 与 `preserve_system_message` 对关键 few-shot 示例的保护效果

**测试映射：**
- `preserve_last_n` + `summarize_strategy: sliding_window` → 验证 few-shot 样例保留
- 极长上下文下的 token 估算精度

#### 9. SAMSum（对话摘要 few-shot）

**推荐理由：**
- 对话摘要任务，包含大量真实的短对话文本，贴近 AI chat 场景
- 可用于测试 `inject_memory`（会话记忆注入），将摘要注入到新对话的上下文中

**测试映射：**
- `inject_memory: true` + 对话摘要作为 memory → 验证记忆注入效果

### ❌ 不推荐（与上下文管理能力相关性低）

| 数据集 | 不推荐理由 |
|--------|-----------|
| **TREC** | 问题分类任务，答案是短标签（如 "HUM", "NUM"），与上下文信息保留无直接关系 |
| **TriviaQA** | 答案通常为实体名词，不能有效衡量长上下文压缩后的信息保留质量 |
| **PassageCount** | 计数任务对精确信息留存要求极高，但上下文管理必然导致计数任务失败，无法提供有意义的对比 |
| **LCC** | 代码补全任务，单文件代码上下文，压缩代码会引入语法错误，不适合评估 |
| **RepoBench-P** | 跨文件代码补全，需要保留所有文件内容，压缩会破坏代码逻辑，不适合评估 |
| **2WikiMultihopQA** | 与 HotpotQA 高度重叠，优先选择 HotpotQA / MuSiQue |
| **MultiFieldQA-en** | 与 MultiFieldQA-zh 能力重叠，优先选择中文版或 NarrativeQA |
| **MultiNews** | 平均长度仅 ~2,100 token，上下文管理触发概率低 |

---

## 推荐数据集汇总

| 优先级 | 数据集 | 语言 | 平均长度 | 主要测试能力 |
|--------|--------|------|---------|-------------|
| ⭐⭐⭐ | **PassageRetrieval-en** | 英文 | ~9,300 token | compaction 信息保留、sliding_window、token 估算 |
| ⭐⭐⭐ | **PassageRetrieval-zh** | 中文 | ~6,300 token | compaction 信息保留（中文）、token_estimate_ratio 验证 |
| ⭐⭐⭐ | **NarrativeQA** | 英文 | ~18,000 token | max_tokens 触发、compaction 全局信息保留 |
| ⭐⭐⭐ | **QMSum** | 英文 | ~14,000 token | preserve_turn_pairs、compaction_interval、多轮对话 |
| ⭐⭐⭐ | **VCSUM** | 中文 | ~15,300 token | 中文多轮对话、preserve_turn_pairs、中文 token 估算 |
| ⭐⭐⭐ | **HotpotQA** | 英文 | ~9,100 token | compaction overlap_size、推理链完整性 |
| ⭐⭐⭐ | **MuSiQue** | 英文 | ~11,200 token | pinned_message_roles、多跳推理链保留 |
| ⭐⭐⭐ | **GovReport** | 英文 | ~8,700 token | compaction_summary_template、token_threshold |
| ⭐⭐ | **MultiFieldQA-zh** | 中文 | ~6,700 token | token_estimate_ratio 中文调优 |
| ⭐⭐ | **DuReader** | 中文 | ~15,800 token | 超长中文上下文、极端 token 压力 |
| ⭐⭐ | **LSHT** | 中文 | ~24,100 token | preserve_last_n、sliding_window（最长数据集）|
| ⭐⭐ | **SAMSum** | 英文 | ~6,300 token | inject_memory、对话摘要记忆注入 |

---

## 能力覆盖矩阵

| ai-context-manager 能力 | 测试数据集 |
|------------------------|----------|
| `max_tokens` / `max_messages` 触发 | NarrativeQA、DuReader、LSHT（超长文本） |
| `summarize_strategy: sliding_window` | PassageRetrieval、HotpotQA（验证窗口边界信息丢失） |
| `summarize_strategy: truncate` | GovReport、NarrativeQA（与 compaction 对比基线） |
| `summarize_strategy: compaction` | PassageRetrieval、NarrativeQA、GovReport（信息保留） |
| `compaction_interval`（按轮次触发） | QMSum、VCSUM（多轮对话，精确轮次计数） |
| `token_threshold`（按 token 触发） | DuReader、NarrativeQA、LSHT（超长文本） |
| `overlap_size`（压缩边界重叠） | HotpotQA、MuSiQue（推理链跨边界） |
| `preserve_turn_pairs`（轮次完整性） | QMSum、VCSUM、SAMSum（对话格式） |
| `pinned_message_roles`（消息固定） | MuSiQue（模拟关键推理步骤固定） |
| `inject_memory`（记忆注入） | SAMSum（对话摘要作为外部记忆） |
| `token_estimate_ratio`（中文 token 估算） | MultiFieldQA-zh、PassageRetrieval-zh、VCSUM |
| `preserve_system_message` | 所有数据集（系统提示保护通用验证） |
| `track_token_usage` | 所有数据集（实际 token 消耗 vs 估算值比对） |

---

## 测试方法说明

使用 LongBench 测试 ai-context-manager 的推荐方法：

1. **基线测试**：不启用上下文管理（passthrough 模式），记录模型在各数据集的原始得分
2. **策略对比**：分别启用 `truncate` / `sliding_window` / `compaction`，对比得分损失
3. **参数敏感性**：固定策略，调整 `max_tokens` / `token_threshold` / `overlap_size`，绘制得分-参数曲线
4. **中文专项**：在 MultiFieldQA-zh / PassageRetrieval-zh / VCSUM 上调优 `token_estimate_ratio`

> **注意**：PassageCount 数据集不适合此类评估，因为计数任务要求完整保留所有段落，任何压缩都会导致任务失败，无法体现上下文管理的价值。
