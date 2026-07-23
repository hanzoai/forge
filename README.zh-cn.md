# Hanzo Git

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](https://opensource.org/licenses/MIT "License: MIT")

Hanzo 自托管的 Git 平台 — Git 托管、代码审查、问题跟踪、包仓库，以及兼容于
GitHub Actions 的 CI/CD，并通过 hanzo.id OIDC 原生集成 IAM。服务位于
[git.hanzo.ai](https://git.hanzo.ai)。

本项目是 [Gitea](https://gitea.com)（MIT）的白牌分支。上游版权与许可完整保留于
[LICENSE](LICENSE)；身份验证改接 Hanzo IAM 而非本地账号，产品品牌为 Hanzo Git。

## 构建与部署

如同每个 Hanzo 仓库，只有一种方式：根目录的 `hanzo.yml` 声明镜像与测试；
约七行的 `.github/workflows/cicd.yml` 导入 `hanzoai/ci`，在 arc pool 构建并推送
`ghcr.io/hanzoai/git`。切勿在本地构建镜像。

运行中的服务定义于 `hanzoai/universe`（`git` operator App CR）：单写入者 SQLite
部署，配置经由 `GIT__<section>__<KEY>` 环境变量契约注入，OIDC 登录对接 hanzo.id。

## 开发

本地环境请见 [docs/development.md](docs/development.md)。构建完成后，运行
`./gitea web` 启动服务器，或 `./gitea help` 查看所有命令。
