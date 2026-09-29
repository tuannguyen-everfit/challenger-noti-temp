package typed

import (
	"fmt"
	"net/http"
	"reflect"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/Everfit-io/go-service-template/internal/platform/apperr"
	"github.com/Everfit-io/go-service-template/internal/platform/localization"
)

// AutoBind reflects over dst's exported fields and populates them from
// request sources declared via struct tags:
//
//	type Req struct {
//	    Key       string `path:"key"       validate:"required,max=128"`
//	    Limit     int    `query:"limit"    validate:"min=1,max=100"`
//	    UserAgent string `header:"User-Agent"`
//	}
//
// Empty query/header values are skipped (zero-value preserved so a `*int`
// field stays nil instead of becoming 0). For complex inputs (multi-value
// lists, custom deserialization) implement the Binder interface — Binder
// runs AFTER AutoBind, so it can override any field.
func AutoBind(r *http.Request, dst any) error {
	v := reflect.ValueOf(dst)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return fmt.Errorf("typed: AutoBind requires a non-nil pointer, got %T", dst)
	}
	v = v.Elem()
	if v.Kind() != reflect.Struct {
		return nil
	}

	t := v.Type()
	for i := range t.NumField() {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		if err := bindField(r, sf, v.Field(i)); err != nil {
			return err
		}
	}
	return nil
}

// bindField applies all source tags (path/query/header) for one struct field.
func bindField(r *http.Request, sf reflect.StructField, fv reflect.Value) error {
	for _, src := range bindSources {
		name, ok := sf.Tag.Lookup(src.tag)
		if !ok || name == "" {
			continue
		}
		raw := src.read(r, name)
		if src.skipEmpty && raw == "" {
			continue
		}
		if err := setFromString(fv, raw); err != nil {
			return wrapBindErr(sf.Name, src.tag, err)
		}
	}
	return nil
}

// bindSources defines the supported tag→request-source mapping. Order:
// path → query → header. Last-non-empty wins per field if multiple sources
// are tagged (rare).
var bindSources = []struct {
	tag       string
	skipEmpty bool
	read      func(r *http.Request, name string) string
}{
	{tag: "path", skipEmpty: false, read: chi.URLParam},
	{tag: "query", skipEmpty: true, read: func(r *http.Request, name string) string {
		return r.URL.Query().Get(name)
	}},
	{tag: "header", skipEmpty: true, read: func(r *http.Request, name string) string {
		return r.Header.Get(name)
	}},
}

// setFromString parses raw into fv. Pointer fields are auto-allocated.
func setFromString(fv reflect.Value, raw string) error {
	if fv.Kind() == reflect.Pointer {
		if fv.IsNil() {
			fv.Set(reflect.New(fv.Type().Elem()))
		}
		return setFromString(fv.Elem(), raw)
	}

	//exhaustive:ignore default branch handles unsupported kinds explicitly.
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(raw)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, fv.Type().Bits())
		if err != nil {
			return fmt.Errorf("expected integer, got %q", raw)
		}
		fv.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(raw, 10, fv.Type().Bits())
		if err != nil {
			return fmt.Errorf("expected unsigned integer, got %q", raw)
		}
		fv.SetUint(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(raw, fv.Type().Bits())
		if err != nil {
			return fmt.Errorf("expected number, got %q", raw)
		}
		fv.SetFloat(f)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("expected boolean, got %q", raw)
		}
		fv.SetBool(b)
	default:
		return fmt.Errorf("unsupported field type %s — implement Binder for complex types", fv.Kind())
	}
	return nil
}

func wrapBindErr(field, source string, cause error) error {
	return apperr.BadRequest(
		localization.CodeInvalidRequest,
		fmt.Sprintf("invalid %s param %q: %s", source, field, cause.Error()),
	).Wrap(cause)
}
