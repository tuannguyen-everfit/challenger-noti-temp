// Package validate exposes a single shared *validator.Validate. Custom
// validators are registered once in init() so feature packages can add tags
// like `validate:"objectid"` without re-registering. This also makes the
// validator reflective-cache warm across requests.
package validate

import (
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"
)

var v = func() *validator.Validate {
	x := validator.New(validator.WithRequiredStructEnabled())

	// Surface JSON field names in validation errors (not Go field names) so
	// the central error mapper can echo them as `details[json_name]`.
	x.RegisterTagNameFunc(func(fld reflect.StructField) string {
		name := strings.SplitN(fld.Tag.Get("json"), ",", 2)[0]
		if name == "-" {
			return ""
		}
		return name
	})

	// register custom validators here — `objectid`, `e164phone`, etc.
	// (none yet — add when first feature needs them)

	return x
}()

// Struct validates the fields of v based on `validate` struct tags. Returns a
// `validator.ValidationErrors` slice on failure — the respond.MapError handler
// recognizes that type and produces a 400 with per-field details.
func Struct(s any) error { return v.Struct(s) }

// Var validates a single value against a tag (rarely used; prefer Struct).
func Var(field any, tag string) error { return v.Var(field, tag) }
