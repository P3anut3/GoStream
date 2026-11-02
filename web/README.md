# 构建产物与历史页面

当前页面源码位于 `frontend/`，通过 `npm --prefix frontend run build` 输出到 `web/dist/`。Gin 只提供 `web/dist` 中的首页、assets 和默认封面。

本目录旧 `index.html`、`app.js`、`style.css`、`cover.svg` 暂时保留为迁移前的学习对照；不会被当前路由或 Docker 镜像提供。`scripts/web_*_test.cjs` 对应这些历史页面，不能用它们的结果证明 Vue 页面正确。当前回归测试位于 `frontend/tests/`。

`dist` 是可重建的生成目录，已被 Git 排除。不要手工修改其中的文件。
