# spanbox 設計文件

日期：2026-09-15
狀態：待審

## 1. 目標

單一 binary 的 LLM 監控平台。只收 OTLP，zero-config 起動，embedded SQLite，一個 port 同時收 trace 與出 UI。對標 Langfuse 的「看 trace、看 token/cost」核心，捨棄它需要 Postgres + ClickHouse + Redis + S3 的部署負擔。精神同 OpenObserve：單機 embedded 起步，保留換儲存層的路。

成功標準：

```
./spanbox            # 或 docker run -p 4318:4318 -v ./data:/data spanbox
```

之後任何 OTel SDK 把 exporter endpoint 指到 `http://host:4318`，打開 `http://host:4318/` 就看得到 trace、span 原文、token、cost、latency。零必填環境變數。

## 2. 非目標（v1 明確不做）

- 多租戶、多使用者帳密、RBAC
- Prompt management、datasets、playground、evals/scores 寫入
- gRPC OTLP、OTLP metrics/logs
- 叢集、S3、Parquet（儲存層走 interface，日後可換，現在不付）
- 自有 SDK、Langfuse ingestion API 相容
- 內容截斷或遮罩（`REDACT` 之類日後再加）

## 3. 已定決策

| 項目 | 決定 |
|---|---|
| 規模 | 單機、每天數萬到數十萬 span、保留數週 |
| Ingest | OTLP/HTTP，`protobuf` 與 `JSON` 兩種 encoding，同一 `POST /v1/traces` |
| 語言 | Go，`CGO_ENABLED=0`，SQLite 用 `modernc.org/sqlite` |
| 儲存 | SQLite 單檔，WAL，FTS5 全文，JSON 欄存 raw attributes |
| UI | `html/template` + htmx，vendor 一支 uPlot，無 node toolchain，`embed.FS` 打包 |
| Auth | 選填 `AUTH_TOKEN`；設了則 ingest 走 Bearer、UI 走 cookie login；沒設就全開並在啟動 log 警告 |
| 屬性相容 | 標準 `gen_ai.*` 為主，加一張 Go map normalize 表對映 OpenLLMetry / OpenInference / Langfuse v3 / Vercel AI SDK；raw attributes 全存 |
| Span 分類 | ingest 時算 `kind` 欄：`llm` / `embedding` / `tool` / `retrieval` / `agent` / `other` |
| 內容 | prompt / completion 全存不截斷，靠 retention 控體積 |
| 價格表 | 內建 LiteLLM `model_prices_and_context_window.json`，`PRICING_FILE` 可覆蓋，對不到就 cost 留空 |
| 設定 | 五個選填 env var，見 §9 |
| 打包 | binary + `FROM scratch` Dockerfile |
| Module | `github.com/Ray0907/spanbox`，repo `/private/tmp/spanbox` |

## 4. 架構

```
OTel SDK ──POST /v1/traces──▶ ingest ──▶ normalize ──▶ store (SQLite)
                                                          ▲
browser ──GET /,/traces/{id},/dashboard,/search,/sql──▶ web ┘
                                                    retention goroutine
```

單一 process、單一 `http.Server`、單一 SQLite 檔 `$DATA_DIR/spanbox.db`。

### 4.1 套件

```
cmd/spanbox/main.go          組裝、flag/env、啟動 server 與 retention
internal/config              讀 env，給預設值
internal/otlp                解 OTLP proto/JSON 成內部 []Span（含 gzip）
internal/normalize           attributes → 內部欄位 + kind 判定（純函式、table-driven）
internal/pricing             embed 價格表、lookup、算 cost
internal/store               SQLite schema、migrate、Insert、查詢
internal/web                 handlers、templates/、static/（htmx、uPlot、css）
Dockerfile
README.md
```

每個 internal 套件對外只暴露幾個函式與 struct。`store` 是唯一碰 SQL 的地方。`normalize` 不 import 任何 OTel 型別，只吃 `map[string]any`，方便 table-driven 測試。

### 4.2 依賴

- `modernc.org/sqlite`
- `go.opentelemetry.io/proto/otlp`（generated types）
- `google.golang.org/protobuf`（proto + protojson）

其餘 stdlib。前端 `htmx.min.js`、`uPlot.min.js`、`uPlot.min.css` vendor 進 `internal/web/static/`。

## 5. 資料模型

### 5.1 `spans`

| 欄 | 型別 | 說明 |
|---|---|---|
| trace_id | TEXT | hex，PK 之一 |
| span_id | TEXT | hex，PK 之一 |
| parent_span_id | TEXT | hex 或空 |
| name | TEXT | span name |
| kind | TEXT | `llm` / `embedding` / `tool` / `retrieval` / `agent` / `other` |
| service_name | TEXT | resource `service.name`，無則 `unknown` |
| start_ns | INTEGER | unix nano |
| end_ns | INTEGER | unix nano |
| duration_ms | REAL | 算好 |
| status_code | INTEGER | OTLP 0 unset / 1 ok / 2 error |
| status_message | TEXT | |
| provider | TEXT | normalize 後 |
| request_model | TEXT | |
| response_model | TEXT | |
| input_tokens | INTEGER | NULL 表示未知 |
| output_tokens | INTEGER | |
| cache_read_tokens | INTEGER | |
| cost_usd | REAL | NULL 表示算不出 |
| input_content | TEXT | 原文，JSON 或 string 照存 |
| output_content | TEXT | |
| tool_name | TEXT | |
| tool_call_id | TEXT | |
| finish_reason | TEXT | 多個以逗號接 |
| session_id | TEXT | conversation / session |
| user_id | TEXT | |
| attributes | TEXT | raw span attributes JSON |
| events | TEXT | raw span events JSON |
| resource | TEXT | raw resource attributes JSON |

`PRIMARY KEY (trace_id, span_id)`，`INSERT OR REPLACE` 處理重送。
索引：`(start_ns)`、`(kind, start_ns)`、`(request_model, start_ns)`、`(session_id)`、`(user_id)`。

### 5.2 `traces`（物化彙總，方便列表）

| 欄 | 說明 |
|---|---|
| trace_id | PK |
| name | root span name；root 未到時取最早 span 的 name，root 到了覆蓋 |
| service_name | |
| start_ns / end_ns / duration_ms | min / max 跨 span |
| span_count | |
| llm_count | kind=llm 的數量 |
| input_tokens / output_tokens / cost_usd | sum |
| has_error | 任一 span status_code=2 |
| session_id / user_id | 第一個非空值 |
| models | 去重後逗號接，供列表顯示與 LIKE 篩選 |

每個 ingest batch 在同一 transaction 內，對受影響的 trace_id 用 `INSERT ... ON CONFLICT DO UPDATE` 從 `spans` 重算（`SELECT ... GROUP BY trace_id WHERE trace_id IN (...)`）。重算比增量加總簡單且對重送 idempotent；一個 batch 通常只碰幾十個 trace，成本可忽略。

索引：`(start_ns)`、`(session_id)`、`(user_id)`、`(has_error, start_ns)`。

### 5.3 `spans_fts`

FTS5 external-content table 對 `spans(input_content, output_content, name)`，rowid 對應 `spans.rowid`，用三個 trigger 同步 insert / update / delete。查詢 `MATCH` 後 join 回 `spans`。

### 5.4 PRAGMA

`journal_mode=WAL`、`synchronous=NORMAL`、`busy_timeout=5000`、`auto_vacuum=INCREMENTAL`（建檔時設）。寫入用單一 `*sql.DB` 且 `SetMaxOpenConns(1)` 給寫路徑；讀另開一個 `mode=ro` 的連線池。

Schema 版本放 `PRAGMA user_version`，migrate 用 Go slice 依序執行。

## 6. Ingest

### 6.1 HTTP

- `POST /v1/traces`
- `Content-Type: application/x-protobuf` → `proto.Unmarshal` 成 `ExportTraceServiceRequest`
- `Content-Type: application/json` → `protojson.Unmarshal`
- `Content-Encoding: gzip` → 先解壓
- body 上限 32 MiB
- 成功回 200 與空 `ExportTraceServiceResponse`（同 encoding）
- 解析失敗 400，DB 失敗 500；整個 batch 一個 transaction，不部分成功
- 設 `AUTH_TOKEN` 時要 `Authorization: Bearer <token>`，否則 401

### 6.2 流程

1. 走 resource → scope → span 三層，攤平成 `[]RawSpan{Resource map, Attrs map, Events []..., 原生欄位}`。attribute `AnyValue` 轉成 Go 原生型別（string / int64 / float64 / bool / []any / map）。
2. 每個 `RawSpan` 丟 `normalize.Span(raw)` 得到 `store.Span`。
3. 對每個 llm / embedding span 呼叫 `pricing.Cost(provider, request_model, response_model, tokens)` 填 `cost_usd`。
4. `store.InsertBatch(spans)`。

## 7. Normalize

### 7.1 kind 判定順序

先看明確欄位，找到就停：

1. `openinference.span.kind`：`LLM`→llm、`EMBEDDING`→embedding、`TOOL`→tool、`RETRIEVER` / `RERANKER`→retrieval、`AGENT`→agent、其餘→other
2. `langfuse.observation.type`：`generation`→llm、`embedding`→embedding、`tool`→tool、`retriever`→retrieval、`agent`→agent、其餘→other
3. `traceloop.span.kind`：`tool`→tool、`agent`→agent、`workflow` / `task`→other；若無此欄但有 `llm.request.type`：`chat` / `completion`→llm、`embedding`→embedding、`rerank`→retrieval
4. `gen_ai.operation.name`：`chat` / `generate_content` / `text_completion`→llm、`embeddings`→embedding、`execute_tool`→tool、`retrieval` / `search_memory`→retrieval、`invoke_agent` / `create_agent` / `invoke_workflow` / `plan`→agent、其餘→other
5. `ai.operationId`（Vercel legacy）：以 `.doGenerate` / `.doStream` 結尾→llm、`ai.toolCall`→tool、`ai.embed` 開頭→embedding、`ai.generateText` / `ai.streamText`→agent
6. 啟發：有任一 model 屬性且有任一 token 屬性→llm；否則 other

### 7.2 欄位對映（依序取第一個非空）

| 內部欄 | 候選屬性（先到先贏） |
|---|---|
| provider | `gen_ai.provider.name`, `gen_ai.system`, `llm.provider`, `llm.system`, `ai.model.provider` |
| request_model | `gen_ai.request.model`, `llm.request.model_name`, `llm.model_name`, `langfuse.observation.model.name`, `ai.model.id`, `embedding.model_name` |
| response_model | `gen_ai.response.model`, `llm.response.model_name`, `ai.response.model` |
| input_tokens | `gen_ai.usage.input_tokens`, `gen_ai.usage.prompt_tokens`, `llm.token_count.prompt`, `ai.usage.promptTokens`, `langfuse.observation.usage_details` JSON 內 `input` 或 `prompt_tokens` 或 `input_tokens` |
| output_tokens | `gen_ai.usage.output_tokens`, `gen_ai.usage.completion_tokens`, `llm.token_count.completion`, `ai.usage.completionTokens`, usage_details 內 `output` / `completion_tokens` / `output_tokens` |
| cache_read_tokens | `gen_ai.usage.cache_read.input_tokens`, `gen_ai.usage.cache_read_input_tokens`, `llm.token_count.prompt_details.cache_read`, usage_details 內 `input_cached_tokens` / `cache_read_input_tokens` |
| input_content | `gen_ai.input.messages`, `gen_ai.system_instructions`+`gen_ai.input.messages` 合併, `langfuse.observation.input`, `input.value`, `traceloop.entity.input`, `ai.prompt.messages`, `ai.prompt`, `gen_ai.prompt`（單一字串）, 攤平 `gen_ai.prompt.{i}.role/content` 重組成 JSON array, 攤平 `llm.input_messages.{i}.message.role/content` 重組, `ai.toolCall.args`, `gen_ai.tool.call.arguments` |
| output_content | `gen_ai.output.messages`, `langfuse.observation.output`, `output.value`, `traceloop.entity.output`, `ai.response.text`, `ai.response.toolCalls`, `ai.response.object`, `gen_ai.completion`, 攤平 `gen_ai.completion.{i}.*` 重組, 攤平 `llm.output_messages.{i}.*` 重組, `ai.toolCall.result`, `gen_ai.tool.call.result` |
| tool_name | `gen_ai.tool.name`, `tool.name`, `ai.toolCall.name`, `traceloop.entity.name`（僅 kind=tool 時） |
| tool_call_id | `gen_ai.tool.call.id`, `tool.id`, `ai.toolCall.id` |
| finish_reason | `gen_ai.response.finish_reasons`（array 逗號接）, `gen_ai.response.finish_reason`, `llm.finish_reason`, `ai.response.finishReason` |
| session_id | `gen_ai.conversation.id`, `session.id`, `langfuse.session.id`, `traceloop.correlation.id` |
| user_id | `user.id`, `langfuse.user.id`, `enduser.id`, `gen_ai.user`, `llm.user` |
| service_name | resource `service.name` |

攤平索引屬性（`prefix.{i}.suffix`）用一個通用函式收集成 `[]map[string]any` 再 `json.Marshal`。輸入若是 `[]any` / `map` 也 `json.Marshal` 成字串存。

所有對映失敗都不擋 ingest；raw `attributes` 永遠可以 SQL 直查。

### 7.3 測試

`normalize` 為 table-driven test：每家 SDK 一組 fixture attributes（取自研究時看到的實際欄位），斷言 kind 與各欄位。這是整個專案最需要測試的地方。

## 8. Pricing

- 內嵌 `internal/pricing/model_prices.json`（LiteLLM 原檔，MIT），README 寫更新指令
- 啟動時載入；`PRICING_FILE` 設了則載該檔取代（不合併）
- lookup 順序：`response_model` 精確 → `request_model` 精確 → 兩者各自去掉 `provider/` 前綴後精確 → 以 `provider/model` 組合查（LiteLLM 鍵常帶 `anthropic/`、`vertex_ai/`、`bedrock/` 前綴）
- `cost = input*input_cost_per_token + output*output_cost_per_token + cache_read*cache_read_input_token_cost`；cache_read 有值但表沒該欄位時按 input 價算
- 查不到回 `nil`，欄位留 NULL，UI 顯示 `—`

## 9. 設定

| env | 預設 | 說明 |
|---|---|---|
| `PORT` | `4318` | 監聽所有介面 |
| `DATA_DIR` | `./data` | 不存在就建 |
| `RETENTION_DAYS` | `30` | `0` 表示不清 |
| `AUTH_TOKEN` | 空 | 空 = 無 auth，啟動時 log warning |
| `PRICING_FILE` | 空 | 覆蓋內建價格表 |

沒有設定檔，沒有 flag。`spanbox --version` 印版本。

## 10. Auth

`AUTH_TOKEN` 有值時：

- `/v1/traces`：`Authorization: Bearer <token>`，不對就 401
- UI 路徑：cookie `spanbox_token` 等於 token 即放行；否則導向 `/login`，表單 POST token，對了就設 HttpOnly cookie（不設 Secure，因為多數 self-host 走 http 或 reverse proxy 終止 TLS）
- `/healthz` 永遠免驗
- 比對用 `subtle.ConstantTimeCompare`

## 11. Web UI

全部 server-rendered，htmx 負責篩選與局部更新。單一 `base.html` layout，頂欄五個連結。

| 路徑 | 內容 |
|---|---|
| `GET /` | trace 列表。篩選：時間範圍（15m / 1h / 24h / 7d / 自訂）、model（LIKE）、service、只看 error、最小 duration、session、user。欄：時間、name、service、models、spans、tokens in/out、cost、duration、status。分頁 cursor 用 `start_ns`。htmx 篩選打 `GET /?partial=1` 回 `<tbody>` 片段 |
| `GET /traces/{trace_id}` | 左：span tree（巢狀 `<details open>`，每列 kind badge、name、model、tokens、duration bar）。右：點選 span 後 htmx 載 `GET /spans/{trace_id}/{span_id}` 顯示 input / output（JSON 自動 pretty print、messages 依 role 分段）、attributes 表、events、status |
| `GET /dashboard` | 時間範圍同上。四張數字卡：總 cost、總 tokens、trace 數、error rate。四張 uPlot：每天 cost、每天 tokens in/out、每天 p50/p95 duration（llm span）、每天 error rate。一張表：依 model 的 calls / tokens / cost / avg duration。資料由 `GET /dashboard/data?range=` 回 JSON 給 uPlot |
| `GET /search?q=` | FTS5 MATCH，回 span 列表（trace name、span name、kind、model、命中片段用 `snippet()`），點進 trace |
| `GET /sql`、`POST /sql` | textarea + 執行。走 read-only 連線、`PRAGMA query_only=1`、context timeout 5s、最多回 1000 列。回表格。頁面附 schema 說明與三個範例查詢 |
| `GET /healthz` | `ok` |

p50 / p95 用 SQLite 視窗函式（`NTILE` 或 `ROW_NUMBER` 配 count）在每天 bucket 內算，不做近似。

CSS 一支手寫，約 200 行，深淺色跟系統。不引入 CSS framework。

## 12. Retention

啟動時與之後每小時一次：

```sql
DELETE FROM spans  WHERE start_ns < ?;
DELETE FROM traces WHERE start_ns < ?;
PRAGMA incremental_vacuum(2000);
```

FTS 由 delete trigger 同步。`RETENTION_DAYS=0` 跳過。

## 13. 錯誤處理

- ingest：任何單一 span 的 normalize 例外都吞掉（log warn），該 span 以 kind=other 與 raw attributes 入庫，不丟 batch
- DB 寫入失敗回 500，SDK 會重送；`INSERT OR REPLACE` 保證重送 idempotent
- UI 查詢錯誤顯示在頁內，不 500 整頁
- SQL 頁：使用者 SQL 錯誤原樣顯示

## 14. 測試

- `go vet ./...`、`go test ./...` 全綠、`CGO_ENABLED=0 go build ./cmd/spanbox` 成功
- `normalize`：table-driven，五家 SDK 各至少一組 llm fixture，另加 tool / embedding / retrieval / Langfuse usage_details JSON 各一
- `pricing`：前綴剝除、cache_read、查不到
- `store`：暫存目錄真 SQLite，InsertBatch 後 traces 物化正確、重送不重複、FTS 查得到、retention 刪對
- `web` e2e 一支：`httptest` 起完整 server，POST 一份 OTLP JSON（含 3 個 span 的 trace），GET `/`、`/traces/{id}`、`/dashboard/data`、`/search?q=`、POST `/sql` 斷言關鍵字出現；再開 `AUTH_TOKEN` 跑一次斷言 401 與 login 流程
- 不做 UI 截圖測試

## 15. 打包

- `go build -ldflags "-s -w -X main.version=$(git describe --tags --always)" ./cmd/spanbox`
- Dockerfile：`golang:1.26-alpine` build stage → `FROM scratch`，複製 binary，`VOLUME /data`，`ENV DATA_DIR=/data`，`EXPOSE 4318`
- README：一段介紹、`docker run` 一行、五個 env var 表、各 SDK 指向 spanbox 的 exporter 設定範例（Python OTel、Langfuse v3、Vercel AI SDK）、更新價格表指令

## 16. 實作階段（範圍切分，非時程）

1. **骨架與 ingest**：config、store schema + InsertBatch、otlp 解碼、normalize 核心（gen_ai.* 與 kind）、`POST /v1/traces`、trace 列表與 detail 頁。此時已可用。
2. **相容與 cost**：normalize 補齊四家 SDK 對映與 fixture 測試、pricing、dashboard。
3. **收尾**：search、sql、retention、auth、Dockerfile、README、e2e 測試。

## 17. 已知風險

- OTel GenAI semconv 全部仍為 Development，欄位可能再改名；對映表集中在一個 Go map，改名只動一處
- OpenLLMetry 舊版攤平 `gen_ai.prompt.{i}.*` 形狀是依常數推得，未在現行源碼驗證；fixture 標註為 legacy
- SQLite 單寫者：寫入量遠超數十萬 span/天時 batch insert 仍夠，但到那規模就是換儲存層的時機
