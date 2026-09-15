# 設計審查 actionable findings

1. **blocker · §5.1、§5.3、§13**
   - **問題：** `spans` 的複合 `PRIMARY KEY` 在 SQLite rowid table 不會自動令兩欄 `NOT NULL`；同時 `INSERT OR REPLACE` 會刪除舊 row 再插入新 row，可能更換 `rowid`，且其隱含 delete trigger 預設不一定執行。這會讓以 `spans.rowid` 為 document id 的 external-content FTS5 留下舊索引項目，重送後即可產生錯誤搜尋結果或索引不一致。
   - **具體修正：** 將 `spans` 改為 `id INTEGER PRIMARY KEY`、`trace_id TEXT NOT NULL CHECK(length(trace_id)=32)`、`span_id TEXT NOT NULL CHECK(length(span_id)=16)`、`UNIQUE(trace_id, span_id)`；FTS5 使用 `content_rowid='id'`。寫入改成 `INSERT ... ON CONFLICT(trace_id, span_id) DO UPDATE SET ...`，不得使用 `REPLACE`，以保留 `id` 並讓 `AFTER UPDATE` trigger 正常同步。

2. **blocker · §11（`/sql`）**
   - **問題：** `mode=ro` 只保護 main database；`ATTACH` 仍可讀取 process 有權限讀取的其他 SQLite 檔，並可能建立可寫 attached database。`PRAGMA query_only=1` 又是 connection-local 且可被 SQL 本身關閉；若先在 `*sql.DB` 執行 PRAGMA，再從 pool 執行查詢，兩次也未必使用同一實體 connection。因此目前設計不是安全的唯讀 SQL sandbox。
   - **具體修正：** `/sql` 每次在同一個專用 `*sql.Conn` 上設定 `query_only=ON` 並執行查詢，且在 prepare 階段用 SQLite authorizer 明確拒絕 `ATTACH`、`DETACH`、所有寫入/DDL、`PRAGMA`、transaction control 與 extension loading，只允許一個 read-only statement；另以 `sqlite3_stmt_readonly()` 或 driver 等價能力做第二道檢查。若 `modernc.org/sqlite` 所選版本無法提供 authorizer/readonly-statement hook，該版本不得啟用 `/sql`。

3. **major · §5.3**
   - **問題：** 「三個 trigger」不足以讓實作者無歧義地寫對 external-content FTS5；update/delete 必須向 FTS table 寫入特殊的 `'delete'` command 與完整 old indexed values，建立 trigger 也不會替既有 `spans` 建索引。
   - **具體修正：** 在 spec 放入完整 migration SQL：`AFTER INSERT` 寫入 `(rowid, input_content, output_content, name)`；`AFTER DELETE` 寫入 `(spans_fts, rowid, ...old values...) VALUES('delete', ...)`；`AFTER UPDATE OF input_content, output_content, name` 先 delete old、再 insert new。若 migration 建立 FTS 時 `spans` 可能已有資料，再執行 `INSERT INTO spans_fts(spans_fts) VALUES('rebuild')`。

4. **major · §5.2**
   - **問題：** trace 重算中 `name`、`service_name`、「第一個非空」`session_id`/`user_id` 與 `models` 的選取順序未定義；用 `MIN()`/`MAX()` 不等於「最早」，`group_concat(DISTINCT ...)` 的順序也不穩定。不同 SQL 寫法會產生不同 materialized trace。
   - **具體修正：** 明定排序鍵為 `(start_ns, span_id)`：`name` 取最早的 `parent_span_id=''` span，沒有才取全 trace 最早 span；`service_name` 同一規則；session/user 取最早非空值；models 先在 ordered subquery 對 `COALESCE(NULLIF(response_model,''), request_model)` 去重，再 `group_concat`。重算的 UPSERT 必須覆寫每一個彙總欄位。

5. **major · §5.2**
   - **問題：** nullable token/cost 的 trace 聚合語意未定義。SQLite `SUM()` 會忽略 `NULL`，因此一個已知 cost 加一個未知 cost 會顯示成看似完整、實際只是部分的總額。
   - **具體修正：** 明定並實作完整性規則；最小安全規則是：只要任一應計價的 `llm`/`embedding` span 的 `cost_usd IS NULL`，trace `cost_usd` 就是 `NULL`，否則才 `SUM(cost_usd)`。tokens 也需明定是已知 subtotal 或全有值才顯示，UI 不得把 subtotal 當完整總數。

6. **major · §12**
   - **問題：** retention 先按 span 的 `start_ns` 刪除，再按 trace 的舊 `start_ns` 刪除，會讓跨 cutoff 的 trace 出現兩種錯誤：較新的 span 尚存但 trace summary 被刪除，或只刪了舊 span 卻沒有重算仍存在的 trace summary。
   - **具體修正：** 選定 trace-level retention 並在一個 transaction 內先收集 `traces.start_ns < cutoff` 的 trace ids、依 ids 刪除全部 spans、再刪 traces；這是最簡單且不會留下 stale summary 的語意。`incremental_vacuum` 放在 transaction commit 後執行。

7. **major · §5.4**
   - **問題：** PRAGMA 的生命週期與 migration 順序未定義。`journal_mode=WAL` 不能在一般 migration transaction 內切換；`auto_vacuum=INCREMENTAL` 必須在首次建表前設定，既有 DB 改值還需要 `VACUUM`；`busy_timeout`、`synchronous`、`query_only` 等是 connection-local，對 `*sql.DB` 執行一次不會設定 pool 之後建立的所有 connection。
   - **具體修正：** 明定 open sequence：先開 writer；新 DB 在建表前設 `auto_vacuum=INCREMENTAL`；在 migration transaction 外設 WAL；每個 migration 與 `user_version` 在同一 transaction；最後才開 read pool。透過 modernc DSN `_pragma` 或 connection hook 對每個實體 connection 設定其 per-connection PRAGMA；既有 DB 若要轉 auto-vacuum，寫獨立 migration 並執行一次 `VACUUM`。

8. **major · §11（dashboard）**
   - **問題：** `NTILE(100)` 不能正確處理每天少於 100 筆的 p95，因為 tile 95 可能根本不存在；p50/p95 採 nearest-rank 還是 interpolation 也未定義。
   - **具體修正：** 明定 nearest-rank，使用同一個 CTE 計算 `ROW_NUMBER() OVER (PARTITION BY day ORDER BY duration_ms)` 與 `COUNT(*) OVER (PARTITION BY day)`；p50 取 `rn=(50*n+99)/100`，p95 取 `rn=(95*n+99)/100`，再依 day aggregate。不要使用 `NTILE(100)`。

9. **major · §11（trace list）**
   - **問題：** 只用 `start_ns` 當 cursor，遇到同 nanosecond 的多筆 trace 會跳筆或重複；`(start_ns)` 單欄索引也無法唯一續頁。
   - **具體修正：** 排序與 cursor 都改為 `(start_ns DESC, trace_id DESC)`，cursor 編碼兩個值，下一頁條件用 row-value comparison `(start_ns, trace_id) < (?, ?)`，索引改為 `(start_ns DESC, trace_id DESC)`。

10. **major · §6.1**
   - **問題：** 32 MiB 上限沒有說是壓縮前或解壓後。只在 gzip 前用 `MaxBytesReader` 仍可被小型 gzip bomb 解壓成大量記憶體；未知或串接的 `Content-Encoding` 也沒有處理規則。
   - **具體修正：** 同時限制 wire body 與 decompressed body：先以 `http.MaxBytesReader` 限制壓縮輸入，再以 `io.LimitReader(gzipReader, 32<<20+1)` 限制解壓輸出並在超限時回 413；只接受空/`identity` 或單一 `gzip`，其他 encoding 回 415，且務必關閉 gzip reader。

11. **major · §6.1**
   - **問題：** Content-Type 比對與錯誤回應格式未定義。直接字串相等會拒絕合法參數；OTLP/HTTP client 也預期 non-2xx body 使用請求同 encoding 的 `google.rpc.Status`，而不是任意文字。
   - **具體修正：** 用 `mime.ParseMediaType` 後只接受 `application/x-protobuf`、`application/json`；缺少/不支援回 415。成功時 protobuf 回空 message bytes、JSON 回 `{}`，並設定對應 Content-Type；400/413/415/500 以同 encoding 回 `google.rpc.Status`。所有 response body 與 status code 寫法放入 §6.1。

12. **major · §6.2**
   - **問題：** OTLP protobuf 的 `trace_id`/`span_id` 是 bytes；proto JSON mapping 則以 base64 表示 bytes，不是 hex。若 JSON decoder 將字串直接當 hex，會存錯 ID。規格也沒有驗證空值、長度、全零 ID、`end < start`，而 OTLP timestamps 是 `uint64`、SQLite INTEGER 是 signed 64-bit。
   - **具體修正：** 一律先由 `proto.Unmarshal`/`protojson.Unmarshal` 得到 `[]byte`，再轉 lowercase hex；要求 trace id 恰為 16 bytes、span/非空 parent id 恰為 8 bytes且非全零。寫 SQLite 前驗證 timestamps `<= math.MaxInt64` 且 `end >= start`；因設計不做 partial success，任一結構錯誤就整批回 400。

13. **major · §6.2、§5.1**
   - **問題：** `AnyValue` 清單漏了 `bytes_value`，也未定義 nested array/kvlist、重複 key、nil value、`NaN`/`±Inf`。`encoding/json` 無法 marshal non-finite float，因此「raw attributes 永遠可入庫」目前不成立。
   - **具體修正：** 明定遞迴轉換所有 oneof：string、bool、int64、float64、bytes、array、kvlist；bytes 存 base64 string。重複 key 採 last-wins 並記 warn，nil 存 `null`；non-finite float 轉為字串 `"NaN"`/`"Infinity"`/`"-Infinity"`（或明定整批 400），並為這些案例加 decoder test。

14. **major · §5.1、§6.2、§11**
   - **問題：** 成功標準宣稱可看「span 原文」，但資料模型丟棄 instrumentation scope、scope attributes/schema URL、span links、trace state、flags 與 dropped counts；detail 頁也沒有顯示 links。實作者無法判斷「原文」是完整 span 還是只有 attributes/events/resource。
   - **具體修正：** Phase 1 至少新增 `scope`、`links` JSON 與 `trace_state`、`flags` 欄並在 detail 顯示；若確定不保存其餘 OTLP span 欄位，將 §1 的承諾明確改為「raw attributes/events/resource」，不可稱完整 span 原文。

15. **major · §7.1**
   - **問題：** explicit kind 的未知值目前直接 `other` 並停止 fallback，會讓 typo 或新版值遮蔽後面的可靠 `gen_ai.operation.name`。另外目前 OpenLLMetry/Vercel GenAI emission 會出現 `llm_request`、`vector_db_retrieve`、`agent_step`、`rerank`，現表會把它們全判成 `other`。
   - **具體修正：** 只對「已識別」的 explicit kind 停止；未知值繼續下一層。補 operation aliases：`llm_request→llm`、`vector_db_retrieve`/`rerank→retrieval`、`agent_step→agent`；保留已識別但不屬內部種類的 `CHAIN`/`workflow`/`task` 等為 `other`。

16. **major · §7.2（OpenInference）**
   - **問題：** 現行 OpenInference 的 structured messages 不只 `llm.*_messages.{i}.message.role/content`，還有 `message.contents.{j}.message_content.*`、`message.tool_calls.{j}.tool_call.*`；completion API 另用 `llm.prompts.{i}.prompt.text` 與 `llm.choices.{i}.completion.text`。現有通用一層 index collector 會漏內容或產生無法依 role 顯示的扁平物件。
   - **具體修正：** collector 要支援任意多層 numeric index 並重建 nested arrays/maps；把 `llm.prompts.*`、`llm.choices.*` 加入 input/output candidates。fixture 至少覆蓋 current `message.contents`、tool call 與 prompt/choice 三種形狀。

17. **major · §7.1、§7.2（Vercel AI SDK）**
   - **問題：** legacy embed spans 使用 `ai.value`/`ai.values`、`ai.embedding`/`ai.embeddings` 與 `ai.usage.tokens`，目前全部漏掉；current `@ai-sdk/otel` 主要送 `gen_ai.*`，且會送上項所列 `agent_step`/`rerank`。只測 `ai.prompt` 的 fixture 無法證明「Vercel AI SDK 相容」。
   - **具體修正：** legacy mapping 加入 `ai.value`/`ai.values` 作 input、`ai.embedding`/`ai.embeddings` 作 output、`ai.usage.tokens` 作 embedding input tokens；同時增加一組 current `gen_ai.*` Vercel fixture，分別驗證 agent root、LLM child、tool、embed、rerank。

18. **major · §7.2、§8（Langfuse v3 / OpenInference）**
   - **問題：** 規格忽略來源已提供的 cost。Langfuse v3 可送 `langfuse.observation.cost_details`，OpenInference 可送 `llm.cost.total`；目前即使已有精確成本，只要 LiteLLM pricing lookup 失敗仍會存 `NULL`。
   - **具體修正：** normalize 增加 explicit `cost_usd` candidates：`langfuse.observation.cost_details.total` 優先，其次對沒有 `total` 的 mutually-exclusive cost buckets 求和，再看 `llm.cost.total`；只有完全沒有有效 explicit cost 時才呼叫 `pricing.Cost`。

19. **major · §7.2**
   - **問題：** 「第一個非空」對 heterogeneous `AnyValue` 沒有型別規則；token 值 `0` 是有效值，不可被當 empty。負數、fractional float、超大 int 或 JSON numeric string 如何處理也未定義，可能造成 SQLite `SUM()` overflow 或錯誤成本。
   - **具體修正：** string 欄只接受非空 string；token 欄接受 `int64` 與可無損轉成 int64 的 integral float/JSON number，`0` 有效，負數、overflow、fractional 值忽略並 warn。為 token 設合理上限並在進 DB 前驗證，以免惡意 attributes 令 aggregate overflow。

20. **major · §7.2**
   - **問題：** `input_content` 候選順序先列 `gen_ai.input.messages`，再列「`gen_ai.system_instructions` + `gen_ai.input.messages` 合併」；只要 messages 存在，合併分支永遠不可達，system instructions 會遺失。
   - **具體修正：** 把兩者視為一個 special case：任一存在就先建立一個 canonical JSON object（例如 `{system_instructions: ..., messages: ...}`）；只有 special case 完全不存在才往後找 Langfuse/OpenInference/legacy candidates。

21. **major · §7.2、§8（Langfuse usage）**
   - **問題：** OTel `gen_ai.usage.input_tokens` 是包含 cache-read tokens 的 inclusive total；Langfuse `langfuse.observation.usage_details` 則規定 `input` 與 `input_cached_tokens` 是互斥 buckets。兩者直接塞進同一 `input_tokens` 欄會有不同語意。
   - **具體修正：** 明定內部 `input_tokens` 永遠是 inclusive total。OTel/OpenInference total 原樣保存；Langfuse usage_details 若有 `input_cached_tokens`/其他 `input_*` buckets，normalize 時以 `input + Σ(input_* buckets)` 得到 total，並另存 `cache_read_tokens`。加一個同時含 `input` 與 `input_cached_tokens` 的 fixture。

22. **major · §8**
   - **問題：** 公式 `input*input_price + cache_read*cache_price` 會對 OTel inclusive input 重複計費 cache tokens；「沒有 cache 價時再按 input 價」也會再次多加一次。
   - **具體修正：** 使用 `uncached=max(input-cache_read, 0)`，公式改為 `uncached*input_price + cache_read*(cache_price ?? input_price) + output*output_price`。若只有 cache count 而沒有 input total，成本留 `NULL`，不要猜。

23. **major · §8**
   - **問題：** OTel canonical provider 值如 `aws.bedrock`、`gcp.vertex_ai`、`azure.ai.openai`、`mistral_ai` 與 LiteLLM key prefix `bedrock/`、`vertex_ai/`、`azure/`、`mistral/` 不同；直接組 `provider/model` 會大量 lookup miss。
   - **具體修正：** 在 pricing package 放一個固定 alias map，把 canonical provider 轉為 LiteLLM prefix，再執行既定 lookup；fixture 至少覆蓋 Bedrock、Vertex AI、Azure OpenAI、Mistral。這不是新增設定，無需增加 env var。

24. **major · §10**
   - **問題：** cookie 直接保存 `AUTH_TOKEN`，一旦 cookie 外洩，同一值也可拿去偽造 ingest；且缺少 `SameSite`、`Path`，明確不設 `Secure` 會讓曾透過 HTTPS 登入的 cookie 在可達的 HTTP endpoint 被送出。TLS 在 reverse proxy 終止不妨礙 browser 使用 `Secure`。
   - **具體修正：** cookie 值改為 `HMAC-SHA256(AUTH_TOKEN, "spanbox-ui-session")` 的編碼，不保存原 token；設定 `HttpOnly`、`SameSite=Strict`、`Path=/`，在 `r.TLS != nil` 或 `X-Forwarded-Proto: https` 時設定 `Secure`。登入仍只接收原 token，token rotation 自然使舊 cookie 失效。

25. **minor · §10**
   - **問題：** `subtle.ConstantTimeCompare` 對不同長度會立即返回，直接比較 user input 與 token 並非長度無關；login body 大小、method/content-type 與失敗回應也未限制。
   - **具體修正：** 對兩邊先做 `sha256.Sum256` 再 constant-time compare；`POST /login` 只收 `application/x-www-form-urlencoded`、用 `MaxBytesReader` 限到數 KiB，所有失敗使用相同 status/body。README 要求以 CSPRNG 產生至少 32-byte token。

26. **major · §11（`/sql`）**
   - **問題：** timeout 與「最多讀 1000 列」不限制 statement 複雜度、欄數、單一 cell/總 response bytes；例如巨大 `printf`、recursive CTE 或大量 sort 可先耗盡 CPU/記憶體才產生第一列。僅停止讀第 1000 列也不等於 SQL engine 少做工作。
   - **具體修正：** 除 authorizer 外，對 SQL text、SQLite `SQLITE_LIMIT_LENGTH`/`SQL_LENGTH`/`COLUMN`/`COMPOUND_SELECT` 等 runtime limits、回傳欄數、每 cell bytes 與總 response bytes 設硬上限；確認 modernc 的 context cancellation 會 interrupt 正在執行的 SQLite statement，並加一個長 recursive CTE 在 5 秒內被中止且 connection 仍可用的測試。

27. **major · §10、§11**
   - **問題：** span name、prompt/output、attributes、event 與 SQL cell 全是未受信任內容。雖然 `html/template` 預設 escape，規格沒有禁止轉成 `template.HTML`/inline script；一旦為 JSON pretty-print 或 uPlot 方便而繞過 escape，就能竊取 UI session 或以使用者身分查 `/sql`。
   - **具體修正：** 明定所有 telemetry/SQL 值只能以普通 template data 放進 text context，禁止 `template.HTML`/`template.JS`；圖表資料只由 `encoding/json` endpoint fetch，不 inline 插入 `<script>`。加 `Content-Security-Policy: default-src 'self'; script-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'` 與 `X-Content-Type-Options: nosniff`。

28. **major · §4、§6、§10**
   - **問題：** public `http.Server` 沒有 header/body 讀取期限與 header 大小限制；即使 body 有 32 MiB cap，未驗證的 client 仍可用 slow headers/slow body 長期佔住 connection。
   - **具體修正：** 在 server 設 `ReadHeaderTimeout`、`IdleTimeout`、`MaxHeaderBytes`，並對 ingest/login 設合理 request deadline 或 server `ReadTimeout`；數值寫入 config defaults 而非新增 env var。加入 client 慢速/逾時行為的 handler test 不必要，至少測 body cap。

29. **major · §9**
   - **問題：** prompt/completion 明確不遮罩且可能含機密，但 `DATA_DIR`/DB/WAL/SHM 的 filesystem permissions 未定義；`MkdirAll(..., 0755)` 或寬鬆 umask 會讓同機其他帳號讀取 telemetry。
   - **具體修正：** 新建 data directory 用 `0700`、DB/WAL/SHM 用 owner-only permissions；既有目錄權限過寬時啟動 log warning。Docker README 同時說明 bind mount 必須只允許 container user 存取。

30. **major · §14、§16（Phase 1）**
   - **問題：** Phase 1 要交付兩種 OTLP encoding 與 gzip，但唯一端到端 ingest test 被排到 Phase 3，且只 POST JSON；Phase 1 沒有可執行驗收能攔住 proto JSON ID 被當 hex、gzip 上限、response encoding 或 UPSERT 重送錯誤。
   - **具體修正：** 把最小 `httptest` ingest test 移到 Phase 1，對同一 fixture 分別送 protobuf、protojson、gzip-protobuf、gzip-JSON，驗證 hex IDs、200 response Content-Type/body、DB rows 與相同 span 重送後只有一筆且內容更新；另測 malformed ID、unsupported media type 與 decompressed 32 MiB 超限。

31. **minor · §13**
   - **問題：** Go 沒有可預期的「normalize 例外」；若實作成每 span `recover()` 並繼續，真正的程式 bug 會被吞掉，batch 可能以錯誤欄位成功寫入。這也與 §7.2「mapping 失敗不擋 ingest」混在一起。
   - **具體修正：** `normalize.Span` 對型別不符採「忽略該 candidate、warn、保留 raw」的普通控制流程；不要 recover panic。只有明確、可預期的 validation error 才走 fallback，panic 讓 request 失敗並由 server-level recovery 記錄 stack。

32. **minor · §11（dashboard）**
   - **問題：** dashboard 各圖到底以 trace start 還是 span start 分日、error rate 分母是 trace 還是 span、日期是 UTC 還是 browser local time均未定義；若 cost/tokens 從 `traces`、model table 從 `spans` 混用，數字可能看似互相矛盾。
   - **具體修正：** 明定 cards 與 daily totals/error rate 從 `traces` 以 trace `start_ns`、UTC bucket 計算；LLM duration 與 per-model table 從 `spans` 以 span `start_ns` 計算；UI 明示 UTC。若要 local timezone，必須由 request 傳明確 IANA/offset，不能依 server timezone。

33. **minor · §11（search）**
   - **問題：** raw user text直接交給 FTS5 `MATCH` 會把引號、`-`、`OR`、欄名等當 query syntax，常見 prompt 片段會報 syntax error；規格沒有說 search 是 literal 還是 advanced FTS syntax。
   - **具體修正：** v1 定義為 literal phrase search，將每個 double quote doubled後包成 FTS5 quoted phrase並以 bind parameter傳入；若保留 advanced syntax，UI 必須明示且將 parse error 回成 400/表單錯誤，而不是一般「查不到」。
