package pipeline

import (
	"kube-console/server/internal/ci/nodetype"
	"kube-console/server/internal/ci/pipeline/compiler"
	"kube-console/server/internal/ci/pipeline/model"
	"kube-console/server/internal/ci/pipeline/validator"
	"kube-console/server/internal/ci/errcode"
)

// Service 聚合 validator + compiler，对外提供“校验 / 编译”能力。
// handler 通过 Deps.Pipe 调用，避免各自 new。
type Service struct {
	Validator *validator.Validator
	Compiler  *compiler.Compiler
	Namespace string
}

// NewService 装配 pipeline 服务。
func NewService(reg nodetype.Registry, namespace string) *Service {
	return &Service{
		Validator: validator.New(reg),
		Compiler:  compiler.New(reg),
		Namespace: namespace,
	}
}

// Validate 校验 DSL。
func (s *Service) Validate(g *model.Graph) validator.Result {
	return s.Validator.Validate(g)
}

// Compile 校验通过后编译为 Pipeline（先校验，失败即返回）。
func (s *Service) Compile(g *model.Graph) (*compiler.PipelineSpec, error) {
	res := s.Validator.Validate(g)
	if !res.Valid {
		return nil, &CompileError{Errors: res.Errors}
	}
	return s.Compiler.Compile(g, s.Namespace)
}

// CompileError 携带可读错误列表。
type CompileError struct{ Errors []string }

func (e *CompileError) Error() string {
	return "pipeline 校验失败: " + join(e.Errors, "; ")
}

// Code 映射业务码：DSL 校验失败是参数问题（400），不是服务端错误。
func (e *CompileError) Code() errcode.Code { return errcode.InvalidParam }

func join(ss []string, sep string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}
