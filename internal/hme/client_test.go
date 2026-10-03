package hme

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	http "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
)

type blockingHTTPClient struct {
	tls_client.HttpClient
}

func (c *blockingHTTPClient) Do(req *http.Request) (*http.Response, error) {
	<-req.Context().Done()
	return nil, req.Context().Err()
}

type headersThenBlockingHTTPClient struct {
	tls_client.HttpClient
}

type staticHTTPClient struct {
	tls_client.HttpClient
	body string
}

func (c *staticHTTPClient) Do(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(c.body)),
	}, nil
}

type partialDeadlineBody struct {
	ctx  context.Context
	sent bool
}

func (b *partialDeadlineBody) Read(p []byte) (int, error) {
	if !b.sent {
		b.sent = true
		return copy(p, []byte(`{"partial":`)), nil
	}
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *partialDeadlineBody) Close() error { return nil }

func (c *headersThenBlockingHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Set-Cookie": []string{"refresh-token=updated; Path=/; HttpOnly"},
		},
		Body: &partialDeadlineBody{ctx: req.Context()},
	}, nil
}

func TestValidationURLs(t *testing.T) {
	tests := []struct {
		name string
		host string
		want []string
	}{
		{
			name: "全球账号只用全球端点",
			host: "icloud.com",
			want: []string{"https://setup.icloud.com/setup/ws/1/validate"},
		},
		{
			name: "国区账号优先全球端点并回退国区端点",
			host: "icloud.com.cn",
			want: []string{
				"https://setup.icloud.com/setup/ws/1/validate",
				"https://setup.icloud.com.cn/setup/ws/1/validate",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &Client{Host: tt.host}
			if got := client.validationURLs(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("validationURLs() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestRequestHonorsPerAttemptTimeout(t *testing.T) {
	client := &Client{
		Cookies:  map[string]string{},
		httpc:    &blockingHTTPClient{},
		clientID: "test-client",
	}
	timeout := 25 * time.Millisecond
	started := time.Now()
	_, err := client.request("GET", "https://example.test/slow", nil, timeout, 1)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("request() error = nil, want context deadline error")
	}
	if elapsed < timeout {
		t.Fatalf("request returned before timeout: %v < %v", elapsed, timeout)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("request ignored timeout: elapsed %v", elapsed)
	}
	if got := fmt.Sprint(err); got == "" {
		t.Fatal("request returned an empty error")
	}
}

func TestRequestReturnsErrorWhenResponseBodyReadHitsDeadline(t *testing.T) {
	client := &Client{
		Cookies:  map[string]string{},
		httpc:    &headersThenBlockingHTTPClient{},
		clientID: "test-client",
	}
	body, err := client.request("GET", "https://example.test/slow-body", nil, 25*time.Millisecond, 1)
	if err == nil {
		t.Fatal("request() error = nil after partial response body hit its deadline")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("request() error = %v, want context deadline exceeded", err)
	}
	if body != "" {
		t.Fatalf("request() body = %q, want empty body on read failure", body)
	}
	if got := client.Cookies["refresh-token"]; got != "updated" {
		t.Fatalf("refreshed cookie = %q, want %q", got, "updated")
	}
}

func TestRequestOrigin(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://setup.icloud.com/setup/ws/1/validate", "https://www.icloud.com"},
		{"https://setup.icloud.com.cn/setup/ws/1/validate", "https://www.icloud.com.cn"},
		{"https://p123-maildomainws.icloud.com.cn/v2/hme/list", "https://www.icloud.com.cn"},
		{"https://p123-maildomainws.icloud.com/v2/hme/list", "https://www.icloud.com"},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			if got := requestOrigin(tt.url); got != tt.want {
				t.Fatalf("requestOrigin(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestParseAliasListStrictAuthorizationSource(t *testing.T) {
	t.Run("deleted excluded and inactive retained", func(t *testing.T) {
		body := `{
			"success": true,
			"result": {
				"hmeEmails": [
					{"hme":"active@icloud.com","anonymousId":"active","label":"Active","isActive":true},
					{"hme":"inactive@icloud.com","anonymousId":"inactive","label":"Inactive","isActive":false},
					{"hme":"state-inactive@icloud.com","anonymousId":"state-inactive","state":"inactive"},
					{"hme":"deleted@icloud.com","anonymousId":"deleted","status":"deleted","isActive":false}
				]
			}
		}`
		aliases, err := parseAliasList(body)
		if err != nil {
			t.Fatalf("parseAliasList() error = %v", err)
		}
		if len(aliases) != 3 {
			t.Fatalf("aliases = %#v, want three non-deleted entries", aliases)
		}
		got := make(map[string]bool, len(aliases))
		for _, alias := range aliases {
			got[alias.Email] = alias.Active
		}
		want := map[string]bool{
			"active@icloud.com":         true,
			"inactive@icloud.com":       false,
			"state-inactive@icloud.com": false,
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("alias states = %#v, want %#v", got, want)
		}
	})

	t.Run("real empty list succeeds", func(t *testing.T) {
		aliases, err := parseAliasList(`{"success":true,"result":{"hmeEmails":[]}}`)
		if err != nil {
			t.Fatalf("parseAliasList() error = %v", err)
		}
		if aliases == nil || len(aliases) != 0 {
			t.Fatalf("aliases = %#v, want non-nil empty list", aliases)
		}
	})

	for _, tc := range []struct {
		name string
		body string
	}{
		{
			name: "business failure",
			body: `{"success":false,"error":{"errorMessage":"session expired"},"result":{"hmeEmails":[]}}`,
		},
		{
			name: "missing success",
			body: `{"result":{"hmeEmails":[]}}`,
		},
		{
			name: "unrecognized structure",
			body: `{"success":true,"result":{"aliases":[]}}`,
		},
		{
			name: "unrelated object array",
			body: `{"success":true,"result":{"accounts":[{"email":"victim@icloud.com"}]}}`,
		},
		{
			name: "generic email field inside named array",
			body: `{"success":true,"result":{"hmeEmails":[{"email":"victim@icloud.com","isActive":true}]}}`,
		},
		{
			name: "invalid json",
			body: `{"success":`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aliases, err := parseAliasList(tc.body)
			if err == nil {
				t.Fatalf("parseAliasList() = %#v, nil error; want fail-closed error", aliases)
			}
			if aliases != nil {
				t.Fatalf("aliases = %#v, want nil on malformed response", aliases)
			}
		})
	}
}

func TestListAliasesPropagatesStrictParseError(t *testing.T) {
	client := &Client{
		Cookies:    map[string]string{},
		httpc:      &staticHTTPClient{body: `{"success":true,"result":{"accounts":[{"email":"victim@icloud.com"}]}}`},
		serviceURL: "https://p123-maildomainws.icloud.com",
	}
	aliases, err := client.ListAliases()
	if err == nil {
		t.Fatalf("ListAliases() = %#v, nil error; want strict parse error", aliases)
	}
	if aliases != nil {
		t.Fatalf("aliases = %#v, want nil after parse failure", aliases)
	}
}
