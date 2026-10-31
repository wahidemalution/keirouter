package costlookup

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// realLogPage is the consume log entry verified against the Subrouter
// dashboard on 2026-10-31 (quota 17 = ¥0.000231), trimmed of unused fields.
const realLogPage = `{"data":{"has_more":true,"items":[{"id":634,"user_id":19579,"created_at":1793458259,"type":2,
"token_name":"Stable API 05","model_name":"deepseek-v4.1-flash","quota":17,"prompt_tokens":872,"completion_tokens":515,
"is_stream":true,"group":"provider:manxueai","request_id":"20261031145028472218319QeMWIGBK",
"upstream_request_id":"202610311450287157668108268d9d6q6FkBvlo","other":"{}"}],
"next_cursor":"ThP9OzrLWOBZMk","page_size":100},"message":"","success":true}`

func TestLogsSendsHeadersAndQuery(t *testing.T) {
	for _, tokenName := range []string{"", "Stable API 05"} {
		t.Run("token name "+tokenName, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/api/log/self", r.URL.Path)
				require.Equal(t, "Bearer sys-token", r.Header.Get("Authorization"))
				require.Equal(t, "19579", r.Header.Get("New-Api-User"))
				q := r.URL.Query()
				require.Equal(t, "2", q.Get("type"))
				require.Equal(t, "100", q.Get("page_size"))
				require.Equal(t, "first", q.Get("cursor"))
				require.Equal(t, "1793458200", q.Get("start_timestamp"))
				require.Equal(t, "1793458300", q.Get("end_timestamp"))
				_, has := q["token_name"]
				require.Equal(t, tokenName != "", has)
				require.Equal(t, tokenName, q.Get("token_name"))
				_, _ = w.Write([]byte(realLogPage))
			}))
			defer srv.Close()
			c := &Client{BaseURL: srv.URL, Token: "sys-token", UserID: "19579", TokenName: tokenName, HTTP: srv.Client()}

			page, err := c.Logs(context.Background(), LogQuery{Cursor: "first", Start: 1793458200, End: 1793458300, PageSize: 100})
			require.NoError(t, err)
			require.True(t, page.HasMore)
			require.Equal(t, "ThP9OzrLWOBZMk", page.NextCursor)
			require.Equal(t, []LogItem{{CreatedAt: 1793458259, IsStream: true, Quota: 17, PromptTokens: 872,
				CompletionTokens: 515, ModelName: "deepseek-v4.1-flash", RequestID: "20261031145028472218319QeMWIGBK"}}, page.Items)
		})
	}
}

func TestLogsErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"401", 401, `{"success":false}`, ErrUnauthorized},
		{"403", 403, `{"success":false}`, ErrUnauthorized},
		{"429", 429, `{}`, ErrTransient},
		{"500", 500, `oops`, ErrTransient},
		{"bad json", 200, `<html>`, ErrTransient},
		{"success false", 200, `{"success":false,"message":"x"}`, ErrTransient},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := &Client{BaseURL: srv.URL, Token: "t", UserID: "1", HTTP: srv.Client()}
			_, err := c.Logs(context.Background(), LogQuery{Cursor: "first", PageSize: 100})
			require.Error(t, err)
			require.True(t, errors.Is(err, tc.want), "got %v", err)
		})
	}
}

func TestLogsDefaultsBaseURL(t *testing.T) {
	c := &Client{}
	require.Equal(t, "https://subrouter.ai/api/log/self", c.logsURL())
	c.BaseURL = "https://example.test/"
	require.Equal(t, "https://example.test/api/log/self", c.logsURL())
}
