// Copyright (c) 2026 Multus Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import "testing"

func TestCleanAbsolutePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    string
		want    string
		wantErr bool
	}{
		{
			name: "cleans absolute path",
			path: "/etc/cni/net.d/./multus.d/daemon-config.json",
			want: "/etc/cni/net.d/multus.d/daemon-config.json",
		},
		{
			name:    "rejects empty path",
			path:    "",
			wantErr: true,
		},
		{
			name:    "rejects relative path",
			path:    "etc/cni/net.d/multus.d/daemon-config.json",
			wantErr: true,
		},
		{
			name:    "rejects parent directory reference",
			path:    "/etc/cni/net.d/../shadow",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := cleanAbsolutePath(tt.path)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.String() != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
			if got.dir != "/etc/cni/net.d/multus.d" {
				t.Fatalf("expected root directory %q, got %q", "/etc/cni/net.d/multus.d", got.dir)
			}
			if got.name != "daemon-config.json" {
				t.Fatalf("expected local file name %q, got %q", "daemon-config.json", got.name)
			}
		})
	}
}
