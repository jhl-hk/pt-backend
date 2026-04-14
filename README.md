# PT-Backend: BGP Collector & Prefix Analyzer

基于 [GoBGP](https://github.com/osrg/gobgp) 开发的高性能 BGP 收集器与前缀分析工具。本项目参考了 [bgp.tools](https://bgp.tools/kb/what-is-a-upstream) 的逻辑，自动识别 BGP 路径中的 Tier 1 提供商、上游 (Upstream) 链路及起源 AS (Origin AS)。

## 🚀 核心功能

- **实时监听**: 基于 GoBGP 嵌入式服务器，实时获取 BGP Update 报文。
- **路径分析**:
    - **Tier 1 识别**: 自动匹配全球核心 Tier 1 运营商 (如 NTT, Cogent, GTT 等)。
    - **上游判定**: 根据 `bgp.tools` 标准，识别 Tier 1 与 Origin 之间的中转 AS 为上游。
    - **起源提取**: 准确识别路由的原始发布者 (Origin AS)。
- **模块化架构**: 清晰的代码分层（BGP 处理、数据库、Web 接口、业务逻辑）。
- **REST API**: 提供接口供外部调用分析后的前缀数据。

## 🏗 项目结构

```text
/
├── main.go           # 项目入口，负责模块初始化与启动
├── bgp/              # BGP 核心模块
│   ├── server.go     # GoBGP 服务器管理 (启动、邻居配置)
│   ├── analyzer.go   # BGP 路径核心分析逻辑 (Tier 1 & Upstream)
│   └── analyzer_test.go # 分析逻辑单元测试
├── handler/          # 业务处理层
│   └── bgp_handler.go # 监听 BGP 事件并协调分析与存储
├── db/               # 数据持久化层
│   └── db.go         # 数据库连接与操作 (待扩展)
└── web/              # Web 服务层
    └── server.go     # REST API 接口定义
```

## 🛠 技术栈

- **语言**: Go 1.26+
- **BGP 库**: [osrg/gobgp/v3](https://github.com/osrg/gobgp)
- **协议**: gRPC (内部使用), BGP, HTTP

## 🚦 快速开始

### 1. 环境准备
确保已安装 Go 环境并下载依赖：
```bash
go mod tidy
```

### 2. 运行程序
```bash
go run main.go
```

- **BGP 监听**: 默认监听 `179` 端口（标准 BGP 端口，运行需 `sudo` 权限）。
- **Web API**: 默认运行在 `http://localhost:8080`。

### 3. 配置 BGP 邻居
在 `main.go` 中添加您的邻居信息：
```go
collector.AddNeighbor(ctx, "192.168.1.1", 65001)
```

## 🔍 分析逻辑说明

本项目遵循 **bgp.tools** 的上游判定准则：
- **Tier 1 定义**: 包含 AS174, AS1299, AS2914, AS3356 等 13 个核心自治系统。
- **Upstream 判断**: 在 `AS_PATH` 中，任何位于 Tier 1 AS 和 Origin AS 之间的 ASN 都被视为上游。
- **示例**: 路径为 `[Peer, 2914 (Tier1), 4809 (CN2), 4134 (Chinatelecom), Origin]`
    - **Tier 1**: AS2914 (NTT)
    - **Origin**: `Origin`
    - **Upstream Chain**: `[AS2914, AS4809, AS4134]`
    - **Direct Upstream**: `AS4134`

## 🧪 运行测试
验证分析逻辑是否准确：
```bash
go test -v ./bgp/...
```

## 📄 开源协议
[MIT License](LICENSE)
