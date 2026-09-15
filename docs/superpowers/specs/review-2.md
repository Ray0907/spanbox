# Spec v2 verification

1. **fixed** — `spans.id INTEGER PRIMARY KEY`、ID `NOT NULL`/`UNIQUE`、stable-rowid UPSERT 與 FTS `content_rowid='id'` 已完整取代 `INSERT OR REPLACE`。
2. **fixed** — §11.2 已以每連線 `mode=ro`/`query_only`、專用 `*sql.Conn`、單 statement tokenizer allowlist、interrupt timeout 與 caps 落實可用的 layered defense，並明載沒有 engine authorizer 的限制。
3. **fixed** — §5.3 已提供正確的 external-content insert/delete/update triggers 與既有資料 `rebuild`。
4. **fixed** — §5.2 已定義 `(start_ns, span_id)` 排序、root fallback、first-nonempty 與 ordered model dedupe。
5. **fixed** — §5.2 已定義 llm/embedding 子集的 NULL 傳染，UI 不顯示 partial totals。
6. **fixed** — §12 已改為同 transaction 的 trace-level retention，vacuum 在 commit 後執行。
7. **fixed** — §5.4 已定義 writer/auto-vacuum/WAL/migration/reader 順序及 DSN per-connection PRAGMA。
8. **fixed** — §11.1 已明定 nearest-rank 並使用 `ROW_NUMBER`/`COUNT`，不再使用 `NTILE`。
9. **fixed** — trace list 已使用 `(start_ns DESC, trace_id DESC)` 排序、雙值 cursor、row comparison 與相符索引。
10. **fixed** — §6.1 已同時限制 32 MiB wire/decompressed body，拒絕未知 encoding 並關閉 gzip reader。
11. **fixed** — §6.1 已規定 `mime.ParseMediaType`、415、同 encoding 成功/錯誤 body 與 `google.rpc.Status`。
12. **fixed** — §6.2 已明定 protojson base64 bytes、解碼後 hex、ID/全零/時間範圍與順序驗證。
13. **fixed** — §6.2 已涵蓋全部 `AnyValue` oneof、nested values、bytes、duplicate keys、nil 與 non-finite float。
14. **fixed** — §1/§2/§5/§11 已一致界定並保存/display attributes、events、resource、scope、links、trace state，且明列不保存項目。
15. **fixed** — §7.1 僅在辨識值時停止，並補齊 `llm_request`、`vector_db_retrieve`、`rerank`、`agent_step`。
16. **fixed** — §7.2 已支援任意深度 numeric path，補齊 OpenInference messages/tool calls/prompts/choices 與 fixtures。
17. **fixed** — Vercel legacy embed 欄位與 current `gen_ai.*` agent/chat/tool/embed/rerank fixtures 均已加入。
18. **fixed** — explicit Langfuse/OpenInference cost 已優先於 pricing，並定義 `total`/bucket sum 與 `cost_source`。
19. **fixed** — §6.3/§7.2 已定義 token 型別、zero、負數、小數、overflow 與 `1e12` 上限。
20. **fixed** — `system_instructions` 與 `input.messages` 已改為先行 special case，不再有 unreachable candidate。
21. **fixed** — internal input/output token 已統一為 inclusive total，Langfuse exclusive buckets 有明確轉換與 fixture。
22. **fixed** — pricing 已用 uncached/cache 分拆公式，缺 input total 時回 NULL，不再重複計費。
23. **fixed** — §8 已加入 canonical OTel provider 到 LiteLLM prefix 的固定 alias map 與測試。
24. **fixed** — UI cookie 已改為 HMAC-derived value，並設定 HttpOnly、SameSite、Path 與條件式 Secure。
25. **fixed** — token 先 SHA-256 再 constant-time compare，login media type/body cap/一致失敗及 token entropy 均已規定。
26. **partially fixed** — SQL text/rows/columns/cells/response/timeout caps 已加入，但沒有 `sqlite3_limit` 時，單一 expression 仍可能在產生 cell 前於 SQLite 內配置大量記憶體；目前只靠 5s interrupt 與 trusted-operator 限制。
27. **fixed** — telemetry/SQL values 被限制為 ordinary template text，禁止 unsafe template types/inline data script，並加入 CSP/nosniff。
28. **fixed** — §4.3 已設定 header/read/idle timeouts 與 `MaxHeaderBytes`，handler body caps 亦已定義。
29. **fixed** — §5.4/§9/§15 已規定 0700 data dir、0600 DB、寬鬆既有權限警告及 bind-mount 提醒。
30. **fixed** — §14/§16 已把四種 encoding、ID/limit/error、UPSERT/FTS 與最小 ingest e2e 納入 Phase 1。
31. **fixed** — §7.2/§13 已區分普通 candidate mismatch 與 panic，明確禁止 normalize-level `recover`。
32. **fixed** — §11 已定義 UTC、trace/span 資料來源及 trace-based error-rate 分母。
33. **fixed** — search 已定義 literal FTS5 phrase escaping、binding 與行為。

## NEW blocker

- **blocker — §3、§10、§11.2：** §3 說 `AUTH_TOKEN` 空時「全開」，但 §11.2 的安全前提是 `/sql`「只給持有 AUTH_TOKEN 的操作者」；空 token 時 `/sql` 是否公開沒有可實作的唯一答案。請明定空 token 時 `/sql` 是停用/拒絕，或承認公開並重寫其安全前提。
