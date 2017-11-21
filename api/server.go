package api

import (
	"net/http"

	"sync"

	"github.com/rancher/norman/api/builtin"
	"github.com/rancher/norman/api/handler"
	"github.com/rancher/norman/api/writer"
	"github.com/rancher/norman/authorization"
	"github.com/rancher/norman/httperror"
	"github.com/rancher/norman/parse"
	"github.com/rancher/norman/store/wrapper"
	"github.com/rancher/norman/types"
)

type StoreWrapper func(types.Store) types.Store

type Parser func(rw http.ResponseWriter, req *http.Request) (*types.APIContext, error)

type Server struct {
	initBuiltin                 sync.Once
	IgnoreBuiltin               bool
	Parser                      Parser
	Resolver                    parse.ResolverFunc
	SubContextAttributeProvider types.SubContextAttributeProvider
	ResponseWriters             map[string]ResponseWriter
	schemas                     *types.Schemas
	QueryFilter                 types.QueryFilter
	StoreWrapper                StoreWrapper
	Defaults                    Defaults
}

type Defaults struct {
	ActionHandler types.ActionHandler
	ListHandler   types.RequestHandler
	LinkHandler   types.RequestHandler
	CreateHandler types.RequestHandler
	DeleteHandler types.RequestHandler
	UpdateHandler types.RequestHandler
	Store         types.Store
	ErrorHandler  types.ErrorHandler
}

func NewAPIServer() *Server {
	s := &Server{
		schemas: types.NewSchemas(),
		ResponseWriters: map[string]ResponseWriter{
			"json": &writer.JSONResponseWriter{},
			"html": &writer.HTMLResponseWriter{},
		},
		SubContextAttributeProvider: &parse.DefaultSubContextAttributeProvider{},
		Resolver:                    parse.DefaultResolver,
		Defaults: Defaults{
			CreateHandler: handler.CreateHandler,
			DeleteHandler: handler.DeleteHandler,
			UpdateHandler: handler.UpdateHandler,
			ListHandler:   handler.ListHandler,
			LinkHandler: func(*types.APIContext) error {
				return httperror.NewAPIError(httperror.NotFound, "Link not found")
			},
			ErrorHandler: httperror.ErrorHandler,
		},
		StoreWrapper: wrapper.Wrap,
		QueryFilter:  handler.QueryFilter,
	}

	s.Parser = s.parser
	return s
}

func (s *Server) parser(rw http.ResponseWriter, req *http.Request) (*types.APIContext, error) {
	ctx, err := parse.Parse(rw, req, s.schemas, s.Resolver)
	ctx.ResponseWriter = s.ResponseWriters[ctx.ResponseFormat]
	if ctx.ResponseWriter == nil {
		ctx.ResponseWriter = s.ResponseWriters["json"]
	}

	if ctx.QueryFilter == nil {
		ctx.QueryFilter = s.QueryFilter
	}

	if ctx.SubContextAttributeProvider == nil {
		ctx.SubContextAttributeProvider = s.SubContextAttributeProvider
	}

	ctx.AccessControl = &authorization.AllAccess{}

	return ctx, err
}

func (s *Server) AddSchemas(schemas *types.Schemas) error {
	if schemas.Err() != nil {
		return schemas.Err()
	}

	s.initBuiltin.Do(func() {
		if s.IgnoreBuiltin {
			return
		}
		for _, schema := range builtin.Schemas.Schemas() {
			s.setupDefaults(schema)
			s.schemas.AddSchema(schema)
		}
	})

	for _, schema := range schemas.Schemas() {
		s.setupDefaults(schema)
		s.schemas.AddSchema(schema)
	}

	return s.schemas.Err()
}

func (s *Server) setupDefaults(schema *types.Schema) {
	if schema.ActionHandler == nil {
		schema.ActionHandler = s.Defaults.ActionHandler
	}

	if schema.Store == nil {
		schema.Store = s.Defaults.Store
	}

	if schema.ListHandler == nil {
		schema.ListHandler = s.Defaults.ListHandler
	}

	if schema.LinkHandler == nil {
		schema.LinkHandler = s.Defaults.LinkHandler
	}

	if schema.CreateHandler == nil {
		schema.CreateHandler = s.Defaults.CreateHandler
	}

	if schema.UpdateHandler == nil {
		schema.UpdateHandler = s.Defaults.UpdateHandler
	}

	if schema.DeleteHandler == nil {
		schema.DeleteHandler = s.Defaults.DeleteHandler
	}

	if schema.ErrorHandler == nil {
		schema.ErrorHandler = s.Defaults.ErrorHandler
	}

	if schema.Store != nil && s.StoreWrapper != nil {
		schema.Store = s.StoreWrapper(schema.Store)
	}
}

func (s *Server) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	if apiResponse, err := s.handle(rw, req); err != nil {
		s.handleError(apiResponse, err)
	}
}

func (s *Server) handle(rw http.ResponseWriter, req *http.Request) (*types.APIContext, error) {
	apiRequest, err := s.Parser(rw, req)
	if err != nil {
		return apiRequest, err
	}

	if err := CheckCSRF(rw, req); err != nil {
		return apiRequest, err
	}

	if err := addCommonResponseHeader(apiRequest); err != nil {
		return apiRequest, err
	}

	action, err := ValidateAction(apiRequest)
	if err != nil {
		return apiRequest, err
	}

	if apiRequest.Schema == nil {
		return apiRequest, nil
	}

	if action == nil && apiRequest.Type != "" {
		var handler types.RequestHandler
		switch apiRequest.Method {
		case http.MethodGet:
			handler = apiRequest.Schema.ListHandler
		case http.MethodPost:
			handler = apiRequest.Schema.CreateHandler
		case http.MethodPut:
			handler = apiRequest.Schema.UpdateHandler
		case http.MethodDelete:
			handler = apiRequest.Schema.DeleteHandler
		}

		if err != nil {
			return apiRequest, err
		}

		if handler == nil {
			return apiRequest, httperror.NewAPIError(httperror.NotFound, "")
		}

		return apiRequest, handler(apiRequest)
	} else if action != nil {
		return apiRequest, handleAction(action, apiRequest)
	}

	return apiRequest, nil
}

func handleAction(action *types.Action, request *types.APIContext) error {
	return request.Schema.ActionHandler(request.Action, action, request)
}

func (s *Server) handleError(apiRequest *types.APIContext, err error) {
	if apiRequest.Schema == nil {
		s.Defaults.ErrorHandler(apiRequest, err)
	} else if apiRequest.Schema.ErrorHandler != nil {
		apiRequest.Schema.ErrorHandler(apiRequest, err)
	}
}
