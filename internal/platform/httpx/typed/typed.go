// Package typed is the typed handler adapter for httpx. Lives in its own
// sub-package so feature packages can import it without creating a cycle with
// `internal/platform/httpx`'s router.go (which imports the feature packages).
//
// Use this for >90% of handlers — pure func(ctx, Req) (Resp, error) with
// automatic decode/bind/validate/respond. For SSE / file downloads /
// chunked streaming, reach for respond.Wrap(func(w, r) error) directly.
package typed

import (
	"context"
	"net/http"

	"github.com/Everfit-io/go-service-template/internal/platform/httpx/decode"
	"github.com/Everfit-io/go-service-template/internal/platform/httpx/respond"
	"github.com/Everfit-io/go-service-template/internal/platform/validate"
)

// Binder is implemented by request DTOs that need data from outside the JSON
// body — path params, query strings, headers. Bind runs AFTER body decode and
// BEFORE validation, so body + path + query fields all get validated together.
//
//	type GetItemReq struct {
//	    Key string `validate:"required,max=128"`
//	}
//	func (q *GetItemReq) Bind(r *http.Request) error {
//	    q.Key = chi.URLParam(r, "key")
//	    return nil
//	}
type Binder interface {
	Bind(r *http.Request) error
}

// JSON wires the full request lifecycle for typed handlers:
//
//  1. Decode JSON body (size cap, strict, granular PAYLOAD_* errors)
//  2. Bind path/query/headers via the optional Binder interface
//  3. Validate the merged struct with validator/v10 (one pass over all sources)
//  4. Invoke the typed handler — pure func(ctx, Req) (Resp, error)
//  5. Wrap the response via respond.JSON with the given status
//
// Failures route through respond.MapError so every error path produces the
// canonical envelope. Handlers never touch http.ResponseWriter — they're
// trivially unit-testable.
//
// Use for >90% of handlers. For SSE / file downloads / chunked streaming
// reach for respond.Wrap(func(w, r) error) instead.
func JSON[Req any, Resp any](status int, h func(ctx context.Context, req Req) (Resp, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req Req

		// Body decode runs only for body-bearing methods AND when the request
		// actually carries a body. Action endpoints like POST /x/{id}/publish
		// have no body — they shouldn't fail with PAYLOAD_EMPTY.
		if hasBody(r.Method) && r.ContentLength != 0 {
			if err := decode.Body(w, r, &req); err != nil {
				respond.MapError(r.Context(), w, err)
				return
			}
		}
		if err := AutoBind(r, &req); err != nil {
			respond.MapError(r.Context(), w, err)
			return
		}
		if b, ok := any(&req).(Binder); ok {
			if err := b.Bind(r); err != nil {
				respond.MapError(r.Context(), w, err)
				return
			}
		}
		if err := validate.Struct(&req); err != nil {
			respond.MapError(r.Context(), w, err)
			return
		}

		resp, err := h(r.Context(), req)
		if err != nil {
			respond.MapError(r.Context(), w, err)
			return
		}
		// Response types that implement ResponseHeaders() can attach headers
		// (ETag, Cache-Control, Link, …) without changing the handler signature.
		// See respond.HeaderProvider. Headers are written BEFORE WriteHeader.
		if hp, ok := any(resp).(respond.HeaderProvider); ok {
			for k, vs := range hp.ResponseHeaders() {
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
		}
		respond.JSON(w, status, resp)
	}
}

// JSONNoContent is JSON for handlers that return only an error (no response
// body, status 204). Useful for DELETE and idempotent state-change endpoints.
func JSONNoContent[Req any](h func(ctx context.Context, req Req) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req Req

		if hasBody(r.Method) && r.ContentLength != 0 {
			if err := decode.Body(w, r, &req); err != nil {
				respond.MapError(r.Context(), w, err)
				return
			}
		}
		if err := AutoBind(r, &req); err != nil {
			respond.MapError(r.Context(), w, err)
			return
		}
		if b, ok := any(&req).(Binder); ok {
			if err := b.Bind(r); err != nil {
				respond.MapError(r.Context(), w, err)
				return
			}
		}
		if err := validate.Struct(&req); err != nil {
			respond.MapError(r.Context(), w, err)
			return
		}

		if err := h(r.Context(), req); err != nil {
			respond.MapError(r.Context(), w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func hasBody(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return true
	}
	return false
}
