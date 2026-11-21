# CRDs for envtest

Third-party CRDs that press's envtest suite installs next to Manifesto's own:

- `gateway.networking.k8s.io_*.yaml`: copied unchanged from
  `sigs.k8s.io/gateway-api` v1.7.0 (`config/crd/standard`), matching `go.mod`.
- `cert-manager.io_certificates.yaml`: a minimal stand-in for cert-manager's
  Certificate CRD (schema not validated), enough to check what press applies.
