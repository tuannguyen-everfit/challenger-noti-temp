// Package ptr exposes generic pointer helpers for DTOs and tests where taking
// the address of a literal isn't possible inline.
//
//	UpdateReq{Name: ptr.Of("Alice")}        // would be ptr.Of() / ptr.Deref() — short.
//	current := ptr.Deref(profile.Birthdate) // returns zero time.Time if nil.
package ptr

// Of returns a pointer to v. Use in struct literals when the type wants `*T`
// but you have a value:
//
//	UpdateReq{Status: ptr.Of("ACTIVE"), Limit: ptr.Of(10)}
func Of[T any](v T) *T { return &v }

// Deref returns *p, or the zero value of T when p is nil. Use to safely read
// an optional pointer field without `if p != nil { ... }` guards:
//
//	name := ptr.Deref(req.Name)  // "" when req.Name == nil
func Deref[T any](p *T) T {
	if p == nil {
		var z T
		return z
	}
	return *p
}

// DerefOr returns *p, or fallback when p is nil. Use when the zero value
// isn't the right default:
//
//	limit := ptr.DerefOr(req.Limit, 20)
func DerefOr[T any](p *T, fallback T) T {
	if p == nil {
		return fallback
	}
	return *p
}

// OfNotZero returns a pointer to v, OR nil when v is the zero value of T.
// Useful for "encode field only when set" semantics on outgoing PATCH bodies.
// CAVEAT: this conflates "absent" and "explicitly zero" — do NOT use it on
// inputs where the client may legitimately send the zero value (e.g. false,
// 0, ""). Reach for `*T` directly there.
func OfNotZero[T comparable](v T) *T {
	var z T
	if v == z {
		return nil
	}
	return &v
}
