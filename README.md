# Manifesto

Manifesto 是完全 Kubernetes 原生的部落格引擎：文章、分類、主題和媒體都是 CRD，系統邏輯由 operator 處理，權限交給 K8s RBAC。設計見 [`docs/design.md`](docs/design.md)。

目前進度：**M1（MVP）**。`kubectl apply` 一篇 Dispatch，排程時間到自動發布，瀏覽器打開就看得到。

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
kubectl apply -n blog-alice -f config/samples/manifesto_v1alpha1_gazette.yaml
kubectl apply -n blog-alice -f config/samples/manifesto_v1alpha1_dispatch.yaml
kubectl get manifesto -n blog-alice
```

sample 的 Gazette 網域是 `alice.localtest.me`（解析到 127.0.0.1），所以可以直接開 <http://alice.localtest.me:8080/>，或是：

```sh
curl -H 'Host: alice.localtest.me' http://localhost:8080/posts/hello-k8s
```

把 sample Dispatch 的 `publishAt` 改成幾分鐘後，可以看到它先是 `Embargoed`（herald 回 404），時間到自動變成 `Published`。

## 部署

```sh
make docker-build docker-push IMG=<registry>/manifesto-press:<tag>
make docker-build docker-push COMPONENT=herald IMG=<registry>/manifesto-herald:<tag>
make deploy IMG=<registry>/manifesto-press:<tag> HERALD_IMG=<registry>/manifesto-herald:<tag>
```

herald 的 Service 是 `manifesto-system/manifesto-herald`。M1 還不會自動建立 HTTPRoute，請自行把 Gateway 指過去（M2 會由 press 依 Gazette 的網域建立）。

## 開發

```sh
make test   # 單元測試 + envtest（包含 press 與 herald 的端到端測試）
make lint
```

## License

MIT License，見 [LICENSE](LICENSE)。
