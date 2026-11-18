# Manifesto

Manifesto 是完全 Kubernetes 原生的部落格引擎：文章、分類、主題和媒體都是 CRD，系統邏輯由 operator 處理，權限交給 K8s RBAC。設計見 [`docs/design.md`](docs/design.md)。

目前進度：**M2（內容與網站）**完成。文章、分類（Section）、標籤（Stamp）、作者（Scribe）與主題（Typeset / ClusterTypeset）都是 CRD；herald 提供分頁列表、篩選頁、Wire（RSS）與 sitemap；press 可以替每個網域建立 Gateway API 路由與 cert-manager 憑證。

| 元件 | 位置 | 職責 |
|---|---|---|
| press | `cmd/press` | operator：決定 Dispatch 的 phase、排程發布、檢查引用、試渲染 |
| herald | `cmd/herald` | 訪客端網站：唯讀監看資源，在記憶體中預先渲染，依 Host 提供頁面 |

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
make deploy IMG=<registry>/manifesto-press:<tag> HERALD_IMG=<registry>/manifesto-herald:<tag>
```

herald 的 Service 是 `manifesto-system/manifesto-herald`。

### 路由與 TLS

預設 press 不碰路由，你可以自己把 Gateway 指向 herald。要讓 press 替每個 Gazette 的網域建立路由，在 `config/manager/manager.yaml` 打開這些參數：

| 參數 | 說明 |
|---|---|
| `--gateway-name`、`--gateway-namespace` | 每個 Gazette 的 `ListenerSet` 要掛上的共用 Gateway |
| `--cluster-issuer` | 選用。cert-manager 的 ClusterIssuer；設定後 press 替每個網域建立 `Certificate` |
| `--herald-service`、`--herald-port` | herald 的 Service，預設 `manifesto-system/manifesto-herald:80` |

需求：

- Gateway API v1.6 以上的標準 CRD（含 `ListenerSet`），以及支援 ListenerSet 的 Gateway 實作
- 共用的 Gateway 要允許 Gazette 所在 namespace 的 ListenerSet，例如 `spec.allowedListeners.namespaces.from: All`
- 用 cert-manager 時建議 DNS-01 的 ClusterIssuer（HTTP-01 要額外設定才能經過每個 ListenerSet）

press 會在 Gazette 的 namespace 建立 `manifesto-<gazette>` 的 ListenerSet、HTTPRoute（以及 HTTP 轉 HTTPS 的 `-redirect`）和 Certificate，並在 herald 的 namespace 建立 ReferenceGrant。`kubectl get blog` 的 `Routed` condition 會顯示 Gateway 是否已經接手。

## 開發

```sh
make test   # 單元測試 + envtest（包含 press 與 herald 的端到端測試）
make lint
```

## License

MIT License，見 [LICENSE](LICENSE)。
