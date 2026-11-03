# multimo-backend

Multimo 的后端服务：GoFrame v2.10.4 + PostgreSQL 16（pgvector）+ FFmpeg。

这个仓库是 [multimo](https://github.com/MeVeryHandsome/multimo) 主仓库的子模块。部署、环境变量模板和测试数据都在主仓库里。

## 本地运行

需要：

- Go 1.24+
- FFmpeg
- poppler-utils（`pdftotext`、`pdfinfo`）
- catdoc
- PostgreSQL 16，并安装 pgvector

所有配置都从环境变量读取，模板见主仓库的 `.env.example`。

```bash
set -a; source ../.env; set +a
```

```bash
make run
```

启动时会自动执行数据库迁移，并把上次中断的任务放回队列。

| 地址 | 内容 |
| :--- | :--- |
| `GET /api/v1/system/health` | 健康检查：版本、数据库、已配置的模型 |
| `/swagger` | Swagger UI |
| `/api.json` | OpenAPI 文档 |

检查三个模型端点是否可用（会记入 `ai_call_log`）：

```bash
./bin/multimo ai-check -i ../testdata/image/visdrone_det_val/images/0000001_05499_d_0000010.jpg
```

## 接口

| 方法 | 路径 | 说明 |
| :--- | :--- | :--- |
| POST | `/api/v1/assets` | 上传文件（multipart：`file`、可选 `sidecar`、`collection`、`type`） |
| POST | `/api/v1/assets/register` | 登记 `TESTDATA_DIR` 中已有的文件，不复制 |
| GET | `/api/v1/assets` | 素材列表，可按类型、集合、状态、文件名筛选 |
| GET | `/api/v1/assets/{id}` | 素材详情，含处理任务和派生数据数量 |
| GET | `/api/v1/assets/{id}/thumbnail` | 缩略图（JPEG） |
| POST | `/api/v1/assets/{id}/reprocess` | 重新处理 |
| DELETE | `/api/v1/assets/{id}` | 删除记录和派生文件 |
| GET | `/api/v1/assets/{id}/keyframes` | 关键帧列表：描述、标签、定位、检测框数量 |
| GET | `/api/v1/keyframes/{id}` | 关键帧详情：检测框（像素坐标）、分析结果、定位候选 |
| GET | `/api/v1/keyframes/{id}/image` | 关键帧图片（JPEG） |
| GET | `/api/v1/assets/{id}/track` | 飞行轨迹（来自配套飞行记录，已对齐到视频时间轴） |
| POST | `/api/v1/pipeline/rerun` | 按条件批量重跑某个处理阶段 |
| GET | `/api/v1/tasks` | 处理任务列表 |
| GET | `/api/v1/system/ai-calls` | AI 调用汇总（token、延迟、降级次数）和最近记录 |
| GET | `/api/v1/system/stats` | 数据概况 |

## 处理链

`ingest → keyframes → analyze → geo`，卫星底图另加 `basemap`，每一步是 `tasks` 表中的一个任务：

| 阶段 | 做什么 |
| :--- | :--- |
| ingest | 识别类型、元数据、缩略图；文档抽文本并切块 |
| keyframes | 视频按内容变化选帧（图片/遥感一帧）；多模态向量；相似帧去重 |
| analyze | VLM 描述、场景标签、目标框（0~1000 相对坐标换算为像素）、地标、文字、地点猜测；文本向量 |
| geo | 飞行记录 / EXIF / 容器 GPS → 地名库粗定位 → 底图视觉匹配 + 航迹平滑 |
| basemap | 大幅 GeoTIFF 按 250/300/400 m 切块并计算多模态向量 |

## 测试

单元测试不需要外部服务。数据库测试和用到真实文件的测试，需要本地 Postgres 和主仓库的 `testdata/`，条件不满足时自动跳过。每个数据库测试都在独立的临时 schema 里运行，结束后删除。

```bash
set -a; source ../.env; set +a
```

```bash
go test -race ./...
```

## 目录

| 路径 | 内容 |
| :--- | :--- |
| `api/<module>/v1` | 接口请求/响应定义，Swagger 从这里生成 |
| `internal/cmd` | 启动入口、`ai-check` 子命令 |
| `internal/config` | 从环境变量读取全部运行参数 |
| `internal/controller` | HTTP 控制器 |
| `internal/service/asset` | 素材存储与查询 |
| `internal/service/ingest` | 上传、登记、第一步处理（元数据、缩略图、文档切块） |
| `internal/service/pipeline` | 持久化任务队列：领取、重试、延后、失败处理 |
| `internal/service/keyframe` | 关键帧选取与图像向量 |
| `internal/service/analyze` | VLM 关键帧分析与检测框 |
| `internal/service/geo` | 飞行记录对齐、地名库、底图索引、视觉定位与航迹平滑 |
| `internal/pkg/llm` | AI 网关：VLM、Embedding、Decision，重试、降级、调用日志 |
| `internal/pkg/media` | ffprobe 元数据、抽帧、缩略图 |
| `internal/pkg/parser` | 文件类型识别；图像 EXIF；PDF、DOC、DOCX、TXT、CSV、GeoJSON 文本 |
| `internal/pkg/geotiff` | GeoTIFF 地理参考与分块读取（大幅卫星图） |
| `internal/pkg/workerpool` | 有界 goroutine 池 |
| `internal/pkg/migrate` | 内嵌 SQL 迁移 |
| `manifest/sql` | 数据库迁移脚本 |
| `manifest/docker` | Dockerfile |

`internal/service` 下直接放业务包，没有使用 `gf gen service` 生成接口层。

## AI 网关的行为

- **重试：** 每次调用最多尝试 `1 + AI_MAX_RETRIES` 次。网络错误、超时、429、5xx 会按指数退避加随机抖动重试，429 优先按 `Retry-After` 等待；其他 4xx 不重试。JSON 模式下，如果回复里没有 JSON 对象，也算可重试。
- **并发：** 每个端点单独限制并发数，即 `*_MAX_CONCURRENCY`。
- **降级：** 全部尝试失败且 `AI_FALLBACK_ENABLED=true` 时，走本地规则。
  - VLM：由调用方提供兜底逻辑。
  - Embedding：改用本地特征哈希向量，模型名记为 `local-hash-v1`。同一批向量要么全部来自真实模型，要么全部来自哈希，不会混用。
  - Decision：按关键词重合度给出判断。
- **日志：** 每次调用写一条 `ai_call_log`，记录端点、模型、用途、状态、尝试次数、延迟、token 用量，以及截断后的输入输出摘要。图片在摘要里只写尺寸和大小，API key 不会写进日志。
