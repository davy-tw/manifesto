# 在 k3s 上部署 Manifesto

這份文件是在 k3s（內建 Traefik）上做小規模正式環境測試的做法。重點是 k3s 內建的 Traefik 與 ListenerSet 模式不相容，所以 press 改用 `gateway` 路由模式。

## 為什麼用 `gateway` 模式

press 預設的 `listenerset` 模式會替每個 Gazette 建立一個 ListenerSet。在 Traefik 上這行不通：

- Traefik 到 v3.8 才支援 ListenerSet，而 v3.8 目前還是 RC；k3s 內建的版本更舊
- Traefik 的 Gateway listener port 必須等於它的 entrypoint port（k3s 預設 `web` 是 8000、`websecure` 是 8443），不是 80 和 443

`gateway` 模式下，press 只在每個 Gazette 的 namespace 建立一個 HTTPRoute，直接掛到你準備好的 Gateway 上，並在 herald 的 namespace 建立 ReferenceGrant。TLS 由 Gateway 的 HTTPS listener 負責，例如一張萬用字元憑證。

## 1. 先確認叢集的狀態

```sh
kubectl -n kube-system get deploy traefik -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}'
kubectl get crd | grep gateway.networking.k8s.io
# press 需要 HTTPRoute 與 ReferenceGrant 的 v1 版本
kubectl get crd httproutes.gateway.networking.k8s.io      -o jsonpath='{.spec.versions[*].name}{"\n"}'
kubectl get crd referencegrants.gateway.networking.k8s.io -o jsonpath='{.spec.versions[*].name}{"\n"}'
```

如果沒有 Gateway API 的 CRD，或 ReferenceGrant 沒有提供 `v1`，請安裝 Gateway API 的 standard CRD（press 用 v1.6.3 開發）：

```sh
kubectl apply --server-side -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.6.3/standard-install.yaml
```

## 2. 啟用 Traefik 的 Gateway API provider

k3s 透過 `HelmChartConfig` 調整內建的 Traefik：

```yaml
# /var/lib/rancher/k3s/server/manifests/traefik-config.yaml
apiVersion: helm.cattle.io/v1
kind: HelmChartConfig
metadata:
  name: traefik
  namespace: kube-system
spec:
  valuesContent: |-
    providers:
      kubernetesGateway:
        enabled: true
```

確認 Traefik 建立了 GatewayClass：

```sh
kubectl get gatewayclass
```

## 3. 建立給 Manifesto 用的 Gateway

listener 的 port 用 Traefik 的 entrypoint port，並允許所有 namespace 的 HTTPRoute 掛上來。`gatewayClassName` 填上一步看到的名稱（通常是 `traefik`）。

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: manifesto
  namespace: kube-system
spec:
  gatewayClassName: traefik
  listeners:
  - name: websecure
    protocol: HTTPS
    port: 8443
    hostname: "*.blog.example.io"
    tls:
      mode: Terminate
      certificateRefs:
      - name: blog-wildcard-tls     # 見第 4 步
    allowedRoutes:
      namespaces:
        from: All
  - name: web
    protocol: HTTP
    port: 8000
    hostname: "*.blog.example.io"
    allowedRoutes:
      namespaces:
        from: All
```

`from: All` 讓任何 namespace 都能掛 HTTPRoute 到這個 Gateway。正式環境可以改成 `from: Selector`，只允許有特定 label 的 namespace。

如果 Gazette 的網域不在同一個萬用字元底下，就替每個網域各加一組 listener。

## 4. TLS 憑證

萬用字元憑證只能用 DNS-01 驗證。用 cert-manager 的話：

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: blog-wildcard
  namespace: kube-system
spec:
  secretName: blog-wildcard-tls
  dnsNames: ["*.blog.example.io"]
  issuerRef:
    kind: ClusterIssuer
    name: letsencrypt-dns01         # 你的 DNS-01 ClusterIssuer
```

HTTP 轉 HTTPS 建議在 Traefik 的 `web` entrypoint 設定轉址。Helm chart 的設定寫法依版本不同，請參考你的 Traefik chart 版本的 `ports.web` 說明。

## 5. 部署 Manifesto 並啟用路由

k3s 用 containerd。沒有 registry 時，可以把 image 直接匯入節點：

```sh
make docker-build IMG=manifesto-press:dev
make docker-build COMPONENT=herald IMG=manifesto-herald:dev
docker save manifesto-press:dev manifesto-herald:dev | sudo k3s ctr images import -
```

在 `config/manager/manager.yaml` 的 press 參數加上：

```yaml
          - --routing-mode=gateway
          - --gateway-name=manifesto
          - --gateway-namespace=kube-system
          - --gateway-section=websecure
```

然後部署：

```sh
make deploy IMG=manifesto-press:dev HERALD_IMG=manifesto-herald:dev
```

`gateway` 模式不會建立 Certificate，所以不用設定 `--cluster-issuer`。

## 6. 確認

```sh
kubectl get blog -A                                   # Ready 與 Routed 都應該是 True
kubectl get httproute -A -l app.kubernetes.io/managed-by=manifesto-press
kubectl -n manifesto-system get referencegrant
curl -sI https://alice.blog.example.io/
```

`Routed` 是 False 時，message 會帶 Traefik 回報的原因。常見的原因是 Gateway listener 不允許該 namespace，或 hostname 對不上。

## CI 上的對應測試

`make test-e2e-k3d` 用 k3d 起一個 k3s 叢集，照這份文件的第 1 到 3 步與第 5 步設定（只用 HTTP listener，不測 TLS），再確認 Gazette 的 `Routed` 為 True、從叢集內經過 Traefik 讀得到文章。設定檔在 `test/e2e/k3d/`。

## 之後的里程碑要注意的地方

- **M3 媒體**：k3s 預設的 `local-path` StorageClass 只支援 RWO，darkroom 用 PVC driver 時只能跑一個副本
- **單節點**：herald 預設 2 個副本，可以留著讓 rolling update 不中斷
