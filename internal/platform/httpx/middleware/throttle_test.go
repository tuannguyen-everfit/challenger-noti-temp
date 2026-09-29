package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestThrottle_Disabled_WhenMaxZero(t *testing.T) {
	calls := 0
	h := Throttle(0, 0, time.Second)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))

	for range 5 {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("got %d, want 200", w.Code)
		}
	}
	if calls != 5 {
		t.Errorf("calls = %d, want 5 (disabled throttle = pass-through)", calls)
	}
}

func TestThrottle_AllowsUpToMax_Concurrently(t *testing.T) {
	const maxInFlight = 3
	release := make(chan struct{})
	var inFlight atomic.Int32
	var peak atomic.Int32

	h := Throttle(maxInFlight, 10, 5*time.Second)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cur := inFlight.Add(1)
		for {
			if p := peak.Load(); cur > p {
				if peak.CompareAndSwap(p, cur) {
					break
				}
				continue
			}
			break
		}
		<-release
		inFlight.Add(-1)
		w.WriteHeader(http.StatusOK)
	}))

	var wg sync.WaitGroup
	for range maxInFlight {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
			if w.Code != http.StatusOK {
				t.Errorf("got %d, want 200", w.Code)
			}
		}()
	}

	// Give all maxInFlight goroutines time to enter the handler.
	deadline := time.After(2 * time.Second)
	for peak.Load() < int32(maxInFlight) {
		select {
		case <-deadline:
			t.Fatalf("only %d reached the handler concurrently, want %d", peak.Load(), maxInFlight)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	close(release)
	wg.Wait()
}

func TestThrottle_RejectsImmediately_WhenBacklogFull(t *testing.T) {
	const maxInFlight = 1
	const backlog = 1
	release := make(chan struct{})
	entered := make(chan struct{}, maxInFlight)

	h := Throttle(maxInFlight, backlog, 5*time.Second)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	}))

	// Fill the in-flight slot.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}()
	<-entered

	// Fill the backlog slot — this one will wait for the in-flight slot, not reject.
	backlogStarted := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		close(backlogStarted)
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}()
	<-backlogStarted
	time.Sleep(50 * time.Millisecond) // let it queue

	// Third request — queue full, must reject immediately.
	w := httptest.NewRecorder()
	start := time.Now()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if took := time.Since(start); took > 100*time.Millisecond {
		t.Errorf("rejection took %v, expected immediate", took)
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got == "" {
		t.Error("missing Retry-After header on busy rejection")
	} else if n, _ := strconv.Atoi(got); n < 1 {
		t.Errorf("Retry-After = %q, want >= 1", got)
	}
	assertBusyEnvelope(t, w)

	close(release)
	wg.Wait()
}

func TestThrottle_TimesOutInBacklog(t *testing.T) {
	const backlogTimeout = 50 * time.Millisecond
	release := make(chan struct{})
	entered := make(chan struct{}, 1)

	h := Throttle(1, 10, backlogTimeout)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	}))

	// Hog the only slot.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}()
	<-entered

	// Second request waits in backlog and should time out around backlogTimeout.
	w := httptest.NewRecorder()
	start := time.Now()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	took := time.Since(start)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503", w.Code)
	}
	if took < backlogTimeout {
		t.Errorf("returned before timeout: %v < %v", took, backlogTimeout)
	}
	if took > 5*backlogTimeout {
		t.Errorf("returned much later than timeout: %v ≫ %v", took, backlogTimeout)
	}
	assertBusyEnvelope(t, w)

	close(release)
	wg.Wait()
}

func TestThrottle_ReleasesOnClientCancel(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)

	h := Throttle(1, 10, 5*time.Second)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	}))

	// Hog the slot.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}()
	<-entered

	// Client gives up while waiting in backlog.
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)

	done := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		done <- w.Code
	}()
	time.Sleep(20 * time.Millisecond) // let it enter backlog
	cancel()

	select {
	case code := <-done:
		// 499 is the nginx convention for client-closed-request; respond.MapError
		// maps context.Canceled to 499 in this codebase.
		if code != 499 {
			t.Errorf("got %d on client cancel, want 499", code)
		}
	case <-time.After(time.Second):
		t.Fatal("handler did not return after client cancel")
	}

	close(release)
	wg.Wait()
}

func assertBusyEnvelope(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	var body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %q (err: %v)", w.Body.String(), err)
	}
	if body.Code != "SERVICE_BUSY" {
		t.Errorf("code = %q, want SERVICE_BUSY", body.Code)
	}
	if body.Message == "" {
		t.Error("empty message in busy envelope")
	}
}
