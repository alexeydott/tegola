package env_test

import (
	"errors"
	"net/url"
	"testing"

	"github.com/go-test/deep"

	"github.com/go-spatial/tegola/internal/env"
)

func TestParseURL(t *testing.T) {
	type tcase struct {
		in          any
		expected    *url.URL
		expectedErr error
	}

	fn := func(tc tcase) func(*testing.T) {
		return func(t *testing.T) {
			got, err := env.ParseURL(tc.in)
			if tc.expectedErr != nil {
				if err == nil {
					t.Errorf("expected err %v, got nil", tc.expectedErr.Error())
					return
				}

				// compare error messages
				if errors.Is(tc.expectedErr, err) {
					t.Errorf("invalid error. expected %v, got %v", tc.expectedErr, err)
					return
				}

				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %s", err)
			}

			if diff := deep.Equal(got, tc.expected); diff != nil {
				t.Fatalf("expected does not match go: %v", diff)
			}
		}
	}

	tests := map[string]tcase{
		"happy path": {
			in: "https://go-spatial.org/tegola",
			expected: &url.URL{
				Scheme: "https",
				Host:   "go-spatial.org",
				Path:   "/tegola",
			},
		},
		"invalid url escape": {
			in:          "https://go-spatial.org/tegola/_20_%+off_60000_",
			expectedErr: url.EscapeError(""),
		},
		"nil": {
			in:       nil,
			expected: nil,
		},
	}

	for name, tc := range tests {
		t.Run(name, fn(tc))
	}
}

// A malformed webserver.hostname must be rejected at parse time rather than
// silently dropped. In particular "cdn.example.com:443" used to be soft-parsed
// as scheme=cdn.example.com, opaque=443 (Host empty) and ignored.
func TestParseURLHostName(t *testing.T) {
	type tcase struct {
		in       any
		expected *url.URL // consulted only when wantErr is false
		wantErr  bool
	}

	tests := map[string]tcase{
		"host with port": {
			in:       "cdn.example.com:443",
			expected: &url.URL{Host: "cdn.example.com:443"},
		},
		"bare host": {
			in:       "example.com",
			expected: &url.URL{Host: "example.com"},
		},
		"url with scheme": {
			in:       "https://example.com",
			expected: &url.URL{Scheme: "https", Host: "example.com"},
		},
		"garbage path in host": {
			in:      "cdn.example.com:443/extra/path",
			wantErr: true,
		},
		"garbage scheme without host": {
			in:      "http://",
			wantErr: true,
		},
		"garbage whitespace": {
			in:      "not a host",
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := env.ParseURL(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got nil (parsed %q)", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.in, err)
			}
			if diff := deep.Equal(got, tc.expected); diff != nil {
				t.Errorf("result mismatch for %q: %v", tc.in, diff)
			}
		})
	}
}
