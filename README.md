# multimo-backend

Multimo 的后端服务：GoFrame v2.10.4 + PostgreSQL（pgvector）+ FFmpeg。

这个仓库是 [multimo](https://github.com/MeVeryHandsome/multimo) 主仓库的子模块。部署、环境变量模板和测试数据都在主仓库里。

## 本地运行

需要 Go 1.24+、FFmpeg，以及一个装了 pgvector 的 PostgreSQL 16。

```bash
set -a; source ../.env; set +a   # 主仓库根目录下的 .env
make run
```

- 健康检查：`GET http://localhost:8000/api/v1/system/health`
- OpenAPI：`http://localhost:8000/api.json`
- Swagger UI：`http://localhost:8000/swagger`

## 目录

| 路径 | 内容 |
| :--- | :--- |
| `api/<module>/v1` | 接口请求/响应定义，Swagger 从这里生成 |
| `internal/cmd` | 启动入口与子命令 |
| `internal/config` | 从环境变量读取全部运行参数 |
| `internal/controller` | HTTP 控制器 |
| `internal/service` | 业务模块：接入、抽帧、识别、定位、检索、问答、评测 |
| `internal/pkg` | 基础设施：LLM 网关、FFmpeg、文件解析、goroutine 池 |
| `manifest/config` | GoFrame 服务与日志配置（不含密钥） |
| `manifest/sql` | 数据库迁移 |
| `manifest/docker` | Dockerfile |

`internal/service` 下直接放业务包，没有使用 `gf gen service` 生成接口层。
