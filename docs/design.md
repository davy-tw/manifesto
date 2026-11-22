# Manifesto 設計文件

> Go module：`go.pkgs.davy.tw/manifesto`。API group：`manifesto.davy.tw`。

## 概述

Manifesto 是完全 Kubernetes 原生的部落格引擎：文章、分類、主題和媒體都是 CRD，系統邏輯由 operator 處理，權限交給 K8s RBAC。它同時提供訪客瀏覽的網站，以及給作者使用的管理介面。

**核心原則**

- **內容即資源**：所有內容都存在 etcd，以 CRD 表示，可以用 `kubectl`、GitOps 或 web UI 管理
- **不重造輪子**：身分驗證、授權、稽核都交給 K8s 內建機制，系統本身不做權限判斷
- **namespace 是信任邊界**：同一個 namespace 的成員彼此信任，可以互相編輯
- **press 決定狀態，herald 負責呈現**：渲染在 herald 的記憶體中進行，etcd 是唯一的資料來源
- **只用標準 S3 API**：不依賴任何儲存廠商的特有功能，後端可以自由替換

## 詞彙表

整套命名沿用報刊出版的意象：namespace 是出版社，Gazette 是旗下的一份刊物，Dispatch 是刊在上面的一篇稿件。

**資源（CRD）**

| 名稱 | 對應概念 | 範圍 | shortName | 意象 |
|---|---|---|---|---|
| Gazette | 部落格站台 | Namespaced | blog | 一份有自己品牌的刊物 |
| Dispatch | 文章 | Namespaced | post | 特派員寄回報社刊登的稿件 |
| Edition | 文章修訂版本 | Namespaced | revision | 版次 |
| Section | 分類（一篇一個） | Namespaced | category | 報紙的版面 |
| Stamp | 標籤（一篇多個） | Namespaced | tag | 蓋在文件上的印章 |
| Scribe | 作者 | Namespaced | author | 執筆者 |
| Typeset / ClusterTypeset | 主題 | Namespaced / Cluster | theme | 排版 |
| Print | 媒體檔案 | Namespaced | media | 印刷品，也是沖洗的照片 |
| Archive / ClusterArchive | 媒體儲存設定 | Namespaced / Cluster | store | 檔案庫 |

**元件**

| 名稱 | 職責 |
|---|---|
| press | operator，reconcile 所有資源 |
| herald | 訪客端網站 |
| desk | 管理端 web UI |
| darkroom | 媒體上傳與對外提供 |
| mfctl | CLI |

**角色（ClusterRole）**

| 名稱 | 權限 |
|---|---|
| proofreader | 唯讀，包括草稿 |
| editor | 建立和修改 Dispatch、Section、Stamp、Print |
| publisher | 全部權限，包括 Gazette、Scribe 和成員管理 |

**Dispatch 狀態**：`Draft`（草稿）、`Embargoed`（已排程，時間到才發布）、`Published`（已發布）、`Unlisted`（有網址可看，不列出）、`Retracted`（撤稿，回傳 410 Gone）。

**訪客端與後續功能**：RSS feed 叫 **Wire**（`/wire.xml`），電子報叫 **Issue**，留言叫 **Letter**。

所有 CRD 都加入 `manifesto` category，`kubectl get manifesto` 可以一次列出全部資源。

## 系統架構

```
訪客 ──瀏覽網站──> herald ──watch（唯讀）──┐
  └──載入圖片──> darkroom <──簽章、刪除── press <──reconcile──> kube-apiserver + etcd
作者 ──直接上傳──┘                                              ↑
  └──管理文章──> desk ──以使用者身分讀寫─────────────────────────┘
```

四個元件彼此不直接共享資料，全部透過 kube-apiserver 協作。herald 和 desk 可以自由擴展；darkroom 使用 RWO PVC 時只跑一個副本。

## CRD 設計

所有資源使用 API group `manifesto.davy.tw`，版本從 `v1alpha1` 開始。spec 是使用者期望的狀態，status 是 press 觀察到的實際狀態。

**Gazette**：一個站台。一個 namespace 可以有多個 Gazette。

```yaml
kind: Gazette
metadata:
  name: alice-notes
  namespace: blog-alice
spec:
  title: "Alice's Notes"
  tagline: "寫給未來的自己"
  domain: alice.example.io
  logo: print:alice-logo
  typesetRef: { kind: ClusterTypeset, name: minimal }
  typesetParams:
    accentColor: "#e11d48"
  language: zh-TW
status:
  url: https://alice.example.io
  conditions:
    - type: Ready
      status: "True"
```

**Dispatch**：一篇文章。`metadata.name` 就是 URL slug，內文 inline 存放。

```yaml
kind: Dispatch
metadata:
  name: hello-k8s
  namespace: blog-alice
  labels:
    section.manifesto.davy.tw/name: tech
    stamp.manifesto.davy.tw/kubernetes: ""
spec:
  gazetteRef: alice-notes
  scribeRef: alice
  title: "Hello K8s"
  summary: "第一篇文章"
  cover: print:hello-cover
  state: Published        # Draft / Published / Unlisted / Retracted
  publishAt: "2026-11-20T09:00:00+08:00"
  content:
    format: markdown
    body: |
      # 內文…
status:
  phase: Embargoed
  publishedAt: null
  url: https://alice.example.io/posts/hello-k8s
  observedGeneration: 3
  conditions:
    - type: Rendered
      status: "True"
```

**其他資源的主要欄位**

| 資源 | 主要 spec 欄位 |
|---|---|
| Edition | `dispatchRef`、`number`、內容快照（`snapshot`：title、summary、cover、scribeRef、content）；由 press 在內容變更時自動建立，建立後不可修改 |
| Section | `displayName`、`description`、`parent`（可形成階層） |
| Stamp | `displayName`、`description`；選用，沒有建立就顯示 label 的英文代號 |
| Scribe | `displayName`、`bio`、`avatar`、對應的 OIDC 身分 |
| Typeset | `contractVersion`、`source.inline.templates`、`source.inline.assets`、`params` |
| Print | `archiveRef`、`contentType`、`size`、`sha256`、`alt` |
| Archive | `s3`（endpoint、bucket、憑證）或 `pvc`（claimName、publicURL），二擇一 |

**設計規則**

- **內文 inline**：schema 設 `maxLength`（例如 512KB），遠低於 etcd 單一物件約 1.5MB 的上限
- **寫入一律用 Server-Side Apply**：避免 `last-applied-configuration` annotation 把物件大小翻倍
- **Section 和 Stamp 用 label 儲存**：可以直接用 label selector 查詢；label 只能用英文，中文顯示名稱放在對應的 CRD 裡
- **欄位驗證寫在 schema 和 CEL rules**：例如 `source`、Archive 類型只能設定一種
- **引用是否存在由 press 檢查**：找不到就把 `Ready` condition 設為 `False`
- **可讀性**：每個 CRD 都設 `shortNames`、printer columns，並在 schema description 寫清楚用途，讓 `kubectl explain` 就是詞彙說明

## 權限模型

namespace 是最小的權限單位，系統本身不做任何權限判斷，全部交給 K8s 的 RBAC。

- **信任邊界**：同一個 namespace 的成員彼此信任，可以編輯彼此的 Dispatch，也看得到草稿
- **角色**：`proofreader`、`editor`、`publisher` 三個 ClusterRole 定義一次，各 namespace 用 RoleBinding 綁定。部署後的名稱有 `manifesto-` 前綴（`manifesto-editor` 等）
- **成員管理**：publisher 可以管理 namespace 裡的 RoleBinding，但只能授予這三個角色（`bind` 限定 resourceNames），不能把人升成 cluster-admin
- **共用目錄**：ClusterTypeset 與 ClusterArchive 是叢集範圍的資源，RoleBinding 授權不到，所以另有 `manifesto-catalog-reader`，以 ClusterRoleBinding 開放給所有登入的使用者（`system:authenticated`）唯讀。它們不含機密，ClusterArchive 只寫憑證 Secret 的名稱
- **Secret 不在角色裡**：S3 憑證由叢集管理者建立；publisher 也不能讀 namespace 的 Secret
- **訪客不經過 K8s API**：herald 只提供 `Published` 和 `Unlisted` 的文章；`Retracted` 回 410，`Draft`、`Embargoed` 和不存在的文章回 404，不透露草稿存在
- **desk 只轉送使用者的 token**：以使用者本人的身分呼叫 API server，audit log 記錄的就是本人；不使用 impersonation

**OIDC 綁定**

API server 用 Structured Authentication Configuration 信任 OIDC 提供者，把 token 的 claim 對應成使用者名稱和群組：

```yaml
apiVersion: apiserver.config.k8s.io/v1
kind: AuthenticationConfiguration
jwt:
- issuer:
    url: https://auth.example.io
    audiences: [manifesto-desk]
  claimMappings:
    username: { claim: email, prefix: "oidc:" }
    groups:   { claim: groups, prefix: "oidc:" }
```

接著用 RoleBinding 把群組綁到角色：

```yaml
kind: RoleBinding
metadata:
  name: editors
  namespace: blog-alice
subjects:
- kind: Group
  name: oidc:blog-alice-editors
  apiGroup: rbac.authorization.k8s.io
roleRef:
  kind: ClusterRole
  name: manifesto-editor
  apiGroup: rbac.authorization.k8s.io
```

**使用 Google 登入時**：Google 的 ID token 沒有 `groups` claim。作者少時直接用 `kind: User` 綁個人，並檢查 `email_verified`；作者多時在中間加一層 Keycloak 或 Dex 來管理群組。這是 K8s 的設定，不影響 Manifesto 本身，等需要時再決定。

## Reconcile 與渲染

press 只決定狀態，不產生任何檔案；herald 在記憶體中預先渲染。因此 Dispatch 不需要 finalizer，也不會留下殘留檔案。

**Dispatch 狀態轉換**

| spec.state | 條件 | status.phase | herald 回應 |
|---|---|---|---|
| Draft | — | Draft | 404 |
| Published / Unlisted | 尚未發布過，且 `publishAt` 還沒到 | Embargoed | 404 |
| Published | 其他情況 | Published | 200 |
| Unlisted | 其他情況 | Unlisted | 200，不出現在列表、Wire、sitemap |
| Retracted | — | Retracted | 410 Gone |

每次 reconcile 也檢查 `gazetteRef`、`scribeRef` 是否存在，並更新 `conditions`。

**publishAt 的規則**

- `publishAt` 是「可見狀態的解禁時間」，同樣適用於 Published 和 Unlisted；Draft 和 Retracted 會忽略它
- 文章第一次變成可見時寫入 `status.publishedAt`，之後狀態來回切換也不會改變。`publishAt` 已過就寫入 `publishAt`（補登或 press 停機後補發的文章保留原定日期），否則寫入當下時間
- `publishedAt` 寫入後就不再看 `publishAt`：把已發布文章的 `publishAt` 改到未來，不會把文章撤回 Embargoed。要下架請改成 Draft 或 Retracted

**排程機制**

- press 用 informer 監看所有 Dispatch，變化由 API server 推送，不做輪詢
- Embargoed 的文章 reconcile 後回傳 `RequeueAfter(min(publishAt - now, 1h))`，放進 workqueue 的延遲佇列；等待期間不消耗運算
- **1 小時上限**：每篇排程文章至少每小時重新檢查一次，時鐘被調整或佇列項目遺失時，最多延遲一小時就會自我修正
- **重啟與換 leader**：延遲佇列只存在記憶體。press 啟動時 informer 會列出所有 Dispatch 並逐一 reconcile，重新算出等待時間；`spec.publishAt` 是唯一依據
- **修改 `publishAt`**：觸發新的 reconcile 並重新排程。舊的佇列項目到點時會再比對一次時間，所以 reconcile 必須是冪等的
- 準時程度：時間到後通常數秒內發布，不保證毫秒級精準

**Rendered condition**

press 不產生渲染結果，但會用共用的 `internal/render` 試渲染一次，只記錄成功或失敗，讓作者在發布前就看得到錯誤。

- 試渲染 Markdown，再用所屬 Gazette 的有效主題執行 `dispatch.html` 模板；輸出直接丟棄
- 失敗時 `reason` 為 `MarkdownError` 或 `TemplateError`，`message` 帶錯誤內容（截斷至 1KB）
- Dispatch 的 generation、所屬 Gazette 或有效 Typeset 改變時觸發；只有 condition 真的改變時才寫 status
- M1 就實作這個 condition，讓 API 從一開始就固定下來
- herald 渲染失敗時繼續提供上一次成功的結果；從來沒有成功過就回 503

**Gazette reconcile**

- 不同 namespace 的 Gazette 搶同一個網域時，最早建立的生效，其他的把 status 設成 `DomainConflict`；同時間建立的依 namespace、名稱排序。規則放在 `internal/domain`，press 與 herald 共用。desk 與 darkroom 的網域由 chart 以 `--reserved-domains` 傳給 press，宣告這些網域的 Gazette 一律回報 `DomainReserved`，不會建立路由
- 主題：`typesetRef` 找不到時 `Ready` 為 `TypesetNotFound`，暫用內建主題；解析失敗為 `TemplateError`
- **路由**（press 以 `--gateway-name`/`--gateway-namespace` 啟用，未設定就交給叢集管理者）有兩種模式。`gateway` 模式只建立掛到共用 Gateway 的 HTTPRoute（加上 ReferenceGrant），listener 與 TLS 由叢集管理者負責，適用於不支援 ListenerSet 的實作，例如 k3s 內建的 Traefik（見 `docs/k3s.md`）。預設的 `listenerset` 模式下，擁有網域的 Gazette 在自己的 namespace 得到以下物件，全部以 ownerReference 掛在 Gazette 下，刪除時由 K8s 回收，不需要 finalizer：
  - `ListenerSet` `manifesto-<gazette>`：掛到共用 Gateway，含該網域的 HTTPS（預設 443）與 HTTP（預設 80）listener，port 可用參數調整；`--listener-http-port=0` 不建 HTTP listener 與下面的轉址 HTTPRoute。HTTPS listener 的憑證由 `--listener-tls-certificates` 決定：`perGazette`（預設，`manifesto-<gazette>-tls`）、`shared`（`--listener-tls-secret` 指定的 Secret，press 在 Secret 的 namespace 替每個有 Gazette 的 namespace 建 ReferenceGrant `manifesto-<namespace>.tls`）或 `none`（不寫 `certificateRefs`，由 Gateway 實作提供）；`--listener-tls-option` 原樣寫進 `tls.options`；`--listener-annotation` 原樣寫進 ListenerSet 的 `metadata.annotations`（給讀 ListenerSet annotation 的 controller，例如憑證管理工具；以 SSA 寫入，從設定拿掉的 annotation 也會從 ListenerSet 移除）。`cert-manager.io/` 開頭的 annotation 不能與 `--cluster-issuer` 並用，避免兩邊替同一個 Secret 各建一張 Certificate。`none` 必須有 option、`shared` 必須有 Secret、`--cluster-issuer` 只能搭 `perGazette`，否則 press 拒絕啟動。設定改變後，不再需要的 Certificate、轉址 HTTPRoute 與 ReferenceGrant 由 press 刪除；press 只刪帶自己 `app.kubernetes.io/managed-by` label 的物件，使用者以同名自建的物件（例如沒設 `--cluster-issuer` 時自己用 cert-manager 建的 Certificate）不受影響。Gateway 必須以 `spec.allowedListeners` 允許這些 namespace（開啟 chart 的 desk／darkroom 路由時還有 release namespace）
  - `HTTPRoute` `manifesto-<gazette>`：HTTPS listener 的流量導向 herald；`manifesto-<gazette>-redirect`：HTTP 以 301 轉址到 HTTPS
  - cert-manager `Certificate` `manifesto-<gazette>`（設定 `--cluster-issuer` 時）：簽發 Secret `manifesto-<gazette>-tls`。HTTP-01 驗證要經過每個 ListenerSet，建議用 DNS-01 的 ClusterIssuer
  - herald 所在 namespace 的 `ReferenceGrant` `manifesto-<namespace>`：允許該 namespace 的 HTTPRoute 指向 herald 的 Service；namespace 沒有 Gazette 時移除
  - `Routed` condition 反映 Gateway 實作回報的狀態：ListenerSet `Programmed` 且 HTTPRoute 被 ListenerSet 接受時為 True

**herald 的路由**

herald 用 informer 監看 Gazette、Dispatch、Scribe、Section、Stamp、Typeset、Print，只需要唯讀的 ClusterRole。先用 `Host` header 找出 Gazette，再依路徑分派：

| 路徑 | 內容 |
|---|---|
| `/`、`/page/{n}` | 文章列表，每頁 `spec.pageSize` 篇（預設 10） |
| `/posts/{name}` | 文章內頁 |
| `/sections/{name}`、`/stamps/{name}`、`/scribes/{name}`（各自加 `/page/{n}`） | 篩選列表；Section 包含所有子孫 Section 的文章 |
| `/wire.xml` | RSS 2.0，最新 20 篇 Published；作者用 `dc:creator`，Section 與 Stamp 為 `category` |
| `/sitemap.xml` | 首頁、所有列表頁與 Published 文章（不含 Unlisted） |

- 分類、標籤、作者只要有對應的 CRD，或有 Published 文章用到，就有列表頁；其他回 404

- Markdown 用 goldmark 轉換，再用 bluemonday 清理 HTML，避免 XSS
- **記憶體內預先渲染**：herald 收到 informer 事件時，就在背景重建快照，算好 HTML 放進快取。快取 key 包含 Dispatch、Gazette、Typeset，以及同 namespace 所有 Section、Stamp、Scribe 的 resourceVersion，任何一個改變就重算
- ETag 由頁面內容雜湊而成（不是快取 key），所以每個副本、以及 rolling update 時新舊版本對同一份內容都給出同一個 ETag；`If-None-Match` 相符回 304
- `Cache-Control` 由 `--cache-control`（chart 的 `herald.cacheControl`）設定，套用在頁面、404 與 410；預設 `no-cache`，瀏覽器與 CDN 每次都用 ETag 重新驗證。放在 CDN 後面時可以改成例如 `public, max-age=0, s-maxage=300, stale-while-revalidate=60, stale-if-error=86400`，變更最多延遲 `s-maxage` 才到訪客；撤回（Retracted、改回 Draft）也一樣，最多還會出現 `s-maxage + stale-while-revalidate`，herald 出錯時 `stale-if-error` 會再延長，緊急撤回要手動 purge CDN。503、未知網域與健康檢查一律 `no-store`
- 擴展：herald 不保存狀態，每個副本各自 watch 並在記憶體保存所有網站的頁面。chart 預設 2 副本與 `maxUnavailable: 1` 的 PodDisruptionBudget；`herald.autoscaling` 改用 HorizontalPodAutoscaler（CPU 與選用的記憶體使用率，相對於 `resources.requests`，開啟前要把 requests 調成實際用量；在既有 release 開啟時 Helm 會移除 `replicas`，Deployment 會先掉到 1 副本，可以先 `kubectl scale` 到 `minReplicas`）
- 主題解析失敗時沿用上一個可用版本；從來沒有成功過，HTML 頁面回 503（Wire 與 sitemap 照常）
- **渲染結果不存進 `status`**：否則物件大小會翻倍，換主題時也得重寫所有文章；herald 保持唯讀，`status` 只放 press 寫入的 `Rendered` condition

**Typeset 模板**

- 使用 Go `html/template`：依上下文自動跳脫，模板只能呼叫系統提供的固定 FuncMap（日期格式化、`asset`、`printurl`、`url`、`truncate` 等），不開放自訂函式
- 每種頁面由 `layout.html` 加上對應的頁面模板組成，頁面填入 `content` block
- view model：`.Gazette`、`.Dispatch`、`.Dispatches`、`.Filter`、`.Pagination`、`.Params`；文章有 `.Section`、`.Stamps`、`.Scribe`、`.Path`。`.Params` 是主題的 `params` 被 Gazette 的 `typesetParams` 覆蓋後的結果。不相容的變更就升 `contractVersion`
- 整個主題 inline 在一個資源裡，更新是原子的；用 `mfctl typeset build ./my-theme` 把資料夾打包成 YAML
- press 解析模板並把錯誤寫進 `status.conditions`；新版解析失敗時，herald 繼續用上一個可用版本

## 媒體儲存

媒體檔案不放在 etcd：Print 只記錄中繼資料，檔案本身存在 Archive 指定的後端。後端支援 S3 和原生 PVC 兩種，對 desk 和 herald 完全透明。

```yaml
kind: Print
metadata:
  name: hero-image
  namespace: blog-alice
spec:
  archiveRef: { kind: ClusterArchive, name: local }
  contentType: image/png
  size: 482113
  sha256: "9f2c..."
  alt: "封面圖"
status:
  phase: Ready          # Pending / Ready / Failed
  uploadURL: "..."      # 短效簽章網址，只在 Pending 時存在
  objectKey: 3f1c9a2e-.../sha256/9f2c...e1.png    # <namespace UID>/sha256/<hash>.<ext>
  url: https://media.example.io/3f1c9a2e-.../sha256/9f2c...e1.png
```

**上傳流程**

1. desk 建立 Print，狀態是 `Pending`
2. press 簽出 5 分鐘有效的上傳網址並寫進 status（S3 用 presigned PUT，PVC 用 HMAC 簽章）
3. 瀏覽器直接上傳檔案，不經過 desk
4. press 核對大小和 sha256，通過就設成 `Ready`；逾時未上傳設成 `Failed` 並清理

**對外提供**

- 公開讀取，物件 key 用內容雜湊，網址猜不到；只開放依 key 讀取，不提供列表
- 內容不變網址就不變，設定 `Cache-Control: immutable`，可以直接接 CDN
- 引用方式：Markdown 寫 `![封面](print:hero-image)`，模板寫 `{{ printurl "logo" }}`
- **去重只在同一個 namespace 內進行**：物件 key 以 namespace UID 開頭，不同 namespace 的相同檔案各存一份，避免跨越信任邊界推測別人有哪些檔案。用 UID 而不是名稱，公開網址才不會洩漏 namespace 名稱
- 同一個 namespace 內最後一個引用的 Print 被刪除時，才由 finalizer 真正刪除檔案

**兩種 Archive 後端**

|  | S3 | 原生 PVC |
|---|---|---|
| 儲存位置 | S3 相容服務，預設建議 SeaweedFS | 叢集內的 PV |
| 上傳簽章 | presigned PUT | press 用 HMAC 簽章，darkroom 驗證 |
| 對外提供 | 物件儲存或 CDN | darkroom |
| 副本數 | 不受限 | RWO 只能 1 個副本，RWX 可以多個 |
| 備份 | 由儲存服務負責 | VolumeSnapshot 或 Velero |

**darkroom 的實作細節**

- HMAC 金鑰由 press 第一次啟動時產生並存成 Secret；輪替時新舊金鑰同時有效一段時間
- 寫入時邊收邊計算 sha256，不符就拒絕；先寫暫存檔，驗證通過再 `rename`
- 檔案路徑分兩層目錄，例如 `/data/<namespace UID>/sha256/9f/2c/9f2c…e1.png`
- 刪除 API 只在叢集內開放，用 NetworkPolicy 限制只有 press 能連
- 簽章是 HMAC-SHA256，涵蓋 method、物件 key、大小與到期時間，一個網址只能做那一件事
- press 無法得知上傳何時完成，Pending 的 Print 每 2 秒檢查一次，直到上傳網址到期；到期仍未上傳就設成 `Failed`
- Print 不存在或還沒 Ready 時，Markdown 裡的圖片會被移除、連結只留文字，Dispatch 的 `Ready` 回報 `PrintNotReady`

**S3 後端**只用標準 S3 API：presigned `PutObject`（只簽入 Content-Type，上傳者只需帶這個 header；長期快取由 `publicURL` 前的 CDN 或路由設定）、`HeadObject`、`GetObject`、`CopyObject`、`DeleteObject`、`HeadBucket`。presigned URL 無法撤銷，到期前可以重複使用，而且能讀 Print 的人（包括 proofreader）都看得到，所以上傳網址簽給暫存 key `staging/<object key>`，不是訪客讀取的 key。S3 無法在上傳時驗證內容，press 下載暫存檔核對大小與 sha256（`GetObject` 帶 `If-Match`，確保核對的就是 HEAD 看到的版本），相符才 `CopyObject`（帶 `x-amz-copy-source-if-match`）到正式 key 並刪除暫存檔，不符就刪除並回報 `UploadRejected`。正式 key 只有 press 會寫入；複製後的 ETag 和核對過的不同時（服務不支援條件複製，或加密讓 ETag 不是 MD5），press 再下載正式檔核對一次。重放上傳網址只會寫到暫存 key，刪除 Print 時一併清掉；bucket 可以另設 lifecycle rule 讓 `staging/` 下的物件一天後過期。press 重啟後第一次看到正式檔仍會重新核對，核對過的物件記在記憶體中，不重複下載。憑證放在 Secret（`accessKeyID`、`secretAccessKey`），press 直接向 API server 讀取，不快取、不監看所有 Secret，只需要 `get` 權限。

文章內容存在 etcd，所以備份媒體時 etcd 也要一起備份。

## Edition（修訂版本）

- press 在 Dispatch 的內容（title、summary、cover、scribeRef、content）改變時建立 Edition；只改 state、publishAt、gazetteRef 或分類 label 不算
- 名稱是 `<dispatch>-<number>`，number 從 1 遞增；用 label `manifesto.davy.tw/dispatch` 查詢。和最新一版的 `digest` 相同就不建立，所以 reconcile 冪等
- 每篇保留最近 20 版（press 的 `--editions`；負數停止記錄），而且這些版本的內容合計不超過 2 MiB（`--edition-bytes`；0 不限制），從最新一版往回算，超過的由 press 刪除，最新一版一定保留。ownerReference 指向 Dispatch，刪文章時一起回收
- spec 用 CEL 禁止修改；所有角色都能讀，只有 publisher 能刪除（例如移除不該留下的內容），沒有人能建立
- 還原：把舊版的 snapshot 複製回 Dispatch（desk 的 History、`mfctl edition restore`），這會成為新的一版，歷史不會被改寫
- 為什麼限制大小：Edition 存在 etcd，整個叢集共用。一篇最多 512KB，只限版數的話一篇最多約 10MB；k3s 用內嵌 etcd 時預設上限 2 GiB，用滿後整個叢集變成唯讀（單節點預設的 SQLite 沒有這個上限）。2 MiB 是一般文章的 20 版，或最大文章的 4 版

## mfctl（命令列工具）

- 用使用者的 kubeconfig，和 desk 一樣不做權限判斷；寫入的 fieldManager 是 `manifesto-mfctl`
- `typeset build DIR`：資料夾（平的，`*.html` 是模板、其他是文字資產）打包成 Typeset / ClusterTypeset 的 YAML，輸出前用 `internal/render` 解析一次
- `print upload FILE`：算 sha256、以 Server-Side Apply 建立 Print、PUT 到 `status.uploadURL`、等 press 核對後 Ready；同 namespace 已有相同檔案就直接 Ready。只有一個 Archive / ClusterArchive 時自動選用
- `edition list | show | restore`：restore 用帶 resourceVersion 的 merge patch，因為還原必須移除 Edition 沒有的欄位（apply 只會移除自己設定過的欄位），並在有人同時修改時拒絕而不是覆蓋

## 安裝

- Helm chart `charts/manifesto` 與 kustomize `config/default` 建立相同的資源與名稱（`manifesto-*`），一個叢集裝一次
- chart 可選擇性地替 desk 與 darkroom 的公開 port 建立路由（`desk.route`、`darkroom.route`，hostname 取自各自的 `publicURL`）：掛到 press 使用的 Gateway，物件與 press 替同名 Gazette 建的相同（`gateway` 模式一條 HTTPRoute；`listenerset` 模式加上 ListenerSet、轉址 HTTPRoute 與選用的 Certificate），`test/chart` 以 `controller.RoutingObjects` 比對兩者。預設關閉。`listenerset` 模式下這些 ListenerSet 建在 release namespace，Gateway 的 `spec.allowedListeners` 也要允許它。這些 ListenerSet 套用 press 同一組 TLS 與 HTTP listener 設定（`press.routing.tls`、`listenerHTTPPort`）；`shared` 時 chart 在 Secret 的 namespace 建 ReferenceGrant `manifesto.<release namespace>.tls`。desk 與 darkroom 的 `publicURL` 網域（不論路由是否由 chart 建立）以 `--reserved-domains` 傳給 press，避免租戶的 Gazette 搶走這些網域。Gateway 本身與 ClusterArchive 仍由叢集管理者建立（chart 的 CRD 在 templates 裡，首次安裝時無法同時建立 CR）
- chart 的 CRD 與 RBAC 不手寫：`make chart` 從 kustomize 的輸出產生 `charts/manifesto/files/`，CI 檢查有沒有過期；`test/chart` 比對兩者的物件、CRD、RBAC 與 Pod 設定
- CRD 放在 templates（不是 `crds/`），升級時才會更新；CRD 和媒體 PVC 加上 `helm.sh/resource-policy: keep`
- Kind 與 k3d 的 E2E 都用 Helm 安裝；k3d 另外開啟 gateway 模式與 desk、darkroom 的 chart 路由。kustomize 的輸出由 `test/chart` 與 chart 比對
- 發布：`v*` tag 把 image（linux/amd64、linux/arm64）與 chart（OCI）推到 ghcr.io，GoReleaser 把 mfctl（Linux、macOS、Windows、FreeBSD，amd64 與 arm64）放到 GitHub Release；chart 的 image 預設是 `ghcr.io/davy-tw/manifesto-<元件>`，tag 是 chart 的 appVersion

## desk（管理介面）

- SPA + 後端；後端負責 OIDC 登入、session cookie，並把請求代理到 API server。SPA 不直接連 API server
- Markdown 編輯與即時預覽：預覽呼叫共用的 `internal/render`，套用真正的主題
- 透過 watch 即時顯示 `phase` 變化
- 編輯器的 History 列出 Edition，可以把舊版載入編輯器，儲存後即還原
- 編輯衝突以 `resourceVersion` 樂觀鎖偵測（API 回 409 時提示合併）；寫入用 Server-Side Apply，fieldManager 為 `manifesto-desk`，一律 `force=true`：`mfctl edition restore` 或 `kubectl edit` 改過的欄位屬於別的 field manager，不 force 的話作者存檔會被誤判為衝突；真正的並行編輯由 resourceVersion 擋下
- 登入用 OIDC authorization code + PKCE；ID token 與 refresh token 以 AES-GCM 加密存在 cookie（金鑰由 press 建立在 Secret `manifesto-desk-session`），desk 不保存任何 session，可以多副本
- desk 以 volume 掛載這個 Secret：初次安裝時 press 啟動前，desk 的 Pod 會停在 ContainerCreating，press 建立 Secret 後 kubelet 自動重試，不需要安排啟動順序（`helm install --wait` 會等到兩者都 Ready）
- 代理只轉送 Manifesto 需要的路徑（manifesto.davy.tw 資源、events、RoleBinding、SelfSubject*Review），其他回 404；用 cookie 的寫入必須帶 `X-Manifesto-Desk` header 且 Origin 相同，防止 CSRF
- 也接受 `Authorization: Bearer`（不經 cookie），方便腳本與測試
- 選用的 token 登入（`--token-sign-in`）：使用者貼上 API server 認得的 token（例如 ServiceAccount token），desk 以 SelfSubjectReview 確認後存進同一個加密 cookie；session 在 token 的 `exp` 或 12 小時到期，不能 refresh。登入請求同樣要求 CSRF header，避免別的網站讓訪客以別人的身分登入

## 技術選型與待辦

**技術選型**

| 項目 | 選擇 | 理由 |
|---|---|---|
| 語言與框架 | Go + kubebuilder | 與 K8s 生態最契合，press 與 herald 共用渲染套件 |
| 流量入口 | Gateway API（Envoy Gateway 或 Traefik） | ingress-nginx 已於 2026 年 3 月停止維護 |
| TLS | cert-manager | 自 1.15 起原生支援 Gateway API |
| Markdown | goldmark + bluemonday | 轉換與 HTML 清理 |
| 物件儲存 | SeaweedFS（選用） | MinIO 社群版已封存；Apache 2.0，S3 支援完整 |
| 身分 | 任何 OIDC 提供者 | 交給 K8s 設定 |
| 交付 | Argo CD + Helm 或 Kustomize | GitOps |
| 本機開發 | k3d 或 kind + Tilt | 熱重載 |

**里程碑**

1. **M1 MVP**（已完成）：專案骨架、Gazette + Dispatch CRD、press 狀態機與 `Rendered` condition、herald + 內建預設主題、envtest 與單元測試
2. **M2 內容與網站**（已完成）：Section、Stamp、Scribe、Typeset / ClusterTypeset、Wire 與 sitemap、分頁與篩選、HTTPRoute + cert-manager
3. **M3 媒體**（已完成）：Print、Archive、darkroom（先 PVC driver，再 S3）
4. **M4 管理介面**（已完成）：desk，以及 `proofreader`、`editor`、`publisher` 三個 ClusterRole
5. **M5 補完**（已完成）：Edition、mfctl、Helm chart

**待辦與未來功能**

- [ ] Typeset 支援 `spec.source.oci`：主題打包成 OCI artifact，依 digest 拉取，可用 cosign 簽章
- [ ] Print 的縮圖與 WebP：由 press 為每個 Print 啟動 Job，結果寫回 `status.variants`
- [ ] Letter：讀者留言，預計做成 Aggregated API Server，背後接資料庫
- [ ] Issue：電子報，每期收錄多篇 Dispatch
- [ ] 搜尋功能：尚未討論

已決定不做：聯合供稿，也就是一篇 Dispatch 同時刊在多個 Gazette。每篇 Dispatch 只屬於一個 Gazette，gazetteRef 維持單一值。

## Sources

- [Ingress NGINX Retirement: What You Need to Know](https://kubernetes.io/blog/2025/11/11/ingress-Nginx-retirement/)
- [Ingress NGINX End of Life](https://coralogix.com/guides/ingress-nginx-end-of-life/)
- [Self-Hosted S3 Alternatives in 2026](https://glukhov.org/data-infrastructure/object-storage/self-hosted-s3-alternatives/)
