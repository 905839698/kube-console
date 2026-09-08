package node

import "embed"

//go:embed nodes
// NodesFS 内嵌的节点插件目录（19 个插件 × node.yaml/schema.json/task.yaml/result.yaml），
// 随二进制分发，单项目自包含、无需外部挂载。
var NodesFS embed.FS
