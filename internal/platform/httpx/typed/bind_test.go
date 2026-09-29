package typed

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// All tests exercise AutoBind through a real chi router so chi.URLParam
// resolves correctly — chi reads path params from its own context, which is
// only populated when the request flows through chi's matcher.

func TestAutoBind_ViaRouter(t *testing.T) {
	type binding struct {
		Key      string `path:"key"`
		Limit    int    `query:"limit"`
		Flag     bool   `query:"flag"`
		UA       string `header:"User-Agent"`
		Optional *int   `query:"opt"`
	}

	r := chi.NewRouter()
	var got binding
	r.Get("/things/{key}", func(_ http.ResponseWriter, req *http.Request) {
		got = binding{}
		if err := AutoBind(req, &got); err != nil {
			t.Errorf("AutoBind: %v", err)
		}
	})

	cases := []struct {
		name string
		path string
		hdr  string
		want binding
	}{
		{"path only", "/things/k1", "", binding{Key: "k1"}},
		{"path + query", "/things/k2?limit=42", "", binding{Key: "k2", Limit: 42}},
		{"bool query", "/things/k3?flag=true", "", binding{Key: "k3", Flag: true}},
		{"header", "/things/k4", "go-test/1.0", binding{Key: "k4", UA: "go-test/1.0"}},
		{"empty query → zero", "/things/k5?limit=", "", binding{Key: "k5"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, c.path, nil)
			if c.hdr != "" {
				req.Header.Set("User-Agent", c.hdr)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestAutoBind_OptionalPointer(t *testing.T) {
	type req struct {
		Opt *int `query:"opt"`
	}
	r := chi.NewRouter()
	var got req
	r.Get("/", func(_ http.ResponseWriter, hr *http.Request) {
		got = req{}
		_ = AutoBind(hr, &got)
	})

	// Absent → nil
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if got.Opt != nil {
		t.Errorf("absent param: Opt = %v, want nil", got.Opt)
	}

	// Present → non-nil
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/?opt=7", nil))
	if got.Opt == nil || *got.Opt != 7 {
		t.Errorf("present param: Opt = %v, want *7", got.Opt)
	}
}

func TestAutoBind_BadIntReturnsError(t *testing.T) {
	type req struct {
		N int `query:"n"`
	}
	r := chi.NewRouter()
	var seen error
	r.Get("/", func(_ http.ResponseWriter, hr *http.Request) {
		var v req
		seen = AutoBind(hr, &v)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/?n=not-a-number", nil))
	if seen == nil {
		t.Fatal("expected non-nil err for bad int")
	}
}

func TestAutoBind_NilOrNonStructPtrIsHandled(t *testing.T) {
	if err := AutoBind(httptest.NewRequest(http.MethodGet, "/", nil), nil); err == nil {
		t.Error("nil dst should error")
	}
	x := 42
	if err := AutoBind(httptest.NewRequest(http.MethodGet, "/", nil), &x); err != nil {
		t.Errorf("non-struct ptr should be no-op, got %v", err)
	}
}
