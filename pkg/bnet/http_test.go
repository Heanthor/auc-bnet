package bnet

import "testing"

func Test_realBnetHTTP_subRegion(t *testing.T) {

	tests := []struct {
		name    string
		path    string
		region  string
		baseUrl string
		want    string
	}{
		{
			name:    "path with leading slash",
			path:    "/abc/123",
			region:  "us",
			baseUrl: "https://{region}.unittest.com",
			want:    "https://us.unittest.com/abc/123",
		},
		{
			name:    "path without leading slash",
			path:    "abc/123",
			region:  "us",
			baseUrl: "https://{region}.unittest.com",
			want:    "https://us.unittest.com/abc/123",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			if got := subRegion(tt.baseUrl, tt.path, tt.region); got != tt.want {
				t.Errorf("subRegion() = %v, want %v", got, tt.want)
			}
		})
	}
}
