package demo

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestCheckoutTraversesServiceGraph(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	paymentsHandler, err := Handler(Config{Role: "payments-api"}, logger, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		paymentsHandler.ServeHTTP(response, request)
		return response.Result(), nil
	})}
	ordersHandler, err := Handler(Config{Role: "orders-api", Upstream: "http://payments"}, logger, client)
	if err != nil {
		t.Fatal(err)
	}
	client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		if request.URL.Host == "orders" {
			ordersHandler.ServeHTTP(response, request)
		} else {
			paymentsHandler.ServeHTTP(response, request)
		}
		return response.Result(), nil
	})
	frontend, err := Handler(Config{Role: "frontend", Upstream: "http://orders"}, logger, client)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	frontend.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/checkout", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("checkout returned %d: %s", response.Code, response.Body.String())
	}
}

func TestCheckoutFailsClosedOnUpstreamError(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		http.Error(response, "failed", http.StatusServiceUnavailable)
		return response.Result(), nil
	})}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	handler, err := Handler(Config{Role: "frontend", Upstream: "http://orders"}, logger, client)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/checkout", nil))
	if response.Code != http.StatusBadGateway {
		t.Fatalf("expected bad gateway, got %d", response.Code)
	}
}

func TestHandlerRejectsUnsafeWorkingSet(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	for _, test := range []Config{
		{Role: "payments-api", PaymentWorkingSetMiB: -1},
		{Role: "payments-api", PaymentWorkingSetMiB: 97},
		{Role: "frontend", Upstream: "http://orders", PaymentWorkingSetMiB: 8},
		{Role: "orders-api", Upstream: "http://payments", OrderMode: "invalid"},
		{Role: "orders-api", Upstream: "http://payments", OrderCPUBurn: 1600 * time.Millisecond},
		{Role: "frontend", Upstream: "http://orders", OrderCPUBurn: time.Second},
		{Role: "payments-api", PaymentMode: "unknown"},
		{Role: "orders-api", Upstream: "http://payments", PaymentMode: "fail"},
	} {
		if _, err := Handler(test, logger, nil); err == nil {
			t.Fatalf("expected invalid config %+v to fail", test)
		}
	}
}

func TestPaymentFailureIsVisibleAtDownstream(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	payments, err := Handler(Config{Role: "payments-api", PaymentMode: "fail"}, logger, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	payments.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/charge", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected payment failure, got %d", response.Code)
	}
}

func TestOrderCPUBurnAddsLatency(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		response.WriteHeader(http.StatusOK)
		return response.Result(), nil
	})}
	handler, err := Handler(Config{Role: "orders-api", Upstream: "http://payments", OrderCPUBurn: 30 * time.Millisecond}, logger, client)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/order", nil))
	if response.Code != http.StatusOK || time.Since(started) < 25*time.Millisecond {
		t.Fatalf("expected successful delayed order, got status %d after %s", response.Code, time.Since(started))
	}
}
