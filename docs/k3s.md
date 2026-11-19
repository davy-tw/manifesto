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
make docker-build COMPONENT=darkroom IMG=manifesto-darkroom:dev
make docker-build COMPONENT=desk IMG=manifesto-desk:dev
docker save manifesto-press:dev manifesto-herald:dev manifesto-darkroom:dev manifesto-desk:dev | sudo k3s ctr images import -
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
make deploy IMG=manifesto-press:dev HERALD_IMG=manifesto-herald:dev DARKROOM_IMG=manifesto-darkroom:dev \
  DESK_IMG=manifesto-desk:dev
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

## 7. 媒體（darkroom）

darkroom 的 PVC 用 k3s 預設的 `local-path`，資料在節點的 `/var/lib/rancher/k3s/storage` 底下，備份時要一起備份（文章本身在 etcd，用 k3s 的 etcd snapshot）。`local-path` 只有 RWO，所以 darkroom 只跑一個副本、更新時先停再起。

讓訪客讀得到檔案：替媒體網域建一條 HTTPRoute，掛到同一個 Gateway（`*.blog.example.io` 底下的網域就能共用萬用字元憑證）：

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: media
  namespace: manifesto-system
spec:
  parentRefs:
  - {name: manifesto, namespace: kube-system, sectionName: websecure}
  hostnames: ["media.blog.example.io"]
  rules:
  - backendRefs:
    - {name: manifesto-darkroom, port: 80}
```

再建立 ClusterArchive，`publicURL: https://media.blog.example.io`。

## 8. 管理介面（desk）與 OIDC

desk 用使用者本人的 OIDC ID token 呼叫 API server，所以 k3s 的 API server 要信任 OIDC 提供者。在 OIDC 提供者註冊一個 client：client ID `manifesto-desk`，redirect URL `https://desk.blog.example.io/auth/callback`，並讓 ID token 帶 `email` 與 `groups` claim。

把 AuthenticationConfiguration 放到 server 節點上：

```yaml
# /etc/rancher/k3s/authn.yaml
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

在 `/etc/rancher/k3s/config.yaml` 加上，然後重新啟動 k3s（`systemctl restart k3s`）：

```yaml
kube-apiserver-arg:
  - authentication-config=/etc/rancher/k3s/authn.yaml
```

API server 會定期重新讀取這個檔案，之後改 issuer 或 claim 對應不用再重啟。用 `kubectl auth whoami`（帶上 OIDC token）可以確認對應出來的使用者名稱與群組。

設定 desk：在 `config/desk/deployment.yaml` 把 `--public-url` 改成 `https://desk.blog.example.io`，打開 `--oidc-issuer=https://auth.example.io`。confidential client 再建立 client secret：

```sh
kubectl -n manifesto-system create secret generic manifesto-desk-oidc --from-literal=clientSecret=<secret>
```

替 desk 建一條 HTTPRoute（同樣用萬用字元憑證）。desk 的 session cookie 只走 HTTPS，所以要從 `websecure` 連：

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: desk
  namespace: manifesto-system
spec:
  parentRefs:
  - {name: manifesto, namespace: kube-system, sectionName: websecure}
  hostnames: ["desk.blog.example.io"]
  rules:
  - backendRefs:
    - {name: manifesto-desk, port: 80}
```

最後用 RoleBinding 把群組綁到角色（見 README 的「權限」）：

```sh
kubectl create rolebinding editors -n blog-alice --clusterrole=manifesto-editor --group=oidc:blog-alice-editors
```

## CI 上的對應測試

`make test-e2e-k3d` 用 k3d 起一個 k3s 叢集，照這份文件的第 1 到 3 步與第 5 步設定（只用 HTTP listener，不測 TLS），再確認 Gazette 的 `Routed` 為 True、從叢集內經過 Traefik 讀得到文章，並上傳一個 Print 經過 darkroom 存取。desk 以 ServiceAccount token 代替 OIDC token，確認它用呼叫者的權限讀寫與預覽（OIDC 登入流程由 envtest 與 Playwright 的 `make test-ui` 涵蓋）。設定檔在 `test/e2e/k3d/`。

## 之後的里程碑要注意的地方

- **單節點**：herald 預設 2 個副本，可以留著讓 rolling update 不中斷
