# portal

Customer portal for Acme: a React front end (`web/`), a Go API (`server/`) and
a few Python operations scripts (`scripts/`).

## Develop

```sh
cd server && go run ./cmd/api        # API on :8080
cd web && npm install && npm run dev # UI on :5173, proxies /api to :8080
```

## Test

```sh
cd server && go test ./...
cd web && npm test
python -m unittest discover scripts
```
