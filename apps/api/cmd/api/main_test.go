package main

import "testing"

func TestLoopbackURL(t *testing.T) {
	tests := []struct {
		addr    string
		want    string
		wantErr bool
	}{
		{addr: ":8081", want: "http://127.0.0.1:8081/healthz"},
		{addr: "0.0.0.0:8081", want: "http://127.0.0.1:8081/healthz"},
		{addr: "[::]:8081", want: "http://127.0.0.1:8081/healthz"},
		{addr: "localhost:9000", want: "http://localhost:9000/healthz"},
		{addr: "8081", wantErr: true},
	}
	for _, tt := range tests {
		got, err := loopbackURL(tt.addr, "/healthz")
		if (err != nil) != tt.wantErr {
			t.Errorf("loopbackURL(%q) error = %v, wantErr %v", tt.addr, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("loopbackURL(%q) = %q, want %q", tt.addr, got, tt.want)
		}
	}
}
