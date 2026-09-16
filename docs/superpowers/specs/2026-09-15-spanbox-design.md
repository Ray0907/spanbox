# spanbox 設計文件

日期：2026-09-15
狀態：v2，已吸收 review-1

## 1. 目標

單一 binary 的 LLM 監控平台。只收 OTLP，zero-config 起動，embedded SQLite，一個 port 同時收 trace 與出 UI。對標 Langfuse 的「看 trace、看 token/cost」核心，捨棄它需要 Postgres + ClickHouse + Redis + S3 的部署負擔。精神同 OpenObserve：單機 embedded 起步，保留換儲存層的路。

成功標準：

```
./spanbox            # 或 docker run -p 4318:4318 -v ./data:/data spanbox
```

之後任何 OTel SDK 把 exporter endpoint 指到 `http://host:4318`，打開 `http://host:4318/` 就看得到 trace、每個 span 的 raw attributes / events / resource / scope / links、prompt 與 completion 內容、token、cost、latency。零必填環境變數。

## 2. 非目標（v1 明確不做）

- 多租戶、多使用者帳密、RBAC
- Prompt management、datasets、playground、evals/scores 寫入
- gRPC OTLP（OTLP/HTTP logs 僅接受 GenAI operation events；metrics 接受後丟棄）
- 叢集、S3、Parquet（儲存層走 interface，日後可換，現在不付）
- 自有 SDK、Langfuse ingestion API 相容
- 內容截斷或遮罩（`REDACT` 之類日後再加）
- 儲存 OTLP span 的 `flags`、dropped counts、scope schema URL

## 3. 已定決策

| 項目 | 決定 |
|---|---|
| 規模 | 單機、每天數萬到數十萬 span、保留數週 |
| Ingest | OTLP/HTTP，`protobuf` 與 `JSON` 兩種 encoding，同一 `POST /v1/traces` |
| 語言 | Go，`CGO_ENABLED=0`，SQLite 用 `modernc.org/sqlite` v1.59+ |
| 儲存 | SQLite 單檔，WAL，FTS5 全文，JSON 欄存 raw attributes |
| UI | `html/template` + htmx，vendor 一支 uPlot，無 node toolchain，`embed.FS` 打包 |
| Auth | 選填 `AUTH_TOKEN`；設了則 ingest 走 Bearer、UI 走 HMAC cookie login；沒設就 ingest 與 UI 全開並在啟動 log 警告，但 `/sql` 停用（回 404 並說明需設 `AUTH_TOKEN`） |
| 屬性相容 | 標準 `gen_ai.*` 為主，加一張 Go map normalize 表對映 OpenLLMetry / OpenInference / Langfuse v3 / Vercel AI SDK；raw attributes 全存 |
| Span 分類 | ingest 時算 `kind` 欄：`llm` / `embedding` / `tool` / `retrieval` / `agent` / `other` |
| 內容 | prompt / completion 全存不截斷，靠 retention 控體積 |
| 價格表 | 來源已給 cost 優先；否則查內建 LiteLLM `model_prices_and_context_window.json`，`PRICING_FILE` 可覆蓋，對不到就 cost 留空 |
| 設定 | 五個選填 env var，見 §9 |
| 打包 | binary + `FROM scratch` Dockerfile |
| Module | `github.com/Ray0907/spanbox`，repo `/private/tmp/spanbox` |

## 4. 架構

```
OTel SDK ──POST /v1/traces──▶ otlp decode ──▶ normalize ──▶ pricing ──▶ store (SQLite)
                                                                          ▲
browser ──GET /,/traces/{id},/dashboard,/search,/sql──▶ web ──────────────┘
                                                              retention goroutine
```

單一 process、單一 `http.Server`、單一 SQLite 檔 `$DATA_DIR/spanbox.db`。

### 4.1 套件

```
cmd/spanbox/main.go          組裝、env、啟動 server 與 retention
internal/config              讀 env，給預設值，內含非 env 的常數（timeouts、limits）
internal/otlp                解 OTLP proto/JSON 成 []RawSpan（含 gzip、id 驗證、AnyValue 轉換）
internal/normalize           RawSpan → store.Span：欄位對映 + kind 判定（純函式、table-driven）
internal/pricing             embed 價格表、provider alias、lookup、算 cost
internal/store               SQLite open sequence、migrate、InsertBatch、查詢、retention
internal/web                 handlers、auth、templates/、static/（htmx、uPlot、css）
Dockerfile
README.md
```

`store` 是唯一碰 SQL 的地方。`normalize` 不 import 任何 OTel 型別，只吃 `map[string]any`。

### 4.2 依賴

- `modernc.org/sqlite`
- `go.opentelemetry.io/proto/otlp`（generated types）
- `google.golang.org/protobuf`（proto + protojson）
- `google.golang.org/genproto/googleapis/rpc/status`（錯誤回應用 `google.rpc.Status`）

其餘 stdlib。前端 `htmx.min.js`、`uPlot.min.js`、`uPlot.min.css` vendor 進 `internal/web/static/`。

### 4.3 `http.Server` 參數（config 常數，非 env）

`ReadHeaderTimeout=10s`、`ReadTimeout=60s`、`IdleTimeout=120s`、`MaxHeaderBytes=64 KiB`。所有回應加 `X-Content-Type-Options: nosniff`；UI 路徑再加 `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'`。

## 5. 資料模型

### 5.1 `spans`

```sql
CREATE TABLE spans (
  id              INTEGER PRIMARY KEY,
  trace_id        TEXT NOT NULL CHECK(length(trace_id) = 32),
  span_id         TEXT NOT NULL CHECK(length(span_id) = 16),
  parent_span_id  TEXT NOT NULL DEFAULT '',           -- '' 表示 root
  name            TEXT NOT NULL,
  kind            TEXT NOT NULL CHECK(kind IN ('llm','embedding','tool','retrieval','agent','other')),
  service_name    TEXT NOT NULL DEFAULT 'unknown',
  start_ns        INTEGER NOT NULL,
  end_ns          INTEGER NOT NULL,
  duration_ms     REAL NOT NULL,
  status_code     INTEGER NOT NULL DEFAULT 0,          -- 0 unset / 1 ok / 2 error
  status_message  TEXT NOT NULL DEFAULT '',
  provider        TEXT NOT NULL DEFAULT '',
  request_model   TEXT NOT NULL DEFAULT '',
  response_model  TEXT NOT NULL DEFAULT '',
  input_tokens    INTEGER,                              -- inclusive total（含 cache read），NULL = 未知
  output_tokens   INTEGER,
  cache_read_tokens INTEGER,
  cost_usd        REAL,                                 -- NULL = 算不出
  cost_source     TEXT NOT NULL DEFAULT '',             -- 'explicit' / 'pricing' / ''
  input_content   TEXT NOT NULL DEFAULT '',
  output_content  TEXT NOT NULL DEFAULT '',
  tool_name       TEXT NOT NULL DEFAULT '',
  tool_call_id    TEXT NOT NULL DEFAULT '',
  finish_reason   TEXT NOT NULL DEFAULT '',
  session_id      TEXT NOT NULL DEFAULT '',
  user_id         TEXT NOT NULL DEFAULT '',
  trace_state     TEXT NOT NULL DEFAULT '',
  attributes      TEXT NOT NULL DEFAULT '{}',           -- raw span attributes JSON
  events          TEXT NOT NULL DEFAULT '[]',
  links           TEXT NOT NULL DEFAULT '[]',
  resource        TEXT NOT NULL DEFAULT '{}',
  scope           TEXT NOT NULL DEFAULT '{}',           -- {name, version, attributes}
  UNIQUE (trace_id, span_id)
);
CREATE INDEX spans_start   ON spans(start_ns);
CREATE INDEX spans_kind    ON spans(kind, start_ns);
CREATE INDEX spans_model   ON spans(request_model, start_ns);
CREATE INDEX spans_session ON spans(session_id);
CREATE INDEX spans_user    ON spans(user_id);
```

寫入一律 `INSERT ... ON CONFLICT(trace_id, span_id) DO UPDATE SET <每一欄> = excluded.<欄>`。禁止 `INSERT OR REPLACE`（會換 rowid，破壞 FTS）。

### 5.2 `traces`（物化彙總）

```sql
CREATE TABLE traces (
  trace_id      TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  service_name  TEXT NOT NULL,
  start_ns      INTEGER NOT NULL,
  end_ns        INTEGER NOT NULL,
  duration_ms   REAL NOT NULL,
  span_count    INTEGER NOT NULL,
  llm_count     INTEGER NOT NULL,
  input_tokens  INTEGER,        -- NULL 若任一 llm/embedding span 的 input_tokens IS NULL
  output_tokens INTEGER,        -- 同上規則
  cost_usd      REAL,           -- NULL 若任一 llm/embedding span 的 cost_usd IS NULL
  has_error     INTEGER NOT NULL,
  session_id    TEXT NOT NULL,
  user_id       TEXT NOT NULL,
  models        TEXT NOT NULL   -- 去重、依首次出現順序、逗號接
);
CREATE INDEX traces_start   ON traces(start_ns DESC, trace_id DESC);
CREATE INDEX traces_error   ON traces(has_error, start_ns DESC);
CREATE INDEX traces_session ON traces(session_id);
CREATE INDEX traces_user    ON traces(user_id);
```

重算規則（每個 ingest batch 的同一 transaction 內，對受影響 trace_id 執行，UPSERT 覆寫每一欄）：

- 排序鍵一律 `(start_ns ASC, span_id ASC)`
- `name`、`service_name`：取最早的 `parent_span_id = ''` span；沒有 root 則取全 trace 最早 span
- `session_id`、`user_id`：依排序取第一個非空值，都空則 `''`
- `models`：對 `COALESCE(NULLIF(response_model,''), request_model)` 非空值，依排序去重後 `group_concat`（用 ordered subquery + `ROW_NUMBER() OVER (PARTITION BY model ORDER BY start_ns, span_id) = 1` 過濾再串）
- `start_ns = MIN`、`end_ns = MAX`、`duration_ms = (end-start)/1e6`
- `span_count = COUNT(*)`、`llm_count = SUM(kind='llm')`、`has_error = MAX(status_code=2)`
- `input_tokens` / `output_tokens` / `cost_usd`：只看 `kind IN ('llm','embedding')` 的 span；若該子集中任一值 NULL 則結果 NULL，否則 SUM；子集為空則 NULL

UI 顯示 NULL 為 `—`，不顯示部分加總。

### 5.3 `spans_fts`

```sql
CREATE VIRTUAL TABLE spans_fts USING fts5(
  input_content, output_content, name,
  content='spans', content_rowid='id'
);
CREATE TRIGGER spans_ai AFTER INSERT ON spans BEGIN
  INSERT INTO spans_fts(rowid, input_content, output_content, name)
  VALUES (new.id, new.input_content, new.output_content, new.name);
END;
CREATE TRIGGER spans_ad AFTER DELETE ON spans BEGIN
  INSERT INTO spans_fts(spans_fts, rowid, input_content, output_content, name)
  VALUES ('delete', old.id, old.input_content, old.output_content, old.name);
END;
CREATE TRIGGER spans_au AFTER UPDATE OF input_content, output_content, name ON spans BEGIN
  INSERT INTO spans_fts(spans_fts, rowid, input_content, output_content, name)
  VALUES ('delete', old.id, old.input_content, old.output_content, old.name);
  INSERT INTO spans_fts(rowid, input_content, output_content, name)
  VALUES (new.id, new.input_content, new.output_content, new.name);
END;
```

若 migration 在已有資料的 DB 上建立 FTS，緊接 `INSERT INTO spans_fts(spans_fts) VALUES('rebuild')`。

### 5.4 Open sequence 與 PRAGMA

1. 建 `DATA_DIR`（`0700`）；DB 檔以 `0600` 建（先 `os.OpenFile` 建空檔再交給 SQLite）。既有目錄權限寬於 `0700` 時 log warning。
2. 開 writer `*sql.DB`，DSN：`file:<path>?_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)`，`SetMaxOpenConns(1)`。
3. 若 `PRAGMA user_version = 0` 且 DB 為空：先 `PRAGMA auto_vacuum = INCREMENTAL`（必須在建表前）。
4. transaction 外執行 `PRAGMA journal_mode = WAL`。
5. migrations 為 Go slice，每條在自己的 transaction 內執行並同 transaction 更新 `user_version`。
6. 開 reader pool，DSN 加 `mode=ro&_pragma=query_only(ON)&_pragma=busy_timeout(5000)`，`SetMaxOpenConns(4)`。所有 UI 讀查詢與 `/sql` 走這個 pool。

`_pragma` 在 DSN 上保證每條實體連線都套用，不依賴一次性 `Exec`。

## 6. Ingest

### 6.1 HTTP 合約

- `POST /v1/traces`
- `Content-Type` 用 `mime.ParseMediaType` 解析後只接受 `application/x-protobuf` 與 `application/json`；其他或缺少回 415
- `Content-Encoding`：空或 `identity` 直接讀；`gzip` 解壓；其他值回 415
- 大小：wire body 以 `http.MaxBytesReader` 限 32 MiB；解壓後以 `io.LimitReader(gz, 32<<20 + 1)` 限 32 MiB，超過回 413；gzip reader 必 `Close`
- 設 `AUTH_TOKEN` 時要 `Authorization: Bearer <token>`，比對方式見 §10，不對回 401
- 成功：200，protobuf 回空 `ExportTraceServiceResponse` bytes、JSON 回 `{}`，`Content-Type` 同請求
- 失敗：400（解碼或驗證失敗）、413、415、500（DB），body 為 `google.rpc.Status{code, message}`，encoding 同請求（無法判斷 encoding 時用 JSON）
- 整批一個 transaction，不做 partial success

### 6.2 解碼與驗證

1. `proto.Unmarshal` 或 `protojson.Unmarshal` 進 `ExportTraceServiceRequest`。JSON 中 `traceId` / `spanId` 為 base64，由 protojson 自動還原成 `[]byte`；程式一律從 `[]byte` 轉 lowercase hex，絕不把 JSON 字串當 hex。
2. 驗證：trace id 恰 16 bytes 且非全零；span id 恰 8 bytes 且非全零；parent span id 為空或恰 8 bytes 且非全零；`start`、`end` 皆 `<= math.MaxInt64` 且 `end >= start`。任一違反整批 400。
3. `AnyValue` 遞迴轉 Go 值：string、bool、int64、float64、bytes（base64 string）、array（`[]any`）、kvlist（`map[string]any`，重複 key last-wins 並 warn）、未設值存 `nil`。非有限 float 轉字串 `"NaN"` / `"Infinity"` / `"-Infinity"`。
4. 攤成 `RawSpan{TraceID, SpanID, ParentSpanID, Name, StartNs, EndNs, StatusCode, StatusMessage, TraceState, Attrs, Events, Links, Resource, Scope}`。
5. 每個 RawSpan 經 `normalize.Span` 得 `store.Span`；llm / embedding 且 `cost_usd` 仍 NULL 者呼叫 `pricing.Cost`。
6. `store.InsertBatch`。

### 6.3 Token 數值規則

token 欄接受 `int64`，以及可無損轉 int64 的整數值 float 或 JSON 數字字串；`0` 有效。負數、小數、超過 `1e12` 的值忽略並 warn。

## 7. Normalize

### 7.1 kind 判定

依序檢查，**只有值被辨識時才停止**，未辨識的值繼續下一層：

| 層 | 屬性 | 對映 |
|---|---|---|
| 1 | `openinference.span.kind` | `LLM`→llm、`EMBEDDING`→embedding、`TOOL`→tool、`RETRIEVER` / `RERANKER`→retrieval、`AGENT`→agent、`CHAIN` / `GUARDRAIL` / `EVALUATOR` / `PROMPT`→other |
| 2 | `langfuse.observation.type` | `generation`→llm、`embedding`→embedding、`tool`→tool、`retriever`→retrieval、`agent`→agent、`span` / `chain` / `evaluator` / `guardrail` / `event`→other |
| 3 | `traceloop.span.kind` | `tool`→tool、`agent`→agent、`workflow` / `task`→other |
| 4 | `llm.request.type` | `chat` / `completion`→llm、`embedding`→embedding、`rerank`→retrieval |
| 5 | `gen_ai.operation.name` | `chat` / `generate_content` / `text_completion` / `llm_request`→llm、`embeddings`→embedding、`execute_tool`→tool、`retrieval` / `search_memory` / `vector_db_retrieve` / `rerank`→retrieval、`invoke_agent` / `create_agent` / `invoke_workflow` / `plan` / `agent_step`→agent |
| 6 | `ai.operationId`（Vercel legacy） | 以 `.doGenerate` / `.doStream` 結尾→llm、`ai.toolCall`→tool、`ai.embed` 開頭→embedding、`ai.generateText` / `ai.streamText` / `ai.generateObject` / `ai.streamObject`→agent |
| 7 | 啟發 | 有任一 model 候選且有任一 token 候選→llm；否則 other |

### 7.2 欄位對映

「第一個有效值」規則：string 欄只接受非空 string；token 欄依 §6.3；JSON 欄接受 string、`[]any`、`map`（後兩者 `json.Marshal`）。型別不符的候選跳過並 warn，不 panic、不 recover。

| 內部欄 | 候選（先到先贏） |
|---|---|
| provider | `gen_ai.provider.name`, `gen_ai.system`, `llm.provider`, `llm.system`, `ai.model.provider` |
| request_model | `gen_ai.request.model`, `llm.request.model_name`, `llm.model_name`, `langfuse.observation.model.name`, `ai.model.id`, `embedding.model_name` |
| response_model | `gen_ai.response.model`, `llm.response.model_name`, `ai.response.model` |
| input_tokens | `gen_ai.usage.input_tokens`, `gen_ai.usage.prompt_tokens`, `llm.token_count.prompt`, `ai.usage.promptTokens`, `ai.usage.tokens`（僅 kind=embedding）, Langfuse usage_details（見下） |
| output_tokens | `gen_ai.usage.output_tokens`, `gen_ai.usage.completion_tokens`, `llm.token_count.completion`, `ai.usage.completionTokens`, Langfuse usage_details |
| cache_read_tokens | `gen_ai.usage.cache_read.input_tokens`, `gen_ai.usage.cache_read_input_tokens`, `llm.token_count.prompt_details.cache_read`, Langfuse usage_details |
| cost_usd（explicit） | `langfuse.observation.cost_details` JSON 的 `total`；無 `total` 則對其餘數值 bucket 求和；再 `llm.cost.total`。有效則 `cost_source='explicit'` |
| input_content | 特例先行：`gen_ai.input.messages` 或 `gen_ai.system_instructions` 任一存在，組成 `{"system_instructions": ..., "messages": ...}`（缺的鍵省略）。否則依序：`langfuse.observation.input`, `input.value`, `traceloop.entity.input`, `ai.prompt.messages`, `ai.prompt`, `ai.value`, `ai.values`, `gen_ai.prompt`（單一字串）, 攤平 `gen_ai.prompt.{i}.*`, 攤平 `llm.input_messages.{i}.*`, 攤平 `llm.prompts.{i}.*`, `ai.toolCall.args`, `gen_ai.tool.call.arguments` |
| output_content | `gen_ai.output.messages`, `langfuse.observation.output`, `output.value`, `traceloop.entity.output`, `ai.response.text`, `ai.response.toolCalls`, `ai.response.object`, `ai.embedding`, `ai.embeddings`, `gen_ai.completion`（單一字串）, 攤平 `gen_ai.completion.{i}.*`, 攤平 `llm.output_messages.{i}.*`, 攤平 `llm.choices.{i}.*`, `ai.toolCall.result`, `gen_ai.tool.call.result` |
| tool_name | `gen_ai.tool.name`, `tool.name`, `ai.toolCall.name`, `traceloop.entity.name`（僅 kind=tool） |
| tool_call_id | `gen_ai.tool.call.id`, `tool.id`, `ai.toolCall.id` |
| finish_reason | `gen_ai.response.finish_reasons`（array 逗號接）, `gen_ai.response.finish_reason`, `llm.finish_reason`, `ai.response.finishReason` |
| session_id | `gen_ai.conversation.id`, `session.id`, `langfuse.session.id`, `traceloop.correlation.id` |
| user_id | `user.id`, `langfuse.user.id`, `enduser.id`, `gen_ai.user`, `llm.user` |
| service_name | resource `service.name` |

**Langfuse usage_details**：`langfuse.observation.usage_details` 是 JSON string，key 互斥。`input_tokens = input + Σ(其他 input_* 鍵)`，`cache_read_tokens = input_cached_tokens`（或 `cache_read_input_tokens`），`output_tokens = output + Σ(其他 output_* 鍵)`。內部 `input_tokens` 永遠是 inclusive total。

**攤平索引重建**：通用函式處理任意深度的 `prefix.{i}.rest.{j}.leaf` 路徑，重建成巢狀 `[]any` / `map[string]any` 後 `json.Marshal`。OpenInference 的 `message.contents.{j}.message_content.*` 與 `message.tool_calls.{j}.tool_call.*` 靠這個機制自然成形。

### 7.3 測試

table-driven，每組 fixture 是一份 attrs map 與期望的 `store.Span` 欄位：

- OTel GenAI 現行：chat（含 `gen_ai.input.messages` + `system_instructions`）、`execute_tool`、`embeddings`、`invoke_agent`
- OpenLLMetry：新版 `gen_ai.input.messages` chat、legacy 攤平 `gen_ai.prompt.{i}`（標註 legacy）、`traceloop.span.kind=tool`
- OpenInference：LLM（`message.contents` 形狀 + `tool_calls`）、completion API（`llm.prompts` / `llm.choices`）、RETRIEVER、`llm.cost.total`
- Langfuse v3：generation 含 `usage_details`（同時有 `input` 與 `input_cached_tokens`）、含 `cost_details`、tool
- Vercel AI SDK：現行 `gen_ai.*` 五種（agent root、chat、execute_tool、embeddings、rerank）；legacy `ai.generateText.doGenerate`、`ai.toolCall`、`ai.embed.doEmbed`（`ai.value` / `ai.embedding` / `ai.usage.tokens`）
- 邊界：未辨識 explicit kind 落到下一層、token `0` 有效、負數忽略、非有限 float、bytes attribute

## 8. Pricing

- 內嵌 `internal/pricing/model_prices.json`（LiteLLM 原檔），README 寫更新指令
- 啟動時載入；`PRICING_FILE` 設了則載該檔取代
- provider alias map（固定，Go 內）：`aws.bedrock`→`bedrock`、`gcp.vertex_ai`→`vertex_ai`、`gcp.gemini`→`gemini`、`azure.ai.openai`→`azure`、`azure.ai.inference`→`azure_ai`、`mistral_ai`→`mistral`、`anthropic`→`anthropic`、`openai`→`openai`、`cohere`→`cohere`、`groq`→`groq`；未列者原樣
- lookup 順序，每步先試 `response_model` 再 `request_model`：精確 → 去掉任何 `xxx/` 前綴後精確 → `alias(provider) + "/" + model`
- 只在 explicit cost 不存在時算。公式：

```
uncached = max(input_tokens - cache_read_tokens, 0)     # cache_read NULL 視為 0
cost = uncached * input_cost_per_token
     + cache_read * (cache_read_input_token_cost ?? input_cost_per_token)
     + output_tokens * output_cost_per_token
```

- `input_tokens` 或 `output_tokens` 任一 NULL → cost NULL。只有 cache count 沒有 input total → NULL，不猜。
- 算得出則 `cost_source='pricing'`
- 測試：精確、前綴剝除、Bedrock / Vertex / Azure / Mistral alias、cache_read、缺 token、查不到

## 9. 設定

| env | 預設 | 說明 |
|---|---|---|
| `PORT` | `4318` | 監聽所有介面 |
| `DATA_DIR` | `./data` | 不存在就建，`0700` |
| `RETENTION_DAYS` | `30` | `0` 表示不清 |
| `AUTH_TOKEN` | 空 | 空 = 無 auth，啟動時 log warning |
| `PRICING_FILE` | 空 | 覆蓋內建價格表 |

沒有設定檔，沒有 flag。`spanbox --version` 印版本。README 要求 token 以 CSPRNG 產生至少 32 bytes（給範例 `openssl rand -hex 32`）。

## 10. Auth

`AUTH_TOKEN` 有值時：

- 比對：兩邊先 `sha256.Sum256` 再 `subtle.ConstantTimeCompare`
- `/v1/traces`：`Authorization: Bearer <token>`，不對回 401
- UI cookie `spanbox_session`，值為 `hex(HMAC-SHA256(key=AUTH_TOKEN, msg="spanbox-ui-session"))`，不存原 token；`HttpOnly`、`SameSite=Strict`、`Path=/`；`r.TLS != nil` 或 `X-Forwarded-Proto: https` 時加 `Secure`。token 換掉舊 cookie 自然失效
- UI 路徑無有效 cookie 導向 `GET /login`；`POST /login` 只收 `application/x-www-form-urlencoded`、`MaxBytesReader` 4 KiB、失敗一律同 status 與 body
- `/healthz` 永遠免驗
- 所有 telemetry 值與 SQL 結果只能以一般 template data 進 text context，禁止 `template.HTML` / `template.JS` / inline `<script>` 塞資料；圖表資料一律由 JSON endpoint fetch

## 11. Web UI

全部 server-rendered，htmx 負責篩選與局部更新。單一 `base.html`，頂欄五個連結。時間一律 UTC，UI 標明。

| 路徑 | 內容 |
|---|---|
| `GET /` | trace 列表。篩選：時間範圍（15m / 1h / 24h / 7d / 自訂）、model（LIKE）、service、只看 error、最小 duration、session、user。欄：時間、name、service、models、spans、tokens in/out、cost、duration、status。排序與 cursor 皆 `(start_ns DESC, trace_id DESC)`，下一頁條件 `(start_ns, trace_id) < (?, ?)`，cursor 編碼兩值。htmx 篩選打 `GET /?partial=1` 回 `<tbody>` 片段 |
| `GET /traces/{trace_id}` | 左：span tree（巢狀 `<details open>`，每列 kind badge、name、model、tokens、duration bar）。右：點 span 後 htmx 載 `GET /spans/{trace_id}/{span_id}`：input / output（JSON 可解析就 pretty print、messages 依 role 分段，全部 text context）、attributes、events、links、resource、scope、status |
| `GET /dashboard` | 時間範圍同上。數字卡與每日 cost / tokens / error rate 從 `traces` 以 trace `start_ns` 分 UTC 日；error rate 分母是 trace 數。每日 p50 / p95 duration 從 `spans` 取 `kind='llm'`。per-model 表從 `spans` 取 `kind IN ('llm','embedding')`：calls / tokens / cost / avg duration。資料由 `GET /dashboard/data?range=` 回 JSON 給 uPlot |
| `GET /search?q=` | literal phrase：把 `"` 加倍後包成 FTS5 quoted phrase 以 bind 參數傳給 `MATCH`。回 span 列表（trace name、span name、kind、model、`snippet()` 片段） |
| `GET /sql`、`POST /sql` | 僅 `AUTH_TOKEN` 有值時存在，見 §11.2 |
| `GET /healthz` | `ok` |

### 11.1 百分位

nearest-rank：

```sql
WITH r AS (
  SELECT day, duration_ms,
         ROW_NUMBER() OVER (PARTITION BY day ORDER BY duration_ms) AS rn,
         COUNT(*)     OVER (PARTITION BY day) AS n
  FROM ...
)
SELECT day,
       MAX(CASE WHEN rn = (50*n+99)/100 THEN duration_ms END) AS p50,
       MAX(CASE WHEN rn = (95*n+99)/100 THEN duration_ms END) AS p95
FROM r GROUP BY day;
```

不用 `NTILE`。

### 11.2 `/sql` 防護

`modernc.org/sqlite` v1.59 未暴露 SQLite authorizer 與 `sqlite3_limit`（fact，已查源碼），因此採分層：

1. 走 reader pool（`mode=ro` + DSN `query_only(ON)` 每連線生效），且每次請求 `db.Conn(ctx)` 取專用 `*sql.Conn` 執行
2. Go 端 tokenizer（處理 `--` / `/* */` 註解、單雙引號與方括號字串）檢查：恰一個 statement（去尾端分號後不得再有分號）；首個 keyword 必須是 `SELECT`、`WITH` 或 `EXPLAIN`；任何位置出現 keyword `ATTACH`、`DETACH`、`PRAGMA`、`VACUUM`、`REINDEX`、`ALTER`、`CREATE`、`DROP`、`INSERT`、`UPDATE`、`DELETE`、`REPLACE`、`BEGIN`、`COMMIT`、`ROLLBACK`、`SAVEPOINT`、`RELEASE` 或函式名 `load_extension`、`readfile`、`writefile`、`fts5`（table-valued 形式）即拒絕 400
3. SQL 文字上限 16 KiB
4. `context.WithTimeout` 5s；modernc 在 ctx cancel 時呼叫 `sqlite3_interrupt`，測試須驗證一個長 recursive CTE 在時限內被中止且連線仍可用
5. 結果上限：1000 列、64 欄、單 cell 64 KiB（超過截斷並標記）、總回應 4 MiB
6. 頁面附 schema 說明與三個範例查詢

7. `AUTH_TOKEN` 為空時 `/sql` 一律回 404，body 說明「set AUTH_TOKEN to enable」；頂欄不顯示 SQL 連結

已知上限：沒有 authorizer 意味著防線是 tokenizer 而非 engine，單一 expression 仍可能在產生第一列前於 SQLite 內配置大量記憶體，只靠 5s interrupt 兜底；因此 `/sql` 只在有 `AUTH_TOKEN` 時存在、只給持有 token 的操作者用，README 註明。

## 12. Retention

trace-level，每小時一次，`RETENTION_DAYS=0` 跳過：

```sql
BEGIN;
CREATE TEMP TABLE del AS SELECT trace_id FROM traces WHERE start_ns < :cutoff;
DELETE FROM spans  WHERE trace_id IN (SELECT trace_id FROM del);
DELETE FROM traces WHERE trace_id IN (SELECT trace_id FROM del);
DROP TABLE del;
COMMIT;
PRAGMA incremental_vacuum(2000);   -- transaction 外
```

FTS 由 delete trigger 同步。不會留下跨 cutoff 的殘缺 trace。

## 13. 錯誤處理

- normalize：型別不符的候選跳過並 warn，該 span 仍以其他欄位與 raw JSON 入庫；不 `recover()`，真 bug 讓 request 500 並由 server-level recovery 記 stack
- 結構驗證失敗（§6.2 第 2 點）整批 400
- DB 寫入失敗 500，SDK 重送；UPSERT 保證 idempotent
- UI 查詢錯誤顯示在頁內
- SQL 頁：使用者 SQL 錯誤原樣顯示（text context）

## 14. 測試

- `go vet ./...`、`go test ./...` 全綠、`CGO_ENABLED=0 go build ./cmd/spanbox` 成功
- `otlp`：同一 fixture 以 protobuf、protojson、gzip-protobuf、gzip-JSON 四種送法解出相同 RawSpan；hex id 正確；malformed id、`end < start`、unsupported media type、解壓超限各一
- `normalize`：§7.3 清單
- `pricing`：§8 清單
- `store`：暫存目錄真 SQLite。InsertBatch 後 traces 每欄符合 §5.2 規則（含 NULL 傳染）；同 span 重送兩次只有一筆且內容更新、`id` 不變、FTS 查得到新內容查不到舊內容；retention 刪整個 trace；open sequence 在新 DB 與既有 DB 上都成功
- `web` e2e：`httptest` 起完整 server，POST 一份三 span trace（protobuf 與 JSON 各一次），GET `/`、`/traces/{id}`、`/spans/{t}/{s}`、`/dashboard/data`、`/search?q=`、POST `/sql` 斷言關鍵字；無 `AUTH_TOKEN` 時 `/sql` 回 404；開 `AUTH_TOKEN` 再跑：`/sql` 可用且拒絕 `ATTACH`、`PRAGMA`、多 statement，長 recursive CTE 5s 內中止：無 token 401、錯 token 401、login 成功後 cookie 可用、cookie 不含原 token；回應帶 CSP 與 nosniff
- 不做 UI 截圖測試

## 15. 打包

- `go build -ldflags "-s -w -X main.version=$(git describe --tags --always)" ./cmd/spanbox`
- Dockerfile：`golang:1.26-alpine` build → `FROM scratch`，複製 binary，`VOLUME /data`，`ENV DATA_DIR=/data`，`EXPOSE 4318`
- README：一段介紹、`docker run` 一行、五個 env var 表、bind mount 權限提醒、各 SDK 指向 spanbox 的 exporter 設定範例（Python OTel、Langfuse v3、Vercel AI SDK）、`/sql` 安全註記、更新價格表指令

## 16. 實作階段（範圍切分，非時程）

1. **骨架與 ingest**：config、store open sequence + schema + InsertBatch + traces 重算、otlp 解碼與驗證、normalize 核心（gen_ai.* 現行 + kind 全表）、`POST /v1/traces` 完整合約、trace 列表與 detail 頁、§14 的 `otlp` 與 `store` 測試與最小 ingest e2e。此時已可用。
2. **相容與 cost**：normalize 補齊四家 SDK 對映與全部 fixture、pricing、dashboard。
3. **收尾**：search、`/sql`、retention、auth、CSP、Dockerfile、README、完整 e2e。

## 17. 已知風險

- OTel GenAI semconv 全部仍為 Development，欄位可能再改名；對映表集中一處
- OpenLLMetry legacy 攤平形狀依常數推得，未在現行源碼驗證；fixture 標註 legacy
- `/sql` 無 engine 級 authorizer，見 §11.2
- SQLite 單寫者：寫入量遠超數十萬 span/天時就是換儲存層的時機
