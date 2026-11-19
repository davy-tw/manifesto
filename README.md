# Manifesto

Manifesto 是完全 Kubernetes 原生的部落格引擎：文章、分類、主題和媒體都是 CRD，系統邏輯由 operator 處理，權限交給 K8s RBAC。設計見 [`docs/design.md`](docs/design.md)。

目前進度：**M3（媒體）**完成，PVC（darkroom）與 S3 兩種後端都可以用。文章、分類（Section）、標籤（Stamp）、作者（Scribe）與主題（Typeset / ClusterTypeset）都是 CRD；herald 提供分頁列表、篩選頁、Wire（RSS）與 sitemap；press 可以替每個網域建立 Gateway API 路由與 cert-manager 憑證。

| 元件 | 位置 | 職責 |
|---|---|---|
| press | `cmd/press` | operator：決定 Dispatch 的 phase、排程發布、檢查引用、試渲染 |
| herald | `cmd/herald` | 訪客端網站：唯讀監看資源，在記憶體中預先渲染，依 Host 提供頁面 |
| darkroom | `cmd/darkroom` | 媒體檔案：驗證簽章上傳、以內容雜湊存放與對外提供；不碰 K8s API |

## 本機試用

需要 Go 1.26 和一個 Kubernetes 叢集（kind 或 k3d 都可以）。

```sh
make install                  # 安裝 CRD
make run                      # 終端機 1：執行 press
make run-herald               # 終端機 2：執行 herald，監聽 :8080

kubectl create namespace blog-alice
kubectl apply -n blog-alice -k config/samples   # Gazette、Dispatch、Scribe、Section、Stamp 與 ClusterTypeset
kubectl get manifesto -n blog-alice
```

sample 的 Gazette 網域是 `alice.localtest.me`（解析到 127.0.0.1），所以可以直接開 <http://alice.localtest.me:8080/>，或是：

```sh
curl -H 'Host: alice.localtest.me' http://localhost:8080/posts/hello-k8s
```

把 sample Dispatch 的 `publishAt` 改成幾分鐘後，可以看到它先是 `Embargoed`（herald 回 404），時間到自動變成 `Published`。其他頁面：`/sections/tech`、`/stamps/kubernetes`、`/scribes/alice`、`/wire.xml`、`/sitemap.xml`。

## 部署

```sh
make docker-build docker-push IMG=<registry>/manifesto-press:<tag>
make docker-build docker-push COMPONENT=herald IMG=<registry>/manifesto-herald:<tag>
make docker-build docker-push COMPONENT=darkroom IMG=<registry>/manifesto-darkroom:<tag>
make deploy IMG=<registry>/manifesto-press:<tag> HERALD_IMG=<registry>/manifesto-herald:<tag> \
  DARKROOM_IMG=<registry>/manifesto-darkroom:<tag>
```

herald 的 Service 是 `manifesto-system/manifesto-herald`。

### 權限

Manifesto 不做權限判斷，全靠 RBAC。在每個 namespace 用 RoleBinding 把人（或 OIDC 群組）綁到以下 ClusterRole：

| 角色 | 可以做的事 |
|---|---|
| `manifesto-proofreader` | 讀取所有內容，包括草稿 |
| `manifesto-editor` | proofreader，加上建立、修改、刪除 Dispatch、Section、Stamp、Print |
| `manifesto-publisher` | editor，加上 Gazette、Scribe、Typeset、Archive，以及用上面三個角色管理成員 |

```sh
kubectl create rolebinding editors -n blog-alice --clusterrole=manifesto-editor --group=oidc:blog-alice-editors
```

所有登入的使用者都能讀取共用的 ClusterTypeset 與 ClusterArchive（`manifesto-catalog-reader`）。

### 媒體（Print）

darkroom 把檔案存在 PVC `manifesto-system/manifesto-media-data`（叢集預設的 StorageClass，k3s 上是 `local-path`），因為 PVC 是 RWO，只跑一個副本。

1. 讓訪客連得到 darkroom 的 Service `manifesto-system/manifesto-darkroom`（port 80），例如用 HTTPRoute 掛到 `media.example.io`
2. 建立 ClusterArchive，`publicURL` 填上一步的網址（見 `config/samples/manifesto_v1alpha1_clusterarchive.yaml`）
3. 建立 Print（`size`、`sha256` 必須和檔案一致），等 press 寫入 `status.uploadURL` 後上傳：

```sh
curl -T docs/assets/hello-cover.svg "$(kubectl get media hello-cover -n blog-alice -o jsonpath='{.status.uploadURL}')"
kubectl get media -n blog-alice   # Phase 變成 Ready
```

內容用 `![封面](print:hello-cover)` 引用，主題用 `{{ printurl "hello-cover" }}`、`.Dispatch.Cover`、`.Gazette.Logo`。上傳網址 5 分鐘內有效（`--upload-ttl`），逾時 Print 變成 `Failed`，刪掉重建即可。同 namespace 內容相同的檔案只存一份，最後一個引用的 Print 刪除時才刪檔。

**改用 S3**：建立 bucket 和存放 `accessKeyID`、`secretAccessKey` 的 Secret，再建立 `s3` 後端的 Archive（見 `config/samples/manifesto_v1alpha1_archive_s3.yaml`）。Archive 讀自己 namespace 的 Secret；ClusterArchive 讀 `credentialsSecretRef.namespace`，沒填就讀 press 所在的 namespace。上傳時要帶 Print 的 Content-Type（`curl -T file -H "Content-Type: image/png" "$URL"`），它被簽進 presigned URL。S3 不會在上傳時驗證內容，所以 press 第一次看到物件時會下載核對 sha256，不符就刪除並回報 `UploadRejected`，可以在網址到期前重傳。訪客要能匿名讀取物件（bucket 開放讀取，或在前面接 CDN 並設定 `publicURL`）。

press 第一次啟動會在 `manifesto-system` 建立簽章用的 Secret `manifesto-darkroom-keys`；要換 key 時，把舊 key 改名（例如 `previous`）並放入新的 `current`，darkroom 會同時接受兩者。

### 路由與 TLS

預設 press 不碰路由，你可以自己把 Gateway 指向 herald。要讓 press 替每個 Gazette 的網域建立路由，在 `config/manager/manager.yaml` 打開這些參數：

| 參數 | 說明 |
|---|---|
| `--routing-mode` | `listenerset`（預設）：每個 Gazette 一個 ListenerSet 與憑證；`gateway`：HTTPRoute 直接掛到 Gateway 的 listener，TLS 由 Gateway 負責 |
| `--gateway-name`、`--gateway-namespace` | 共用的 Gateway |
| `--gateway-section` | `gateway` 模式下要掛的 listener 名稱（選用） |
| `--listener-https-port`、`--listener-http-port` | `listenerset` 模式下 listener 的 port，預設 443 與 80 |
| `--cluster-issuer` | 選用。cert-manager 的 ClusterIssuer；設定後 press 替每個網域建立 `Certificate` |
| `--herald-service`、`--herald-port` | herald 的 Service，預設 `manifesto-system/manifesto-herald:80` |

**k3s（內建 Traefik）請用 `gateway` 模式，步驟見 [`docs/k3s.md`](docs/k3s.md)。**

`listenerset` 模式的需求：

- Gateway API v1.6 以上的標準 CRD（含 `ListenerSet`），以及支援 ListenerSet 的 Gateway 實作
- 共用的 Gateway 要允許 Gazette 所在 namespace 的 ListenerSet，例如 `spec.allowedListeners.namespaces.from: All`
- 用 cert-manager 時建議 DNS-01 的 ClusterIssuer（HTTP-01 要額外設定才能經過每個 ListenerSet）

press 會在 Gazette 的 namespace 建立 `manifesto-<gazette>` 的 ListenerSet、HTTPRoute（以及 HTTP 轉 HTTPS 的 `-redirect`）和 Certificate，並在 herald 的 namespace 建立 ReferenceGrant。`kubectl get blog` 的 `Routed` condition 會顯示 Gateway 是否已經接手。

## 開發

```sh
make test           # 單元測試 + envtest（包含 press 與 herald 的端到端測試）
make lint
make test-e2e       # 在 Kind 上部署並跑 E2E（需要 docker 與 kind）
make test-e2e-k3d   # 在 k3d（k3s + Traefik）上跑 E2E，並驗證 gateway 路由模式（需要 docker 與 k3d）
```

## License

MIT License，見 [LICENSE](LICENSE)。
