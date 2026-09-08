package handler

import (
	"kube-console/server/internal/ci/credential"
	"kube-console/server/internal/ci/pipeline"
	"kube-console/server/internal/ci/pipeline/compiler"
	"kube-console/server/internal/ci/pipeline/model"
)

// cicd_types.go handler 与 ci 包类型之间的薄适配层。

type ciGraphBody struct {
	Graph *model.Graph `json:"graphJson" binding:"required"`
}

type ciCredCreateReq = credential.CreateReq
type ciCredUpdateReq = credential.UpdateReq

func ciPipelineCreateReq(cluster string, projectID uint, name, desc string) pipeline.CreateReq {
	return pipeline.CreateReq{ClusterName: cluster, ProjectID: projectID, Name: name, Description: desc}
}

func ciPipelineUpdateReq(name, desc *string) pipeline.UpdateReq {
	return pipeline.UpdateReq{Name: name, Description: desc}
}

func ciParseGraph(graphJSON string) (*model.Graph, error) {
	return pipeline.ParseGraph(graphJSON)
}

func ciCompileToYAML(spec *compiler.PipelineSpec, _ string) (string, error) {
	return compiler.ToYAML(spec)
}
