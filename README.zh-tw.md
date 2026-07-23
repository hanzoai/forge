# Hanzo Git

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](https://opensource.org/licenses/MIT "License: MIT")

Hanzo 自架的 Git 平台 — Git 託管、程式碼審查、問題追蹤、套件庫，以及相容於
GitHub Actions 的 CI/CD，並透過 hanzo.id OIDC 原生整合 IAM。服務位於
[git.hanzo.ai](https://git.hanzo.ai)。

本專案是 [Gitea](https://gitea.com)（MIT）的白牌分支。上游版權與授權完整保留於
[LICENSE](LICENSE)；身分驗證改接 Hanzo IAM 而非本機帳號，產品品牌為 Hanzo Git。

## 建置與部署

如同每個 Hanzo 儲存庫，只有一種方式：根目錄的 `hanzo.yml` 宣告映像檔與測試；
約七行的 `.github/workflows/cicd.yml` 匯入 `hanzoai/ci`，於 arc pool 建置並推送
`ghcr.io/hanzoai/git`。切勿在本機建置映像檔。

執行中的服務定義於 `hanzoai/universe`（`git` operator App CR）：單寫入者 SQLite
部署，組態經由 `GIT__<section>__<KEY>` 環境變數契約注入，OIDC 登入對接 hanzo.id。

## 開發

本機環境請見 [docs/development.md](docs/development.md)。建置完成後，執行
`./gitea web` 啟動伺服器，或 `./gitea help` 檢視所有指令。
