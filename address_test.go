package dynamic53

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

type mockHttpClient struct {
	mockDo func(request *http.Request) (*http.Response, error)
}

func (m mockHttpClient) Do(request *http.Request) (*http.Response, error) {
	return m.mockDo(request)
}

func TestGetPublicIPv4(t *testing.T) {
	type testCase struct {
		name               string
		mockResponseStatus int
		mockResponseBody   string
		mockErrMsg         string
		expectIPv4Str      string
		expectErr          bool
	}

	cases := []testCase{
		{
			name:       "failed request",
			mockErrMsg: "test error",
			expectErr:  true,
		},
		{
			name:               "non-200 response code",
			mockResponseStatus: http.StatusForbidden,
			expectErr:          true,
		},
		{
			name:               "successful request, no address in body",
			mockResponseStatus: http.StatusOK,
			mockResponseBody:   "this ain't an IP address",
			expectErr:          true,
		},
		{
			name:               "successful request, IPv6 address in body",
			mockResponseStatus: http.StatusOK,
			mockResponseBody:   "::1",
			expectErr:          true,
		},
		{
			name:               "successful request, IPv4 address in body",
			mockResponseStatus: http.StatusOK,
			mockResponseBody:   "192.168.0.1",
			expectIPv4Str:      "192.168.0.1",
			expectErr:          false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(tt *testing.T) {
			assertion := assert.New(tt)

			type key string
			ctx := context.WithValue(context.Background(), key("foo"), "bar")

			client := AddressClient{
				Url: "http://foobar:123/test",
				httpClient: mockHttpClient{
					mockDo: func(request *http.Request) (*http.Response, error) {
						assertion.Nil(request.Context().Err(), "context should not be canceled")
						assertion.Equal("bar", request.Context().Value(key("foo")), "context should be the same as passed in")
						assertion.Equal("GET", request.Method)
						assertion.Equal("http://foobar:123/test", request.URL.String())

						if tc.mockErrMsg != "" {
							return nil, errors.New(tc.mockErrMsg)
						}

						response := &http.Response{
							StatusCode: tc.mockResponseStatus,
							Body:       io.NopCloser(bytes.NewBufferString(tc.mockResponseBody)),
						}
						return response, nil
					},
				},
			}
			ipv4, err := client.GetPublicIPv4(ctx)

			if tc.expectErr {
				assertion.Error(err)

				if tc.mockErrMsg != "" {
					assertion.ErrorContains(err, tc.mockErrMsg)
				}
				return
			}

			assertion.NoError(err)
			assertion.Equal(tc.expectIPv4Str, ipv4.String())
		})
	}
}
