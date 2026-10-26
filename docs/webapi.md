# Web API 兼容页

Web API 文档已拆分为两个公开入口：

- [对外 API v1](api.md)：推荐新集成使用。覆盖完整 `/api/v1` 端点、管理员签名、普通用户 Token、统一响应信封、JSON 请求体与分页。
- [Web API 鉴权](extend/api.md)：签名、时间戳、加密 `auth_key` 和调用示例。
- [旧版 Web API 清单](extend/webapi.md)：客户端、用户、隧道、域名和全局设置的历史路径与兼容约定。

新系统集成请使用 `/api/v1`；已经依赖旧 `/auth/*`、`/client/*`、`/index/*`、`/user/*` 或 `/global/*` 的调用可以继续使用，升级 1.1.33 不需要修改。旧链接保留用于兼容外部引用。
