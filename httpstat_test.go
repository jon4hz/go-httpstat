package httpstat

import (
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"
)

const (
	TestDomainHTTP  = "http://example.com"
	TestDomainHTTPS = "https://example.com"
)

func DefaultTransport() *http.Transport {
	// It comes from std transport.go
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// To avoid shared transport
func DefaultClient() *http.Client {
	return &http.Client{
		Transport: DefaultTransport(),
	}
}

func NewRequest(t *testing.T, urlStr string, result *Result) *http.Request {
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		t.Fatal("NewRequest failed:", err)
	}

	ctx := WithHTTPStat(req.Context(), result)
	return req.WithContext(ctx)
}

func TestHTTPStat_HTTPS(t *testing.T) {
	var result Result
	req := NewRequest(t, TestDomainHTTPS, &result)

	client := DefaultClient()
	res, err := client.Do(req)
	if err != nil {
		t.Fatal("client.Do failed:", err)
	}

	if _, err := io.Copy(ioutil.Discard, res.Body); err != nil {
		t.Fatal("io.Copy failed:", err)
	}
	res.Body.Close()
	result.End(time.Now())

	if !result.isTLS {
		t.Fatal("isTLS should be true")
	}

	for k, d := range result.durations() {
		if d <= 0*time.Millisecond {
			t.Fatalf("expect %s to be non-zero", k)
		}
	}
}

func TestHTTPStat_HTTP(t *testing.T) {
	var result Result
	req := NewRequest(t, TestDomainHTTP, &result)

	client := DefaultClient()
	res, err := client.Do(req)
	if err != nil {
		t.Fatal("client.Do failed:", err)
	}

	if _, err := io.Copy(ioutil.Discard, res.Body); err != nil {
		t.Fatal("io.Copy failed:", err)
	}
	res.Body.Close()
	result.End(time.Now())

	if result.isTLS {
		t.Fatal("isTLS should be false")
	}

	if got, want := result.TLSHandshake, 0*time.Millisecond; got != want {
		t.Fatalf("TLSHandshake time of HTTP = %d, want %d", got, want)
	}

	// Except TLS should be non zero
	durations := result.durations()
	delete(durations, "TLSHandshake")

	for k, d := range durations {
		if d <= 0*time.Millisecond {
			t.Fatalf("expect %s to be non-zero", k)
		}
	}
}

func TestHTTPStat_KeepAlive(t *testing.T) {
	req1, err := http.NewRequest("GET", TestDomainHTTPS, nil)
	if err != nil {
		t.Fatal("NewRequest failed:", err)
	}

	client := DefaultClient()
	res1, err := client.Do(req1)
	if err != nil {
		t.Fatal("Request failed:", err)
	}

	if _, err := io.Copy(ioutil.Discard, res1.Body); err != nil {
		t.Fatal("Copy body failed:", err)
	}
	res1.Body.Close()

	var result Result
	req2 := NewRequest(t, TestDomainHTTPS, &result)

	// When second request, connection should be re-used.
	res2, err := client.Do(req2)
	if err != nil {
		t.Fatal("Request failed:", err)
	}

	if _, err := io.Copy(ioutil.Discard, res2.Body); err != nil {
		t.Fatal("Copy body failed:", err)
	}
	res2.Body.Close()
	result.End(time.Now())

	// The following values should be zero.
	// Because connection is reused.
	durations := []time.Duration{
		result.DNSLookup,
		result.TCPConnection,
		result.TLSHandshake,
	}

	for i, d := range durations {
		if got, want := d, 0*time.Millisecond; got != want {
			t.Fatalf("#%d expect %d to be eq %d", i, got, want)
		}
	}
}

func TestHTTPStat_beforeGO17(t *testing.T) {
	var result Result
	req := NewRequest(t, TestDomainHTTPS, &result)

	// Before go1.7, it uses non context based Dial function.
	// It doesn't support httptrace.
	oldTransport := &http.Transport{
		Dial: (&net.Dialer{
			Timeout: 5 * time.Second,
		}).Dial,
		TLSHandshakeTimeout: 5 * time.Second,
	}

	client := &http.Client{
		Timeout:   time.Second * 10,
		Transport: oldTransport,
	}

	res, err := client.Do(req)
	if err != nil {
		t.Fatal("client.Do failed:", err)
	}

	if _, err := io.Copy(ioutil.Discard, res.Body); err != nil {
		t.Fatal("io.Copy failed:", err)
	}
	res.Body.Close()
	result.End(time.Now())

	// The following values are not mesured.
	durations := []time.Duration{
		result.DNSLookup,
		result.TCPConnection,
		// result.TLSHandshake,
	}

	for i, d := range durations {
		if got, want := d, 0*time.Millisecond; got != want {
			t.Fatalf("#%d expect %d to be eq %d", i, got, want)
		}
	}
}

func TestTotal_Zero(t *testing.T) {
	result := &Result{}
	result.End(time.Now())

	zero := 0 * time.Millisecond
	if result.total != zero {
		t.Fatalf("Total time is %d, want %d", result.total, zero)
	}

	if result.contentTransfer != zero {
		t.Fatalf("Total time is %d, want %d", result.contentTransfer, zero)
	}
}

func TestConcurrent(t *testing.T) {
	urls := []string{"https://example.com", "https://duckduckgo.com"}
	var eg errgroup.Group
	for _, url := range urls {
		func(url string) {
			eg.Go(func() error {
				return getStat(url)
			})
		}(url)
	}
	if err := eg.Wait(); err != nil {
		t.Fatal(err)
	}
}

func getStat(url string) error {
	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		return err
	}
	var result Result
	ctx := WithHTTPStat(req.Context(), &result)
	req = req.WithContext(ctx) // Send request by default HTTP client
	client := new(http.Client)
	defer client.CloseIdleConnections()
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	result.End(time.Now())
	defer res.Body.Close()
	fmt.Println(result.total)
	return nil
}
