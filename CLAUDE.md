# Manifesto

Manifesto 是完全 Kubernetes 原生的部落格引擎：內容都是 CRD，邏輯由 operator 處理，權限交給 K8s RBAC。
完整設計見 `docs/design.md`；這份文件只放每次工作都需要的規則。

- Go module：`go.pkgs.davy.tw/manifesto`
- API group：暫用 `manifesto.davy.tw`，確定網域後全域替換

## 核心原則（不可違反）

- **內容即資源**：所有內容存在 etcd，以 CRD 表示。不引入額外資料庫（Letter 留言除外，未來另議）。
- **不做權限判斷**：身分驗證、授權、稽核全部交給 K8s。desk 只轉送使用者 token，不用 impersonation。
- **namespace 是信任邊界**：同 namespace 成員互相信任，不做 namespace 內的租戶隔離；跨 namespace 不共享任何東西（包括媒體去重）。
- **press 決定狀態，herald 負責呈現**：herald 唯讀（get/list/watch），不寫任何資源；渲染結果不存進 status。
- **只用標準 S3 API**：不依賴任何儲存廠商的特有功能。

## 詞彙

| Kind | 概念 | shortName |
|---|---|---|
| Gazette | 部落格站台（網域、logo、主題） | blog |
| Dispatch | 文章；只屬於一個 Gazette（`gazetteRef` 單一值） | post |
| Edition | 文章修訂版本，由 press 自動建立 | revision |
| Section | 分類，一篇一個，可設 parent | category |
| Stamp | 標籤，一篇多個 | tag |
| Scribe | 作者 | author |
| Typeset / ClusterTypeset | 主題 | theme |
| Print | 媒體檔案的中繼資料 | media |
| Archive / ClusterArchive | 媒體儲存設定（`s3` 或 `pvc` 二擇一） | store |

- 元件：`press`（operator）、`herald`（訪客端）、`desk`（管理 web UI）、`darkroom`（媒體）、`mfctl`（CLI）
- ClusterRole：`proofreader`（唯讀含草稿）、`editor`、`publisher`
- Dispatch phase：`Draft`、`Embargoed`（已排程）、`Published`、`Unlisted`、`Retracted`
- RSS 叫 Wire（`/wire.xml`）；未來：電子報 Issue、留言 Letter
- 所有 CRD 都加入 `manifesto` category，並設定 shortNames、printer columns、schema description

## Dispatch 狀態規則

| spec.state | 條件 | status.phase | herald 回應 |
|---|---|---|---|
| Draft | — | Draft | 404 |
| Published / Unlisted | 尚未發布過，且 `publishAt` 還沒到 | Embargoed | 404 |
| Published | 其他情況 | Published | 200 |
| Unlisted | 其他情況 | Unlisted | 200，不出現在列表、Wire、sitemap |
| Retracted | — | Retracted | 410 Gone |

- 第一次變成可見時寫入 `status.publishedAt`（`publishAt` 已過就用 `publishAt`，否則用當下時間），之後不再改變，也不再看 `publishAt`
- Embargoed 回傳 `RequeueAfter(min(publishAt - now, 1h))`；reconcile 必須冪等
- `Rendered` condition：press 用 `internal/render` 試渲染（輸出丟棄），失敗 reason 為 `MarkdownError` / `TemplateError`，message 截斷至 1KB；只有 condition 真的改變才寫 status
- herald 渲染失敗時沿用上一次成功的結果；從來沒成功過回 503

## 程式慣例

- Go + kubebuilder + controller-runtime；API 版本從 `v1alpha1` 開始
- spec 是期望狀態，status 是觀察到的狀態；狀態一律用 `metav1.Condition`，並維護 `observedGeneration`
- 寫入一律用 Server-Side Apply（fieldManager 用元件名稱，例如 `manifesto-press`）
- 欄位驗證寫在 CRD schema 與 CEL（`x-kubernetes-validations`）；引用是否存在由 press 檢查並寫入 conditions
- Section / Stamp 用 label 存英文代號：`section.manifesto.davy.tw/name: <name>`、`stamp.manifesto.davy.tw/<name>: ""`
- `internal/render` 是 press 與 herald 共用的渲染套件（goldmark + bluemonday + html/template）
- Typeset 模板只能用固定 FuncMap（日期格式化、`asset`、`printurl`、`url`、`truncate`），不開放自訂函式
- 排程發布用 `RequeueAfter`，不用 cron
- 只有需要清理叢集外資源時才用 finalizer（目前只有 Print）

## 測試

- press 的 reconcile 用 envtest
- 渲染、狀態轉換規則用單元測試（table-driven）
- 完成任何變更前執行 `make test` 和 `make lint`

## 里程碑

M1（MVP）與 M2（內容與網站）已完成。程式位置：

- press：`internal/controller`（`setup.go` 註冊 index 與所有 controller；狀態機 `phase.go`；主題解析 `theme.go`；路由 `routing.go`）
- 網域歸屬（press 與 herald 共用）：`internal/domain`
- 渲染與 view model：`internal/render`（內建主題在 `theme/`）
- herald：`internal/herald`（`build.go` 快照、`feed.go` Wire 與 sitemap、`handler.go` 路由）
- 測試：envtest 在 `internal/controller`、`api/v1alpha1`；端到端 `internal/herald/milestone_test.go`；Kind 與 k3d（k3s + Traefik，`make test-e2e-k3d`，含 gateway 路由經過 Traefik 的驗證）上的 E2E 在 `test/e2e`，CI 兩者都跑；envtest 用的第三方 CRD 在 `test/crds`

目標環境：先以 k3s（內建 Traefik）做小規模正式測試，見 `docs/k3s.md`。新功能要能在 k3s 上運作：Traefik 不支援 ListenerSet（v3.8 前）、listener port 必須是 entrypoint port（8000/8443）、預設 StorageClass `local-path` 只有 RWO。

路由慣例：press 以 `--gateway-name` 啟用，`--routing-mode=gateway`（k3s 用）只建立掛到 Gateway 的 HTTPRoute；預設的 `listenerset` 模式下，每個擁有網域的 Gazette 在自己的 namespace 得到 `manifesto-<gazette>` 的 ListenerSet、HTTPRoute、`-redirect` HTTPRoute 與 Certificate（ownerReference 回收，不用 finalizer），herald 的 namespace 有每個 namespace 一個的 ReferenceGrant。

下一個里程碑：M3（媒體）— Print、Archive / ClusterArchive、darkroom（先 PVC driver，再 S3）。範圍外：desk、Edition、mfctl。
